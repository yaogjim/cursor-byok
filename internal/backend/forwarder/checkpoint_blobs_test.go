package forwarder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	"cursor/internal/observability"
)

func TestCheckpointAcknowledgesSubagentOnlyAfterCheckpointPublished(t *testing.T) {
	service, stream, projection := testCheckpointBlobProjection(t)
	runStore := NewSubagentRunStore(t.TempDir())
	service.subagentRuns = runStore
	runID := "run-checkpoint-ack"
	record, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: stream.ConversationID,
		ParentToolCallID:     "tool-call-1",
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	envelope := &SubagentResultEnvelope{
		SubagentRunID:    runID,
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "commit-key-1",
	}
	if _, err := runStore.PrepareTerminal(runID, record.Version, envelope); err != nil {
		t.Fatalf("PrepareTerminal() error = %v", err)
	}
	if _, err := runStore.MarkParentCommitted(runID); err != nil {
		t.Fatalf("MarkParentCommitted() error = %v", err)
	}

	action := checkpointActionWithSubagentAcknowledgement(runID)
	if err := service.queueCheckpointProjectionWithTerminal(stream, projection, action); err != nil {
		t.Fatalf("queueCheckpointProjectionWithTerminal() error = %v", err)
	}
	before, err := runStore.LoadRun(runID)
	if err != nil {
		t.Fatalf("LoadRun() before ACK error = %v", err)
	}
	if before.Status != SubagentRunParentCommitted {
		t.Fatalf("status before checkpoint publish = %q, want parent_committed", before.Status)
	}

	acknowledgeCheckpointBlobs(t, service, stream)
	after, err := runStore.LoadRun(runID)
	if err != nil {
		t.Fatalf("LoadRun() after ACK error = %v", err)
	}
	if after.Status != SubagentRunAcknowledged {
		t.Fatalf("status after checkpoint publish = %q, want acknowledged", after.Status)
	}
}

func TestCheckpointBlobSyncWaitsForAcknowledgementsBeforePublishingNonTerminalCheckpoint(t *testing.T) {
	service, stream, projection := testCheckpointBlobProjection(t)
	if err := service.queueCheckpointProjection(stream, projection, nil); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	events := readCheckpointTestEvents(t, service, stream)
	if len(events) != len(projection.Blobs) {
		t.Fatalf("events before ACK = %d, want %d Blob writes", len(events), len(projection.Blobs))
	}
	for _, event := range events {
		if event.Message.GetKvServerMessage().GetSetBlobArgs() == nil {
			t.Fatalf("event before ACK = %#v, want set_blob_args", event.Message)
		}
	}

	stream.mu.Lock()
	var firstRequestID uint32
	for requestID := range stream.PendingCheckpointBlobWrites {
		firstRequestID = requestID
		break
	}
	stream.mu.Unlock()
	if firstRequestID == 0 {
		t.Fatal("checkpoint projection has no pending Blob writes")
	}
	if err := service.handleCheckpointBlobResult(stream, &agentv1.KvClientMessage{
		Id: firstRequestID,
		Message: &agentv1.KvClientMessage_SetBlobResult{
			SetBlobResult: &agentv1.SetBlobResult{},
		},
	}); err != nil {
		t.Fatalf("first Blob ACK error = %v", err)
	}
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if event.Message.GetConversationCheckpointUpdate() != nil {
			t.Fatal("checkpoint published after only a partial Blob acknowledgement")
		}
	}

	acknowledgeCheckpointBlobs(t, service, stream)
	events = readCheckpointTestEvents(t, service, stream)
	checkpointCount := 0
	for _, event := range events {
		if event.Message.GetConversationCheckpointUpdate() != nil {
			checkpointCount++
		}
	}
	if checkpointCount != 1 {
		t.Fatalf("checkpoints after ACK = %d, want 1", checkpointCount)
	}
}

func TestCheckpointBlobSyncPublishesCheckpointBeforeSuccessfulTerminal(t *testing.T) {
	service, stream, projection := testCheckpointBlobProjection(t)
	completion := &pendingTurnCompletion{
		RequestID: stream.RequestID,
		Usage:     turnUsageSnapshot{InputTokens: 11, OutputTokens: 7},
	}
	if err := service.queueCheckpointProjection(stream, projection, completion); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	eventsBeforeACK := readCheckpointTestEvents(t, service, stream)
	for _, event := range eventsBeforeACK {
		if event.Message.GetConversationCheckpointUpdate() != nil || event.Message.GetInteractionUpdate().GetTurnEnded() != nil || event.End {
			t.Fatalf("event before ACK = %#v, want only Blob writes", event)
		}
	}
	acknowledgeCheckpointBlobs(t, service, stream)

	events := readCheckpointTestEvents(t, service, stream)
	checkpointIndex, turnEndedIndex, endIndex := -1, -1, -1
	for index, event := range events {
		switch {
		case event.Message.GetConversationCheckpointUpdate() != nil:
			checkpointIndex = index
		case event.Message.GetInteractionUpdate().GetTurnEnded() != nil:
			turnEndedIndex = index
		case event.End:
			endIndex = index
		}
	}
	if checkpointIndex < 0 || turnEndedIndex <= checkpointIndex || endIndex <= turnEndedIndex {
		t.Fatalf("terminal order checkpoint=%d turn_ended=%d end=%d", checkpointIndex, turnEndedIndex, endIndex)
	}
}

func TestCheckpointBlobTimeoutDoesNotFailSuccessfulTurn(t *testing.T) {
	service, stream, projection := testCheckpointBlobProjection(t)
	completion := &pendingTurnCompletion{
		RequestID: stream.RequestID,
		Usage:     turnUsageSnapshot{InputTokens: 11, OutputTokens: 7},
	}
	if err := service.queueCheckpointProjection(stream, projection, completion); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	if err := service.handleCheckpointBlobTimeout(stream); err != nil {
		t.Fatalf("handleCheckpointBlobTimeout() error = %v", err)
	}

	events := readCheckpointTestEvents(t, service, stream)
	var checkpoint, turnEnded, successfulEnd bool
	for _, event := range events {
		checkpoint = checkpoint || event.Message.GetConversationCheckpointUpdate() != nil
		turnEnded = turnEnded || event.Message.GetInteractionUpdate().GetTurnEnded() != nil
		successfulEnd = successfulEnd || event.End && event.TerminalErrorCode == ""
	}
	if checkpoint || !turnEnded || !successfulEnd {
		t.Fatalf("timeout events checkpoint=%v turn_ended=%v successful_end=%v", checkpoint, turnEnded, successfulEnd)
	}
}

