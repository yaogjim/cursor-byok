package forwarder

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
)

func TestOverflowRecoveryAllowedRequiresZeroCurrentCallOutput(t *testing.T) {
	overflow := &modeladapter.HTTPStatusError{StatusCode: http.StatusBadRequest, Code: "context_length_exceeded", Body: "too long"}
	idle := overflowRecoveryCallState{}
	if !overflowRecoveryAllowed(overflow, idle) {
		t.Fatal("zero-output overflow should be recoverable")
	}
	if overflowRecoveryAllowed(overflow, overflowRecoveryCallState{Text: "hi"}) {
		t.Fatal("current text must block recovery")
	}
	if overflowRecoveryAllowed(overflow, overflowRecoveryCallState{Text: " \n\t"}) {
		t.Fatal("whitespace-only current text must block recovery")
	}
	if overflowRecoveryAllowed(overflow, overflowRecoveryCallState{Reasoning: "think"}) {
		t.Fatal("current thinking must block recovery")
	}
	if overflowRecoveryAllowed(overflow, overflowRecoveryCallState{Reasoning: "  "}) {
		t.Fatal("whitespace-only current thinking must block recovery")
	}
	if overflowRecoveryAllowed(overflow, overflowRecoveryCallState{SyntheticThinking: true}) {
		t.Fatal("synthetic thinking must block recovery")
	}
	if overflowRecoveryAllowed(overflow, overflowRecoveryCallState{HadTool: true}) {
		t.Fatal("current tool output must block recovery")
	}
	if overflowRecoveryAllowed(overflow, overflowRecoveryCallState{PartialTools: true}) {
		t.Fatal("partial tool output must block recovery")
	}
	if overflowRecoveryAllowed(overflow, overflowRecoveryCallState{HadTool: false, PartialTools: true}) {
		t.Fatal("published tool-call argument output must block recovery")
	}
	if overflowRecoveryAllowed(overflow, overflowRecoveryCallState{Pending: 1}) {
		t.Fatal("pending exec/interaction must block recovery")
	}
	if overflowRecoveryAllowed(overflow, overflowRecoveryCallState{Continuation: true}) {
		t.Fatal("continuation must block recovery")
	}
	if overflowRecoveryAllowed(overflow, overflowRecoveryCallState{AlreadyAttempted: true}) {
		t.Fatal("second overflow must not retry recovery")
	}
	if overflowRecoveryAllowed(overflow, overflowRecoveryCallState{Terminal: true}) {
		t.Fatal("terminal stream must not recover")
	}
	if overflowRecoveryAllowed(&modeladapter.HTTPStatusError{StatusCode: http.StatusBadRequest, Body: "bad json"}, idle) {
		t.Fatal("generic 400 must not recover")
	}
	if overflowRecoveryAllowed(&modeladapter.HTTPStatusError{StatusCode: http.StatusTooManyRequests, Body: "prompt is too long"}, idle) {
		t.Fatal("429 must not recover")
	}
}

func TestHandleProviderDoneAcceptsFirstOverflowAndFencesLateEvents(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	overflow := providerTerminalError{cause: &modeladapter.HTTPStatusError{
		StatusCode: http.StatusBadRequest,
		Code:       "context_length_exceeded",
		Body:       "prompt is too long",
	}}
	stream.mu.Lock()
	beforeToken := stream.CurrentProviderToken
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: beforeToken, Done: true, Err: overflow}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	if !stream.OverflowRecoveryAttempted {
		stream.mu.Unlock()
		t.Fatal("accepted recovery did not mark the run")
	}
	if stream.CurrentProviderToken <= beforeToken {
		stream.mu.Unlock()
		t.Fatal("accepted recovery did not advance provider token")
	}
	if stream.PendingCompaction == nil || stream.Phase != TurnPhaseCompacting {
		pending := stream.PendingCompaction
		phase := stream.Phase
		stream.mu.Unlock()
		t.Fatalf("pending compaction = %#v phase=%q", pending, phase)
	}
	if isTerminalStreamStatus(stream.Status) {
		status := stream.Status
		stream.mu.Unlock()
		t.Fatalf("status = %q, want non-terminal after accepted recovery", status)
	}
	stream.mu.Unlock()
	late := service.handleProviderEvent(stream, &streamProviderEvent{
		Token: beforeToken,
		Event: modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "late"},
	})
	if late != nil {
		t.Fatalf("late event error = %v", late)
	}
	stream.mu.Lock()
	if strings.TrimSpace(stream.ProviderAccumulatedText) != "" {
		stream.mu.Unlock()
		t.Fatal("late event after token fence was applied")
	}
	stream.mu.Unlock()
}

