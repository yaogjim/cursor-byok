// subagent_idempotent.go 实现 Task exec 的 durable handoff 与幂等 parent tool-result 提交。
//
// 事务顺序（crash-safe）：
//  1. PrepareTerminal     → _subagents/<run_id>/result.json 持久化（durable prepare）
//  2. AppendEntriesToExistingWithUpdate → parent conversation lock 内幂等追加 tool_result + metadata
//  3. MarkParentCommitted → 更新 run 状态为 parent_committed
//
// 崩溃重试安全：
//   - parent_commit_key 作为 HistoryEntry.IdempotencyKey 写入 conversation；
//     重试时 update callback 发现已有该键，返回 errSubagentAlreadyCommitted。
//   - MarkParentCommitted 失败只记录日志，history 已落盘则不会回滚。
package forwarder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

// errSubagentAlreadyCommitted 是 AppendEntriesWithUpdate callback 中发现
// parent_commit_key 已存在时返回的哨兵值；调用者将其视为幂等成功。
var errSubagentAlreadyCommitted = errors.New("subagent_already_committed")

// errSubagentTerminalConflict 是 PrepareTerminal 发现已有不同 commit key 的终态记录时
// 返回的哨兵值（first-write-wins 策略）；调用者将其视为本次结果被丢弃。
var errSubagentTerminalConflict = errors.New("subagent_terminal_conflict")

