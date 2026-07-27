package workspace

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestOpenCreatesPrivateSQLiteWorkspaceAndCleansUp(t *testing.T) {
	root := t.TempDir()
	ws, err := Open(context.Background(), Options{TempDir: root})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if filepath.Dir(ws.Dir()) != root {
		t.Fatalf("workspace dir %q not under temp root %q", ws.Dir(), root)
	}
	if filepath.Base(ws.Dir())[:len(tempPrefix)] != tempPrefix {
		t.Fatalf("workspace dir %q missing prefix %q", ws.Dir(), tempPrefix)
	}
	assertMode(t, ws.Dir(), 0o700)
	assertMode(t, ws.DBPath(), 0o600)

	checks := map[string]string{
		"PRAGMA journal_mode": "delete",
		"PRAGMA foreign_keys": "1",
		"PRAGMA temp_store":   "1",
		"PRAGMA mmap_size":    "0",
		"PRAGMA cache_size":   "-8192",
	}
	for query, want := range checks {
		var got string
		if err := ws.db.QueryRowContext(context.Background(), query).Scan(&got); err != nil {
			t.Fatalf("%s scan error = %v", query, err)
		}
		if got != want {
			t.Fatalf("%s = %q, want %q", query, got, want)
		}
	}

	if err := ws.CloseAndRemove(); err != nil {
		t.Fatalf("CloseAndRemove() error = %v", err)
	}
	if _, err := os.Stat(ws.Dir()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("workspace dir still exists or unexpected stat error: %v", err)
	}
	if err := ws.CloseAndRemove(); err != nil {
		t.Fatalf("CloseAndRemove() second call error = %v", err)
	}
}

