package forwarder

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"
	"time"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
)

const checkpointBlobWriteTimeout = 5 * time.Second

type pendingCheckpointBlobWrite struct {
	requestID uint32
	blob      CheckpointBlob
}

func successfulCheckpointTerminalAction(completion *pendingTurnCompletion) checkpointTerminalAction {
	if completion == nil {
		return checkpointTerminalAction{}
	}
	return checkpointTerminalAction{
		Kind:       checkpointTerminalActionComplete,
		Completion: *completion,
	}
}

func failedCheckpointTerminalAction(errorCode string, errorMessage string) checkpointTerminalAction {
	return failedCheckpointTerminalActionWithMeta(errorCode, errorMessage, checkpointTerminalAction{})
}

func failedCheckpointTerminalActionWithMeta(errorCode string, errorMessage string, meta checkpointTerminalAction) checkpointTerminalAction {
	return checkpointTerminalAction{
		Kind:          checkpointTerminalActionFail,
		ErrorCode:     strings.TrimSpace(errorCode),
		ErrorMessage:  strings.TrimSpace(errorMessage),
		Retryable:     meta.Retryable,
		HTTPStatus:    strings.TrimSpace(meta.HTTPStatus),
		ErrorCategory: strings.TrimSpace(meta.ErrorCategory),
		ModelCallID:   strings.TrimSpace(meta.ModelCallID),
		RequestID:     strings.TrimSpace(meta.RequestID),
	}
}

func checkpointActionWithSubagentAcknowledgement(runID string) checkpointTerminalAction {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return checkpointTerminalAction{}
	}
	return checkpointTerminalAction{AcknowledgeSubagentRunIDs: []string{runID}}
}

func mergeCheckpointTerminalAction(existing checkpointTerminalAction, incoming checkpointTerminalAction) checkpointTerminalAction {
	merged := existing
	if incoming.Kind != checkpointTerminalActionNone {
		merged.Kind = incoming.Kind
		merged.Completion = incoming.Completion
		merged.ErrorCode = incoming.ErrorCode
		merged.ErrorMessage = incoming.ErrorMessage
		merged.Retryable = incoming.Retryable
		merged.HTTPStatus = incoming.HTTPStatus
		merged.ErrorCategory = incoming.ErrorCategory
		merged.ModelCallID = incoming.ModelCallID
		merged.RequestID = incoming.RequestID
	}
	seen := make(map[string]struct{}, len(existing.AcknowledgeSubagentRunIDs)+len(incoming.AcknowledgeSubagentRunIDs))
	merged.AcknowledgeSubagentRunIDs = nil
	for _, runID := range append(append([]string(nil), existing.AcknowledgeSubagentRunIDs...), incoming.AcknowledgeSubagentRunIDs...) {
		runID = strings.TrimSpace(runID)
		if runID == "" {
			continue
		}
		if _, ok := seen[runID]; ok {
			continue
		}
		seen[runID] = struct{}{}
		merged.AcknowledgeSubagentRunIDs = append(merged.AcknowledgeSubagentRunIDs, runID)
	}
	return merged
}

func (service *Service) queueCheckpointProjection(stream *ActiveStream, projection *CheckpointProjection, completion *pendingTurnCompletion) error {
	return service.queueCheckpointProjectionWithTerminal(stream, projection, successfulCheckpointTerminalAction(completion))
}

