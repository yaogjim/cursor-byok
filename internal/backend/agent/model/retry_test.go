package modeladapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"cursor/internal/audit"

	"golang.org/x/net/http2"
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
	if events[0].Attempt != 1 || events[0].MaxAttempts != providerRequestMaxAttempts || events[0].RetryDecision != retryDecisionRetry {
		t.Fatalf("first response = %+v", events[0])
	}
	if events[0].ErrorCategory != ProviderErrorTransport {
		t.Fatalf("first error_category = %q", events[0].ErrorCategory)
	}
	if events[1].Attempt != 2 || events[1].MaxAttempts != providerRequestMaxAttempts || events[1].RetryDecision != retryDecisionSuccessAfterRetry {
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
	if hits != providerRequestMaxAttempts || builds != providerRequestMaxAttempts {
		t.Fatalf("hits=%d builds=%d, want %d/%d", hits, builds, providerRequestMaxAttempts, providerRequestMaxAttempts)
	}
	if closed != providerRequestMaxAttempts-1 {
		t.Fatalf("retried bodies closed = %d, want %d", closed, providerRequestMaxAttempts-1)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if string(body) != "unavailable" {
		t.Fatalf("final body = %q", body)
	}

	events := mustFindAuditEvents(t, readClosedAudit(t, observer, auditPath), "provider_response")
	if len(events) != providerRequestMaxAttempts {
		t.Fatalf("provider_response count = %d", len(events))
	}
	if events[0].RetryDecision != retryDecisionRetry {
		t.Fatalf("intermediate decisions = %q", events[0].RetryDecision)
	}
	if events[len(events)-1].Attempt != providerRequestMaxAttempts || events[len(events)-1].MaxAttempts != providerRequestMaxAttempts || events[len(events)-1].RetryDecision != retryDecisionExhausted {
		t.Fatalf("final event = %+v", events[len(events)-1])
	}
}

func TestHTTPRetryObservationAllowlist(t *testing.T) {
	cases := []struct {
		status      int
		retryable   bool
		reason      string
		suppression string
	}{
		{http.StatusTooManyRequests, true, "http_429", "not_recorded"},
		{http.StatusInternalServerError, true, "http_5xx", "not_recorded"},
		{http.StatusBadGateway, true, "http_5xx", "not_recorded"},
		{http.StatusServiceUnavailable, true, "http_5xx", "not_recorded"},
		{http.StatusGatewayTimeout, true, "http_5xx", "not_recorded"},
		{HTTPStatusCloudflareTimeout, true, "http_524", "not_recorded"},
		{529, false, "not_recorded", "http_status"},
		{http.StatusBadRequest, false, "not_recorded", "http_status"},
		{http.StatusOK, false, "not_recorded", "http_status"},
	}
	for _, test := range cases {
		retryable, reason, suppression := HTTPRetryObservation(test.status)
		if retryable != test.retryable || reason != test.reason || suppression != test.suppression {
			t.Fatalf("status %d: retryable=%v reason=%q suppression=%q, want %v %q %q",
				test.status, retryable, reason, suppression, test.retryable, test.reason, test.suppression)
		}
		if isRetryableHTTPStatus(test.status) != test.retryable {
			t.Fatalf("isRetryableHTTPStatus(%d) = %v, want %v", test.status, !test.retryable, test.retryable)
		}
	}
}

func TestProviderRetry524ThenSucceeds(t *testing.T) {
	hits := 0
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		if hits == 1 {
			writer.WriteHeader(HTTPStatusCloudflareTimeout)
			_, _ = writer.Write([]byte("timeout"))
			return
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok-body"))
	}))
	defer providerServer.Close()

	observer, auditPath := newRetryAuditObserver(t)
	var builds int
	response, err := doProviderRequest(
		context.Background(),
		providerServer.Client(),
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
		t.Fatalf("retry after 524: %v", err)
	}
	body, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil {
		t.Fatalf("read success body: %v", err)
	}
	if string(body) != "ok-body" {
		t.Fatalf("2xx body = %q", body)
	}
	if hits != 2 || builds != 2 {
		t.Fatalf("hits=%d builds=%d, want 2/2", hits, builds)
	}
	events := mustFindAuditEvents(t, readClosedAudit(t, observer, auditPath), "provider_response")
	if len(events) != 2 {
		t.Fatalf("provider_response count = %d", len(events))
	}
	if events[0].Status != HTTPStatusCloudflareTimeout || events[0].RetryDecision != retryDecisionRetry || events[0].ErrorCategory != ProviderErrorServer5xx {
		t.Fatalf("first 524 response = %+v", events[0])
	}
	if events[1].RetryDecision != retryDecisionSuccessAfterRetry {
		t.Fatalf("final response = %+v", events[1])
	}
}