func TestDatasetInputFileAndEventSchema(t *testing.T) {
	ws, err := Open(context.Background(), Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer func() {
		if err := ws.CloseAndRemove(); err != nil {
			t.Fatalf("CloseAndRemove() error = %v", err)
		}
	}()

	currentID, err := ws.DatasetID(context.Background(), DatasetCurrent)
	if err != nil {
		t.Fatalf("DatasetID(current) error = %v", err)
	}
	baselineID, err := ws.DatasetID(context.Background(), DatasetBaseline)
	if err != nil {
		t.Fatalf("DatasetID(baseline) error = %v", err)
	}
	if currentID == baselineID {
		t.Fatal("current and baseline dataset IDs are equal")
	}

	argumentID, err := ws.InsertInputArgument(context.Background(), currentID, 0, "/tmp/input")
	if err != nil {
		t.Fatalf("InsertInputArgument() error = %v", err)
	}
	fileID, inserted, err := ws.UpsertInputFile(context.Background(), currentID, argumentID, "/tmp/input/session/events.jsonl", FileEvents)
	if err != nil {
		t.Fatalf("UpsertInputFile() error = %v", err)
	}
	if !inserted {
		t.Fatal("first input file upsert did not insert")
	}
	duplicateID, inserted, err := ws.UpsertInputFile(context.Background(), currentID, argumentID, "/tmp/input/session/events.jsonl", FileEvents)
	if err != nil {
		t.Fatalf("duplicate UpsertInputFile() error = %v", err)
	}
	if inserted || duplicateID != fileID {
		t.Fatalf("duplicate upsert = id %d inserted %v, want id %d inserted false", duplicateID, inserted, fileID)
	}

	when := time.Date(2026, 3, 14, 0, 0, 0, 123, time.UTC)
	records := []EventRecord{
		{DatasetID: currentID, SourceFileID: fileID, LineNumber: 1, Timestamp: when, Sequence: ^uint64(0), IngestOrder: 2, TraceKey: "trace-b", SchemaVersion: 1, AppSessionID: "session", TraceID: "trace-b", Layer: "backend", Event: "request_started", SafeFieldsJSON: `{"method":"POST"}`},
		{DatasetID: currentID, SourceFileID: fileID, LineNumber: 2, Timestamp: when, Sequence: 2, IngestOrder: 1, TraceKey: "trace-a", SchemaVersion: 1, AppSessionID: "session", TraceID: "trace-a", Layer: "backend", Event: "request_finished", Status: "ok"},
	}
	for _, record := range records {
		if _, err := ws.InsertEvent(context.Background(), record); err != nil {
			t.Fatalf("InsertEvent(%+v) error = %v", record, err)
		}
	}
	count, err := ws.EventCount(context.Background(), currentID)
	if err != nil {
		t.Fatalf("EventCount() error = %v", err)
	}
	if count != int64(len(records)) {
		t.Fatalf("EventCount() = %d, want %d", count, len(records))
	}

	rows, err := ws.db.QueryContext(context.Background(), `
		SELECT sequence_key, trace_key
		FROM events
		WHERE dataset_id = ?
		ORDER BY timestamp_seconds, timestamp_nanoseconds, sequence_key, ingest_order
	`, currentID)
	if err != nil {
		t.Fatalf("query ordered events: %v", err)
	}
	defer rows.Close()
	var ordered []string
	for rows.Next() {
		var sequenceKey string
		var traceKey string
		if err := rows.Scan(&sequenceKey, &traceKey); err != nil {
			t.Fatalf("scan ordered event: %v", err)
		}
		ordered = append(ordered, sequenceKey+":"+traceKey)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("ordered rows error = %v", err)
	}
	want := []string{"00000000000000000002:trace-a", "18446744073709551615:trace-b"}
	if len(ordered) != len(want) {
		t.Fatalf("ordered length = %d, want %d: %#v", len(ordered), len(want), ordered)
	}
	for index := range want {
		if ordered[index] != want[index] {
			t.Fatalf("ordered[%d] = %q, want %q (all=%#v)", index, ordered[index], want[index], ordered)
		}
	}
}

func TestInsertEventRejectsInvalidRecord(t *testing.T) {
	ws, err := Open(context.Background(), Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer ws.CloseAndRemove()
	currentID, err := ws.DatasetID(context.Background(), DatasetCurrent)
	if err != nil {
		t.Fatalf("DatasetID(current) error = %v", err)
	}
	_, err = ws.InsertEvent(context.Background(), EventRecord{DatasetID: currentID, Timestamp: time.Now(), Sequence: 1, IngestOrder: 1, SchemaVersion: 1, Layer: "backend", Event: "request_started"})
	if err == nil {
		t.Fatal("InsertEvent() accepted empty trace key")
	}
}

func TestInsertEventsRollsBackInvalidBatch(t *testing.T) {
	ws, err := Open(context.Background(), Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer ws.CloseAndRemove()
	currentID, err := ws.DatasetID(context.Background(), DatasetCurrent)
	if err != nil {
		t.Fatalf("DatasetID(current) error = %v", err)
	}
	now := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	err = ws.InsertEvents(context.Background(), []EventRecord{
		{DatasetID: currentID, Timestamp: now, Sequence: 1, IngestOrder: 1, TraceKey: "trace-ok", SchemaVersion: 1, AppSessionID: "session", TraceID: "trace-ok", Layer: "backend", Event: "request_started"},
		{DatasetID: currentID, Timestamp: now, Sequence: 2, IngestOrder: 2, SchemaVersion: 1, AppSessionID: "session", Layer: "backend", Event: "request_finished"},
	})
	if err == nil {
		t.Fatal("InsertEvents() accepted invalid batch")
	}
	count, err := ws.EventCount(context.Background(), currentID)
	if err != nil {
		t.Fatalf("EventCount() error = %v", err)
	}
	if count != 0 {
		t.Fatalf("EventCount() after rolled back batch = %d, want 0", count)
	}
	var rows int64
	if err := ws.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM events WHERE dataset_id = ?`, currentID).Scan(&rows); err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if rows != 0 {
		t.Fatalf("stored events after rolled back batch = %d, want 0", rows)
	}
}

func assertMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if runtime.GOOS == "windows" {
		return
	}
	if got := info.Mode().Perm(); got != want {
		t.Fatalf("mode %s = %04o, want %04o", path, got, want)
	}
}

func TestWorkspaceDoesNotExposeRawDBWhenClosed(t *testing.T) {
	ws, err := Open(context.Background(), Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if err := ws.CloseAndRemove(); err != nil {
		t.Fatalf("CloseAndRemove() error = %v", err)
	}
	_, err = ws.DatasetID(context.Background(), DatasetCurrent)
	if !errors.Is(err, sql.ErrConnDone) {
		t.Fatalf("DatasetID() after close error = %v, want sql.ErrConnDone", err)
	}
}
