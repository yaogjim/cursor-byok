package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/observability"
)

type continuationTestCompiler struct{}

func (continuationTestCompiler) Compile(conversation *ConversationFile, mode agentv1.AgentMode, latestUserText string, _ string) (CompiledConversation, error) {
	messages := []modeladapter.Message{{Role: "user", Content: firstNonEmpty(latestUserText, "continue")}}
	if conversation != nil {
		for _, entry := range conversation.Entries {
			if strings.TrimSpace(entry.Kind) != "prompt_context" {
				continue
			}
			var payload promptContextEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				continue
			}
			if strings.TrimSpace(payload.Source) == promptContextSourceStreamContinuation {
				messages = append(messages, modeladapter.Message{Role: firstNonEmpty(payload.Role, "user"), Content: payload.Content})
			}
		}
	}
	return CompiledConversation{
		Mode:     mode,
		Messages: messages,
		Tools:    []json.RawMessage{json.RawMessage(`{"name":"Shell"}`)},
	}, nil
}

func (continuationTestCompiler) DerivePromptContexts(*ConversationFile, agentv1.AgentMode, string) ([]PromptContextMessage, error) {
	return nil, nil
}

type continuationTestProvider struct {
	mu       sync.Mutex
	requests []ProviderRequest
	handler  func(context.Context, ProviderRequest, func(modeladapter.ModelEvent) error) error
}

func (provider *continuationTestProvider) StartStream(ctx context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
	provider.mu.Lock()
	provider.requests = append(provider.requests, req)
	handler := provider.handler
	provider.mu.Unlock()
	if handler != nil {
		return handler(ctx, req, sink)
	}
	return nil
}

func (provider *continuationTestProvider) requestCount() int {
	provider.mu.Lock()
	defer provider.mu.Unlock()
	return len(provider.requests)
}

func continuationFixture(t *testing.T, enabled bool) (*Service, *ActiveStream, *continuationTestProvider, *debugRecorderTestCapture) {
	t.Helper()
	service, stream, _ := testCheckpointBlobProjection(t)
	provider := &continuationTestProvider{}
	service.compiler = continuationTestCompiler{}
	service.provider = provider
	service.usageStore = NewUsageFileStore(t.TempDir())
	service.testStreamContinuation = &StreamContinuationSettings{
		Enabled:            enabled,
		MaxPerTurn:         1,
		TotalDeadline:      time.Minute,
		OverlapWindowChars: 2048,
	}
	capture := &debugRecorderTestCapture{}
	service.debug = newDebugRecorder(t.TempDir(), service.broker, debugRecorderTestConfig("basic"), capture)
	t.Cleanup(service.debug.Close)
	return service, stream, provider, capture
}

func prepareTruncatedParent(t *testing.T, service *Service, stream *ActiveStream, text string) string {
	t.Helper()
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-parent"
	stream.ProviderPassCount = 1
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.TurnProviderStartedAt = time.Now().UTC().Add(-time.Second)
	stream.ProviderStreamStats = ProviderStreamStats{
		Attempt:             1,
		ProtocolFinalStatus: "streaming",
		ToolDispatchState:   "not_dispatched",
		PotentialSideEffect: "none",
	}
	stream.mu.Unlock()
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind:       modeladapter.ModelEventKindTextDelta,
		Text:       text,
		OccurredAt: time.Now(),
	}); err != nil {
		t.Fatalf("apply parent text: %v", err)
	}
	return "model-call-parent"
}