func TestProviderRetry524Exhausted(t *testing.T) {
	hits := 0
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(HTTPStatusCloudflareTimeout)
		_, _ = writer.Write([]byte("timeout"))
	}))
	defer providerServer.Close()

	observer, auditPath := newRetryAuditObserver(t)
	var builds int
	response, err := doProviderRequest(
		context.Background(),
		providerServer.Client(),
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
		t.Fatalf("exhausted 524 should return response: %v", err)
	}
	if response.StatusCode != HTTPStatusCloudflareTimeout {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if hits != providerRequestMaxAttempts || builds != providerRequestMaxAttempts {
		t.Fatalf("hits=%d builds=%d, want %d/%d", hits, builds, providerRequestMaxAttempts, providerRequestMaxAttempts)
	}
	_ = response.Body.Close()
	events := mustFindAuditEvents(t, readClosedAudit(t, observer, auditPath), "provider_response")
	if len(events) != providerRequestMaxAttempts {
		t.Fatalf("provider_response count = %d", len(events))
	}
	if events[0].RetryDecision != retryDecisionRetry {
		t.Fatalf("intermediate decisions = %q", events[0].RetryDecision)
	}
	if events[len(events)-1].RetryDecision != retryDecisionExhausted || events[len(events)-1].ErrorCategory != ProviderErrorServer5xx {
		t.Fatalf("final event = %+v", events[len(events)-1])
	}
}

func TestProviderRetry529DoesNotRetry(t *testing.T) {
	hits := 0
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(529)
		_, _ = writer.Write([]byte("overloaded"))
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
		t.Fatalf("529 should return response: %v", err)
	}
	if response.StatusCode != 529 {
		t.Fatalf("status = %d", response.StatusCode)
	}
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
	_ = response.Body.Close()
	events := mustFindAuditEvents(t, readClosedAudit(t, observer, auditPath), "provider_response")
	if len(events) != 1 {
		t.Fatalf("provider_response count = %d", len(events))
	}
	if events[0].RetryDecision != retryDecisionNoRetryStatus || events[0].ErrorCategory != ProviderErrorServer5xx {
		t.Fatalf("529 event = %+v", events[0])
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
	if events[0].Attempt != 1 || events[0].MaxAttempts != providerRequestMaxAttempts || events[0].RetryDecision != retryDecisionNoRetryStatus {
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
	if builds != hits || builds != providerRequestMaxAttempts {
		t.Fatalf("builds=%d hits=%d, want %d/%d", builds, hits, providerRequestMaxAttempts, providerRequestMaxAttempts)
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

func TestRetryingStreamBodyDeadlineDoesNotRetry(t *testing.T) {
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	calls := 0
	body := newRetryingStreamBody(ctx, &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected retry")
	})}, "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	}, io.NopCloser(strings.NewReader("")), providerRetryState{attempt: 1}, nil, instantRetry(), func() bool { return true })
	_, err := io.ReadAll(body)
	_ = body.Close()
	if !errors.Is(err, context.DeadlineExceeded) || calls != 0 {
		t.Fatalf("err=%v calls=%d, want deadline exceeded/0", err, calls)
	}
}

func TestRetryingStreamBodyRawBytesSuppressRetryAndCloseBodies(t *testing.T) {
	first := &countedBody{ReadCloser: io.NopCloser(strings.NewReader(`data: {"choices":[]}`))}
	calls := 0
	observer, auditPath := newRetryAuditObserver(t)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("must not retry after raw bytes")
	})}
	body := newRetryingStreamBody(context.Background(), client, "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	}, first, providerRetryState{attempt: 1}, observer, instantRetry(), func() bool { return true })
	payload, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(payload) != `data: {"choices":[]}` || calls != 0 || first.closes != 1 {
		t.Fatalf("payload=%q err=%v calls=%d closes=%d", payload, err, calls, first.closes)
	}
	if data := readClosedAudit(t, observer, auditPath); len(strings.TrimSpace(string(data))) != 0 {
		t.Fatalf("normal EOF after raw bytes recorded a failed retry decision: %s", data)
	}
}

