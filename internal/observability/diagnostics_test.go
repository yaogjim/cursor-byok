package observability

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func testBudget(root string, normalBytes int64, diagnosticBytes int64) *logBudget {
	return &logBudget{
		root:            root,
		retention:       7 * 24 * time.Hour,
		normalLimit:     normalBytes,
		diagnosticLimit: diagnosticBytes,
	}
}

func readEventsFile(t *testing.T, path string) []Event {
	t.Helper()
	payload, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read events file %s: %v", path, err)
	}
	lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
	events := make([]Event, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var event Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

// waitForStatus polls the recorder until the async run loop publishes the
// expected state.
func waitForStatus(t *testing.T, recorder *Recorder, predicate func(Status) bool) Status {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	var status Status
	for time.Now().Before(deadline) {
		status = recorder.Status()
		if predicate(status) {
			return status
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("recorder status never matched: %+v", status)
	return status
}

func diagnosticShards(t *testing.T, root string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, diagnosticsDirname))
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		t.Fatalf("read diagnostics dir: %v", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), diagnosticsPrefix+"-") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	return names
}

func readAllDiagnosticEvents(t *testing.T, root string) []Event {
	t.Helper()
	var events []Event
	for _, name := range diagnosticShards(t, root) {
		events = append(events, readEventsFile(t, filepath.Join(root, diagnosticsDirname, name))...)
	}
	return events
}

// diagnosticRecordBytes mirrors the diagnostics sink payload sizing: the event
// is marshaled with no payload ref and a trailing newline.
func diagnosticRecordBytes(t *testing.T, event Event) int64 {
	t.Helper()
	event.PayloadRef = ""
	payload, err := json.Marshal(event)
	if err != nil {
		t.Fatalf("marshal diagnostic event: %v", err)
	}
	return int64(len(payload) + 1)
}

func TestDiagnosticsSinkWritesOnlyWarningAndError(t *testing.T) {
	root := t.TempDir()
	recorder, err := newRecorderWithBudget(root, Settings{Mode: ModeBasic, RetentionDays: 7}, nil, testBudget(root, 1<<20, 1<<20))
	if err != nil {
		t.Fatalf("newRecorderWithBudget: %v", err)
	}
	_ = recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "info_event", SemanticOutcome: OutcomeSucceeded})
	_ = recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "warn_event", SemanticOutcome: OutcomeDegraded})
	_ = recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "error_event", ErrorCategory: "provider_error", SemanticOutcome: OutcomeFailed})
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	shards := diagnosticShards(t, root)
	if len(shards) != 1 {
		t.Fatalf("diagnostic shards = %v, want 1", shards)
	}
	events := readEventsFile(t, filepath.Join(root, diagnosticsDirname, shards[0]))
	if len(events) != 2 {
		t.Fatalf("diagnostic events = %d, want 2 (%+v)", len(events), events)
	}
	for _, event := range events {
		if event.Severity != SeverityWarning && event.Severity != SeverityError {
			t.Fatalf("diagnostics retained non WARN/ERROR event: %+v", event)
		}
		if event.PayloadRef != "" {
			t.Fatalf("diagnostics retained payload ref: %+v", event)
		}
	}
	if events[0].Event != "warn_event" || events[1].Event != "error_event" {
		t.Fatalf("unexpected diagnostic events: %+v", events)
	}
}