func TestHandleProviderDoneRejectsCurrentOutputAndSecondOverflow(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	overflow := providerTerminalError{cause: &modeladapter.HTTPStatusError{
		StatusCode: http.StatusBadRequest,
		Code:       "context_length_exceeded",
		Body:       "prompt is too long",
	}}
	stream.mu.Lock()
	stream.ProviderAccumulatedText = "already said hello"
	token := stream.CurrentProviderToken
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: overflow}); err != nil {
		t.Fatalf("text overflow handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	if stream.OverflowRecoveryAttempted || stream.PendingCompaction != nil {
		stream.mu.Unlock()
		t.Fatal("current text overflow must not start recovery")
	}
	stream.mu.Unlock()

	service, stream = testOverflowRecoveryStream(t)
	stream.mu.Lock()
	stream.OverflowRecoveryAttempted = true
	token = stream.CurrentProviderToken
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: overflow}); err != nil {
		t.Fatalf("second overflow handleProviderDoneEvent() error = %v", err)
	}
	acknowledgeCheckpointBlobs(t, service, stream)
	events := readCheckpointTestEvents(t, service, stream)
	found := false
	for _, event := range events {
		if !event.End || strings.TrimSpace(event.TerminalErrorCode) != compactionOverflowTerminalCode {
			continue
		}
		found = true
		if event.TerminalRetryable == nil || *event.TerminalRetryable {
			t.Fatal("second overflow terminal must be non-retryable")
		}
	}
	if !found {
		t.Fatal("second overflow missing context_overflow_after_compaction terminal")
	}
}

func TestHandleProviderDoneAllowsOverflowAfterEarlierCompletedPartialTools(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	overflow := testContextOverflowErr()
	stream.mu.Lock()
	stream.PartialToolCallIDs["call-completed"] = struct{}{}
	token := stream.CurrentProviderToken
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: overflow}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	accepted := stream.OverflowRecoveryAttempted && stream.PendingCompaction != nil
	stream.mu.Unlock()
	if !accepted {
		t.Fatal("earlier completed PartialToolCallIDs must not suppress current-call zero-output recovery")
	}
}

func TestHandleProviderDoneRejectsCurrentCallPartialToolWhitespaceAndSyntheticThinking(t *testing.T) {
	overflow := testContextOverflowErr()

	service, stream := testOverflowRecoveryStream(t)
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind:       modeladapter.ModelEventKindPartialToolCall,
		ToolCallID: "tool-current",
		ToolCall:   &agentv1.ToolCall{},
	}); err != nil {
		t.Fatalf("apply current partial tool: %v", err)
	}
	stream.mu.Lock()
	token := stream.CurrentProviderToken
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: overflow}); err != nil {
		t.Fatalf("current partial overflow handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	if stream.OverflowRecoveryAttempted || stream.PendingCompaction != nil {
		stream.mu.Unlock()
		t.Fatal("current-call partial tool must block recovery")
	}
	stream.mu.Unlock()

	service, stream = testOverflowRecoveryStream(t)
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind: modeladapter.ModelEventKindTextDelta,
		Text: " \n\t",
	}); err != nil {
		t.Fatalf("apply whitespace text: %v", err)
	}
	stream.mu.Lock()
	token = stream.CurrentProviderToken
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: overflow}); err != nil {
		t.Fatalf("whitespace overflow handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	if stream.OverflowRecoveryAttempted || stream.PendingCompaction != nil {
		stream.mu.Unlock()
		t.Fatal("whitespace-only current text must block recovery")
	}
	stream.mu.Unlock()

	service, stream = testOverflowRecoveryStream(t)
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind:                    modeladapter.ModelEventKindThinkingCompleted,
		ThinkingSignature:       "encrypted-reasoning",
		ThinkingSignatureSource: modeladapter.ReasoningSignatureSourceOpenAIResponses,
	}); err != nil {
		t.Fatalf("apply synthetic thinking: %v", err)
	}
	stream.mu.Lock()
	token = stream.CurrentProviderToken
	published := stream.ProviderSyntheticThinkingPublished
	stream.mu.Unlock()
	if !published {
		t.Fatal("expected synthetic thinking to be published")
	}
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: overflow}); err != nil {
		t.Fatalf("synthetic thinking overflow handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	if stream.OverflowRecoveryAttempted || stream.PendingCompaction != nil {
		stream.mu.Unlock()
		t.Fatal("synthetic thinking must block recovery")
	}
	stream.mu.Unlock()
}

func TestHandleProviderDoneRejectsToolCallDeltaOnlyArgumentOutput(t *testing.T) {
	overflow := testContextOverflowErr()
	service, stream := testOverflowRecoveryStream(t)
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind:       modeladapter.ModelEventKindToolCallDelta,
		ToolCallID: "tool-args-only",
		ToolCallDelta: &agentv1.ToolCallDelta{
			Delta: &agentv1.ToolCallDelta_EditToolCallDelta{
				EditToolCallDelta: &agentv1.EditToolCallDelta{StreamContentDelta: `{"path":"/tmp/secret.txt"}`},
			},
		},
	}); err != nil {
		t.Fatalf("apply tool-call delta: %v", err)
	}
	stream.mu.Lock()
	token := stream.CurrentProviderToken
	published := stream.ProviderPublishedToolArgs
	partialTools := stream.ProviderStreamStats.PartialToolCount
	stream.mu.Unlock()
	if !published {
		t.Fatal("published tool-call delta must set per-call observed tool-arg state")
	}
	if partialTools == 0 {
		t.Fatal("published tool-call delta must count as current-call partial tool output")
	}
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: overflow}); err != nil {
		t.Fatalf("tool-call delta overflow handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	if stream.OverflowRecoveryAttempted || stream.PendingCompaction != nil {
		stream.mu.Unlock()
		t.Fatal("tool-call delta argument output must block recovery")
	}
	stream.mu.Unlock()

	service, stream = testOverflowRecoveryStream(t)
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind:       modeladapter.ModelEventKindToolCallDelta,
		ToolCallID: "",
	}); err != nil {
		t.Fatalf("apply empty tool-call delta: %v", err)
	}
	stream.mu.Lock()
	token = stream.CurrentProviderToken
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: overflow}); err != nil {
		t.Fatalf("empty tool-call delta overflow handleProviderDoneEvent() error = %v", err)
	}
	stream.mu.Lock()
	accepted := stream.OverflowRecoveryAttempted && stream.PendingCompaction != nil
	stream.mu.Unlock()
	if !accepted {
		t.Fatal("unpublished empty tool-call delta must not block zero-output recovery")
	}
	_ = service.broker.Cancel(stream.RequestID, "test cleanup")
}