func TestRetryStateCarriesInitialWaitIntoStreamBudget(t *testing.T) {
	var bodies []*countedBody
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if calls == 1 {
			return &http.Response{StatusCode: http.StatusTooManyRequests, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("retry")), Request: request}, nil
		}
		body := &countedBody{ReadCloser: io.NopCloser(strings.NewReader(""))}
		bodies = append(bodies, body)
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: body, Request: request}, nil
	})}
	retry := instantRetry()
	retry.baseDelay, retry.maxDelay, retry.maxTotalWait = time.Second, time.Second, time.Second
	resp, err := doProviderRequest(context.Background(), client, "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	}, nil, retry)
	if err != nil {
		t.Fatal(err)
	}
	state := responseRetryState(resp)
	if state.attempt != 2 || state.waited != time.Second {
		t.Fatalf("state=%+v", state)
	}
	stream := newRetryingStreamBody(context.Background(), client, "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	}, resp.Body, state, nil, retry, func() bool { return true })
	_, err = io.ReadAll(stream)
	_ = stream.Close()
	if err == nil || calls != 2 || len(bodies) != 1 || bodies[0].closes != 1 {
		t.Fatalf("err=%v calls=%d bodies=%d closes=%d", err, calls, len(bodies), bodies[0].closes)
	}
}

func TestRetryingStreamBodyRetriesOnlyZeroByteEOF(t *testing.T) {
	for _, test := range []struct {
		name string
		err  error
	}{
		{name: "EOF", err: io.EOF},
		{name: "unexpected EOF", err: io.ErrUnexpectedEOF},
	} {
		t.Run(test.name, func(t *testing.T) {
			first := &countedBody{ReadCloser: &errorAfterReader{err: test.err}}
			calls := 0
			client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("recovered")), Request: request}, nil
			})}
			body := newRetryingStreamBody(context.Background(), client, "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
				return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
			}, first, providerRetryState{attempt: 1}, nil, instantRetry(), func() bool { return true })
			payload, err := io.ReadAll(body)
			_ = body.Close()
			if err != nil || string(payload) != "recovered" || calls != 1 || first.closes != 1 {
				t.Fatalf("payload=%q err=%v calls=%d closes=%d", payload, err, calls, first.closes)
			}
		})
	}
}

func TestRetryingStreamBodyZeroByteNonEOFErrorDoesNotRetry(t *testing.T) {
	first := &countedBody{ReadCloser: &errorAfterReader{err: errors.New("connection reset by peer")}}
	calls := 0
	observer, auditPath := newRetryAuditObserver(t)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected retry")
	})}
	body := newRetryingStreamBody(context.Background(), client, "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	}, first, providerRetryState{attempt: 1}, observer, instantRetry(), func() bool { return true })
	_, err := io.ReadAll(body)
	_ = body.Close()
	if err == nil || !strings.Contains(err.Error(), "connection reset by peer") || calls != 0 || first.closes != 1 {
		t.Fatalf("err=%v calls=%d closes=%d", err, calls, first.closes)
	}
	decision := mustFindAuditEvent(t, readClosedAudit(t, observer, auditPath), "provider_response")
	if decision.RetryDecision != retryDecisionNoRetryStreamError || decision.Attempt != 1 {
		t.Fatalf("retry decision = %+v", decision)
	}
}

func TestRetryingStreamBodyRawBytesWithReadErrorNeverRetries(t *testing.T) {
	first := &countedBody{ReadCloser: &singleReadErrorBody{data: []byte("data: partial"), err: io.ErrUnexpectedEOF}}
	calls := 0
	observer, auditPath := newRetryAuditObserver(t)
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unexpected retry")
	})}
	body := newRetryingStreamBody(context.Background(), client, "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	}, first, providerRetryState{attempt: 1}, observer, instantRetry(), func() bool { return true })
	payload, err := io.ReadAll(body)
	_ = body.Close()
	if !errors.Is(err, io.ErrUnexpectedEOF) || string(payload) != "data: partial" || calls != 0 || first.closes != 1 {
		t.Fatalf("payload=%q err=%v calls=%d closes=%d", payload, err, calls, first.closes)
	}
	decision := mustFindAuditEvent(t, readClosedAudit(t, observer, auditPath), "provider_response")
	if decision.RetryDecision != retryDecisionNoRetryStreamRawBytes || decision.Attempt != 1 {
		t.Fatalf("retry decision = %+v", decision)
	}
}