func TestDiagnosticsSharesEventIdentityWithTrace(t *testing.T) {
	root := t.TempDir()
	recorder, err := newRecorderWithBudget(root, Settings{Mode: ModeBasic, RetentionDays: 7}, nil, testBudget(root, eventReserveBytes+1<<20, 1<<20))
	if err != nil {
		t.Fatalf("newRecorderWithBudget: %v", err)
	}
	status := recorder.Status()
	recorded := recorder.RecordEvent(context.Background(), Event{
		Layer:           "backend",
		Event:           "error_event",
		ErrorCategory:   "provider_error",
		SemanticOutcome: OutcomeFailed,
	})
	if !recorded {
		t.Fatal("event was not accepted")
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	traceEvents := readEventsFile(t, filepath.Join(status.SessionPath, eventsFilename))
	shards := diagnosticShards(t, root)
	if len(shards) != 1 {
		t.Fatalf("diagnostic shards = %v", shards)
	}
	diagnosticEvents := readEventsFile(t, filepath.Join(root, diagnosticsDirname, shards[0]))
	if len(traceEvents) != 1 || len(diagnosticEvents) != 1 {
		t.Fatalf("trace events = %d, diagnostic events = %d", len(traceEvents), len(diagnosticEvents))
	}
	traceEvent := traceEvents[0]
	diagnosticEvent := diagnosticEvents[0]
	if traceEvent.Sequence != diagnosticEvent.Sequence || traceEvent.AppSessionID != diagnosticEvent.AppSessionID {
		t.Fatalf("identity mismatch: trace=%+v diagnostic=%+v", traceEvent, diagnosticEvent)
	}
	if !traceEvent.Timestamp.Equal(diagnosticEvent.Timestamp) {
		t.Fatalf("timestamp mismatch: %v vs %v", traceEvent.Timestamp, diagnosticEvent.Timestamp)
	}
	if traceEvent.AppSessionID != status.SessionID {
		t.Fatalf("app_session_id = %q, want %q", traceEvent.AppSessionID, status.SessionID)
	}
}

func TestTraceQuotaFailureStillWritesDiagnostics(t *testing.T) {
	root := t.TempDir()
	budget := testBudget(root, 1<<20, 1<<20)
	recorder, err := newRecorderWithBudget(root, Settings{Mode: ModeBasic, RetentionDays: 7}, nil, budget)
	if err != nil {
		t.Fatalf("newRecorderWithBudget: %v", err)
	}
	// Tighten the normal partition after the session manifest was persisted so
	// trace writes are refused while the open session stays protected.
	budget.mu.Lock()
	budget.normalLimit = 1
	budget.usageKnown = true
	budget.mu.Unlock()
	accepted := recorder.RecordEvent(context.Background(), Event{
		Layer:           "backend",
		Event:           "error_event",
		ErrorCategory:   "provider_error",
		SemanticOutcome: OutcomeFailed,
	})
	if !accepted {
		t.Fatal("Record must still accept the event for the caller when trace quota is exceeded")
	}
	status := waitForStatus(t, recorder, func(status Status) bool { return status.QuotaBlocked })
	if !status.Enabled {
		t.Fatalf("trace quota failure must not disable the recorder: %+v", status)
	}
	if !status.DiagnosticEnabled || status.DiagnosticDegraded {
		t.Fatalf("diagnostic status = %+v, want enabled and not degraded", status)
	}
	// The normal partition is exhausted on purpose, so the terminal manifest is
	// denied and Close must report the quota failure instead of swallowing it.
	if err := recorder.Close(); !errors.Is(err, errSessionQuotaExceeded) {
		t.Fatalf("Close error = %v, want %v", err, errSessionQuotaExceeded)
	}
	shards := diagnosticShards(t, root)
	if len(shards) != 1 {
		t.Fatalf("diagnostic shards = %v, want 1", shards)
	}
	events := readEventsFile(t, filepath.Join(root, diagnosticsDirname, shards[0]))
	if len(events) != 1 || events[0].Event != "error_event" {
		t.Fatalf("diagnostics did not retain the quota-blocked error: %+v", events)
	}
}

func TestDiagnosticsHasIndependentBudgetAndRotates(t *testing.T) {
	root := t.TempDir()
	const diagnosticLimit = int64(1024)
	budget := testBudget(root, 1<<20, diagnosticLimit)
	recorder, err := newRecorderWithBudget(root, Settings{Mode: ModeBasic, RetentionDays: 7}, nil, budget)
	if err != nil {
		t.Fatalf("newRecorderWithBudget: %v", err)
	}
	// Tighten the normal partition after the session manifest was persisted.
	budget.mu.Lock()
	budget.normalLimit = 1
	budget.usageKnown = true
	budget.mu.Unlock()
	for index := 0; index < 5; index++ {
		_ = recorder.RecordEvent(context.Background(), Event{
			Layer:           "backend",
			Event:           "warn_event",
			ErrorCategory:   "provider_error",
			SemanticOutcome: OutcomeFailed,
			Fields:          map[string]any{"detail": strings.Repeat("x", 256)},
		})
	}
	// The normal partition is exhausted on purpose, so the terminal manifest is
	// denied and Close must report the quota failure instead of swallowing it.
	if err := recorder.Close(); !errors.Is(err, errSessionQuotaExceeded) {
		t.Fatalf("Close error = %v, want %v", err, errSessionQuotaExceeded)
	}
	shards := diagnosticShards(t, root)
	total := int64(0)
	for _, name := range shards {
		info, err := os.Stat(filepath.Join(root, diagnosticsDirname, name))
		if err != nil {
			t.Fatalf("stat diagnostic shard: %v", err)
		}
		total += info.Size()
	}
	if total > diagnosticLimit+4096 {
		t.Fatalf("diagnostics exceeded its own budget: total=%d limit=%d", total, diagnosticLimit)
	}
	// Five oversized WARN events cannot all be retained inside the bounded
	// diagnostics partition, so older shards must have been rotated out.
	retained := 0
	for _, name := range shards {
		retained += len(readEventsFile(t, filepath.Join(root, diagnosticsDirname, name)))
	}
	if retained >= 5 {
		t.Fatalf("diagnostics did not rotate out old shards: retained=%d", retained)
	}
}

// TestDiagnosticsReserveSharedBetweenOverlappingWriters proves the diagnostics
// reserve D is enforced through the shared budget even while two diagnostics
// sinks (for example an old and a new sink during a reconfigure) are open at
// the same time: the directory cannot exceed D and rejected records are
// reported as drops.
func TestDiagnosticsReserveSharedBetweenOverlappingWriters(t *testing.T) {
	root := t.TempDir()
	const limit = int64(20000)
	budget := testBudget(root, 1<<20, limit)
	// A generous rotation budget keeps the shards from rotating, so the shared
	// reserve check, not rotation, is what bounds the directory.
	first, err := openDiagnosticSink(root, budget, 7*24*time.Hour, 1<<20)
	if err != nil {
		t.Fatalf("open first diagnostics sink: %v", err)
	}
	second, err := openDiagnosticSink(root, budget, 7*24*time.Hour, 1<<20)
	if err != nil {
		t.Fatalf("open second diagnostics sink: %v", err)
	}
	event := Event{
		Layer:           "backend",
		Event:           "error_event",
		ErrorCategory:   "provider_error",
		SemanticOutcome: OutcomeFailed,
		Severity:        SeverityError,
		Fields:          map[string]any{"detail": strings.Repeat("x", 4000)},
	}
	for index := 0; index < 40; index++ {
		first.write(event)
		second.write(event)
	}
	usage, err := directorySize(filepath.Join(root, diagnosticsDirname))
	if err != nil {
		t.Fatalf("diagnostics usage: %v", err)
	}
	if usage > limit {
		t.Fatalf("diagnostics partition exceeded reserve: usage=%d limit=%d", usage, limit)
	}
	// Sealing the caller's own oversized active shard keeps the newest records
	// flowing, so dropped counters are not required; the hard cap above is the
	// invariant.
}

func TestNormalReclaimProtectsOpenTraceUnknownAndRemovesClosedBasicAndApp(t *testing.T) {
	root := t.TempDir()
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(tracesRoot); err != nil {
		t.Fatalf("create traces root: %v", err)
	}
	now := time.Now().UTC()
	closedBasic := writeTestSession(t, tracesRoot, "closed-basic", Manifest{
		SchemaVersion: SchemaVersion, AppSessionID: "closed-basic", Mode: ModeBasic, Status: "closed", StartedAt: now.Add(-3 * time.Hour),
	})
	if err := os.WriteFile(filepath.Join(closedBasic, eventsFilename), []byte(strings.Repeat("b", 4096)), 0o600); err != nil {
		t.Fatalf("write closed basic events: %v", err)
	}
	openFull := writeTestSession(t, tracesRoot, "open-full", Manifest{
		SchemaVersion: SchemaVersion, AppSessionID: "open-full", Mode: ModeFull, Status: "open", StartedAt: now.Add(-2 * time.Hour),
	})
	unknown := filepath.Join(root, "notes.txt")
	if err := os.WriteFile(unknown, []byte("keep me"), 0o600); err != nil {
		t.Fatalf("write unknown file: %v", err)
	}
	appDir := filepath.Join(root, "app")
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatalf("create app dir: %v", err)
	}
	oldApp := filepath.Join(appDir, "app-20260101T000000.000000000Z-000001.log")
	newApp := filepath.Join(appDir, "app-20260102T000000.000000000Z-000002.log")
	for _, path := range []string{oldApp, newApp} {
		if err := os.WriteFile(path, []byte(strings.Repeat("a", 2048)), 0o600); err != nil {
			t.Fatalf("write app shard: %v", err)
		}
	}
	if err := os.Chtimes(oldApp, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("age app shard: %v", err)
	}
	if err := os.Chtimes(newApp, now, now); err != nil {
		t.Fatalf("touch app shard: %v", err)
	}

	shards := diagnosticShards(t, root)
	if len(shards) != 0 {
		t.Fatalf("unexpected diagnostics: %v", shards)
	}
	budget := testBudget(root, 4096, 1<<20)
	if err := budget.reclaimNormal(1); err != nil {
		t.Fatalf("reclaimNormal: %v", err)
	}
	if _, err := os.Stat(closedBasic); !os.IsNotExist(err) {
		t.Fatalf("closed basic session was not reclaimed: %v", err)
	}
	for _, path := range []string{openFull, unknown, newApp} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("protected path was removed %s: %v", path, err)
		}
	}
	if _, err := os.Stat(oldApp); !os.IsNotExist(err) {
		t.Fatalf("oldest archived app shard was not reclaimed: %v", err)
	}
}