func waitForContinuationTurn(t *testing.T, service *Service, stream *ActiveStream, parentModelCallID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		acknowledgeCheckpointBlobs(t, service, stream)
		stream.mu.Lock()
		phase := stream.Phase
		active := stream.ProviderActive
		index := stream.ContinuationIndex
		childID := stream.CurrentModelCallID
		status := stream.Status
		pending := stream.PendingCheckpoint != nil || len(stream.PendingCheckpointBlobWrites) > 0
		actorDone := stream.ActorDone
		stream.mu.Unlock()
		if index == 1 && childID != parentModelCallID && !active && !pending {
			terminal := isTerminalStreamStatus(status)
			switch phase {
			case TurnPhaseCompleted, TurnPhaseFailed, TurnPhaseCanceled:
				terminal = true
			}
			if terminal {
				if actorDone == nil {
					return
				}
				select {
				case <-actorDone:
					return
				default:
				}
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	stream.mu.Lock()
	phase := stream.Phase
	active := stream.ProviderActive
	index := stream.ContinuationIndex
	childID := stream.CurrentModelCallID
	status := stream.Status
	pending := stream.PendingCheckpoint != nil
	writes := len(stream.PendingCheckpointBlobWrites)
	stream.mu.Unlock()
	t.Fatalf("timed out waiting for continuation turn parent=%s child=%s index=%d active=%v phase=%s status=%s pending=%v writes=%d", parentModelCallID, childID, index, active, phase, status, pending, writes)
}

func publishedAssistantText(t *testing.T, service *Service, stream *ActiveStream) string {
	t.Helper()
	events := readCheckpointTestEvents(t, service, stream)
	var builder strings.Builder
	for _, event := range events {
		if event.Message == nil {
			continue
		}
		update := event.Message.GetInteractionUpdate()
		if update == nil || update.GetTextDelta() == nil {
			continue
		}
		builder.WriteString(update.GetTextDelta().GetText())
	}
	return builder.String()
}

func TestLongestParentSuffixPrefix(t *testing.T) {
	t.Parallel()
	if got := longestParentSuffixPrefix("Hello world", "Hello world, more", 2048); got != len("Hello world") {
		t.Fatalf("overlap = %d, want %d", got, len("Hello world"))
	}
	if got := longestParentSuffixPrefix("abc def ghi", "def ghi!", 2048); got != len("def ghi") {
		t.Fatalf("suffix overlap = %d, want %d", got, len("def ghi"))
	}
	if got := longestParentSuffixPrefix("parent", "unrelated", 2048); got != 0 {
		t.Fatalf("mismatch overlap = %d, want 0", got)
	}
	if got := longestParentSuffixPrefix("", "new", 2048); got != 0 {
		t.Fatalf("empty parent overlap = %d, want 0", got)
	}
}

func TestEvaluateStreamContinuationEligibilityGates(t *testing.T) {
	t.Parallel()
	base := streamContinuationEvalInput{
		Settings:            StreamContinuationSettings{Enabled: true, MaxPerTurn: 1, TotalDeadline: time.Minute},
		Now:                 time.Now().UTC(),
		TurnStartedAt:       time.Now().UTC().Add(-time.Second),
		Status:              StreamStatusStreaming,
		ProtocolFinalStatus: "truncated",
		ParentText:          "partial answer",
		ToolDispatchState:   "not_dispatched",
		PotentialSideEffect: "none",
	}
	if decision := evaluateStreamContinuationEligibility(base); !decision.Eligible {
		t.Fatalf("base eligible = %+v", decision)
	}
	cases := []struct {
		name   string
		mutate func(*streamContinuationEvalInput)
		reason string
	}{
		{name: "disabled", mutate: func(input *streamContinuationEvalInput) { input.Settings.Enabled = false }, reason: continuationReasonDisabled},
		{name: "nested", mutate: func(input *streamContinuationEvalInput) { input.ContinuationIndex = 1 }, reason: continuationReasonNested},
		{name: "spawned", mutate: func(input *streamContinuationEvalInput) { input.ContinuationSpawned = true }, reason: continuationReasonSpawned},
		{name: "no content", mutate: func(input *streamContinuationEvalInput) { input.ParentText = "" }, reason: continuationReasonNoPersistableContent},
		{name: "partial tool", mutate: func(input *streamContinuationEvalInput) {
			input.PartialToolCount = 1
			input.ToolDispatchState = "partial_not_dispatched"
		}, reason: continuationReasonToolProgress},
		{name: "side effect", mutate: func(input *streamContinuationEvalInput) { input.PotentialSideEffect = "possible" }, reason: continuationReasonSideEffect},
		{name: "pending interaction", mutate: func(input *streamContinuationEvalInput) { input.PendingInteractionCount = 1 }, reason: continuationReasonPendingInteraction},
		{name: "checkpoint", mutate: func(input *streamContinuationEvalInput) { input.CheckpointCommitted = true }, reason: continuationReasonCheckpoint},
		{name: "cancel", mutate: func(input *streamContinuationEvalInput) { input.ClientCanceled = true }, reason: continuationReasonClientCancel},
		{name: "deadline", mutate: func(input *streamContinuationEvalInput) { input.DeadlineExceeded = true }, reason: continuationReasonDeadline},
		{name: "subagent", mutate: func(input *streamContinuationEvalInput) { input.SubagentChild = true }, reason: continuationReasonSubagentChild},
		{name: "gateway chat", mutate: func(input *streamContinuationEvalInput) { input.StreamSource = streamSourceGatewayChat }, reason: continuationReasonGatewayPath},
		{name: "gateway responses", mutate: func(input *streamContinuationEvalInput) { input.StreamSource = streamSourceGatewayResponses }, reason: continuationReasonGatewayPath},
		{name: "non run sse", mutate: func(input *streamContinuationEvalInput) { input.StreamSource = "acp" }, reason: continuationReasonNonRunSSE},
		{name: "completed", mutate: func(input *streamContinuationEvalInput) { input.ProtocolFinalStatus = "completed" }, reason: continuationReasonCompleted},
		{name: "turn deadline", mutate: func(input *streamContinuationEvalInput) {
			input.TurnStartedAt = input.Now.Add(-2 * time.Minute)
			input.Settings.TotalDeadline = time.Minute
		}, reason: continuationReasonTurnDeadline},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			input := base
			test.mutate(&input)
			decision := evaluateStreamContinuationEligibility(input)
			if decision.Eligible || decision.Reason != test.reason {
				t.Fatalf("decision = %+v, want ineligible reason %q", decision, test.reason)
			}
		})
	}
	reasoningOnly := base
	reasoningOnly.ParentText = ""
	reasoningOnly.ParentReasoning = "partial thinking"
	if decision := evaluateStreamContinuationEligibility(reasoningOnly); !decision.Eligible {
		t.Fatalf("reasoning-only eligible = %+v", decision)
	}
	signatureOnly := base
	signatureOnly.ParentText = ""
	signatureOnly.ParentReasoningSignature = "sig"
	signatureOnly.ParentReasoningSignatureSource = modeladapter.ReasoningSignatureSourceOpenAIResponses
	if decision := evaluateStreamContinuationEligibility(signatureOnly); !decision.Eligible {
		t.Fatalf("signature-only eligible = %+v", decision)
	}
}

