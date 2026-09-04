// fallback_router_test.go 测试 FallbackAwareRouter 的核心语义：
// 单渠道降级路径、安全窗口内切换、typed safety 门禁、预算共享。
package modeladapter

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	legacyruntime "cursor/internal/runtime"
)

// ─── mock helpers ────────────────────────────────────────────────────────────

type stubPlanResolver struct {
	plan *legacyruntime.ChannelPlan
	err  error
}

func (s *stubPlanResolver) SelectChannelForModel(_ context.Context, _ string) (*legacyruntime.ResolvedChannel, error) {
	if s.plan != nil && len(s.plan.Channels) > 0 {
		ch := s.plan.Channels[0]
		return &ch, nil
	}
	return nil, legacyruntime.ErrChannelNotAvailable
}

func (s *stubPlanResolver) SelectChannelPlanForModel(_ context.Context, _ string) (*legacyruntime.ChannelPlan, error) {
	return s.plan, s.err
}

// controlledAdapter 返回可配置的错误序列，并追踪调用次数。
type controlledAdapter struct {
	errs   []error // errs[i] 返回第 i+1 次调用的结果
	calls  int
	emitAt int // 在第 emitAt 次调用时向 sink 发送一个 model event（0=不发送）
}

func (a *controlledAdapter) Stream(_ context.Context, req StreamRequest, sink func(ModelEvent) error) error {
	a.calls++
	if req.FallbackSafety != nil {
		req.FallbackSafety.markHTTPAttempt()
	}
	if req.FallbackBudget != nil {
		_ = req.FallbackBudget.TryConsumeAttempt()
	}
	idx := a.calls - 1
	if a.emitAt > 0 && a.calls == a.emitAt {
		_ = sink(ModelEvent{Kind: ModelEventKindTextDelta, Text: "x"})
	}
	if idx < len(a.errs) {
		return a.errs[idx]
	}
	return nil
}

func newFallbackRouterForTest(adapter ModelAdapter, plan *legacyruntime.ChannelPlan) *FallbackAwareRouter {
	inner := &Router{
		openai:    adapter,
		anthropic: adapter,
		resolver: staticChannelResolver{channel: func() *legacyruntime.ResolvedChannel {
			if plan != nil && len(plan.Channels) > 0 {
				ch := plan.Channels[0]
				return &ch
			}
			return &legacyruntime.ResolvedChannel{}
		}()},
	}
	resolver := &stubPlanResolver{plan: plan}
	return NewFallbackAwareRouter(inner, resolver)
}

func makeTestChannel(id, provider string) legacyruntime.ResolvedChannel {
	return legacyruntime.ResolvedChannel{
		ID:       id,
		Provider: provider,
		BaseURL:  "https://api.example.com/v1",
		APIKey:   "key-" + id,
		Model:    "model-x",
	}
}

// ─── tests ───────────────────────────────────────────────────────────────────

// TestFallbackDisabled_SingleChannelPath 验证 FallbackEnabled=false 时走单渠道路径。
func TestFallbackDisabled_SingleChannelPath(t *testing.T) {
	adapter := &controlledAdapter{errs: []error{nil}}
	plan := &legacyruntime.ChannelPlan{
		Channels:        []legacyruntime.ResolvedChannel{makeTestChannel("ch-a", "openai")},
		FallbackEnabled: false,
	}
	r := newFallbackRouterForTest(adapter, plan)
	err := r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected 1 call, got %d", adapter.calls)
	}
}

// TestFallbackEnabled_SuccessOnFirst 验证首渠道成功时不调用候选渠道。
func TestFallbackEnabled_SuccessOnFirst(t *testing.T) {
	adapter := &controlledAdapter{errs: []error{nil}}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	err := r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("expected nil, got %v", err)
	}
	if adapter.calls != 1 {
		t.Fatalf("expected 1 call (primary succeeded), got %d", adapter.calls)
	}
}

// TestFallbackEnabled_SwitchOnTransportError 验证 5xx 错误允许切换渠道。
func TestFallbackEnabled_SwitchOnTransportError(t *testing.T) {
	transportErr := &HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Attempt: 1}
	adapter := &controlledAdapter{errs: []error{transportErr, nil}}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	err := r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("expected nil after fallback to ch-b, got %v", err)
	}
	if adapter.calls != 2 {
		t.Fatalf("expected 2 calls (ch-a failed + ch-b succeeded), got %d", adapter.calls)
	}
}

// TestFallbackEnabled_NoSwitchAfterModelEvent 验证已发出 model event 后禁止切换渠道。
func TestFallbackEnabled_NoSwitchAfterModelEvent(t *testing.T) {
	transportErr := &HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Attempt: 1}
	// 第1次调用：先向 sink 发 event，再返回 transport 错误。
	adapter := &controlledAdapter{
		errs:   []error{transportErr, nil},
		emitAt: 1,
	}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	err := r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected error (switch blocked by model event), got nil")
	}
	if adapter.calls != 1 {
		t.Fatalf("expected only 1 call (no fallback after event), got %d", adapter.calls)
	}
}

