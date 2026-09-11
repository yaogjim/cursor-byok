package forwarder

import (
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
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
