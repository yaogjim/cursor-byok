package modeladapter

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	legacyruntime "cursor/internal/runtime"
)

func capacityTestRequest(t *testing.T, limit int) StreamRequest {
	t.Helper()
	host := strings.ToLower(strings.NewReplacer("/", "-", "_", "-", " ", "-").Replace(t.Name()))
	return StreamRequest{
		Provider:              "openai",
		BaseURL:               "https://" + host + ".capacity.test/v1",
		APIKey:                "key-" + t.Name(),
		MaxConcurrentRequests: limit,
	}
}

func startOpenAIInflightServer(t *testing.T, hold <-chan struct{}, inflight *atomic.Int32, peak *atomic.Int32, entered chan struct{}) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		n := inflight.Add(1)
		for {
			old := peak.Load()
			if n <= old || peak.CompareAndSwap(old, n) {
				break
			}
		}
		if entered != nil {
			select {
			case entered <- struct{}{}:
			default:
			}
		}
		if hold != nil {
			<-hold
		}
		inflight.Add(-1)
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(server.Close)
	return server
}

func newOpenAICapacityRouter(channel *legacyruntime.ResolvedChannel, retry providerRetry) *Router {
	inner := NewRouter(&staticChannelResolver{channel: channel})
	inner.openai = &OpenAIAdapter{client: http.DefaultClient, retry: retry}
	return inner
}

func TestCapacityUnavailableErrorTypedAndRedacted(t *testing.T) {
	err := &CapacityUnavailableError{}
	if ClassifyProviderError(err) != ProviderErrorCapacityUnavailable {
		t.Fatalf("category = %q, want %q", ClassifyProviderError(err), ProviderErrorCapacityUnavailable)
	}
	if ProviderErrorCapacityUnavailable != "capacity_unavailable" {
		t.Fatalf("reserved capacity_unavailable category changed: %q", ProviderErrorCapacityUnavailable)
	}
	text := err.Error()
	if strings.TrimSpace(text) == "" {
		t.Fatal("Error() must be non-empty")
	}
	if strings.Contains(strings.ToLower(text), "key-") || strings.Contains(text, "sk-") {
		t.Fatalf("error text leaked credential: %q", text)
	}
	for _, banned := range []string{"sha256", "sha-256", "UpstreamCapacityGroupKey"} {
		if strings.Contains(text, banned) {
			t.Fatalf("error text leaked group identity %q: %q", banned, text)
		}
	}
}

func TestCapacityUnavailableFallbackEligibleWithZeroHTTP(t *testing.T) {
	wrapped := WrapFallbackSafetyError(&CapacityUnavailableError{}, &FallbackSafetyInfo{})
	if !isFallbackEligibleError(wrapped) {
		t.Fatal("capacity timeout must be fallback-eligible with zero HTTP attempts")
	}
	buildErr := WrapFallbackSafetyError(&RequestBuildError{Err: errors.New("marshal")}, &FallbackSafetyInfo{})
	if isFallbackEligibleError(buildErr) {
		t.Fatal("request-build zero-HTTP errors must remain fail-closed")
	}
	unknown := WrapFallbackSafetyError(errors.New("local failure"), &FallbackSafetyInfo{})
	if isFallbackEligibleError(unknown) {
		t.Fatal("unknown zero-HTTP errors must remain fail-closed")
	}
}

func TestUpstreamCapacityGroupKeyNormalized(t *testing.T) {
	left := upstreamCapacityGroupKey("OpenAI", "https://API.Example.com/v1/", "secret-key")
	right := upstreamCapacityGroupKey("openai", "https://api.example.com/v1", "secret-key")
	if left == "" || left != right {
		t.Fatal("provider type and normalized baseURL must share the same in-memory group")
	}
	otherKey := upstreamCapacityGroupKey("openai", "https://api.example.com/v1", "other-key")
	if otherKey == left {
		t.Fatal("different API keys must not share a group")
	}
	otherHost := upstreamCapacityGroupKey("openai", "https://other.example.com/v1", "secret-key")
	if otherHost == left {
		t.Fatal("different baseURL must not share a group")
	}
}

func TestApplyChannelToRequestProjectsCapacity(t *testing.T) {
	channel := legacyruntime.ResolvedChannel{
		Provider:                 "openai",
		BaseURL:                  "https://api.example.com/v1",
		APIKey:                   "channel-key",
		Model:                    "provider-model",
		MaxConcurrentRequests:    2,
		UpstreamCapacityGroupKey: "resolved-group-key",
	}
	req := capacityTestRequest(t, 9)
	req.UpstreamCapacityGroupKey = "stale-request-key"

	got := applyChannelToRequest(req, &channel)
	if got.MaxConcurrentRequests != 2 {
		t.Fatalf("projected capacity = %d, want 2", got.MaxConcurrentRequests)
	}
	if got.UpstreamCapacityGroupKey != "resolved-group-key" {
		t.Fatalf("projected group key = %q, want resolved-group-key", got.UpstreamCapacityGroupKey)
	}
}

