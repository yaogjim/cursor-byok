package forwarder

import (
	"context"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestBrokerCancelActiveProvidersLeavesIdleStreams(t *testing.T) {
	broker := NewStreamBroker()
	idle, err := broker.OpenStream("idle", "conv", 1, "model", "name", agentv1.AgentMode_AGENT_MODE_AGENT, "hi")
	if err != nil || idle == nil {
		t.Fatalf("OpenStream(idle) error = %v stream=%v", err, idle)
	}
	var canceled atomic.Bool
	active, err := broker.OpenStream("active", "conv", 1, "model", "name", agentv1.AgentMode_AGENT_MODE_AGENT, "hi")
	if err != nil || active == nil {
		t.Fatalf("OpenStream(active) error = %v stream=%v", err, active)
	}
	active.mu.Lock()
	active.ProviderActive = true
	active.ProviderCancel = func() { canceled.Store(true) }
	active.mu.Unlock()
	if got := broker.ActiveProviderCount(); got != 1 {
		t.Fatalf("ActiveProviderCount() = %d, want 1", got)
	}
	if got := broker.CancelActiveProviders("shutdown reason=app_quit"); got != 1 {
		t.Fatalf("CancelActiveProviders() = %d, want 1", got)
	}
	if !canceled.Load() {
		t.Fatal("active provider was not canceled")
	}
	if got := broker.ActiveProviderCount(); got != 0 {
		t.Fatalf("ActiveProviderCount() after cancel = %d", got)
	}
	idle.mu.Lock()
	idleStatus := idle.Status
	idle.mu.Unlock()
	if idleStatus == StreamStatusCanceled {
		t.Fatal("idle stream was canceled")
	}
}

func TestBrokerWaitForIdleReturnsWhenProviderFinishes(t *testing.T) {
	broker := NewStreamBroker()
	stream, err := broker.OpenStream("wait", "conv", 1, "model", "name", agentv1.AgentMode_AGENT_MODE_AGENT, "hi")
	if err != nil || stream == nil {
		t.Fatalf("OpenStream() error = %v stream=%v", err, stream)
	}
	stream.mu.Lock()
	stream.ProviderActive = true
	stream.ProviderCancel = func() {}
	stream.mu.Unlock()
	go func() {
		time.Sleep(20 * time.Millisecond)
		stream.mu.Lock()
		stream.ProviderActive = false
		stream.ProviderCancel = nil
		stream.mu.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	broker.WaitForIdle(ctx)
	if broker.ActiveProviderCount() != 0 {
		t.Fatal("WaitForIdle returned while provider still active")
	}
}

func TestUnknownToolInvocationRecordsFailedResultWithoutPendingOrFailedStream(t *testing.T) {
	service, stream, _ := testCheckpointBlobProjection(t)
	stream.mu.Lock()
	stream.CurrentProviderToken = 1
	stream.CurrentModelCallID = "model-call-1"
	stream.ProviderActive = true
	stream.Status = StreamStatusStreaming
	stream.Phase = TurnPhaseProviderRunning
	stream.mu.Unlock()

	err := service.handleToolInvocation(stream, runtimecore.ToolInvocation{
		CallID:      "call-mystery",
		ToolName:    "MysteryRemovedTool",
		ArgsJSON:    []byte(`{}`),
		ModelCallID: "model-call-1",
	})
	if err != nil {
		t.Fatalf("handleToolInvocation() error = %v", err)
	}

	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	foundFailedResult := false
	for _, entry := range conversation.Entries {
		if entry.Kind != "tool_result" || entry.ToolCallID != "call-mystery" {
			continue
		}
		payload := string(entry.Payload)
		if !strings.Contains(payload, "MysteryRemovedTool") || !strings.Contains(payload, "nonexistent tool") {
			t.Fatalf("tool_result payload = %s, want failed unknown-tool result", payload)
		}
		foundFailedResult = true
	}
	if !foundFailedResult {
		t.Fatal("missing failed tool_result for MysteryRemovedTool")
	}

	foundStarted := false
	foundCompleted := false
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		interaction := event.Message.GetInteractionUpdate()
		if interaction == nil {
			continue
		}
		if started := interaction.GetToolCallStarted(); started != nil && started.GetCallId() == "call-mystery" {
			foundStarted = true
		}
		if completed := interaction.GetToolCallCompleted(); completed != nil && completed.GetCallId() == "call-mystery" {
			foundCompleted = true
		}
	}
	if !foundStarted {
		t.Fatal("missing ToolCallStarted for MysteryRemovedTool")
	}
	if !foundCompleted {
		t.Fatal("missing ToolCallCompleted for MysteryRemovedTool")
	}

	acknowledgeCheckpointBlobs(t, service, stream)
	foundCheckpoint := false
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if event.Message.GetConversationCheckpointUpdate() != nil {
			foundCheckpoint = true
			break
		}
	}
	if !foundCheckpoint {
		t.Fatal("missing ConversationCheckpointUpdate after unknown-tool checkpoint ACK")
	}

	stream.mu.Lock()
	pending := len(stream.PendingExecs) + len(stream.PendingInteractions)
	status := stream.Status
	stream.mu.Unlock()
	if pending != 0 {
		t.Fatalf("pending execs/interactions = %d, want 0", pending)
	}
	if status == StreamStatusFailed {
		t.Fatalf("stream status = %s, unknown tool must not fail the stream", status)
	}
}