func TestNormalReclaimUsesFullTierBeforeMergedTier(t *testing.T) {
	root := t.TempDir()
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(tracesRoot); err != nil {
		t.Fatalf("create traces root: %v", err)
	}
	now := time.Now().UTC()
	closedFull := writeTestSession(t, tracesRoot, "closed-full", Manifest{
		SchemaVersion: SchemaVersion, AppSessionID: "closed-full", Mode: ModeFull, Status: "closed", StartedAt: now,
	})
	closedBasic := writeTestSession(t, tracesRoot, "closed-basic", Manifest{
		SchemaVersion: SchemaVersion, AppSessionID: "closed-basic", Mode: ModeBasic, Status: "closed", StartedAt: now.Add(-time.Hour),
	})
	for _, path := range []string{closedFull, closedBasic} {
		if err := os.WriteFile(filepath.Join(path, eventsFilename), []byte(strings.Repeat("t", 2048)), 0o600); err != nil {
			t.Fatalf("write trace data: %v", err)
		}
	}
	appDir := filepath.Join(root, appLogDirname)
	if err := os.MkdirAll(appDir, 0o700); err != nil {
		t.Fatalf("create app dir: %v", err)
	}
	oldApp := filepath.Join(appDir, "app-20260101T000000.000000000Z-000001.log")
	newApp := filepath.Join(appDir, "app-20260102T000000.000000000Z-000002.log")
	for _, path := range []string{oldApp, newApp} {
		if err := os.WriteFile(path, []byte(strings.Repeat("a", 2048)), 0o600); err != nil {
			t.Fatalf("write app shard: %v", err)
		}
	}
	if err := os.Chtimes(oldApp, now.Add(-2*time.Hour), now.Add(-2*time.Hour)); err != nil {
		t.Fatalf("age app shard: %v", err)
	}
	if err := os.Chtimes(newApp, now, now); err != nil {
		t.Fatalf("touch app shard: %v", err)
	}
	usage, err := normalUsageBytes(root)
	if err != nil {
		t.Fatalf("normal usage: %v", err)
	}
	fullSize, err := directorySize(closedFull)
	if err != nil {
		t.Fatalf("full trace size: %v", err)
	}
	budget := testBudget(root, usage-fullSize+1, 1<<20)
	if err := budget.reclaimNormal(1); err != nil {
		t.Fatalf("reclaimNormal: %v", err)
	}
	if _, err := os.Stat(closedFull); !os.IsNotExist(err) {
		t.Fatalf("closed full trace was not reclaimed first: %v", err)
	}
	for _, path := range []string{closedBasic, oldApp, newApp} {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("later reclaim tier was removed before needed %s: %v", path, err)
		}
	}
}

