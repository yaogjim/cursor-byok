// subagent_recovery.go 实现 backend 重启后的 subagent run 离线恢复扫描。
//
// 启动时扫描 SubagentRunStore 并分类：
//   - terminal_prepared → 执行文件级幂等 parent commit（无需 ActiveStream）
//   - dispatched/running/backgrounded → ScanRecovery 已转为 awaiting_client_resume
//   - parent_committed/acknowledged/awaiting_client_resume → 无操作
//
// 恢复事务顺序与在线路径相同：
//  1. PrepareTerminal 已在首次运行时完成（result.json 已存在）
//  2. AppendEntriesToExistingWithUpdate 幂等追加 tool_result + metadata
//  3. MarkParentCommitted
package forwarder

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	runtimecore "cursor/internal/backend/agent/core"
)

// startSubagentRecovery 在 Service 启动时异步运行 subagent run 恢复扫描。
// ScanRecovery 负责将非终结 run 转为 awaiting_client_resume；
// 此方法额外重试所有 terminal_prepared run 的文件级 parent commit。
func (service *Service) startSubagentRecovery() {
	if service == nil || service.subagentRuns == nil {
		return
	}
	go func() {
		records, err := service.subagentRuns.ScanRecovery()
		if err != nil {
			log.Printf("subagent_recovery scan_failed err=%v", err)
			return
		}
		if len(records) == 0 {
			return
		}
		log.Printf("subagent_recovery recovering_runs count=%d", len(records))
		for _, record := range records {
			if record == nil {
				continue
			}
			if err := service.recoverSubagentTerminalPrepared(record); err != nil {
				log.Printf("subagent_recovery commit_failed run_id=%s err=%v",
					record.Identity.SubagentRunID, err)
			}
		}
	}()
}