// TestFallbackEnabled_NoSwitchOnContextCancel 验证上下文取消时不切换渠道。
func TestFallbackEnabled_NoSwitchOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 立即取消
	adapter := &controlledAdapter{errs: []error{context.Canceled, nil}}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	err := r.Stream(ctx, StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected context cancel error, got nil")
	}
}

// TestFallbackEnabled_NoSwitchOn4xx 验证 400/401/403/404 禁止 fallback。
func TestFallbackEnabled_NoSwitchOn4xx(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		code := code
		t.Run(http.StatusText(code), func(t *testing.T) {
			errHTTP := &HTTPStatusError{StatusCode: code, Attempt: 1}
			adapter := &controlledAdapter{errs: []error{errHTTP, nil}}
			plan := &legacyruntime.ChannelPlan{
				Channels: []legacyruntime.ResolvedChannel{
					makeTestChannel("ch-a", "openai"),
					makeTestChannel("ch-b", "openai"),
				},
				FallbackEnabled: true,
			}
			r := newFallbackRouterForTest(adapter, plan)
			err := r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
			if err == nil {
				t.Fatalf("code %d: expected error (4xx blocks fallback), got nil", code)
			}
			if adapter.calls != 1 {
				t.Fatalf("code %d: expected 1 call (no fallback), got %d", code, adapter.calls)
			}
		})
	}
}

// TestFallbackEnabled_CrossProviderRequestBodyOverrideBlocked 验证 RequestBodyOverride 阻断跨 provider 切换。
func TestFallbackEnabled_CrossProviderRequestBodyOverrideBlocked(t *testing.T) {
	transportErr := &HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Attempt: 1}
	adapter := &controlledAdapter{errs: []error{transportErr, nil}}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "anthropic"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	req := StreamRequest{
		ModelID:             "m1",
		RequestBodyOverride: map[string]any{"custom": "data"},
	}
	err := r.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	// ch-b 因 RequestBodyOverride 跨 provider 被跳过 → 所有渠道耗尽后返回错误
	if err == nil {
		t.Fatal("expected error (cross-provider with RequestBodyOverride blocked), got nil")
	}
	// ch-b 被跳过（incompatible），adapter 只调用了1次（ch-a）
	if adapter.calls != 1 {
		t.Fatalf("expected 1 call (ch-b skipped), got %d", adapter.calls)
	}
}

func TestFallbackEnabled_IncompatibleCandidatesCannotBypassPreviousAttempt(t *testing.T) {
	transportErr := &HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Attempt: 1}
	adapter := &controlledAdapter{errs: []error{transportErr, nil}}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "anthropic"),
			makeTestChannel("ch-c", "anthropic"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	err := r.Stream(context.Background(), StreamRequest{
		ModelID: "m1",
		Tools:   []json.RawMessage{json.RawMessage(`{"name":"tool"}`)},
	}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected the primary error after all cross-provider tool candidates were suppressed")
	}
	if adapter.calls != 1 {
		t.Fatalf("incompatible candidates bypassed the last attempted provider: calls=%d, want 1", adapter.calls)
	}
}

func TestFallbackEnabled_CandidateCapacityMustNotRegress(t *testing.T) {
	transportErr := &HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Attempt: 1}
	tests := []struct {
		name      string
		configure func(*legacyruntime.ResolvedChannel)
	}{
		{
			name: "context_window",
			configure: func(channel *legacyruntime.ResolvedChannel) {
				channel.ContextWindowTokens = 100_000
			},
		},
		{
			name: "max_output_tokens",
			configure: func(channel *legacyruntime.ResolvedChannel) {
				channel.MaxTokens = 1024
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			primary := makeTestChannel("ch-a", "openai")
			primary.ContextWindowTokens = 200_000
			primary.MaxTokens = 4096
			candidate := makeTestChannel("ch-b", "openai")
			candidate.ContextWindowTokens = 200_000
			candidate.MaxTokens = 4096
			test.configure(&candidate)
			adapter := &controlledAdapter{errs: []error{transportErr, nil}}
			r := newFallbackRouterForTest(adapter, &legacyruntime.ChannelPlan{
				Channels:        []legacyruntime.ResolvedChannel{primary, candidate},
				FallbackEnabled: true,
			})
			err := r.Stream(context.Background(), StreamRequest{ModelID: "m1", MaxTokens: 4096}, func(ModelEvent) error { return nil })
			if err == nil {
				t.Fatal("expected the lower-capacity candidate to be suppressed")
			}
			if adapter.calls != 1 {
				t.Fatalf("candidate capacity gate was bypassed: calls=%d", adapter.calls)
			}
		})
	}
}

