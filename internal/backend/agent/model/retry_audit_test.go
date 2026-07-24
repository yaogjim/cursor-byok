package modeladapter

import (
	"bytes"
	"context"
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
}