func TestHandleProviderDoneCompilerFailureIsOrdinaryError(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	provider := &overflowRecoveryTestProvider{}
	service.provider = provider
	service.compiler = overflowRecoveryFailingCompiler{PromptCompiler: service.compiler}
	stream.mu.Lock()
	original := cloneHistoryEntries(stream.CheckpointConversation.Entries)
	token := stream.CurrentProviderToken
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: testContextOverflowErr()}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	assertOverflowRecoveryFailureContract(t, service, stream, original, provider, "unknown", "injected compiler failure", false)
}

func TestHandleProviderDoneStorageFailureIsOrdinaryError(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	provider := &overflowRecoveryTestProvider{}
	service.provider = provider
	stream.mu.Lock()
	original := cloneHistoryEntries(stream.CheckpointConversation.Entries)
	token := stream.CurrentProviderToken
	stream.mu.Unlock()
	blocked := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(blocked, []byte("blocked"), 0o600); err != nil {
		t.Fatalf("write blocked store root: %v", err)
	}
	service.store = NewConversationFileStore(blocked)
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: testContextOverflowErr()}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	assertOverflowRecoveryFailureContract(t, service, stream, original, provider, "usage_persistence_error", "", false)
}

func TestHandleProviderDoneOverflowNonretryableIsExactlyOnce(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	provider := &overflowRecoveryTestProvider{}
	service.provider = provider
	stream.mu.Lock()
	stream.OverflowRecoveryAttempted = true
	original := cloneHistoryEntries(stream.CheckpointConversation.Entries)
	token := stream.CurrentProviderToken
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: testContextOverflowErr()}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	assertOverflowRecoveryFailureContract(t, service, stream, original, provider, compactionOverflowTerminalCode, "provider context overflow after compaction recovery", true)
}

func TestOverflowRecoveryFailureCodePreservesCategories(t *testing.T) {
	if got := overflowRecoveryFailureCode(compactionTerminalError{code: compactionOverflowTerminalCode, message: "overflow"}); got != compactionOverflowTerminalCode {
		t.Fatalf("overflow code = %q", got)
	}
	if got := overflowRecoveryFailureCode(compactionTerminalError{code: "usage_persistence_error", message: "usage"}); got != "usage_persistence_error" {
		t.Fatalf("usage code = %q", got)
	}
	if got := overflowRecoveryFailureCode(errors.New("injected compiler failure")); got != "unknown" {
		t.Fatalf("compiler code = %q", got)
	}
	if got := overflowRecoveryFailureCode(errors.New("create conversation directory")); got != "unknown" {
		t.Fatalf("storage code = %q", got)
	}
}

