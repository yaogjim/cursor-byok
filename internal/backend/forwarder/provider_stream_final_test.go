package forwarder

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
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

func TestProviderTerminalTyped524KeepsStatusAndRetryDecision(t *testing.T) {
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
			StatusCode:  modeladapter.HTTPStatusCloudflareTimeout,
			Attempt:     3,
			MaxAttempts: 3,
			Body:        "sk-secret should not leak",
		}},
	})
	if err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	stats := stream.ProviderStreamStats
	stream.mu.Unlock()
	if stats.HTTPStatus != "524" || stats.ErrorCategory != modeladapter.ProviderErrorServer5xx || stats.Retryable != "true" || stats.RetryReason != "http_524" || stats.RetrySuppressionReason != "not_recorded" {
		t.Fatalf("typed 524 terminal stats = %#v", stats)
	}
	providerFinal := capturedEventByName(t, capture, "provider_stream_finished")
	payload, ok := providerFinal.Payload.Data.(map[string]any)
	if !ok {
		t.Fatalf("provider final payload type = %T", providerFinal.Payload.Data)
	}
	if payload["http_status"] != "524" || payload["retryable"] != "true" || payload["retry_reason"] != "http_524" || payload["error_category"] != modeladapter.ProviderErrorServer5xx {
		t.Fatalf("provider final payload = %#v", payload)
	}
	if summary, _ := payload["error_summary"].(string); strings.Contains(summary, "sk-secret") {
		t.Fatalf("error summary leaked body: %#v", payload)
	}
}

func TestProviderTerminal524AfterOutputIsSuppressed(t *testing.T) {
	service, stream, _ := providerTerminalCaptureFixture(t)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderPassCount = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "streaming", HTTPStatus: "not_recorded"}
	stream.mu.Unlock()
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "partial", OccurredAt: time.Now()}); err != nil {
		t.Fatalf("applyProviderModelEvent() error = %v", err)
	}
	err := service.handleProviderDoneEvent(stream, &streamProviderEvent{
		Token: 1,
		Done:  true,
		Err: providerTerminalError{cause: &modeladapter.HTTPStatusError{
			Provider:   "openai adapter",
			StatusCode: modeladapter.HTTPStatusCloudflareTimeout,
			Attempt:    1,
		}},
	})
	if err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	stats := stream.ProviderStreamStats
	stream.mu.Unlock()
	if stats.HTTPStatus != "524" || stats.ErrorCategory != modeladapter.ProviderErrorServer5xx {
		t.Fatalf("typed 524 after output lost status: %#v", stats)
	}
	if stats.Retryable != "false" || stats.RetryReason == "http_524" || stats.RetrySuppressionReason != "output_or_tool_progress" {
		t.Fatalf("524 after output should be suppressed, stats = %#v", stats)
	}
}

func TestProviderTerminal529IsNotRetryable(t *testing.T) {
	stats := ProviderStreamStats{Attempt: 1, HTTPStatus: "not_recorded"}
	retryable, reason, suppression := providerRetryObservation(&modeladapter.HTTPStatusError{StatusCode: 529}, stats)
	if retryable != "false" || reason == "http_5xx" || reason == "http_524" || suppression != "http_status" {
		t.Fatalf("529 observation = %q %q %q", retryable, reason, suppression)
	}
	retryable, reason, suppression = providerRetryObservation(&modeladapter.HTTPStatusError{StatusCode: 500}, stats)
	if retryable != "true" || reason != "http_5xx" {
		t.Fatalf("500 observation = %q %q %q", retryable, reason, suppression)
	}
}

func TestProviderTerminalTransportKeepsNotRecordedStatus(t *testing.T) {
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
		Err:   providerTerminalError{cause: errors.New("connection reset")},
	})
	if err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	stats := stream.ProviderStreamStats
	stream.mu.Unlock()
	if stats.HTTPStatus != "not_recorded" || stats.ErrorCategory != modeladapter.ProviderErrorTransport || stats.Retryable != "true" || stats.RetryReason != "transport" {
		t.Fatalf("transport terminal stats = %#v", stats)
	}
	providerFinal := capturedEventByName(t, capture, "provider_stream_finished")
	payload, ok := providerFinal.Payload.Data.(map[string]any)
	if !ok {
		t.Fatalf("provider final payload type = %T", providerFinal.Payload.Data)
	}
	if payload["http_status"] != "not_recorded" || payload["error_category"] != modeladapter.ProviderErrorTransport {
		t.Fatalf("transport final payload = %#v", payload)
	}
}