func TestCheckpointBlobTimeoutEmitsDegradedEventWithoutFailingTurn(t *testing.T) {
	service, stream, projection := testCheckpointBlobProjection(t)
	capture := &debugRecorderTestCapture{}
	service.debug = newDebugRecorder(t.TempDir(), service.broker, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(service.debug.Close)
	completion := &pendingTurnCompletion{
		RequestID: stream.RequestID,
		Usage:     turnUsageSnapshot{InputTokens: 11, OutputTokens: 7},
	}
	if err := service.queueCheckpointProjection(stream, projection, completion); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	if err := service.handleCheckpointBlobTimeout(stream); err != nil {
		t.Fatalf("handleCheckpointBlobTimeout() error = %v", err)
	}

	events := readCheckpointTestEvents(t, service, stream)
	var turnEnded, successfulEnd bool
	for _, event := range events {
		turnEnded = turnEnded || event.Message.GetInteractionUpdate().GetTurnEnded() != nil
		successfulEnd = successfulEnd || event.End && event.TerminalErrorCode == ""
	}
	if !turnEnded || !successfulEnd {
		t.Fatalf("timeout still has to complete the turn: turn_ended=%v successful_end=%v", turnEnded, successfulEnd)
	}

	var skipped observability.Capture
	for _, item := range capture.captures {
		if item.Event.Event == "checkpoint_blob_sync_skipped" {
			skipped = item
		}
	}
	if skipped.Event.Event == "" {
		t.Fatalf("missing checkpoint skip event: %+v", capture.captures)
	}
	if skipped.Event.Status != "degraded" || skipped.Event.SemanticOutcome != observability.OutcomeDegraded {
		t.Fatalf("skip event = %+v", skipped.Event)
	}
	if skipped.Event.Fields["kind"] != "blob_sync" || skipped.Event.Fields["skip_reason"] != "blob_sync" || skipped.Event.Fields["error_summary"] == nil {
		t.Fatalf("skip event fields = %#v", skipped.Event.Fields)
	}
	if checkpointTestFieldInt(skipped.Event, "missing_blob_key_count") == 0 {
		t.Fatalf("missing blob key count not recorded: %#v", skipped.Event.Fields)
	}
	if _, leaked := skipped.Event.Fields["missing_blob_keys"]; leaked {
		t.Fatalf("skip event must not carry blob keys: %#v", skipped.Event.Fields)
	}
	raw, _ := skipped.Payload.Data.(map[string]any)
	if raw["request_id"] != stream.RequestID || raw["conversation_id"] != stream.ConversationID {
		t.Fatalf("skip payload lost query fields: %#v", raw)
	}
	if _, leaked := raw["missing_blob_keys"]; leaked {
		t.Fatalf("skip payload must not carry blob keys: %#v", raw)
	}
}

func TestCheckpointBlobSyncPublishesCheckpointBeforeFailedTerminal(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	if err := service.failActiveStream(
		stream,
		stream.ConversationID,
		stream.RequestID,
		"model-call-1",
		"provider_error",
		"provider failed",
	); err != nil {
		t.Fatalf("failActiveStream() error = %v", err)
	}

	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if event.Message.GetConversationCheckpointUpdate() != nil || event.End {
			t.Fatalf("event before ACK = %#v, want only Blob writes", event)
		}
	}
	stream.mu.Lock()
	phaseBeforeACK := stream.Phase
	statusBeforeACK := stream.Status
	stream.mu.Unlock()
	if phaseBeforeACK != TurnPhaseCheckpointing || isTerminalStreamStatus(statusBeforeACK) {
		t.Fatalf("before ACK phase=%s status=%s, want checkpointing and non-terminal", phaseBeforeACK, statusBeforeACK)
	}

	acknowledgeCheckpointBlobs(t, service, stream)
	events := readCheckpointTestEvents(t, service, stream)
	checkpointIndex, endIndex := -1, -1
	for index, event := range events {
		switch {
		case event.Message.GetConversationCheckpointUpdate() != nil:
			checkpointIndex = index
		case event.End:
			endIndex = index
			if event.TerminalErrorCode != "provider_error" || event.TerminalErrorMessage != "provider failed" {
				t.Fatalf("terminal event = %#v, want provider error", event)
			}
		}
	}
	if checkpointIndex < 0 || endIndex <= checkpointIndex {
		t.Fatalf("terminal order checkpoint=%d end=%d", checkpointIndex, endIndex)
	}
	stream.mu.Lock()
	phaseAfterACK := stream.Phase
	statusAfterACK := stream.Status
	stream.mu.Unlock()
	if phaseAfterACK != TurnPhaseFailed || statusAfterACK != StreamStatusFailed {
		t.Fatalf("after ACK phase=%s status=%s, want failed", phaseAfterACK, statusAfterACK)
	}
}

func TestCheckpointBlobTimeoutStillPublishesFailedTerminal(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	if err := service.failActiveStream(
		stream,
		stream.ConversationID,
		stream.RequestID,
		"model-call-1",
		"provider_error",
		"provider failed",
	); err != nil {
		t.Fatalf("failActiveStream() error = %v", err)
	}
	if err := service.handleCheckpointBlobTimeout(stream); err != nil {
		t.Fatalf("handleCheckpointBlobTimeout() error = %v", err)
	}

	events := readCheckpointTestEvents(t, service, stream)
	var checkpoint, failedEnd bool
	for _, event := range events {
		checkpoint = checkpoint || event.Message.GetConversationCheckpointUpdate() != nil
		failedEnd = failedEnd || event.End && event.TerminalErrorCode == "provider_error" && event.TerminalErrorMessage == "provider failed"
	}
	if checkpoint || !failedEnd {
		t.Fatalf("timeout events checkpoint=%v failed_end=%v", checkpoint, failedEnd)
	}
}

func TestManualCompactionNoopWaitsForCheckpointBeforeTerminal(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if _, err := service.store.SaveConversationWithEntries(stream.ConversationID, conversation, conversation.Entries); err != nil {
		t.Fatalf("SaveConversationWithEntries() error = %v", err)
	}
	if err := service.finishManualCompactionNoop(stream); err != nil {
		t.Fatalf("finishManualCompactionNoop() error = %v", err)
	}

	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if event.Message.GetInteractionUpdate().GetTurnEnded() != nil || event.End {
			t.Fatalf("terminal event before checkpoint Blob ACK = %#v", event)
		}
	}
	acknowledgeCheckpointBlobs(t, service, stream)

	events := readCheckpointTestEvents(t, service, stream)
	checkpointIndex, turnEndedIndex, endIndex := -1, -1, -1
	for index, event := range events {
		switch {
		case event.Message.GetConversationCheckpointUpdate() != nil:
			checkpointIndex = index
		case event.Message.GetInteractionUpdate().GetTurnEnded() != nil:
			turnEndedIndex = index
		case event.End:
			endIndex = index
		}
	}
	if checkpointIndex < 0 || turnEndedIndex <= checkpointIndex || endIndex <= turnEndedIndex {
		t.Fatalf("terminal order checkpoint=%d turn_ended=%d end=%d", checkpointIndex, turnEndedIndex, endIndex)
	}
}

func TestCancellationDiscardsUnpublishedCheckpointAndIgnoresLateAcknowledgements(t *testing.T) {
	service, stream, projection := testCheckpointBlobProjection(t)
	if err := service.queueCheckpointProjection(stream, projection, nil); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	eventsBeforeCancel := readCheckpointTestEvents(t, service, stream)
	checkpointBeforeCancel := 0
	for _, event := range eventsBeforeCancel {
		if event.Message.GetConversationCheckpointUpdate() != nil {
			checkpointBeforeCancel++
		}
	}
	if checkpointBeforeCancel != 0 {
		t.Fatalf("checkpoints before cancel = %d, want 0", checkpointBeforeCancel)
	}
	stream.mu.Lock()
	requestIDs := make([]uint32, 0, len(stream.PendingCheckpointBlobWrites))
	for requestID := range stream.PendingCheckpointBlobWrites {
		requestIDs = append(requestIDs, requestID)
	}
	stream.mu.Unlock()
	if err := service.handleCancelIntent(InboundIntent{
		Kind:         "cancel",
		RequestID:    stream.RequestID,
		CancelReason: "user stopped",
	}); err != nil {
		t.Fatalf("handleCancelIntent() error = %v", err)
	}
	for _, requestID := range requestIDs {
		if err := service.handleCheckpointBlobResult(stream, &agentv1.KvClientMessage{
			Id: requestID,
			Message: &agentv1.KvClientMessage_SetBlobResult{
				SetBlobResult: &agentv1.SetBlobResult{},
			},
		}); err != nil {
			t.Fatalf("late ACK %d error = %v", requestID, err)
		}
	}

	events := readCheckpointTestEvents(t, service, stream)
	checkpointCount := 0
	var canceledEnd bool
	for _, event := range events {
		if event.Message.GetConversationCheckpointUpdate() != nil {
			checkpointCount++
		}
		canceledEnd = canceledEnd || event.End && event.TerminalErrorCode == "canceled"
	}
	stream.mu.Lock()
	pending := stream.PendingCheckpoint
	stream.mu.Unlock()
	if checkpointCount != 0 || !canceledEnd || pending != nil {
		t.Fatalf("cancel events checkpoints=%d canceled_end=%v pending=%v", checkpointCount, canceledEnd, pending != nil)
	}
}