func TestLateProviderDoneEventOnTerminalStreamKeepsStatusAndDoesNotAppendSecondEnd(t *testing.T) {
	cases := []struct {
		name   string
		status StreamStatus
		term   func(*testing.T, *Service, *ActiveStream)
	}{
		{"canceled", StreamStatusCanceled, func(t *testing.T, service *Service, stream *ActiveStream) {
			t.Helper()
			if err := service.broker.Cancel(stream.RequestID, "user stopped"); err != nil {
				t.Fatalf("Cancel() error = %v", err)
			}
		}},
		{"failed", StreamStatusFailed, func(t *testing.T, service *Service, stream *ActiveStream) {
			t.Helper()
			if err := service.broker.Fail(stream.RequestID, "provider_error", "provider failed"); err != nil {
				t.Fatalf("Fail() error = %v", err)
			}
		}},
		{"completed", StreamStatusCompleted, func(t *testing.T, service *Service, stream *ActiveStream) {
			t.Helper()
			if err := service.broker.Complete(stream.RequestID, "", ""); err != nil {
				t.Fatalf("Complete() error = %v", err)
			}
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, stream, _ := testCheckpointBlobProjection(t)
			stream.mu.Lock()
			stream.CurrentProviderToken = 1
			stream.CurrentModelCallID = "model-call-1"
			stream.ProviderActive = true
			stream.Status = StreamStatusStreaming
			stream.Phase = TurnPhaseProviderRunning
			stream.mu.Unlock()

			test.term(t, service, stream)
			if got := countStreamEndEvents(t, service, stream); got != 1 {
				t.Fatalf("end events after %s = %d, want 1", test.name, got)
			}

			if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: 1, Done: true}); err != nil {
				t.Fatalf("late handleProviderDoneEvent() error = %v", err)
			}
			stream.mu.Lock()
			status := stream.Status
			stream.mu.Unlock()
			if status != test.status {
				t.Fatalf("status after late done = %s, want %s", status, test.status)
			}
			if got := countStreamEndEvents(t, service, stream); got != 1 {
				t.Fatalf("end events after late done = %d, want 1", got)
			}
		})
	}
}

