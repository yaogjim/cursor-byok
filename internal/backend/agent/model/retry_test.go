package modeladapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cursor/internal/audit"
)

func instantRetry() providerRetry {
	retry := defaultProviderRetry()
	retry.sleep = func(ctx context.Context, delay time.Duration) error {
		return ctx.Err()
	}
	retry.jitter = func(delay time.Duration) time.Duration { return delay }
	return retry
}

func recordingRetry(delays *[]time.Duration) providerRetry {
	retry := instantRetry()
	retry.sleep = func(ctx context.Context, delay time.Duration) error {
		*delays = append(*delays, delay)
		return ctx.Err()
	}
	return retry
}

func TestProviderRetryTransportFailsThenSucceeds(t *testing.T) {
	observer, auditPath := newRetryAuditObserver(t)
	var builds int
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return nil, errors.New("connection refused")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader("ok-body")),
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}

	response, err := doProviderRequest(
		context.Background(),
		client,
		"openai",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			builds++
			return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/v1/responses", bytes.NewReader([]byte(`{}`)))
		},
		observer,
		instantRetry(),
	)
	if err != nil {
		t.Fatalf("retry after transport error: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read success body: %v", err)
	}
	if string(body) != "ok-body" {
		t.Fatalf("2xx body = %q", body)
	}
	if builds != 2 || calls != 2 {
		t.Fatalf("builds=%d calls=%d, want 2/2", builds, calls)
	}

	events := mustFindAuditEvents(t, readClosedAudit(t, observer, auditPath), "provider_response")
	if len(events) != 2 {
		t.Fatalf("provider_response count = %d", len(events))
	}
	if events[0].Attempt != 1 || events[0].MaxAttempts != 3 || events[0].RetryDecision != retryDecisionRetry {
		t.Fatalf("first response = %+v", events[0])
	}
	if events[0].ErrorCategory != ProviderErrorTransport {
		t.Fatalf("first error_category = %q", events[0].ErrorCategory)
	}
	if events[1].Attempt != 2 || events[1].MaxAttempts != 3 || events[1].RetryDecision != retryDecisionSuccessAfterRetry {
		t.Fatalf("final response = %+v", events[1])
	}
}

func TestProviderRetryRespectsRetryAfterSeconds(t *testing.T) {
	hits := 0
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		if hits == 1 {
			writer.Header().Set("Retry-After", "2")
			writer.WriteHeader(http.StatusTooManyRequests)
			_, _ = writer.Write([]byte(`{"error":"slow down"}`))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	}))
	defer providerServer.Close()

	observer, auditPath := newRetryAuditObserver(t)
	var delays []time.Duration
	retry := recordingRetry(&delays)
	retry.maxDelay = 10 * time.Second
	retry.maxTotalWait = 10 * time.Second

	response, err := doProviderRequest(
		context.Background(),
		providerServer.Client(),
		"openai",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodPost, providerServer.URL+"/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
		},
		observer,
		retry,
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if hits != 2 {
		t.Fatalf("hits = %d", hits)
	}
	if len(delays) != 1 || delays[0] != 2*time.Second {
		t.Fatalf("slept %v, want [2s]", delays)
	}

	events := mustFindAuditEvents(t, readClosedAudit(t, observer, auditPath), "provider_response")
	if len(events) != 2 {
		t.Fatalf("provider_response count = %d", len(events))
	}
	if !events[0].RetryAfterPresent || events[0].RetryDecision != retryDecisionRetry || events[0].Status != http.StatusTooManyRequests {
		t.Fatalf("first 429 event = %+v", events[0])
	}
	if events[1].RetryDecision != retryDecisionSuccessAfterRetry {
		t.Fatalf("final decision = %q", events[1].RetryDecision)
	}
}

func TestParseRetryAfterSecondsAndHTTPDate(t *testing.T) {
	now := time.Date(2026, 8, 19, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name   string
		header string
		want   time.Duration
		ok     bool
	}{
		{name: "seconds", header: "3", want: 3 * time.Second, ok: true},
		{name: "zero", header: "0", want: 0, ok: true},
		{name: "negative", header: "-1", want: 0, ok: true},
		{name: "http-date future", header: now.Add(2 * time.Second).UTC().Format(http.TimeFormat), want: 2 * time.Second, ok: true},
		{name: "http-date past", header: now.Add(-time.Second).UTC().Format(http.TimeFormat), want: 0, ok: true},
		{name: "empty", header: "", want: 0, ok: false},
		{name: "invalid", header: "soon", want: 0, ok: false},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			resp := &http.Response{Header: make(http.Header)}
			if testCase.header != "" {
				resp.Header.Set("Retry-After", testCase.header)
			}
			got, ok := parseRetryAfter(resp, now)
			if ok != testCase.ok || got != testCase.want {
				t.Fatalf("parseRetryAfter(%q) = (%v, %v), want (%v, %v)", testCase.header, got, ok, testCase.want, testCase.ok)
			}
		})
	}
}