// recoverSubagentTerminalPrepared 对一个 terminal_prepared 的 run 执行
// 文件级幂等 parent commit，不依赖 ActiveStream。
//
// 恢复安全保证：
//   - parent_commit_key 作为 HistoryEntry.IdempotencyKey，重复执行不产生重复 entry。
//   - 若 parent conversation 不存在（parent 已被清理），只记录日志，不报错。
//   - 若 store 未初始化，返回错误（run 状态保持 terminal_prepared 待下次重启）。
func (service *Service) recoverSubagentTerminalPrepared(record *SubagentRunRecord) error {
	if record == nil {
		return nil
	}
	runID := strings.TrimSpace(record.Identity.SubagentRunID)
	parentConvID := strings.TrimSpace(record.Identity.ParentConversationID)

	if runID == "" {
		return fmt.Errorf("subagent_recovery: empty run_id in record")
	}
	if parentConvID == "" {
		// parent conversation ID 缺失：标记为 awaiting_parent_resume，等待 parent 重新连接。
		log.Printf("subagent_recovery parent_conv_missing run_id=%s — marking awaiting_parent_resume", runID)
		if updateErr := service.markSubagentAwaitingParentResume(runID); updateErr != nil {
			log.Printf("subagent_recovery awaiting_parent_resume_failed run_id=%s err=%v", runID, updateErr)
		}
		return nil
	}
	if service.store == nil {
		return fmt.Errorf("subagent_recovery: conversation store not initialized for run %s", runID)
	}

	// 加载 result envelope（由首次运行 PrepareTerminal 写入）。
	envelope, err := service.subagentRuns.LoadResult(runID)
	if err != nil {
		return fmt.Errorf("subagent_recovery load_result run=%s: %w", runID, err)
	}
	if envelope == nil {
		return fmt.Errorf("subagent_recovery: result.json missing for run %s", runID)
	}

	parentCommitKey := strings.TrimSpace(envelope.ParentCommitKey)
	if parentCommitKey == "" {
		return fmt.Errorf("subagent_recovery: empty parent_commit_key in result for run %s", runID)
	}

	// 从 identity 和 envelope 重建 history entries。
	toolCallID := strings.TrimSpace(record.Identity.ParentToolCallID)
	turnSeq := record.Identity.ParentTurnSeq
	requestID := strings.TrimSpace(record.Identity.ParentRequestID)

	var toolCallPayload json.RawMessage
	if len(envelope.ToolCallEncoded) > 0 {
		toolCallPayload = json.RawMessage(envelope.ToolCallEncoded)
	}

	// 恢复时使用 envelope.ToolName；旧记录空值默认 "Task"（向后兼容）。
	toolNameForReplay := firstNonEmpty(strings.TrimSpace(envelope.ToolName), "Task")

	trEntry := withHistoryModelCallID(newToolResultEntry(
		turnSeq, requestID, toolCallID, toolNameForReplay,
		string(envelope.ArgsJSON), envelope.ToolResultPayload, "",
		toolCallPayload,
	), record.Identity.ParentModelCallID)
	trEntry.IdempotencyKey = parentCommitKey

	metaEntry := newMetadataEntry(turnSeq, requestID, "subagent_handoff_recovery", map[string]any{
		"subagent_run_id":        runID,
		"terminal_category":      string(record.TerminalCategory),
		"parent_commit_key":      parentCommitKey,
		"tool_call_id":           toolCallID,
		"root_conversation_id":   record.Identity.RootConversationID,
		"parent_conversation_id": record.Identity.ParentConversationID,
		"parent_model_call_id":   record.Identity.ParentModelCallID,
		"parent_tool_call_id":    record.Identity.ParentToolCallID,
		"child_conversation_id":  record.Identity.ChildConversationID,
		"agent_id":               record.Identity.AgentID,
		"recovered_at":           time.Now().UTC().Format(time.RFC3339),
	})

	entries := []HistoryEntry{trEntry, metaEntry}
	evidenceCategory := envelope.TerminalCategory
	if evidenceCategory == "" {
		evidenceCategory = record.TerminalCategory
	}
	if evidenceEntry, ok := newSubagentExecutionEvidenceEntry(
		turnSeq, requestID, record.Identity.ParentModelCallID, toolCallID, toolNameForReplay,
		envelope.ArgsJSON, envelope.ToolCallEncoded, evidenceCategory, 0,
	); ok {
		entries = append(entries, withHistoryModelCallID(evidenceEntry, record.Identity.ParentModelCallID))
	}

	// 在 parent conversation lock 内幂等追加（与在线路径相同的 CAS 检查）。
	_, _, appendErr := service.store.AppendEntriesToExistingWithUpdate(
		parentConvID,
		resetEntrySequences(entries),
		func(conv *ConversationFile) error {
			// entries 已追加到末尾；检查先前条目是否含相同幂等键。
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
		if errors.Is(appendErr, errSubagentAlreadyCommitted) {
			log.Printf("subagent_recovery already_committed run_id=%s", runID)
			// 继续 MarkParentCommitted（幂等）。
		} else if errors.Is(appendErr, errConversationNotFound) {
			log.Printf("subagent_recovery parent_conv_not_found run_id=%s parent_conv=%s — marking awaiting_parent_resume", runID, parentConvID)
			if updateErr := service.markSubagentAwaitingParentResume(runID); updateErr != nil {
				return fmt.Errorf("subagent_recovery mark awaiting_parent_resume run=%s: %w", runID, updateErr)
			}
			return nil
		} else {
			return fmt.Errorf("subagent_recovery append run=%s parent=%s: %w",
				runID, parentConvID, appendErr)
		}
	}

	// MarkParentCommitted（幂等）。
	if _, markErr := service.subagentRuns.MarkParentCommitted(runID); markErr != nil {
		// history 已落盘；只记录日志，不回滚。
		log.Printf("subagent_recovery mark_committed_failed run_id=%s err=%v", runID, markErr)
	} else {
		log.Printf("subagent_recovery committed run_id=%s parent_conv=%s", runID, parentConvID)
	}
	return nil
}

// recoverLateSubagentResult 仅保存已派发子任务的迟到终态，不启动模型、不重派工具、
// 不改变父请求的取消状态。只有稳定执行桥身份匹配时才接管结果。
func (service *Service) recoverLateSubagentResult(intent InboundIntent) (bool, error) {
	msg := intent.ExecClientMessage
	if service.subagentRuns == nil || service.store == nil || msg == nil || (msg.GetSubagentResult() == nil && msg.GetForceBackgroundSubagentResult() == nil) {
		return false, nil
	}
	conversationID := strings.TrimSpace(intent.ConversationID)
	stream, _ := service.broker.Get(intent.RequestID)
	if stream != nil {
		if conversationID != "" && conversationID != stream.ConversationID {
			return false, fmt.Errorf("subagent result conversation mismatch")
		}
		conversationID = stream.ConversationID
	}
	record, err := service.subagentRuns.FindDispatch(msg.GetExecId(), msg.GetId(), intent.RequestID, conversationID)
	if err != nil || record == nil {
		return false, err
	}
	runID := record.Identity.SubagentRunID
	if !isTerminalRunStatus(record.Status) && record.Status != SubagentRunTerminalPrepared && record.Status != SubagentRunAwaitingParentResume {
		if service.execBridge == nil {
			return true, fmt.Errorf("subagent recovery exec bridge is not initialized")
		}
		record, err = service.subagentRuns.BindChildIdentity(runID, "", extractSubagentAgentID(msg))
		if err != nil {
			return true, fmt.Errorf("subagent recovery bind identity: %w", err)
		}
		if record == nil || record.Dispatch == nil {
			return true, fmt.Errorf("subagent recovery dispatch disappeared")
		}
		pending := runtimecore.PendingExec{
			ExecID: record.Dispatch.ExecID, MessageID: record.Dispatch.MessageID,
			ExecKind: "subagent", SubagentRunID: runID,
			ToolCallID: record.Identity.ParentToolCallID, ModelCallID: record.Identity.ParentModelCallID,
			ArgsJSON: append([]byte(nil), record.Dispatch.ArgsJSON...),
		}
		result, applyErr := service.execBridge.ApplyExecClientMessage(msg, pending)
		if applyErr != nil {
			return true, applyErr
		}
		if !result.IsTerminal {
			return false, nil
		}
		if msg.GetForceBackgroundSubagentResult() != nil {
			result.ToolResultPayload = "[canceled] Subagent moved to background"
			result.ToolCall = nil
		}
		var encoded []byte
		if result.ToolCall != nil {
			encoded, err = protojson.Marshal(result.ToolCall)
			if err != nil {
				return true, err
			}
		}
		digest := computeSubagentStringDigest(result.ToolResultPayload)
		envelope := &SubagentResultEnvelope{
			SchemaVersion: subagentContractSchemaVersion, SubagentRunID: runID,
			TerminalCategory: categorizeSubagentResultMsg(msg), TerminalAt: time.Now().UTC(),
			ResultDigest: digest, ResultSummary: extractSubagentErrorSummary(msg),
			ToolName: deriveToolNameFromPendingExec(pending), ArgsJSON: pending.ArgsJSON,
			ToolResultPayload: result.ToolResultPayload, ToolCallEncoded: encoded,
			ParentCommitKey: computeSubagentParentCommitKey(runID, pending.ToolCallID, digest),
		}
		record, err = service.prepareRecoveredSubagentTerminal(record, envelope)
		if err != nil {
			return true, err
		}
		if record == nil {
			return true, fmt.Errorf("subagent recovery run disappeared")
		}
	}
	if !isTerminalRunStatus(record.Status) {
		if err := service.recoverSubagentTerminalPrepared(record); err != nil {
			return true, err
		}
	}
	// 新请求中回传旧任务结果时，同步其 checkpoint 内存；后续 replay 可读到已落盘结果。
	// 与正常追加保持一致的 stream -> conversation 锁顺序，避免旧快照覆盖更新。
	if stream != nil && !isTerminalIntentStream(stream) {
		stream.mu.Lock()
		conversation, loadErr := service.store.LoadConversation(conversationID)
		if loadErr == nil && conversation != nil {
			stream.CheckpointConversation = conversation
			rebuildStreamExecutionEvidenceLocked(stream)
		}
		stream.mu.Unlock()
		if loadErr != nil {
			return true, loadErr
		}
	}
	return true, nil
}

// prepareRecoveredSubagentTerminal 只重试 durable prepare 的版本竞争，不重放执行桥或工具。
// 已有终态始终胜出；非版本错误直接返回，连续竞争也有明确次数上限。
func (service *Service) prepareRecoveredSubagentTerminal(record *SubagentRunRecord, envelope *SubagentResultEnvelope) (*SubagentRunRecord, error) {
	runID := record.Identity.SubagentRunID
	for attempt := 0; attempt < 3; attempt++ {
		prepared, err := service.subagentRuns.PrepareTerminal(runID, record.Version, envelope)
		if err == nil {
			return prepared, nil
		}
		if !errors.Is(err, errSubagentPrepareVersionConflict) && !errors.Is(err, errSubagentTerminalConflict) {
			return nil, err
		}
		terminalConflict := errors.Is(err, errSubagentTerminalConflict)
		record, err = service.subagentRuns.LoadRun(runID)
		if err != nil {
			return nil, err
		}
		if record == nil {
			return nil, fmt.Errorf("subagent recovery run disappeared")
		}
		if isTerminalRunStatus(record.Status) || record.Status == SubagentRunTerminalPrepared || record.Status == SubagentRunAwaitingParentResume {
			return record, nil
		}
		if terminalConflict {
			// 上次可能在 result.json 与 run.json 两次原子写之间崩溃，
			// 使用已落盘的胜出信封修复 run 状态，而不是覆盖它。
			envelope, err = service.subagentRuns.LoadResult(runID)
			if err != nil {
				return nil, err
			}
			if envelope == nil {
				return nil, fmt.Errorf("subagent recovery terminal result missing")
			}
		}
	}
	return nil, fmt.Errorf("subagent recovery prepare contention: %w", errSubagentPrepareVersionConflict)
}

func (service *Service) markSubagentAwaitingParentResume(runID string) error {
	if service == nil || service.subagentRuns == nil {
		return nil
	}
	loaded, err := service.subagentRuns.LoadRun(runID)
	if err != nil || loaded == nil {
		return err
	}
	if loaded.Status != SubagentRunTerminalPrepared && loaded.Status != SubagentRunAwaitingParentResume {
		return nil
	}
	_, err = service.subagentRuns.UpdateRunStatus(runID, loaded.Version, SubagentRunAwaitingParentResume)
	return err
}
