package modeladapter

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"cursor/internal/observability"
	legacyruntime "cursor/internal/runtime"
)

type httpScriptStep struct {
	status     int
	retryAfter string
	body       string
}

func startOpenAIScript(t *testing.T, steps []httpScriptStep) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	if len(steps) == 0 {
		t.Fatal("scripted OpenAI server requires at least one step")
	}
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		n := int(hits.Add(1))
		step := steps[len(steps)-1]
		if n-1 < len(steps) {
			step = steps[n-1]
		}
		if step.retryAfter != "" {
			writer.Header().Set("Retry-After", step.retryAfter)
		}
		if step.status == 0 || (step.status >= 200 && step.status < 300) {
			writer.Header().Set("Content-Type", "text/event-stream")
			if step.status != 0 {
				writer.WriteHeader(step.status)
			}
			body := step.body
			if body == "" {
				body = "data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"
			}
			_, _ = writer.Write([]byte(body))
			return
		}
		writer.WriteHeader(step.status)
		if step.body != "" {
			_, _ = writer.Write([]byte(step.body))
		}
	}))
	t.Cleanup(server.Close)
	return server, &hits
}

func fallbackTestRetry() providerRetry {
	retry := defaultProviderRetry()
	retry.jitter = func(time.Duration) time.Duration { return 0 }
	retry.sleep = func(ctx context.Context, delay time.Duration) error {
		return ctx.Err()
	}
	retry.now = func() time.Time {
		return time.Date(2026, 8, 24, 0, 0, 0, 0, time.UTC)
	}
	return retry
}

func recordingFallbackRetry(delays *[]time.Duration) providerRetry {
	retry := fallbackTestRetry()
	retry.sleep = func(ctx context.Context, delay time.Duration) error {
		*delays = append(*delays, delay)
		return ctx.Err()
	}
	return retry
}

func newHTTPFallbackRouter(plan *legacyruntime.ChannelPlan, retry providerRetry) *FallbackAwareRouter {
	inner := NewRouter(&stubPlanResolver{plan: plan})
	inner.openai = &OpenAIAdapter{client: http.DefaultClient, retry: retry}
	inner.anthropic = &AnthropicAdapter{client: http.DefaultClient, retry: retry}
	return NewFallbackAwareRouter(inner, &stubPlanResolver{plan: plan})
}

func openaiHTTPChannel(id string, server *httptest.Server) legacyruntime.ResolvedChannel {
	return legacyruntime.ResolvedChannel{
		ID:             id,
		Provider:       "openai",
		BaseURL:        server.URL,
		APIKey:         "test-key",
		Model:          "m1",
		OpenAIEndpoint: "/v1/chat/completions",
	}
}

func anthropicHTTPChannel(id string, server *httptest.Server) legacyruntime.ResolvedChannel {
	return legacyruntime.ResolvedChannel{
		ID:       id,
		Provider: "anthropic",
		BaseURL:  server.URL,
		APIKey:   "test-key",
		Model:    "m1",
	}
}

func fallbackHTTPRequest() StreamRequest {
	return StreamRequest{
		RequestID:   "req-1",
		ModelCallID: "call-1",
		ModelID:     "m1",
		Messages:    []Message{{Role: "user", Content: "hi"}},
		MaxTokens:   16,
		Stream:      true,
	}
}

type fallbackEventSink struct {
	mu     sync.Mutex
	events []observability.Event
}

func (s *fallbackEventSink) Record(_ context.Context, capture observability.Capture) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, capture.Event)
	return true
}

func (s *fallbackEventSink) named(name string) []observability.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]observability.Event, 0)
	for _, event := range s.events {
		if event.Event == name {
			out = append(out, event)
		}
	}
	return out
}

func (s *fallbackEventSink) all() []observability.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]observability.Event, len(s.events))
	copy(out, s.events)
	return out
}