func TestHTTPAndStreamRetryShareMaximumAttempts(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		switch calls {
		case 1:
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("")), Request: request}, nil
		default:
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("recovered")), Request: request}, nil
		}
	})}
	retry := instantRetry()
	resp, err := doProviderRequest(context.Background(), client, "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	}, nil, retry)
	if err != nil {
		t.Fatal(err)
	}
	body := newRetryingStreamBody(context.Background(), client, "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	}, resp.Body, responseRetryState(resp), nil, retry, func() bool { return true })
	payload, err := io.ReadAll(body)
	_ = body.Close()
	if err != nil || string(payload) != "recovered" || calls != providerRequestMaxAttempts {
		t.Fatalf("payload=%q err=%v calls=%d, want calls=%d", payload, err, calls, providerRequestMaxAttempts)
	}
}

// TestRetryingStreamBodyHasRawBytesPropagatesViaHTTPTest 使用真实 httptest 服务器验证：
// 1. 服务器发送部分字节后断开 → retryingStreamBody.HasRawBytes() 返回 true
// 2. StreamTruncatedError.RawBytesObserved 被设置为 true
// 3. isFallbackEligibleError 对该错误返回 false（有字节后禁止 fallback）
func TestRetryingStreamBodyHasRawBytesPropagatesViaHTTPTest(t *testing.T) {
	// 服务器：写入部分 SSE 内容后立即关闭连接（模拟截断）
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		// 写入不完整的 SSE frame——有字节但没有 [DONE] marker
		_, _ = fmt.Fprint(w, "data: {\"partial\":true}\n")
		// 强制 flush 后关闭
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// 服务器侧直接返回，HTTP/1.1 会关闭连接
	}))
	defer server.Close()

	var streamBodyRef *retryingStreamBody
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls++
		return server.Client().Transport.RoundTrip(req)
	})}

	// 先拿到首次 2xx 响应
	initialResp, err := doProviderRequest(context.Background(), server.Client(), "openai",
		"req-1", "call-1",
		func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/chat/completions", nil)
		}, nil, instantRetry())
	if err != nil {
		t.Fatalf("initial request: %v", err)
	}

	// 包装为 retryingStreamBody，canRetry=false 防止内部重试干扰断言
	streamBody := newRetryingStreamBody(
		context.Background(), client, "openai", "req-1", "call-1",
		func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodGet, server.URL+"/v1/chat/completions", nil)
		},
		initialResp.Body,
		responseRetryState(initialResp),
		nil,
		instantRetry(),
		func() bool { return false }, // canRetry=false
	)
	defer func() { _ = streamBody.Close() }()

	if rsb, ok := streamBody.(*retryingStreamBody); ok {
		streamBodyRef = rsb
	}

	// 读取全部内容（会读到服务器关闭后的 EOF）
	payload, readErr := io.ReadAll(streamBody)

	// 断言1：读到了字节内容
	if len(payload) == 0 {
		t.Fatal("expected some bytes from partial SSE, got nothing")
	}

	// 断言2：HasRawBytes() 返回 true
	if streamBodyRef != nil && !streamBodyRef.HasRawBytes() {
		t.Error("HasRawBytes() = false after reading partial SSE bytes, want true")
	}

	// 断言3：readErr 为 nil（EOF）或 io.EOF，因为服务器正常关闭
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		t.Logf("readErr = %v (acceptable non-EOF error after server close)", readErr)
	}

	// 断言4：若发生 StreamTruncatedError，其 RawBytesObserved 必须为 true；
	//         且 isFallbackEligibleError 必须返回 false
	if readErr != nil {
		var trunc *StreamTruncatedError
		if errors.As(readErr, &trunc) {
			if !trunc.RawBytesObserved {
				t.Error("StreamTruncatedError.RawBytesObserved = false after raw bytes, want true")
			}
			if isFallbackEligibleError(readErr) {
				t.Error("isFallbackEligibleError(truncated+bytes) = true, want false (fallback must be blocked)")
			}
		}
	}

	// 辅助：若读取成功但 HasRawBytes=true，构造一个显式的 StreamTruncatedError 来校验 eligible 判断
	if streamBodyRef != nil && streamBodyRef.HasRawBytes() {
		syntheticErr := &StreamTruncatedError{Provider: "openai", RawBytesObserved: true}
		if isFallbackEligibleError(syntheticErr) {
			t.Error("isFallbackEligibleError(StreamTruncatedError{RawBytesObserved:true}) = true, want false")
		}
		// RawBytesObserved=false 时仍然允许 fallback（对照）
		syntheticNoBytes := &StreamTruncatedError{Provider: "openai", RawBytesObserved: false}
		if !isFallbackEligibleError(syntheticNoBytes) {
			t.Error("isFallbackEligibleError(StreamTruncatedError{RawBytesObserved:false}) = false, want true")
		}
	}
}

