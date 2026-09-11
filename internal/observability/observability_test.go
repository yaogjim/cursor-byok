package observability

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestWithCorrelationMergesAndNormalizesStableIdentifiers(t *testing.T) {
	parent := Correlation{
		TraceID:              " trace-1 ",
		SpanID:               " span-parent ",
		RootConversationID:   " root-1 ",
		ParentConversationID: " parent-1 ",
		ParentModelCallID:    " parent-model-1 ",
		ParentToolCallID:     " parent-tool-1 ",
		SubagentRunID:        " run-1 ",
		SubagentAttemptID:    " attempt-2 ",
		SubagentAttemptNo:    2,
		ChildConversationID:  " child-1 ",
		AgentID:              " agent-1 ",
		ProviderPass:         2,
	}
	ctx := WithCorrelation(context.Background(), parent)
	ctx = WithCorrelation(ctx, Correlation{
		SpanID:         " span-child ",
		ConversationID: " conversation-1 ",
		ModelCallID:    " model-1 ",
		HTTPAttempt:    3,
	})
	got := CorrelationFromContext(ctx)
	if got.TraceID != "trace-1" || got.SpanID != "span-child" {
		t.Fatalf("trace correlation = %+v", got)
	}
	if got.RootConversationID != "root-1" || got.ParentConversationID != "parent-1" || got.ParentModelCallID != "parent-model-1" || got.ParentToolCallID != "parent-tool-1" {
		t.Fatalf("parent correlation was not preserved: %+v", got)
	}
	if got.SubagentRunID != "run-1" || got.SubagentAttemptID != "attempt-2" || got.SubagentAttemptNo != 2 || got.ChildConversationID != "child-1" || got.AgentID != "agent-1" {
		t.Fatalf("subagent correlation was not preserved: %+v", got)
	}
	if got.ConversationID != "conversation-1" || got.ModelCallID != "model-1" || got.ProviderPass != 2 || got.HTTPAttempt != 3 {
		t.Fatalf("downstream correlation was not merged: %+v", got)
	}
}

func TestApplyCorrelationIncludesSubagentAndAttemptFields(t *testing.T) {
	event := Event{}
	applyCorrelation(&event, Correlation{
		RootConversationID:   "root-1",
		ParentConversationID: "parent-1",
		ParentModelCallID:    "parent-model-1",
		ParentToolCallID:     "parent-tool-1",
		SubagentRunID:        "run-1",
		SubagentAttemptID:    "attempt-4",
		SubagentAttemptNo:    4,
		ChildConversationID:  "child-1",
		AgentID:              "agent-1",
		ProviderPass:         2,
		HTTPAttempt:          4,
	})
	if event.RootConversationID != "root-1" || event.ParentConversationID != "parent-1" || event.ParentModelCallID != "parent-model-1" || event.ParentToolCallID != "parent-tool-1" {
		t.Fatalf("event parent correlation = %+v", event)
	}
	if event.SubagentRunID != "run-1" || event.SubagentAttemptID != "attempt-4" || event.SubagentAttemptNo != 4 || event.ChildConversationID != "child-1" || event.AgentID != "agent-1" || event.ProviderPass != 2 || event.HTTPAttempt != 4 {
		t.Fatalf("event subagent correlation = %+v", event)
	}
}

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

	budget := recorder.writer.budget
	if budget == nil {
		t.Fatal("recorder has no shared budget")
	}
	diskUsage, err := normalUsageBytes(root)
	if err != nil {
		t.Fatalf("normal usage: %v", err)
	}
	budget.mu.Lock()
	cachedUsage := budget.normalUsage
	budget.mu.Unlock()
	if cachedUsage != diskUsage {
		t.Fatalf("manifest bytes were not accounted in normal usage: cached=%d disk=%d", cachedUsage, diskUsage)
	}
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