func TestOverflowRecoveryCompactsAndResumesNewModelCall(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	appendCompletedToolHistory(t, service, stream)
	provider := &overflowRecoveryTestProvider{}
	var resumeRequest ProviderRequest
	provider.handler = func(_ context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		if strings.Contains(req.CompileSummary, "compaction") {
			if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "compacted earlier turns"}); err != nil {
				return err
			}
			return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, FinishReason: "stop", UsagePresent: true, InputTokens: 11, OutputTokens: 7})
		}
		resumeRequest = req
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "recovered answer"}); err != nil {
			return err
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, FinishReason: "stop", UsagePresent: true, InputTokens: 13, OutputTokens: 5})
	}
	service.provider = provider

	stream.mu.Lock()
	failedModelCallID := stream.CurrentModelCallID
	requestID := stream.RequestID
	conversationID := stream.ConversationID
	turnSeq := stream.TurnSeq
	token := stream.CurrentProviderToken
	originalEntries := cloneHistoryEntries(stream.CheckpointConversation.Entries)
	stream.PartialToolCallIDs["call-completed"] = struct{}{}
	stream.mu.Unlock()

	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: testContextOverflowErr()}); err != nil {
		t.Fatalf("overflow handleProviderDoneEvent() error = %v", err)
	}
	completeOverflowPreCompactHook(t, service, stream)
	waitForOverflowRecoveryResume(t, service, stream, failedModelCallID, provider)

	provider.mu.Lock()
	requests := append([]ProviderRequest(nil), provider.requests...)
	provider.mu.Unlock()
	if len(requests) < 2 {
		t.Fatalf("provider requests = %d, want summary and resume", len(requests))
	}
	summaryReq := requests[0]
	if !strings.Contains(summaryReq.CompileSummary, "compaction") {
		t.Fatalf("first provider request compile summary = %q, want compaction", summaryReq.CompileSummary)
	}
	if strings.TrimSpace(summaryReq.ModelCallID) == "" || summaryReq.ModelCallID == failedModelCallID {
		t.Fatalf("summary model_call_id = %q, want independent of failed call %q", summaryReq.ModelCallID, failedModelCallID)
	}
	if strings.TrimSpace(resumeRequest.ModelCallID) == "" || resumeRequest.ModelCallID == failedModelCallID || resumeRequest.ModelCallID == summaryReq.ModelCallID {
		t.Fatalf("resume model_call_id = %q, failed=%q summary=%q", resumeRequest.ModelCallID, failedModelCallID, summaryReq.ModelCallID)
	}
	if resumeRequest.RequestID != requestID || resumeRequest.ConversationID != conversationID {
		t.Fatalf("resume identity request=%q conversation=%q, want %q / %q", resumeRequest.RequestID, resumeRequest.ConversationID, requestID, conversationID)
	}

	stream.mu.Lock()
	if stream.TurnSeq != turnSeq {
		t.Fatalf("turn_seq = %d, want %d", stream.TurnSeq, turnSeq)
	}
	if !stream.OverflowRecoveryAttempted {
		stream.mu.Unlock()
		t.Fatal("recovery flag was cleared")
	}
	after := cloneHistoryEntries(stream.CheckpointConversation.Entries)
	stream.mu.Unlock()
	if len(after) <= len(originalEntries) {
		t.Fatalf("entries after recovery = %d, want original %d plus compaction records", len(after), len(originalEntries))
	}
	for index := range originalEntries {
		if after[index].Kind != originalEntries[index].Kind || after[index].TurnSeq != originalEntries[index].TurnSeq {
			t.Fatalf("canonical history prefix changed at %d: got kind=%s turn=%d, want kind=%s turn=%d", index, after[index].Kind, after[index].TurnSeq, originalEntries[index].Kind, originalEntries[index].TurnSeq)
		}
	}
	if countHistoryKind(originalEntries, "tool_call") != countHistoryKind(after, "tool_call") {
		t.Fatalf("tool_call entries changed from %d to %d", countHistoryKind(originalEntries, "tool_call"), countHistoryKind(after, "tool_call"))
	}

	stream.mu.Lock()
	secondToken := stream.CurrentProviderToken
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.ProviderActive = true
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: secondToken, Done: true, Err: testContextOverflowErr()}); err != nil {
		t.Fatalf("second overflow after resume error = %v", err)
	}
	acknowledgeCheckpointBlobs(t, service, stream)
	foundTerminal := false
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if event.End && strings.TrimSpace(event.TerminalErrorCode) == compactionOverflowTerminalCode {
			foundTerminal = true
			if event.TerminalRetryable == nil || *event.TerminalRetryable {
				t.Fatal("second overflow terminal must be non-retryable")
			}
		}
	}
	if !foundTerminal {
		t.Fatal("resume overflow missing exactly-once context_overflow_after_compaction terminal")
	}
}

func TestOverflowRecoveryCancelDoesNotStartResume(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	provider := &overflowRecoveryTestProvider{}
	service.provider = provider
	stream.mu.Lock()
	token := stream.CurrentProviderToken
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: token, Done: true, Err: testContextOverflowErr()}); err != nil {
		t.Fatalf("overflow handleProviderDoneEvent() error = %v", err)
	}
	if err := service.broker.Cancel(stream.RequestID, "user stopped"); err != nil {
		t.Fatalf("Cancel() error = %v", err)
	}
	completeOverflowPreCompactHook(t, service, stream)
	injectOverflowStaleEvents(t, service, stream, token, 9999)
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		acknowledgeCheckpointBlobs(t, service, stream)
		if provider.requestCount() > 0 {
			t.Fatalf("canceled recovery issued %d provider requests", provider.requestCount())
		}
		time.Sleep(10 * time.Millisecond)
	}
	stream.mu.Lock()
	status := stream.Status
	pending := stream.PendingCompaction
	stream.mu.Unlock()
	if status != StreamStatusCanceled {
		t.Fatalf("status = %q, want canceled", status)
	}
	if pending != nil {
		t.Fatal("canceled recovery left a pending compaction")
	}
	if provider.requestCount() != 0 {
		t.Fatalf("canceled recovery issued %d provider requests", provider.requestCount())
	}
}