// TestFallbackEnabled_SharedBudget 验证所有渠道共用总 attempt 预算，候选切换不重置。
func TestFallbackEnabled_SharedBudget(t *testing.T) {
	errs := make([]error, 20)
	for i := range errs {
		errs[i] = &HTTPStatusError{StatusCode: http.StatusTooManyRequests, Attempt: 1}
	}
	adapter := &controlledAdapter{errs: errs}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
			makeTestChannel("ch-c", "openai"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	err := r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected error (all channels exhausted), got nil")
	}
	if adapter.calls > fallbackChainTotalAttempts {
		t.Fatalf("shared budget exceeded: got %d calls, want <= %d", adapter.calls, fallbackChainTotalAttempts)
	}
}

// TestFallbackEnabled_AllChannelsFail 验证所有渠道失败后返回错误。
func TestFallbackEnabled_AllChannelsFail(t *testing.T) {
	transportErr := &HTTPStatusError{StatusCode: http.StatusBadGateway, Attempt: 1}
	adapter := &controlledAdapter{errs: []error{transportErr, transportErr}}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	err := r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected error when all channels fail")
	}
}

// TestIsFallbackEligibleError 验证 typed safety 分类器。
func TestIsFallbackEligibleError(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		eligible bool
	}{
		{"nil", nil, false},
		{"5xx_502", &HTTPStatusError{StatusCode: http.StatusBadGateway}, true},
		{"5xx_503", &HTTPStatusError{StatusCode: http.StatusServiceUnavailable}, true},
		{"5xx_504", &HTTPStatusError{StatusCode: http.StatusGatewayTimeout}, true},
		{"5xx_524", &HTTPStatusError{StatusCode: HTTPStatusCloudflareTimeout}, true},
		{"5xx_500", &HTTPStatusError{StatusCode: http.StatusInternalServerError}, true},
		{"5xx_529", &HTTPStatusError{StatusCode: 529}, false},
		{"429", &HTTPStatusError{StatusCode: http.StatusTooManyRequests}, true},
		{"400", &HTTPStatusError{StatusCode: http.StatusBadRequest}, false},
		{"401", &HTTPStatusError{StatusCode: http.StatusUnauthorized}, false},
		{"403", &HTTPStatusError{StatusCode: http.StatusForbidden}, false},
		{"404", &HTTPStatusError{StatusCode: http.StatusNotFound}, false},
		{"ctx_cancel", context.Canceled, false},
		{"ctx_deadline", context.DeadlineExceeded, false},
		{"transport", errors.New("connection refused"), true},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := isFallbackEligibleError(c.err)
			if got != c.eligible {
				t.Errorf("isFallbackEligibleError(%v) = %v, want %v", c.err, got, c.eligible)
			}
		})
	}
}

// TestCheckFallbackCompatibility 验证跨 provider 兼容性检查。
func TestCheckFallbackCompatibility(t *testing.T) {
	// 同 provider 始终兼容
	ok, reason := checkFallbackCompatibility(StreamRequest{}, "openai", "openai")
	if !ok || reason != "" {
		t.Errorf("same provider: got ok=%v reason=%q", ok, reason)
	}

	// 任意 provider 组合 + RequestBodyOverride → 不兼容
	for _, providers := range [][2]string{{"openai", "anthropic"}, {"openai", "openai"}} {
		ok, reason = checkFallbackCompatibility(
			StreamRequest{RequestBodyOverride: map[string]any{"x": 1}},
			providers[0], providers[1],
		)
		if ok {
			t.Errorf("expected incompatible with RequestBodyOverride for %s -> %s", providers[0], providers[1])
		}
		if reason != "request_body_override" {
			t.Errorf("reason = %q, want request_body_override", reason)
		}
	}

	// 跨 provider 无 override → 必须同 adapter type
	ok, reason = checkFallbackCompatibility(StreamRequest{}, "openai", "anthropic")
	if ok || reason != "adapter_type" {
		t.Errorf("cross-provider no override: got ok=%v reason=%q, want adapter_type", ok, reason)
	}
}

// TestIsFallbackEligibleError_RawBytesObservedBlocks 验证 RawBytesObserved=true 时即使是
// stream_decode 类型也禁止 fallback（需求1：有字节但零 model event 的非法/不完整 SSE）。
func TestIsFallbackEligibleError_RawBytesObservedBlocks(t *testing.T) {
	cases := []struct {
		name     string
		err      error
		eligible bool
	}{
		{
			"stream_decode_no_bytes",
			&StreamTruncatedError{Provider: "openai", RawBytesObserved: false},
			true, // 零字节时允许 fallback
		},
		{
			"stream_decode_with_bytes",
			&StreamTruncatedError{Provider: "openai", RawBytesObserved: true},
			false, // 有字节时必须阻断 fallback
		},
		{
			"request_build",
			&RequestBuildError{Err: errTest},
			false,
		},
		{
			"request_build_size_limit",
			&RequestBuildError{Actual: 10, Limit: 5},
			false,
		},
	}
	for _, c := range cases {
		c := c
		t.Run(c.name, func(t *testing.T) {
			got := isFallbackEligibleError(c.err)
			if got != c.eligible {
				t.Errorf("isFallbackEligibleError(%T) = %v, want %v", c.err, got, c.eligible)
			}
		})
	}
}