// appendSubagentToolResultIdempotent 实现 Task exec 的 durable handoff：
//  1. 快速路径：run 已是 parent_committed/acknowledged → 直接返回（幂等）
//  2. PrepareTerminal（持久化 result.json）
//  3. AppendEntriesWithUpdate：conversation file lock 内检查 parent_commit_key 幂等性
//  4. MarkParentCommitted
//
// 若 subagentRuns 为 nil 或 pending.SubagentRunID 为空，降级为普通 appendToolResult。
// appendSubagentToolResultIdempotent 实现 Task exec 的 durable handoff。
// resultSummary 是可选的安全非敏感诊断摘要（最多 subagentResultSummaryLimit 字节），
// 用于错误分类时的观测；不得包含 prompt/result 正文。
func (service *Service) appendSubagentToolResultIdempotent(
	stream *ActiveStream,
	pending runtimecore.PendingExec,
	toolCallID string,
	resultPayload string,
	toolCallEncoded []byte,
	category SubagentTerminalCategory,
	resultSummary ...string, // 可选安全诊断摘要
) error {
	if service.subagentRuns == nil || strings.TrimSpace(pending.SubagentRunID) == "" {
		return service.appendToolResult(
			stream, toolCallID, deriveToolNameFromPendingExec(pending),
			pending.ArgsJSON, resultPayload, pending.ReasoningContent, nil, pending.ModelCallID,
		)
	}
	runID := strings.TrimSpace(pending.SubagentRunID)

	// 快速路径：已 parent_committed / acknowledged → 幂等返回。
	record, err := service.subagentRuns.LoadRun(runID)
	if err != nil {
		return fmt.Errorf("subagent load_run for handoff: %w", err)
	}
	if record != nil && isTerminalRunStatus(record.Status) {
		return nil
	}

	// 建立 parent_commit_key（稳定、内容可寻址）。
	resultDigest := computeSubagentStringDigest(resultPayload)
	parentCommitKey := computeSubagentParentCommitKey(runID, toolCallID, resultDigest)

	// 确定 CAS 版本。
	var prevVersion int64 = 1
	if record != nil {
		prevVersion = record.Version
	}

	// 截断 payload（隐私 + 存储上限）。
	storedPayload := resultPayload
	if len(storedPayload) > subagentResultPayloadLimit {
		storedPayload = storedPayload[:subagentResultPayloadLimit]
	}

	// online prepare 写入实际 tool 名称，恢复时使用，旧空值默认 "Task"。
	toolName := deriveToolNameFromPendingExec(pending)
	// commitCategory/commitToolCallEncoded 可能在冲突时被替换为胜出信封的值。
	commitCategory := category
	commitToolCallEncoded := toolCallEncoded
	commitToolName := toolName
	commitArgsJSON := append([]byte(nil), pending.ArgsJSON...)

	// ── Step 1: terminal_prepared 重入：在 Prepare 前加载现有 result 并复用 ──────
	// 若记录已是 terminal_prepared，直接从 result.json 取胜出信封，跳过 PrepareTerminal。
	if record != nil && record.Status == SubagentRunTerminalPrepared {
		existingEnv, loadErr := service.subagentRuns.LoadResult(runID)
		if loadErr == nil && existingEnv != nil {
			parentCommitKey = existingEnv.ParentCommitKey
			storedPayload = existingEnv.ToolResultPayload
			commitToolCallEncoded = existingEnv.ToolCallEncoded
			commitCategory = existingEnv.TerminalCategory
			commitToolName = firstNonEmpty(strings.TrimSpace(existingEnv.ToolName), "Task")
			if len(existingEnv.ArgsJSON) > 0 {
				commitArgsJSON = append([]byte(nil), existingEnv.ArgsJSON...)
			}
			goto commitStep
		}
	}

	// ── Step 1: Durable prepare ──────────────────────────────────────────────
	{
		// 提取可选诊断摘要（不含 prompt/result 正文）。
		var summary string
		if len(resultSummary) > 0 {
			summary = resultSummary[0]
		}
		if len(summary) > subagentResultSummaryLimit {
			summary = summary[:subagentResultSummaryLimit]
		}
		now := time.Now().UTC()
		envelope := &SubagentResultEnvelope{
			SchemaVersion:     subagentContractSchemaVersion,
			SubagentRunID:     runID,
			TerminalCategory:  category,
			TerminalAt:        now,
			ResultDigest:      resultDigest,
			ResultSummary:     summary,
			ToolResultPayload: storedPayload,
			ToolCallEncoded:   toolCallEncoded,
			ToolName:          toolName,
			ArgsJSON:          append([]byte(nil), pending.ArgsJSON...),
			ParentCommitKey:   parentCommitKey,
		}
		if _, prepErr := service.subagentRuns.PrepareTerminal(runID, prevVersion, envelope); prepErr != nil {
			if !errors.Is(prepErr, errSubagentTerminalConflict) {
				return fmt.Errorf("subagent prepare_terminal: %w", prepErr)
			}
			// First-terminal-wins：另一路径已写入终态，加载已有信封继续 parent commit。
			// 冲突日志只记录受控 ID/category/digest，不含 payload 正文。
			log.Printf("subagent_idempotent terminal_conflict_reuse run_id=%s existing_category=%s new_category=%s digest=%.12s",
				runID, record.TerminalCategory, category, resultDigest)
			existing, loadErr := service.subagentRuns.LoadResult(runID)
			if loadErr != nil {
				return fmt.Errorf("subagent load_result_after_conflict run=%s: %w", runID, loadErr)
			}
			if existing == nil {
				// 崩溃窗口极短：conflict sentinel 但 result.json 尚未写入，视为已处理。
				return nil
			}
			// 提交时全部字段来自胜出信封（category/toolName/payload/toolCall）。
			parentCommitKey = existing.ParentCommitKey
			storedPayload = existing.ToolResultPayload
			commitToolCallEncoded = existing.ToolCallEncoded
			commitCategory = existing.TerminalCategory
			commitToolName = firstNonEmpty(strings.TrimSpace(existing.ToolName), "Task")
			if len(existing.ArgsJSON) > 0 {
				commitArgsJSON = append([]byte(nil), existing.ArgsJSON...)
			}
		}
	}

commitStep:
	// ── Step 2: Idempotent AppendEntriesWithUpdate ───────────────────────────
	// tool_result entry 带 IdempotencyKey = parentCommitKey。
	// 所有字段（category/toolName/payload/toolCall）来自胜出信封。
	trEntry := withHistoryModelCallID(newToolResultEntry(
		stream.TurnSeq, stream.RequestID, toolCallID, commitToolName,
		string(commitArgsJSON), storedPayload, pending.ReasoningContent,
		json.RawMessage(commitToolCallEncoded),
	), pending.ModelCallID)
	trEntry.IdempotencyKey = parentCommitKey

	// metadata entry 记录 subagent handoff 信息（供观测，不含 prompt/result 正文）。
	// terminal_category 使用胜出信封的值（commitCategory），与 entry 保持一致。
	metaFields := map[string]any{
		"subagent_run_id":   runID,
		"terminal_category": string(commitCategory),
		"parent_commit_key": parentCommitKey,
		"tool_call_id":      toolCallID,
	}
	if record != nil {
		metaFields["root_conversation_id"] = record.Identity.RootConversationID
		metaFields["parent_conversation_id"] = record.Identity.ParentConversationID
		metaFields["parent_model_call_id"] = record.Identity.ParentModelCallID
		metaFields["parent_tool_call_id"] = record.Identity.ParentToolCallID
		metaFields["child_conversation_id"] = record.Identity.ChildConversationID
		metaFields["agent_id"] = record.Identity.AgentID
	}
	metaEntry := newMetadataEntry(stream.TurnSeq, stream.RequestID, "subagent_handoff", metaFields)
	entries := []HistoryEntry{trEntry, metaEntry}
	if evidenceEntry, ok := newSubagentExecutionEvidenceEntry(
		stream.TurnSeq, stream.RequestID, pending.ModelCallID, toolCallID, commitToolName,
		commitArgsJSON, commitToolCallEncoded, commitCategory, 0,
	); ok {
		entries = append(entries, withHistoryModelCallID(evidenceEntry, pending.ModelCallID))
	}

	conversationID := stream.ConversationID
	committed := false

	stream.mu.Lock()
	if stream.CheckpointConversation == nil {
		stream.mu.Unlock()
		return fmt.Errorf("checkpoint conversation not initialized for subagent handoff")
	}

	if service.store != nil {
		persisted, _, appendErr := service.store.AppendEntriesToExistingWithUpdate(
			conversationID,
			resetEntrySequences(entries),
			func(conv *ConversationFile) error {
				// entries 已追加到末尾；检查其前的已有条目是否含相同幂等键。
				priorCount := len(conv.Entries) - len(entries)
				if priorCount < 0 {
					priorCount = 0
				}
				for i := 0; i < priorCount; i++ {
					if conv.Entries[i].IdempotencyKey == parentCommitKey {
						return errSubagentAlreadyCommitted
					}
				}
				return nil
			},
		)
		if appendErr != nil {
			stream.mu.Unlock()
			if errors.Is(appendErr, errSubagentAlreadyCommitted) {
				committed = true
			} else if errors.Is(appendErr, errConversationNotFound) {
				if updateErr := service.markSubagentAwaitingParentResume(runID); updateErr != nil {
					return fmt.Errorf("subagent parent missing and awaiting_parent_resume update failed: %w", updateErr)
				}
				return fmt.Errorf("subagent parent conversation missing: %w", appendErr)
			} else {
				return fmt.Errorf("subagent append_entries: %w", appendErr)
			}
		} else {
			if persisted != nil {
				stream.CheckpointConversation = persisted
			}
			rebuildStreamExecutionEvidenceLocked(stream)
			stream.UpdatedAt = time.Now().UTC()
			stream.mu.Unlock()
			committed = true
		}
	} else {
		// 纯内存路径（测试）。
		appendEntriesInPlace(stream.CheckpointConversation, entries)
		rebuildStreamExecutionEvidenceLocked(stream)
		stream.UpdatedAt = time.Now().UTC()
		stream.mu.Unlock()
		committed = true
	}

	if !committed {
		return nil
	}

	// ── Step 3: MarkParentCommitted ─────────────────────────────────────────
	if _, markErr := service.subagentRuns.MarkParentCommitted(runID); markErr != nil {
		// history 已落盘，此处只记录日志，不回滚。
		log.Printf("subagent_idempotent mark_parent_committed_failed run_id=%s err=%v", runID, markErr)
	}
	return nil
}