func installFallbackSink(t *testing.T) *fallbackEventSink {
	t.Helper()
	sink := &fallbackEventSink{}
	previous := observability.ProcessSink()
	t.Cleanup(func() { observability.SetProcessSink(previous) })
	observability.SetProcessSink(sink)
	return sink
}

func requireFallbackIntField(t *testing.T, event observability.Event, key string, want int) {
	t.Helper()
	if _, ok := event.Fields[key]; !ok {
		t.Fatalf("missing %s: %#v", key, event.Fields)
	}
	if got := observabilityFieldInt(event, key); got != want {
		t.Fatalf("%s = %d, want %d (%#v)", key, got, want, event.Fields)
	}
}

func fallbackAttemptByChannel(t *testing.T, events []observability.Event, channelID string) observability.Event {
	t.Helper()
	for _, event := range events {
		if observabilityFieldString(event, "channel_id") == channelID {
			return event
		}
	}
	t.Fatalf("no provider_fallback_attempt for channel %q", channelID)
	return observability.Event{}
}

func TestFallbackHTTPDefaultBudgetIsThreePlusTwo(t *testing.T) {
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusTooManyRequests}})
	second, secondHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusTooManyRequests}})
	third, thirdHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusTooManyRequests}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			openaiHTTPChannel("ch-b", second),
			openaiHTTPChannel("ch-c", third),
		},
		FallbackEnabled: true,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  8,
	}
	err := newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), fallbackHTTPRequest(), func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected chain to exhaust")
	}
	if primaryHits.Load() != 3 || secondHits.Load() != 2 || thirdHits.Load() != 0 {
		t.Fatalf("default 3+2 hits = %d/%d/%d, want 3/2/0", primaryHits.Load(), secondHits.Load(), thirdHits.Load())
	}
	if total := primaryHits.Load() + secondHits.Load() + thirdHits.Load(); total != 5 {
		t.Fatalf("total HTTP attempts = %d, want 5 (sixth must not be sent)", total)
	}
}

func TestFallbackHTTPConfiguredAttemptBudgets(t *testing.T) {
	cases := []struct {
		name     string
		attempts int
		want     []int32
	}{
		{name: "2", attempts: 2, want: []int32{2, 0, 0}},
		{name: "7", attempts: 7, want: []int32{3, 3, 1}},
		{name: "9", attempts: 9, want: []int32{3, 3, 3}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusServiceUnavailable}})
			second, secondHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusServiceUnavailable}})
			third, thirdHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusServiceUnavailable}})
			plan := &legacyruntime.ChannelPlan{
				Channels: []legacyruntime.ResolvedChannel{
					openaiHTTPChannel("ch-a", primary),
					openaiHTTPChannel("ch-b", second),
					openaiHTTPChannel("ch-c", third),
				},
				FallbackEnabled: true,
				MaxHttpAttempts: test.attempts,
				MaxWaitSeconds:  8,
			}
			_ = newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), fallbackHTTPRequest(), func(ModelEvent) error { return nil })
			got := []int32{primaryHits.Load(), secondHits.Load(), thirdHits.Load()}
			if got[0] != test.want[0] || got[1] != test.want[1] || got[2] != test.want[2] {
				t.Fatalf("hits = %v, want %v", got, test.want)
			}
			for _, hits := range got {
				if hits > int32(providerRequestMaxAttempts) {
					t.Fatalf("single channel exceeded 3 attempts: %v", got)
				}
			}
		})
	}
}

func TestFallbackHTTPOnePlusThreePlusOne(t *testing.T) {
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusServiceUnavailable, retryAfter: "999"}})
	second, secondHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusServiceUnavailable}})
	third, thirdHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusServiceUnavailable}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			openaiHTTPChannel("ch-b", second),
			openaiHTTPChannel("ch-c", third),
		},
		FallbackEnabled: true,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  8,
	}
	err := newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), fallbackHTTPRequest(), func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected chain to exhaust")
	}
	if primaryHits.Load() != 1 || secondHits.Load() != 3 || thirdHits.Load() != 1 {
		t.Fatalf("1+3+1 hits = %d/%d/%d", primaryHits.Load(), secondHits.Load(), thirdHits.Load())
	}
}

