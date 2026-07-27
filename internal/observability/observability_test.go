package observability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestSanitizeRemovesCredentialsRecursively(t *testing.T) {
	input := map[string]any{
		"Authorization": "Bearer top-secret",
		"nested": map[string]any{
			"api_key":  "key-secret",
			"safe":     "prompt remains available in full mode",
			"endpoint": "https://example.test/v1/messages?token=query-secret&model=safe",
		},
		"headers": map[string][]string{
			"Cookie":         {"session=cookie-secret"},
			"X-Correlation":  {"safe-id"},
			"X-API-Key":      {"header-secret"},
			"Content-Length": {"123"},
		},
		"binary": []byte("binary-secret"),
		"json":   json.RawMessage(`{"client_secret":"json-secret","value":"safe"}`),
		"error":  `Post "https://example.test/v1?api_key=embedded-secret": Bearer bearer-secret`,
	}

	payload, err := json.Marshal(Sanitize(input))
	if err != nil {
		t.Fatalf("marshal sanitized value: %v", err)
	}
	text := string(payload)
	for _, secret := range []string{"top-secret", "key-secret", "query-secret", "cookie-secret", "header-secret", "binary-secret", "json-secret", "embedded-secret", "bearer-secret"} {
		if strings.Contains(text, secret) {
			t.Fatalf("sanitized payload retained %q: %s", secret, text)
		}
	}
	for _, expected := range []string{RedactedValue, "prompt remains available in full mode", "safe-id", "binary_payload", "model=safe"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("sanitized payload missing %q: %s", expected, text)
		}
	}
}

func TestSanitizeTextRemovesLabeledCredentials(t *testing.T) {
	input := `provider failed: API key provided: sk-secret; token=token-secret; Authorization: Bearer bearer-secret`
	output := SanitizeText(input)
	for _, secret := range []string{"sk-secret", "token-secret", "bearer-secret"} {
		if strings.Contains(output, secret) {
			t.Fatalf("sanitized text retained %q: %s", secret, output)
		}
	}
	if !strings.Contains(output, RedactedValue) {
		t.Fatalf("sanitized text missing redaction marker: %s", output)
	}
}

func TestRecorderBasicWritesEventsWithoutPayload(t *testing.T) {
	root := t.TempDir()
	recorder, err := NewRecorder(root, Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	status := recorder.Status()
	correlation := Correlation{TraceID: "trace-1", SpanID: "span-1", CursorRequestID: "cursor-1"}
	accepted := recorder.Record(WithCorrelation(context.Background(), correlation), Capture{
		Event: Event{
			Layer:  "backend",
			Event:  "request_finished",
			Status: "success",
			Fields: map[string]any{"Authorization": "Bearer event-secret", "method": "POST"},
		},
		Payload: &Payload{Name: "request", Data: map[string]any{"prompt": "must-not-be-written"}},
	})
	if !accepted {
		t.Fatal("basic event was not accepted")
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	events, err := os.ReadFile(filepath.Join(status.SessionPath, eventsFilename))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	text := string(events)
	for _, unexpected := range []string{"event-secret", "must-not-be-written", "payload_ref"} {
		if strings.Contains(text, unexpected) {
			t.Fatalf("basic events retained %q: %s", unexpected, text)
		}
	}
	for _, expected := range []string{"trace-1", "span-1", "cursor-1", RedactedValue} {
		if !strings.Contains(text, expected) {
			t.Fatalf("basic events missing %q: %s", expected, text)
		}
	}
	if _, err := os.Stat(filepath.Join(status.SessionPath, payloadsDirname)); !os.IsNotExist(err) {
		t.Fatalf("basic session created payload directory: %v", err)
	}
	manifest, err := readManifest(filepath.Join(status.SessionPath, manifestFilename))
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if manifest.Status != "closed" || manifest.ClosedAt == nil {
		t.Fatalf("manifest was not closed: %+v", manifest)
	}
	assertPrivatePermissions(t, status.SessionPath, 0o700)
	assertPrivatePermissions(t, filepath.Join(status.SessionPath, eventsFilename), 0o600)
}

func TestRecorderContainsHumanSinkPanic(t *testing.T) {
	recorder, err := NewRecorderWithHumanSink(
		t.TempDir(),
		Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64},
		func(Event) { panic("sink failure") },
	)
	if err != nil {
		t.Fatalf("NewRecorderWithHumanSink() error = %v", err)
	}
	if !recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "request_finished"}) {
		t.Fatal("event was not accepted")
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	status := recorder.Status()
	if status.LastError != "capture_sink_panic" {
		t.Fatalf("last error = %q, want capture_sink_panic", status.LastError)
	}
}