func TestUpstreamCapacityZeroIsNoop(t *testing.T) {
	req := capacityTestRequest(t, 0)
	key := upstreamCapacityGroupKey(req.Provider, req.BaseURL, req.APIKey)
	var ready sync.WaitGroup
	var hold sync.WaitGroup
	var inflight atomic.Int32
	var peak atomic.Int32
	var wg sync.WaitGroup
	const n = 8
	ready.Add(n)
	hold.Add(1)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			release, err := acquireUpstreamCapacity(context.Background(), req)
			if err != nil {
				t.Errorf("limit=0 acquire: %v", err)
				ready.Done()
				return
			}
			defer release()
			cur := inflight.Add(1)
			for {
				old := peak.Load()
				if cur <= old || peak.CompareAndSwap(old, cur) {
					break
				}
			}
			ready.Done()
			hold.Wait()
			inflight.Add(-1)
		}()
	}
	ready.Wait()
	hold.Done()
	wg.Wait()
	if peak.Load() != n {
		t.Fatalf("limit=0 peak = %d, want %d", peak.Load(), n)
	}
	if got := upstreamCapacityActive(key); got != 0 {
		t.Fatalf("limit=0 active slots = %d, want 0", got)
	}
}

func TestUpstreamCapacitySameGroupPeak(t *testing.T) {
	req := capacityTestRequest(t, 2)
	key := upstreamCapacityGroupKey(req.Provider, req.BaseURL, req.APIKey)
	hold := make(chan struct{})
	entered := make(chan struct{}, 8)
	var wg sync.WaitGroup
	const n = 6
	const limit = 2
	errCh := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			release, err := acquireUpstreamCapacity(context.Background(), req)
			if err != nil {
				errCh <- err
				return
			}
			defer release()
			entered <- struct{}{}
			<-hold
		}()
	}
	for i := 0; i < limit; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for in-limit acquires")
		}
	}
	select {
	case <-entered:
		t.Fatal("same-group acquire exceeded limit before release")
	case <-time.After(40 * time.Millisecond):
	}
	if got := upstreamCapacityActive(key); got != limit {
		t.Fatalf("active = %d, want %d", got, limit)
	}
	close(hold)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("acquire: %v", err)
		}
	}
	if got := upstreamCapacityActive(key); got != 0 {
		t.Fatalf("active after release = %d, want 0", got)
	}
}

func TestUpstreamCapacitySameGroupPeakHTTP(t *testing.T) {
	const n = 6
	const limit = 2
	var inflight atomic.Int32
	var peak atomic.Int32
	entered := make(chan struct{}, 8)
	hold := make(chan struct{})
	server := startOpenAIInflightServer(t, hold, &inflight, &peak, entered)
	channel := openaiHTTPChannel("ch-a", server)
	channel.MaxConcurrentRequests = limit
	router := newOpenAICapacityRouter(&channel, fallbackTestRetry())
	var wg sync.WaitGroup
	errCh := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			req := fallbackHTTPRequest()
			req.MaxConcurrentRequests = limit
			errCh <- router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
		}()
	}
	for i := 0; i < limit; i++ {
		select {
		case <-entered:
		case <-time.After(2 * time.Second):
			t.Fatal("timed out waiting for HTTP holders")
		}
	}
	time.Sleep(30 * time.Millisecond)
	if peak.Load() != int32(limit) {
		t.Fatalf("HTTP peak = %d, want %d", peak.Load(), limit)
	}
	close(hold)
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
	}
}

func TestUpstreamCapacityDifferentGroupsIsolated(t *testing.T) {
	reqA := capacityTestRequest(t, 1)
	reqB := reqA
	reqB.APIKey = reqA.APIKey + "-b"
	hold := make(chan struct{})
	entered := make(chan struct{}, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for _, req := range []StreamRequest{reqA, reqB} {
		req := req
		go func() {
			defer wg.Done()
			release, err := acquireUpstreamCapacity(context.Background(), req)
			if err != nil {
				t.Errorf("acquire: %v", err)
				return
			}
			defer release()
			entered <- struct{}{}
			<-hold
		}()
	}
	for i := 0; i < 2; i++ {
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("different groups should acquire concurrently")
		}
	}
	close(hold)
	wg.Wait()
}