func TestFallbackHTTP500RetriesSameChannelButNeverSwitches(t *testing.T) {
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusInternalServerError}})
	candidate, candidateHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusOK}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			openaiHTTPChannel("ch-b", candidate),
		},
		FallbackEnabled: true,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  8,
	}
	err := newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), fallbackHTTPRequest(), func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected HTTP 500 to remain terminal for the chain")
	}
	if primaryHits.Load() != 3 {
		t.Fatalf("primary 500 retries = %d, want 3", primaryHits.Load())
	}
	if candidateHits.Load() != 0 {
		t.Fatalf("candidate hits = %d, want 0", candidateHits.Load())
	}
}

func TestFallbackHTTPDisabledKeepsSingleChannelP0(t *testing.T) {
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusTooManyRequests}})
	candidate, candidateHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusOK}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			openaiHTTPChannel("ch-b", candidate),
		},
		FallbackEnabled: false,
		MaxHttpAttempts: 9,
		MaxWaitSeconds:  30,
	}
	err := newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), fallbackHTTPRequest(), func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected disabled fallback to fail on the single channel")
	}
	if primaryHits.Load() != 3 {
		t.Fatalf("disabled path hits = %d, want P0 max 3", primaryHits.Load())
	}
	if candidateHits.Load() != 0 {
		t.Fatalf("disabled path must not use candidates: %d", candidateHits.Load())
	}
}

func TestFallbackHTTPWaitZeroDoesNotRetryOrSleepOnNextChannel(t *testing.T) {
	sink := installFallbackSink(t)
	var delays []time.Duration
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusTooManyRequests, retryAfter: "1"}})
	candidate, candidateHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusTooManyRequests, retryAfter: "1"}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			openaiHTTPChannel("ch-b", candidate),
		},
		FallbackEnabled: true,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  1,
	}
	_ = newHTTPFallbackRouter(plan, recordingFallbackRetry(&delays)).Stream(context.Background(), fallbackHTTPRequest(), func(ModelEvent) error { return nil })
	if primaryHits.Load() != 2 {
		t.Fatalf("primary hits = %d, want 2 (one wait then wait exhausted)", primaryHits.Load())
	}
	if candidateHits.Load() != 1 {
		t.Fatalf("candidate hits = %d, want 1 after remaining wait is 0", candidateHits.Load())
	}
	if len(delays) != 1 || delays[0] != time.Second {
		t.Fatalf("delays = %v, want [1s] from primary only", delays)
	}
	attempts := sink.named("provider_fallback_attempt")
	if len(attempts) == 0 {
		t.Fatal("expected provider_fallback_attempt events")
	}
	primaryEvent := fallbackAttemptByChannel(t, attempts, "ch-a")
	if observabilityFieldString(primaryEvent, "fallback_to") != "ch-b" {
		t.Fatalf("fallback_to = %q, want ch-b", observabilityFieldString(primaryEvent, "fallback_to"))
	}
	if observabilityFieldString(primaryEvent, "fallback_suppressed_reason") != "wait_budget_exhausted" {
		t.Fatalf("primary fallback_suppressed_reason = %q, want wait_budget_exhausted", observabilityFieldString(primaryEvent, "fallback_suppressed_reason"))
	}
	requireFallbackIntField(t, primaryEvent, "retry_delay_ms", 1000)
	requireFallbackIntField(t, primaryEvent, "chain_wait_used_ms", 1000)
	requireFallbackIntField(t, primaryEvent, "chain_wait_remaining_ms", 0)
	used := observabilityFieldInt(primaryEvent, "chain_attempts_used")
	remaining := observabilityFieldInt(primaryEvent, "chain_attempts_remaining")
	maxAttempts := observabilityFieldInt(primaryEvent, "chain_max_attempts")
	if used+remaining != maxAttempts {
		t.Fatalf("attempts used(%d)+remaining(%d) != max(%d)", used, remaining, maxAttempts)
	}
	candidateEvent := fallbackAttemptByChannel(t, attempts, "ch-b")
	requireFallbackIntField(t, candidateEvent, "retry_delay_ms", 0)
	requireFallbackIntField(t, candidateEvent, "chain_wait_used_ms", 1000)
	requireFallbackIntField(t, candidateEvent, "chain_wait_remaining_ms", 0)
}