func TestCheckpointBlobEventsRecordSafeDeliveryAndAckMetadata(t *testing.T) {
	service, stream, projection := testCheckpointBlobProjection(t)
	capture := &debugRecorderTestCapture{}
	service.debug = newDebugRecorder(t.TempDir(), service.broker, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(service.debug.Close)

	if err := service.queueCheckpointProjection(stream, projection, nil); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	deliveries := checkpointTestCaptureEvents(capture, "checkpoint_blob_dispatch")
	if len(deliveries) != len(projection.Blobs) {
		t.Fatalf("dispatch events = %d, want %d Blobs", len(deliveries), len(projection.Blobs))
	}
	for _, item := range deliveries {
		if checkpointTestFieldString(item.Event, "phase") != "delivery" || checkpointTestFieldString(item.Event, "checkpoint_result") != "sent" {
			t.Fatalf("dispatch fields = %#v", item.Event.Fields)
		}
		if checkpointTestFieldInt(item.Event, "checkpoint_request_id") == 0 || checkpointTestFieldInt(item.Event, "blob_count") != 1 {
			t.Fatalf("dispatch metadata = %#v", item.Event.Fields)
		}
		if _, leaked := item.Event.Fields["missing_blob_keys"]; leaked {
			t.Fatalf("dispatch leaked blob keys: %#v", item.Event.Fields)
		}
	}

	acknowledgeCheckpointBlobs(t, service, stream)
	acks := checkpointTestCaptureEvents(capture, "checkpoint_blob_result")
	if len(acks) != len(projection.Blobs) {
		t.Fatalf("result events = %d, want %d Blobs", len(acks), len(projection.Blobs))
	}
	for _, item := range acks {
		if checkpointTestFieldString(item.Event, "phase") != "ack" || checkpointTestFieldString(item.Event, "checkpoint_result") != "ack" {
			t.Fatalf("ack fields = %#v", item.Event.Fields)
		}
		if _, leaked := item.Event.Fields["missing_blob_keys"]; leaked {
			t.Fatalf("ack leaked blob keys: %#v", item.Event.Fields)
		}
	}
}

func TestCheckpointBlobEventsDistinguishRejectAndUnmatchedAck(t *testing.T) {
	service, stream, projection := testCheckpointBlobProjection(t)
	capture := &debugRecorderTestCapture{}
	service.debug = newDebugRecorder(t.TempDir(), service.broker, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(service.debug.Close)

	if err := service.queueCheckpointProjection(stream, projection, nil); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	stream.mu.Lock()
	var firstRequestID uint32
	for requestID := range stream.PendingCheckpointBlobWrites {
		firstRequestID = requestID
		break
	}
	stream.mu.Unlock()

	if err := service.handleCheckpointBlobResult(stream, &agentv1.KvClientMessage{
		Id: firstRequestID,
		Message: &agentv1.KvClientMessage_SetBlobResult{
			SetBlobResult: &agentv1.SetBlobResult{Error: &agentv1.Error{Message: "private-client-body https://private.invalid/checkpoint?token=canary"}},
		},
	}); err != nil {
		t.Fatalf("reject ACK error = %v", err)
	}
	results := checkpointTestCaptureEvents(capture, "checkpoint_blob_result")
	if len(results) == 0 || checkpointTestFieldString(results[len(results)-1].Event, "checkpoint_result") != "rejected" {
		t.Fatalf("rejected result fields = %#v", capture.captures)
	}
	if results[len(results)-1].Event.Status != "error" {
		t.Fatalf("rejected status = %q, want error", results[len(results)-1].Event.Status)
	}

	skips := checkpointTestCaptureEvents(capture, "checkpoint_blob_sync_skipped")
	if len(skips) != 1 || checkpointTestFieldString(skips[0].Event, "error_summary") != "client rejected checkpoint blob" {
		t.Fatal("rejected ACK must record only the fixed category, without blob key or client text")
	}
	stream.mu.Lock()
	pendingCleared := stream.PendingCheckpoint == nil && len(stream.PendingCheckpointBlobWrites) == 0
	stream.mu.Unlock()
	if !pendingCleared {
		t.Fatal("rejected ACK must still discard the pending checkpoint")
	}

	// 全部 ACK 后再收到任何不在 pending 表的 ACK：重复与超时后迟到无法可靠区分，
	// 统一记 unmatched/degraded，不猜测 late_ack。
	unmatchedService, unmatchedStream, unmatchedProjection := testCheckpointBlobProjection(t)
	unmatchedCapture := &debugRecorderTestCapture{}
	unmatchedService.debug = newDebugRecorder(t.TempDir(), unmatchedService.broker, debugRecorderTestConfig("basic"), unmatchedCapture)
	t.Cleanup(unmatchedService.debug.Close)
	if err := unmatchedService.queueCheckpointProjection(unmatchedStream, unmatchedProjection, nil); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	acknowledgeCheckpointBlobs(t, unmatchedService, unmatchedStream)
	unmatchedStream.mu.Lock()
	settledRequestID := unmatchedStream.NextCheckpointBlobRequestID
	neverDispatchedID := unmatchedStream.NextCheckpointBlobRequestID + 1000
	unmatchedStream.mu.Unlock()

	for _, ackID := range []uint32{settledRequestID, neverDispatchedID} {
		if err := unmatchedService.handleCheckpointBlobResult(unmatchedStream, &agentv1.KvClientMessage{
			Id: ackID,
			Message: &agentv1.KvClientMessage_SetBlobResult{
				SetBlobResult: &agentv1.SetBlobResult{},
			},
		}); err != nil {
			t.Fatalf("unmatched ACK %d error = %v", ackID, err)
		}
	}
	seen := make(map[int]string)
	for _, item := range checkpointTestCaptureEvents(unmatchedCapture, "checkpoint_blob_result") {
		seen[checkpointTestFieldInt(item.Event, "checkpoint_request_id")] = checkpointTestFieldString(item.Event, "checkpoint_result")
	}
	for _, ackID := range []uint32{settledRequestID, neverDispatchedID} {
		if got := seen[int(ackID)]; got != "unmatched" {
			t.Fatalf("unmatched ACK %d result = %q, want unmatched in %#v", ackID, got, unmatchedCapture.captures)
		}
	}
}

func TestCheckpointBlobTimeoutAndCancelEmitSafeEvents(t *testing.T) {
	service, stream, projection := testCheckpointBlobProjection(t)
	capture := &debugRecorderTestCapture{}
	service.debug = newDebugRecorder(t.TempDir(), service.broker, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(service.debug.Close)
	if err := service.queueCheckpointProjection(stream, projection, nil); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	if err := service.handleCheckpointBlobTimeout(stream); err != nil {
		t.Fatalf("handleCheckpointBlobTimeout() error = %v", err)
	}
	timeouts := checkpointTestCaptureEvents(capture, "checkpoint_blob_timeout")
	if len(timeouts) != 1 {
		t.Fatalf("timeout events = %d, want 1", len(timeouts))
	}
	if got := checkpointTestFieldString(timeouts[0].Event, "checkpoint_result"); got != "timeout" {
		t.Fatalf("timeout result = %q", got)
	}
	if checkpointTestFieldInt(timeouts[0].Event, "blob_count") == 0 || timeouts[0].Event.Fields["missing_blob_count"] != timeouts[0].Event.Fields["blob_count"] {
		t.Fatalf("timeout counts = %#v", timeouts[0].Event.Fields)
	}
	if _, leaked := timeouts[0].Event.Fields["missing_blob_keys"]; leaked {
		t.Fatalf("timeout leaked blob keys: %#v", timeouts[0].Event.Fields)
	}

	cancelService, cancelStream, cancelProjection := testCheckpointBlobProjection(t)
	cancelCapture := &debugRecorderTestCapture{}
	cancelService.debug = newDebugRecorder(t.TempDir(), cancelService.broker, debugRecorderTestConfig("basic"), cancelCapture)
	t.Cleanup(cancelService.debug.Close)
	if err := cancelService.queueCheckpointProjection(cancelStream, cancelProjection, nil); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	if err := cancelService.handleCancelIntent(InboundIntent{
		Kind:         "cancel",
		RequestID:    cancelStream.RequestID,
		CancelReason: "user stopped",
	}); err != nil {
		t.Fatalf("handleCancelIntent() error = %v", err)
	}
	cancels := checkpointTestCaptureEvents(cancelCapture, "checkpoint_blob_canceled")
	if len(cancels) != 1 {
		t.Fatalf("cancel events = %d, want 1", len(cancels))
	}
	if got := checkpointTestFieldString(cancels[0].Event, "checkpoint_result"); got != "canceled" {
		t.Fatalf("cancel result = %q", got)
	}
	if _, leaked := cancels[0].Event.Fields["missing_blob_keys"]; leaked {
		t.Fatalf("cancel leaked blob keys: %#v", cancels[0].Event.Fields)
	}
}

func TestCheckpointBlobStaleTimerAfterPublishDoesNotSkipNextPending(t *testing.T) {
	service, stream, projectionA := testCheckpointBlobProjection(t)
	capture := &debugRecorderTestCapture{}
	service.debug = newDebugRecorder(t.TempDir(), service.broker, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(service.debug.Close)

	if err := service.queueCheckpointProjection(stream, projectionA, nil); err != nil {
		t.Fatalf("queue A error = %v", err)
	}
	timerKey := providerTimerKey(streamTimerCheckpointBlobs, "")
	stream.mu.Lock()
	tokenA := stream.TimerTokens[timerKey]
	stream.mu.Unlock()
	if tokenA == 0 {
		t.Fatal("queue A did not schedule a checkpoint blob timer")
	}
	stale := &streamTimerEvent{
		Key:    timerKey,
		Kind:   streamTimerCheckpointBlobs,
		Token:  tokenA,
		Reason: "checkpoint blob write timeout",
	}

	acknowledgeCheckpointBlobs(t, service, stream)
	if timerEventMatches(stream, stale) {
		t.Fatal("stale timer still matches after publishReadyCheckpoint cleared it")
	}

	conversationB := &ConversationFile{
		ConversationID:        "conversation-1",
		RootConversationID:    "conversation-1",
		Mode:                  "agent",
		NextTurnSeq:           2,
		NextEntrySeq:          3,
		TokenDetailsMaxTokens: projectedConversationMaxTokens,
		Entries: []HistoryEntry{
			testCheckpointUserEntry(t),
			newAssistantTextEntry(1, "request-1", "hi-b", "", ""),
		},
	}
	projectionB, err := service.projector.ProjectCheckpointProjection(conversationB)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection B error = %v", err)
	}
	if len(projectionB.Blobs) == 0 {
		t.Fatal("projection B has no blobs")
	}
	stream.mu.Lock()
	confirmedB := 0
	for _, blob := range projectionB.Blobs {
		if _, ok := stream.ConfirmedCheckpointBlobs[string(blob.ID)]; ok {
			confirmedB++
		}
	}
	stream.mu.Unlock()
	if confirmedB == len(projectionB.Blobs) {
		t.Fatal("projection B blobs are already confirmed; queue B would not stay pending")
	}

	if err := service.queueCheckpointProjection(stream, projectionB, nil); err != nil {
		t.Fatalf("queue B error = %v", err)
	}
	stream.mu.Lock()
	pendingBefore := stream.PendingCheckpoint != nil
	writesBefore := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()
	if !pendingBefore || writesBefore == 0 {
		t.Fatalf("queue B pending=%v writes=%d, want pending writes", pendingBefore, writesBefore)
	}

	if err := service.handleTimerEvent(stream, stale); err != nil {
		t.Fatalf("handleTimerEvent(stale A) error = %v", err)
	}

	stream.mu.Lock()
	pendingAfter := stream.PendingCheckpoint != nil
	writesAfter := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()
	if !pendingAfter || writesAfter == 0 {
		t.Fatalf("stale timer skipped B pending=%v writes=%d", pendingAfter, writesAfter)
	}
	if skips := checkpointTestCaptureEvents(capture, "checkpoint_blob_sync_skipped"); len(skips) != 0 {
		t.Fatalf("stale timer emitted skip: %+v", skips)
	}

	acknowledgeCheckpointBlobs(t, service, stream)
	checkpointCount := 0
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if event.Message.GetConversationCheckpointUpdate() != nil {
			checkpointCount++
		}
	}
	if checkpointCount != 2 {
		t.Fatalf("checkpoints after ACK B = %d, want 2", checkpointCount)
	}

	conversationC := &ConversationFile{
		ConversationID:        "conversation-1",
		RootConversationID:    "conversation-1",
		Mode:                  "agent",
		NextTurnSeq:           2,
		NextEntrySeq:          3,
		TokenDetailsMaxTokens: projectedConversationMaxTokens,
		Entries: []HistoryEntry{
			testCheckpointUserEntry(t),
			newAssistantTextEntry(1, "request-1", "hi-c", "", ""),
		},
	}
	projectionC, err := service.projector.ProjectCheckpointProjection(conversationC)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection C error = %v", err)
	}
	if err := service.queueCheckpointProjection(stream, projectionC, nil); err != nil {
		t.Fatalf("queue C error = %v", err)
	}
	stream.mu.Lock()
	tokenC := stream.TimerTokens[timerKey]
	pendingC := stream.PendingCheckpoint != nil
	writesC := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()
	if tokenC == 0 || !pendingC || writesC == 0 {
		t.Fatalf("queue C token=%d pending=%v writes=%d, want a live timer and pending writes", tokenC, pendingC, writesC)
	}
	if err := service.handleTimerEvent(stream, &streamTimerEvent{
		Key:    timerKey,
		Kind:   streamTimerCheckpointBlobs,
		Token:  tokenC,
		Reason: "checkpoint blob write timeout",
	}); err != nil {
		t.Fatalf("handleTimerEvent(current C) error = %v", err)
	}
	stream.mu.Lock()
	pendingAfterTimeout := stream.PendingCheckpoint != nil
	writesAfterTimeout := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()
	if pendingAfterTimeout || writesAfterTimeout != 0 {
		t.Fatalf("current timer did not time out C pending=%v writes=%d", pendingAfterTimeout, writesAfterTimeout)
	}
	checkpointCount = 0
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if event.Message.GetConversationCheckpointUpdate() != nil {
			checkpointCount++
		}
	}
	if checkpointCount != 2 {
		t.Fatalf("checkpoints after current C timeout = %d, want 2", checkpointCount)
	}
}