func (service *Service) queueCheckpointProjectionWithTerminal(stream *ActiveStream, projection *CheckpointProjection, terminal checkpointTerminalAction) error {
	if service == nil || stream == nil || projection == nil || projection.State == nil {
		return nil
	}
	state, ok := proto.Clone(projection.State).(*agentv1.ConversationStateStructure)
	if !ok || state == nil {
		return fmt.Errorf("clone checkpoint state")
	}

	stream.mu.Lock()
	if stream.PendingCheckpointBlobWrites == nil {
		stream.PendingCheckpointBlobWrites = make(map[uint32]string)
	}
	if stream.ConfirmedCheckpointBlobs == nil {
		stream.ConfirmedCheckpointBlobs = make(map[string]struct{})
	}
	if stream.PendingCheckpoint != nil {
		terminal = mergeCheckpointTerminalAction(stream.PendingCheckpoint.Terminal, terminal)
	}
	required := make(map[string]struct{}, len(projection.Blobs))
	pendingKeys := make(map[string]struct{}, len(stream.PendingCheckpointBlobWrites))
	for _, key := range stream.PendingCheckpointBlobWrites {
		pendingKeys[key] = struct{}{}
	}
	toWrite := make([]pendingCheckpointBlobWrite, 0, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		key := string(blob.ID)
		if key == "" {
			continue
		}
		required[key] = struct{}{}
		if _, confirmed := stream.ConfirmedCheckpointBlobs[key]; confirmed {
			continue
		}
		if _, pending := pendingKeys[key]; pending {
			continue
		}
		stream.NextCheckpointBlobRequestID++
		if stream.NextCheckpointBlobRequestID == 0 {
			stream.NextCheckpointBlobRequestID++
		}
		requestID := stream.NextCheckpointBlobRequestID
		stream.PendingCheckpointBlobWrites[requestID] = key
		pendingKeys[key] = struct{}{}
		toWrite = append(toWrite, pendingCheckpointBlobWrite{requestID: requestID, blob: blob})
	}
	stream.PendingCheckpoint = &pendingCheckpointPublish{
		State:    state,
		Required: required,
		Terminal: terminal,
	}
	if terminal.Kind != checkpointTerminalActionNone {
		stream.Phase = TurnPhaseCheckpointing
	}
	stream.UpdatedAt = time.Now().UTC()
	pendingCount := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()

	for _, write := range toWrite {
		if err := service.broker.Publish(stream.RequestID, StreamEvent{
			Message: buildSetCheckpointBlobMessage(write.requestID, write.blob),
		}); err != nil {
			service.recordCheckpointBlobDelivery(stream, write.requestID, pendingCount, "publish_failed")
			return service.finishAfterCheckpointSyncFailure(stream, fmt.Errorf("publish checkpoint blob: %w", err))
		}
		service.recordCheckpointBlobDelivery(stream, write.requestID, pendingCount, "sent")
	}
	if service.checkpointProjectionReady(stream) {
		return service.publishReadyCheckpoint(stream)
	}
	// Checkpoints reference these Blob IDs, so the client must confirm every
	// required Blob before the checkpoint becomes visible.
	service.scheduleStreamTimer(
		stream,
		providerTimerKey(streamTimerCheckpointBlobs, ""),
		checkpointBlobWriteTimeout,
		streamTimerCheckpointBlobs,
		"",
		0,
		"checkpoint blob write timeout",
	)
	return nil
}