func TestOverflowRecoveryRunSSESucceedsWithDelayedAckAndDistinctUsage(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	appendCompletedToolHistory(t, service, stream)
	prepareUnderBudgetOverflowDrive(t, service, stream)
	provider := newOverflowRecoveryChainProvider(false)
	service.provider = provider
	runSSE := startOverflowRunSSE(t, service, stream.RequestID)

	stream.mu.Lock()
	originalEntries := cloneHistoryEntries(stream.CheckpointConversation.Entries)
	turnSeq := stream.TurnSeq
	requestID := stream.RequestID
	conversationID := stream.ConversationID
	stream.mu.Unlock()

	if err := service.driveProvider(stream); err != nil {
		t.Fatalf("driveProvider() error = %v", err)
	}
	stream.mu.Lock()
	overflowToken := stream.CurrentProviderToken
	stream.mu.Unlock()

	resume := waitForOverflowResumeRequest(t, service, stream, provider, overflowToken)
	stream.mu.Lock()
	pendingWrites := len(stream.PendingCheckpointBlobWrites)
	statusBeforeAck := stream.Status
	stream.mu.Unlock()
	if isTerminalStreamStatus(statusBeforeAck) {
		t.Fatalf("resume started only after ACK; status=%q writes=%d", statusBeforeAck, pendingWrites)
	}
	if strings.TrimSpace(resume.ModelCallID) == "" {
		t.Fatal("resume missing model_call_id")
	}

	acknowledgeCheckpointBlobs(t, service, stream)
	waitForOverflowRunSSEResult(t, runSSE, false)

	failed, summary, resumeReq := overflowProviderCalls(provider)
	if strings.TrimSpace(failed.ModelCallID) == "" || strings.TrimSpace(summary.ModelCallID) == "" {
		t.Fatalf("provider calls failed=%q summary=%q resume=%q", failed.ModelCallID, summary.ModelCallID, resumeReq.ModelCallID)
	}
	if failed.ModelCallID == summary.ModelCallID || failed.ModelCallID == resumeReq.ModelCallID || summary.ModelCallID == resumeReq.ModelCallID {
		t.Fatalf("model_call_id collapsed failed=%q summary=%q resume=%q", failed.ModelCallID, summary.ModelCallID, resumeReq.ModelCallID)
	}
	if resumeReq.RequestID != requestID || resumeReq.ConversationID != conversationID {
		t.Fatalf("resume identity request=%q conversation=%q, want %q / %q", resumeReq.RequestID, resumeReq.ConversationID, requestID, conversationID)
	}
	assertOverflowUsageRow(t, service, requestID, failed.ModelCallID, 3, 1)
	assertOverflowUsageRow(t, service, requestID, summary.ModelCallID, 11, 7)
	assertOverflowUsageRow(t, service, requestID, resumeReq.ModelCallID, 13, 5)

	if got := overflowRunSSEText(runSSE); !strings.Contains(got, "recovered answer") {
		t.Fatalf("RunSSE text = %q, want recovered answer", got)
	}

	stream.mu.Lock()
	if stream.TurnSeq != turnSeq {
		t.Fatalf("turn_seq = %d, want %d", stream.TurnSeq, turnSeq)
	}
	after := cloneHistoryEntries(stream.CheckpointConversation.Entries)
	stream.mu.Unlock()
	if len(after) <= len(originalEntries) {
		t.Fatalf("entries after recovery = %d, want original %d plus compaction records", len(after), len(originalEntries))
	}
	for index := range originalEntries {
		if after[index].Kind != originalEntries[index].Kind || after[index].TurnSeq != originalEntries[index].TurnSeq {
			t.Fatalf("canonical history prefix changed at %d", index)
		}
	}
	if countHistoryKind(originalEntries, "tool_call") != countHistoryKind(after, "tool_call") {
		t.Fatalf("tool_call entries changed from %d to %d", countHistoryKind(originalEntries, "tool_call"), countHistoryKind(after, "tool_call"))
	}

	lateAckErr := service.handleCheckpointBlobResult(stream, &agentv1.KvClientMessage{
		Id: 0x00ffffff,
		Message: &agentv1.KvClientMessage_SetBlobResult{
			SetBlobResult: &agentv1.SetBlobResult{},
		},
	})
	if lateAckErr != nil {
		t.Fatalf("late blob ACK error = %v", lateAckErr)
	}
	if count := nonCompactionProviderCount(provider); count != 2 {
		t.Fatalf("provider non-compaction requests = %d, want overflow + single resume", count)
	}
}

func TestOverflowRecoveryRunSSERepeatOverflowIsUniqueNonRetryable(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	appendCompletedToolHistory(t, service, stream)
	prepareUnderBudgetOverflowDrive(t, service, stream)
	provider := newOverflowRecoveryChainProvider(true)
	service.provider = provider
	runSSE := startOverflowRunSSE(t, service, stream.RequestID)

	if err := service.driveProvider(stream); err != nil {
		t.Fatalf("driveProvider() error = %v", err)
	}
	stream.mu.Lock()
	overflowToken := stream.CurrentProviderToken
	stream.mu.Unlock()
	_ = waitForOverflowResumeRequest(t, service, stream, provider, overflowToken)
	err := waitForOverflowRunSSEResult(t, runSSE, true)
	details := extractRunSSEErrorDetails(t, err)
	custom := details.GetDetails()
	if custom == nil {
		t.Fatal("missing CustomErrorDetails")
	}
	if custom.GetIsRetryable() {
		t.Fatal("repeat overflow RunSSE IsRetryable must be false")
	}
	if custom.GetTitle() != "Context Too Large After Compaction" {
		t.Fatalf("RunSSE title = %q, want Context Too Large After Compaction", custom.GetTitle())
	}
	if count := nonCompactionProviderCount(provider); count != 2 {
		t.Fatalf("provider non-compaction requests = %d, want overflow + resume overflow", count)
	}
	provider.mu.Lock()
	summaryCount := 0
	for _, req := range provider.requests {
		if strings.Contains(req.CompileSummary, "compaction") {
			summaryCount++
		}
	}
	provider.mu.Unlock()
	if summaryCount != 1 {
		t.Fatalf("summary requests = %d, want exactly one recovery attempt", summaryCount)
	}
}

func testContextOverflowErr() error {
	return providerTerminalError{cause: &modeladapter.HTTPStatusError{
		StatusCode: http.StatusBadRequest,
		Code:       "context_length_exceeded",
		Body:       "prompt is too long",
	}}
}

type overflowRecoveryTestProvider struct {
	mu       sync.Mutex
	requests []ProviderRequest
	handler  func(context.Context, ProviderRequest, func(modeladapter.ModelEvent) error) error
}

func (provider *overflowRecoveryTestProvider) StartStream(ctx context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
	provider.mu.Lock()
	provider.requests = append(provider.requests, req)
	handler := provider.handler
	provider.mu.Unlock()
	if handler != nil {
		return handler(ctx, req, sink)
	}
	return nil
}