func checkpointTestCaptureEvents(capture *debugRecorderTestCapture, name string) []observability.Capture {
	if capture == nil {
		return nil
	}
	result := make([]observability.Capture, 0, len(capture.captures))
	for _, item := range capture.captures {
		if item.Event.Event == name {
			result = append(result, item)
		}
	}
	return result
}

func checkpointTestFieldString(event observability.Event, key string) string {
	if event.Fields == nil {
		return ""
	}
	text, _ := event.Fields[key].(string)
	return text
}

func checkpointTestFieldInt(event observability.Event, key string) int {
	if event.Fields == nil {
		return 0
	}
	switch value := event.Fields[key].(type) {
	case int:
		return value
	case int8:
		return int(value)
	case int16:
		return int(value)
	case int32:
		return int(value)
	case int64:
		return int(value)
	case uint:
		return int(value)
	case uint8:
		return int(value)
	case uint16:
		return int(value)
	case uint32:
		return int(value)
	case uint64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func testCheckpointBlobProjection(t *testing.T) (*Service, *ActiveStream, *CheckpointProjection) {
	t.Helper()
	broker := NewStreamBroker()
	service := &Service{
		store:     NewConversationFileStore(t.TempDir()),
		projector: NewHistoryProjector(),
		broker:    broker,
	}
	stream, err := broker.OpenStream(
		"request-1", "conversation-1", 1, "default", "default",
		agentv1.AgentMode_AGENT_MODE_AGENT, "hello",
	)
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	conversation := &ConversationFile{
		ConversationID:        "conversation-1",
		RootConversationID:    "conversation-1",
		Mode:                  "agent",
		NextTurnSeq:           2,
		NextEntrySeq:          3,
		TokenDetailsMaxTokens: projectedConversationMaxTokens,
		Entries: []HistoryEntry{
			testCheckpointUserEntry(t),
			newAssistantTextEntry(1, "request-1", "hi", "", ""),
		},
	}
	projection, err := service.projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}
	return service, stream, projection
}

func testCheckpointUserEntry(t *testing.T) HistoryEntry {
	t.Helper()
	payload, err := protojson.Marshal(&agentv1.UserMessage{Text: "hello", MessageId: "message-1"})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	return HistoryEntry{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: payload}
}

func acknowledgeCheckpointBlobs(t *testing.T, service *Service, stream *ActiveStream) {
	t.Helper()
	for {
		stream.mu.Lock()
		requestIDs := make([]uint32, 0, len(stream.PendingCheckpointBlobWrites))
		for requestID := range stream.PendingCheckpointBlobWrites {
			requestIDs = append(requestIDs, requestID)
		}
		stream.mu.Unlock()
		if len(requestIDs) == 0 {
			return
		}
		for _, requestID := range requestIDs {
			if err := service.handleCheckpointBlobResult(stream, &agentv1.KvClientMessage{
				Id: requestID,
				Message: &agentv1.KvClientMessage_SetBlobResult{
					SetBlobResult: &agentv1.SetBlobResult{},
				},
			}); err != nil {
				t.Fatalf("handleCheckpointBlobResult(%d) error = %v", requestID, err)
			}
		}
	}
}

func readCheckpointTestEvents(t *testing.T, service *Service, stream *ActiveStream) []StreamEvent {
	t.Helper()
	events, err := service.broker.ReadFromCursor(stream.RequestID, 0)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	return events
}

// testNativeCheckpointBlobProjection 复用既有合成镜像，但改用 C0-W1 独立原生投影写出。
// 默认发布入口仍是 ProjectCheckpointProjection；本用例只验证独立接缝经既有队列后端的端到端行为。
func testNativeCheckpointBlobProjection(t *testing.T) (*Service, *ActiveStream, *CheckpointProjection) {
	t.Helper()
	service, stream, _ := testCheckpointBlobProjection(t)
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	projection, err := service.projector.projectNativeCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("projectNativeCheckpointProjection() error = %v", err)
	}
	if projection == nil || projection.State == nil {
		t.Fatal("projectNativeCheckpointProjection() returned nil projection")
	}
	return service, stream, projection
}