func (service *Service) checkpointProjectionReady(stream *ActiveStream) bool {
	if stream == nil {
		return false
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.PendingCheckpoint == nil {
		return false
	}
	for key := range stream.PendingCheckpoint.Required {
		if _, confirmed := stream.ConfirmedCheckpointBlobs[key]; !confirmed {
			return false
		}
	}
	return true
}

func (service *Service) handleCheckpointBlobResult(stream *ActiveStream, message *agentv1.KvClientMessage) error {
	if service == nil || stream == nil || message == nil || message.GetSetBlobResult() == nil {
		return nil
	}
	blobErr := message.GetSetBlobResult().GetError()
	stream.mu.Lock()
	key, ok := stream.PendingCheckpointBlobWrites[message.GetId()]
	if ok {
		delete(stream.PendingCheckpointBlobWrites, message.GetId())
	}
	required := false
	if ok && stream.PendingCheckpoint != nil {
		_, required = stream.PendingCheckpoint.Required[key]
	}
	if ok && blobErr == nil {
		stream.ConfirmedCheckpointBlobs[key] = struct{}{}
	}
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	if !ok {
		// 不在待确认表说明该 ACK 未匹配本次 pending 写入：可能是正常重复 ACK，
		// 也可能是超时/取消后的迟到 ACK。当前状态无法可靠区分，统一记 unmatched，
		// 不引入持久账本或超出当前进程的 timedOut 集合。
		service.recordCheckpointBlobResultEvent(stream, "degraded", "unmatched", message.GetId())
		return nil
	}
	if blobErr != nil {
		service.recordCheckpointBlobResultEvent(stream, "error", "rejected", message.GetId())
	} else {
		service.recordCheckpointBlobResultEvent(stream, "ok", "ack", message.GetId())
	}
	if blobErr != nil && required {
		// This cause reaches both application logs and structured diagnostics.
		// Keep the rejection category, never the blob key or client-provided text.
		return service.finishAfterCheckpointSyncFailure(stream, errors.New("client rejected checkpoint blob"))
	}
	if service.checkpointProjectionReady(stream) {
		return service.publishReadyCheckpoint(stream)
	}
	return nil
}

func (service *Service) publishReadyCheckpoint(stream *ActiveStream) error {
	if service == nil || stream == nil {
		return nil
	}
	stream.mu.Lock()
	pending := stream.PendingCheckpoint
	if pending == nil {
		stream.mu.Unlock()
		return nil
	}
	for key := range pending.Required {
		if _, confirmed := stream.ConfirmedCheckpointBlobs[key]; !confirmed {
			stream.mu.Unlock()
			return nil
		}
	}
	stream.PendingCheckpoint = nil
	state := pending.State
	terminal := pending.Terminal
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerCheckpointBlobs, ""))
	if err := service.broker.Publish(stream.RequestID, StreamEvent{Message: buildCheckpointMessage(state)}); err != nil {
		if terminal.Kind != checkpointTerminalActionNone {
			log.Printf("forwarder checkpoint publish skipped before terminal request_id=%s err=%v", stream.RequestID, err)
			service.recordCheckpointSkip(stream, "checkpoint_publish_skipped", "publish_before_terminal", err, 0)
			return service.finishCheckpointTerminalAction(stream, terminal)
		}
		return err
	}
	service.acknowledgeSubagentRunsAfterCheckpoint(terminal.AcknowledgeSubagentRunIDs)
	return service.finishCheckpointTerminalAction(stream, terminal)
}

func (service *Service) acknowledgeSubagentRunsAfterCheckpoint(runIDs []string) {
	if service == nil || service.subagentRuns == nil {
		return
	}
	for _, runID := range runIDs {
		runID = strings.TrimSpace(runID)
		if runID == "" {
			continue
		}
		if _, err := service.subagentRuns.MarkAcknowledged(runID); err != nil {
			log.Printf("subagent_service mark_acknowledged_failed run_id=%s err=%v", runID, err)
		}
	}
}

func (service *Service) handleCheckpointBlobTimeout(stream *ActiveStream) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	pendingCount := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()
	service.recordCheckpointBlobEvent(stream, "checkpoint_blob_timeout", "error", "timeout", "timeout", map[string]any{
		"blob_count":         pendingCount,
		"missing_blob_count": pendingCount,
	})
	return service.finishAfterCheckpointSyncFailure(stream, fmt.Errorf("%d checkpoint blob writes timed out", pendingCount))
}

func (service *Service) finishAfterCheckpointSyncFailure(stream *ActiveStream, cause error) error {
	if stream == nil {
		return nil
	}
	stream.mu.Lock()
	pending := stream.PendingCheckpoint
	missingKeyCount := checkpointMissingBlobKeyCount(stream.PendingCheckpointBlobWrites)
	stream.PendingCheckpoint = nil
	stream.PendingCheckpointBlobWrites = make(map[uint32]string)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerCheckpointBlobs, ""))
	if cause != nil {
		log.Printf("forwarder checkpoint blob sync skipped request_id=%s conversation_id=%s err=%v", stream.RequestID, stream.ConversationID, cause)
		service.recordCheckpointSkip(stream, "checkpoint_blob_sync_skipped", "blob_sync", cause, missingKeyCount)
	}
	if pending != nil {
		return service.finishCheckpointTerminalAction(stream, pending.Terminal)
	}
	return nil
}

