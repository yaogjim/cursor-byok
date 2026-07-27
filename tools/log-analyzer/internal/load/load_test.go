package load

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"cursor-log-analyzer/internal/workspace"
)

func TestIntoWorkspaceRejectsUnknownSchemaByDefault(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "events.jsonl")
	payload := []byte(`{"schema_version":99,"timestamp":"2026-03-14T00:00:00Z","layer":"backend","event":"request_started"}` + "\n")
	writeFile(t, path, payload)

	ws := openWorkspace(t)
	defer ws.CloseAndRemove()
	if err := IntoWorkspace(context.Background(), ws, workspace.DatasetCurrent, []string{root}, Options{}); err == nil {
		t.Fatal("IntoWorkspace() accepted unknown schema")
	}
	currentID := mustDatasetID(t, ws, workspace.DatasetCurrent)
	stats, err := ws.Stats(context.Background(), currentID)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.EventCount != 0 {
		t.Fatalf("EventCount after rejected schema = %d, want 0", stats.EventCount)
	}

	allowed := openWorkspace(t)
	defer allowed.CloseAndRemove()
	if err := IntoWorkspace(context.Background(), allowed, workspace.DatasetCurrent, []string{root}, Options{AllowUnknownSchema: true}); err != nil {
		t.Fatalf("IntoWorkspace() compatibility mode error = %v", err)
	}
	allowedID := mustDatasetID(t, allowed, workspace.DatasetCurrent)
	stats, err = allowed.Stats(context.Background(), allowedID)
	if err != nil {
		t.Fatalf("Stats() compatibility error = %v", err)
	}
	if stats.EventCount != 1 || stats.WarningCount != 1 {
		t.Fatalf("compatibility stats = %+v, want 1 event and 1 warning", stats)
	}
	warnings := queryStrings(t, allowed.DBPath(), `SELECT message FROM warnings WHERE dataset_id = ? ORDER BY ordinal`, allowedID)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "schema_version=99") {
		t.Fatalf("warnings = %#v", warnings)
	}
}