func TestProviderRetry503Exhausted(t *testing.T) {
	hits := 0
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(http.StatusServiceUnavailable)
		_, _ = writer.Write([]byte("unavailable"))
	}))
	defer providerServer.Close()

	observer, auditPath := newRetryAuditObserver(t)
	var builds int
	var closed int
	client := &http.Client{Transport: &closeTrackingTransport{
		base:   providerServer.Client().Transport,
		closed: &closed,
	}}

	response, err := doProviderRequest(
		context.Background(),
		client,
		"openai",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			builds++
			return http.NewRequestWithContext(ctx, http.MethodPost, providerServer.URL+"/v1/responses", bytes.NewReader([]byte(`{}`)))
		},
		observer,
		instantRetry(),
	)
	if err != nil {
		t.Fatalf("exhausted 503 should return response: %v", err)
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if hits != 3 || builds != 3 {
		t.Fatalf("hits=%d builds=%d, want 3/3", hits, builds)
	}
	if closed != 2 {
		t.Fatalf("retried bodies closed = %d, want 2", closed)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "unavailable" {
		t.Fatalf("final body = %q", body)
	}

	events := mustFindAuditEvents(t, readClosedAudit(t, observer, auditPath), "provider_response")
	if len(events) != 3 {
		t.Fatalf("provider_response count = %d", len(events))
	}
	if events[0].RetryDecision != retryDecisionRetry || events[1].RetryDecision != retryDecisionRetry {
		t.Fatalf("intermediate decisions = %q, %q", events[0].RetryDecision, events[1].RetryDecision)
	}
	if events[2].Attempt != 3 || events[2].MaxAttempts != 3 || events[2].RetryDecision != retryDecisionExhausted {
		t.Fatalf("final event = %+v", events[2])
	}
}

func TestProviderRetry401DoesNotRetry(t *testing.T) {
	hits := 0
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":"no"}`))
	}))
	defer providerServer.Close()

	observer, auditPath := newRetryAuditObserver(t)
	response, err := doProviderRequest(
		context.Background(),
		providerServer.Client(),
		"openai",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodPost, providerServer.URL+"/v1/responses", bytes.NewReader([]byte(`{}`)))
		},
		observer,
		instantRetry(),
	)
	if err != nil {
		t.Fatalf("401 should return response: %v", err)
	}
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", response.StatusCode)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if !strings.Contains(string(body), "no") {
		t.Fatalf("401 body = %q", body)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}

	events := mustFindAuditEvents(t, readClosedAudit(t, observer, auditPath), "provider_response")
	if len(events) != 1 {
		t.Fatalf("provider_response count = %d", len(events))
	}
	if events[0].Attempt != 1 || events[0].MaxAttempts != 3 || events[0].RetryDecision != retryDecisionNoRetryStatus {
		t.Fatalf("401 event = %+v", events[0])
	}
}

func TestProviderRetryRebuildsRequestEachAttempt(t *testing.T) {
	hits := 0
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(http.StatusBadGateway)
	}))
	defer providerServer.Close()

	var builds int
	response, err := doProviderRequest(
		context.Background(),
		providerServer.Client(),
		"openai",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			builds++
			return http.NewRequestWithContext(ctx, http.MethodPost, providerServer.URL+"/v1/responses", bytes.NewReader([]byte(`{"n":1}`)))
		},
		audit.Default(),
		instantRetry(),
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if builds != hits || builds != 3 {
		t.Fatalf("builds=%d hits=%d, want 3/3", builds, hits)
	}
}