func TestStreamContinuationDefaultDisabled(t *testing.T) {
	service, stream, provider, capture := continuationFixture(t, false)
	parentID := prepareTruncatedParent(t, service, stream, "partial answer")
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("handleProviderDoneEvent() error = %v", err)
	}
	if provider.requestCount() != 0 {
		t.Fatalf("disabled continuation issued %d child requests", provider.requestCount())
	}
	stream.mu.Lock()
	index := stream.ContinuationIndex
	current := stream.CurrentModelCallID
	stream.mu.Unlock()
	if index != 0 || current != parentID {
		t.Fatalf("disabled continuation mutated stream index=%d current=%q", index, current)
	}
	if countCapturedEvent(capture, "stream_continuation_spawned") != 0 {
		t.Fatal("disabled continuation recorded a spawn")
	}
}

func TestStreamContinuationSpawnsOnceWithNewModelCallID(t *testing.T) {
	service, stream, provider, capture := continuationFixture(t, true)
	parentText := "Hello world from parent"
	childRemainder := " and the rest of the answer."
	provider.handler = func(_ context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		if strings.TrimSpace(req.ModelCallID) == "" || req.ModelCallID == "model-call-parent" {
			t.Errorf("child reused parent model_call_id %q", req.ModelCallID)
		}
		if len(req.Tools) != 0 {
			t.Errorf("child request advertised %d tools, want 0", len(req.Tools))
		}
		foundPrompt := false
		for _, message := range req.Messages {
			if strings.Contains(message.Content, "Continue ONLY from the breakpoint") {
				foundPrompt = true
			}
		}
		if !foundPrompt {
			t.Error("child request missing continuation breakpoint prompt")
		}
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: parentText + childRemainder}); err != nil {
			return err
		}
		return sink(modeladapter.ModelEvent{
			Kind:         modeladapter.ModelEventKindTurnFinished,
			FinishReason: "stop",
			UsagePresent: true,
			InputTokens:  3,
			OutputTokens: 5,
		})
	}
	parentID := prepareTruncatedParent(t, service, stream, parentText)
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("parent handleProviderDoneEvent() error = %v", err)
	}
	waitForContinuationTurn(t, service, stream, parentID)
	if provider.requestCount() != 1 {
		t.Fatalf("child provider requests = %d, want 1", provider.requestCount())
	}
	stream.mu.Lock()
	childID := stream.CurrentModelCallID
	index := stream.ContinuationIndex
	continuedFrom := stream.ContinuedFromModelCallID
	status := stream.Status
	stream.mu.Unlock()
	if index != 1 || continuedFrom != parentID || childID == parentID || childID == "" {
		t.Fatalf("parent/child identity index=%d from=%q child=%q parent=%q", index, continuedFrom, childID, parentID)
	}
	if isTerminalStreamStatus(status) && status == StreamStatusFailed {
		t.Fatalf("successful continuation left stream failed: %s", status)
	}
	if countCapturedEvent(capture, "stream_continuation_spawned") != 1 {
		t.Fatalf("spawn events = %d, want 1", countCapturedEvent(capture, "stream_continuation_spawned"))
	}
	spawned := capturedEventByName(t, capture, "stream_continuation_spawned")
	spawnPayload, _ := spawned.Payload.Data.(map[string]any)
	if spawnPayload["continued_from_model_call_id"] != parentID {
		t.Fatalf("spawn parent = %#v", spawnPayload)
	}
	if spawnPayload["continuation_index"] != 1 && spawnPayload["continuation_index"] != int64(1) && spawnPayload["continuation_index"] != float64(1) {
		t.Fatalf("spawn index = %#v, want 1", spawnPayload["continuation_index"])
	}
	if strings.TrimSpace(fmt.Sprint(spawnPayload["reason"])) == "" {
		t.Fatalf("spawn missing reason: %#v", spawnPayload)
	}
	if got := publishedAssistantText(t, service, stream); !strings.Contains(got, childRemainder) || strings.Count(got, parentText) != 1 {
		t.Fatalf("downstream text = %q, want parent once plus remainder", got)
	}

	parentUsage, ok, err := service.usageStore.LookupEvent(usageEventID(stream.RequestID, parentID))
	if err != nil || !ok {
		t.Fatalf("parent usage lookup ok=%v err=%v", ok, err)
	}
	childUsage, ok, err := service.usageStore.LookupEvent(usageEventID(stream.RequestID, childID))
	if err != nil || !ok {
		t.Fatalf("child usage lookup ok=%v err=%v", ok, err)
	}
	if parentUsage.EventID == childUsage.EventID {
		t.Fatal("parent and child usage events collapsed")
	}
	if childUsage.OutputTokens != 5 {
		t.Fatalf("child output tokens = %d, want 5", childUsage.OutputTokens)
	}
	finals := 0
	var parentFinal observability.Capture
	var childFinal observability.Capture
	for _, captured := range capture.captures {
		if captured.Event.Event != "model_call_final" {
			continue
		}
		finals++
		payload, _ := captured.Payload.Data.(map[string]any)
		id, _ := payload["model_call_id"].(string)
		switch id {
		case parentID:
			parentFinal = captured
		case childID:
			childFinal = captured
		}
	}
	if finals < 2 {
		t.Fatalf("model_call_final count = %d, want at least 2", finals)
	}
	parentPayload, _ := parentFinal.Payload.Data.(map[string]any)
	if parentPayload["status"] != "partial" && parentPayload["business_outcome"] != "partial" {
		t.Fatalf("parent final = %#v, want partial", parentPayload)
	}
	childPayload, _ := childFinal.Payload.Data.(map[string]any)
	if childPayload["continued_from_model_call_id"] != parentID {
		t.Fatalf("child final missing parent link: %#v", childPayload)
	}
}