func (provider *overflowRecoveryTestProvider) requestCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return len(provider.requests)
}

func completeOverflowPreCompactHook(t *testing.T, service *Service, stream *ActiveStream) {
	t.Helper()
	if !tryCompleteOverflowPreCompactHook(t, service, stream) {
		t.Fatal("missing execute_hook_pre_compact pending exec")
	}
}

func tryCompleteOverflowPreCompactHook(t *testing.T, service *Service, stream *ActiveStream) bool {
	t.Helper()
	stream.mu.Lock()
	var pending runtimecore.PendingExec
	found := false
	for _, item := range stream.PendingExecs {
		if strings.TrimSpace(item.ExecKind) == "execute_hook_pre_compact" {
			pending = item
			found = true
			break
		}
	}
	stream.mu.Unlock()
	if !found {
		return false
	}
	markExecCompleted(stream, pending)
	if err := service.handlePreCompactTerminal(stream, pending.ProviderPass, ""); err != nil {
		t.Fatalf("handlePreCompactTerminal() error = %v", err)
	}
	return true
}

func waitForOverflowRecoveryResume(t *testing.T, service *Service, stream *ActiveStream, failedModelCallID string, provider *overflowRecoveryTestProvider) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		acknowledgeCheckpointBlobs(t, service, stream)
		provider.mu.Lock()
		resumeSeen := false
		for _, req := range provider.requests {
			if strings.Contains(req.CompileSummary, "compaction") {
				continue
			}
			if strings.TrimSpace(req.ModelCallID) != "" && req.ModelCallID != failedModelCallID {
				resumeSeen = true
				break
			}
		}
		provider.mu.Unlock()
		stream.mu.Lock()
		status := stream.Status
		pending := stream.PendingCompaction != nil || len(stream.PendingCheckpointBlobWrites) > 0
		stream.mu.Unlock()
		if resumeSeen && !pending && isTerminalStreamStatus(status) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	provider.mu.Lock()
	requestCount := len(provider.requests)
	provider.mu.Unlock()
	stream.mu.Lock()
	status := stream.Status
	phase := stream.Phase
	current := stream.CurrentModelCallID
	pending := stream.PendingCompaction != nil
	writes := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()
	t.Fatalf("timed out waiting for overflow resume failed=%s current=%s requests=%d status=%s phase=%s pending=%v writes=%d", failedModelCallID, current, requestCount, status, phase, pending, writes)
}

func appendCompletedToolHistory(t *testing.T, service *Service, stream *ActiveStream) {
	t.Helper()
	toolCall := checkpointTestReadToolCall(t, nil)
	entries := []HistoryEntry{
		newToolCallEntry(1, "request-1", "call-completed", "Read", "", "", toolCall),
		newToolResultEntry(1, "request-1", "call-completed", "Read", `{"path":"/tmp/example.txt"}`, "file contents", "", toolCall),
	}
	persisted, _, err := service.store.AppendEntries(stream.ConversationID, resetEntrySequences(entries))
	if err != nil {
		t.Fatalf("AppendEntries() error = %v", err)
	}
	if err := service.replaceCheckpointConversation(stream, persisted); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}
}

func cloneHistoryEntries(entries []HistoryEntry) []HistoryEntry {
	if len(entries) == 0 {
		return nil
	}
	cloned := make([]HistoryEntry, len(entries))
	copy(cloned, entries)
	return cloned
}

func countHistoryKind(entries []HistoryEntry, kind string) int {
	count := 0
	for _, entry := range entries {
		if entry.Kind == kind {
			count++
		}
	}
	return count
}

func testOverflowRecoveryStream(t *testing.T) (*Service, *ActiveStream) {
	t.Helper()
	service, stream, _ := testCheckpointBlobProjection(t)
	service.execBridge = execbridge.NewBridge()
	service.compiler = compactionProjectionCompiler{projector: service.projector}
	service.usageStore = NewUsageFileStore(t.TempDir())
	conversation := compactionAppendOnlyConversation(t)
	conversation.ConversationID = stream.ConversationID
	conversation.RootConversationID = stream.ConversationID
	if _, err := service.store.SaveConversationWithEntries(conversation.ConversationID, conversation, resetEntrySequences(conversation.Entries)); err != nil {
		t.Fatalf("SaveConversationWithEntries() error = %v", err)
	}
	if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}
	stream.mu.Lock()
	stream.TurnSeq = 2
	stream.LatestUserText = "second question"
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-overflow"
	stream.ProviderPassCount = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.PendingExecs = make(map[string]runtimecore.PendingExec)
	stream.PendingInteractions = make(map[string]runtimecore.PendingInteraction)
	stream.PartialToolCallIDs = make(map[string]struct{})
	stream.mu.Unlock()
	return service, stream
}

type overflowRecoveryFailingCompiler struct {
	PromptCompiler
}

func (overflowRecoveryFailingCompiler) Compile(*ConversationFile, agentv1.AgentMode, string, string) (CompiledConversation, error) {
	return CompiledConversation{}, errors.New("injected compiler failure")
}