// TestNormalReclaimMergesBasicTracesAndAppShardsByTime proves that closed basic
// trace sessions and archived app log shards share a single reclaim tier
// ordered by time, in both directions: an older app shard is reclaimed before a
// newer basic trace, and an older basic trace is reclaimed before a newer
// archived app shard.
func TestNormalReclaimMergesBasicTracesAndAppShardsByTime(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name         string
		basicStarted time.Time
		appModTime   time.Time
		wantRemoved  string
	}{
		{
			// The app shard is the oldest merged candidate, so it must be
			// reclaimed before the newer closed basic trace.
			name:         "older_app_shard_before_newer_basic_trace",
			basicStarted: now,
			appModTime:   now.Add(-2 * time.Hour),
			wantRemoved:  "app",
		},
		{
			// The closed basic trace is the oldest merged candidate, so it must
			// be reclaimed before the newer archived app shard.
			name:         "older_basic_trace_before_newer_app_shard",
			basicStarted: now.Add(-2 * time.Hour),
			appModTime:   now.Add(-time.Hour),
			wantRemoved:  "basic",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			tracesRoot := filepath.Join(root, tracesDirname)
			if err := ensurePrivateDir(tracesRoot); err != nil {
				t.Fatalf("create traces root: %v", err)
			}
			basic := writeTestSession(t, tracesRoot, "closed-basic", Manifest{
				SchemaVersion: SchemaVersion,
				AppSessionID:  "closed-basic",
				Mode:          ModeBasic,
				Status:        "closed",
				StartedAt:     testCase.basicStarted,
			})
			if err := os.WriteFile(filepath.Join(basic, eventsFilename), []byte(strings.Repeat("b", 2048)), 0o600); err != nil {
				t.Fatalf("write basic events: %v", err)
			}
			appDir := filepath.Join(root, appLogDirname)
			if err := os.MkdirAll(appDir, 0o700); err != nil {
				t.Fatalf("create app dir: %v", err)
			}
			candidateApp := filepath.Join(appDir, "app-20260101T000000.000000000Z-000001.log")
			newestApp := filepath.Join(appDir, "app-20260102T000000.000000000Z-000002.log")
			for _, path := range []string{candidateApp, newestApp} {
				if err := os.WriteFile(path, []byte(strings.Repeat("a", 2048)), 0o600); err != nil {
					t.Fatalf("write app shard: %v", err)
				}
			}
			touchTime := time.Now().UTC()
			if err := os.Chtimes(newestApp, touchTime, touchTime); err != nil {
				t.Fatalf("touch newest app shard: %v", err)
			}
			if err := os.Chtimes(candidateApp, testCase.appModTime, testCase.appModTime); err != nil {
				t.Fatalf("age candidate app shard: %v", err)
			}
			usage, err := normalUsageBytes(root)
			if err != nil {
				t.Fatalf("normal usage: %v", err)
			}
			var oldestSize int64
			if testCase.wantRemoved == "basic" {
				oldestSize, err = directorySize(basic)
			} else {
				var info os.FileInfo
				info, err = os.Stat(candidateApp)
				if err == nil {
					oldestSize = info.Size()
				}
			}
			if err != nil {
				t.Fatalf("oldest candidate size: %v", err)
			}
			// Leave room for exactly the oldest merged candidate, so exactly one
			// item must be reclaimed and it must be the time-oldest one.
			budget := testBudget(root, usage-oldestSize+1, 1<<20)
			if err := budget.reclaimNormal(1); err != nil {
				t.Fatalf("reclaimNormal: %v", err)
			}
			if testCase.wantRemoved == "basic" {
				if _, err := os.Stat(basic); !os.IsNotExist(err) {
					t.Fatalf("oldest basic trace was not reclaimed first: %v", err)
				}
				if _, err := os.Stat(candidateApp); err != nil {
					t.Fatalf("newer app shard was reclaimed early: %v", err)
				}
			} else {
				if _, err := os.Stat(candidateApp); !os.IsNotExist(err) {
					t.Fatalf("oldest app shard was not reclaimed first: %v", err)
				}
				if _, err := os.Stat(basic); err != nil {
					t.Fatalf("newer basic trace was reclaimed early: %v", err)
				}
			}
			if _, err := os.Stat(newestApp); err != nil {
				t.Fatalf("protected newest app shard was removed: %v", err)
			}
		})
	}
}