func TestTerminalStreamStatusIsIrreversible(t *testing.T) {
	cases := []struct {
		name   string
		first  func(*testing.T, *Service, *ActiveStream)
		late   func(*testing.T, *Service, *ActiveStream)
		status StreamStatus
		phase  TurnPhase
	}{
		{
			name:   "completed_then_cancel_intent",
			status: StreamStatusCompleted,
			phase:  TurnPhaseProviderRunning,
			first: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Complete(stream.RequestID, "", ""); err != nil {
					t.Fatalf("Complete() error = %v", err)
				}
			},
			late: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.handleCancelIntent(InboundIntent{RequestID: stream.RequestID, CancelReason: "late stop"}); err != nil {
					t.Fatalf("handleCancelIntent() error = %v", err)
				}
			},
		},
		{
			name:   "completed_then_fail",
			status: StreamStatusCompleted,
			phase:  TurnPhaseProviderRunning,
			first: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Complete(stream.RequestID, "", ""); err != nil {
					t.Fatalf("Complete() error = %v", err)
				}
			},
			late: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Fail(stream.RequestID, "provider_error", "late fail"); err != nil {
					t.Fatalf("Fail() error = %v", err)
				}
			},
		},
		{
			name:   "canceled_then_cancel",
			status: StreamStatusCanceled,
			phase:  TurnPhaseProviderRunning,
			first: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Cancel(stream.RequestID, "user stopped"); err != nil {
					t.Fatalf("Cancel() error = %v", err)
				}
			},
			late: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Cancel(stream.RequestID, "second cancel"); err != nil {
					t.Fatalf("late Cancel() error = %v", err)
				}
			},
		},
		{
			name:   "canceled_then_fail",
			status: StreamStatusCanceled,
			phase:  TurnPhaseProviderRunning,
			first: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Cancel(stream.RequestID, "user stopped"); err != nil {
					t.Fatalf("Cancel() error = %v", err)
				}
			},
			late: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Fail(stream.RequestID, "provider_error", "late fail"); err != nil {
					t.Fatalf("Fail() error = %v", err)
				}
			},
		},
		{
			name:   "failed_then_fail",
			status: StreamStatusFailed,
			phase:  TurnPhaseProviderRunning,
			first: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Fail(stream.RequestID, "provider_error", "provider failed"); err != nil {
					t.Fatalf("Fail() error = %v", err)
				}
			},
			late: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Fail(stream.RequestID, "provider_error", "second fail"); err != nil {
					t.Fatalf("late Fail() error = %v", err)
				}
			},
		},
		{
			name:   "failed_then_cancel",
			status: StreamStatusFailed,
			phase:  TurnPhaseProviderRunning,
			first: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Fail(stream.RequestID, "provider_error", "provider failed"); err != nil {
					t.Fatalf("Fail() error = %v", err)
				}
			},
			late: func(t *testing.T, service *Service, stream *ActiveStream) {
				t.Helper()
				if err := service.broker.Cancel(stream.RequestID, "late cancel"); err != nil {
					t.Fatalf("Cancel() error = %v", err)
				}
			},
		},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			service, stream, _ := testCheckpointBlobProjection(t)
			stream.mu.Lock()
			stream.CurrentProviderToken = 1
			stream.CurrentModelCallID = "model-call-1"
			stream.ProviderActive = true
			stream.Status = StreamStatusStreaming
			stream.Phase = TurnPhaseProviderRunning
			stream.mu.Unlock()

			test.first(t, service, stream)
			if got := countStreamEndEvents(t, service, stream); got != 1 {
				t.Fatalf("end events after first terminal = %d, want 1", got)
			}

			test.late(t, service, stream)
			stream.mu.Lock()
			status := stream.Status
			phase := stream.Phase
			stream.mu.Unlock()
			if status != test.status {
				t.Fatalf("status after late op = %s, want %s", status, test.status)
			}
			if phase != test.phase {
				t.Fatalf("phase after late op = %s, want %s", phase, test.phase)
			}
			if got := countStreamEndEvents(t, service, stream); got != 1 {
				t.Fatalf("end events after late op = %d, want 1", got)
			}

			if test.name == "completed_then_cancel_intent" {
				conversation, _, _, err := service.snapshotCheckpointConversation(stream)
				if err != nil {
					t.Fatalf("snapshotCheckpointConversation() error = %v", err)
				}
				for _, entry := range conversation.Entries {
					if entry.Kind == "metadata" && strings.Contains(string(entry.Payload), `"status":"canceled"`) {
						t.Fatalf("late cancel polluted checkpoint metadata: %s", entry.Payload)
					}
				}
			}
		})
	}
}

func countStreamEndEvents(t *testing.T, service *Service, stream *ActiveStream) int {
	t.Helper()
	count := 0
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if event.End {
			count++
		}
	}
	return count
}