func TestProviderHTTP200ThenTruncationDoesNotInventHTTPStatus(t *testing.T) {
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
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "partial", OccurredAt: time.Now()}); err != nil {
		t.Fatalf("applyProviderModelEvent() error = %v", err)
	}
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	stats := stream.ProviderStreamStats
	stream.mu.Unlock()
	if stats.ProtocolFinalStatus != "truncated" || stats.HTTPStatus != "not_recorded" || stats.Retryable != "false" {
		t.Fatalf("HTTP 200 then truncated stats = %#v", stats)
	}
	if stats.RetrySuppressionReason != "output_or_tool_progress" && stats.RetrySuppressionReason != "missing_completion_marker" && stats.RetrySuppressionReason != "stream_raw_bytes" {
		t.Fatalf("unexpected suppression %q in %#v", stats.RetrySuppressionReason, stats)
	}
	providerFinal := capturedEventByName(t, capture, "provider_stream_finished")
	payload, ok := providerFinal.Payload.Data.(map[string]any)
	if !ok {
		t.Fatalf("provider final payload type = %T", providerFinal.Payload.Data)
	}
	if payload["http_status"] != "not_recorded" || payload["status"] == "completed" {
		t.Fatalf("200 header must not mark protocol completed: %#v", payload)
	}
}

func TestProviderHTTP200HeadersAreNotProtocolCompletion(t *testing.T) {
	service, stream, capture := providerTerminalCaptureFixture(t)
	headerAt := time.Now().UTC().Add(-time.Second)
	diag := &modeladapter.StreamDiagnostics{}
	diag.RecordHeader(http.StatusOK, 1, headerAt)
	diag.RecordClose(io.EOF)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderPassCount = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderStreamDiagnostics = diag
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "streaming", HTTPStatus: "not_recorded", TransportOutcome: "started"}
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
	if stats.ProtocolFinalStatus != "truncated" || stats.HTTPStatus != "200" {
		t.Fatalf("200 headers must not complete protocol: %#v", stats)
	}
	if stats.TransportOutcome != modeladapter.TransportOutcomeSucceeded {
		t.Fatalf("transport_outcome = %q, want succeeded", stats.TransportOutcome)
	}
	if stats.CloseCause != modeladapter.StreamCloseCauseEOF {
		t.Fatalf("close_cause = %q, want eof", stats.CloseCause)
	}
	if stats.PartialBoundary != modeladapter.PartialBoundaryText {
		t.Fatalf("partial_boundary = %q, want text", stats.PartialBoundary)
	}
	if stats.LastEffectiveContentAt.IsZero() || stats.HeaderAt.IsZero() {
		t.Fatalf("timeline missing: %#v", stats)
	}
	providerFinal := capturedEventByName(t, capture, "provider_stream_finished")
	payload, ok := providerFinal.Payload.Data.(map[string]any)
	if !ok {
		t.Fatalf("provider final payload type = %T", providerFinal.Payload.Data)
	}
	if payload["http_status"] != "200" || payload["status"] == "completed" || payload["transport_outcome"] != "succeeded" {
		t.Fatalf("header 200 projected as stream complete: %#v", payload)
	}
}

func TestProviderStreamDiagnosticsRawBytesVersusEffectiveContent(t *testing.T) {
	stats := ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "streaming"}
	diag := &modeladapter.StreamDiagnostics{}
	diag.RecordHeader(http.StatusOK, 1, time.Now())
	diag.RecordBytes(8, time.Now())
	applyProviderStreamDiagnostics(&stats, diag, &modeladapter.StreamTruncatedError{}, false)
	if stats.FirstByteAt.IsZero() || !stats.LastEffectiveContentAt.IsZero() || !stats.FirstEffectiveContentAt.IsZero() {
		t.Fatalf("raw bytes must not invent effective content: %#v", stats)
	}
	if stats.PartialBoundary != modeladapter.PartialBoundaryNone {
		t.Fatalf("raw-only partial_boundary = %q", stats.PartialBoundary)
	}
	observeProviderModelEvent(&stats, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "hi", OccurredAt: time.Now()})
	if stats.LastEffectiveContentAt.IsZero() || stats.FirstEffectiveContentAt.IsZero() || stats.PartialBoundary != modeladapter.PartialBoundaryText {
		t.Fatalf("text delta did not record effective content: %#v", stats)
	}
}