func TestStreamContinuationStripsLongestPrefixOverlap(t *testing.T) {
	service, stream, _, _ := continuationFixture(t, true)
	stream.mu.Lock()
	stream.ContinuationIndex = 1
	stream.ContinuationParentText = "Hello world"
	stream.ContinuationOverlapWindow = 2048
	stream.mu.Unlock()
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "Hello world, continued"}); err != nil {
		t.Fatalf("apply child overlap: %v", err)
	}
	if got := publishedAssistantText(t, service, stream); got != ", continued" {
		t.Fatalf("published = %q, want remainder only", got)
	}
	stream.mu.Lock()
	remainder := stream.ContinuationRemainderText
	bytes := stream.ContinuationNewVisibleBytes
	stream.mu.Unlock()
	if remainder != ", continued" || bytes != len(", continued") {
		t.Fatalf("remainder=%q bytes=%d", remainder, bytes)
	}
}

func TestStreamContinuationMismatchFailClosed(t *testing.T) {
	service, stream, _, capture := continuationFixture(t, true)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-child"
	stream.ProviderPassCount = 2
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.ContinuationIndex = 1
	stream.ContinuedFromModelCallID = "model-call-parent"
	stream.ContinuationParentText = "Hello world"
	stream.ContinuationOverlapWindow = 2048
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 2, ProtocolFinalStatus: "streaming", ContinuationIndex: 1, ContinuedFromModelCallID: "model-call-parent"}
	stream.mu.Unlock()
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "totally different"}); err != nil {
		t.Fatalf("apply mismatch: %v", err)
	}
	if got := publishedAssistantText(t, service, stream); strings.Contains(got, "totally different") {
		t.Fatalf("mismatch leaked child text %q", got)
	}
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("child done: %v", err)
	}
	if countCapturedEvent(capture, "stream_continuation_fused") == 0 {
		t.Fatal("mismatch did not fuse")
	}
	final := capturedEventByName(t, capture, "model_call_final")
	payload, _ := final.Payload.Data.(map[string]any)
	if payload["status"] != "partial" && payload["business_outcome"] != "partial" {
		t.Fatalf("mismatch final = %#v, want partial", payload)
	}
}

