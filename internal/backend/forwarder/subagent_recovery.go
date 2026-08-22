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

	trEntry := newToolResultEntry(
		turnSeq, requestID, toolCallID, toolNameForReplay,
		string(envelope.ArgsJSON), envelope.ToolResultPayload, "",
		toolCallPayload,
	)
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