func TestDiagnosticsFailureDoesNotAffectRecordOrTrace(t *testing.T) {
	root := t.TempDir()
	// Block the diagnostics directory so the sink cannot write.
	if err := os.WriteFile(filepath.Join(root, diagnosticsDirname), []byte("blocked"), 0o600); err != nil {
		t.Fatalf("block diagnostics dir: %v", err)
	}
	recorder, err := newRecorderWithBudget(root, Settings{Mode: ModeBasic, RetentionDays: 7}, nil, testBudget(root, eventReserveBytes+1<<20, 1<<20))
	if err != nil {
		t.Fatalf("newRecorderWithBudget: %v", err)
	}
	status := recorder.Status()
	if !recorder.RecordEvent(context.Background(), Event{
		Layer: "backend", Event: "error_event", ErrorCategory: "provider_error", SemanticOutcome: OutcomeFailed,
	}) {
		t.Fatal("Record returned false after diagnostic failure")
	}
	final := waitForStatus(t, recorder, func(status Status) bool { return status.DiagnosticDegraded && status.DiagnosticDropped > 0 })
	if !final.Enabled {
		t.Fatalf("diagnostic failure disabled the recorder: %+v", final)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	traceEvents := readEventsFile(t, filepath.Join(status.SessionPath, eventsFilename))
	if len(traceEvents) != 1 {
		t.Fatalf("trace events = %d, want 1", len(traceEvents))
	}
}

func TestNormalPartitionDoesNotConsumeDiagnosticReserve(t *testing.T) {
	root := t.TempDir()
	diagnosticsDir := filepath.Join(root, diagnosticsDirname)
	if err := ensurePrivateDir(diagnosticsDir); err != nil {
		t.Fatalf("create diagnostics dir: %v", err)
	}
	shard := filepath.Join(diagnosticsDir, "diagnostics-20260101T000000.000000000Z-000001.jsonl")
	if err := os.WriteFile(shard, []byte(strings.Repeat("d", 8192)), 0o600); err != nil {
		t.Fatalf("write diagnostics shard: %v", err)
	}
	usage, err := normalUsageBytes(root)
	if err != nil {
		t.Fatalf("normal usage: %v", err)
	}
	if usage != 0 {
		t.Fatalf("normal usage counted diagnostics bytes: %d", usage)
	}
	budget := testBudget(root, 2048, 1<<20)
	if err := budget.initialize(); err != nil {
		t.Fatalf("initialize budget: %v", err)
	}
	if !budget.admitNormalLocked(1024, 0, "") {
		t.Fatal("normal partition refused a write because of diagnostics usage")
	}
}

func TestSharedBudgetRejectsConcurrentOvercommit(t *testing.T) {
	root := t.TempDir()
	// Data room on top of the eventReserveBytes metadata headroom that every
	// ordinary write preserves.
	const normalLimit = eventReserveBytes + 4096
	budget := testBudget(root, normalLimit, 1<<20)
	first, err := newRecorderWithBudget(root, Settings{Mode: ModeBasic, RetentionDays: 7}, nil, budget)
	if err != nil {
		t.Fatalf("first recorder: %v", err)
	}
	second, err := newRecorderWithBudget(root, Settings{Mode: ModeBasic, RetentionDays: 7}, nil, budget)
	if err != nil {
		t.Fatalf("second recorder: %v", err)
	}
	payload := strings.Repeat("x", 1024)
	for index := 0; index < 20; index++ {
		_ = first.RecordEvent(context.Background(), Event{Layer: "backend", Event: "info_event", Fields: map[string]any{"detail": payload}})
		_ = second.RecordEvent(context.Background(), Event{Layer: "backend", Event: "info_event", Fields: map[string]any{"detail": payload}})
	}
	if err := first.Close(); err != nil {
		t.Fatalf("first close: %v", err)
	}
	if err := second.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	usage, err := normalUsageBytes(root)
	if err != nil {
		t.Fatalf("normal usage: %v", err)
	}
	if usage > normalLimit {
		t.Fatalf("shared budget over-committed: usage=%d limit=%d", usage, normalLimit)
	}
}

func TestControllerReconfigureRestoresBudgetAfterOpenFailure(t *testing.T) {
	root := t.TempDir()
	initial := Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64}
	controller, err := NewController(root, initial)
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	budget := controller.budget
	budget.mu.Lock()
	oldNormal := budget.normalLimit
	oldDiagnostic := budget.diagnosticLimit
	budget.mu.Unlock()

	tracesRoot := filepath.Join(root, tracesDirname)
	backupRoot := tracesRoot + ".backup"
	if err := os.Rename(tracesRoot, backupRoot); err != nil {
		t.Fatalf("rename traces root: %v", err)
	}
	if err := os.WriteFile(tracesRoot, []byte("blocked"), 0o600); err != nil {
		t.Fatalf("block traces root: %v", err)
	}
	if err := controller.Reconfigure(Settings{Mode: ModeFull, RetentionDays: 14, MaxDiskMB: 128}); err == nil {
		t.Fatal("Reconfigure succeeded with a blocked traces root")
	}
	budget.mu.Lock()
	gotNormal := budget.normalLimit
	gotDiagnostic := budget.diagnosticLimit
	budget.mu.Unlock()
	if gotNormal != oldNormal || gotDiagnostic != oldDiagnostic {
		t.Fatalf("failed Reconfigure left new budget limits: normal=%d/%d diagnostic=%d/%d", gotNormal, oldNormal, gotDiagnostic, oldDiagnostic)
	}
	if err := os.Remove(tracesRoot); err != nil {
		t.Fatalf("remove blocked traces root: %v", err)
	}
	if err := os.Rename(backupRoot, tracesRoot); err != nil {
		t.Fatalf("restore traces root: %v", err)
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func TestControllerReconfigureReusesSharedBudget(t *testing.T) {
	root := t.TempDir()
	controller, err := NewController(root, Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewController: %v", err)
	}
	shared := controller.budget
	if shared == nil {
		t.Fatal("controller has no shared budget")
	}
	if err := controller.Reconfigure(Settings{Mode: ModeFull, RetentionDays: 14, MaxDiskMB: 128}); err != nil {
		t.Fatalf("Reconfigure: %v", err)
	}
	if controller.budget != shared {
		t.Fatal("Reconfigure replaced the shared budget")
	}
	controller.mu.RLock()
	next := controller.recorder
	controller.mu.RUnlock()
	if next == nil || next.writer.budget != shared {
		t.Fatal("new recorder does not share the controller budget")
	}
	if next.diag == nil || next.diag.budget != shared {
		t.Fatal("new diagnostics sink does not share the controller budget")
	}
	if err := controller.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestDiagnosticsReclaimsSealedShardsInsteadOfFreezing guards the regression
// where the reserve precheck ran before Append: once the directory was within
// one record of D, the check rejected every write and rotation/cleanup inside
// Append never ran again, so the newest diagnostics were lost forever.
func TestDiagnosticsReclaimsSealedShardsInsteadOfFreezing(t *testing.T) {
	root := t.TempDir()
	const diagnosticLimit = int64(2048)
	budget := testBudget(root, 1<<20, diagnosticLimit)
	sink, err := openDiagnosticSink(root, budget, 0, diagnosticLimit)
	if err != nil {
		t.Fatalf("openDiagnosticSink: %v", err)
	}
	const writes = 40
	for index := 0; index < writes; index++ {
		sink.write(Event{
			Layer:           "backend",
			Event:           fmt.Sprintf("warn_%02d", index),
			Severity:        SeverityWarning,
			SemanticOutcome: OutcomeDegraded,
			Fields:          map[string]any{"detail": strings.Repeat("x", 200)},
		})
	}
	usage, err := directorySize(filepath.Join(root, diagnosticsDirname))
	if err != nil {
		t.Fatalf("diagnostics usage: %v", err)
	}
	if usage > diagnosticLimit {
		t.Fatalf("diagnostics exceeded reserve: usage=%d limit=%d", usage, diagnosticLimit)
	}
	newest := fmt.Sprintf("warn_%02d", writes-1)
	retained := readAllDiagnosticEvents(t, root)
	for _, event := range retained {
		if event.Event == newest {
			return
		}
	}
	t.Fatalf("newest diagnostic event %q was not retained (retained %d events)", newest, len(retained))
}

// TestDiagnosticsOverlappingWriterActiveShardSurvives guards the regression
// where a rotating writer deleted the active shard of another diagnostics
// writer sharing the directory, because cleanup only protected its own path.
func TestDiagnosticsOverlappingWriterActiveShardSurvives(t *testing.T) {
	root := t.TempDir()
	const diagnosticLimit = int64(4096)
	budget := testBudget(root, 1<<20, diagnosticLimit)
	first, err := openDiagnosticSink(root, budget, 0, diagnosticLimit)
	if err != nil {
		t.Fatalf("open first diagnostics sink: %v", err)
	}
	second, err := openDiagnosticSink(root, budget, 0, diagnosticLimit)
	if err != nil {
		t.Fatalf("open second diagnostics sink: %v", err)
	}
	event := Event{
		Layer:           "backend",
		Event:           "warn_shared",
		Severity:        SeverityWarning,
		SemanticOutcome: OutcomeDegraded,
		Fields:          map[string]any{"detail": strings.Repeat("y", 200)},
	}
	first.write(event)
	firstPath := first.writer.CurrentPath()
	if firstPath == "" {
		t.Fatal("first diagnostics sink has no active shard")
	}
	// The first sink goes idle (as the old recorder does during a reconfigure)
	// while the second keeps rotating and reclaiming the shared directory.
	for index := 0; index < 80; index++ {
		second.write(event)
	}
	if _, statErr := os.Stat(firstPath); statErr != nil {
		t.Fatalf("overlapping active diagnostics shard was deleted: %v", statErr)
	}
}

// TestNormalReclaimProtectsUnknownManifests proves that closed sessions with an
// unknown mode, unknown schema version, or invalid identity are protected from
// both age-based and capacity-based reclaim.
func TestNormalReclaimProtectsUnknownManifests(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		name     string
		dirName  string
		manifest Manifest
	}{
		{
			name:    "unknown mode",
			dirName: "unknown-mode",
			manifest: Manifest{
				SchemaVersion: SchemaVersion, AppSessionID: "unknown-mode", Mode: "future_mode",
				Status: "closed", StartedAt: now.Add(-2 * time.Hour),
			},
		},
		{
			name:    "unknown schema",
			dirName: "unknown-schema",
			manifest: Manifest{
				SchemaVersion: SchemaVersion + 9, AppSessionID: "unknown-schema", Mode: ModeBasic,
				Status: "closed", StartedAt: now.Add(-2 * time.Hour),
			},
		},
		{
			name:    "mismatched identity",
			dirName: "mismatched-identity",
			manifest: Manifest{
				SchemaVersion: SchemaVersion, AppSessionID: "some-other-id", Mode: ModeBasic,
				Status: "closed", StartedAt: now.Add(-2 * time.Hour),
			},
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			tracesRoot := filepath.Join(root, tracesDirname)
			if err := ensurePrivateDir(tracesRoot); err != nil {
				t.Fatalf("create traces root: %v", err)
			}
			session := writeTestSession(t, tracesRoot, testCase.dirName, testCase.manifest)
			if err := os.WriteFile(filepath.Join(session, eventsFilename), []byte(strings.Repeat("u", 2048)), 0o600); err != nil {
				t.Fatalf("write events: %v", err)
			}

			capacity := testBudget(root, 1, 1<<20)
			_ = capacity.reclaimNormal(1)
			if _, statErr := os.Stat(session); statErr != nil {
				t.Fatalf("capacity reclaim removed a protected session: %v", statErr)
			}

			age := testBudget(root, 1<<20, 1<<20)
			age.retention = time.Hour
			age.removeExpiredNormalLocked("")
			if _, statErr := os.Stat(session); statErr != nil {
				t.Fatalf("age reclaim removed a protected session: %v", statErr)
			}
		})
	}
}

// TestReclaimableManifestAcceptsKnownSchemaVersions pins the schema gate to the
// contract the log analyzer enforces: versions 1..SchemaVersion are known, so a
// closed v1 trace stays reclaimable even though the current writer emits v2.
// Only versions outside that range are treated as a future format and protected.
func TestReclaimableManifestAcceptsKnownSchemaVersions(t *testing.T) {
	now := time.Now().UTC()
	cases := []struct {
		version int
		want    bool
	}{
		{version: 0, want: false},
		{version: 1, want: true},
		{version: SchemaVersion, want: true},
		{version: SchemaVersion + 1, want: false},
	}
	for _, testCase := range cases {
		t.Run(fmt.Sprintf("v%d", testCase.version), func(t *testing.T) {
			manifest := Manifest{
				SchemaVersion: testCase.version,
				AppSessionID:  "v-session",
				Mode:          ModeBasic,
				Status:        "closed",
				StartedAt:     now,
			}
			if got := reclaimableManifest("v-session", manifest); got != testCase.want {
				t.Fatalf("reclaimableManifest(v%d) = %v, want %v", testCase.version, got, testCase.want)
			}
		})
	}
}

// TestNormalReclaimRemovesKnownV1Session guards the regression where only the
// current schema version counted as reclaimable, which made every older but
// still supported trace permanent once the normal partition filled up.
func TestNormalReclaimRemovesKnownV1Session(t *testing.T) {
	root := t.TempDir()
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(tracesRoot); err != nil {
		t.Fatalf("create traces root: %v", err)
	}
	session := writeTestSession(t, tracesRoot, "closed-v1", Manifest{
		SchemaVersion: 1, AppSessionID: "closed-v1", Mode: ModeBasic, Status: "closed",
		StartedAt: time.Now().UTC().Add(-time.Hour),
	})
	if err := os.WriteFile(filepath.Join(session, eventsFilename), []byte(strings.Repeat("v", 2048)), 0o600); err != nil {
		t.Fatalf("write events: %v", err)
	}
	budget := testBudget(root, 1, 1<<20)
	if err := budget.reclaimNormal(1); err != nil {
		t.Fatalf("reclaimNormal: %v", err)
	}
	if _, statErr := os.Stat(session); !os.IsNotExist(statErr) {
		t.Fatalf("closed v1 session was not reclaimed: %v", statErr)
	}
}

// TestDiagnosticsOversizeRecordPreservesSealedShards guards the ordering rule:
// a record larger than the whole reserve can never fit, so it must be rejected
// before any sealed shard is deleted. Otherwise a single oversized WARN would
// wipe retained diagnostics while still not being stored.
func TestDiagnosticsOversizeRecordPreservesSealedShards(t *testing.T) {
	root := t.TempDir()
	normal := Event{
		Layer:           "backend",
		Event:           "warn_normal_1",
		Severity:        SeverityWarning,
		SemanticOutcome: OutcomeDegraded,
		Fields:          map[string]any{"detail": strings.Repeat("n", 200)},
	}
	record := diagnosticRecordBytes(t, normal)
	limit := record * 3
	budget := testBudget(root, 1<<20, limit)
	sink, err := openDiagnosticSink(root, budget, 0, limit)
	if err != nil {
		t.Fatalf("openDiagnosticSink: %v", err)
	}
	sink.write(normal)
	normal.Event = "warn_normal_2"
	sink.write(normal)
	before := diagnosticShards(t, root)
	if len(before) < 2 {
		t.Fatalf("expected sealed and active shards before oversize write, got %v", before)
	}

	oversize := Event{
		Layer:           "backend",
		Event:           "warn_oversize",
		Severity:        SeverityWarning,
		SemanticOutcome: OutcomeDegraded,
		Fields:          map[string]any{"detail": strings.Repeat("o", int(limit*2))},
	}
	if diagnosticRecordBytes(t, oversize) <= limit {
		t.Fatalf("oversize fixture is not larger than the reserve %d", limit)
	}
	droppedBefore := sink.status().dropped
	sink.write(oversize)

	after := diagnosticShards(t, root)
	if strings.Join(after, ",") != strings.Join(before, ",") {
		t.Fatalf("oversize record changed retained shards: before=%v after=%v", before, after)
	}
	status := sink.status()
	if status.dropped != droppedBefore+1 || status.lastErr != "diagnostic_reserve_exceeded" {
		t.Fatalf("oversize record was not rejected as a drop: %+v", status)
	}
}

// TestDiagnosticsSealsOversizedActiveShard covers the no-sealed case: a single
// record can exceed a shard's MaxBytes, so the active shard alone can fill D.
// The next record must seal that active shard, reclaim it, and be retained
// instead of freezing forever.
func TestDiagnosticsSealsOversizedActiveShard(t *testing.T) {
	root := t.TempDir()
	big := Event{
		Layer:           "backend",
		Event:           "warn_big",
		Severity:        SeverityWarning,
		SemanticOutcome: OutcomeDegraded,
		Fields:          map[string]any{"detail": strings.Repeat("b", 600)},
	}
	small := Event{
		Layer:           "backend",
		Event:           "warn_small",
		Severity:        SeverityWarning,
		SemanticOutcome: OutcomeDegraded,
		Fields:          map[string]any{"detail": strings.Repeat("s", 40)},
	}
	bigBytes := diagnosticRecordBytes(t, big)
	smallBytes := diagnosticRecordBytes(t, small)
	limit := bigBytes + smallBytes - 1
	if bigBytes <= limit/4 {
		t.Fatalf("fixture is not oversized for D/4: big=%d limit=%d", bigBytes, limit)
	}
	if smallBytes > limit {
		t.Fatalf("fixture small record exceeds reserve: small=%d limit=%d", smallBytes, limit)
	}
	budget := testBudget(root, 1<<20, limit)
	sink, err := openDiagnosticSink(root, budget, 0, limit)
	if err != nil {
		t.Fatalf("openDiagnosticSink: %v", err)
	}
	sink.write(big)
	sink.write(small)

	retained := readAllDiagnosticEvents(t, root)
	found := false
	for _, event := range retained {
		if event.Event == "warn_small" {
			found = true
		}
	}
	if !found {
		t.Fatalf("small record not retained after sealing oversized active shard: %v", retained)
	}
	usage, err := directorySize(filepath.Join(root, diagnosticsDirname))
	if err != nil {
		t.Fatalf("diagnostics usage: %v", err)
	}
	if usage > limit {
		t.Fatalf("diagnostics exceeded reserve: usage=%d limit=%d", usage, limit)
	}
}

// TestDiagnosticsCloseReleasesShard documents the close ordering: the handle is
// closed before the writer is unregistered, so the closed shard becomes
// reclaimable and nothing is left registered after close.
func TestDiagnosticsCloseReleasesShard(t *testing.T) {
	root := t.TempDir()
	const limit = int64(4096)
	budget := testBudget(root, 1<<20, limit)
	sink, err := openDiagnosticSink(root, budget, 0, limit)
	if err != nil {
		t.Fatalf("openDiagnosticSink: %v", err)
	}
	sink.write(Event{
		Layer:           "backend",
		Event:           "warn_close",
		Severity:        SeverityWarning,
		SemanticOutcome: OutcomeDegraded,
		Fields:          map[string]any{"detail": strings.Repeat("c", 64)},
	})
	shardPath := sink.writer.CurrentPath()
	if shardPath == "" {
		t.Fatal("sink has no active shard before close")
	}
	if err := sink.close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if sink.writer.CurrentPath() != "" {
		t.Fatal("closed writer still reports an active path")
	}
	budget.mu.Lock()
	registered := false
	for writer := range budget.activeDiagnostics {
		if writer == sink.writer {
			registered = true
		}
	}
	stillProtected := budget.isActiveDiagnosticPathLocked(shardPath)
	budget.mu.Unlock()
	if registered {
		t.Fatal("closed diagnostics sink is still registered")
	}
	if stillProtected {
		t.Fatal("closed diagnostics shard is still protected from reclaim")
	}
}

// TestRecorderQueueReservesDiagnosticSlots proves the queue-admission reserve:
// with QueueSize=8 the reserve is max(1, 8/8)=1, so ordinary events fill at most
// QueueSize-reserve=7 slots while WARN/ERROR may consume the reserved slot. The
// blocking human sink parks the single consumer so the queue state is
// deterministic without timing.
func TestRecorderQueueReservesDiagnosticSlots(t *testing.T) {
	root := t.TempDir()
	entered := make(chan struct{})
	release := make(chan struct{})
	sink := func(Event) {
		select {
		case <-entered:
		default:
			close(entered)
		}
		<-release
	}
	recorder, err := newRecorderWithBudget(root, Settings{Mode: ModeBasic, RetentionDays: 7, QueueSize: 8}, sink, testBudget(root, eventReserveBytes+1<<20, 1<<20))
	if err != nil {
		t.Fatalf("newRecorderWithBudget: %v", err)
	}
	// Consume one event so the sink blocks the consumer and the queue is empty.
	if !recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "info_0"}) {
		t.Fatal("priming info event was not accepted")
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("human sink was not entered")
	}
	// Ordinary events fill exactly QueueSize-reserve=7 slots.
	for index := 0; index < 7; index++ {
		if !recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "info_fill"}) {
			t.Fatalf("ordinary event %d was rejected before the normal cap was reached", index)
		}
	}
	// The normal cap is reached: another ordinary event is rejected although the
	// diagnostic reserve slot is still physically free.
	if recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "info_overflow"}) {
		t.Fatal("ordinary event was accepted while the diagnostic reserve was held")
	}
	// A WARN may consume the reserved slot.
	if !recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "warn_reserved", SemanticOutcome: OutcomeDegraded}) {
		t.Fatal("WARN was rejected although the reserved slot was free")
	}
	// The queue is physically full now, so even a diagnostic is queue-rejected.
	if recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "error_overflow", SemanticOutcome: OutcomeFailed}) {
		t.Fatal("ERROR was accepted while the queue was physically full")
	}
	status := recorder.Status()
	if status.DroppedEvents != 2 {
		t.Fatalf("DroppedEvents = %d, want 2 (total queue drops)", status.DroppedEvents)
	}
	if status.DiagnosticDropped != 1 || !status.DiagnosticDegraded || status.DiagnosticLastError != "diagnostic_queue_full" {
		t.Fatalf("diagnostic queue drop not reported: %+v", status)
	}
	close(release)
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	drained := recorder.Status()
	if drained.DiagnosticDropped != 1 || !drained.DiagnosticDegraded || drained.DiagnosticLastError != "diagnostic_queue_full" {
		t.Fatalf("queue drop status was erased after drain/Close: %+v", drained)
	}
	retained := readAllDiagnosticEvents(t, root)
	if len(retained) != 1 || retained[0].Event != "warn_reserved" {
		t.Fatalf("diagnostics = %+v, want only the reserved WARN", retained)
	}
}