type singleReadErrorBody struct {
	data []byte
	err  error
	done bool
}

func (body *singleReadErrorBody) Read(p []byte) (int, error) {
	if body.done {
		return 0, io.EOF
	}
	body.done = true
	return copy(p, body.data), body.err
}

func (body *singleReadErrorBody) Close() error { return nil }

func TestRetryingStreamBodyClosesWrapperAfterRetry(t *testing.T) {
	// 验证首个空 body 重试成功后，最终的 wrapper body 在 Stream 返回时被关闭
	originalClosed := false
	retryClosed := false
	attempt := 0

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempt++
		w.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			// 首次返回空 body，触发重试
			return
		}
		// 第二次返回正常完成
		_, _ = fmt.Fprint(w, "data: [DONE]\n\n")
	}))
	defer server.Close()

	adapter := &OpenAIAdapter{
		client: server.Client(),
		retry:  instantRetry(),
	}

	// 包装客户端以跟踪 body 关闭
	wrappedClient := &http.Client{
		Transport: &trackingTransport{
			base: server.Client().Transport,
			onResponse: func(resp *http.Response, attemptNum int) {
				// 包装 body 以跟踪关闭
				originalBody := resp.Body
				resp.Body = &trackingReadCloser{
					ReadCloser: originalBody,
					onClose: func() error {
						if attemptNum == 1 {
							originalClosed = true
						} else if attemptNum == 2 {
							retryClosed = true
						}
						return originalBody.Close()
					},
				}
			},
			attempt: &attempt,
		},
	}
	adapter.client = wrappedClient

	err := adapter.Stream(context.Background(), StreamRequest{
		RequestID:       "test-req",
		RunID:           "test-run",
		ModelCallID:     "test-call",
		BaseURL:         server.URL,
		APIKey:          "test-key",
		ProviderModelID: "gpt-test",
		Messages:        []Message{{Role: "user", Content: "test"}},
		MaxTokens:       100,
	}, func(ModelEvent) error { return nil })

	if err != nil {
		t.Fatalf("stream failed: %v", err)
	}
	if !originalClosed {
		t.Error("original body was not closed after retry")
	}
	if !retryClosed {
		t.Error("retry wrapper body was not closed after stream completed")
	}
	if attempt != 2 {
		t.Errorf("expected 2 attempts, got %d", attempt)
	}
}

type trackingTransport struct {
	base       http.RoundTripper
	onResponse func(*http.Response, int)
	attempt    *int
}

func (t *trackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.base == nil {
		t.base = http.DefaultTransport
	}
	resp, err := t.base.RoundTrip(req)
	if err == nil && t.onResponse != nil {
		t.onResponse(resp, *t.attempt)
	}
	return resp, err
}

type trackingReadCloser struct {
	io.ReadCloser
	onClose func() error
}

func (t *trackingReadCloser) Close() error {
	if t.onClose != nil {
		return t.onClose()
	}
	return t.ReadCloser.Close()
}

