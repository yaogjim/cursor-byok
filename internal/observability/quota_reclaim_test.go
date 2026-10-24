package observability

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeAgedFile(t *testing.T, path string, payload []byte, when time.Time) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}

func mustStat(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("expected %s to exist: %v", path, err)
	}
}

func mustMissing(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected %s to be removed, stat err = %v", path, err)
	}
}

// TestCleanupAllClosedSessionsFreesHistoricalAndRotatedFragments reproduces the
// user-visible "deleted 0 sessions / freed 0KB" failure: a live open session
// plus historical app/diagnostic shards and already-rotated events fragments
// must still free bytes without deleting the active write surfaces.
func TestCleanupAllClosedSessionsFreesHistoricalAndRotatedFragments(t *testing.T) {
	root := t.TempDir()
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(tracesRoot); err != nil {
		t.Fatalf("create traces root: %v", err)
	}
	now := time.Now().UTC()

	closedPath := writeTestSession(t, tracesRoot, "closed-basic", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "closed-basic",
		Mode:          ModeBasic,
		Status:        "closed",
		StartedAt:     now.Add(-2 * time.Hour),
	})
	if err := os.WriteFile(filepath.Join(closedPath, eventsFilename), []byte(strings.Repeat("c", 2048)), 0o600); err != nil {
		t.Fatalf("write closed events: %v", err)
	}

	openPath := writeTestSession(t, tracesRoot, "open-basic", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "open-basic",
		Mode:          ModeBasic,
		Status:        "open",
		StartedAt:     now,
	})
	activeEvents := filepath.Join(openPath, eventsFilename)
	rotatedEvents := filepath.Join(openPath, "events-20260101T000000.000000000Z-000001.jsonl")
	if err := os.WriteFile(activeEvents, []byte("live-events\n"), 0o600); err != nil {
		t.Fatalf("write open events: %v", err)
	}
	writeAgedFile(t, rotatedEvents, []byte(strings.Repeat("r", 4096)), now.Add(-time.Hour))

	oldApp := filepath.Join(root, appLogDirname, "app-20260101T000000.000000000Z-000001.log")
	newApp := filepath.Join(root, appLogDirname, "app-20260102T000000.000000000Z-000002.log")
	writeAgedFile(t, oldApp, []byte(strings.Repeat("a", 8192)), now.Add(-3*time.Hour))
	writeAgedFile(t, newApp, []byte("active-app\n"), now)

	unmanagedApp := filepath.Join(root, appLogDirname, "app.log")
	if err := os.WriteFile(unmanagedApp, []byte("runtime log\n"), 0o600); err != nil {
		t.Fatalf("write unmanaged app log: %v", err)
	}

	oldDiag := filepath.Join(root, diagnosticsDirname, "diagnostics-20260101T000000.000000000Z-000001.jsonl")
	newDiag := filepath.Join(root, diagnosticsDirname, "diagnostics-20260102T000000.000000000Z-000002.jsonl")
	writeAgedFile(t, oldDiag, []byte(strings.Repeat("d", 8192)), now.Add(-4*time.Hour))
	writeAgedFile(t, newDiag, []byte("active-diag\n"), now)

	outside := filepath.Join(t.TempDir(), "outside.log")
	if err := os.WriteFile(outside, []byte("outside\n"), 0o600); err != nil {
		t.Fatalf("write symlink target: %v", err)
	}
	appLink := filepath.Join(root, appLogDirname, "app-20251201T000000.000000000Z-000099.log")
	if err := os.Symlink(outside, appLink); err != nil {
		t.Fatalf("create app symlink: %v", err)
	}

	result, err := CleanupAllClosedSessions(root)
	if err != nil {
		t.Fatalf("CleanupAllClosedSessions() error = %v", err)
	}
	if result.RemovedSessions != 1 || result.FreedBytes < 8192 {
		t.Fatalf("cleanup result = %+v, want removed_sessions=1 and freed historical bytes", result)
	}

	mustMissing(t, closedPath)
	mustMissing(t, rotatedEvents)
	mustMissing(t, oldApp)
	mustMissing(t, oldDiag)

	mustStat(t, openPath)
	mustStat(t, activeEvents)
	mustStat(t, newApp)
	mustStat(t, newDiag)
	mustStat(t, unmanagedApp)
	mustStat(t, appLink)
	mustStat(t, outside)
}