// TestRecorderQueueReserveClampsWithTinyQueue pins the QueueSize=1 edge: the
// reserve clamps to at least one slot, leaving no ordinary capacity, so an
// ordinary event is rejected while a WARN still enters the reserved slot.
func TestRecorderQueueReserveClampsWithTinyQueue(t *testing.T) {
	root := t.TempDir()
	recorder, err := newRecorderWithBudget(root, Settings{Mode: ModeBasic, RetentionDays: 7, QueueSize: 1}, nil, testBudget(root, 1<<20, 1<<20))
	if err != nil {
		t.Fatalf("newRecorderWithBudget: %v", err)
	}
	if recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "info_event"}) {
		t.Fatal("ordinary event was accepted with QueueSize=1 and a full reserve")
	}
	if !recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "warn_event", SemanticOutcome: OutcomeDegraded}) {
		t.Fatal("WARN was rejected although the reserved slot was free")
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	status := recorder.Status()
	if status.DroppedEvents != 1 {
		t.Fatalf("DroppedEvents = %d, want 1", status.DroppedEvents)
	}
	if status.DiagnosticDropped != 0 {
		t.Fatalf("DiagnosticDropped = %d, want 0", status.DiagnosticDropped)
	}
	retained := readAllDiagnosticEvents(t, root)
	if len(retained) != 1 || retained[0].Event != "warn_event" {
		t.Fatalf("diagnostics = %+v, want the retained WARN", retained)
	}
}

// TestRecorderQueueReserveDisabledInOffMode proves the reserve is not applied
// when structured capture is off, so admission keeps the pre-change shared
// queue behavior: with QueueSize=1 an ordinary event is still admitted.
func TestRecorderQueueReserveDisabledInOffMode(t *testing.T) {
	root := t.TempDir()
	recorder, err := newRecorderWithBudget(root, Settings{Mode: ModeOff, RetentionDays: 7, QueueSize: 1}, nil, testBudget(root, 1<<20, 1<<20))
	if err != nil {
		t.Fatalf("newRecorderWithBudget: %v", err)
	}
	if !recorder.RecordEvent(context.Background(), Event{Layer: "backend", Event: "info_event"}) {
		t.Fatal("ordinary event was rejected in off mode although no reserve should apply")
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}