func TestProviderStreamDiagnosticsProjectsFinalRecoveryAttempt(t *testing.T) {
	stats := ProviderStreamStats{Attempt: 1, HTTPAttempt: 1, ProtocolFinalStatus: "streaming"}
	diag := &modeladapter.StreamDiagnostics{}
	diag.RecordHTTPResponse(&http.Response{
		StatusCode: http.StatusOK, Proto: "HTTP/2.0", Header: http.Header{"Content-Encoding": []string{"gzip"}}, ContentLength: -1,
	}, 1, time.Now())
	diag.RecordConnection(true, true)
	diag.BeginStreamRecoveryAttempt()
	diag.RecordHTTPResponse(&http.Response{
		StatusCode: http.StatusOK, Proto: "HTTP/1.1", Header: make(http.Header), ContentLength: -1,
	}, 2, time.Now())
	diag.RecordConnection(false, false)
	diag.RecordBytes(8, time.Now())

	applyProviderStreamDiagnostics(&stats, diag, &modeladapter.StreamTruncatedError{Err: io.ErrUnexpectedEOF}, false)
	if stats.HTTPAttempt != 2 || stats.HTTPProtocol != "HTTP/1.1" || stats.ContentEncoding != "identity" {
		t.Fatalf("final attempt transport metadata = %#v", stats)
	}
	if stats.ConnectionReused || stats.ConnectionWasIdle || !stats.ConnectionObserved || stats.RawByteCount != 8 || stats.StreamRecoveryAttempts != 1 {
		t.Fatalf("final attempt diagnostics = %#v", stats)
	}
}

func TestProviderStreamDiagnosticsIdleTimeoutAndMissingMarker(t *testing.T) {
	service, stream, capture := providerTerminalCaptureFixture(t)
	diag := &modeladapter.StreamDiagnostics{}
	diag.RecordHeader(http.StatusOK, 1, time.Now())
	diag.RecordBytes(3, time.Now())
	diag.RecordClose(&modeladapter.StreamIdleTimeoutError{Timeout: time.Second})
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderPassCount = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderStreamDiagnostics = diag
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "streaming", HTTPStatus: "not_recorded"}
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true, Err: providerTerminalError{cause: &modeladapter.StreamIdleTimeoutError{Timeout: time.Second}}}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	stats := stream.ProviderStreamStats
	stream.mu.Unlock()
	if stats.CloseCause != modeladapter.StreamCloseCauseIdleTimeout {
		t.Fatalf("idle close_cause = %q", stats.CloseCause)
	}
	if stats.ProtocolFinalStatus != "timeout" || stats.HTTPStatus != "200" {
		t.Fatalf("idle timeout stats = %#v", stats)
	}
	if stats.CompletionMarker {
		t.Fatal("idle timeout invented completion marker")
	}
	payload, ok := capturedEventByName(t, capture, "provider_stream_finished").Payload.Data.(map[string]any)
	if !ok || payload["close_cause"] != "idle_timeout" || payload["status"] == "completed" {
		t.Fatalf("idle timeout payload = %#v", payload)
	}
}

func TestProviderStreamDiagnosticsCompletionMarkerClearsPartialBoundary(t *testing.T) {
	service, stream, _ := providerTerminalCaptureFixture(t)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderPassCount = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "streaming", HTTPStatus: "not_recorded"}
	stream.mu.Unlock()
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "done", OccurredAt: time.Now()}); err != nil {
		t.Fatalf("text delta: %v", err)
	}
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, OccurredAt: time.Now()}); err != nil {
		t.Fatalf("turn finished: %v", err)
	}
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	stats := stream.ProviderStreamStats
	stream.mu.Unlock()
	if !stats.CompletionMarker || stats.ProtocolFinalStatus != "completed" {
		t.Fatalf("completed stats = %#v", stats)
	}
	if stats.PartialBoundary != modeladapter.PartialBoundaryNone {
		t.Fatalf("completed partial_boundary = %q, want none", stats.PartialBoundary)
	}
}