func TestCleanupAllClosedSessionsPreservesRuntimeLogsAndSkipsUnsafeEntries(t *testing.T) {
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
	if err := os.WriteFile(filepath.Join(closedPath, eventsFilename), []byte("event\n"), 0o600); err != nil {
		t.Fatalf("write closed event: %v", err)
	}
	openPath := writeTestSession(t, tracesRoot, "open-full", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "open-full",
		Mode:          ModeFull,
		Status:        "open",
		StartedAt:     time.Now().UTC(),
	})
	failedPath := writeTestSession(t, tracesRoot, "open-failed", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "open-failed",
		Mode:          ModeBasic,
		Status:        "open_failed",
		StartedAt:     time.Now().UTC(),
	})
	badPath := filepath.Join(tracesRoot, "bad-manifest")
	if err := ensurePrivateDir(badPath); err != nil {
		t.Fatalf("create bad manifest session: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badPath, manifestFilename), []byte("not-json"), 0o600); err != nil {
		t.Fatalf("write bad manifest: %v", err)
	}

	outsideClosed := writeTestSession(t, t.TempDir(), "outside-closed", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "outside-closed",
		Mode:          ModeBasic,
		Status:        "closed",
		StartedAt:     time.Now().UTC(),
	})
	symlinkPath := filepath.Join(tracesRoot, "closed-link")
	if err := os.Symlink(outsideClosed, symlinkPath); err != nil {
		t.Fatalf("create closed symlink: %v", err)
	}

	appLogPath := filepath.Join(root, "app", "app.log")
	if err := os.MkdirAll(filepath.Dir(appLogPath), 0o755); err != nil {
		t.Fatalf("create app log dir: %v", err)
	}
	if err := os.WriteFile(appLogPath, []byte("runtime log\n"), 0o644); err != nil {
		t.Fatalf("write app log: %v", err)
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
	for _, path := range []string{openPath, failedPath, badPath, symlinkPath, outsideClosed, appLogPath} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("preserved path removed %s: %v", path, err)
		}
	}
}

func TestCleanupAllClosedSessionsNoClosedReturnsZero(t *testing.T) {
	root := t.TempDir()
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(tracesRoot); err != nil {
		t.Fatalf("create traces root: %v", err)
	}
	openPath := writeTestSession(t, tracesRoot, "open-only", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "open-only",
		Mode:          ModeBasic,
		Status:        "open",
		StartedAt:     time.Now().UTC(),
	})

	result, err := CleanupAllClosedSessions(root)
	if err != nil {
		t.Fatalf("CleanupAllClosedSessions() error = %v", err)
	}
	if result.RemovedSessions != 0 || result.FreedBytes != 0 {
		t.Fatalf("unexpected empty cleanup result: %+v", result)
	}
	if _, err := os.Stat(openPath); err != nil {
		t.Fatalf("open session was removed: %v", err)
	}
}

func TestCleanupAllClosedSessionsRejectsInvalidRoot(t *testing.T) {
	tmp := t.TempDir()
	t.Chdir(tmp)
	for _, root := range []string{"", ".", "   ", "./."} {
		if _, err := CleanupAllClosedSessions(root); err == nil {
			t.Fatalf("CleanupAllClosedSessions(%q) succeeded, want error", root)
		}
	}
	if _, err := os.Stat(filepath.Join(tmp, tracesDirname)); !os.IsNotExist(err) {
		t.Fatalf("invalid root created ./traces: %v", err)
	}
}