func TestFallbackHTTPRetryAfterOverBudgetSwitchesWithoutSleep(t *testing.T) {
	sink := installFallbackSink(t)
	var delays []time.Duration
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusTooManyRequests, retryAfter: "30"}})
	candidate, candidateHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusOK}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			openaiHTTPChannel("ch-b", candidate),
		},
		FallbackEnabled: true,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  8,
	}
	err := newHTTPFallbackRouter(plan, recordingFallbackRetry(&delays)).Stream(context.Background(), fallbackHTTPRequest(), func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("candidate should succeed after over-budget Retry-After: %v", err)
	}
	if primaryHits.Load() != 1 {
		t.Fatalf("primary hits = %d, want 1", primaryHits.Load())
	}
	if candidateHits.Load() != 1 {
		t.Fatalf("candidate hits = %d, want 1", candidateHits.Load())
	}
	if len(delays) != 0 {
		t.Fatalf("slept %v, want none", delays)
	}
	primaryEvent := fallbackAttemptByChannel(t, sink.named("provider_fallback_attempt"), "ch-a")
	if observabilityFieldString(primaryEvent, "fallback_to") != "ch-b" {
		t.Fatalf("fallback_to = %q, want ch-b", observabilityFieldString(primaryEvent, "fallback_to"))
	}
	if observabilityFieldString(primaryEvent, "fallback_suppressed_reason") != "wait_budget_exhausted" {
		t.Fatalf("fallback_suppressed_reason = %q, want wait_budget_exhausted", observabilityFieldString(primaryEvent, "fallback_suppressed_reason"))
	}
	requireFallbackIntField(t, primaryEvent, "retry_delay_ms", 0)
}

func TestFallbackHTTPCancelDoesNotSwitch(t *testing.T) {
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusServiceUnavailable}})
	candidate, candidateHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusOK}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			openaiHTTPChannel("ch-b", candidate),
		},
		FallbackEnabled: true,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  8,
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(ctx, fallbackHTTPRequest(), func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected canceled error")
	}
	if primaryHits.Load() != 0 || candidateHits.Load() != 0 {
		t.Fatalf("canceled chain sent HTTP: primary=%d candidate=%d", primaryHits.Load(), candidateHits.Load())
	}
}

func TestFallbackHTTPRawBytesBlockCandidate(t *testing.T) {
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{
		status: http.StatusOK,
		body:   "data: {invalid-json}\n\n",
	}})
	candidate, candidateHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusOK}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			openaiHTTPChannel("ch-b", candidate),
		},
		FallbackEnabled: true,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  8,
	}
	err := newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), fallbackHTTPRequest(), func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected raw-byte stream to fail")
	}
	if primaryHits.Load() != 1 {
		t.Fatalf("primary hits = %d, want 1", primaryHits.Load())
	}
	if candidateHits.Load() != 0 {
		t.Fatalf("candidate hits = %d, want 0 after raw bytes", candidateHits.Load())
	}
}

func TestFallbackHTTPModelEventBlocksCandidate(t *testing.T) {
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{
		status: http.StatusOK,
		body:   "data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n",
	}})
	candidate, candidateHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusOK}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			openaiHTTPChannel("ch-b", candidate),
		},
		FallbackEnabled: true,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  8,
	}
	var events []ModelEvent
	err := newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), fallbackHTTPRequest(), func(event ModelEvent) error {
		events = append(events, event)
		return nil
	})
	if err == nil {
		t.Fatal("expected truncated stream after model event")
	}
	if len(events) == 0 {
		t.Fatal("expected at least one model event before the chain stopped")
	}
	if primaryHits.Load() != 1 {
		t.Fatalf("primary hits = %d, want 1", primaryHits.Load())
	}
	if candidateHits.Load() != 0 {
		t.Fatalf("candidate hits = %d, want 0 after model event", candidateHits.Load())
	}
}