var errTest = errors.New("test error")

// TestFallbackEnabled_NoSwitchOnRawBytesObserved 验证 StreamTruncatedError{RawBytesObserved:true}
// 阻断 fallback 切换（安全门禁3：有字节但零 model event）。
func TestFallbackEnabled_NoSwitchOnRawBytesObserved(t *testing.T) {
	rawBytesErr := &StreamTruncatedError{Provider: "openai", RawBytesObserved: true}
	adapter := &controlledAdapter{errs: []error{rawBytesErr, nil}}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	err := r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected error (raw bytes blocks fallback), got nil")
	}
	// 有字节后不得切换渠道，adapter 只调用了1次
	if adapter.calls != 1 {
		t.Fatalf("expected 1 call (no fallback after raw bytes), got %d", adapter.calls)
	}
}

func TestFallbackEnabled_RealInvalidSSEBytesBlockCandidate(t *testing.T) {
	var primaryCalls atomic.Int32
	primary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		primaryCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {invalid-json}\n\n"))
	}))
	defer primary.Close()
	var candidateCalls atomic.Int32
	candidate := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		candidateCalls.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: [DONE]\n\n"))
	}))
	defer candidate.Close()

	plan := &legacyruntime.ChannelPlan{Channels: []legacyruntime.ResolvedChannel{
		{ID: "primary", Provider: "openai", BaseURL: primary.URL, APIKey: "test", Model: "m1", OpenAIEndpoint: "/v1/chat/completions"},
		{ID: "candidate", Provider: "openai", BaseURL: candidate.URL, APIKey: "test", Model: "m1", OpenAIEndpoint: "/v1/chat/completions"},
	}, FallbackEnabled: true}
	inner := NewRouter(&stubPlanResolver{plan: plan})
	r := NewFallbackAwareRouter(inner, &stubPlanResolver{plan: plan})
	err := r.Stream(context.Background(), StreamRequest{ModelID: "m1", Stream: true}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected invalid SSE error")
	}
	if primaryCalls.Load() != 1 {
		t.Fatalf("primary calls = %d, want 1", primaryCalls.Load())
	}
	if candidateCalls.Load() != 0 {
		t.Fatalf("candidate calls = %d, want 0 after raw bytes", candidateCalls.Load())
	}
	var safetyErr *FallbackSafetyError
	if !errors.As(err, &safetyErr) || !safetyErr.Safety.RawBytesObserved {
		t.Fatalf("error safety = %#v, want raw bytes observed", safetyErr)
	}
}

// TestFallbackEnabled_RequestBuildErrorBlocksFallback 验证 RequestBuildError 阻断跨渠道 fallback
// （需求2：request 构建/序列化错误 typed 标记，禁止切换渠道）。
func TestFallbackEnabled_RequestBuildErrorBlocksFallback(t *testing.T) {
	buildErr := &RequestBuildError{Err: errors.New("json marshal failed")}
	adapter := &controlledAdapter{errs: []error{buildErr, nil}}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	err := r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected error (request build error blocks fallback), got nil")
	}
	if adapter.calls != 1 {
		t.Fatalf("expected 1 call (no fallback on build error), got %d", adapter.calls)
	}
	if got := fallbackSuppressionReason(err); got != "request_build" {
		t.Fatalf("suppression = %q, want request_build", got)
	}
}

func TestAllocateFallbackChannelAttemptsCoverageFirst(t *testing.T) {
	cases := []struct {
		name       string
		remaining  int
		subsequent int
		want       int
	}{
		{name: "3ch_budget5_first", remaining: 5, subsequent: 2, want: 2},
		{name: "3ch_budget5_second", remaining: 2, subsequent: 1, want: 1},
		{name: "3ch_budget5_third", remaining: 1, subsequent: 0, want: 1},
		{name: "5ch_budget5_first", remaining: 5, subsequent: 4, want: 1},
		{name: "5ch_budget9_first", remaining: 9, subsequent: 4, want: 2},
		{name: "5ch_budget9_second", remaining: 6, subsequent: 3, want: 2},
		{name: "5ch_budget9_third", remaining: 3, subsequent: 2, want: 1},
		{name: "5ch_budget2_first", remaining: 2, subsequent: 4, want: 1},
		{name: "5ch_budget2_second", remaining: 1, subsequent: 3, want: 1},
		{name: "exhausted", remaining: 0, subsequent: 2, want: 0},
		{name: "single_channel", remaining: 5, subsequent: 0, want: 2},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			got := allocateFallbackChannelAttempts(test.remaining, test.subsequent, providerRequestMaxAttempts)
			if got != test.want {
				t.Fatalf("allocate(%d, %d) = %d, want %d", test.remaining, test.subsequent, got, test.want)
			}
			if got > test.remaining {
				t.Fatalf("allocation %d exceeds remaining %d", got, test.remaining)
			}
			if got > providerRequestMaxAttempts {
				t.Fatalf("allocation %d exceeds per-channel cap %d", got, providerRequestMaxAttempts)
			}
		})
	}
}