// ──────────────────────────────────────────────────────────────────────────────
// 辅助函数
// ──────────────────────────────────────────────────────────────────────────────

// computeSubagentStringDigest 返回字符串的 sha256 十六进制摘要。
func computeSubagentStringDigest(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// computeSubagentParentCommitKey 返回 (runID + toolCallID + resultDigest) 的
// sha256 前 16 字节（32 hex 字符），用作 parent history 追加的幂等键。
func computeSubagentParentCommitKey(runID, toolCallID, resultDigest string) string {
	raw := fmt.Sprintf("%s:%s:%s", runID, toolCallID, resultDigest)
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:16])
}

// categorizeSubagentResultMsg 根据 ExecClientMessage 中的 SubagentResult 一键字段
// 返回对应的 SubagentTerminalCategory。
// nil 消息或缺失 SubagentResult 均映射为 SubagentTerminalProtocolError。
//
// 分类规则（仅现有 typed proto 信号）：
//   - ForceBackgroundSubagentResult → canceled
//   - SubagentResult_Success + BackgroundReason 非 unspecified → canceled（子代理被后台化）
//   - SubagentResult_Success + BackgroundReason unspecified → succeeded
//   - SubagentResult_Error → protocol_error（无 typed 来源，不猜测 provider_error）
//   - 其他/nil → protocol_error
func categorizeSubagentResultMsg(msg *agentv1.ExecClientMessage) SubagentTerminalCategory {
	if msg == nil {
		return SubagentTerminalProtocolError
	}
	// ForceBackground：父侧或用户请求后台化 → 视为取消。
	if msg.GetForceBackgroundSubagentResult() != nil {
		return SubagentTerminalCanceled
	}
	sr := msg.GetSubagentResult()
	if sr == nil {
		return SubagentTerminalProtocolError
	}
	switch r := sr.GetResult().(type) {
	case *agentv1.SubagentResult_Success:
		// BackgroundReason 非 unspecified → 子代理被后台化/取消（未真正完成）。
		if r.Success != nil && r.Success.GetBackgroundReason() != agentv1.SubagentBackgroundReason_SUBAGENT_BACKGROUND_REASON_UNSPECIFIED {
			return SubagentTerminalCanceled
		}
		return SubagentTerminalSucceeded
	case *agentv1.SubagentResult_Error:
		// SubagentError 只携带 agent_id 和 error 字符串，无 typed 来源；
		// 无法区分 provider_error 与其他错误，统一返回 protocol_error。
		// Throw 路径（exec-bridge）由调用方显式传入 SubagentTerminalToolError，不经过此函数。
		return SubagentTerminalProtocolError
	default:
		return SubagentTerminalProtocolError
	}
}