func TestProviderStreamDiagnosticsPartialToolBoundary(t *testing.T) {
	stats := ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "truncated"}
	observeProviderModelEvent(&stats, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindPartialToolCall, OccurredAt: time.Now()})
	finalizeProviderPartialBoundary(&stats, false)
	if stats.PartialBoundary != modeladapter.PartialBoundaryPartialTool {
		t.Fatalf("partial tool boundary = %q", stats.PartialBoundary)
	}
	finalizeProviderPartialBoundary(&stats, true)
	if stats.PartialBoundary != modeladapter.PartialBoundaryCheckpoint {
		t.Fatalf("checkpoint boundary = %q", stats.PartialBoundary)
	}
}

func TestProviderStreamDiagnosticsPostOutputTransportCloseKeepsReplayGate(t *testing.T) {
	service, stream, capture := providerTerminalCaptureFixture(t)
	headerAt := time.Now().UTC().Add(-2 * time.Second)
	firstByteAt := headerAt.Add(20 * time.Millisecond)
	diag := &modeladapter.StreamDiagnostics{}
	diag.RecordHeader(http.StatusOK, 1, headerAt)
	diag.RecordBytes(7, firstByteAt)
	diag.RecordClose(io.ErrUnexpectedEOF)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderPassCount = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderStreamDiagnostics = diag
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "streaming", HTTPStatus: "not_recorded"}
	stream.mu.Unlock()
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "partial", OccurredAt: time.Now()}); err != nil {
		t.Fatalf("applyProviderModelEvent() error = %v", err)
	}
	err := service.handleProviderDoneEvent(stream, &streamProviderEvent{
		Token: 1,
		Done:  true,
		Err: providerTerminalError{cause: &modeladapter.StreamTruncatedError{
			Provider:         "openai",
			Err:              io.ErrUnexpectedEOF,
			RawBytesObserved: true,
		}},
	})
	if err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	stats := stream.ProviderStreamStats
	stream.mu.Unlock()
	if stats.VisibleTextBytes != len("partial") || stats.ModelEventCount != 1 || stats.ChunkCount != 1 {
		t.Fatalf("existing stream stats regressed: %#v", stats)
	}
	if stats.CompletionMarker || stats.ProtocolFinalStatus == "completed" {
		t.Fatalf("transport close after output completed the protocol: %#v", stats)
	}
	if stats.Retryable != "false" || stats.RetrySuppressionReason != "output_or_tool_progress" {
		t.Fatalf("post-output replay gate lost: %#v", stats)
	}
	if stats.CloseCause != modeladapter.StreamCloseCauseUnexpectedEOF {
		t.Fatalf("close_cause = %q, want unexpected_eof", stats.CloseCause)
	}
	if stats.PartialBoundary != modeladapter.PartialBoundaryText {
		t.Fatalf("partial_boundary = %q, want text", stats.PartialBoundary)
	}
	if stats.HeaderAt.IsZero() || stats.FirstByteAt.IsZero() || stats.BodyEndAt.IsZero() || stats.LastEffectiveContentAt.IsZero() {
		t.Fatalf("timeline missing after post-output close: %#v", stats)
	}
	if stats.HTTPStatus != "200" {
		t.Fatalf("header 200 lost after body close: %#v", stats)
	}
	payload, ok := capturedEventByName(t, capture, "provider_stream_finished").Payload.Data.(map[string]any)
	if !ok {
		t.Fatalf("provider final payload type = %T", capturedEventByName(t, capture, "provider_stream_finished").Payload.Data)
	}
	if payload["completion_marker"] != false || payload["status"] == "completed" || payload["visible_text_bytes"] != stats.VisibleTextBytes {
		t.Fatalf("existing finished fields regressed: %#v", payload)
	}
	if payload["close_cause"] != "unexpected_eof" || payload["retryable"] != "false" || payload["retry_suppression_reason"] != "output_or_tool_progress" {
		t.Fatalf("post-output diagnostics payload = %#v", payload)
	}
}