func TestUpstreamCapacityRetryHoldsSlot(t *testing.T) {
	var hits atomic.Int32
	first429 := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		n := int(hits.Add(1))
		if n == 1 {
			close(first429)
			writer.WriteHeader(http.StatusTooManyRequests)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"ok\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	t.Cleanup(server.Close)

	retry := fallbackTestRetry()
	retry.sleep = func(ctx context.Context, _ time.Duration) error {
		timer := time.NewTimer(200 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	channel := openaiHTTPChannel("ch-a", server)
	channel.MaxConcurrentRequests = 1
	router := newOpenAICapacityRouter(&channel, retry)

	errA := make(chan error, 1)
	go func() {
		req := fallbackHTTPRequest()
		req.MaxConcurrentRequests = 1
		errA <- router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	}()
	select {
	case <-first429:
	case <-time.After(2 * time.Second):
		t.Fatal("first 429 did not arrive")
	}

	errB := make(chan error, 1)
	go func() {
		req := fallbackHTTPRequest()
		req.ModelCallID = "call-2"
		req.MaxConcurrentRequests = 1
		errB <- router.Stream(context.Background(), req, func(ModelEvent) error { return nil })
	}()
	time.Sleep(80 * time.Millisecond)
	if got := hits.Load(); got != 1 {
		t.Fatalf("HTTP hits during retry sleep = %d, want 1 (slot must cover retry)", got)
	}
	if err := <-errA; err != nil {
		t.Fatalf("stream A: %v", err)
	}
	if err := <-errB; err != nil {
		t.Fatalf("stream B: %v", err)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("HTTP hits = %d, want 3 (429 + retry success + waiting stream)", got)
	}
}

func TestUpstreamCapacityTimeoutZeroHTTPFallback(t *testing.T) {
	sink := installFallbackSink(t)
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: 0}})
	second, secondHits := startOpenAIScript(t, []httpScriptStep{{status: 0}})
	chA := openaiHTTPChannel("ch-a", primary)
	chB := openaiHTTPChannel("ch-b", second)
	chA.MaxConcurrentRequests = 1
	chB.MaxConcurrentRequests = 1

	release, err := acquireUpstreamCapacity(context.Background(), StreamRequest{
		Provider:              chA.Provider,
		BaseURL:               chA.BaseURL,
		APIKey:                chA.APIKey,
		MaxConcurrentRequests: 1,
	})
	if err != nil {
		t.Fatalf("hold slot: %v", err)
	}
	defer release()

	plan := &legacyruntime.ChannelPlan{
		Channels:        []legacyruntime.ResolvedChannel{chA, chB},
		FallbackEnabled: true,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  8,
	}
	req := fallbackHTTPRequest()
	req.MaxConcurrentRequests = 1
	start := time.Now()
	err = newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), req, func(ModelEvent) error { return nil })
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("expected fallback success, got %v", err)
	}
	if primaryHits.Load() != 0 {
		t.Fatalf("primary HTTP hits = %d, want 0", primaryHits.Load())
	}
	if secondHits.Load() != 1 {
		t.Fatalf("second HTTP hits = %d, want 1", secondHits.Load())
	}
	if elapsed < 1800*time.Millisecond {
		t.Fatalf("capacity wait too short: %s", elapsed)
	}
	attempts := sink.named("provider_fallback_attempt")
	first := fallbackAttemptByChannel(t, attempts, "ch-a")
	if observabilityFieldString(first, "fallback_reason") != ProviderErrorCapacityUnavailable {
		t.Fatalf("fallback_reason = %q, want %q (%#v)", observabilityFieldString(first, "fallback_reason"), ProviderErrorCapacityUnavailable, first.Fields)
	}
	if observabilityFieldString(first, "fallback_to") != "ch-b" {
		t.Fatalf("fallback_to = %q, want ch-b", observabilityFieldString(first, "fallback_to"))
	}
	requireFallbackIntField(t, first, "chain_attempts_used", 0)
	requireFallbackIntField(t, first, "chain_wait_used_ms", 0)
}