// parseArgsJSONSubagentType 从 argsJSON 字节中提取 subagent_type 字段。
// 缺失或解析失败时返回空字符串（保持 unknown）。
func parseArgsJSONSubagentType(argsJSON []byte) string {
	if len(argsJSON) == 0 {
		return ""
	}
	var args map[string]any
	if err := json.Unmarshal(argsJSON, &args); err != nil {
		return ""
	}
	return readStringMapValue(args, "subagent_type", "subagentType")
}

func extractSubagentAgentID(msg *agentv1.ExecClientMessage) string {
	if msg == nil {
		return ""
	}
	result := msg.GetSubagentResult()
	if result == nil {
		return ""
	}
	switch typed := result.GetResult().(type) {
	case *agentv1.SubagentResult_Success:
		if typed.Success != nil {
			return strings.TrimSpace(typed.Success.GetAgentId())
		}
	case *agentv1.SubagentResult_Error:
		if typed.Error != nil {
			return strings.TrimSpace(typed.Error.GetAgentId())
		}
	}
	return ""
}

// extractSubagentErrorSummary 为错误终态生成受控诊断摘要。
// SubagentError.Error 是客户端提供的自由文本，可能包含 provider 响应、prompt、
// result 或凭证，因此不得复制到 result.json、观测日志或其他持久化元数据。
func extractSubagentErrorSummary(msg *agentv1.ExecClientMessage) string {
	if msg == nil {
		return ""
	}
	sr := msg.GetSubagentResult()
	if sr == nil {
		return ""
	}
	if _, ok := sr.GetResult().(*agentv1.SubagentResult_Error); !ok {
		return ""
	}
	return "subagent_error category=protocol_error"
}