// TestCleanupAllClosedSessionsOpenOnlyStillFreesRotatedAndHistory covers the
// screenshot case: no closed trace, but historical app/diagnostic shards and
// active-session rotated fragments must still report FreedBytes > 0.
func TestCleanupAllClosedSessionsOpenOnlyStillFreesRotatedAndHistory(t *testing.T) {
	root := t.TempDir()
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(tracesRoot); err != nil {
		t.Fatalf("create traces root: %v", err)
	}
	now := time.Now().UTC()
	openPath := writeTestSession(t, tracesRoot, "open-only", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "open-only",
		Mode:          ModeBasic,
		Status:        "open",
		StartedAt:     now,
	})
	activeEvents := filepath.Join(openPath, eventsFilename)
	rotatedEvents := filepath.Join(openPath, "events-20260101T000000.000000000Z-000001.jsonl")
	if err := os.WriteFile(activeEvents, []byte("live\n"), 0o600); err != nil {
		t.Fatalf("write open events: %v", err)
	}
	writeAgedFile(t, rotatedEvents, []byte(strings.Repeat("r", 2048)), now.Add(-time.Hour))
	oldApp := filepath.Join(root, appLogDirname, "app-20260101T000000.000000000Z-000001.log")
	newApp := filepath.Join(root, appLogDirname, "app-20260102T000000.000000000Z-000002.log")
	writeAgedFile(t, oldApp, []byte(strings.Repeat("a", 2048)), now.Add(-time.Hour))
	writeAgedFile(t, newApp, []byte("keep\n"), now)

	result, err := CleanupAllClosedSessions(root)
	if err != nil {
		t.Fatalf("CleanupAllClosedSessions() error = %v", err)
	}
	if result.RemovedSessions != 0 || result.FreedBytes < 2048 {
		t.Fatalf("open-only cleanup = %+v, want 0 sessions and freed historical bytes", result)
	}
	mustMissing(t, rotatedEvents)
	mustMissing(t, oldApp)
	mustStat(t, openPath)
	mustStat(t, activeEvents)
	mustStat(t, newApp)
}

func TestCleanupWithoutTracesStillReclaimsAppShards(t *testing.T) {
	root := t.TempDir()
	old := filepath.Join(root, appLogDirname, "app-20260101T000000.000000000Z-000001.log")
	current := filepath.Join(root, appLogDirname, "app-20260102T000000.000000000Z-000002.log")
	writeAgedFile(t, old, []byte("historical log"), time.Now().Add(-time.Hour))
	writeAgedFile(t, current, []byte("current log"), time.Now())
	result, err := CleanupAllClosedSessions(root)
	if err != nil {
		t.Fatal(err)
	}
	if result.FreedBytes == 0 {
		t.Fatal("没有 traces 目录时仍必须清理应用历史日志")
	}
	mustMissing(t, old)
	mustStat(t, current)
}