// TestNativeCheckpointProjectionAcknowledgementsThroughBidiAppend 固定独立原生投影→既有 Blob 队列→
// 真实 HTTP BidiAppend 确认→终态发布顺序的整链合同。ACK 必须走真实 Connect 客户端与 dispatchInboundIntent，
// 因此 HTTP 与 actor 均真实参与；用例不启动 provider，也不依赖默认运行入口已被切换为原生投影。
func TestNativeCheckpointProjectionAcknowledgementsThroughBidiAppend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	service, stream, projection := testNativeCheckpointBlobProjection(t)
	completion := &pendingTurnCompletion{RequestID: stream.RequestID, Usage: turnUsageSnapshot{InputTokens: 11, OutputTokens: 7}}
	if err := service.queueCheckpointProjectionWithTerminal(stream, projection, successfulCheckpointTerminalAction(completion)); err != nil {
		t.Fatalf("queueCheckpointProjectionWithTerminal() error = %v", err)
	}

	repo := make(map[string][]byte)
	var ackIDs []uint32
	cursor := 0
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		cursor++
		args := event.Message.GetKvServerMessage().GetSetBlobArgs()
		if args == nil {
			t.Fatalf("queued checkpoint blob event = %#v, want SetBlobArgs", event.Message)
		}
		digest := sha256.Sum256(args.GetBlobData())
		if !bytes.Equal(digest[:], args.GetBlobId()) {
			t.Fatal("SetBlob blob id does not match its SHA-256")
		}
		repo[string(args.GetBlobId())] = append([]byte(nil), args.GetBlobData()...)
		ackIDs = append(ackIDs, event.Message.GetKvServerMessage().GetId())
	}
	if len(ackIDs) != len(projection.Blobs) || len(ackIDs) < 2 {
		t.Fatalf("SetBlob writes = %d, want %d (>=2 for a partial acknowledgement)", len(ackIDs), len(projection.Blobs))
	}

	server := httptest.NewServer(connect.NewUnaryHandler("/aiserver.v1.BidiService/BidiAppend", service.BidiAppend))
	defer server.Close()
	client := connect.NewClient[aiserverv1.BidiAppendRequest, aiserverv1.BidiAppendResponse](server.Client(), server.URL+"/aiserver.v1.BidiService/BidiAppend")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	t.Cleanup(func() {
		stream.mu.Lock()
		done, status := stream.ActorDone, stream.Status
		stream.mu.Unlock()
		if done == nil {
			return
		}
		if !isTerminalStreamStatus(status) {
			if err := service.dispatchInboundIntent(InboundIntent{Kind: "cancel", RequestID: stream.RequestID, CancelReason: "test cleanup"}); err != nil {
				t.Errorf("cleanup cancel error = %v", err)
			}
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("stream actor did not stop")
		}
	})

	seqno := int64(0)
	for _, requestID := range ackIDs[:len(ackIDs)-1] {
		seqno++
		nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, requestID, seqno)
		nativeCheckpointAssertNoTerminalEvents(t, service, stream, &cursor)
	}
	seqno++
	nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, ackIDs[len(ackIDs)-1], seqno)

	final, err := service.broker.ReadFromCursor(stream.RequestID, cursor)
	if err != nil {
		t.Fatalf("ReadFromCursor() after final ACK error = %v", err)
	}
	cursor += len(final)
	state := nativeCheckpointAssertTerminalOrder(t, final)
	nativeCheckpointAssertPublishedReferences(t, repo, state)

	// 重复/迟到 ACK 经 HTTP 不再发布终态：终态流忽略上行是既有行为，不应被独立投影改变。
	seqno++
	nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, ackIDs[len(ackIDs)-1], seqno)
	late, err := service.broker.ReadFromCursor(stream.RequestID, cursor)
	if err != nil {
		t.Fatalf("ReadFromCursor() after late ACK error = %v", err)
	}
	for _, event := range late {
		if event.Message.GetConversationCheckpointUpdate() != nil || event.Message.GetInteractionUpdate().GetTurnEnded() != nil || event.End {
			t.Fatalf("late ACK republished terminal state: %#v", event.Message)
		}
	}
}

func nativeCheckpointBidiAck(t *testing.T, ctx context.Context, client *connect.Client[aiserverv1.BidiAppendRequest, aiserverv1.BidiAppendResponse], requestID string, blobRequestID uint32, seqno int64) {
	t.Helper()
	payload, err := proto.Marshal(&agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_KvClientMessage{
		KvClientMessage: &agentv1.KvClientMessage{Id: blobRequestID, Message: &agentv1.KvClientMessage_SetBlobResult{SetBlobResult: &agentv1.SetBlobResult{}}},
	}})
	if err != nil {
		t.Fatalf("marshal checkpoint blob ack: %v", err)
	}
	if _, err := client.CallUnary(ctx, connect.NewRequest(&aiserverv1.BidiAppendRequest{
		RequestId: &aiserverv1.BidiRequestId{RequestId: requestID}, AppendSeqno: seqno, Data: hex.EncodeToString(payload),
	})); err != nil {
		t.Fatalf("BidiAppend checkpoint blob ack seqno=%d: %v", seqno, err)
	}
}

func nativeCheckpointAssertNoTerminalEvents(t *testing.T, service *Service, stream *ActiveStream, cursor *int) {
	t.Helper()
	events, err := service.broker.ReadFromCursor(stream.RequestID, *cursor)
	if err != nil {
		t.Fatalf("ReadFromCursor() error = %v", err)
	}
	*cursor += len(events)
	for _, event := range events {
		if event.Message.GetConversationCheckpointUpdate() != nil || event.Message.GetInteractionUpdate().GetTurnEnded() != nil || event.End {
			t.Fatalf("terminal state published after only a partial Blob acknowledgement: %#v", event.Message)
		}
	}
}

func nativeCheckpointAssertTerminalOrder(t *testing.T, events []StreamEvent) *agentv1.ConversationStateStructure {
	t.Helper()
	var state *agentv1.ConversationStateStructure
	order := make([]string, 0, 3)
	for _, event := range events {
		switch {
		case event.Message.GetConversationCheckpointUpdate() != nil:
			state = event.Message.GetConversationCheckpointUpdate()
			order = append(order, "checkpoint")
		case event.Message.GetInteractionUpdate().GetTurnEnded() != nil:
			order = append(order, "turn_ended")
		case event.End:
			order = append(order, "end")
		}
	}
	if len(order) != 3 || order[0] != "checkpoint" || order[1] != "turn_ended" || order[2] != "end" {
		t.Fatalf("terminal event order = %v, want exactly checkpoint -> turn_ended -> end", order)
	}
	if state == nil {
		t.Fatal("checkpoint event carries no state")
	}
	return state
}

// nativeCheckpointCoreMessage 是本测试独立维护的最小原生 CoreMessage 视图，只读取 role/content。
type nativeCheckpointCoreMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

func nativeCheckpointBlob(t *testing.T, repo map[string][]byte, reference []byte, label string) []byte {
	t.Helper()
	if len(reference) != sha256.Size {
		t.Fatalf("%s reference length = %d, want %d", label, len(reference), sha256.Size)
	}
	data, ok := repo[string(reference)]
	if !ok {
		t.Fatalf("%s reference %x is missing from the acknowledged client repository", label, reference)
	}
	if digest := sha256.Sum256(data); !bytes.Equal(digest[:], reference) {
		t.Fatalf("%s reference does not match its stored SHA-256", label)
	}
	return data
}

// nativeCheckpointAssertPublishedReferences 从已确认的客户端仓库逐项解析发布 state 的 root 与
// turn/user/step 子引用：原生 root 为纯 role/content JSON，turn/user/step 为对应 protobuf 类型。
func nativeCheckpointAssertPublishedReferences(t *testing.T, repo map[string][]byte, state *agentv1.ConversationStateStructure) {
	t.Helper()
	root := state.GetRootPromptMessagesJson()
	if len(root) != 2 {
		t.Fatalf("native root messages = %d, want user hello and assistant hi", len(root))
	}
	roles := make([]string, 0, len(root))
	for index, reference := range root {
		var message nativeCheckpointCoreMessage
		if err := json.Unmarshal(nativeCheckpointBlob(t, repo, reference, fmt.Sprintf("native root message %d", index)), &message); err != nil {
			t.Fatalf("native root message %d decode error = %v", index, err)
		}
		var content string
		if err := json.Unmarshal(message.Content, &content); err != nil {
			t.Fatalf("native root message %d content is not a JSON string: %v", index, err)
		}
		switch message.Role {
		case "user":
			if !strings.Contains(content, "hello") {
				t.Fatalf("native root user content = %q, want it to contain hello", content)
			}
		case "assistant":
			if content != "hi" {
				t.Fatalf("native root assistant content = %q, want hi", content)
			}
		default:
			t.Fatalf("native root message %d role = %q, want user or assistant", index, message.Role)
		}
		roles = append(roles, message.Role)
	}
	if roles[0] != "user" || roles[1] != "assistant" {
		t.Fatalf("native root roles = %v, want user then assistant", roles)
	}

	if len(state.GetTurns()) != 1 {
		t.Fatalf("published state turns = %d, want 1", len(state.GetTurns()))
	}
	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(nativeCheckpointBlob(t, repo, state.GetTurns()[0], "turn 0"), turn); err != nil {
		t.Fatalf("turn 0 is not a ConversationTurnStructure: %v", err)
	}
	agentTurn := turn.GetAgentConversationTurn()
	if agentTurn == nil {
		t.Fatal("turn 0 has no agent conversation turn")
	}
	user := &agentv1.UserMessage{}
	if err := proto.Unmarshal(nativeCheckpointBlob(t, repo, agentTurn.GetUserMessage(), "turn 0 user"), user); err != nil {
		t.Fatalf("turn 0 user reference is not a UserMessage: %v", err)
	}
	if !strings.Contains(user.GetText(), "hello") {
		t.Fatalf("turn 0 user text = %q, want it to contain hello", user.GetText())
	}
	steps := agentTurn.GetSteps()
	if len(steps) != 1 {
		t.Fatalf("turn 0 steps = %d, want 1", len(steps))
	}
	for index, reference := range steps {
		step := &agentv1.ConversationStep{}
		if err := proto.Unmarshal(nativeCheckpointBlob(t, repo, reference, fmt.Sprintf("turn 0 step %d", index)), step); err != nil {
			t.Fatalf("turn 0 step %d is not a ConversationStep: %v", index, err)
		}
	}
}

