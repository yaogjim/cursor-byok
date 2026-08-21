package forwarder

import (
	"context"
	"errors"
	"testing"
	"time"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/observability"
)

func TestProviderStreamStatsRequireCompletionMarker(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderPassCount = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "streaming"}
	stream.mu.Unlock()

	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "partial", OccurredAt: time.Now()}); err != nil {
		t.Fatalf("applyProviderModelEvent() error = %v", err)
	}
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	stats := stream.ProviderStreamStats
	stream.mu.Unlock()
	if stats.CompletionMarker {
		t.Fatal("missing completion marker recorded as completed")
	}
	if stats.ProtocolFinalStatus != "truncated" {
		t.Fatalf("protocol final status = %q, want truncated", stats.ProtocolFinalStatus)
	}
	if stats.ErrorCategory != modeladapter.ProviderErrorStreamDecode {
		t.Fatalf("error category = %q, want %q", stats.ErrorCategory, modeladapter.ProviderErrorStreamDecode)
	}
	if stats.VisibleTextBytes != len("partial") {
		t.Fatalf("visible bytes = %d, want %d", stats.VisibleTextBytes, len("partial"))
	}
}

func TestProviderStatsCompletionAndCancellationAttribution(t *testing.T) {
	stats := ProviderStreamStats{}
	observeProviderModelEvent(&stats, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, Provider: "openai", Model: "test", OccurredAt: time.Now()})
	if !stats.CompletionMarker || stats.Provider != "openai" || stats.Model != "test" {
		t.Fatalf("completion stats = %#v", stats)
	}
	if got := providerTerminalAttribution(context.Canceled); got != "client" {
		t.Fatalf("canceled attribution = %q, want client", got)
	}
	if got := providerTerminalAttribution(context.DeadlineExceeded); got != "deadline" {
		t.Fatalf("deadline attribution = %q, want deadline", got)
	}
}

func TestPartialToolEventDoesNotDispatch(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	stream.mu.Lock()
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderStreamStats.ToolDispatchState = "not_dispatched"
	stream.mu.Unlock()
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind:       modeladapter.ModelEventKindPartialToolCall,
		ToolCallID: "tool-1",
		ToolCall:   &agentv1.ToolCall{},
	}); err != nil {
		t.Fatalf("applyProviderModelEvent() error = %v", err)
	}
	stream.mu.Lock()
	partial := len(stream.PartialToolCallIDs)
	pending := len(stream.PendingExecs) + len(stream.PendingInteractions)
	stats := stream.ProviderStreamStats
	stream.mu.Unlock()
	if partial != 1 || pending != 0 {
		t.Fatalf("partial=%d pending dispatches=%d, want partial recorded without dispatch", partial, pending)
	}
	if stats.PartialToolCount != 1 || stats.DispatchedToolCount != 0 || stats.ToolDispatchState != "partial_not_dispatched" || !stats.DownstreamPublished {
		t.Fatalf("partial tool stats = %#v", stats)
	}
}

func TestProviderAndModelFinalAreSeparateAndIdempotent(t *testing.T) {
	service, stream, capture := providerTerminalCaptureFixture(t)
	startedAt := time.Now().UTC().Add(-1500 * time.Millisecond)
	stream.mu.Lock()
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderStreamStats = ProviderStreamStats{
		Provider:            "openai",
		Model:               "gpt-test",
		Attempt:             1,
		ProtocolFinalStatus: "completed",
		CompletionMarker:    true,
		HTTPStatus:          "not_recorded",
		StartedAt:           startedAt,
		FinishedAt:          startedAt.Add(1500 * time.Millisecond),
	}
	stream.mu.Unlock()

	service.recordProviderTerminal(stream)
	service.recordProviderTerminal(stream)
	if got := countCapturedEvent(capture, "provider_stream_finished"); got != 1 {
		t.Fatalf("provider_stream_finished count = %d, want 1", got)
	}
	providerFinal := capturedEventByName(t, capture, "provider_stream_finished")
	if providerFinal.Event.SemanticOutcome != "succeeded" || providerFinal.Event.Status != "completed" || providerFinal.Event.ErrorCategory != "" || providerFinal.Event.DurationMS != 1500 {
		t.Fatalf("provider final event = %+v", providerFinal.Event)
	}
	providerPayload, ok := providerFinal.Payload.Data.(map[string]any)
	if !ok || providerPayload["status"] != "completed" {
		t.Fatalf("provider final payload = %#v, want status=completed", providerFinal.Payload.Data)
	}
	stream.mu.Lock()
	_, providerFinalizedModelCall := stream.FinalizedModelCallIDs["model-call-1"]
	stream.mu.Unlock()
	if providerFinalizedModelCall {
		t.Fatal("provider protocol terminal prematurely finalized model call")
	}

	service.recordModelCallFinal(stream, "succeeded")
	service.recordModelCallFinal(stream, "failed")
	if got := countCapturedEvent(capture, "model_call_final"); got != 1 {
		t.Fatalf("model_call_final count = %d, want 1", got)
	}
	final := capturedEventByName(t, capture, "model_call_final")
	if final.Event.SemanticOutcome != "succeeded" || final.Event.Status != "succeeded" {
		t.Fatalf("model final event = %+v", final.Event)
	}
	finalPayload, ok := final.Payload.Data.(map[string]any)
	if !ok || finalPayload["status"] != "succeeded" || finalPayload["model_call_final_status"] != "succeeded" {
		t.Fatalf("model final payload = %#v, want succeeded terminal fields", final.Payload.Data)
	}
}