func TestSafeProviderTerminalMessageKeepsTypedStatusAndRedactsBody(t *testing.T) {
	got := safeProviderTerminalMessage(providerTerminalError{cause: &modeladapter.HTTPStatusError{
		Provider:   "openai adapter",
		StatusCode: modeladapter.HTTPStatusCloudflareTimeout,
		Body:       "sk-secret and user prompt",
	}})
	if got != "server_5xx status=524" {
		t.Fatalf("safe 524 message = %q", got)
	}
	if strings.Contains(got, "sk-secret") || strings.Contains(got, "user prompt") {
		t.Fatalf("safe message leaked body: %q", got)
	}
	got = safeProviderTerminalMessage(errors.New("connection refused"))
	if got != "transport status=not_recorded" {
		t.Fatalf("safe transport message = %q", got)
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

func TestObserveProviderModelEventRecordsFirstEffectiveAfterInvalidEvents(t *testing.T) {
	started := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	invalidAt := started.Add(10 * time.Millisecond)
	effectiveAt := started.Add(40 * time.Millisecond)
	laterAt := started.Add(80 * time.Millisecond)
	stats := ProviderStreamStats{StartedAt: started}

	observeProviderModelEvent(&stats, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "", OccurredAt: invalidAt})
	if !stats.FirstEventAt.Equal(invalidAt.UTC()) {
		t.Fatalf("FirstEventAt = %v, want first any event %v", stats.FirstEventAt, invalidAt.UTC())
	}
	if stats.LastEffectiveContentAt.IsZero() {
		t.Fatal("empty text delta must still record LastEffectiveContentAt")
	}
	if !stats.FirstEffectiveContentAt.IsZero() {
		t.Fatalf("empty text delta must not record FirstEffectiveContentAt: %#v", stats)
	}

	for _, event := range []modeladapter.ModelEvent{
		{Kind: modeladapter.ModelEventKindThinkingDelta, Text: "", OccurredAt: invalidAt.Add(time.Millisecond)},
		{Kind: modeladapter.ModelEventKindThinkingCompleted, OccurredAt: invalidAt.Add(2 * time.Millisecond)},
		{Kind: modeladapter.ModelEventKindTurnFinished, OccurredAt: invalidAt.Add(3 * time.Millisecond)},
		{Kind: modeladapter.ModelEventKindProviderError, OccurredAt: invalidAt.Add(4 * time.Millisecond)},
		{Kind: modeladapter.ModelEventKindPartialToolCall, OccurredAt: invalidAt.Add(5 * time.Millisecond)},
		{Kind: modeladapter.ModelEventKindToolCallDelta, ToolCallID: "tool-1", OccurredAt: invalidAt.Add(6 * time.Millisecond)},
		{Kind: modeladapter.ModelEventKindToolLikeCompleted, OccurredAt: invalidAt.Add(7 * time.Millisecond)},
	} {
		observeProviderModelEvent(&stats, event)
	}
	if !stats.FirstEffectiveContentAt.IsZero() {
		t.Fatalf("invalid events invented FirstEffectiveContentAt: %#v", stats)
	}
	if !stats.FirstEventAt.Equal(invalidAt.UTC()) {
		t.Fatalf("FirstEventAt mutated by later invalid events: %v", stats.FirstEventAt)
	}

	observeProviderModelEvent(&stats, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "hello", OccurredAt: effectiveAt})
	if !stats.FirstEffectiveContentAt.Equal(effectiveAt.UTC()) {
		t.Fatalf("FirstEffectiveContentAt = %v, want first valid event %v", stats.FirstEffectiveContentAt, effectiveAt.UTC())
	}

	observeProviderModelEvent(&stats, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindThinkingDelta, Text: "think", OccurredAt: laterAt})
	if !stats.FirstEffectiveContentAt.Equal(effectiveAt.UTC()) {
		t.Fatalf("FirstEffectiveContentAt overwritten: %v", stats.FirstEffectiveContentAt)
	}
	if !stats.LastEffectiveContentAt.Equal(laterAt.UTC()) {
		t.Fatalf("LastEffectiveContentAt = %v, want last content %v", stats.LastEffectiveContentAt, laterAt.UTC())
	}
}