func TestCountSubsequentReservableChannelsSkipsIncompatible(t *testing.T) {
	channels := []legacyruntime.ResolvedChannel{
		makeTestChannel("ch-a", "openai"),
		makeTestChannel("ch-b", "anthropic"),
		makeTestChannel("ch-c", "openai"),
	}
	req := StreamRequest{
		Tools: []json.RawMessage{json.RawMessage(`{"type":"function"}`)},
	}
	if got := countSubsequentReservableChannels(req, channels, 0); got != 1 {
		t.Fatalf("reservable after primary = %d, want 1 (skip incompatible anthropic)", got)
	}
	compatible := []legacyruntime.ResolvedChannel{
		makeTestChannel("ch-a", "openai"),
		makeTestChannel("ch-b", "openai"),
		makeTestChannel("ch-c", "openai"),
	}
	if got := countSubsequentReservableChannels(StreamRequest{}, compatible, 0); got != 2 {
		t.Fatalf("reservable all-compatible = %d, want 2", got)
	}
}

// TestFallbackEnabled_SharedAttemptBudget_ReservesCoverage 验证 3 渠道共享默认预算时
// 总调用不突破上限，且分配器为后续兼容渠道保留覆盖机会。
func TestFallbackEnabled_SharedAttemptBudget_ReservesCoverage(t *testing.T) {
	// 每次调用都返回 429（可重试），但 attempt=1 确保 fallbackExtractAttemptsUsed 返回 allocated
	errs := make([]error, 20)
	for i := range errs {
		errs[i] = &HTTPStatusError{StatusCode: http.StatusTooManyRequests, Attempt: 1}
	}
	adapter := &controlledAdapter{errs: errs}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
			makeTestChannel("ch-c", "openai"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	err := r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected error (all channels exhausted)")
	}
	// 总调用次数不超过 fallbackChainTotalAttempts（5），且每渠道最多 providerRequestMaxAttempts（3）
	if adapter.calls > fallbackChainTotalAttempts {
		t.Fatalf("shared budget exceeded: got %d calls, want <= %d", adapter.calls, fallbackChainTotalAttempts)
	}
}

func TestFallbackRetryBudget_ExactAttemptAndWaitConsumption(t *testing.T) {
	budget := NewFallbackRetryBudget(5, 8*time.Second)
	for i := 0; i < 5; i++ {
		if !budget.TryConsumeAttempt() {
			t.Fatalf("attempt %d unexpectedly rejected", i+1)
		}
	}
	if budget.TryConsumeAttempt() {
		t.Fatal("sixth HTTP attempt must be rejected")
	}
	if !budget.TryReserveWait(5 * time.Second) {
		t.Fatal("first 5s retry wait unexpectedly rejected")
	}
	if budget.TryReserveWait(4 * time.Second) {
		t.Fatal("wait exceeding remaining 3s must be rejected")
	}
	if !budget.TryReserveWait(3 * time.Second) {
		t.Fatal("remaining 3s retry wait unexpectedly rejected")
	}
	attempts, wait := budget.Remaining()
	if attempts != 0 || wait != 0 {
		t.Fatalf("remaining budget = (%d, %v), want (0, 0)", attempts, wait)
	}
}

// TestFallbackEnabled_WaitBudgetPassedToChannels 验证每个渠道都收到非零的 FallbackRemainingWait
// （需求3：sleep 预算全链共享，不重置）。该测试通过注入 controlledWithWaitAdapter 校验字段。
func TestFallbackEnabled_WaitBudgetPassedToChannels(t *testing.T) {
	var receivedWaits []time.Duration
	adapter := &waitCapturingAdapter{
		errs: []error{
			&HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Attempt: 1},
			nil,
		},
		captured: &receivedWaits,
	}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
		},
		FallbackEnabled: true,
	}
	inner := &Router{
		openai:    adapter,
		anthropic: adapter,
		resolver:  staticChannelResolver{channel: &legacyruntime.ResolvedChannel{}},
	}
	r := NewFallbackAwareRouter(inner, &stubPlanResolver{plan: plan})
	_ = r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })

	// 两个渠道都应收到非零的 FallbackRemainingWait（由 streamWithFallback 注入）
	if len(receivedWaits) < 2 {
		t.Fatalf("expected >=2 wait captures, got %d", len(receivedWaits))
	}
	for i, w := range receivedWaits {
		if w <= 0 {
			t.Errorf("channel %d FallbackRemainingWait = %v, want > 0", i, w)
		}
		if w > fallbackChainMaxWait {
			t.Errorf("channel %d FallbackRemainingWait = %v exceeds chain max %v", i, w, fallbackChainMaxWait)
		}
	}
}

