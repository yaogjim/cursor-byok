package upstream

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"cursor/gen/aiserverv1"
	"cursor/internal/audit"
	"cursor/internal/backend/server"

	"google.golang.org/protobuf/proto"
)

func TestHandleDirectAuditsMetadataWithoutPersistingCanary(t *testing.T) {
	const canary = "synthetic-route-canary"
	requestBody, err := proto.Marshal(&aiserverv1.CppConfigRequest{Model: canary})
	if err != nil {
		t.Fatal(err)
	}

	forwarded := false
	upstreamServer := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, readErr := io.ReadAll(request.Body)
		if readErr != nil {
			t.Fatalf("read fake upstream body: %v", readErr)
		}
		forwarded = bytes.Contains(body, []byte(canary))
		writer.WriteHeader(http.StatusOK)
		_, _ = writer.Write([]byte("ok"))
	}))
	defer upstreamServer.Close()

	targetURL, err := url.Parse(upstreamServer.URL + "/aiserver.v1.AiService/CppConfig")
	if err != nil {
		t.Fatal(err)
	}
	auditPath := filepath.Join(t.TempDir(), "audit.jsonl")
	observer, err := audit.New(audit.Options{FilePath: auditPath, Canary: canary})
	if err != nil {
		t.Fatal(err)
	}
	defer observer.Close()

	request := httptest.NewRequest(http.MethodPost, "http://backend/aiserver.v1.AiService/CppConfig", bytes.NewReader(requestBody))
	request.Header.Set("content-type", "application/proto")
	recorder := httptest.NewRecorder()
	requestContext := &RequestContext{
		ResponseWriter: recorder,
		Request:        request,
		StartedAt:      time.Now(),
		TargetURL:      targetURL,
		Method:         http.MethodPost,
		Headers:        request.Header.Clone(),
		RequestBody:    requestBody,
		Mode:           server.ModeLocal,
		Deps:           &Dependencies{HTTPClient: upstreamServer.Client(), Audit: observer},
	}

	if err := handleDirect(requestContext, &Route{Name: "cpp_config", Audit: true}); err != nil {
		t.Fatal(err)
	}
	if !forwarded {
		t.Fatal("fake upstream did not receive the canary request")
	}
	if recorder.Code != http.StatusOK || recorder.Body.String() != "ok" {
		t.Fatalf("unexpected response: status=%d body=%q", recorder.Code, recorder.Body.String())
	}
	if err := observer.Close(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(auditPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), canary) {
		t.Fatal("audit output persisted the canary")
	}
	if !strings.Contains(string(data), "\"canary_matched\":true") {
		t.Fatal("audit output did not record the canary match metadata")
	}
	if !strings.Contains(string(data), "\"target_host\":\"127.0.0.1\"") {
		t.Fatal("audit output did not record the target host")
	}
}