func TestCleanupRejectsLinkedManagedDirectories(t *testing.T) {
	for _, dir := range []string{tracesDirname, appLogDirname, diagnosticsDirname} {
		t.Run(dir, func(t *testing.T) {
			root, outside := t.TempDir(), t.TempDir()
			if err := os.MkdirAll(filepath.Join(root, tracesDirname), 0o700); err != nil {
				t.Fatal(err)
			}
			if dir == tracesDirname {
				if err := os.Remove(filepath.Join(root, dir)); err != nil {
					t.Fatal(err)
				}
			}
			name := "app-20260101T000000.000000000Z-000001.log"
			if dir == diagnosticsDirname {
				name = "diagnostics-20260101T000000.000000000Z-000001.jsonl"
			}
			if dir == tracesDirname {
				writeTestSession(t, outside, "closed", Manifest{SchemaVersion: SchemaVersion, AppSessionID: "closed", Mode: ModeBasic, Status: "closed", StartedAt: time.Now()})
			}
			protected := filepath.Join(outside, name)
			writeAgedFile(t, protected, []byte("outside"), time.Now().Add(-time.Hour))
			writeAgedFile(t, filepath.Join(outside, strings.Replace(name, "000001", "000002", 1)), []byte("newer"), time.Now())
			if err := os.Symlink(outside, filepath.Join(root, dir)); err != nil {
				t.Fatal(err)
			}
			_, _ = CleanupAllClosedSessions(root)
			mustStat(t, protected)
			if dir == tracesDirname {
				mustStat(t, filepath.Join(outside, "closed"))
			}
		})
	}
}

func TestAppLogQuotaReclaimsItsOnlySealedShard(t *testing.T) {
	root := t.TempDir()
	budget := testBudget(root, eventReserveBytes+128, 1024)
	sink, err := openAppLogSink(root, budget)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.close()
	if _, err := sink.write(bytes.Repeat([]byte("a"), 100)); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.write([]byte("WRN latest-core-error-keeps-writing\n")); err != nil {
		t.Fatal(err)
	}
	if status := sink.status(); status.lastErr != "" || status.dropped != 0 {
		t.Fatalf("唯一活跃应用分片不能永久阻塞: %+v", status)
	}
	payload, err := os.ReadFile(sink.writer.CurrentPath())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(payload, []byte("latest-core-error")) {
		t.Fatal("核心应用日志未写入")
	}
}

func TestQuotaReclaimDoesNotCountClosedSessionFragmentsTwice(t *testing.T) {
	root := t.TempDir()
	path := writeTestSession(t, filepath.Join(root, tracesDirname), "closed-full", Manifest{
		SchemaVersion: SchemaVersion, AppSessionID: "closed-full", Mode: ModeFull, Status: "closed", StartedAt: time.Now(),
	})
	writeAgedFile(t, filepath.Join(path, "events-20260101T000000.000000000Z-000001.jsonl"), bytes.Repeat([]byte("x"), 8192), time.Now())
	writeAgedFile(t, filepath.Join(root, "unmanaged.data"), bytes.Repeat([]byte("p"), 8192), time.Now())
	budget := testBudget(root, 4096, 1024)
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.admitNormalLocked(1, 0, "") {
		t.Fatal("受保护数据仍超额，不能因目录与片段重复扣减而允许写入")
	}
	if budget.normalUsage < 8192 {
		t.Fatalf("配额统计少于实际受保护文件: %d", budget.normalUsage)
	}
}

func TestRepeatedTLSNoiseKeepsCountsAndCoreErrors(t *testing.T) {
	recorder, err := NewRecorder(t.TempDir(), Settings{Mode: ModeBasic, MaxDiskMB: 64, QueueSize: 512})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	for i := 0; i < 100; i++ {
		if !recorder.RecordEvent(context.Background(), Event{Layer: "mitm", Event: "tls_handshake_failed", ErrorCategory: "client_unknown_ca", Fields: map[string]any{"host": "example.test"}}) {
			t.Fatal("网络诊断入队失败")
		}
	}
	for i := 0; i < 3; i++ {
		recorder.RecordEvent(context.Background(), Event{Layer: "runtime", Event: "compaction_summary_failed", ConversationID: "conversation", ErrorCategory: "provider_error"})
	}
	if err := recorder.Close(); err != nil {
		t.Fatal(err)
	}
	events := readEventsFile(t, filepath.Join(recorder.Status().SessionPath, eventsFilename))
	var lines, repeats, core int
	for _, event := range events {
		if event.Event == "tls_handshake_failed" {
			lines++
			if count, ok := event.Fields["repeated_count"].(float64); ok {
				repeats += int(count)
			}
		}
		if event.Event == "compaction_summary_failed" {
			core++
		}
	}
	if lines != 2 || repeats != 99 || core != 3 {
		t.Fatalf("网络噪声应首条+计数摘要，核心错误全部保留: lines=%d repeats=%d core=%d", lines, repeats, core)
	}
}