// waitCapturingAdapter 捕获每次 Stream 调用时 req.FallbackRemainingWait 的值。
type waitCapturingAdapter struct {
	errs     []error
	calls    int
	captured *[]time.Duration
}

func (a *waitCapturingAdapter) Stream(_ context.Context, req StreamRequest, _ func(ModelEvent) error) error {
	a.calls++
	if req.FallbackSafety != nil {
		req.FallbackSafety.markHTTPAttempt()
	}
	if req.FallbackBudget != nil {
		_ = req.FallbackBudget.TryConsumeAttempt()
	}
	*a.captured = append(*a.captured, req.FallbackRemainingWait)
	idx := a.calls - 1
	if idx < len(a.errs) {
		return a.errs[idx]
	}
	return nil
}

// TestCheckFallbackCompatibility_ReasoningSignatureBlocked 验证 ReasoningSignature 阻断跨 provider（需求4）。
func TestCheckFallbackCompatibility_ReasoningSignatureBlocked(t *testing.T) {
	req := StreamRequest{
		Messages: []Message{
			{Role: "assistant", ReasoningSignature: "sig-abc123"},
		},
	}
	ok, reason := checkFallbackCompatibility(req, "anthropic", "openai")
	if ok {
		t.Error("expected incompatible with ReasoningSignature")
	}
	if reason != "provider_reasoning_signature" {
		t.Errorf("reason = %q, want provider_reasoning_signature", reason)
	}
}

// TestCheckFallbackCompatibility_ImageContentPartBlocked 验证图片 ContentPart 阻断跨 provider（需求4）。
func TestCheckFallbackCompatibility_ImageContentPartBlocked(t *testing.T) {
	req := StreamRequest{
		Messages: []Message{
			{
				Role: "user",
				ContentParts: []ContentPart{
					{Type: contentPartTypeText, Text: "describe this"},
					{Type: contentPartTypeImage, Image: &ImageContent{MIMEType: "image/png"}},
				},
			},
		},
	}
	ok, reason := checkFallbackCompatibility(req, "openai", "anthropic")
	if ok {
		t.Error("expected incompatible with image ContentPart")
	}
	if reason != "image_content_part" {
		t.Errorf("reason = %q, want image_content_part", reason)
	}
}

// TestCheckFallbackCompatibility_ToolsBlockedCrossProvider 验证跨 provider 时 Tools 存在阻断 fallback（需求4）。
func TestCheckFallbackCompatibility_ToolsBlockedCrossProvider(t *testing.T) {
	req := StreamRequest{
		Tools: []json.RawMessage{
			json.RawMessage(`{"type":"function","function":{"name":"bash","parameters":{}}}`),
		},
	}
	// 跨 provider + tools → 不兼容
	ok, reason := checkFallbackCompatibility(req, "openai", "anthropic")
	if ok {
		t.Error("expected incompatible with Tools cross-provider")
	}
	if reason != "tools" {
		t.Errorf("reason = %q, want tools", reason)
	}

	// 同 provider + tools → 兼容（不涉及跨 provider 投影问题）
	ok, reason = checkFallbackCompatibility(req, "openai", "openai")
	if !ok || reason != "" {
		t.Errorf("same provider with tools: got ok=%v reason=%q, want ok=true", ok, reason)
	}

	// 跨 provider 无 tools → 仍因 adapter type 不兼容
	ok, reason = checkFallbackCompatibility(StreamRequest{}, "openai", "anthropic")
	if ok || reason != "adapter_type" {
		t.Errorf("cross-provider no tools: got ok=%v reason=%q, want adapter_type", ok, reason)
	}
}

// TestCheckFallbackCompatibility_OpenAIResponsesReasoningAnyDirection 验证
// OpenAI Responses reasoning state 在任意跨 provider 方向均被阻断（需求4）。
func TestCheckFallbackCompatibility_OpenAIResponsesReasoningAnyDirection(t *testing.T) {
	req := StreamRequest{
		Messages: []Message{
			{Role: "assistant", OpenAIResponsesReasoningID: "reasoning-123"},
		},
	}
	// openai → anthropic
	ok, reason := checkFallbackCompatibility(req, "openai", "anthropic")
	if ok {
		t.Error("openai→anthropic: expected incompatible with OpenAIResponsesReasoningID")
	}
	if reason != "openai_responses_reasoning_state" {
		t.Errorf("openai→anthropic reason = %q", reason)
	}
}

