package observability

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func appDirSize(t *testing.T, root string) int64 {
	t.Helper()
	size, err := directorySize(filepath.Join(root, appLogDirname))
	if err != nil {
		t.Fatalf("app dir size: %v", err)
	}
	return size
}

// TestAppLogSinkSharesNormalBudgetWithTrace proves an app log byte and a trace
// byte are admitted from the same normal partition, that an oversized app write
// is rejected without landing on disk, and that the diagnostics reserve is not
// consumed by normal pressure.
func TestAppLogSinkSharesNormalBudgetWithTrace(t *testing.T) {
	root := t.TempDir()
	// The eventReserveBytes metadata headroom is part of the normal limit, so
	// the data room is the 4096 bytes on top of it.
	const normalLimit = eventReserveBytes + 4096
	budget := testBudget(root, normalLimit, 1<<20)
	controller, err := newControllerWithBudget(root, Settings{Mode: ModeBasic, RetentionDays: 7}, nil, budget)
	if err != nil {
		t.Fatalf("newControllerWithBudget: %v", err)
	}
	// newControllerWithBudget derives the limits from settings; pin them for a
	// deterministic white-box assertion.
	budget.mu.Lock()
	budget.normalLimit = normalLimit
	budget.normalUsage = 0
	budget.usageKnown = true
	budget.mu.Unlock()

	oversized := bytes.Repeat([]byte("A"), int(normalLimit)+1024)
	if written, err := controller.WriteAppLog(oversized); err != nil || written != len(oversized) {
		t.Fatalf("WriteAppLog oversized = (%d, %v), want a silent drop", written, err)
	}
	if size := appDirSize(t, root); size != 0 {
		t.Fatalf("rejected app write retained %d bytes", size)
	}
	status := controller.Status()
	if !status.AppLogDegraded || status.AppLogDropped != 1 || status.AppLogLastError != "app_log_quota_exceeded" || !status.QuotaBlocked {
		t.Fatalf("app log quota status = %+v", status)
	}

	appPayload := bytes.Repeat([]byte("B"), 3000)
	if written, err := controller.WriteAppLog(appPayload); err != nil || written != len(appPayload) {
		t.Fatalf("WriteAppLog = (%d, %v), want %d bytes", written, err, len(appPayload))
	}
	if size := appDirSize(t, root); size == 0 {
		t.Fatal("accepted app write left no bytes on disk")
	}

	// The trace writer starts from the usage the app write already consumed, so
	// a large trace event is rejected.
	recorder := controller.recorder
	if recorder == nil {
		t.Fatal("controller has no recorder")
	}
	if !recorder.RecordEvent(context.Background(), Event{
		Layer: "backend", Event: "info_event",
		Fields: map[string]any{"detail": strings.Repeat("x", 2048)},
	}) {
		t.Fatal("Record must still accept the event for the caller")
	}
	traceStatus := waitForStatus(t, recorder, func(status Status) bool { return status.QuotaBlocked })
	if !traceStatus.Enabled {
		t.Fatalf("trace quota block disabled the recorder: %+v", traceStatus)
	}

	// Diagnostics keeps its own reserve and still persists WARN/ERROR events.
	recorder.RecordEvent(context.Background(), Event{
		Layer: "backend", Event: "error_event",
		ErrorCategory: "provider_error", SemanticOutcome: OutcomeFailed,
	})
	if err := controller.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if shards := diagnosticShards(t, root); len(shards) == 0 {
		t.Fatal("normal-partition pressure consumed the diagnostics reserve")
	}
}

// TestDiagnosticSinksShareReserveAcrossWriters models the brief overlap of an
// old and a new diagnostic writer during Reconfigure: both must keep their
// combined footprint inside D through the shared budget lock.
func TestDiagnosticSinksShareReserveAcrossWriters(t *testing.T) {
	root := t.TempDir()
	const reserve = int64(4096)
	budget := testBudget(root, 1<<20, reserve)
	first, err := openDiagnosticSink(root, budget, 0, reserve)
	if err != nil {
		t.Fatalf("open first diagnostic sink: %v", err)
	}
	second, err := openDiagnosticSink(root, budget, 0, reserve)
	if err != nil {
		t.Fatalf("open second diagnostic sink: %v", err)
	}
	var wg sync.WaitGroup
	for _, sink := range []*diagnosticSink{first, second} {
		wg.Add(1)
		go func(sink *diagnosticSink) {
			defer wg.Done()
			for index := 0; index < 30; index++ {
				sink.write(Event{
					Layer:    "backend",
					Event:    "warn_event",
					Severity: SeverityWarning,
					Fields:   map[string]any{"detail": strings.Repeat("x", 256)},
				})
			}
		}(sink)
	}
	wg.Wait()
	total, err := directorySize(filepath.Join(root, diagnosticsDirname))
	if err != nil {
		t.Fatalf("diagnostics size: %v", err)
	}
	if total > reserve+4096 {
		t.Fatalf("concurrent diagnostic writers exceeded the reserve: total=%d reserve=%d", total, reserve)
	}
}

// TestDiagnosticPieceIsQuarterOfReserve pins the shard-size rule: a single
// diagnostics shard must be at most min(10MiB, D/4).
func TestDiagnosticPieceIsQuarterOfReserve(t *testing.T) {
	if got := diagnosticPieceBytes(4096); got != 1024 {
		t.Fatalf("piece for 4096 = %d, want 1024", got)
	}
	if got := diagnosticPieceBytes(1 << 40); got != 10<<20 {
		t.Fatalf("piece for large reserve = %d, want 10MiB", got)
	}
	if got := diagnosticPieceBytes(1); got != 1 {
		t.Fatalf("piece for 1 = %d, want 1", got)
	}
	if got := diagnosticPieceBytes(0); got != 0 {
		t.Fatalf("piece for 0 = %d, want 0", got)
	}
}