func TestRetryingStreamBodyCloseDuringRetryDoesNotLeakOrRetryAfterClose(t *testing.T) {
	started := make(chan struct{})
	unblock := make(chan struct{})
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		close(started)
		<-unblock
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("late")), Request: request}, nil
	})}
	first := &countedBody{ReadCloser: &errorAfterReader{err: io.EOF}}
	body := newRetryingStreamBody(context.Background(), client, "openai", "request-id", "model-call-id", func(ctx context.Context) (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test", nil)
	}, first, providerRetryState{attempt: 1}, nil, instantRetry(), func() bool { return true })

	errCh := make(chan error, 1)
	go func() {
		_, err := io.ReadAll(body)
		errCh <- err
	}()
	<-started
	if err := body.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	close(unblock)
	if err := <-errCh; !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Read after close = %v, want closed pipe", err)
	}
	if first.closes != 1 {
		t.Fatalf("original body closes = %d, want 1", first.closes)
	}
	if calls != 1 {
		t.Fatalf("retry calls = %d, want 1", calls)
	}
}

type privacyArtifactObserver struct {
	request map[string]any
}

func (observer *privacyArtifactObserver) RecordLLMRequest(_ string, _ string, _ string, payload map[string]any) (string, error) {
	observer.request = payload
	return "", nil
}

func (*privacyArtifactObserver) AppendLLMResponseChunk(string, string, string, string) (string, error) {
	return "", nil
}

func (*privacyArtifactObserver) RecordLLMSummary(string, string, string, map[string]any) (string, error) {
	return "", nil
}

func TestLLMRequestArtifactOmitsRequestAndMessageBodies(t *testing.T) {
	observer := &privacyArtifactObserver{}
	const secret = "child prompt and result must not be logged"
	req := StreamRequest{
		RequestID:              "request-privacy",
		RunID:                  "run-privacy",
		ModelCallID:            "call-privacy",
		FallbackArtifactSuffix: "_fb0",
		Messages:               []Message{{Role: "user", Content: secret}},
		Observer:               observer,
	}
	recordLLMRequestArtifact(req, "openai", "gpt-test", http.MethodPost, "https://example.test/v1/responses?api_key=url-secret#fragment", map[string]any{"input": secret, "api_key": "secret-key"})
	encoded, err := json.Marshal(observer.request)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte(secret)) || bytes.Contains(encoded, []byte("secret-key")) || bytes.Contains(encoded, []byte("url-secret")) || bytes.Contains(encoded, []byte("fragment")) || bytes.Contains(encoded, []byte("content_preview")) {
		t.Fatalf("request artifact retained sensitive content: %s", encoded)
	}
	if observer.request["body_byte_len"] == nil {
		t.Fatalf("request artifact omitted body size metadata: %#v", observer.request)
	}
	if observer.request["model_call_id"] != "call-privacy" {
		t.Fatalf("canonical model_call_id = %#v", observer.request["model_call_id"])
	}
	if observer.request["artifact_model_call_id"] != "call-privacy_fb0" || observer.request["fallback_channel_index"] != 0 {
		t.Fatalf("fallback artifact identity = %#v", observer.request)
	}
}

func TestModelArtifactErrorOmitsProviderResponseBody(t *testing.T) {
	const secretBody = "sensitive provider response"
	summary := summarizeModelArtifactError(&HTTPStatusError{Provider: "openai", StatusCode: http.StatusTooManyRequests, Body: secretBody})
	if strings.Contains(summary, secretBody) {
		t.Fatalf("error summary retained provider body: %q", summary)
	}
	if summary != ProviderErrorRateLimited+" status=429" {
		t.Fatalf("error summary = %q", summary)
	}
}

func TestApplyStreamRequestRetryOverridesWaitIncludingZero(t *testing.T) {
	base := providerRetry{}
	zeroWait := applyStreamRequestRetry(base, StreamRequest{
		FallbackBudget:        NewFallbackRetryBudget(5, 0),
		FallbackRemainingWait: 0,
		FallbackMaxAttempts:   2,
	})
	if zeroWait.maxTotalWait != 0 {
		t.Fatalf("wait=0 overlay fell back to %v, want 0", zeroWait.maxTotalWait)
	}
	if zeroWait.maxAttempts != 2 {
		t.Fatalf("maxAttempts = %d, want 2", zeroWait.maxAttempts)
	}

	chainWait := applyStreamRequestRetry(base, StreamRequest{
		FallbackBudget:        NewFallbackRetryBudget(7, 8*time.Second),
		FallbackRemainingWait: 8 * time.Second,
	})
	if chainWait.maxTotalWait != 8*time.Second {
		t.Fatalf("chain wait overlay = %v, want 8s", chainWait.maxTotalWait)
	}

	disabled := applyStreamRequestRetry(base, StreamRequest{
		FallbackRemainingWait: 0,
		FallbackMaxAttempts:   2,
	})
	if disabled.maxTotalWait != defaultRetryMaxTotalWait {
		t.Fatalf("nil FallbackBudget wait = %v, want default %v", disabled.maxTotalWait, defaultRetryMaxTotalWait)
	}
	if disabled.maxAttempts != 2 {
		t.Fatalf("disabled path still honors explicit FallbackMaxAttempts: got %d", disabled.maxAttempts)
	}
}