func TestModelCallFinalWaitsForCheckpointTerminal(t *testing.T) {
	service, stream, projection := testCheckpointBlobProjection(t)
	capture := &debugRecorderTestCapture{}
	service.debug = newDebugRecorder(t.TempDir(), service.broker, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(service.debug.Close)
	stream.mu.Lock()
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderStreamStats = ProviderStreamStats{
		Provider:             "openai",
		Model:                "gpt-test",
		Attempt:              1,
		ProtocolFinalStatus:  "completed",
		CompletionMarker:     true,
		HTTPStatus:           "not_recorded",
		ModelCallFinalStatus: "not_finalized",
	}
	stream.mu.Unlock()
	service.recordProviderTerminal(stream)

	completion := &pendingTurnCompletion{
		ConversationID: stream.ConversationID,
		RequestID:      stream.RequestID,
		TurnSeq:        stream.TurnSeq,
		ModelCallID:    "model-call-1",
		ProviderPass:   1,
		Usage:          turnUsageSnapshot{InputTokens: 11, OutputTokens: 7},
	}
	if err := service.queueCheckpointProjection(stream, projection, completion); err != nil {
		t.Fatalf("queueCheckpointProjection() error = %v", err)
	}
	if got := countCapturedEvent(capture, "model_call_final"); got != 0 {
		t.Fatalf("model_call_final count before checkpoint ACK = %d, want 0", got)
	}
	acknowledgeCheckpointBlobs(t, service, stream)
	if got := countCapturedEvent(capture, "model_call_final"); got != 1 {
		t.Fatalf("model_call_final count after checkpoint terminal = %d, want 1", got)
	}
	final := capturedEventByName(t, capture, "model_call_final")
	if final.Event.SemanticOutcome != "succeeded" || final.Event.Status != "succeeded" {
		t.Fatalf("model final event = %+v", final.Event)
	}
}

func TestProviderTerminalRecordsTypedHTTPStatusAndRetryDecision(t *testing.T) {
	service, stream, capture := providerTerminalCaptureFixture(t)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderPassCount = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "streaming", HTTPStatus: "not_recorded"}
	stream.mu.Unlock()

	err := service.handleProviderDoneEvent(stream, &streamProviderEvent{
		Token: 1,
		Done:  true,
		Err: providerTerminalError{cause: &modeladapter.HTTPStatusError{
			Provider:    "openai adapter",
			StatusCode:  429,
			Attempt:     3,
			MaxAttempts: 3,
		}},
	})
	if err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	stats := stream.ProviderStreamStats
	stream.mu.Unlock()
	if stats.HTTPStatus != "429" || stats.HTTPAttempt != 3 || stats.ErrorCategory != modeladapter.ProviderErrorRateLimited || stats.Retryable != "true" || stats.RetryReason != "http_429" {
		t.Fatalf("typed http terminal stats = %#v", stats)
	}
	providerFinal := capturedEventByName(t, capture, "provider_stream_finished")
	payload, ok := providerFinal.Payload.Data.(map[string]any)
	if !ok {
		t.Fatalf("provider final payload type = %T", providerFinal.Payload.Data)
	}
	if payload["http_status"] != "429" || payload["http_attempt"] != 3 || payload["retryable"] != "true" || payload["retry_reason"] != "http_429" {
		t.Fatalf("provider final payload = %#v", payload)
	}
}