func TestStreamContinuationNoProgressFusesPartial(t *testing.T) {
	service, stream, provider, capture := continuationFixture(t, true)
	parentText := "unchanged answer"
	provider.handler = func(_ context.Context, _ ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: parentText}); err != nil {
			return err
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, FinishReason: "stop"})
	}
	parentID := prepareTruncatedParent(t, service, stream, parentText)
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("parent done: %v", err)
	}
	waitForContinuationTurn(t, service, stream, parentID)
	if provider.requestCount() != 1 {
		t.Fatalf("requests = %d, want 1", provider.requestCount())
	}
	if countCapturedEvent(capture, "stream_continuation_fused") == 0 {
		t.Fatal("no-progress child did not fuse")
	}
}

func TestStreamContinuationBlocksToolProgress(t *testing.T) {
	service, stream, provider, capture := continuationFixture(t, true)
	parentID := prepareTruncatedParent(t, service, stream, "before tool")
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind:       modeladapter.ModelEventKindPartialToolCall,
		ToolCallID: "tool-1",
		ToolCall:   &agentv1.ToolCall{},
	}); err != nil {
		t.Fatalf("partial tool: %v", err)
	}
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("parent done: %v", err)
	}
	if provider.requestCount() != 0 {
		t.Fatal("tool progress spawned a continuation")
	}
	stream.mu.Lock()
	current := stream.CurrentModelCallID
	stream.mu.Unlock()
	if current != parentID {
		t.Fatalf("model_call_id changed to %q", current)
	}
	if countCapturedEvent(capture, "stream_continuation_suppressed") == 0 {
		t.Fatal("missing suppression event")
	}
}

func TestStreamContinuationBlocksCancel(t *testing.T) {
	service, stream, provider, _ := continuationFixture(t, true)
	prepareTruncatedParent(t, service, stream, "canceled partial")
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true, Err: context.Canceled}); err != nil {
		t.Fatalf("canceled done: %v", err)
	}
	if provider.requestCount() != 0 {
		t.Fatal("client cancel spawned a continuation")
	}
}

func TestStreamContinuationBlocksExplicitProviderTerminal(t *testing.T) {
	service, stream, provider, capture := continuationFixture(t, true)
	prepareTruncatedParent(t, service, stream, "failed partial")
	terminal := &modeladapter.ProviderTerminalStatusError{Provider: "openai", Status: "failed", Message: "rejected"}
	_ = service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true, Err: terminal})
	if provider.requestCount() != 0 {
		t.Fatal("explicit provider terminal spawned a continuation")
	}
	if countCapturedEvent(capture, "stream_continuation_suppressed") == 0 {
		t.Fatal("missing non-recoverable continuation suppression event")
	}
	captured := capturedEventByName(t, capture, "stream_continuation_suppressed")
	payload, _ := captured.Payload.Data.(map[string]any)
	if payload["reason"] != continuationReasonNonRecoverable {
		t.Fatalf("suppression reason = %#v, want %q", payload["reason"], continuationReasonNonRecoverable)
	}
}