func (service *Service) finishCheckpointTerminalAction(stream *ActiveStream, terminal checkpointTerminalAction) error {
	switch terminal.Kind {
	case checkpointTerminalActionComplete:
		return service.finishSuccessfulTurnAfterCheckpoint(stream, terminal.Completion)
	case checkpointTerminalActionFail:
		return service.finishFailedTurnAfterCheckpoint(stream, terminal)
	default:
		return nil
	}
}

func (service *Service) discardPendingCheckpoint(stream *ActiveStream, reason string) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	pendingCount := len(stream.PendingCheckpointBlobWrites)
	stream.PendingCheckpoint = nil
	stream.PendingCheckpointBlobWrites = make(map[uint32]string)
	stream.UpdatedAt = time.Now().UTC()
	stream.mu.Unlock()
	clearStreamTimer(stream, providerTimerKey(streamTimerCheckpointBlobs, ""))
	if strings.TrimSpace(reason) != "" {
		log.Printf("forwarder pending checkpoint discarded request_id=%s conversation_id=%s reason=%s", stream.RequestID, stream.ConversationID, strings.TrimSpace(reason))
	}
	if pendingCount > 0 {
		result := "discarded"
		if strings.Contains(strings.ToLower(reason), "cancel") {
			result = "canceled"
		}
		service.recordCheckpointBlobEvent(stream, "checkpoint_blob_canceled", "canceled", "cancel", result, map[string]any{
			"blob_count":         pendingCount,
			"missing_blob_count": pendingCount,
		})
	}
}

// recordCheckpointBlobDelivery 记录一次 SetBlob 投递；只带请求标识与计数，不带 blob key。
func (service *Service) recordCheckpointBlobDelivery(stream *ActiveStream, requestID uint32, pendingCount int, result string) {
	status := "ok"
	if result != "sent" {
		status = "error"
	}
	service.recordCheckpointBlobEvent(stream, "checkpoint_blob_dispatch", status, "delivery", result, map[string]any{
		"checkpoint_request_id": requestID,
		"blob_count":            1,
		"pending_blob_count":    pendingCount,
	})
}

func (service *Service) recordCheckpointBlobResultEvent(stream *ActiveStream, status string, result string, requestID uint32) {
	service.recordCheckpointBlobEvent(stream, "checkpoint_blob_result", status, "ack", result, map[string]any{
		"checkpoint_request_id": requestID,
		"blob_count":            1,
	})
}

// recordCheckpointBlobEvent 仅投影安全元数据：请求标识、blob 请求 id、计数、阶段与结果。
// 不记录 blob key 数组或正文；missing_blob_keys 已从投影与白名单移除，只保留计数。
func (service *Service) recordCheckpointBlobEvent(stream *ActiveStream, eventName string, status string, phase string, result string, fields map[string]any) {
	if service == nil || stream == nil {
		return
	}
	payload := map[string]any{
		"phase":             strings.TrimSpace(phase),
		"checkpoint_result": strings.TrimSpace(result),
	}
	if trimmed := strings.TrimSpace(status); trimmed != "" {
		payload["status"] = trimmed
	}
	for key, value := range fields {
		payload[key] = value
	}
	service.debug.LogRuntime(context.Background(), stream.RequestID, stream.ConversationID, eventName, payload)
}

func (service *Service) recordCheckpointSkip(stream *ActiveStream, eventName string, reason string, cause error, missingBlobKeyCount int) {
	if service == nil || stream == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	fields := map[string]any{
		"status":      "degraded",
		"kind":        reason,
		"skip_reason": reason,
	}
	if cause != nil {
		fields["error_summary"] = cause.Error()
	}
	if missingBlobKeyCount > 0 {
		fields["missing_blob_key_count"] = missingBlobKeyCount
	}
	service.debug.LogRuntime(context.Background(), stream.RequestID, stream.ConversationID, eventName, fields)
}

func checkpointMissingBlobKeyCount(writes map[uint32]string) int {
	if len(writes) == 0 {
		return 0
	}
	seen := make(map[string]struct{}, len(writes))
	for _, key := range writes {
		if key == "" {
			continue
		}
		seen[key] = struct{}{}
	}
	return len(seen)
}