func TestIntoWorkspaceDiscoversDeduplicatesAndOrdersInputs(t *testing.T) {
	root := t.TempDir()
	sessionA := filepath.Join(root, "session-a")
	sessionB := filepath.Join(root, "session-b")
	if err := os.MkdirAll(sessionA, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(sessionB, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(sessionA, "events.jsonl"), []byte(`{"schema_version":1,"timestamp":"2026-03-14T00:00:00Z","sequence":18446744073709551615,"app_session_id":"app-a","trace_id":"trace-a","layer":"backend","event":"request_started","fields":{"authorization":"secret","method":"POST"}}`+"\n"))
	writeFile(t, filepath.Join(sessionA, "manifest.json"), []byte(`{"schema_version":1,"app_session_id":"app-a","mode":"capture","status":"closed","started_at":"2026-03-14T00:00:00Z"}`))
	writeFile(t, filepath.Join(sessionB, "events.jsonl"), []byte(`{"schema_version":1,"timestamp":"2026-03-14T00:00:00Z","sequence":2,"app_session_id":"app-b","trace_id":"trace-b","layer":"provider","event":"request_finished"}`+"\n"))
	if runtime.GOOS != "windows" {
		_ = os.Symlink(sessionA, filepath.Join(root, "link-to-session-a"))
	}

	ws := openWorkspace(t)
	defer ws.CloseAndRemove()
	if err := IntoWorkspace(context.Background(), ws, workspace.DatasetCurrent, []string{root, sessionA}, Options{}); err != nil {
		t.Fatalf("IntoWorkspace() error = %v", err)
	}
	currentID := mustDatasetID(t, ws, workspace.DatasetCurrent)
	stats, err := ws.Stats(context.Background(), currentID)
	if err != nil {
		t.Fatalf("Stats() error = %v", err)
	}
	if stats.EventCount != 2 || stats.ManifestCount != 1 || stats.WarningCount != 0 {
		t.Fatalf("stats = %+v, want 2 events, 1 manifest, 0 warnings", stats)
	}
	inputFiles := queryInt(t, ws.DBPath(), `SELECT COUNT(*) FROM input_files WHERE dataset_id = ?`, currentID)
	if inputFiles != 3 {
		t.Fatalf("input file count = %d, want 3", inputFiles)
	}
	ordered := queryStrings(t, ws.DBPath(), `
		SELECT sequence_key || ':' || trace_key
		FROM events
		WHERE dataset_id = ?
		ORDER BY timestamp_seconds, timestamp_nanoseconds, sequence_key, ingest_order
	`, currentID)
	wantOrdered := []string{"00000000000000000002:trace-b", "18446744073709551615:trace-a"}
	assertStrings(t, ordered, wantOrdered)
	ingestOrder := queryStrings(t, ws.DBPath(), `
		SELECT CAST(ingest_order AS TEXT) || ':' || trace_key
		FROM events
		WHERE dataset_id = ?
		ORDER BY ingest_order
	`, currentID)
	assertStrings(t, ingestOrder, []string{"1:trace-a", "2:trace-b"})
	safeFields := queryStrings(t, ws.DBPath(), `SELECT safe_fields_json FROM events WHERE dataset_id = ? AND trace_key = 'trace-a'`, currentID)
	assertStrings(t, safeFields, []string{`{"method":"POST"}`})
}

func TestIntoWorkspaceKeepsDatasetsIsolatedAndBatches(t *testing.T) {
	current := t.TempDir()
	baseline := t.TempDir()
	writeFile(t, filepath.Join(current, "events.jsonl"), []byte(
		`{"schema_version":1,"timestamp":"2026-03-14T00:00:00Z","sequence":1,"app_session_id":"current","trace_id":"trace-current-1","layer":"backend","event":"request_started"}`+"\n"+
			`{"schema_version":1,"timestamp":"2026-03-14T00:00:01Z","sequence":2,"app_session_id":"current","trace_id":"trace-current-2","layer":"backend","event":"request_finished"}`+"\n",
	))
	writeFile(t, filepath.Join(baseline, "events.jsonl"), []byte(`{"schema_version":1,"timestamp":"2026-03-14T00:00:00Z","sequence":1,"app_session_id":"baseline","trace_id":"trace-baseline","layer":"backend","event":"request_started"}`+"\n"))

	ws := openWorkspace(t)
	defer ws.CloseAndRemove()
	options := Options{BatchEventLimit: 1, BatchByteLimit: 1}
	if err := IntoWorkspace(context.Background(), ws, workspace.DatasetCurrent, []string{current}, options); err != nil {
		t.Fatalf("IntoWorkspace(current) error = %v", err)
	}
	if err := IntoWorkspace(context.Background(), ws, workspace.DatasetBaseline, []string{baseline}, options); err != nil {
		t.Fatalf("IntoWorkspace(baseline) error = %v", err)
	}
	currentID := mustDatasetID(t, ws, workspace.DatasetCurrent)
	baselineID := mustDatasetID(t, ws, workspace.DatasetBaseline)
	currentStats, err := ws.Stats(context.Background(), currentID)
	if err != nil {
		t.Fatalf("Stats(current) error = %v", err)
	}
	baselineStats, err := ws.Stats(context.Background(), baselineID)
	if err != nil {
		t.Fatalf("Stats(baseline) error = %v", err)
	}
	if currentStats.EventCount != 2 || baselineStats.EventCount != 1 {
		t.Fatalf("current stats = %+v baseline stats = %+v", currentStats, baselineStats)
	}
}

func TestIntoWorkspaceRejectsOversizedEventLine(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "events.jsonl"), []byte(`{"schema_version":1,"timestamp":"2026-03-14T00:00:00Z","sequence":1,"app_session_id":"app","trace_id":"trace","layer":"backend","event":"request_started"}`+"\n"))
	ws := openWorkspace(t)
	defer ws.CloseAndRemove()
	err := IntoWorkspace(context.Background(), ws, workspace.DatasetCurrent, []string{root}, Options{MaxEventLineBytes: 32})
	if err == nil || !strings.Contains(err.Error(), "line exceeds") {
		t.Fatalf("IntoWorkspace() oversized error = %v", err)
	}
	currentID := mustDatasetID(t, ws, workspace.DatasetCurrent)
	count, countErr := ws.EventCount(context.Background(), currentID)
	if countErr != nil {
		t.Fatalf("EventCount() error = %v", countErr)
	}
	if count != 0 {
		t.Fatalf("EventCount after oversized line = %d, want 0", count)
	}
}

func openWorkspace(t *testing.T) *workspace.Workspace {
	t.Helper()
	ws, err := workspace.Open(context.Background(), workspace.Options{TempDir: t.TempDir()})
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	return ws
}

func mustDatasetID(t *testing.T, ws *workspace.Workspace, kind workspace.DatasetKind) int64 {
	t.Helper()
	id, err := ws.DatasetID(context.Background(), kind)
	if err != nil {
		t.Fatalf("DatasetID(%s) error = %v", kind, err)
	}
	return id
}

func writeFile(t *testing.T, path string, payload []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
}

func queryStrings(t *testing.T, dbPath string, query string, args ...any) []string {
	t.Helper()
	db := openSQLite(t, dbPath)
	defer db.Close()
	rows, err := db.Query(query, args...)
	if err != nil {
		t.Fatalf("query strings: %v", err)
	}
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatalf("scan string: %v", err)
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("rows error: %v", err)
	}
	return result
}

func queryInt(t *testing.T, dbPath string, query string, args ...any) int64 {
	t.Helper()
	db := openSQLite(t, dbPath)
	defer db.Close()
	var result int64
	if err := db.QueryRow(query, args...).Scan(&result); err != nil {
		t.Fatalf("query int: %v", err)
	}
	return result
}

func openSQLite(t *testing.T, dbPath string) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func assertStrings(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d strings, want %d: %#v", len(got), len(want), got)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("got[%d] = %q, want %q (all=%#v)", index, got[index], want[index], got)
		}
	}
}