func TestProviderRetryClosesBodyBeforeRetry(t *testing.T) {
	var bodies []*countedBody
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		status := http.StatusServiceUnavailable
		payload := "retry-me"
		if len(bodies) == 1 {
			status = http.StatusOK
			payload = "final-ok"
		}
		body := &countedBody{ReadCloser: io.NopCloser(strings.NewReader(payload))}
		bodies = append(bodies, body)
		return &http.Response{
			StatusCode: status,
			Body:       body,
			Header:     make(http.Header),
			Request:    request,
		}, nil
	})}

	response, err := doProviderRequest(
		context.Background(),
		client,
		"openai",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/v1/responses", bytes.NewReader([]byte(`{}`)))
		},
		audit.Default(),
		instantRetry(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(bodies) != 2 {
		t.Fatalf("attempts = %d, want 2", len(bodies))
	}
	if bodies[0].closes == 0 {
		t.Fatal("retried 503 body was not closed")
	}
	if bodies[1].closes != 0 {
		t.Fatal("2xx body was closed by retry")
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(body) != "final-ok" {
		t.Fatalf("final 2xx body = %q err=%v", body, err)
	}
	if bodies[1].closes == 0 {
		t.Fatal("caller close did not close 2xx body")
	}
}

func TestProviderRetryContextCancelDoesNotWait(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Retry-After", "30")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte("later"))
		cancel()
	}))
	defer providerServer.Close()

	retry := defaultProviderRetry()
	retry.jitter = func(delay time.Duration) time.Duration { return delay }
	retry.maxDelay = 30 * time.Second
	retry.maxTotalWait = 30 * time.Second
	retry.sleep = sleepContext

	started := time.Now()
	_, err := doProviderRequest(
		ctx,
		providerServer.Client(),
		"openai",
		"request-id",
		"model-call-id",
		func(requestContext context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(requestContext, http.MethodPost, providerServer.URL+"/v1/responses", bytes.NewReader([]byte(`{}`)))
		},
		audit.Default(),
		retry,
	)
	elapsed := time.Since(started)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if elapsed >= 500*time.Millisecond {
		t.Fatalf("canceled retry waited %s", elapsed)
	}
}

func TestProviderRetryDoesNotRetryClientErrorsOrBuildError(t *testing.T) {
	for _, status := range []int{http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusUnprocessableEntity} {
		hits := 0
		providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
			hits++
			writer.WriteHeader(status)
		}))
		response, err := doProviderRequest(
			context.Background(),
			providerServer.Client(),
			"openai",
			"request-id",
			"model-call-id",
			func(ctx context.Context) (*http.Request, error) {
				return http.NewRequestWithContext(ctx, http.MethodPost, providerServer.URL+"/v1/responses", bytes.NewReader([]byte(`{}`)))
			},
			audit.Default(),
			instantRetry(),
		)
		providerServer.Close()
		if err != nil {
			t.Fatalf("status %d err = %v", status, err)
		}
		_ = response.Body.Close()
		if hits != 1 {
			t.Fatalf("status %d hits = %d", status, hits)
		}
	}

	observer, auditPath := newRetryAuditObserver(t)
	var builds int
	_, err := doProviderRequest(
		context.Background(),
		http.DefaultClient,
		"openai",
		"request-id",
		"model-call-id",
		func(context.Context) (*http.Request, error) {
			builds++
			return nil, errors.New("build failed")
		},
		observer,
		instantRetry(),
	)
	if err == nil || err.Error() != "build failed" {
		t.Fatalf("build error = %v", err)
	}
	if builds != 1 {
		t.Fatalf("build calls = %d", builds)
	}
	data := readClosedAudit(t, observer, auditPath)
	requestEvent := mustFindAuditEvent(t, data, "provider_request")
	if requestEvent.RetryDecision != retryDecisionNoRetryBuild || requestEvent.Attempt != 1 {
		t.Fatalf("build event = %+v", requestEvent)
	}
	if strings.Count(string(data), `"kind":"provider_response"`) != 0 {
		t.Fatalf("build error recorded a provider_response: %s", data)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type countedBody struct {
	io.ReadCloser
	closes int
}

func (body *countedBody) Close() error {
	body.closes++
	if body.ReadCloser == nil {
		return nil
	}
	return body.ReadCloser.Close()
}

type closeTrackingTransport struct {
	base   http.RoundTripper
	closed *int
}

func (transport *closeTrackingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	resp, err := transport.base.RoundTrip(request)
	if resp != nil && resp.Body != nil {
		resp.Body = &countedCloseOnce{ReadCloser: resp.Body, closed: transport.closed}
	}
	return resp, err
}

type countedCloseOnce struct {
	io.ReadCloser
	closed *int
}

func (body *countedCloseOnce) Close() error {
	*body.closed++
	return body.ReadCloser.Close()
}

func newRetryAuditObserver(t *testing.T) (*audit.Observer, string) {
	t.Helper()
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	observer, err := audit.New(audit.Options{FilePath: auditPath})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = observer.Close() })
	return observer, auditPath
}

func readClosedAudit(t *testing.T, observer *audit.Observer, path string) []byte {
	t.Helper()
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func mustFindAuditEvents(t *testing.T, data []byte, kind string) []audit.Event {
	t.Helper()
	var events []audit.Event
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event audit.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode audit event: %v", err)
		}
		if event.Kind == kind {
			events = append(events, event)
		}
	}
	if len(events) == 0 {
		t.Fatalf("missing %s audit event in %s", kind, data)
	}
	return events
}