func TestFallbackHTTPIncompatibleCandidateSkipped(t *testing.T) {
	sink := installFallbackSink(t)
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusServiceUnavailable, retryAfter: "999"}})
	incompatible, incompatibleHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusOK}})
	compatible, compatibleHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusOK}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			anthropicHTTPChannel("ch-b", incompatible),
			openaiHTTPChannel("ch-c", compatible),
		},
		FallbackEnabled: true,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  8,
	}
	req := fallbackHTTPRequest()
	req.Tools = []json.RawMessage{json.RawMessage(`{"type":"function","function":{"name":"lookup"}}`)}
	err := newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("expected compatible openai candidate to succeed: %v", err)
	}
	if primaryHits.Load() != 1 {
		t.Fatalf("primary hits = %d, want 1", primaryHits.Load())
	}
	if incompatibleHits.Load() != 0 {
		t.Fatalf("incompatible anthropic hits = %d, want 0", incompatibleHits.Load())
	}
	if compatibleHits.Load() != 1 {
		t.Fatalf("compatible candidate hits = %d, want 1", compatibleHits.Load())
	}
	skipped := sink.named("provider_fallback_incompatible")
	if len(skipped) == 0 {
		t.Fatal("expected provider_fallback_incompatible for the anthropic candidate")
	}
	if skipped[0].ModelCallID != req.ModelCallID {
		t.Fatalf("incompatible model_call_id = %q, want %q", skipped[0].ModelCallID, req.ModelCallID)
	}
}

func TestFallbackHTTPRouterClampsOversizedWait(t *testing.T) {
	var delays []time.Duration
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusTooManyRequests, retryAfter: "31"}})
	candidate, candidateHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusOK}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			openaiHTTPChannel("ch-b", candidate),
		},
		FallbackEnabled: true,
		MaxHttpAttempts: 100,
		MaxWaitSeconds:  99,
	}
	err := newHTTPFallbackRouter(plan, recordingFallbackRetry(&delays)).Stream(context.Background(), fallbackHTTPRequest(), func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("candidate should succeed after clamped wait rejects Retry-After: %v", err)
	}
	if primaryHits.Load() != 1 {
		t.Fatalf("primary hits = %d, want 1", primaryHits.Load())
	}
	if candidateHits.Load() != 1 {
		t.Fatalf("candidate hits = %d, want 1", candidateHits.Load())
	}
	if len(delays) != 0 {
		t.Fatalf("slept %v after oversized wait was clamped; Retry-After 31s must exceed clamped 30s", delays)
	}
}

func TestFallbackHTTPRouterClampsLowAttemptBudget(t *testing.T) {
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusServiceUnavailable}})
	second, secondHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusServiceUnavailable}})
	third, thirdHits := startOpenAIScript(t, []httpScriptStep{{status: http.StatusServiceUnavailable}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			openaiHTTPChannel("ch-b", second),
			openaiHTTPChannel("ch-c", third),
		},
		FallbackEnabled: true,
		MaxHttpAttempts: 1,
		MaxWaitSeconds:  8,
	}
	_ = newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), fallbackHTTPRequest(), func(ModelEvent) error { return nil })
	if primaryHits.Load() != 2 || secondHits.Load() != 0 || thirdHits.Load() != 0 {
		t.Fatalf("clamped min attempts hits = %d/%d/%d, want 2/0/0", primaryHits.Load(), secondHits.Load(), thirdHits.Load())
	}
}