func assertOverflowRecoveryFailureContract(t *testing.T, service *Service, stream *ActiveStream, original []HistoryEntry, provider *overflowRecoveryTestProvider, wantCode string, wantMessage string, wantNonRetryable bool) {
	t.Helper()
	acknowledgeCheckpointBlobs(t, service, stream)
	stream.mu.Lock()
	secondToken := stream.CurrentProviderToken
	stream.mu.Unlock()
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: secondToken, Done: true, Err: testContextOverflowErr()}); err != nil {
		t.Fatalf("second handleProviderDoneEvent() error = %v", err)
	}
	acknowledgeCheckpointBlobs(t, service, stream)
	var terminals []StreamEvent
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if event.End && strings.TrimSpace(event.TerminalErrorCode) != "" {
			terminals = append(terminals, event)
		}
	}
	if len(terminals) != 1 {
		t.Fatalf("terminal events = %d, want exactly 1", len(terminals))
	}
	code := strings.TrimSpace(terminals[0].TerminalErrorCode)
	if code != wantCode {
		t.Fatalf("terminal code = %q, want %q", code, wantCode)
	}
	if wantCode != compactionOverflowTerminalCode && code == compactionOverflowTerminalCode {
		t.Fatal("ordinary failure surfaced as overflow terminal")
	}
	if wantMessage != "" && !strings.Contains(terminals[0].TerminalErrorMessage, wantMessage) {
		t.Fatalf("terminal message = %q, want substring %q", terminals[0].TerminalErrorMessage, wantMessage)
	}
	if wantNonRetryable && (terminals[0].TerminalRetryable == nil || *terminals[0].TerminalRetryable) {
		t.Fatal("overflow terminal must be non-retryable")
	}
	stream.mu.Lock()
	pending := stream.PendingCompaction
	status := stream.Status
	after := []HistoryEntry(nil)
	if stream.CheckpointConversation != nil {
		after = cloneHistoryEntries(stream.CheckpointConversation.Entries)
	}
	stream.mu.Unlock()
	if pending != nil {
		t.Fatal("failed recovery started pending compaction")
	}
	if !isTerminalStreamStatus(status) {
		t.Fatalf("status = %q, want terminal", status)
	}
	if len(after) < len(original) {
		t.Fatalf("history truncated from %d to %d", len(original), len(after))
	}
	for index := range original {
		if after[index].Kind != original[index].Kind || after[index].TurnSeq != original[index].TurnSeq {
			t.Fatalf("canonical history prefix changed at %d", index)
		}
	}
	if countHistoryKind(after, "compacted_summary") != countHistoryKind(original, "compacted_summary") ||
		countHistoryKind(after, "compaction_request") != countHistoryKind(original, "compaction_request") {
		t.Fatal("compaction records were persisted after failed recovery")
	}
	if provider.requestCount() != 0 {
		t.Fatalf("provider retries = %d, want 0", provider.requestCount())
	}
}

type overflowRunSSEClient struct {
	service  *Service
	stream   *ActiveStream
	mu       sync.Mutex
	messages []*agentv1.AgentServerMessage
	errCh    <-chan error
}

func startOverflowRunSSE(t *testing.T, service *Service, requestID string) *overflowRunSSEClient {
	t.Helper()
	handler := connect.NewServerStreamHandler("/agent.v1.AgentService/RunSSE", service.RunSSE)
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	client := connect.NewClient[aiserverv1.BidiRequestId, agentv1.AgentServerMessage](
		server.Client(),
		server.URL+"/agent.v1.AgentService/RunSSE",
	)
	sse, err := client.CallServerStream(ctx, connect.NewRequest(&aiserverv1.BidiRequestId{RequestId: requestID}))
	if err != nil {
		t.Fatalf("RunSSE CallServerStream() error = %v", err)
	}
	t.Cleanup(func() { _ = sse.Close() })
	errCh := make(chan error, 1)
	result := &overflowRunSSEClient{service: service, errCh: errCh}
	go func() {
		defer close(errCh)
		for sse.Receive() {
			msg := sse.Msg()
			if msg == nil {
				continue
			}
			cloned, _ := proto.Clone(msg).(*agentv1.AgentServerMessage)
			result.mu.Lock()
			result.messages = append(result.messages, cloned)
			result.mu.Unlock()
		}
		errCh <- sse.Err()
	}()
	stream, ok := service.broker.Get(requestID)
	if !ok {
		t.Fatal("RunSSE did not attach to the active stream")
	}
	result.stream = stream
	return result
}

func overflowRunSSEText(client *overflowRunSSEClient) string {
	if client == nil {
		return ""
	}
	client.mu.Lock()
	defer client.mu.Unlock()
	var builder strings.Builder
	for _, message := range client.messages {
		if message == nil {
			continue
		}
		update := message.GetInteractionUpdate()
		if update == nil || update.GetTextDelta() == nil {
			continue
		}
		builder.WriteString(update.GetTextDelta().GetText())
	}
	return builder.String()
}

func waitForOverflowRunSSEResult(t *testing.T, client *overflowRunSSEClient, wantErr bool) error {
	t.Helper()
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		if client.service != nil && client.stream != nil {
			acknowledgeCheckpointBlobs(t, client.service, client.stream)
		}
		select {
		case err := <-client.errCh:
			if wantErr && err == nil {
				t.Fatal("RunSSE returned nil, want overflow terminal")
			}
			if !wantErr && err != nil {
				t.Fatalf("RunSSE error = %v", err)
			}
			return err
		default:
			time.Sleep(10 * time.Millisecond)
		}
	}
	t.Fatal("timed out waiting for RunSSE")
	return nil
}