func TestProviderRetryRetryAfterExceedsWaitBudgetDoesNotRetry(t *testing.T) {
	hits := 0
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.Header().Set("Retry-After", "30")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":"later"}`))
	}))
	defer providerServer.Close()

	var delays []time.Duration
	retry := recordingRetry(&delays)
	retry.maxDelay = 30 * time.Second
	retry.maxTotalWait = 8 * time.Second
	retry.fallbackBudget = NewFallbackRetryBudget(5, 8*time.Second)
	retry.fallbackSafety = &FallbackSafetyInfo{}

	response, err := doProviderRequest(
		context.Background(),
		providerServer.Client(),
		"openai",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodPost, providerServer.URL+"/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
		},
		audit.Default(),
		retry,
	)
	if err != nil {
		t.Fatalf("over-budget Retry-After should return the 429 response, not sleep/retry: %v", err)
	}
	if response == nil || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("response = %+v, want 429", response)
	}
	_ = response.Body.Close()
	if hits != 1 {
		t.Fatalf("hits = %d, want 1 (no zero-delay same-channel retry)", hits)
	}
	if len(delays) != 0 {
		t.Fatalf("slept %v, want none", delays)
	}
	if !retry.fallbackSafety.Snapshot().WaitBudgetBlocked {
		t.Fatal("expected WaitBudgetBlocked after Retry-After exceeded remaining wait")
	}
}

func TestProviderRetryWaitZeroDoesNotFallBackToDefaultWait(t *testing.T) {
	hits := 0
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		hits++
		writer.WriteHeader(http.StatusTooManyRequests)
	}))
	defer providerServer.Close()

	var delays []time.Duration
	retry := applyStreamRequestRetry(recordingRetry(&delays), StreamRequest{
		FallbackBudget:        NewFallbackRetryBudget(5, 0),
		FallbackRemainingWait: 0,
		FallbackMaxAttempts:   3,
	})
	retry.fallbackSafety = &FallbackSafetyInfo{}
	retry.sleep = func(ctx context.Context, delay time.Duration) error {
		delays = append(delays, delay)
		return ctx.Err()
	}

	response, err := doProviderRequest(
		context.Background(),
		providerServer.Client(),
		"openai",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodPost, providerServer.URL+"/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
		},
		audit.Default(),
		retry,
	)
	if err != nil {
		t.Fatalf("wait=0 should return the 429 response without retry: %v", err)
	}
	if response == nil || response.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("response = %+v, want 429", response)
	}
	_ = response.Body.Close()
	if hits != 1 {
		t.Fatalf("hits = %d, want 1", hits)
	}
	if len(delays) != 0 {
		t.Fatalf("slept %v, want none", delays)
	}
}

