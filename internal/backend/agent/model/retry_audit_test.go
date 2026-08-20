package modeladapter

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cursor/internal/audit"
)

func TestProviderAuditMatchesCanaryWithoutPersistingBody(t *testing.T) {
	const canary = "synthetic-provider-canary"
	payload := []byte(`{"input":"` + canary + `"}`)
	forwarded := false
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read fake provider body: %v", err)
		}
		forwarded = bytes.Contains(body, []byte(canary))
		writer.Header().Set("content-type", "text/event-stream")
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer providerServer.Close()

	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	observer, err := audit.New(audit.Options{FilePath: auditPath, Canary: canary})
	if err != nil {
		t.Fatal(err)
	}
	response, err := doProviderRequestWithAudit(
		context.Background(),
		providerServer.Client(),
		"openai",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(ctx, http.MethodPost, providerServer.URL+"/v1/responses", bytes.NewReader(payload))
		},
		observer,
	)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	if !forwarded {
		t.Fatal("fake provider did not receive the canary request")
	}
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	output := string(data)
	if strings.Contains(output, canary) || strings.Contains(output, string(payload)) {
		t.Fatal("provider audit persisted request content")
	}
	if !strings.Contains(output, "\"canary_matched\":true") {
		t.Fatal("provider audit did not record canary match metadata")
	}
	if !strings.Contains(output, "\"endpoint\":\"responses\"") {
		t.Fatal("provider audit did not classify the endpoint")
	}
	if !strings.Contains(output, "\"target_host\":\"127.0.0.1\"") {
		t.Fatal("provider audit did not record the target host")
	}
	responseEvent := mustFindAuditEvent(t, data, "provider_response")
	if responseEvent.Attempt != 1 || responseEvent.MaxAttempts != providerRequestMaxAttempts || responseEvent.RetryDecision != retryDecisionSuccess {
		t.Fatalf("success response missing attempt metadata: %+v", responseEvent)
	}
}

func TestProviderAuditRecordsTransportErrorWithoutSecrets(t *testing.T) {
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	observer, err := audit.New(audit.Options{FilePath: auditPath})
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: secretErrorTransport{}}
	_, err = doProviderRequest(
		context.Background(),
		client,
		"openai",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.test/v1/messages?api_key=query-secret", bytes.NewReader([]byte(`{"prompt":"do-not-record"}`)))
			if err != nil {
				return nil, err
			}
			request.Header.Set("Authorization", "Bearer header-secret")
			request.Header.Set("Cookie", "session=cookie-secret")
			return request, nil
		},
		observer,
		instantRetry(),
	)
	if err == nil {
		t.Fatal("transport error expected")
	}
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	assertAuditOmitsSecrets(t, data, "query-secret", "header-secret", "cookie-secret", "do-not-record", "api_key=query-secret")
	events := mustFindAuditEvents(t, data, "provider_response")
	if len(events) != providerRequestMaxAttempts {
		t.Fatalf("transport provider_response count = %d", len(events))
	}
	event := events[len(events)-1]
	if event.ErrorCategory != ProviderErrorTransport {
		t.Fatalf("transport error_category = %q", event.ErrorCategory)
	}
	if event.ErrorMessage == "" || event.Attempt != providerRequestMaxAttempts || event.MaxAttempts != providerRequestMaxAttempts || event.RetryDecision != retryDecisionExhausted {
		t.Fatalf("transport event missing structured fields: %+v", event)
	}
	if events[0].RetryDecision != retryDecisionRetry {
		t.Fatalf("first transport decision = %q", events[0].RetryDecision)
	}
	if strings.Contains(event.ErrorMessage, "?") {
		t.Fatalf("transport error_message retained query: %q", event.ErrorMessage)
	}
}

func TestProviderAuditRecordsHTTPStatusErrorMetadata(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Retry-After", "1")
		writer.WriteHeader(http.StatusTooManyRequests)
		_, _ = writer.Write([]byte(`{"error":{"message":"API key provided: sk-secret"}}`))
	}))
	defer providerServer.Close()

	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	observer, err := audit.New(audit.Options{FilePath: auditPath})
	if err != nil {
		t.Fatal(err)
	}
	response, err := doProviderRequest(
		context.Background(),
		providerServer.Client(),
		"anthropic",
		"request-id",
		"model-call-id",
		func(ctx context.Context) (*http.Request, error) {
			request, err := http.NewRequestWithContext(ctx, http.MethodPost, providerServer.URL+"/v1/messages?token=query-secret", bytes.NewReader([]byte(`{"prompt":"do-not-record"}`)))
			if err != nil {
				return nil, err
			}
			request.Header.Set("Authorization", "Bearer header-secret")
			request.Header.Set("Cookie", "session=cookie-secret")
			return request, nil
		},
		observer,
		instantRetry(),
	)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	assertAuditOmitsSecrets(t, data, "sk-secret", "query-secret", "header-secret", "cookie-secret", "do-not-record")
	if bytes.Contains(data, body) {
		t.Fatal("provider audit persisted HTTP error body")
	}
	events := mustFindAuditEvents(t, data, "provider_response")
	if len(events) != providerRequestMaxAttempts {
		t.Fatalf("429 provider_response count = %d", len(events))
	}
	event := events[len(events)-1]
	if event.Status != http.StatusTooManyRequests || event.ErrorCategory != ProviderErrorRateLimited {
		t.Fatalf("http error metadata = %+v", event)
	}
	if !event.RetryAfterPresent || event.Attempt != providerRequestMaxAttempts || event.MaxAttempts != providerRequestMaxAttempts || event.RetryDecision != retryDecisionExhausted {
		t.Fatalf("http error missing attempt/retry-after metadata: %+v", event)
	}
	if event.ErrorMessage != "http status=429" {
		t.Fatalf("http error_message = %q", event.ErrorMessage)
	}
}

func TestProviderAuditRecordsCanceledTransportCategory(t *testing.T) {
	providerServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(http.StatusOK)
	}))
	defer providerServer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	observer, err := audit.New(audit.Options{FilePath: auditPath})
	if err != nil {
		t.Fatal(err)
	}
	_, err = doProviderRequestWithAudit(
		ctx,
		providerServer.Client(),
		"openai",
		"request-id",
		"model-call-id",
		func(requestContext context.Context) (*http.Request, error) {
			return http.NewRequestWithContext(requestContext, http.MethodPost, providerServer.URL+"/v1/chat/completions", bytes.NewReader([]byte(`{}`)))
		},
		observer,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled request error = %v", err)
	}
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	event := mustFindAuditEvent(t, data, "provider_response")
	if event.ErrorCategory != ProviderErrorContextCanceled || event.Attempt != 1 || event.MaxAttempts != providerRequestMaxAttempts || event.RetryDecision != retryDecisionNoRetryContext {
		t.Fatalf("canceled event = %+v", event)
	}
}

type secretErrorTransport struct{}

func (secretErrorTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	return nil, fmt.Errorf(`Post "%s": Authorization: Bearer header-secret cookie=cookie-secret`, request.URL.String())
}

func mustFindAuditEvent(t *testing.T, data []byte, kind string) audit.Event {
	t.Helper()
	for _, line := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var event audit.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode audit event: %v", err)
		}
		if event.Kind == kind {
			return event
		}
	}
	t.Fatalf("missing %s audit event in %s", kind, data)
	return audit.Event{}
}

func assertAuditOmitsSecrets(t *testing.T, data []byte, secrets ...string) {
	t.Helper()
	output := string(data)
	for _, secret := range secrets {
		if strings.Contains(output, secret) {
			t.Fatalf("audit persisted %q: %s", secret, output)
		}
	}
}