func TestUpstreamCapacitySkipsSameGroupCandidate(t *testing.T) {
	sink := installFallbackSink(t)
	shared, sharedHits := startOpenAIScript(t, []httpScriptStep{{status: 0}})
	other, otherHits := startOpenAIScript(t, []httpScriptStep{{status: 0}})
	chA := openaiHTTPChannel("ch-a", shared)
	chB := openaiHTTPChannel("ch-b", shared)
	chC := openaiHTTPChannel("ch-c", other)
	chA.MaxConcurrentRequests = 1
	chB.MaxConcurrentRequests = 1
	chC.MaxConcurrentRequests = 1

	release, err := acquireUpstreamCapacity(context.Background(), StreamRequest{
		Provider:              chA.Provider,
		BaseURL:               chA.BaseURL,
		APIKey:                chA.APIKey,
		MaxConcurrentRequests: 1,
	})
	if err != nil {
		t.Fatalf("hold slot: %v", err)
	}
	defer release()

	plan := &legacyruntime.ChannelPlan{
		Channels:        []legacyruntime.ResolvedChannel{chA, chB, chC},
		FallbackEnabled: true,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  8,
	}
	req := fallbackHTTPRequest()
	req.MaxConcurrentRequests = 1
	if err := newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), req, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("expected skip-to-other-group success, got %v", err)
	}
	if sharedHits.Load() != 0 {
		t.Fatalf("same-group HTTP hits = %d, want 0", sharedHits.Load())
	}
	if otherHits.Load() != 1 {
		t.Fatalf("other-group HTTP hits = %d, want 1", otherHits.Load())
	}
	foundSkip := false
	for _, event := range sink.named("provider_fallback_incompatible") {
		if observabilityFieldString(event, "channel_id") == "ch-b" && observabilityFieldString(event, "fallback_suppressed_reason") == "same_upstream_group" {
			foundSkip = true
			break
		}
	}
	if !foundSkip {
		t.Fatal("expected same_upstream_group skip for ch-b")
	}
}

func TestUpstreamCapacityCancelDoesNotLeakOrFallback(t *testing.T) {
	primary, primaryHits := startOpenAIScript(t, []httpScriptStep{{status: 0}})
	second, secondHits := startOpenAIScript(t, []httpScriptStep{{status: 0}})
	chA := openaiHTTPChannel("ch-a", primary)
	chB := openaiHTTPChannel("ch-b", second)
	chA.MaxConcurrentRequests = 1
	chB.MaxConcurrentRequests = 1
	holdReq := StreamRequest{
		Provider:              chA.Provider,
		BaseURL:               chA.BaseURL,
		APIKey:                chA.APIKey,
		MaxConcurrentRequests: 1,
	}
	release, err := acquireUpstreamCapacity(context.Background(), holdReq)
	if err != nil {
		t.Fatalf("hold slot: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	errCh := make(chan error, 1)
	go func() {
		req := fallbackHTTPRequest()
		req.MaxConcurrentRequests = 1
		plan := &legacyruntime.ChannelPlan{
			Channels:        []legacyruntime.ResolvedChannel{chA, chB},
			FallbackEnabled: true,
			MaxHttpAttempts: 5,
			MaxWaitSeconds:  8,
		}
		errCh <- newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(ctx, req, func(ModelEvent) error { return nil })
	}()
	time.Sleep(100 * time.Millisecond)
	cancel()
	got := <-errCh
	if !errors.Is(got, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", got)
	}
	var capErr *CapacityUnavailableError
	if errors.As(got, &capErr) {
		t.Fatal("parent cancel must not be wrapped as CapacityUnavailableError")
	}
	if primaryHits.Load() != 0 || secondHits.Load() != 0 {
		t.Fatalf("cancel must not HTTP or fallback, hits %d/%d", primaryHits.Load(), secondHits.Load())
	}
	key := upstreamCapacityGroupKey(chA.Provider, chA.BaseURL, chA.APIKey)
	if active := upstreamCapacityActive(key); active != 1 {
		t.Fatalf("active after cancel = %d, want 1 (holder only)", active)
	}
	release()

	start := time.Now()
	req := fallbackHTTPRequest()
	req.MaxConcurrentRequests = 1
	plan := &legacyruntime.ChannelPlan{
		Channels:        []legacyruntime.ResolvedChannel{chA},
		FallbackEnabled: false,
		MaxHttpAttempts: 5,
		MaxWaitSeconds:  8,
	}
	if err := newHTTPFallbackRouter(plan, fallbackTestRetry()).Stream(context.Background(), req, func(ModelEvent) error { return nil }); err != nil {
		t.Fatalf("post-cancel stream: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("acquire after cancel looks leaked, took %s", elapsed)
	}
	if primaryHits.Load() != 1 {
		t.Fatalf("post-cancel HTTP hits = %d, want 1", primaryHits.Load())
	}
	if got := upstreamCapacityActive(key); got != 0 {
		t.Fatalf("active after success = %d, want 0", got)
	}
}