// TestFallbackEnabled_ArtifactSuffixInjectedForNonLastChannel 验证非最后渠道收到非空的
// FallbackArtifactSuffix，最后渠道收到空串（需求5：工件标识隔离）。
func TestFallbackEnabled_ArtifactSuffixInjectedForNonLastChannel(t *testing.T) {
	var suffixes []string
	adapter := &suffixCapturingAdapter{
		errs:     []error{&HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Attempt: 1}, nil},
		captured: &suffixes,
	}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
		},
		FallbackEnabled: true,
	}
	inner := &Router{
		openai:    adapter,
		anthropic: adapter,
		resolver:  staticChannelResolver{channel: &legacyruntime.ResolvedChannel{}},
	}
	r := NewFallbackAwareRouter(inner, &stubPlanResolver{plan: plan})
	err := r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(suffixes) != 2 {
		t.Fatalf("expected 2 suffix captures, got %d", len(suffixes))
	}
	// ch-a（非最后）应有非空后缀
	if suffixes[0] == "" {
		t.Error("ch-a (non-last): expected non-empty FallbackArtifactSuffix")
	}
	// ch-b（最后）应为空（使用原始 model_call_id）
	if suffixes[1] != "" {
		t.Errorf("ch-b (last): expected empty FallbackArtifactSuffix, got %q", suffixes[1])
	}
}

func TestFallbackEnabled_SkipsIncompatibleCandidateAndUsesNextCompatible(t *testing.T) {
	var suffixes []string
	adapter := &suffixCapturingAdapter{
		errs: []error{
			&HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Attempt: 1},
			nil,
		},
		captured: &suffixes,
	}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "anthropic"),
			makeTestChannel("ch-c", "openai"),
		},
		FallbackEnabled: true,
	}
	inner := &Router{
		openai:    adapter,
		anthropic: adapter,
		resolver:  staticChannelResolver{channel: &legacyruntime.ResolvedChannel{}},
	}
	r := NewFallbackAwareRouter(inner, &stubPlanResolver{plan: plan})
	err := r.Stream(context.Background(), StreamRequest{
		ModelID: "m1",
		Tools:   []json.RawMessage{json.RawMessage(`{"name":"tool"}`)},
	}, func(ModelEvent) error { return nil })
	if err != nil {
		t.Fatalf("unexpected error after compatible fallback: %v", err)
	}
	if len(suffixes) != 2 {
		t.Fatalf("expected primary and next compatible channel, got %d calls", len(suffixes))
	}
	if suffixes[0] == "" || suffixes[1] != "" {
		t.Fatalf("unexpected artifact suffixes: %#v", suffixes)
	}
}

func TestFallbackEnabled_ArtifactSuffixUsesLastCompatibleCandidate(t *testing.T) {
	var suffixes []string
	adapter := &suffixCapturingAdapter{
		errs:     []error{&HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Attempt: 1}},
		captured: &suffixes,
	}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "anthropic"),
			makeTestChannel("ch-c", "anthropic"),
		},
		FallbackEnabled: true,
	}
	inner := &Router{
		openai:    adapter,
		anthropic: adapter,
		resolver:  staticChannelResolver{channel: &legacyruntime.ResolvedChannel{}},
	}
	r := NewFallbackAwareRouter(inner, &stubPlanResolver{plan: plan})
	err := r.Stream(context.Background(), StreamRequest{
		ModelID: "m1",
		Tools:   []json.RawMessage{json.RawMessage(`{"name":"tool"}`)},
	}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected primary error after incompatible candidates were skipped")
	}
	if len(suffixes) != 1 {
		t.Fatalf("expected only the primary channel to run, got %d calls", len(suffixes))
	}
	if suffixes[0] != "" {
		t.Fatalf("actual final channel must retain the original model_call_id, got suffix %q", suffixes[0])
	}
}

type suffixCapturingAdapter struct {
	errs     []error
	calls    int
	captured *[]string
}

func (a *suffixCapturingAdapter) Stream(_ context.Context, req StreamRequest, _ func(ModelEvent) error) error {
	a.calls++
	if req.FallbackSafety != nil {
		req.FallbackSafety.markHTTPAttempt()
	}
	if req.FallbackBudget != nil {
		_ = req.FallbackBudget.TryConsumeAttempt()
	}
	*a.captured = append(*a.captured, req.FallbackArtifactSuffix)
	idx := a.calls - 1
	if idx < len(a.errs) {
		return a.errs[idx]
	}
	return nil
}