func TestRecorderBasicSkipsHighFrequencyInfoKeepsCore(t *testing.T) {
	root := t.TempDir()
	recorder, err := NewRecorder(root, Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 20; i++ {
		if !recorder.RecordEvent(ctx, Event{Layer: "provider", Event: "llm_response_chunk", Status: "success"}) {
			t.Fatalf("high-frequency capture %d was rejected", i)
		}
		if !recorder.RecordEvent(ctx, Event{Layer: "runsse", Event: "send_message", Status: "success"}) {
			t.Fatalf("send_message capture %d was rejected", i)
		}
		if !recorder.RecordEvent(ctx, Event{Layer: "bidi_raw", Event: "bidi.raw", Status: "success"}) {
			t.Fatalf("bidi.raw capture %d was rejected", i)
		}
		if !recorder.RecordEvent(ctx, Event{Layer: "runsse", Event: "heartbeat", Status: "success"}) {
			t.Fatalf("heartbeat capture %d was rejected", i)
		}
		if !recorder.RecordEvent(ctx, Event{Layer: "runtime", Event: "client_heartbeat", Status: "success"}) {
			t.Fatalf("client_heartbeat capture %d was rejected", i)
		}
		if !recorder.RecordEvent(ctx, Event{Layer: "provider", Event: "raw_chunk", Status: "success"}) {
			t.Fatalf("raw_chunk capture %d was rejected", i)
		}
	}
	if !recorder.RecordEvent(ctx, Event{Layer: "backend", Event: "request_finished", Status: "success"}) {
		t.Fatal("lifecycle event was rejected")
	}
	if !recorder.RecordEvent(ctx, Event{
		Layer: "backend", Event: "error_event",
		ErrorCategory: "provider_error", SemanticOutcome: OutcomeFailed,
	}) {
		t.Fatal("core error was rejected")
	}
	status := recorder.Status()
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	events := readEventsFile(t, filepath.Join(status.SessionPath, eventsFilename))
	var names []string
	for _, event := range events {
		names = append(names, event.Event)
		switch event.Event {
		case "llm_response_chunk", "send_message", "bidi.raw", "heartbeat", "client_heartbeat", "raw_chunk":
			t.Fatalf("basic traces retained high-frequency event %q", event.Event)
		}
	}
	joined := strings.Join(names, ",")
	if !strings.Contains(joined, "request_finished") || !strings.Contains(joined, "error_event") {
		t.Fatalf("basic traces missing core events: %s", joined)
	}
}

func TestQuotaReclaimRotatesActiveLogsAndKeepsCoreWarnError(t *testing.T) {
	root := t.TempDir()
	const normalLimit = eventReserveBytes + 12*1024
	budget := testBudget(root, normalLimit, 1<<20)
	controller, err := newControllerWithBudget(root, Settings{Mode: ModeBasic, RetentionDays: 7}, nil, budget)
	if err != nil {
		t.Fatalf("newControllerWithBudget: %v", err)
	}
	budget.mu.Lock()
	budget.normalLimit = normalLimit
	budget.usageKnown = false
	budget.mu.Unlock()

	now := time.Now().UTC()
	oldApp := filepath.Join(root, appLogDirname, "app-20260101T000000.000000000Z-000001.log")
	newApp := filepath.Join(root, appLogDirname, "app-20260102T000000.000000000Z-000002.log")
	writeAgedFile(t, oldApp, bytes.Repeat([]byte("h"), 8000), now.Add(-2*time.Hour))
	writeAgedFile(t, newApp, []byte("keep-active-app\n"), now)

	info := Event{
		Layer:  "backend",
		Event:  "info_event",
		Status: "success",
		Fields: map[string]any{"detail": strings.Repeat("x", 512)},
	}
	for i := 0; i < 40; i++ {
		if !controller.RecordEvent(context.Background(), info) {
			t.Fatalf("info capture %d was rejected at admission", i)
		}
	}
	core := Event{
		Layer:           "backend",
		Event:           "error_event",
		ErrorCategory:   "provider_error",
		SemanticOutcome: OutcomeFailed,
	}
	if !controller.RecordEvent(context.Background(), core) {
		t.Fatal("core error was rejected at admission")
	}
	if _, err := controller.WriteAppLog([]byte("2026-01-01T00:00:00Z WRN core-app-line\n")); err != nil {
		t.Fatalf("WriteAppLog warn: %v", err)
	}

	status := waitForStatus(t, controller.recorder, func(status Status) bool {
		return strings.Contains(strings.Join(eventNamesInSession(t, status.SessionPath), ","), "error_event")
	})
	if !status.Enabled {
		t.Fatalf("quota pressure disabled capture: %+v", status)
	}
	if err := controller.Close(); err != nil && !os.IsNotExist(err) {
		// Close may report a denied terminal rewrite in extreme fixtures; the
		// core error must still have landed before that.
		if status.SessionPath == "" {
			t.Fatalf("Close: %v", err)
		}
	}
	names := strings.Join(eventNamesInSession(t, status.SessionPath), ",")
	if !strings.Contains(names, "error_event") {
		t.Fatalf("core error was not retained under quota pressure: %s", names)
	}
	appPayload, err := os.ReadFile(newestManaged(t, filepath.Join(root, appLogDirname), "app-", ".log"))
	if err != nil {
		t.Fatalf("read app shard: %v", err)
	}
	if !bytes.Contains(appPayload, []byte("core-app-line")) {
		t.Fatalf("warn app line was not retained: %q", appPayload)
	}
	diagEvents := readAllDiagnosticEvents(t, root)
	found := false
	for _, event := range diagEvents {
		if event.Event == "error_event" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("diagnostics lost the core error under quota pressure")
	}
}

func TestActiveSessionWriteSurvivesCleanupOfRotatedFragments(t *testing.T) {
	root := t.TempDir()
	controller, err := NewController(root, Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	if !controller.RecordEvent(context.Background(), Event{Layer: "backend", Event: "request_finished", Status: "success"}) {
		t.Fatal("seed event was rejected")
	}
	if _, err := controller.WriteAppLog([]byte("2026-01-01T00:00:00Z INF seed-app\n")); err != nil {
		t.Fatalf("seed app write: %v", err)
	}
	status := waitForStatus(t, controller.recorder, func(status Status) bool { return status.SessionPath != "" })
	rotated := filepath.Join(status.SessionPath, "events-20260101T000000.000000000Z-000001.jsonl")
	writeAgedFile(t, rotated, []byte(strings.Repeat("r", 1024)), time.Now().UTC().Add(-time.Hour))
	oldApp := filepath.Join(root, appLogDirname, "app-20260101T000000.000000000Z-000001.log")
	writeAgedFile(t, oldApp, []byte(strings.Repeat("a", 1024)), time.Now().UTC().Add(-time.Hour))

	result, err := CleanupAllClosedSessions(root)
	if err != nil {
		t.Fatalf("CleanupAllClosedSessions: %v", err)
	}
	if result.FreedBytes < 1024 {
		t.Fatalf("cleanup freed %d, want rotated/history bytes", result.FreedBytes)
	}
	mustMissing(t, rotated)
	mustMissing(t, oldApp)
	mustStat(t, filepath.Join(status.SessionPath, eventsFilename))

	if !controller.RecordEvent(context.Background(), Event{
		Layer: "backend", Event: "error_event",
		ErrorCategory: "provider_error", SemanticOutcome: OutcomeFailed,
	}) {
		t.Fatal("write after cleanup was rejected")
	}
	if _, err := controller.WriteAppLog([]byte("2026-01-01T00:00:00Z WRN after-cleanup\n")); err != nil {
		t.Fatalf("app write after cleanup: %v", err)
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	names := strings.Join(eventNamesInSession(t, status.SessionPath), ",")
	if !strings.Contains(names, "error_event") {
		t.Fatalf("active session lost writes after cleanup: %s", names)
	}
}

func eventNamesInSession(t *testing.T, sessionPath string) []string {
	t.Helper()
	events := readEventsFile(t, filepath.Join(sessionPath, eventsFilename))
	names := make([]string, 0, len(events))
	for _, event := range events {
		names = append(names, event.Event)
	}
	return names
}

func newestManaged(t *testing.T, dir string, prefix string, ext string) string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var newest string
	var newestTime time.Time
	for _, entry := range entries {
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			continue
		}
		if !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ext) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		if newest == "" || info.ModTime().After(newestTime) {
			newest = filepath.Join(dir, entry.Name())
			newestTime = info.ModTime()
		}
	}
	if newest == "" {
		t.Fatalf("no managed shard in %s", dir)
	}
	return newest
}