func TestProviderTerminalClassifiesErrorDeadlineAndCancel(t *testing.T) {
	tests := []struct {
		name            string
		err             error
		wantProtocol    string
		wantAttribution string
		wantBusiness    string
	}{
		{name: "provider failure", err: errors.New("upstream disconnected"), wantProtocol: "provider_failed", wantAttribution: "provider", wantBusiness: "failed"},
		{name: "deadline", err: context.DeadlineExceeded, wantProtocol: "timeout", wantAttribution: "deadline", wantBusiness: "timeout"},
		{name: "client cancellation", err: context.Canceled, wantProtocol: "canceled", wantAttribution: "client", wantBusiness: "canceled"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			service, stream, capture := providerTerminalCaptureFixture(t)
			stream.mu.Lock()
			stream.CurrentProviderToken = 1
			stream.CurrentModelCallID = "model-call-1"
			stream.ProviderPassCount = 1
			stream.ProviderActive = true
			stream.Status = StreamStatusStreaming
			stream.Phase = TurnPhaseProviderRunning
			stream.ProviderStreamStats = ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "streaming", HTTPStatus: "not_recorded"}
			stream.mu.Unlock()

			err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true, Err: providerTerminalError{cause: test.err}})
			if err != nil {
				t.Fatalf("handleProviderDoneEvent() error = %v", err)
			}
			stream.mu.Lock()
			stats := stream.ProviderStreamStats
			stream.mu.Unlock()
			if stats.ProtocolFinalStatus != test.wantProtocol || stats.Attribution != test.wantAttribution || stats.ModelCallFinalStatus != test.wantBusiness {
				t.Fatalf("terminal stats = %#v, want protocol=%q attribution=%q business=%q", stats, test.wantProtocol, test.wantAttribution, test.wantBusiness)
			}
			if countCapturedEvent(capture, "provider_stream_finished") != 1 || countCapturedEvent(capture, "model_call_final") != 1 {
				t.Fatalf("terminal event counts provider=%d model=%d", countCapturedEvent(capture, "provider_stream_finished"), countCapturedEvent(capture, "model_call_final"))
			}
		})
	}
}

func TestProviderLoopInterruptionDoesNotRecordFailure(t *testing.T) {
	service, stream, capture := providerTerminalCaptureFixture(t)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderPassCount = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderStreamStats = ProviderStreamStats{
		Attempt:             1,
		ProtocolFinalStatus: "streaming",
		HTTPStatus:          "not_recorded",
	}
	stream.mu.Unlock()

	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true, Err: errProviderLoopInterrupted}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	stats := stream.ProviderStreamStats
	providerActive := stream.ProviderActive
	stream.mu.Unlock()
	if stats.ProtocolFinalStatus != "streaming" || !providerActive {
		t.Fatalf("internal interruption mutated provider state: stats=%#v active=%v", stats, providerActive)
	}
	if countCapturedEvent(capture, "provider_stream_finished") != 0 || countCapturedEvent(capture, "model_call_final") != 0 {
		t.Fatalf("internal interruption recorded terminal events provider=%d model=%d", countCapturedEvent(capture, "provider_stream_finished"), countCapturedEvent(capture, "model_call_final"))
	}
}

func TestCancelThenProviderDoneRecordsOneTerminalPair(t *testing.T) {
	service, stream, capture := providerTerminalCaptureFixture(t)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderPassCount = 1
	stream.ProviderActive = true
	stream.ProviderCancel = func() {}
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "streaming", HTTPStatus: "not_recorded"}
	stream.mu.Unlock()

	if err := service.handleCancelIntent(InboundIntent{RequestID: stream.RequestID, CancelReason: "user aborted"}); err != nil {
		t.Fatalf("handleCancelIntent() error = %v", err)
	}
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true, Err: context.Canceled}); err != nil {
		t.Fatalf("late handleProviderDoneEvent() error = %v", err)
	}
	if countCapturedEvent(capture, "provider_stream_finished") != 1 || countCapturedEvent(capture, "model_call_final") != 1 {
		t.Fatalf("terminal event counts provider=%d model=%d", countCapturedEvent(capture, "provider_stream_finished"), countCapturedEvent(capture, "model_call_final"))
	}
	stream.mu.Lock()
	stats := stream.ProviderStreamStats
	stream.mu.Unlock()
	if stats.ProtocolFinalStatus != "canceled" || stats.ModelCallFinalStatus != "canceled" || stats.Attribution != "client" {
		t.Fatalf("cancel terminal stats = %#v", stats)
	}
}

func providerTerminalCaptureFixture(t *testing.T) (*Service, *ActiveStream, *debugRecorderTestCapture) {
	t.Helper()
	service, stream, _ := testCheckpointBlobProjection(t)
	capture := &debugRecorderTestCapture{}
	service.debug = newDebugRecorder(t.TempDir(), service.broker, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(service.debug.Close)
	return service, stream, capture
}

func countCapturedEvent(capture *debugRecorderTestCapture, eventName string) int {
	count := 0
	for _, captured := range capture.captures {
		if captured.Event.Event == eventName {
			count++
		}
	}
	return count
}

func capturedEventByName(t *testing.T, capture *debugRecorderTestCapture, eventName string) observability.Capture {
	t.Helper()
	for _, captured := range capture.captures {
		if captured.Event.Event == eventName {
			return captured
		}
	}
	t.Fatalf("missing captured event %q", eventName)
	return observability.Capture{}
}