func TestZeroEventStreamRetryAllowlist(t *testing.T) {
	resetErr := &net.OpError{Op: "read", Net: "tcp", Err: syscall.ECONNRESET}
	for _, test := range []struct {
		name string
		err  error
		want bool
	}{
		{name: "plain EOF", err: newStreamTruncatedError("openai", io.EOF), want: true},
		{name: "unexpected EOF", err: newStreamTruncatedError("openai", io.ErrUnexpectedEOF), want: true},
		{name: "connection reset", err: newStreamTruncatedError("openai", resetErr), want: true},
		{name: "HTTP2 stream reset", err: newStreamTruncatedError("openai", http2.StreamError{StreamID: 1, Code: http2.ErrCodeInternal}), want: true},
		{name: "HTTP2 GOAWAY", err: newStreamTruncatedError("openai", http2.GoAwayError{LastStreamID: 1, ErrCode: http2.ErrCodeInternal}), want: true},
		{name: "missing marker after clean EOF", err: newStreamTruncatedError("openai", nil), want: true},
		{name: "context canceled", err: newStreamTruncatedError("openai", context.Canceled), want: false},
		{name: "deadline", err: newStreamTruncatedError("openai", context.DeadlineExceeded), want: false},
		{name: "HTTP 400", err: &HTTPStatusError{StatusCode: http.StatusBadRequest}, want: false},
		{name: "authentication", err: &HTTPStatusError{StatusCode: http.StatusUnauthorized}, want: false},
		{name: "quota", err: &HTTPStatusError{StatusCode: http.StatusTooManyRequests}, want: false},
		{name: "provider terminal", err: &ProviderTerminalStatusError{Provider: "openai", Status: "failed"}, want: false},
		{name: "JSON syntax", err: &json.SyntaxError{}, want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := IsRetryableZeroEventStreamError(test.err); got != test.want {
				t.Fatalf("IsRetryableZeroEventStreamError(%T) = %v, want %v", test.err, got, test.want)
			}
		})
	}
}

func TestRunStreamWithZeroEventRecoverySafetyBoundary(t *testing.T) {
	t.Run("retries exactly once and keeps final diagnostics", func(t *testing.T) {
		diagnostics := &StreamDiagnostics{}
		calls := 0
		err := runStreamWithZeroEventRecovery(context.Background(), StreamRequest{StreamDiagnostics: diagnostics}, func(ModelEvent) error { return nil }, func(_ context.Context, req StreamRequest, _ func(ModelEvent) error) error {
			calls++
			if calls == 1 {
				req.StreamDiagnostics.RecordHTTPResponse(&http.Response{StatusCode: http.StatusOK, Proto: "HTTP/2.0", Header: http.Header{"Content-Encoding": []string{"gzip"}}, ContentLength: -1}, 1, time.Now())
				req.StreamDiagnostics.RecordConnection(true, true)
				return newStreamTruncatedError("openai", io.ErrUnexpectedEOF)
			}
			if req.StreamRecoveryAttempt != 1 || req.FallbackMaxAttempts != 1 {
				t.Fatalf("recovery request = %#v", req)
			}
			req.StreamDiagnostics.RecordHTTPResponse(&http.Response{StatusCode: http.StatusOK, Proto: "HTTP/1.1", Header: make(http.Header), ContentLength: -1}, 1, time.Now())
			req.StreamDiagnostics.RecordConnection(false, false)
			return newStreamTruncatedError("openai", io.EOF)
		})
		if err == nil || calls != 2 {
			t.Fatalf("err=%v calls=%d, want error/2", err, calls)
		}
		snapshot := diagnostics.Snapshot()
		if snapshot.StreamRecoveryAttempts != 1 || snapshot.HTTPProtocol != "HTTP/1.1" || snapshot.ContentEncoding != "identity" || snapshot.ConnectionReused {
			t.Fatalf("final diagnostics = %#v", snapshot)
		}
	})

	for _, test := range []struct {
		name    string
		prepare func(*StreamDiagnostics, func(ModelEvent) error)
	}{
		{name: "raw bytes observed", prepare: func(diagnostics *StreamDiagnostics, _ func(ModelEvent) error) { diagnostics.RecordBytes(1, time.Now()) }},
		{name: "model event observed", prepare: func(_ *StreamDiagnostics, sink func(ModelEvent) error) {
			_ = sink(ModelEvent{Kind: ModelEventKindTextDelta})
		}},
	} {
		t.Run(test.name+" prevents retry", func(t *testing.T) {
			diagnostics := &StreamDiagnostics{}
			calls := 0
			err := runStreamWithZeroEventRecovery(context.Background(), StreamRequest{StreamDiagnostics: diagnostics}, func(ModelEvent) error { return nil }, func(_ context.Context, _ StreamRequest, sink func(ModelEvent) error) error {
				calls++
				test.prepare(diagnostics, sink)
				return newStreamTruncatedError("openai", io.ErrUnexpectedEOF)
			})
			if err == nil || calls != 1 || diagnostics.Snapshot().StreamRecoveryAttempts != 0 {
				t.Fatalf("err=%v calls=%d diagnostics=%#v", err, calls, diagnostics.Snapshot())
			}
		})
	}
}
