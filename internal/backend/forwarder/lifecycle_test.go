package forwarder

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"cursor/gen/agentv1"
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