func TestCleanupAllClosedSessionsConcurrentCallsDoNotDoubleCount(t *testing.T) {
	root := t.TempDir()
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(tracesRoot); err != nil {
		t.Fatalf("create traces root: %v", err)
	}
	closedA := writeTestSession(t, tracesRoot, "closed-a", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "closed-a",
		Mode:          ModeBasic,
		Status:        "closed",
		StartedAt:     time.Now().UTC(),
	})
	closedB := writeTestSession(t, tracesRoot, "closed-b", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "closed-b",
		Mode:          ModeBasic,
		Status:        "closed",
		StartedAt:     time.Now().UTC(),
	})
	if err := os.WriteFile(filepath.Join(closedA, eventsFilename), []byte("a\n"), 0o600); err != nil {
		t.Fatalf("write closed-a event: %v", err)
	}
	if err := os.WriteFile(filepath.Join(closedB, eventsFilename), []byte("b\n"), 0o600); err != nil {
		t.Fatalf("write closed-b event: %v", err)
	}

	const workers = 8
	results := make([]CleanupResult, workers)
	errs := make([]error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = CleanupAllClosedSessions(root)
		}(i)
	}
	wg.Wait()

	removed := 0
	for i, err := range errs {
		if err != nil {
			t.Fatalf("concurrent CleanupAllClosedSessions()[%d] error = %v", i, err)
		}
		removed += results[i].RemovedSessions
	}
	if removed != 2 {
		t.Fatalf("concurrent removed sessions = %d, want 2 (results=%+v)", removed, results)
	}
	for _, path := range []string{closedA, closedB} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("closed session still exists %s: %v", path, err)
		}
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
	deadline := time.Now().Add(2 * time.Second)
	var manifest Manifest
	for {
		manifest, err = readManifest(filepath.Join(first.SessionPath, manifestFilename))
		if err == nil && manifest.Status == "closed" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("previous session status = %q, want closed (err=%v)", manifest.Status, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestControllerReconfigureIgnoresRuntimeFingerprint(t *testing.T) {
	root := t.TempDir()
	controller, err := NewController(root, Settings{
		Mode:               ModeBasic,
		RetentionDays:      7,
		MaxDiskMB:          64,
		RuntimeFingerprint: "routing-a",
		Metadata:           SessionMetadata{ConfigFingerprint: "storage-a"},
	})
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	first := controller.Status()
	if err := controller.Reconfigure(Settings{
		Mode:               ModeBasic,
		RetentionDays:      7,
		MaxDiskMB:          64,
		RuntimeFingerprint: "routing-b",
		Metadata:           SessionMetadata{ConfigFingerprint: "storage-a"},
	}); err != nil {
		t.Fatalf("Reconfigure() error = %v", err)
	}
	second := controller.Status()
	if second.SessionID != first.SessionID {
		t.Fatalf("runtime fingerprint change rotated session: first=%+v second=%+v", first, second)
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestControllerReconfigureDoesNotBlockOnSlowRecorderClose(t *testing.T) {
	entered := make(chan struct{})
	block := make(chan struct{})
	controller, err := NewControllerWithHumanSink(
		t.TempDir(),
		Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64},
		func(Event) {
			select {
			case <-entered:
			default:
				close(entered)
			}
			<-block
		},
	)
	if err != nil {
		t.Fatalf("NewControllerWithHumanSink() error = %v", err)
	}
	if !controller.RecordEvent(context.Background(), Event{Layer: "backend", Event: "request_finished"}) {
		t.Fatal("event was not accepted")
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("human sink was not entered")
	}
	started := time.Now()
	if err := controller.Reconfigure(Settings{Mode: ModeFull, RetentionDays: 7, MaxDiskMB: 64}); err != nil {
		t.Fatalf("Reconfigure() error = %v", err)
	}
	if elapsed := time.Since(started); elapsed > 300*time.Millisecond {
		t.Fatalf("Reconfigure blocked for %s", elapsed)
	}
	if !controller.RecordEvent(context.Background(), Event{Layer: "backend", Event: "request_finished"}) {
		t.Fatal("new recorder did not accept event")
	}
	status := controller.Status()
	if status.Mode != ModeFull {
		t.Fatalf("status mode = %q, want full", status.Mode)
	}
	close(block)
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

// TestManifestAdmissionDeniedPreservesOldManifest guards the regression where a
// full normal partition was ignored for the manifest and the bytes were written
// over the quota anyway. A denied admission must leave the previous manifest on
// disk untouched and keep the partition within its limit.
func TestManifestAdmissionDeniedPreservesOldManifest(t *testing.T) {
	root := t.TempDir()
	budget := testBudget(root, 1<<20, 1<<20)
	writer, err := openSession(root, Settings{Mode: ModeBasic, RetentionDays: 7}, budget)
	if err != nil {
		t.Fatalf("openSession: %v", err)
	}
	manifestPath := filepath.Join(writer.dir, manifestFilename)
	before, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	usage, err := normalUsageBytes(root)
	if err != nil {
		t.Fatalf("normal usage: %v", err)
	}
	// No headroom: the open session is protected, so the rewrite cannot be
	// admitted without exceeding the normal limit.
	budget.mu.Lock()
	budget.normalLimit = usage
	budget.normalUsage = usage
	budget.usageKnown = true
	budget.mu.Unlock()

	writer.markDegraded(3, "event_quota_exceeded")

	after, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest after denied rewrite: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("denied manifest rewrite changed on-disk bytes")
	}
	if writer.manifestError() == "" {
		t.Fatal("denied manifest rewrite was not recorded in memory")
	}
	finalUsage, err := normalUsageBytes(root)
	if err != nil {
		t.Fatalf("final normal usage: %v", err)
	}
	if finalUsage > budget.normalLimit {
		t.Fatalf("normal partition exceeded limit after denied manifest: usage=%d limit=%d", finalUsage, budget.normalLimit)
	}
	// Close reports the denied terminal manifest instead of swallowing it; the
	// in-memory status already records the same failure.
	if err := writer.close("closed"); !errors.Is(err, errSessionQuotaExceeded) {
		t.Fatalf("close error = %v, want %v", err, errSessionQuotaExceeded)
	}
}

// TestManifestWriteFailureVisibleInStatus proves a failed manifest rewrite is
// surfaced through the existing in-memory status instead of being silently
// dropped, without adding a new schema field.
func TestManifestWriteFailureVisibleInStatus(t *testing.T) {
	root := t.TempDir()
	budget := testBudget(root, 1<<20, 1<<20)
	recorder, err := newRecorderWithBudget(root, Settings{Mode: ModeBasic, RetentionDays: 7}, nil, budget)
	if err != nil {
		t.Fatalf("newRecorderWithBudget: %v", err)
	}
	usage, err := normalUsageBytes(root)
	if err != nil {
		t.Fatalf("normal usage: %v", err)
	}
	budget.mu.Lock()
	budget.normalLimit = usage
	budget.normalUsage = usage
	budget.usageKnown = true
	budget.mu.Unlock()

	recorder.setTraceDegraded("event_quota_exceeded")

	status := recorder.Status()
	if !strings.Contains(status.LastError, "manifest") {
		t.Fatalf("manifest write failure not visible in status: %+v", status)
	}
	_ = recorder.Close()
}

// TestManifestRewriteRemovesTempAfterFailedRename pins the failure contract of
// the temp-file manifest write: a deterministic rename failure must not leave
// the just-written temp bytes behind, and it must invalidate the cached normal
// usage so the next admission re-scans the directory instead of admitting
// against a counter that no longer matches disk.
func TestManifestRewriteRemovesTempAfterFailedRename(t *testing.T) {
	root := t.TempDir()
	budget := testBudget(root, 1<<20, 1<<20)
	writer, err := openSession(root, Settings{Mode: ModeBasic, RetentionDays: 7}, budget)
	if err != nil {
		t.Fatalf("openSession: %v", err)
	}
	manifestPath := filepath.Join(writer.dir, manifestFilename)
	tempPath := manifestPath + ".tmp"
	// A directory at the manifest path makes the final rename fail
	// deterministically while the temp write itself still succeeds.
	if err := os.Remove(manifestPath); err != nil {
		t.Fatalf("remove manifest: %v", err)
	}
	if err := os.Mkdir(manifestPath, 0o700); err != nil {
		t.Fatalf("block manifest path: %v", err)
	}

	writer.markDegraded(1, "manifest_rename_failed")

	if writer.manifestError() == "" {
		t.Fatal("failed manifest rewrite was not recorded in memory")
	}
	if _, statErr := os.Stat(tempPath); !os.IsNotExist(statErr) {
		t.Fatalf("failed manifest rewrite left temp bytes behind: %v", statErr)
	}
	budget.mu.Lock()
	known := budget.usageKnown
	budget.mu.Unlock()
	if known {
		t.Fatal("failed manifest rewrite kept a cached normal usage")
	}
	// The next admission must re-scan: the counter has to match the bytes
	// actually on disk before any further manifest write is admitted.
	if !budget.admitNormalLocked(0, 0, "") {
		t.Fatal("admission re-scan failed after a manifest write failure")
	}
	actual, err := normalUsageBytes(root)
	if err != nil {
		t.Fatalf("normal usage: %v", err)
	}
	budget.mu.Lock()
	counted := budget.normalUsage
	budget.mu.Unlock()
	if counted != actual {
		t.Fatalf("admission did not re-scan after a failed manifest write: counted=%d actual=%d", counted, actual)
	}
}

// TestManifestRewriteCountsLeftoverTempAtNextAdmission covers what the temp
// removal cannot fix: when the temp path is a non-empty directory the write and
// its cleanup both fail, so leftover bytes really do stay on disk. The cached
// usage is still invalidated, so the next admission re-scans and counts them
// rather than admitting against a stale counter.
func TestManifestRewriteCountsLeftoverTempAtNextAdmission(t *testing.T) {
	root := t.TempDir()
	budget := testBudget(root, 1<<20, 1<<20)
	writer, err := openSession(root, Settings{Mode: ModeBasic, RetentionDays: 7}, budget)
	if err != nil {
		t.Fatalf("openSession: %v", err)
	}
	tempPath := filepath.Join(writer.dir, manifestFilename) + ".tmp"
	if err := os.Mkdir(tempPath, 0o700); err != nil {
		t.Fatalf("block temp manifest path: %v", err)
	}
	leftover := []byte(strings.Repeat("p", 4096))
	if err := os.WriteFile(filepath.Join(tempPath, "partial.bin"), leftover, 0o600); err != nil {
		t.Fatalf("seed leftover temp bytes: %v", err)
	}
	// The cached counter does not know about these bytes yet.
	budget.mu.Lock()
	before := budget.normalUsage
	budget.mu.Unlock()

	writer.markDegraded(1, "manifest_write_failed")

	if writer.manifestError() == "" {
		t.Fatal("failed manifest rewrite was not recorded in memory")
	}
	if _, statErr := os.Stat(tempPath); statErr != nil {
		t.Fatalf("fixture did not exercise an unremovable temp path: %v", statErr)
	}
	if !budget.admitNormalLocked(0, 0, "") {
		t.Fatal("admission re-scan failed after a failed manifest cleanup")
	}
	actual, err := normalUsageBytes(root)
	if err != nil {
		t.Fatalf("normal usage: %v", err)
	}
	budget.mu.Lock()
	counted := budget.normalUsage
	budget.mu.Unlock()
	if counted != actual {
		t.Fatalf("admission did not re-scan leftovers from a failed cleanup: counted=%d actual=%d", counted, actual)
	}
	if counted < before+int64(len(leftover)) {
		t.Fatalf("re-scan lost the leftover temp bytes: before=%d counted=%d leftover=%d", before, counted, len(leftover))
	}
	_ = writer.close("closed")
}

// TestClosedWriterRewriteDoesNotReclaimOwnSession guards the double-close
// regression: a session already marked closed on disk is a reclaim candidate,
// so an unaffordable manifest rewrite (Close called more than once) would free
// room by deleting the very trace it belongs to and then fail on the missing
// directory. Admission must exclude the caller's own session directory.
func TestClosedWriterRewriteDoesNotReclaimOwnSession(t *testing.T) {
	root := t.TempDir()
	budget := testBudget(root, eventReserveBytes+1<<20, 1<<20)
	writer, err := openSession(root, Settings{Mode: ModeBasic, RetentionDays: 7}, budget)
	if err != nil {
		t.Fatalf("openSession: %v", err)
	}
	if err := writer.appendEvent(Event{Layer: "backend", Event: "info_event"}); err != nil {
		t.Fatalf("appendEvent: %v", err)
	}
	if err := writer.close("closed"); err != nil {
		t.Fatalf("first close: %v", err)
	}
	// The session is closed on disk now, so only its own directory could
	// satisfy the rewrite: leave no headroom at all.
	usage, err := normalUsageBytes(root)
	if err != nil {
		t.Fatalf("normal usage: %v", err)
	}
	budget.mu.Lock()
	budget.normalLimit = usage
	budget.normalUsage = usage
	budget.usageKnown = true
	budget.mu.Unlock()

	// The rewrite is denied and reported, but it must never free room by
	// deleting the writer's own session.
	if err := writer.close("closed"); !errors.Is(err, errSessionQuotaExceeded) {
		t.Fatalf("second close error = %v, want %v", err, errSessionQuotaExceeded)
	}
	manifest, err := readManifest(filepath.Join(writer.dir, manifestFilename))
	if err != nil {
		t.Fatalf("own session manifest was lost by its own rewrite: %v", err)
	}
	if manifest.Status != "closed" {
		t.Fatalf("own session manifest status = %q, want closed", manifest.Status)
	}
	if _, statErr := os.Stat(filepath.Join(writer.dir, eventsFilename)); statErr != nil {
		t.Fatalf("own session events were removed by its own rewrite: %v", statErr)
	}
	if writer.manifestError() != "manifest_quota_exceeded" {
		t.Fatalf("denied own rewrite not recorded in memory: %q", writer.manifestError())
	}
}

// TestReservedHeadroomKeepsTerminalManifestWritable proves the approved budget
// split end to end with real writes: ordinary event and app-log writes keep the
// eventReserveBytes metadata headroom, so after the data room is exhausted the
// terminal manifest still lands, the session reaches "closed", and the next
// session can open and reclaim it instead of being blocked behind a trace stuck
// in "open".
func TestReservedHeadroomKeepsTerminalManifestWritable(t *testing.T) {
	root := t.TempDir()
	const dataRoom = int64(8192)
	budget := testBudget(root, eventReserveBytes+dataRoom, 1<<20)
	writer, err := openSession(root, Settings{Mode: ModeBasic, RetentionDays: 7}, budget)
	if err != nil {
		t.Fatalf("openSession: %v", err)
	}
	filler := strings.Repeat("e", 1024)
	refused := false
	// With the reserve in place the data room rejects after a handful of events;
	// the generous bound is only there so a missing reserve is proven by the
	// terminal manifest being denied rather than by this loop ending.
	for index := 0; index < 4096; index++ {
		appendErr := writer.appendEvent(Event{Layer: "backend", Event: "info_fill", Fields: map[string]any{"detail": filler}})
		if appendErr == nil {
			continue
		}
		if !errors.Is(appendErr, errSessionQuotaExceeded) {
			t.Fatalf("appendEvent: %v", appendErr)
		}
		refused = true
		break
	}
	if !refused {
		t.Fatal("ordinary event writes never exhausted the reserved data room")
	}
	// The invariant the reserve exists for: after any amount of ordinary writing
	// the metadata headroom is still free, so the terminal manifest can land.
	budget.mu.Lock()
	remaining := budget.normalLimit - budget.normalUsage
	budget.mu.Unlock()
	if remaining < eventReserveBytes {
		t.Fatalf("ordinary event writes consumed the metadata headroom: remaining=%d reserve=%d", remaining, eventReserveBytes)
	}

	// The app log path shares the same admission, so it is refused too and can
	// never eat into the metadata headroom.
	appSink, err := openAppLogSink(root, budget)
	if err != nil {
		t.Fatalf("openAppLogSink: %v", err)
	}
	appPayload := []byte(strings.Repeat("a", 4096))
	if written, writeErr := appSink.write(appPayload); writeErr != nil || written != len(appPayload) {
		t.Fatalf("app log write = (%d, %v), want a silent quota drop", written, writeErr)
	}
	if appSink.status().lastErr != "app_log_quota_exceeded" {
		t.Fatalf("app log did not report the quota drop: %+v", appSink.status())
	}
	if size := appDirSize(t, root); size != 0 {
		t.Fatalf("refused app write retained %d bytes", size)
	}
	if err := appSink.close(); err != nil {
		t.Fatalf("app log close: %v", err)
	}

	// The terminal manifest must still fit inside the preserved headroom.
	if err := writer.close("closed"); err != nil {
		t.Fatalf("close after quota refusal: %v", err)
	}
	manifest, err := readManifest(filepath.Join(writer.dir, manifestFilename))
	if err != nil {
		t.Fatalf("read closed manifest: %v", err)
	}
	if manifest.Status != "closed" {
		t.Fatalf("manifest status = %q, want closed", manifest.Status)
	}
	usage, err := normalUsageBytes(root)
	if err != nil {
		t.Fatalf("normal usage: %v", err)
	}
	budget.mu.Lock()
	limit := budget.normalLimit
	budget.mu.Unlock()
	if usage > limit {
		t.Fatalf("normal partition exceeded its limit: usage=%d limit=%d", usage, limit)
	}

	// Leave no headroom beyond the closed trace itself: the next session can only
	// open by reclaiming it, which requires the "closed" status written above.
	closedDir := writer.dir
	budget.mu.Lock()
	budget.normalLimit = usage
	budget.normalUsage = usage
	budget.usageKnown = true
	budget.mu.Unlock()
	next, err := openSession(root, Settings{Mode: ModeBasic, RetentionDays: 7}, budget)
	if err != nil {
		t.Fatalf("next session was blocked by the previous session: %v", err)
	}
	if _, statErr := os.Stat(closedDir); !os.IsNotExist(statErr) {
		t.Fatalf("next session did not reclaim the closed trace: stat err = %v", statErr)
	}
	if err := next.close("closed"); err != nil {
		t.Fatalf("next session close: %v", err)
	}
}
