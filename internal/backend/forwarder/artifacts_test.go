package forwarder

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cursor/internal/observability"
)

type enabledDebugLogConfig struct{}

func (enabledDebugLogConfig) IsObservabilityLogEnabled(context.Context) bool {
	return true
}

func TestDebugRecorderUsesUnifiedSanitizedCapture(t *testing.T) {
	historyRoot := filepath.Join(t.TempDir(), "history")
	logsRoot := filepath.Join(t.TempDir(), "logs")
	capture, err := observability.NewRecorder(logsRoot, observability.Settings{
		Mode:          observability.ModeFull,
		RetentionDays: 7,
		MaxDiskMB:     64,
	})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	status := capture.Status()
	debug := newDebugRecorder(historyRoot, nil, enabledDebugLogConfig{}, capture)
	debug.LogProviderArtifact(context.Background(), "request-1", "conversation-1", "model-call-1", "llm_request", map[string]any{
		"prompt": "full prompt",
		"apiKey": "provider-secret",
	})
	if err := capture.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	legacyPath := filepath.Join(historyRoot, "conversation-1", "debug", "provider.jsonl")
	if _, err := os.Stat(legacyPath); !os.IsNotExist(err) {
		t.Fatalf("legacy debug artifact still exists: %v", err)
	}
	payloadDir := filepath.Join(status.SessionPath, "payloads")
	entries, err := os.ReadDir(payloadDir)
	if err != nil {
		t.Fatalf("read unified payload directory: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("payload count = %d, want 1", len(entries))
	}
	payload, err := os.ReadFile(filepath.Join(payloadDir, entries[0].Name()))
	if err != nil {
		t.Fatalf("read unified payload: %v", err)
	}
	text := string(payload)
	if !strings.Contains(text, "full prompt") || !strings.Contains(text, observability.RedactedValue) {
		t.Fatalf("unexpected unified payload: %s", text)
	}
	if strings.Contains(text, "provider-secret") {
		t.Fatalf("unified payload retained credential: %s", text)
	}
}

func TestUnifiedCaptureOmitsUnknownBidiRawBytes(t *testing.T) {
	logsRoot := filepath.Join(t.TempDir(), "logs")
	capture, err := observability.NewRecorder(logsRoot, observability.Settings{
		Mode:          observability.ModeFull,
		RetentionDays: 7,
		MaxDiskMB:     64,
	})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	status := capture.Status()
	debug := newDebugRecorder(t.TempDir(), nil, enabledDebugLogConfig{}, capture)
	debug.LogBidiRaw(context.Background(), "request-raw", "conversation-raw", 1, "deadbeef", "accepted", nil)
	if err := capture.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(status.SessionPath, "payloads"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("payload count = %d, want 1", len(entries))
	}
	payload, err := os.ReadFile(filepath.Join(status.SessionPath, "payloads", entries[0].Name()))
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	if strings.Contains(text, "deadbeef") || strings.Contains(text, "data_hex") {
		t.Fatalf("unknown raw bytes were persisted: %s", text)
	}
	if !strings.Contains(text, "raw_omitted") {
		t.Fatalf("omission metadata missing: %s", text)
	}
}

func TestHTTPTraceReplacesPrematureBackgroundCorrelation(t *testing.T) {
	capture, err := observability.NewRecorder(t.TempDir(), observability.Settings{
		Mode: observability.ModeBasic, RetentionDays: 7, MaxDiskMB: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	status := capture.Status()
	debug := newDebugRecorder(t.TempDir(), nil, enabledDebugLogConfig{}, capture)
	debug.LogRuntime(context.Background(), "request-correlation", "conversation-1", "premature_background_event", nil)
	authoritative := observability.Correlation{
		TraceID: "trace-from-mitm", SpanID: "backend-span", HTTPRequestID: "http-request-1",
	}
	httpContext := observability.WithCorrelation(context.Background(), authoritative)
	debug.LogBidiRaw(httpContext, "request-correlation", "conversation-1", 1, "00", "accepted", nil)
	debug.LogProviderArtifact(context.Background(), "request-correlation", "conversation-1", "model-call-1", "llm_request", map[string]any{"model": "test"})
	if err := capture.Close(); err != nil {
		t.Fatal(err)
	}
	payload, err := os.ReadFile(filepath.Join(status.SessionPath, "events.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
	if len(lines) != 3 {
		t.Fatalf("event count = %d, want 3", len(lines))
	}
	var bidiEvent observability.Event
	var providerEvent observability.Event
	if err := json.Unmarshal([]byte(lines[1]), &bidiEvent); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(lines[2]), &providerEvent); err != nil {
		t.Fatal(err)
	}
	if bidiEvent.TraceID != authoritative.TraceID || providerEvent.TraceID != authoritative.TraceID {
		t.Fatalf("authoritative trace was not retained: bidi=%+v provider=%+v", bidiEvent, providerEvent)
	}
	if providerEvent.HTTPRequestID != authoritative.HTTPRequestID {
		t.Fatalf("provider HTTP correlation = %q, want %q", providerEvent.HTTPRequestID, authoritative.HTTPRequestID)
	}
}

func TestArtifactRecorderRetainsOnlyRequestPrefixUntilCleared(t *testing.T) {
	recorder := newArtifactRecorder(nil, nil, nil)
	const sensitivePayload = "complete replay payload must not remain in memory"
	payload := map[string]any{
		"provider":        "openai",
		"model":           "gpt-test",
		"openai_endpoint": "/v1/responses",
		"messages_summary": []any{
			map[string]any{"role": "system"},
			map[string]any{"role": "user"},
			map[string]any{"role": "assistant"},
		},
		"full_payload": sensitivePayload,
	}
	if _, err := recorder.RecordLLMRequest("request-1", "run-1", "model-call-1", payload); err != nil {
		t.Fatalf("RecordLLMRequest() error = %v", err)
	}

	recorder.mu.Lock()
	session, ok := recorder.sessions[artifactSessionKey("request-1", "model-call-1")]
	recorder.mu.Unlock()
	if !ok {
		t.Fatal("request prefix session was not created")
	}
	if !session.hasRequestPrefix {
		t.Fatal("request prefix was not retained")
	}
	if session.requestPrefix.Provider != "openai" || session.requestPrefix.Model != "gpt-test" || session.requestPrefix.ReplayMessageCount != 2 {
		t.Fatalf("request prefix = %#v", session.requestPrefix)
	}
	if strings.Contains(fmt.Sprintf("%#v", session), sensitivePayload) {
		t.Fatal("artifact session retained the complete provider payload")
	}

	recorder.ClearActiveArtifacts("request-1", "model-call-1")
	recorder.mu.Lock()
	remaining := len(recorder.sessions)
	recorder.mu.Unlock()
	if remaining != 0 {
		t.Fatalf("active artifact sessions = %d, want 0", remaining)
	}
}

func TestArtifactFilesAreOwnerOnly(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows does not expose Unix permission bits consistently")
	}

	t.Run("atomic conversation file", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "state.json")
		file, tempPath, err := openUniqueArtifactTempFile(path)
		if err != nil {
			t.Fatalf("openUniqueArtifactTempFile() error = %v", err)
		}
		if err := file.Close(); err != nil {
			t.Fatalf("close temp artifact: %v", err)
		}
		info, err := os.Stat(tempPath)
		if err != nil {
			t.Fatalf("stat temp artifact: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("artifact permissions = %04o, want 0600", got)
		}
	})

	t.Run("provider debug log", func(t *testing.T) {
		root := t.TempDir()
		recorder := newDebugRecorder(root, nil, enabledDebugLogConfig{})
		recorder.LogProviderArtifact(context.Background(), "request-1", "conversation-1", "model-call-1", "llm_request", map[string]any{"payload": "sensitive"})
		path := filepath.Join(root, "conversation-1", "debug", "provider.jsonl")
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat provider debug log: %v", err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("provider debug log permissions = %04o, want 0600", got)
		}
	})
}