func TestRecorderFullWritesSanitizedPayload(t *testing.T) {
	root := t.TempDir()
	recorder, err := NewRecorder(root, Settings{Mode: ModeFull, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	status := recorder.Status()
	if !recorder.Record(context.Background(), Capture{
		Event: Event{Layer: "provider", Event: "request_sent"},
		Payload: &Payload{
			Name:        "provider_request",
			ContentType: "application/json",
			Data: map[string]any{
				"prompt":        "full prompt",
				"apiKey":        "provider-secret",
				"Authorization": "Bearer provider-secret",
			},
		},
	}) {
		t.Fatal("full event was not accepted")
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	events, err := os.ReadFile(filepath.Join(status.SessionPath, eventsFilename))
	if err != nil {
		t.Fatalf("read events: %v", err)
	}
	var event Event
	if err := json.Unmarshal([]byte(strings.TrimSpace(string(events))), &event); err != nil {
		t.Fatalf("decode event: %v", err)
	}
	if event.PayloadRef == "" {
		t.Fatalf("full event missing payload ref: %s", events)
	}
	payloadPath := filepath.Join(status.SessionPath, filepath.FromSlash(event.PayloadRef))
	payload, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatalf("read payload: %v", err)
	}
	text := string(payload)
	if !strings.Contains(text, "full prompt") || !strings.Contains(text, RedactedValue) {
		t.Fatalf("unexpected full payload: %s", text)
	}
	if strings.Contains(text, "provider-secret") {
		t.Fatalf("full payload retained credential: %s", text)
	}
	assertPrivatePermissions(t, filepath.Join(status.SessionPath, payloadsDirname), 0o700)
	assertPrivatePermissions(t, payloadPath, 0o600)
}

func TestCleanupClosedSessionsPreservesOpenSessions(t *testing.T) {
	root := t.TempDir()
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(tracesRoot); err != nil {
		t.Fatalf("create traces root: %v", err)
	}
	old := time.Now().UTC().Add(-48 * time.Hour)
	closedPath := writeTestSession(t, tracesRoot, "closed-session", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "closed-session",
		Mode:          ModeFull,
		Status:        "closed",
		StartedAt:     old,
	})
	openPath := writeTestSession(t, tracesRoot, "open-session", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "open-session",
		Mode:          ModeFull,
		Status:        "open",
		StartedAt:     old,
	})

	if err := CleanupClosedSessions(root, Settings{Mode: ModeFull, RetentionDays: 1, MaxDiskMB: 64}); err != nil {
		t.Fatalf("CleanupClosedSessions() error = %v", err)
	}
	if _, err := os.Stat(closedPath); !os.IsNotExist(err) {
		t.Fatalf("expired closed session still exists: %v", err)
	}
	if _, err := os.Stat(openPath); err != nil {
		t.Fatalf("open session was removed: %v", err)
	}
}