// testNativeStructuredCheckpointProjection 在既有合成快照上追加标准 JSON runtime_state，
// 经独立原生接缝投影出 plan/todos raw32 引用，并返回期望的 plan 正文与 A/B/A 待办实例。
func testNativeStructuredCheckpointProjection(t *testing.T) (*Service, *ActiveStream, *CheckpointProjection, string, []*agentv1.TodoItem) {
	t.Helper()
	service, stream, _ := testCheckpointBlobProjection(t)
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	planText := "结构化计划：先核对原生引用，再保留重复的待办实例。"
	todoA := &agentv1.TodoItem{
		Id:           "todo-a",
		Content:      "读取预取的结构化引用",
		Status:       agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS,
		CreatedAt:    9007199254740993,
		UpdatedAt:    9223372036854775807,
		Dependencies: []string{"todo-root"},
	}
	todoB := &agentv1.TodoItem{
		Id:           "todo-b",
		Content:      "复核失败保全",
		Status:       agentv1.TodoStatus_TODO_STATUS_PENDING,
		CreatedAt:    1,
		UpdatedAt:    9007199254740993,
		Dependencies: []string{"todo-a", "todo-root"},
	}
	payload, err := json.Marshal(runtimeStateEntryPayload{
		PlanText: planText,
		Plans:    map[string]*agentv1.PlanRegistryEntry{"current": {Id: "current", Path: "plan://synthetic/structured"}},
		Todos:    []*agentv1.TodoItem{todoA, todoB, todoA},
	})
	if err != nil {
		t.Fatalf("marshal runtime_state payload: %v", err)
	}
	conversation.Entries = append(conversation.Entries, HistoryEntry{Kind: "runtime_state", Role: "system", Payload: payload})
	projection, err := service.projector.projectNativeCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("projectNativeCheckpointProjection() error = %v", err)
	}
	if projection == nil || projection.State == nil {
		t.Fatal("projectNativeCheckpointProjection() returned nil projection")
	}
	return service, stream, projection, planText, []*agentv1.TodoItem{todoA, todoB, todoA}
}

// TestNativeCheckpointStructuredAcknowledgementsThroughBidiAppend 固定 §19.3e C0-W2 的结构化
// plan/todos raw32 引用经既有 Blob 队列与真实 HTTP BidiAppend 确认后才发布终态的整链合同。
// 至少保留一条结构化 Todo 正文到最后确认；全部其他 Blob（含一次重复 ACK）先上行且不得发布。
func TestNativeCheckpointStructuredAcknowledgementsThroughBidiAppend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	service, stream, projection, planText, wantTodos := testNativeStructuredCheckpointProjection(t)
	completion := &pendingTurnCompletion{RequestID: stream.RequestID, Usage: turnUsageSnapshot{InputTokens: 11, OutputTokens: 7}}
	if err := service.queueCheckpointProjectionWithTerminal(stream, projection, successfulCheckpointTerminalAction(completion)); err != nil {
		t.Fatalf("queueCheckpointProjectionWithTerminal() error = %v", err)
	}

	repo := make(map[string][]byte)
	requestIDByKey := make(map[string]uint32)
	var ackIDs []uint32
	cursor := 0
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		cursor++
		args := event.Message.GetKvServerMessage().GetSetBlobArgs()
		if args == nil {
			t.Fatalf("queued checkpoint blob event = %#v, want SetBlobArgs", event.Message)
		}
		digest := sha256.Sum256(args.GetBlobData())
		if !bytes.Equal(digest[:], args.GetBlobId()) {
			t.Fatal("SetBlob blob id does not match its SHA-256")
		}
		key := string(args.GetBlobId())
		repo[key] = append([]byte(nil), args.GetBlobData()...)
		requestIDByKey[key] = event.Message.GetKvServerMessage().GetId()
		ackIDs = append(ackIDs, event.Message.GetKvServerMessage().GetId())
	}
	if len(ackIDs) != len(projection.Blobs) || len(ackIDs) < 2 {
		t.Fatalf("SetBlob writes = %d, want %d (>=2)", len(ackIDs), len(projection.Blobs))
	}
	nativeCheckpointAssertStructuredState(t, repo, projection.State, planText, wantTodos)

	todoBody, err := proto.Marshal(wantTodos[0])
	if err != nil {
		t.Fatalf("marshal todo A body: %v", err)
	}
	retainKey := sha256.Sum256(todoBody)
	retainID, ok := requestIDByKey[string(retainKey[:])]
	if !ok || retainID == 0 {
		t.Fatalf("retained todo A body SetBlob was never dispatched: key=%x", retainKey)
	}

	server := httptest.NewServer(connect.NewUnaryHandler("/aiserver.v1.BidiService/BidiAppend", service.BidiAppend))
	defer server.Close()
	client := connect.NewClient[aiserverv1.BidiAppendRequest, aiserverv1.BidiAppendResponse](server.Client(), server.URL+"/aiserver.v1.BidiService/BidiAppend")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		stream.mu.Lock()
		done, status := stream.ActorDone, stream.Status
		stream.mu.Unlock()
		if done == nil {
			return
		}
		if !isTerminalStreamStatus(status) {
			if err := service.dispatchInboundIntent(InboundIntent{Kind: "cancel", RequestID: stream.RequestID, CancelReason: "test cleanup"}); err != nil {
				t.Errorf("cleanup cancel error = %v", err)
			}
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("stream actor did not stop")
		}
	})

	seqno := int64(0)
	repeated := false
	for _, requestID := range ackIDs {
		if requestID == retainID {
			continue
		}
		seqno++
		nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, requestID, seqno)
		if !repeated {
			repeated = true
			seqno++
			nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, requestID, seqno)
		}
		nativeCheckpointAssertNoTerminalEvents(t, service, stream, &cursor)
	}

	seqno++
	nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, retainID, seqno)
	final, err := service.broker.ReadFromCursor(stream.RequestID, cursor)
	if err != nil {
		t.Fatalf("ReadFromCursor() after final ACK error = %v", err)
	}
	cursor += len(final)
	state := nativeCheckpointAssertTerminalOrder(t, final)
	nativeCheckpointAssertStructuredState(t, repo, state, planText, wantTodos)

	seqno++
	nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, retainID, seqno)
	late, err := service.broker.ReadFromCursor(stream.RequestID, cursor)
	if err != nil {
		t.Fatalf("ReadFromCursor() after late ACK error = %v", err)
	}
	for _, event := range late {
		if event.Message.GetConversationCheckpointUpdate() != nil || event.Message.GetInteractionUpdate().GetTurnEnded() != nil || event.End {
			t.Fatalf("late ACK republished terminal state: %#v", event.Message)
		}
	}
}

// nativeCheckpointAssertStructuredState 用已确认仓库解析 state.Plan/Todos raw32 引用，
// 逐字段比较 plan 与 3 个 Todo 实例（含重复引用），并确认 Plans map 仍内联。
func nativeCheckpointAssertStructuredState(t *testing.T, repo map[string][]byte, state *agentv1.ConversationStateStructure, planText string, wantTodos []*agentv1.TodoItem) {
	t.Helper()
	plan := &agentv1.ConversationPlan{}
	if err := proto.Unmarshal(nativeCheckpointBlob(t, repo, state.GetPlan(), "state plan"), plan); err != nil {
		t.Fatalf("State.Plan is not a ConversationPlan: %v", err)
	}
	if plan.GetPlan() != planText {
		t.Fatalf("State.Plan text = %q, want %q", plan.GetPlan(), planText)
	}
	refs := state.GetTodos()
	if len(refs) != len(wantTodos) {
		t.Fatalf("State.Todos count = %d, want %d", len(refs), len(wantTodos))
	}
	for index, want := range wantTodos {
		item := &agentv1.TodoItem{}
		if err := proto.Unmarshal(nativeCheckpointBlob(t, repo, refs[index], fmt.Sprintf("state todo %d", index)), item); err != nil {
			t.Fatalf("State.Todos[%d] is not a TodoItem: %v", index, err)
		}
		if !proto.Equal(item, want) {
			t.Fatalf("State.Todos[%d] = %v, want %v", index, item, want)
		}
	}
	if !bytes.Equal(refs[0], refs[2]) {
		t.Fatal("duplicate todo instances must share one content reference")
	}
	if plans := state.GetPlans(); plans["current"] == nil || plans["current"].GetPath() != "plan://synthetic/structured" {
		t.Fatalf("State.Plans map is no longer inline: %#v", plans)
	}
}