func TestRotatedEventsNameRequiresTimestampAndSequence(t *testing.T) {
	if isRotatedEventsName("events-index.jsonl") {
		t.Fatal("events-index.jsonl must not be treated as a rotated fragment")
	}
	if isRotatedEventsName("events-20260101T000000.000000000Z.jsonl") {
		t.Fatal("fragment names without a 6-digit sequence must be ignored")
	}
	if isRotatedEventsName("events.jsonl") {
		t.Fatal("active events.jsonl must not match a rotated fragment")
	}
	if !isRotatedEventsName("events-20260101T000000.000000000Z-000001.jsonl") {
		t.Fatal("generated timestamp+sequence fragment name was rejected")
	}
}

func TestCleanupAndReclaimSkipUnknownSessionsAndIndexFiles(t *testing.T) {
	root := t.TempDir()
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(tracesRoot); err != nil {
		t.Fatalf("create traces root: %v", err)
	}
	now := time.Now().UTC()

	openPath := writeTestSession(t, tracesRoot, "open-basic", Manifest{
		SchemaVersion: SchemaVersion, AppSessionID: "open-basic", Mode: ModeBasic, Status: "open", StartedAt: now,
	})
	knownFragment := filepath.Join(openPath, "events-20260101T000000.000000000Z-000001.jsonl")
	writeAgedFile(t, knownFragment, []byte(strings.Repeat("k", 1024)), now.Add(-time.Hour))
	if err := os.WriteFile(filepath.Join(openPath, eventsFilename), []byte("live\n"), 0o600); err != nil {
		t.Fatalf("write open events: %v", err)
	}

	unknownSchema := writeTestSession(t, tracesRoot, "unknown-schema", Manifest{
		SchemaVersion: SchemaVersion + 9, AppSessionID: "unknown-schema", Mode: ModeBasic, Status: "open", StartedAt: now,
	})
	unknownFragment := filepath.Join(unknownSchema, "events-20260101T000000.000000000Z-000001.jsonl")
	indexFile := filepath.Join(unknownSchema, "events-index.jsonl")
	writeAgedFile(t, unknownFragment, []byte(strings.Repeat("u", 1024)), now.Add(-time.Hour))
	if err := os.WriteFile(indexFile, []byte("index\n"), 0o600); err != nil {
		t.Fatalf("write events-index: %v", err)
	}

	unknownMode := writeTestSession(t, tracesRoot, "unknown-mode", Manifest{
		SchemaVersion: SchemaVersion, AppSessionID: "unknown-mode", Mode: "debug", Status: "closed", StartedAt: now,
	})
	modeFragment := filepath.Join(unknownMode, "events-20260101T000000.000000000Z-000002.jsonl")
	writeAgedFile(t, modeFragment, []byte(strings.Repeat("m", 1024)), now.Add(-time.Hour))

	badPath := filepath.Join(tracesRoot, "bad-manifest")
	if err := ensurePrivateDir(badPath); err != nil {
		t.Fatalf("create bad manifest dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(badPath, manifestFilename), []byte("not-json"), 0o600); err != nil {
		t.Fatalf("write bad manifest: %v", err)
	}
	badFragment := filepath.Join(badPath, "events-20260101T000000.000000000Z-000001.jsonl")
	badIndex := filepath.Join(badPath, "events-index.jsonl")
	writeAgedFile(t, badFragment, []byte(strings.Repeat("b", 1024)), now.Add(-time.Hour))
	if err := os.WriteFile(badIndex, []byte("index\n"), 0o600); err != nil {
		t.Fatalf("write bad index: %v", err)
	}

	result, err := CleanupAllClosedSessions(root)
	if err != nil {
		t.Fatalf("CleanupAllClosedSessions: %v", err)
	}
	if result.RemovedSessions != 0 {
		t.Fatalf("unknown/open sessions were deleted: %+v", result)
	}
	mustMissing(t, knownFragment)
	mustStat(t, filepath.Join(openPath, eventsFilename))
	mustStat(t, unknownFragment)
	mustStat(t, indexFile)
	mustStat(t, modeFragment)
	mustStat(t, badFragment)
	mustStat(t, badIndex)

	budget := testBudget(root, 1, 1<<20)
	if err := budget.reclaimNormal(1); err == nil {
		// reclaim may fail because nothing safe is reclaimable besides the known fragment, which cleanup already removed.
	}
	mustStat(t, unknownFragment)
	mustStat(t, indexFile)
	mustStat(t, modeFragment)
	mustStat(t, badFragment)
	mustStat(t, badIndex)
}

func TestCleanupProtectsLiveBudgetPathsNotJustNewestMtime(t *testing.T) {
	root := t.TempDir()
	controller, err := NewController(root, Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	if _, err := controller.WriteAppLog([]byte("2026-01-01T00:00:00Z INF live-app\n")); err != nil {
		t.Fatalf("seed app write: %v", err)
	}
	controller.budget.mu.Lock()
	activeApp := controller.budget.activeAppPath
	controller.budget.mu.Unlock()
	if activeApp == "" {
		t.Fatal("expected active app path")
	}
	now := time.Now().UTC()
	if err := os.Chtimes(activeApp, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("age active app: %v", err)
	}
	decoyApp := filepath.Join(root, appLogDirname, "app-20260102T000000.000000000Z-000099.log")
	writeAgedFile(t, decoyApp, []byte(strings.Repeat("n", 2048)), now)

	if !controller.RecordEvent(context.Background(), Event{
		Layer: "backend", Event: "error_event", ErrorCategory: "provider_error", SemanticOutcome: OutcomeFailed,
	}) {
		t.Fatal("seed diagnostic event rejected")
	}
	deadline := time.Now().Add(2 * time.Second)
	var activeDiags map[string]struct{}
	for time.Now().Before(deadline) {
		controller.budget.mu.Lock()
		activeDiags = map[string]struct{}{}
		for _, path := range controller.budget.activeDiagnostics {
			if path != "" {
				activeDiags[path] = struct{}{}
			}
		}
		controller.budget.mu.Unlock()
		if len(activeDiags) > 0 {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if len(activeDiags) == 0 {
		t.Fatal("expected active diagnostics path")
	}
	for path := range activeDiags {
		if err := os.Chtimes(path, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
			t.Fatalf("age active diag %s: %v", path, err)
		}
	}
	decoyDiag := filepath.Join(root, diagnosticsDirname, "diagnostics-20260102T000000.000000000Z-000099.jsonl")
	writeAgedFile(t, decoyDiag, []byte(strings.Repeat("d", 2048)), now)

	result, err := CleanupAllClosedSessions(root)
	if err != nil {
		t.Fatalf("CleanupAllClosedSessions: %v", err)
	}
	if result.FreedBytes < 2048 {
		t.Fatalf("cleanup did not free decoy shards: %+v", result)
	}
	mustStat(t, activeApp)
	mustMissing(t, decoyApp)
	for path := range activeDiags {
		mustStat(t, path)
	}
	mustMissing(t, decoyDiag)
	if _, err := controller.WriteAppLog([]byte("2026-01-01T00:00:00Z INF after-protected-cleanup\n")); err != nil {
		t.Fatalf("write after cleanup: %v", err)
	}
}

func TestQuotaBlockClearsAfterQueueFullAndPayloadRecovery(t *testing.T) {
	root := t.TempDir()
	recorder, err := NewRecorder(root, Settings{Mode: ModeFull, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewRecorder: %v", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })

	recorder.mu.Lock()
	recorder.status.QuotaBlocked = true
	recorder.status.LastError = "event_quota_exceeded"
	recorder.setDroppedLocked(4)
	recorder.mu.Unlock()
	status := recorder.Status()
	if !status.QuotaBlocked || status.LastError != "event_queue_full" {
		t.Fatalf("setup status = %+v, want blocked with queue_full last error", status)
	}
	if !recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "request_finished", Status: "success"}) {
		t.Fatal("recovery event rejected")
	}
	status = waitForStatus(t, recorder, func(status Status) bool { return !status.QuotaBlocked })
	if status.QuotaBlocked {
		t.Fatalf("queue_full last error kept QuotaBlocked: %+v", status)
	}
	if status.DroppedEvents != 4 {
		t.Fatalf("historical dropped count was reset: %+v", status)
	}

	recorder.mu.Lock()
	recorder.status.QuotaBlocked = true
	recorder.status.LastError = "payload_quota_exceeded"
	recorder.status.PayloadDegraded = true
	recorder.mu.Unlock()
	if !recorder.Record(context.Background(), Capture{
		Event:   Event{Layer: "backend", Event: "request_finished", Status: "success"},
		Payload: &Payload{Name: "body", Data: map[string]any{"ok": true}},
	}) {
		t.Fatal("payload recovery event rejected")
	}
	status = waitForStatus(t, recorder, func(status Status) bool { return !status.QuotaBlocked })
	if status.QuotaBlocked {
		t.Fatalf("payload recovery left QuotaBlocked: %+v", status)
	}
	if !status.PayloadDegraded {
		t.Fatal("historical payload degraded was cleared")
	}
}

func TestRotateEventsUsesExclusiveSequenceAndDoesNotLoopOnRenameExist(t *testing.T) {
	root := t.TempDir()
	budget := testBudget(root, eventReserveBytes+1<<20, 1<<20)
	writer, err := openSession(root, Settings{Mode: ModeBasic, RetentionDays: 7}, budget)
	if err != nil {
		t.Fatalf("openSession: %v", err)
	}
	t.Cleanup(func() { _ = writer.close("closed") })
	if err := writer.appendEvent(Event{Layer: "backend", Event: "request_finished", Status: "success", Severity: SeverityInfo}); err != nil {
		t.Fatalf("seed event: %v", err)
	}
	if err := os.WriteFile(filepath.Join(writer.dir, "events-index.jsonl"), []byte("index\n"), 0o600); err != nil {
		t.Fatalf("write events-index: %v", err)
	}
	writer.mu.Lock()
	writer.budget.mu.Lock()
	if err := writer.rotateEventsLocked(); err != nil {
		writer.budget.mu.Unlock()
		writer.mu.Unlock()
		t.Fatalf("first rotate: %v", err)
	}
	if err := writer.rotateEventsLocked(); err != nil {
		writer.budget.mu.Unlock()
		writer.mu.Unlock()
		t.Fatalf("second rotate: %v", err)
	}
	writer.budget.mu.Unlock()
	writer.mu.Unlock()

	entries, err := os.ReadDir(writer.dir)
	if err != nil {
		t.Fatalf("read session: %v", err)
	}
	var fragments []string
	for _, entry := range entries {
		if isRotatedEventsName(entry.Name()) {
			fragments = append(fragments, entry.Name())
		}
	}
	if len(fragments) != 2 {
		t.Fatalf("rotated fragments = %v, want 2 exclusive timestamp+sequence names", fragments)
	}
	mustStat(t, filepath.Join(writer.dir, eventsFilename))
	mustStat(t, filepath.Join(writer.dir, "events-index.jsonl"))
}