func TestStreamContinuationBlocksSubagentAndGateway(t *testing.T) {
	service, stream, provider, _ := continuationFixture(t, true)
	stream.mu.Lock()
	if stream.CheckpointConversation != nil {
		stream.CheckpointConversation.ParentConversationID = "parent-conv"
		stream.CheckpointConversation.SubagentTypeName = "explore"
	}
	stream.mu.Unlock()
	prepareTruncatedParent(t, service, stream, "subagent partial")
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("subagent done: %v", err)
	}
	if provider.requestCount() != 0 {
		t.Fatal("subagent child spawned a continuation")
	}

	service, stream, provider, _ = continuationFixture(t, true)
	stream.mu.Lock()
	stream.StreamSource = streamSourceGatewayChat
	stream.mu.Unlock()
	prepareTruncatedParent(t, service, stream, "gateway partial")
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("gateway done: %v", err)
	}
	if provider.requestCount() != 0 {
		t.Fatal("gateway path spawned a continuation")
	}
}

func TestRequestProviderActionContinueDoesNotReuseStartOrResume(t *testing.T) {
	service, stream, _, _ := continuationFixture(t, true)
	stream.mu.Lock()
	stream.Status = StreamStatusStreaming
	stream.ProviderActive = true
	stream.mu.Unlock()
	if err := service.requestProviderAction(stream, providerActionContinue); err != nil {
		t.Fatalf("request continue: %v", err)
	}
	stream.mu.Lock()
	pending := stream.PendingProviderAction
	stream.mu.Unlock()
	if pending != providerActionContinue {
		t.Fatalf("pending action = %q, want continue", pending)
	}
	if err := service.requestProviderAction(stream, providerActionResume); err != nil {
		t.Fatalf("request resume: %v", err)
	}
	stream.mu.Lock()
	pending = stream.PendingProviderAction
	stream.mu.Unlock()
	if pending != providerActionContinue {
		t.Fatalf("resume overwrote continue: %q", pending)
	}
}

func TestContinuationProviderContextHonorsDeadline(t *testing.T) {
	t.Parallel()
	ctx, cancel := continuationProviderContext(time.Now().UTC().Add(-time.Second))
	defer cancel()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("past deadline context was not done")
	}
	openCtx, openCancel := continuationProviderContext(time.Time{})
	defer openCancel()
	select {
	case <-openCtx.Done():
		t.Fatal("zero deadline context should stay open")
	default:
	}
}

func TestStreamContinuationChildToolProgressFusesWithoutDispatch(t *testing.T) {
	service, stream, _, capture := continuationFixture(t, true)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-child"
	stream.ProviderPassCount = 2
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.ContinuationIndex = 1
	stream.ContinuedFromModelCallID = "model-call-parent"
	stream.ContinuationParentText = "Hello world"
	stream.ContinuationOverlapWindow = 2048
	stream.ContinuationNewVisibleBytes = 4
	stream.ProviderStreamStats = ProviderStreamStats{Attempt: 2, ProtocolFinalStatus: "streaming", ContinuationIndex: 1, ContinuedFromModelCallID: "model-call-parent"}
	stream.mu.Unlock()
	if err := service.applyProviderModelEvent(stream, modeladapter.ModelEvent{
		Kind:       modeladapter.ModelEventKindPartialToolCall,
		ToolCallID: "tool-1",
		ToolCall:   &agentv1.ToolCall{},
	}); err != nil {
		t.Fatalf("apply child tool: %v", err)
	}
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if event.Message == nil {
			continue
		}
		update := event.Message.GetInteractionUpdate()
		if update == nil {
			continue
		}
		if update.GetPartialToolCall() != nil {
			t.Fatal("child tool event leaked downstream")
		}
	}
	stream.mu.Lock()
	abort := stream.ContinuationAbortReason
	stream.mu.Unlock()
	if abort != continuationReasonToolProgress {
		t.Fatalf("abort reason = %q, want %s", abort, continuationReasonToolProgress)
	}
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
		t.Fatalf("child done: %v", err)
	}
	if countCapturedEvent(capture, "stream_continuation_fused") == 0 {
		t.Fatal("child tool progress did not fuse")
	}
}
