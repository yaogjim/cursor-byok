package forwarder

import (
	"context"
	"errors"
	"sync"
	"testing"

	modeladapter "cursor/internal/backend/agent/model"
)

type providerGatewayTestRouter struct {
	stream func(context.Context, modeladapter.StreamRequest, func(modeladapter.ModelEvent) error) error
}

func (router providerGatewayTestRouter) Stream(ctx context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
	return router.stream(ctx, req, sink)
}

type providerGatewayTestObserver struct {
	mu         sync.Mutex
	active     map[string]bool
	clearCalls int
}

func newProviderGatewayTestObserver() *providerGatewayTestObserver {
	return &providerGatewayTestObserver{active: make(map[string]bool)}
}

func (observer *providerGatewayTestObserver) RecordLLMRequest(requestID string, _ string, modelCallID string, _ map[string]any) (string, error) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	observer.active[artifactSessionKey(requestID, modelCallID)] = true
	return "", nil
}

func (observer *providerGatewayTestObserver) AppendLLMResponseChunk(string, string, string, string) (string, error) {
	return "", nil
}

func (observer *providerGatewayTestObserver) RecordLLMSummary(string, string, string, map[string]any) (string, error) {
	return "", nil
}

func (observer *providerGatewayTestObserver) ClearActiveArtifacts(requestID string, modelCallID string) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	delete(observer.active, artifactSessionKey(requestID, modelCallID))
	observer.clearCalls++
}

func (observer *providerGatewayTestObserver) state(requestID string, modelCallID string) (bool, int) {
	observer.mu.Lock()
	defer observer.mu.Unlock()
	return observer.active[artifactSessionKey(requestID, modelCallID)], observer.clearCalls
}

func TestDefaultProviderGatewayReleasesArtifactSessionOnEveryExit(t *testing.T) {
	errProvider := errors.New("provider failed")
	errSink := errors.New("sink stopped")
	tests := []struct {
		name      string
		prepare   func() context.Context
		streamErr func(context.Context, func(modeladapter.ModelEvent) error) error
		sink      func(modeladapter.ModelEvent) error
		wantErr   error
	}{
		{
			name:    "success",
			prepare: context.Background,
			streamErr: func(context.Context, func(modeladapter.ModelEvent) error) error {
				return nil
			},
		},
		{
			name:    "provider error",
			prepare: context.Background,
			streamErr: func(context.Context, func(modeladapter.ModelEvent) error) error {
				return errProvider
			},
			wantErr: errProvider,
		},
		{
			name: "canceled context",
			prepare: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			streamErr: func(ctx context.Context, _ func(modeladapter.ModelEvent) error) error {
				return ctx.Err()
			},
			wantErr: context.Canceled,
		},
		{
			name:    "sink exits early",
			prepare: context.Background,
			streamErr: func(_ context.Context, sink func(modeladapter.ModelEvent) error) error {
				return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta})
			},
			sink: func(modeladapter.ModelEvent) error {
				return errSink
			},
			wantErr: errSink,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			observer := newProviderGatewayTestObserver()
			gateway := &DefaultProviderGateway{router: providerGatewayTestRouter{
				stream: func(ctx context.Context, req modeladapter.StreamRequest, sink func(modeladapter.ModelEvent) error) error {
					if _, err := req.Observer.RecordLLMRequest(req.RequestID, req.RunID, req.ModelCallID, map[string]any{"full_payload": "sensitive"}); err != nil {
						return err
					}
					return test.streamErr(ctx, sink)
				},
			}}
			sink := test.sink
			if sink == nil {
				sink = func(modeladapter.ModelEvent) error { return nil }
			}
			err := gateway.StartStream(test.prepare(), ProviderRequest{
				RequestID:   "request-1",
				RunID:       "run-1",
				ModelCallID: "model-call-1",
				Observer:    observer,
			}, sink)
			if test.wantErr == nil && err != nil {
				t.Fatalf("StartStream() error = %v", err)
			}
			if test.wantErr != nil && !errors.Is(err, test.wantErr) {
				t.Fatalf("StartStream() error = %v, want %v", err, test.wantErr)
			}
			active, clearCalls := observer.state("request-1", "model-call-1")
			if active {
				t.Fatal("artifact session remains active after StartStream returned")
			}
			if clearCalls != 1 {
				t.Fatalf("ClearActiveArtifacts() calls = %d, want 1", clearCalls)
			}
		})
	}
}
