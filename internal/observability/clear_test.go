package observability

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestClearStoredLogsRemovesManagedContentAndKeepsUnknown(t *testing.T) {
	root := t.TempDir()
	tracesRoot := filepath.Join(root, tracesDirname)
	if err := ensurePrivateDir(tracesRoot); err != nil {
		t.Fatalf("create traces root: %v", err)
	}
	now := time.Now().UTC()

	knownPath := writeTestSession(t, tracesRoot, "known-session", Manifest{
		SchemaVersion: SchemaVersion,
		AppSessionID:  "known-session",
		Mode:          ModeBasic,
		Status:        "open",
		StartedAt:     now,
	})
	if err := os.WriteFile(filepath.Join(knownPath, eventsFilename), []byte("live\n"), 0o600); err != nil {
		t.Fatalf("write known events: %v", err)
	}

	futurePath := writeTestSession(t, tracesRoot, "future-session", Manifest{
		SchemaVersion: SchemaVersion + 1,
		AppSessionID:  "future-session",
		Mode:          ModeBasic,
		Status:        "closed",
		StartedAt:     now,
	})

	managedApp := filepath.Join(root, appLogDirname, "app-20260101T000000.000000000Z-000001.log")
	unmanagedApp := filepath.Join(root, appLogDirname, "app.log")
	writeAgedFile(t, managedApp, []byte("managed app\n"), now)
	writeAgedFile(t, unmanagedApp, []byte("unmanaged app\n"), now)

	managedDiag := filepath.Join(root, diagnosticsDirname, "diagnostics-20260101T000000.000000000Z-000001.jsonl")
	unmanagedDiag := filepath.Join(root, diagnosticsDirname, "notes.txt")
	writeAgedFile(t, managedDiag, []byte("managed diag\n"), now)
	writeAgedFile(t, unmanagedDiag, []byte("unmanaged diag\n"), now)

	configPath := filepath.Join(root, "project.key")
	if err := os.WriteFile(configPath, []byte("config\n"), 0o600); err != nil {
		t.Fatalf("write unmanaged root file: %v", err)
	}

	result, err := ClearStoredLogs(root)
	if err != nil {
		t.Fatalf("ClearStoredLogs() error = %v", err)
	}
	if result.RemovedSessions != 1 || result.RemovedShards != 2 {
		t.Fatalf("unexpected clear result: %+v", result)
	}
	if result.FreedBytes <= 0 {
		t.Fatalf("FreedBytes = %d, want > 0", result.FreedBytes)
	}
	if result.FailedItems != 0 {
		t.Fatalf("FailedItems = %d, want 0", result.FailedItems)
	}

	mustMissing(t, knownPath)
	mustMissing(t, managedApp)
	mustMissing(t, managedDiag)
	mustStat(t, futurePath)
	mustStat(t, unmanagedApp)
	mustStat(t, unmanagedDiag)
	mustStat(t, configPath)
}

func TestControllerClearAllLogsRemovesActiveSessionAndReopens(t *testing.T) {
	root := t.TempDir()
	controller, err := NewController(root, Settings{Mode: ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	defer controller.Close()

	controller.RecordEvent(context.Background(), Event{Layer: "test", Event: "before-clear", Severity: SeverityInfo})
	if _, err := controller.WriteAppLog([]byte("before-clear\n")); err != nil {
		t.Fatalf("WriteAppLog() error = %v", err)
	}
	first := controller.Status()
	if first.SessionPath == "" {
		t.Fatalf("expected an active session before clear: %+v", first)
	}

	result, err := controller.ClearAllLogs()
	if err != nil {
		t.Fatalf("ClearAllLogs() error = %v", err)
	}
	if result.RemovedSessions != 1 {
		t.Fatalf("RemovedSessions = %d, want 1", result.RemovedSessions)
	}
	if result.RemovedShards < 1 {
		t.Fatalf("RemovedShards = %d, want >= 1", result.RemovedShards)
	}
	if !result.Collecting || result.NewSessionID == "" || result.NewSessionID == first.SessionID {
		t.Fatalf("expected a fresh collector session: first=%+v result=%+v", first, result)
	}
	mustMissing(t, first.SessionPath)
	mustStat(t, result.NewSessionPath)

	status := controller.Status()
	if status.SessionID != result.NewSessionID || !status.Enabled {
		t.Fatalf("status after clear = %+v, want session %s", status, result.NewSessionID)
	}

	if _, err := controller.WriteAppLog([]byte("after-clear\n")); err != nil {
		t.Fatalf("WriteAppLog() after clear error = %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(root, appLogDirname))
	if err != nil {
		t.Fatalf("read app log dir: %v", err)
	}
	managed := 0
	for _, entry := range entries {
		if !entry.IsDir() && isAppShardName(entry.Name()) {
			managed++
		}
	}
	if managed != 1 {
		t.Fatalf("app shards after clear = %d, want 1 (stale shards removed)", managed)
	}
}

func TestControllerClearAllLogsInOffModeKeepsCollectorOff(t *testing.T) {
	root := t.TempDir()
	controller, err := NewController(root, Settings{Mode: ModeOff, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	defer controller.Close()

	if _, err := controller.WriteAppLog([]byte("off-mode\n")); err != nil {
		t.Fatalf("WriteAppLog() error = %v", err)
	}

	result, err := controller.ClearAllLogs()
	if err != nil {
		t.Fatalf("ClearAllLogs() error = %v", err)
	}
	if result.Collecting || result.NewSessionID != "" {
		t.Fatalf("off mode must not reopen a trace session: %+v", result)
	}
	if result.RemovedShards < 1 || result.FreedBytes <= 0 {
		t.Fatalf("expected app shard removal: %+v", result)
	}
	if !controller.Status().AppLogEnabled {
		t.Fatalf("app log sink should be reopened after clear: %+v", controller.Status())
	}
}

func isAppShardName(name string) bool {
	return strings.HasPrefix(name, appLogPrefix+"-") && strings.HasSuffix(name, ".log")
}