func prepareUnderBudgetOverflowDrive(t *testing.T, service *Service, stream *ActiveStream) {
	t.Helper()
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	conversation.TokenDetailsUsedTokens = 1000
	if err := service.replaceCheckpointConversation(stream, conversation); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}
	stream.mu.Lock()
	stream.ProviderActive = false
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseIdle
	stream.PendingProviderAction = providerActionNone
	stream.mu.Unlock()
}

func overflowUsageFinished(inputTokens int64, outputTokens int64) modeladapter.ModelEvent {
	return modeladapter.ModelEvent{
		Kind:         modeladapter.ModelEventKindTurnFinished,
		FinishReason: "stop",
		UsagePresent: true,
		InputTokens:  inputTokens,
		OutputTokens: outputTokens,
	}
}

func newOverflowRecoveryChainProvider(resumeOverflow bool) *overflowRecoveryTestProvider {
	provider := &overflowRecoveryTestProvider{}
	provider.handler = func(_ context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		if strings.Contains(req.CompileSummary, "compaction") {
			if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "compacted earlier turns"}); err != nil {
				return err
			}
			return sink(overflowUsageFinished(11, 7))
		}
		nonCompaction := 0
		provider.mu.Lock()
		for _, item := range provider.requests {
			if !strings.Contains(item.CompileSummary, "compaction") {
				nonCompaction++
			}
		}
		provider.mu.Unlock()
		if nonCompaction == 1 || resumeOverflow {
			if err := sink(overflowUsageFinished(3, 1)); err != nil {
				return err
			}
			return testContextOverflowErr()
		}
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "recovered answer"}); err != nil {
			return err
		}
		return sink(overflowUsageFinished(13, 5))
	}
	return provider
}

func overflowProviderCalls(provider *overflowRecoveryTestProvider) (failed ProviderRequest, summary ProviderRequest, resume ProviderRequest) {
	if provider == nil {
		return
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	for _, req := range provider.requests {
		if strings.Contains(req.CompileSummary, "compaction") {
			if strings.TrimSpace(summary.ModelCallID) == "" {
				summary = req
			}
			continue
		}
		if strings.TrimSpace(failed.ModelCallID) == "" {
			failed = req
			continue
		}
		if strings.TrimSpace(resume.ModelCallID) == "" {
			resume = req
		}
	}
	return
}

func nonCompactionProviderCount(provider *overflowRecoveryTestProvider) int {
	if provider == nil {
		return 0
	}
	provider.mu.Lock()
	defer provider.mu.Unlock()
	count := 0
	for _, req := range provider.requests {
		if !strings.Contains(req.CompileSummary, "compaction") {
			count++
		}
	}
	return count
}

func waitForOverflowResumeRequest(t *testing.T, service *Service, stream *ActiveStream, provider *overflowRecoveryTestProvider, overflowToken uint64) ProviderRequest {
	t.Helper()
	injectedStale := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_ = tryCompleteOverflowPreCompactHook(t, service, stream)
		stream.mu.Lock()
		accepted := stream.OverflowRecoveryAttempted
		currentToken := stream.CurrentProviderToken
		stream.mu.Unlock()
		if accepted && !injectedStale && currentToken != overflowToken {
			injectOverflowStaleEvents(t, service, stream, overflowToken, 9999)
			injectedStale = true
		}
		_, _, resume := overflowProviderCalls(provider)
		if strings.TrimSpace(resume.ModelCallID) != "" {
			return resume
		}
		time.Sleep(10 * time.Millisecond)
	}
	provider.mu.Lock()
	requestCount := len(provider.requests)
	provider.mu.Unlock()
	stream.mu.Lock()
	status := stream.Status
	phase := stream.Phase
	pending := stream.PendingCompaction != nil
	writes := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()
	t.Fatalf("timed out waiting for overflow resume without ACK requests=%d status=%s phase=%s pending=%v writes=%d", requestCount, status, phase, pending, writes)
	return ProviderRequest{}
}

func injectOverflowStaleEvents(t *testing.T, service *Service, stream *ActiveStream, providerToken uint64, compactionToken uint64) {
	t.Helper()
	if err := service.handleProviderEvent(stream, &streamProviderEvent{
		Token: providerToken,
		Event: modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "stale-provider"},
	}); err != nil {
		t.Fatalf("stale provider event error = %v", err)
	}
	if err := service.handleCompactionEvent(stream, &streamCompactionEvent{
		Token:       compactionToken,
		SummaryText: "stale-summary",
	}); err != nil {
		t.Fatalf("stale compaction event error = %v", err)
	}
}

func assertOverflowUsageRow(t *testing.T, service *Service, requestID string, modelCallID string, inputTokens int64, outputTokens int64) {
	t.Helper()
	if service == nil || service.usageStore == nil {
		t.Fatal("usage store is required")
	}
	event, ok, err := service.usageStore.LookupEvent(usageEventID(requestID, modelCallID))
	if err != nil || !ok {
		t.Fatalf("usage lookup model_call_id=%s ok=%v err=%v", modelCallID, ok, err)
	}
	if event.InputTokens != inputTokens || event.OutputTokens != outputTokens {
		t.Fatalf("usage model_call_id=%s input=%d output=%d, want %d/%d", modelCallID, event.InputTokens, event.OutputTokens, inputTokens, outputTokens)
	}
	if !event.UsagePresent {
		t.Fatalf("usage model_call_id=%s missing UsagePresent", modelCallID)
	}
}