// TestNativeCheckpointImportedTurnGraphSetBlobIncludesOriginalNodes 独立 native 投影在有内容源时
// 必须把导入回合图原字节送入既有 SetBlob 队列，并经真实 HTTP BidiAppend ACK 后才发布唯一终态。
// 默认发布入口仍不切换；本用例不启动 provider。
func TestNativeCheckpointImportedTurnGraphSetBlobIncludesOriginalNodes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	graph := newTestImportedTurnGraph(t)
	historyRoot := t.TempDir()
	broker := NewStreamBroker()
	service := &Service{
		store:        NewConversationFileStore(historyRoot),
		contentBlobs: NewContentBlobStore(historyRoot),
		projector:    NewHistoryProjector(),
		broker:       broker,
	}
	conversation, err := newRuntimeConversation("conversation-imported-graph", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := service.importConversationState(conversation, graph.state, graph.blobs)
	if err != nil {
		t.Fatalf("importConversationState() error = %v", err)
	}
	appendEntriesInPlace(conversation, entries)
	stream, err := broker.OpenStream("request-imported-graph", conversation.ConversationID, 1, "default", "default", agentv1.AgentMode_AGENT_MODE_AGENT, "hello")
	if err != nil {
		t.Fatalf("OpenStream() error = %v", err)
	}
	if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}
	projection, err := service.projector.projectNativeCheckpointProjection(conversation, service.contentBlobs)
	if err != nil {
		t.Fatalf("projectNativeCheckpointProjection() error = %v", err)
	}
	completion := &pendingTurnCompletion{RequestID: stream.RequestID, Usage: turnUsageSnapshot{InputTokens: 11, OutputTokens: 7}}
	if err := service.queueCheckpointProjectionWithTerminal(stream, projection, successfulCheckpointTerminalAction(completion)); err != nil {
		t.Fatalf("queueCheckpointProjectionWithTerminal() error = %v", err)
	}

	repo := make(map[string][]byte)
	requestIDByKey := make(map[string]uint32)
	var ackIDs []uint32
	cursor := 0
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		cursor++
		args := event.Message.GetKvServerMessage().GetSetBlobArgs()
		if args == nil {
			t.Fatalf("queued checkpoint blob event = %#v, want SetBlobArgs", event.Message)
		}
		digest := sha256.Sum256(args.GetBlobData())
		if !bytes.Equal(digest[:], args.GetBlobId()) {
			t.Fatal("SetBlob blob id does not match its SHA-256")
		}
		key := string(args.GetBlobId())
		repo[key] = append([]byte(nil), args.GetBlobData()...)
		requestIDByKey[key] = event.Message.GetKvServerMessage().GetId()
		ackIDs = append(ackIDs, event.Message.GetKvServerMessage().GetId())
	}
	for _, want := range []struct {
		name string
		id   []byte
		body []byte
	}{
		{"turn", graph.turnID, graph.turnBody},
		{"user", graph.userID, graph.userBody},
		{"step", graph.stepID, graph.stepBody},
	} {
		if !bytes.Equal(repo[string(want.id)], want.body) {
			t.Fatalf("SetBlob 缺少原%s正文", want.name)
		}
	}
	if len(ackIDs) != len(projection.Blobs) || len(ackIDs) < 2 {
		t.Fatalf("SetBlob writes = %d, want %d (>=2)", len(ackIDs), len(projection.Blobs))
	}
	retainID, ok := requestIDByKey[string(graph.userID)]
	if !ok || retainID == 0 {
		t.Fatalf("retained imported user SetBlob was never dispatched: key=%x", graph.userID)
	}

	server := httptest.NewServer(connect.NewUnaryHandler("/aiserver.v1.BidiService/BidiAppend", service.BidiAppend))
	defer server.Close()
	client := connect.NewClient[aiserverv1.BidiAppendRequest, aiserverv1.BidiAppendResponse](server.Client(), server.URL+"/aiserver.v1.BidiService/BidiAppend")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		stream.mu.Lock()
		done, status := stream.ActorDone, stream.Status
		stream.mu.Unlock()
		if done == nil {
			return
		}
		if !isTerminalStreamStatus(status) {
			if err := service.dispatchInboundIntent(InboundIntent{Kind: "cancel", RequestID: stream.RequestID, CancelReason: "test cleanup"}); err != nil {
				t.Errorf("cleanup cancel error = %v", err)
			}
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("stream actor did not stop")
		}
	})

	seqno := int64(0)
	repeated := false
	for _, requestID := range ackIDs {
		if requestID == retainID {
			continue
		}
		seqno++
		nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, requestID, seqno)
		if !repeated {
			repeated = true
			seqno++
			nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, requestID, seqno)
		}
		nativeCheckpointAssertNoTerminalEvents(t, service, stream, &cursor)
	}

	seqno++
	nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, retainID, seqno)
	final, err := service.broker.ReadFromCursor(stream.RequestID, cursor)
	if err != nil {
		t.Fatalf("ReadFromCursor() after final ACK error = %v", err)
	}
	cursor += len(final)
	state := nativeCheckpointAssertTerminalOrder(t, final)
	nativeCheckpointAssertImportedTurnGraph(t, repo, state, graph)

	seqno++
	nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, retainID, seqno)
	late, err := service.broker.ReadFromCursor(stream.RequestID, cursor)
	if err != nil {
		t.Fatalf("ReadFromCursor() after late ACK error = %v", err)
	}
	for _, event := range late {
		if event.Message.GetConversationCheckpointUpdate() != nil || event.Message.GetInteractionUpdate().GetTurnEnded() != nil || event.End {
			t.Fatalf("late ACK republished terminal state: %#v", event.Message)
		}
	}
}

func nativeCheckpointAssertImportedTurnGraph(t *testing.T, repo map[string][]byte, state *agentv1.ConversationStateStructure, graph testImportedTurnGraph) {
	t.Helper()
	if !bytes.Equal(repo[string(graph.turnID)], graph.turnBody) || !bytes.Equal(repo[string(graph.userID)], graph.userBody) || !bytes.Equal(repo[string(graph.stepID)], graph.stepBody) {
		t.Fatal("published checkpoint missing original imported turn/user/step bytes")
	}
	turns := state.GetTurns()
	if len(turns) == 0 {
		t.Fatal("published checkpoint has no Turns")
	}
	foundImported := 0
	for index, turnID := range turns {
		turn := &agentv1.ConversationTurnStructure{}
		if err := proto.Unmarshal(nativeCheckpointBlob(t, repo, turnID, fmt.Sprintf("turn %d", index)), turn); err != nil || turn.GetTurn() == nil {
			t.Fatalf("turn %d is not a ConversationTurnStructure: %v", index, err)
		}
		if string(turnID) == string(graph.turnID) {
			foundImported++
			agentTurn := turn.GetAgentConversationTurn()
			if agentTurn == nil {
				t.Fatal("imported turn has no agent conversation turn")
			}
			if !bytes.Equal(agentTurn.GetUserMessage(), graph.userID) {
				t.Fatal("imported turn user_message is not the original user id")
			}
			user := &agentv1.UserMessage{}
			if err := proto.Unmarshal(nativeCheckpointBlob(t, repo, agentTurn.GetUserMessage(), "imported user"), user); err != nil {
				t.Fatalf("imported user is not a UserMessage: %v", err)
			}
			steps := agentTurn.GetSteps()
			if len(steps) != 2 || len(steps[0]) != 0 {
				t.Fatal("imported empty step slot was dropped")
			}
			step := &agentv1.ConversationStep{}
			if err := proto.Unmarshal(nativeCheckpointBlob(t, repo, steps[1], "imported step"), step); err != nil || step.GetMessage() == nil {
				t.Fatalf("imported step is not a ConversationStep: %v", err)
			}
		}
	}
	if foundImported < 2 {
		t.Fatalf("published Turns kept %d imported duplicates, want 2", foundImported)
	}
}