func TestFallbackHTTPMetadataBudgetFieldsAndSameModelCallID(t *testing.T) {
	sink := installFallbackSink(t)
	primary, _ := startOpenAIScript(t, []httpScriptStep{{status: http.StatusTooManyRequests}})
	candidate, _ := startOpenAIScript(t, []httpScriptStep{{status: http.StatusOK}})
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			openaiHTTPChannel("ch-a", primary),
			openaiHTTPChannel("ch-b", candidate),
		},
		FallbackEnabled: true,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  8,
	}
	req := fallbackHTTPRequest()
	err := newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), req, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("expected success after fallback: %v", err)
	}
	attempts := sink.named("provider_fallback_attempt")
	if len(attempts) < 2 {
		t.Fatalf("fallback attempts = %d, want >= 2", len(attempts))
	}
	finals := sink.named("model_call_final")
	if len(finals) != 0 {
		t.Fatalf("model package must not emit model_call_final, got %d", len(finals))
	}
	for _, event := range sink.all() {
		if event.ModelCallID != req.ModelCallID {
			t.Fatalf("event %q model_call_id = %q, want %q", event.Event, event.ModelCallID, req.ModelCallID)
		}
	}
	seenAllocation := false
	for _, event := range attempts {
		if event.ModelCallID != req.ModelCallID {
			t.Fatalf("model_call_id = %q, want %q", event.ModelCallID, req.ModelCallID)
		}
		if observabilityFieldInt(event, "chain_max_attempts") != 5 {
			t.Fatalf("chain_max_attempts = %d", observabilityFieldInt(event, "chain_max_attempts"))
		}
		if observabilityFieldInt(event, "chain_max_wait_ms") != 8000 {
			t.Fatalf("chain_max_wait_ms = %d", observabilityFieldInt(event, "chain_max_wait_ms"))
		}
		used := observabilityFieldInt(event, "chain_attempts_used")
		remaining := observabilityFieldInt(event, "chain_attempts_remaining")
		maxAttempts := observabilityFieldInt(event, "chain_max_attempts")
		if _, ok := event.Fields["chain_attempts_used"]; !ok {
			t.Fatalf("missing chain_attempts_used: %#v", event.Fields)
		}
		if _, ok := event.Fields["chain_attempts_remaining"]; !ok {
			t.Fatalf("missing chain_attempts_remaining: %#v", event.Fields)
		}
		if used+remaining != maxAttempts {
			t.Fatalf("attempts used(%d)+remaining(%d) != max(%d)", used, remaining, maxAttempts)
		}
		if _, ok := event.Fields["chain_wait_used_ms"]; !ok {
			t.Fatalf("missing chain_wait_used_ms: %#v", event.Fields)
		}
		if _, ok := event.Fields["chain_wait_remaining_ms"]; !ok {
			t.Fatalf("missing chain_wait_remaining_ms: %#v", event.Fields)
		}
		waitUsed := observabilityFieldInt(event, "chain_wait_used_ms")
		waitRemaining := observabilityFieldInt(event, "chain_wait_remaining_ms")
		if waitUsed+waitRemaining != observabilityFieldInt(event, "chain_max_wait_ms") {
			t.Fatalf("wait used(%d)+remaining(%d) != max(%d)", waitUsed, waitRemaining, observabilityFieldInt(event, "chain_max_wait_ms"))
		}
		if _, ok := event.Fields["retry_delay_ms"]; !ok {
			t.Fatalf("missing retry_delay_ms: %#v", event.Fields)
		}
		if observabilityFieldInt(event, "channel_allocation_max_attempts") > 0 {
			seenAllocation = true
		}
		for _, forbidden := range []string{"authorization", "body", "headers", "query", "api_key", "test-key"} {
			if _, ok := event.Fields[forbidden]; ok {
				t.Fatalf("forbidden field %q present: %#v", forbidden, event.Fields)
			}
		}
	}
	if !seenAllocation {
		t.Fatal("expected channel_allocation_max_attempts on fallback attempts")
	}
}