func TestFallbackEnabled_NoSwitchAfterSideEffect(t *testing.T) {
	transportErr := &HTTPStatusError{StatusCode: http.StatusServiceUnavailable, Attempt: 1}
	adapter := &sideEffectAdapter{err: transportErr}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	err := r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if err == nil {
		t.Fatal("expected error (side effect blocks fallback)")
	}
	if adapter.calls != 1 {
		t.Fatalf("expected 1 call, got %d", adapter.calls)
	}
}

type sideEffectAdapter struct {
	calls int
	err   error
}

func (a *sideEffectAdapter) Stream(_ context.Context, req StreamRequest, _ func(ModelEvent) error) error {
	a.calls++
	if req.FallbackSafety != nil {
		req.FallbackSafety.markHTTPAttempt()
		req.FallbackSafety.MarkSideEffectObserved()
	}
	if req.FallbackBudget != nil {
		_ = req.FallbackBudget.TryConsumeAttempt()
	}
	return a.err
}

func TestCheckFallbackChannelCompatibilityEndpointFamily(t *testing.T) {
	chat := makeTestChannel("ch-chat", "openai")
	chat.OpenAIEndpoint = "/v1/chat/completions"
	responses := makeTestChannel("ch-resp", "openai")
	responses.OpenAIEndpoint = "/v1/responses"
	ok, reason := checkFallbackChannelCompatibility(StreamRequest{}, chat, responses)
	if ok || reason != "endpoint_family" {
		t.Fatalf("chat vs responses: ok=%v reason=%q, want endpoint_family", ok, reason)
	}
	ok, reason = checkFallbackChannelCompatibility(StreamRequest{}, chat, chat)
	if !ok || reason != "" {
		t.Fatalf("same family: ok=%v reason=%q", ok, reason)
	}
}

func TestFallbackObservabilityIncludesPolicyBudgetAndSafety(t *testing.T) {
	controller, eventsPath := newObservabilityController(t)
	budget := NewFallbackRetryBudget(5, 8*time.Second)
	_ = budget.TryConsumeAttempt()
	_ = budget.TryConsumeAttempt()
	_ = budget.TryReserveWait(250 * time.Millisecond)
	recordFallbackAttempt(context.Background(), fallbackAttemptRecord{
		requestID:      "request-1",
		modelCallID:    "model-call-1",
		logicalModel:   "logical-model",
		channelAttempt: 1,
		channelID:      "channel-a",
		provider:       "openai",
		failure:        classifyHTTPStatusFailure(http.StatusInternalServerError),
		fallbackTo:     "channel-b",
		budget:         budget,
		allocation:     2,
		retryDelay:     250 * time.Millisecond,
		safety: FallbackSafetySnapshot{
			HTTPAttempts: 2,
			Waited:       250 * time.Millisecond,
		},
	})
	if err := controller.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	events := filterObservabilityEvents(readObservabilityEvents(t, eventsPath), "provider_fallback_attempt")
	if len(events) != 1 {
		t.Fatalf("fallback event count = %d, want 1", len(events))
	}
	event := events[0]
	if event.ErrorCategory != ProviderErrorServer5xx {
		t.Fatalf("error category = %q", event.ErrorCategory)
	}
	for field, want := range map[string]string{
		"logical_model":    "logical-model",
		"channel_id":       "channel-a",
		"provider":         "openai",
		"failure_cause":    FailureCauseHTTP500,
		"failure_phase":    LivenessPhaseHTTP,
		"recovery_action":  RecoveryActionSwitch,
		"fallback_to":      "channel-b",
	} {
		if got := observabilityFieldString(event, field); got != want {
			t.Fatalf("%s = %q, want %q", field, got, want)
		}
	}
	if observabilityFieldInt(event, "failure_http_status") != http.StatusInternalServerError ||
		observabilityFieldInt(event, "channel_http_attempts") != 2 ||
		observabilityFieldInt(event, "chain_attempts_used") != 2 {
		t.Fatalf("attempt/status fields = %#v", event.Fields)
	}
}

func TestFallbackSharedBudgetDoesNotInflatePastFive(t *testing.T) {
	errs := make([]error, 20)
	for i := range errs {
		errs[i] = &HTTPStatusError{StatusCode: http.StatusInternalServerError, Attempt: 1}
	}
	adapter := &controlledAdapter{errs: errs}
	plan := &legacyruntime.ChannelPlan{
		Channels: []legacyruntime.ResolvedChannel{
			makeTestChannel("ch-a", "openai"),
			makeTestChannel("ch-b", "openai"),
			makeTestChannel("ch-c", "openai"),
		},
		FallbackEnabled: true,
	}
	r := newFallbackRouterForTest(adapter, plan)
	_ = r.Stream(context.Background(), StreamRequest{ModelID: "m1"}, func(ModelEvent) error { return nil })
	if adapter.calls > fallbackChainTotalAttempts {
		t.Fatalf("shared budget inflated: got %d calls, want <= %d", adapter.calls, fallbackChainTotalAttempts)
	}
}