func TestNativeCheckpointImportedSummaryGraphAcknowledgementsThroughBidiAppend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	graph := newTestImportedSummaryGraph(t)
	historyRoot := t.TempDir()
	broker := NewStreamBroker()
	service := &Service{
		store:        NewConversationFileStore(historyRoot),
		contentBlobs: NewContentBlobStore(historyRoot),
		projector:    NewHistoryProjector(),
		broker:       broker,
	}
	conversation, err := newRuntimeConversation("conversation-imported-summary", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := service.importConversationState(conversation, graph.state, graph.blobs)
	if err != nil {
		t.Fatalf("importConversationState() error = %v", err)
	}
	appendEntriesInPlace(conversation, entries)
	stream, err := broker.OpenStream("request-imported-summary", conversation.ConversationID, 1, "default", "default", agentv1.AgentMode_AGENT_MODE_AGENT, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
		t.Fatal(err)
	}
	projection, err := service.projector.projectNativeCheckpointProjection(conversation, service.contentBlobs)
	if err != nil {
		t.Fatalf("projectNativeCheckpointProjection() error = %v（旧实现语义 RED）", err)
	}
	completion := &pendingTurnCompletion{RequestID: stream.RequestID, Usage: turnUsageSnapshot{InputTokens: 11, OutputTokens: 7}}
	if err := service.queueCheckpointProjectionWithTerminal(stream, projection, successfulCheckpointTerminalAction(completion)); err != nil {
		t.Fatal(err)
	}
	repo := make(map[string][]byte)
	requestIDByKey := make(map[string]uint32)
	var ackIDs []uint32
	cursor := 0
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		cursor++
		args := event.Message.GetKvServerMessage().GetSetBlobArgs()
		if args == nil {
			t.Fatalf("queued checkpoint blob event = %#v, want SetBlobArgs", event.Message)
		}
		key := string(args.GetBlobId())
		repo[key] = append([]byte(nil), args.GetBlobData()...)
		requestIDByKey[key] = event.Message.GetKvServerMessage().GetId()
		ackIDs = append(ackIDs, event.Message.GetKvServerMessage().GetId())
	}
	for _, want := range []struct {
		name string
		id   []byte
		body []byte
	}{
		{"summary", graph.summaryID, graph.summaryBody},
		{"archive", graph.archiveID, graph.archiveBody},
		{"archive-child", graph.msgUserID, graph.msgUser},
	} {
		if !bytes.Equal(repo[string(want.id)], want.body) {
			t.Fatalf("SetBlob 缺少原%s正文", want.name)
		}
	}
	retainID, ok := requestIDByKey[string(graph.msgUserID)]
	if !ok || retainID == 0 {
		t.Fatal("retained archive child SetBlob was never dispatched")
	}
	server := httptest.NewServer(connect.NewUnaryHandler("/aiserver.v1.BidiService/BidiAppend", service.BidiAppend))
	defer server.Close()
	client := connect.NewClient[aiserverv1.BidiAppendRequest, aiserverv1.BidiAppendResponse](server.Client(), server.URL+"/aiserver.v1.BidiService/BidiAppend")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		stream.mu.Lock()
		done, status := stream.ActorDone, stream.Status
		stream.mu.Unlock()
		if done == nil {
			return
		}
		if !isTerminalStreamStatus(status) {
			if err := service.dispatchInboundIntent(InboundIntent{Kind: "cancel", RequestID: stream.RequestID, CancelReason: "test cleanup"}); err != nil {
				t.Errorf("cleanup cancel error = %v", err)
			}
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("stream actor did not stop")
		}
	})
	seqno := int64(0)
	repeated := false
	for _, requestID := range ackIDs {
		if requestID == retainID {
			continue
		}
		seqno++
		nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, requestID, seqno)
		if !repeated {
			repeated = true
			seqno++
			nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, requestID, seqno)
		}
		nativeCheckpointAssertNoTerminalEvents(t, service, stream, &cursor)
	}
	seqno++
	nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, retainID, seqno)
	final, err := service.broker.ReadFromCursor(stream.RequestID, cursor)
	if err != nil {
		t.Fatal(err)
	}
	cursor += len(final)
	state := nativeCheckpointAssertTerminalOrder(t, final)
	if !bytes.Equal(state.GetSummary(), graph.summaryID) || !bytes.Equal(state.GetSummaryArchive(), graph.archiveID) {
		t.Fatal("终态未发布原摘要/归档父 ID")
	}
	seqno++
	nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, retainID, seqno)
	late, err := service.broker.ReadFromCursor(stream.RequestID, cursor)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range late {
		if event.Message.GetConversationCheckpointUpdate() != nil || event.Message.GetInteractionUpdate().GetTurnEnded() != nil || event.End {
			t.Fatalf("late ACK republished terminal state: %#v", event.Message)
		}
	}
}

func TestNativeCheckpointLocalArchiveChildAcknowledgementsThroughBidiAppend(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	conversation := compactionAppendOnlyConversation(t)
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil || plan == nil {
		t.Fatalf("build plan error = %v", err)
	}
	if err := applyCompactionToConversation(conversation, newPendingCompaction(plan), "ack summary"); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	historyRoot := t.TempDir()
	broker := NewStreamBroker()
	service := &Service{
		store:     NewConversationFileStore(historyRoot),
		projector: NewHistoryProjector(),
		broker:    broker,
	}
	if _, err := service.store.SaveConversationWithEntries(conversation.ConversationID, conversation, conversation.Entries); err != nil {
		t.Fatal(err)
	}
	stream, err := broker.OpenStream("request-local-archive-ack", conversation.ConversationID, 1, "default", "default", agentv1.AgentMode_AGENT_MODE_AGENT, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
		t.Fatal(err)
	}
	projection, err := service.projector.projectNativeCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("projectNativeCheckpointProjection() error = %v", err)
	}
	completion := &pendingTurnCompletion{RequestID: stream.RequestID, Usage: turnUsageSnapshot{InputTokens: 11, OutputTokens: 7}}
	if err := service.queueCheckpointProjectionWithTerminal(stream, projection, successfulCheckpointTerminalAction(completion)); err != nil {
		t.Fatal(err)
	}
	repo := make(map[string][]byte)
	requestIDByKey := make(map[string]uint32)
	var ackIDs []uint32
	cursor := 0
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		cursor++
		args := event.Message.GetKvServerMessage().GetSetBlobArgs()
		if args == nil {
			t.Fatalf("queued checkpoint blob event = %#v, want SetBlobArgs", event.Message)
		}
		key := string(args.GetBlobId())
		repo[key] = append([]byte(nil), args.GetBlobData()...)
		requestIDByKey[key] = event.Message.GetKvServerMessage().GetId()
		ackIDs = append(ackIDs, event.Message.GetKvServerMessage().GetId())
	}
	archives := projection.State.GetSummaryArchives()
	if len(archives) != 1 {
		t.Fatalf("archives=%d, want 1", len(archives))
	}
	archive := &agentv1.ConversationSummaryArchive{}
	if proto.Unmarshal(repo[string(archives[0])], archive) != nil || len(archive.GetSummarizedMessages()) == 0 {
		t.Fatal("缺少本地 archive 子节点")
	}
	retainID := requestIDByKey[string(archive.GetSummarizedMessages()[0])]
	if retainID == 0 {
		t.Fatal("retained archive child SetBlob was never dispatched")
	}
	server := httptest.NewServer(connect.NewUnaryHandler("/aiserver.v1.BidiService/BidiAppend", service.BidiAppend))
	defer server.Close()
	client := connect.NewClient[aiserverv1.BidiAppendRequest, aiserverv1.BidiAppendResponse](server.Client(), server.URL+"/aiserver.v1.BidiService/BidiAppend")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	t.Cleanup(func() {
		stream.mu.Lock()
		done, status := stream.ActorDone, stream.Status
		stream.mu.Unlock()
		if done == nil {
			return
		}
		if !isTerminalStreamStatus(status) {
			if err := service.dispatchInboundIntent(InboundIntent{Kind: "cancel", RequestID: stream.RequestID, CancelReason: "test cleanup"}); err != nil {
				t.Errorf("cleanup cancel error = %v", err)
			}
		}
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("stream actor did not stop")
		}
	})
	seqno := int64(0)
	repeated := false
	for _, requestID := range ackIDs {
		if requestID == retainID {
			continue
		}
		seqno++
		nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, requestID, seqno)
		if !repeated {
			repeated = true
			seqno++
			nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, requestID, seqno)
		}
		nativeCheckpointAssertNoTerminalEvents(t, service, stream, &cursor)
	}
	seqno++
	nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, retainID, seqno)
	final, err := service.broker.ReadFromCursor(stream.RequestID, cursor)
	if err != nil {
		t.Fatal(err)
	}
	cursor += len(final)
	state := nativeCheckpointAssertTerminalOrder(t, final)
	if len(state.GetSummaryArchives()) != 1 || !bytes.Equal(state.GetSummaryArchives()[0], archives[0]) {
		t.Fatal("终态未发布本地 archive")
	}
	seqno++
	nativeCheckpointBidiAck(t, ctx, client, stream.RequestID, retainID, seqno)
	late, err := service.broker.ReadFromCursor(stream.RequestID, cursor)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range late {
		if event.Message.GetConversationCheckpointUpdate() != nil || event.Message.GetInteractionUpdate().GetTurnEnded() != nil || event.End {
			t.Fatalf("late ACK republished terminal state: %#v", event.Message)
		}
	}
}