func TestQuotaCleanupDeletesClosedFullBeforeBasic(t *testing.T) {
	root := t.TempDir()
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(tracesRoot); err != nil {
		t.Fatalf("create traces root: %v", err)
	}
	startedAt := time.Now().UTC()
	basicPath := writeTestSession(t, tracesRoot, "basic-session", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "basic-session",
		Mode:          ModeBasic,
		Status:        "closed",
		StartedAt:     startedAt.Add(-2 * time.Hour),
	})
	fullPath := writeTestSession(t, tracesRoot, "full-session", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "full-session",
		Mode:          ModeFull,
		Status:        "closed",
		StartedAt:     startedAt.Add(-time.Hour),
	})
	if err := os.WriteFile(filepath.Join(basicPath, eventsFilename), nil, 0o600); err != nil {
		t.Fatalf("create basic events: %v", err)
	}
	if err := os.Truncate(filepath.Join(basicPath, eventsFilename), 40*1024*1024); err != nil {
		t.Fatalf("expand basic events: %v", err)
	}
	if err := os.WriteFile(filepath.Join(fullPath, eventsFilename), nil, 0o600); err != nil {
		t.Fatalf("create full events: %v", err)
	}
	if err := os.Truncate(filepath.Join(fullPath, eventsFilename), 30*1024*1024); err != nil {
		t.Fatalf("expand full events: %v", err)
	}

	if err := CleanupClosedSessions(root, Settings{Mode: ModeFull, RetentionDays: 7, MaxDiskMB: 64}); err != nil {
		t.Fatalf("CleanupClosedSessions() error = %v", err)
	}
	if _, err := os.Stat(fullPath); !os.IsNotExist(err) {
		t.Fatalf("closed full session still exists: %v", err)
	}
	if _, err := os.Stat(basicPath); err != nil {
		t.Fatalf("closed basic session was removed for quota: %v", err)
	}
}

func TestCleanupAllClosedSessionsPreservesOpenSession(t *testing.T) {
	root := t.TempDir()
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(tracesRoot); err != nil {
		t.Fatalf("create traces root: %v", err)
	}
	closedPath := writeTestSession(t, tracesRoot, "closed-basic", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "closed-basic",
		Mode:          ModeBasic,
		Status:        "closed",
		StartedAt:     time.Now().UTC(),
	})
	openPath := writeTestSession(t, tracesRoot, "open-full", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "open-full",
		Mode:          ModeFull,
		Status:        "open",
		StartedAt:     time.Now().UTC(),
	})
	if err := os.WriteFile(filepath.Join(closedPath, eventsFilename), []byte("event\n"), 0o600); err != nil {
		t.Fatalf("write closed event: %v", err)
	}

	result, err := CleanupAllClosedSessions(root)
	if err != nil {
		t.Fatalf("CleanupAllClosedSessions() error = %v", err)
	}
	if result.RemovedSessions != 1 || result.FreedBytes <= 0 {
		t.Fatalf("unexpected cleanup result: %+v", result)
	}
	if _, err := os.Stat(closedPath); !os.IsNotExist(err) {
		t.Fatalf("closed session still exists: %v", err)
	}
	if _, err := os.Stat(openPath); err != nil {
		t.Fatalf("open session was removed: %v", err)
	}
}

func TestControllerReconfigureClosesPreviousSession(t *testing.T) {
	root := t.TempDir()
	controller, err := NewController(root, Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	first := controller.Status()
	if err := controller.Reconfigure(Settings{Mode: ModeFull, RetentionDays: 14, MaxDiskMB: 128}); err != nil {
		t.Fatalf("Reconfigure() error = %v", err)
	}
	second := controller.Status()
	if second.Mode != ModeFull || second.SessionID == first.SessionID {
		t.Fatalf("unexpected reconfigured status: first=%+v second=%+v", first, second)
	}
	manifest, err := readManifest(filepath.Join(first.SessionPath, manifestFilename))
	if err != nil {
		t.Fatalf("read first manifest: %v", err)
	}
	if manifest.Status != "closed" {
		t.Fatalf("previous session status = %q, want closed", manifest.Status)
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func writeTestSession(t *testing.T, root string, name string, manifest Manifest) string {
	t.Helper()
	path := filepath.Join(root, name)
	if err := ensurePrivateDir(path); err != nil {
		t.Fatalf("create test session: %v", err)
	}
	payload, err := json.Marshal(manifest)
	if err != nil {
		t.Fatalf("marshal test manifest: %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, manifestFilename), payload, 0o600); err != nil {
		t.Fatalf("write test manifest: %v", err)
	}
	return path
}

func assertPrivatePermissions(t *testing.T, path string, expected os.FileMode) {
	t.Helper()
	if runtime.GOOS == "windows" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if actual := info.Mode().Perm(); actual != expected {
		t.Fatalf("permissions for %s = %04o, want %04o", path, actual, expected)
	}
}