func TestObserveProviderModelEventEffectiveContentKinds(t *testing.T) {
	at := time.Date(2026, 8, 30, 12, 0, 0, 40_000_000, time.UTC)
	cases := []modeladapter.ModelEvent{
		{Kind: modeladapter.ModelEventKindTextDelta, Text: "x", OccurredAt: at},
		{Kind: modeladapter.ModelEventKindThinkingDelta, Text: "y", OccurredAt: at},
		{Kind: modeladapter.ModelEventKindPartialToolCall, ToolCallID: "tool-1", ToolCall: &agentv1.ToolCall{}, OccurredAt: at},
		{Kind: modeladapter.ModelEventKindToolCallDelta, ToolCallID: "tool-1", ToolCallDelta: &agentv1.ToolCallDelta{}, OccurredAt: at},
		{Kind: modeladapter.ModelEventKindToolLikeCompleted, ToolInvocation: &runtimecore.ToolInvocation{CallID: "tool-1"}, OccurredAt: at},
	}
	for _, event := range cases {
		stats := ProviderStreamStats{}
		observeProviderModelEvent(&stats, event)
		if stats.FirstEffectiveContentAt.IsZero() {
			t.Fatalf("kind %s did not record first effective content: %#v", event.Kind, stats)
		}
		if event.Kind == modeladapter.ModelEventKindToolCallDelta && !stats.LastEffectiveContentAt.IsZero() {
			t.Fatalf("tool call delta must not change LastEffectiveContentAt: %#v", stats)
		}
	}
}

func TestProviderTerminalFieldsProjectIndependentTTFR(t *testing.T) {
	started := time.Date(2026, 8, 30, 12, 0, 0, 0, time.UTC)
	firstEvent := started.Add(10 * time.Millisecond)
	firstByte := started.Add(5 * time.Millisecond)
	firstEffective := started.Add(40 * time.Millisecond)
	lastEffective := started.Add(80 * time.Millisecond)
	fields := providerTerminalFields("call-1", ProviderStreamStats{
		StartedAt:               started,
		FirstEventAt:            firstEvent,
		FirstByteAt:             firstByte,
		FirstEffectiveContentAt: firstEffective,
		LastEffectiveContentAt:  lastEffective,
		FinishedAt:              started.Add(100 * time.Millisecond),
	})
	if fields["ttft_ms"] != nil {
		t.Fatalf("provider terminal must not project adapter ttft_ms: %#v", fields)
	}
	if fields["first_event_at"] != firstEvent.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("first_event_at = %#v", fields["first_event_at"])
	}
	if fields["first_byte_at"] != firstByte.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("first_byte_at = %#v", fields["first_byte_at"])
	}
	if fields["last_effective_content_at"] != lastEffective.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("last_effective_content_at = %#v", fields["last_effective_content_at"])
	}
	if fields["first_effective_content_at"] != firstEffective.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("first_effective_content_at = %#v", fields["first_effective_content_at"])
	}
	if fields["ttfr_ms"] != int64(40) {
		t.Fatalf("ttfr_ms = %#v, want 40", fields["ttfr_ms"])
	}

	missing := providerTerminalFields("call-1", ProviderStreamStats{StartedAt: started, FirstEventAt: firstEvent, FirstByteAt: firstByte})
	if missing["first_effective_content_at"] != "not_recorded" || missing["ttfr_ms"] != "not_recorded" {
		t.Fatalf("missing ttfr must stay not_recorded: %#v", missing)
	}
	if missing["first_event_at"] != firstEvent.UTC().Format(time.RFC3339Nano) || missing["first_byte_at"] != firstByte.UTC().Format(time.RFC3339Nano) {
		t.Fatalf("existing timeline fields regressed: %#v", missing)
	}

	clamped := providerTerminalFields("call-1", ProviderStreamStats{
		StartedAt:               firstEffective,
		FirstEffectiveContentAt: started,
	})
	if clamped["ttfr_ms"] != int64(0) {
		t.Fatalf("negative ttfr_ms = %#v, want 0", clamped["ttfr_ms"])
	}
}

func TestProviderStreamDiagnosticsDoNotCopyAdapterEffectiveIntoFirstEffective(t *testing.T) {
	stats := ProviderStreamStats{Attempt: 1, ProtocolFinalStatus: "streaming"}
	diag := &modeladapter.StreamDiagnostics{}
	diag.RecordHeader(http.StatusOK, 1, time.Now())
	diag.RecordBytes(8, time.Now())
	diag.MarkEffectiveContent()
	applyProviderStreamDiagnostics(&stats, diag, &modeladapter.StreamTruncatedError{}, false)
	if stats.FirstByteAt.IsZero() || stats.LastEffectiveContentAt.IsZero() {
		t.Fatalf("adapter diagnostics should copy first byte and last effective: %#v", stats)
	}
	if !stats.FirstEffectiveContentAt.IsZero() {
		t.Fatalf("adapter last effective must not invent FirstEffectiveContentAt: %#v", stats)
	}
}
