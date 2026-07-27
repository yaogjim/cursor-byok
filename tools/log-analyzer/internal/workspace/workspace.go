package workspace

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const tempPrefix = "cursor-log-analyzer-"
const databaseName = "workspace.sqlite"

const (
	DatasetCurrent  DatasetKind = "current"
	DatasetBaseline DatasetKind = "baseline"
)

const (
	FileEvents   FileKind = "events"
	FileManifest FileKind = "manifest"
)

type DatasetKind string

type FileKind string

type Options struct {
	TempDir string
}

type Workspace struct {
	mu     sync.Mutex
	dir    string
	dbPath string
	db     *sql.DB
	closed bool
}

type EventRecord struct {
	DatasetID       int64
	SourceFileID    int64
	LineNumber      int
	Timestamp       time.Time
	Sequence        uint64
	IngestOrder     int64
	TraceKey        string
	SchemaVersion   int
	AppSessionID    string
	TraceID         string
	SpanID          string
	ParentSpanID    string
	HTTPRequestID   string
	CursorRequestID string
	ConversationID  string
	ModelCallID     string
	ToolCallID      string
	Layer           string
	Event           string
	Route           string
	ExecutionTarget string
	Protocol        string
	Status          string
	ErrorCategory   string
	DurationMS      int64
	RequestBytes    int64
	ResponseBytes   int64
	DecodeError     bool
	DroppedEvents   uint64
	SafeFieldsJSON  string
}

type ManifestRecord struct {
	DatasetID       int64
	InputFileID     int64
	SchemaVersion   int
	AppSessionID    string
	Mode            string
	Status          string
	StartedAt       time.Time
	ClosedAt        *time.Time
	PayloadDegraded bool
	DroppedEvents   uint64
	LastError       string
}

type WarningRecord struct {
	DatasetID int64
	Ordinal   int
	Message   string
}

type DatasetStats struct {
	EventCount    int64
	ManifestCount int64
	WarningCount  int64
}

type EventCursor struct {
	TraceKey             string
	TimestampSeconds     int64
	TimestampNanoseconds int
	SequenceKey          string
	IngestOrder          int64
}

type EventRow struct {
	EventRecord
	Cursor EventCursor
}

type TraceSummaryRecord struct {
	DatasetID        int64
	TraceKey         string
	EventCount       int
	StartedAt        string
	FinishedAt       string
	DurationMS       int64
	HasError         bool
	FirstIngestOrder int64
	LastIngestOrder  int64
	Layers           []string
	Targets          []string
}

type TargetSummaryRecord struct {
	DatasetID       int64
	Target          string
	Events          int
	Finished        int
	Errors          int
	DurationTotalMS int64
	RequestBytes    int64
	ResponseBytes   int64
}

type FindingRecord struct {
	DatasetID        int64
	Severity         string
	SeverityRank     int
	Code             string
	Message          string
	TraceKey         string
	Count            int
	FirstIngestOrder int64
}

type ComparisonRecord struct {
	Target                 string
	CurrentFinished        int
	BaselineFinished       int
	ErrorRateDelta         float64
	AverageDurationDeltaMS float64
}

type PairStateRecord struct {
	Key      string
	Starts   int
	Finishes int
}

func Open(ctx context.Context, options Options) (*Workspace, error) {
	root := strings.TrimSpace(options.TempDir)
	if root == "" {
		root = os.TempDir()
	}
	dir, err := os.MkdirTemp(root, tempPrefix)
	if err != nil {
		return nil, err
	}
	workspace := &Workspace{dir: dir, dbPath: filepath.Join(dir, databaseName)}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	file, err := os.OpenFile(workspace.dbPath, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := file.Close(); err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	db, err := sql.Open("sqlite", workspace.dbPath)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	workspace.db = db
	if err := workspace.initialize(ctx); err != nil {
		return nil, errors.Join(err, workspace.CloseAndRemove())
	}
	return workspace, nil
}

func (workspace *Workspace) Dir() string {
	return workspace.dir
}

func (workspace *Workspace) DBPath() string {
	return workspace.dbPath
}

func (workspace *Workspace) CloseAndRemove() error {
	workspace.mu.Lock()
	db := workspace.db
	dir := workspace.dir
	workspace.db = nil
	workspace.closed = true
	workspace.mu.Unlock()
	var result error
	if db != nil {
		result = errors.Join(result, db.Close())
	}
	if dir != "" {
		result = errors.Join(result, os.RemoveAll(dir))
	}
	return result
}

func (workspace *Workspace) DatasetID(ctx context.Context, kind DatasetKind) (int64, error) {
	db, err := workspace.database()
	if err != nil {
		return 0, err
	}
	var id int64
	if err := db.QueryRowContext(ctx, `SELECT id FROM datasets WHERE kind = ?`, string(kind)).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (workspace *Workspace) InsertInputArgument(ctx context.Context, datasetID int64, ordinal int, path string) (int64, error) {
	db, err := workspace.database()
	if err != nil {
		return 0, err
	}
	result, err := db.ExecContext(ctx, `
		INSERT INTO input_arguments(dataset_id, ordinal, path)
		VALUES (?, ?, ?)
	`, datasetID, ordinal, strings.TrimSpace(path))
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (workspace *Workspace) UpsertInputFile(ctx context.Context, datasetID int64, argumentID int64, canonicalPath string, kind FileKind) (int64, bool, error) {
	db, err := workspace.database()
	if err != nil {
		return 0, false, err
	}
	canonicalPath = strings.TrimSpace(canonicalPath)
	result, err := db.ExecContext(ctx, `
		INSERT OR IGNORE INTO input_files(dataset_id, argument_id, canonical_path, file_type, first_argument_ordinal)
		VALUES (?, ?, ?, ?, (SELECT ordinal FROM input_arguments WHERE id = ?))
	`, datasetID, argumentID, canonicalPath, string(kind), argumentID)
	if err != nil {
		return 0, false, err
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, false, err
	}
	var id int64
	if err := db.QueryRowContext(ctx, `
		SELECT id FROM input_files WHERE dataset_id = ? AND canonical_path = ?
	`, datasetID, canonicalPath).Scan(&id); err != nil {
		return 0, false, err
	}
	return id, affected > 0, nil
}

func (workspace *Workspace) InsertManifest(ctx context.Context, record ManifestRecord) (int64, error) {
	db, err := workspace.database()
	if err != nil {
		return 0, err
	}
	startedAt := record.StartedAt.UTC().Format(time.RFC3339Nano)
	var closedAt any
	if record.ClosedAt != nil {
		closedAt = record.ClosedAt.UTC().Format(time.RFC3339Nano)
	}
	result, err := db.ExecContext(ctx, `
		INSERT INTO manifests(
			dataset_id, input_file_id, schema_version, app_session_id, mode, status,
			started_at, closed_at, payload_degraded, dropped_events_key, last_error
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		record.DatasetID, nullableID(record.InputFileID), record.SchemaVersion,
		record.AppSessionID, record.Mode, record.Status, startedAt, closedAt,
		boolInt(record.PayloadDegraded), SequenceKey(record.DroppedEvents), nullEmpty(record.LastError),
	)
	if err != nil {
		return 0, err
	}
	if _, err := db.ExecContext(ctx, `UPDATE datasets SET manifest_count = manifest_count + 1 WHERE id = ?`, record.DatasetID); err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (workspace *Workspace) InsertWarning(ctx context.Context, record WarningRecord) (int64, error) {
	if strings.TrimSpace(record.Message) == "" {
		return 0, errors.New("warning message is required")
	}
	db, err := workspace.database()
	if err != nil {
		return 0, err
	}
	result, err := db.ExecContext(ctx, `
		INSERT INTO warnings(dataset_id, ordinal, message)
		VALUES (?, ?, ?)
	`, record.DatasetID, record.Ordinal, strings.TrimSpace(record.Message))
	if err != nil {
		return 0, err
	}
	if _, err := db.ExecContext(ctx, `UPDATE datasets SET warning_count = warning_count + 1 WHERE id = ?`, record.DatasetID); err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (workspace *Workspace) InsertEvent(ctx context.Context, record EventRecord) (int64, error) {
	if err := validateEventRecord(record); err != nil {
		return 0, err
	}
	db, err := workspace.database()
	if err != nil {
		return 0, err
	}
	result, err := db.ExecContext(ctx, insertEventSQL, eventValues(record)...)
	if err != nil {
		return 0, err
	}
	if _, err := db.ExecContext(ctx, `UPDATE datasets SET event_count = event_count + 1 WHERE id = ?`, record.DatasetID); err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

func (workspace *Workspace) InsertEvents(ctx context.Context, records []EventRecord) error {
	if len(records) == 0 {
		return nil
	}
	db, err := workspace.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	statement, err := tx.PrepareContext(ctx, insertEventSQL)
	if err != nil {
		return err
	}
	defer statement.Close()
	counts := make(map[int64]int64)
	for _, record := range records {
		if err := validateEventRecord(record); err != nil {
			return err
		}
		if _, err := statement.ExecContext(ctx, eventValues(record)...); err != nil {
			return err
		}
		counts[record.DatasetID]++
	}
	if err := statement.Close(); err != nil {
		return err
	}
	for datasetID, count := range counts {
		if _, err := tx.ExecContext(ctx, `UPDATE datasets SET event_count = event_count + ? WHERE id = ?`, count, datasetID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (workspace *Workspace) EventCount(ctx context.Context, datasetID int64) (int64, error) {
	db, err := workspace.database()
	if err != nil {
		return 0, err
	}
	var count int64
	if err := db.QueryRowContext(ctx, `SELECT event_count FROM datasets WHERE id = ?`, datasetID).Scan(&count); err != nil {
		return 0, err
	}
	return count, nil
}

func (workspace *Workspace) Stats(ctx context.Context, datasetID int64) (DatasetStats, error) {
	db, err := workspace.database()
	if err != nil {
		return DatasetStats{}, err
	}
	var stats DatasetStats
	if err := db.QueryRowContext(ctx, `
		SELECT event_count, manifest_count, warning_count
		FROM datasets
		WHERE id = ?
	`, datasetID).Scan(&stats.EventCount, &stats.ManifestCount, &stats.WarningCount); err != nil {
		return DatasetStats{}, err
	}
	return stats, nil
}

func (workspace *Workspace) ClearAnalysis(ctx context.Context, datasetID int64) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	statements := []string{
		`DELETE FROM trace_summaries WHERE dataset_id = ?`,
		`DELETE FROM trace_layers WHERE dataset_id = ?`,
		`DELETE FROM trace_targets WHERE dataset_id = ?`,
		`DELETE FROM target_summaries WHERE dataset_id = ?`,
		`DELETE FROM findings WHERE dataset_id = ?`,
		`DELETE FROM trace_pair_state WHERE dataset_id = ?`,
		`DELETE FROM trace_tool_state WHERE dataset_id = ?`,
	}
	for _, statement := range statements {
		if _, err := db.ExecContext(ctx, statement, datasetID); err != nil {
			return err
		}
	}
	return nil
}

func (workspace *Workspace) ClearComparisons(ctx context.Context) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM comparisons`)
	return err
}

func (workspace *Workspace) ListTraceEvents(ctx context.Context, datasetID int64, after *EventCursor, limit int) ([]EventRow, error) {
	if limit <= 0 {
		limit = 2048
	}
	query := `
		SELECT source_file_id, line_number, timestamp_seconds, timestamp_nanoseconds, timestamp_text,
			sequence_key, ingest_order, trace_key, schema_version, app_session_id, trace_id, span_id,
			parent_span_id, http_request_id, cursor_request_id, conversation_id, model_call_id, tool_call_id,
			layer, event, route, execution_target, protocol, status, error_category, duration_ms,
			request_bytes, response_bytes, decode_error, dropped_events_key, safe_fields_json
		FROM events
		WHERE dataset_id = ?
		ORDER BY trace_key, timestamp_seconds, timestamp_nanoseconds, sequence_key, ingest_order
		LIMIT ?
	`
	args := []any{datasetID, limit}
	if after != nil {
		query = `
			SELECT source_file_id, line_number, timestamp_seconds, timestamp_nanoseconds, timestamp_text,
				sequence_key, ingest_order, trace_key, schema_version, app_session_id, trace_id, span_id,
				parent_span_id, http_request_id, cursor_request_id, conversation_id, model_call_id, tool_call_id,
				layer, event, route, execution_target, protocol, status, error_category, duration_ms,
				request_bytes, response_bytes, decode_error, dropped_events_key, safe_fields_json
			FROM events
			WHERE dataset_id = ? AND (
				trace_key > ? OR
				(trace_key = ? AND timestamp_seconds > ?) OR
				(trace_key = ? AND timestamp_seconds = ? AND timestamp_nanoseconds > ?) OR
				(trace_key = ? AND timestamp_seconds = ? AND timestamp_nanoseconds = ? AND sequence_key > ?) OR
				(trace_key = ? AND timestamp_seconds = ? AND timestamp_nanoseconds = ? AND sequence_key = ? AND ingest_order > ?)
			)
			ORDER BY trace_key, timestamp_seconds, timestamp_nanoseconds, sequence_key, ingest_order
			LIMIT ?
		`
		args = []any{
			datasetID,
			after.TraceKey,
			after.TraceKey, after.TimestampSeconds,
			after.TraceKey, after.TimestampSeconds, after.TimestampNanoseconds,
			after.TraceKey, after.TimestampSeconds, after.TimestampNanoseconds, after.SequenceKey,
			after.TraceKey, after.TimestampSeconds, after.TimestampNanoseconds, after.SequenceKey, after.IngestOrder,
			limit,
		}
	}
	return workspace.listEvents(ctx, query, args...)
}

func (workspace *Workspace) ListGlobalEvents(ctx context.Context, datasetID int64, after *EventCursor, limit int) ([]EventRow, error) {
	if limit <= 0 {
		limit = 2048
	}
	query := `
		SELECT source_file_id, line_number, timestamp_seconds, timestamp_nanoseconds, timestamp_text,
			sequence_key, ingest_order, trace_key, schema_version, app_session_id, trace_id, span_id,
			parent_span_id, http_request_id, cursor_request_id, conversation_id, model_call_id, tool_call_id,
			layer, event, route, execution_target, protocol, status, error_category, duration_ms,
			request_bytes, response_bytes, decode_error, dropped_events_key, safe_fields_json
		FROM events
		WHERE dataset_id = ?
		ORDER BY timestamp_seconds, timestamp_nanoseconds, sequence_key, ingest_order
		LIMIT ?
	`
	args := []any{datasetID, limit}
	if after != nil {
		query = `
			SELECT source_file_id, line_number, timestamp_seconds, timestamp_nanoseconds, timestamp_text,
				sequence_key, ingest_order, trace_key, schema_version, app_session_id, trace_id, span_id,
				parent_span_id, http_request_id, cursor_request_id, conversation_id, model_call_id, tool_call_id,
				layer, event, route, execution_target, protocol, status, error_category, duration_ms,
				request_bytes, response_bytes, decode_error, dropped_events_key, safe_fields_json
			FROM events
			WHERE dataset_id = ? AND (
				timestamp_seconds > ? OR
				(timestamp_seconds = ? AND timestamp_nanoseconds > ?) OR
				(timestamp_seconds = ? AND timestamp_nanoseconds = ? AND sequence_key > ?) OR
				(timestamp_seconds = ? AND timestamp_nanoseconds = ? AND sequence_key = ? AND ingest_order > ?)
			)
			ORDER BY timestamp_seconds, timestamp_nanoseconds, sequence_key, ingest_order
			LIMIT ?
		`
		args = []any{
			datasetID,
			after.TimestampSeconds,
			after.TimestampSeconds, after.TimestampNanoseconds,
			after.TimestampSeconds, after.TimestampNanoseconds, after.SequenceKey,
			after.TimestampSeconds, after.TimestampNanoseconds, after.SequenceKey, after.IngestOrder,
			limit,
		}
	}
	return workspace.listEvents(ctx, query, args...)
}

func (workspace *Workspace) InsertTraceSummary(ctx context.Context, record TraceSummaryRecord) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO trace_summaries(dataset_id, trace_key, event_count, started_at, finished_at, duration_ms, has_error, first_ingest_order, last_ingest_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dataset_id, trace_key) DO UPDATE SET
			event_count = excluded.event_count,
			started_at = excluded.started_at,
			finished_at = excluded.finished_at,
			duration_ms = excluded.duration_ms,
			has_error = excluded.has_error,
			first_ingest_order = excluded.first_ingest_order,
			last_ingest_order = excluded.last_ingest_order
	`, record.DatasetID, record.TraceKey, record.EventCount, nullEmpty(record.StartedAt), nullEmpty(record.FinishedAt), record.DurationMS, boolInt(record.HasError), record.FirstIngestOrder, record.LastIngestOrder)
	if err != nil {
		return err
	}
	return nil
}

func (workspace *Workspace) InsertTargetSummary(ctx context.Context, record TargetSummaryRecord) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO target_summaries(dataset_id, target, events, finished, errors, duration_total_ms, request_bytes, response_bytes)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(dataset_id, target) DO UPDATE SET
			events = target_summaries.events + excluded.events,
			finished = target_summaries.finished + excluded.finished,
			errors = target_summaries.errors + excluded.errors,
			duration_total_ms = target_summaries.duration_total_ms + excluded.duration_total_ms,
			request_bytes = target_summaries.request_bytes + excluded.request_bytes,
			response_bytes = target_summaries.response_bytes + excluded.response_bytes
	`, record.DatasetID, strings.TrimSpace(record.Target), record.Events, record.Finished, record.Errors, record.DurationTotalMS, record.RequestBytes, record.ResponseBytes)
	return err
}

func (workspace *Workspace) InsertTraceLabels(ctx context.Context, datasetID int64, traceKey string, layers []string, targets []string) error {
	if len(layers) == 0 && len(targets) == 0 {
		return nil
	}
	db, err := workspace.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	for _, layer := range layers {
		if strings.TrimSpace(layer) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO trace_layers(dataset_id, trace_key, layer) VALUES (?, ?, ?)`, datasetID, traceKey, strings.TrimSpace(layer)); err != nil {
			return err
		}
	}
	for _, target := range targets {
		if strings.TrimSpace(target) == "" {
			continue
		}
		if _, err := tx.ExecContext(ctx, `INSERT OR IGNORE INTO trace_targets(dataset_id, trace_key, target) VALUES (?, ?, ?)`, datasetID, traceKey, strings.TrimSpace(target)); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (workspace *Workspace) InsertFinding(ctx context.Context, record FindingRecord) error {
	if strings.TrimSpace(record.TraceKey) == "" {
		record.TraceKey = "unknown"
	}
	db, err := workspace.database()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT OR IGNORE INTO findings(dataset_id, severity, severity_rank, code, message, trace_key, count, first_ingest_order)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, record.DatasetID, record.Severity, record.SeverityRank, record.Code, record.Message, record.TraceKey, record.Count, record.FirstIngestOrder)
	return err
}

func (workspace *Workspace) UpsertTracePairStates(ctx context.Context, datasetID int64, traceKey string, records []PairStateRecord) error {
	return workspace.upsertPairStates(ctx, `
		INSERT INTO trace_pair_state(dataset_id, trace_key, pair_key, starts, finishes)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(dataset_id, trace_key, pair_key) DO UPDATE SET
			starts = starts + excluded.starts,
			finishes = finishes + excluded.finishes
	`, datasetID, traceKey, records)
}

func (workspace *Workspace) UpsertTraceToolStates(ctx context.Context, datasetID int64, traceKey string, records []PairStateRecord) error {
	return workspace.upsertPairStates(ctx, `
		INSERT INTO trace_tool_state(dataset_id, trace_key, tool_call_id, starts, finishes)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(dataset_id, trace_key, tool_call_id) DO UPDATE SET
			starts = starts + excluded.starts,
			finishes = finishes + excluded.finishes
	`, datasetID, traceKey, records)
}

func (workspace *Workspace) TracePairStates(ctx context.Context, datasetID int64, traceKey string) ([]PairStateRecord, error) {
	return workspace.listPairStates(ctx, `SELECT pair_key, starts, finishes FROM trace_pair_state WHERE dataset_id = ? AND trace_key = ? ORDER BY pair_key`, datasetID, traceKey)
}

func (workspace *Workspace) TraceToolStates(ctx context.Context, datasetID int64, traceKey string) ([]PairStateRecord, error) {
	return workspace.listPairStates(ctx, `SELECT tool_call_id, starts, finishes FROM trace_tool_state WHERE dataset_id = ? AND trace_key = ? ORDER BY tool_call_id`, datasetID, traceKey)
}

func (workspace *Workspace) ListTracePairStates(ctx context.Context, datasetID int64, traceKey string, after string, limit int) ([]PairStateRecord, error) {
	if limit <= 0 {
		limit = 2048
	}
	return workspace.listPairStates(ctx, `
		SELECT pair_key, starts, finishes
		FROM trace_pair_state
		WHERE dataset_id = ? AND trace_key = ? AND pair_key > ?
		ORDER BY pair_key
		LIMIT ?
	`, datasetID, traceKey, after, limit)
}

func (workspace *Workspace) ListTraceToolStates(ctx context.Context, datasetID int64, traceKey string, after string, limit int) ([]PairStateRecord, error) {
	if limit <= 0 {
		limit = 2048
	}
	return workspace.listPairStates(ctx, `
		SELECT tool_call_id, starts, finishes
		FROM trace_tool_state
		WHERE dataset_id = ? AND trace_key = ? AND tool_call_id > ?
		ORDER BY tool_call_id
		LIMIT ?
	`, datasetID, traceKey, after, limit)
}

func (workspace *Workspace) DeleteTraceScratch(ctx context.Context, datasetID int64, traceKey string) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM trace_pair_state WHERE dataset_id = ? AND trace_key = ?`, datasetID, traceKey); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `DELETE FROM trace_tool_state WHERE dataset_id = ? AND trace_key = ?`, datasetID, traceKey)
	return err
}

func (workspace *Workspace) InsertComparison(ctx context.Context, record ComparisonRecord) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO comparisons(target, current_finished, baseline_finished, error_rate_delta, average_duration_delta_ms)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(target) DO UPDATE SET
			current_finished = excluded.current_finished,
			baseline_finished = excluded.baseline_finished,
			error_rate_delta = excluded.error_rate_delta,
			average_duration_delta_ms = excluded.average_duration_delta_ms
	`, record.Target, record.CurrentFinished, record.BaselineFinished, record.ErrorRateDelta, record.AverageDurationDeltaMS)
	return err
}

func (workspace *Workspace) InsertComparisonsFromTargetSummaries(ctx context.Context, currentID int64, baselineID int64) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		INSERT INTO comparisons(target, current_finished, baseline_finished, error_rate_delta, average_duration_delta_ms)
		SELECT
			current.target,
			current.finished,
			COALESCE(baseline.finished, 0),
			(CASE WHEN current.finished = 0 THEN 0.0 ELSE CAST(current.errors AS REAL) / CAST(current.finished AS REAL) END) -
				(CASE WHEN COALESCE(baseline.finished, 0) = 0 THEN 0.0 ELSE CAST(COALESCE(baseline.errors, 0) AS REAL) / CAST(baseline.finished AS REAL) END),
			(CASE WHEN current.finished = 0 THEN 0.0 ELSE CAST(current.duration_total_ms AS REAL) / CAST(current.finished AS REAL) END) -
				(CASE WHEN COALESCE(baseline.finished, 0) = 0 THEN 0.0 ELSE CAST(COALESCE(baseline.duration_total_ms, 0) AS REAL) / CAST(baseline.finished AS REAL) END)
		FROM target_summaries current
		LEFT JOIN target_summaries baseline ON baseline.dataset_id = ? AND baseline.target = current.target
		WHERE current.dataset_id = ?
		ON CONFLICT(target) DO UPDATE SET
			current_finished = excluded.current_finished,
			baseline_finished = excluded.baseline_finished,
			error_rate_delta = excluded.error_rate_delta,
			average_duration_delta_ms = excluded.average_duration_delta_ms
	`, baselineID, currentID)
	return err
}

func (workspace *Workspace) CountRows(ctx context.Context, table string, datasetID int64) (int64, error) {
	db, err := workspace.database()
	if err != nil {
		return 0, err
	}
	queries := map[string]string{
		"warnings":         `SELECT COUNT(*) FROM warnings WHERE dataset_id = ?`,
		"findings":         `SELECT COUNT(*) FROM findings WHERE dataset_id = ?`,
		"trace_summaries":  `SELECT COUNT(*) FROM trace_summaries WHERE dataset_id = ?`,
		"target_summaries": `SELECT COUNT(*) FROM target_summaries WHERE dataset_id = ?`,
		"comparisons":      `SELECT COUNT(*) FROM comparisons`,
	}
	query, ok := queries[table]
	if !ok {
		return 0, fmt.Errorf("unsupported count table %s", table)
	}
	var count int64
	var scanErr error
	if table == "comparisons" {
		scanErr = db.QueryRowContext(ctx, query).Scan(&count)
	} else {
		scanErr = db.QueryRowContext(ctx, query, datasetID).Scan(&count)
	}
	return count, scanErr
}

func (workspace *Workspace) ForEachInput(ctx context.Context, datasetID int64, visit func(string) error) error {
	return workspace.forEachString(ctx, `SELECT path FROM input_arguments WHERE dataset_id = ? ORDER BY ordinal`, visit, datasetID)
}

func (workspace *Workspace) ForEachWarning(ctx context.Context, datasetID int64, visit func(string) error) error {
	return workspace.forEachString(ctx, `SELECT message FROM warnings WHERE dataset_id = ? ORDER BY ordinal`, visit, datasetID)
}

func (workspace *Workspace) ForEachManifest(ctx context.Context, datasetID int64, visit func(ManifestRecord) error) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT input_file_id, schema_version, app_session_id, mode, status, started_at, closed_at, payload_degraded, dropped_events_key, last_error
		FROM manifests
		WHERE dataset_id = ?
		ORDER BY id
	`, datasetID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		record := ManifestRecord{DatasetID: datasetID}
		var inputFileID sql.NullInt64
		var startedAt string
		var closedAt sql.NullString
		var payloadDegraded int
		var droppedEventsKey string
		var lastError sql.NullString
		if err := rows.Scan(&inputFileID, &record.SchemaVersion, &record.AppSessionID, &record.Mode, &record.Status, &startedAt, &closedAt, &payloadDegraded, &droppedEventsKey, &lastError); err != nil {
			return err
		}
		if inputFileID.Valid {
			record.InputFileID = inputFileID.Int64
		}
		parsedStartedAt, err := time.Parse(time.RFC3339Nano, startedAt)
		if err != nil {
			return err
		}
		record.StartedAt = parsedStartedAt
		if closedAt.Valid && strings.TrimSpace(closedAt.String) != "" {
			parsedClosedAt, err := time.Parse(time.RFC3339Nano, closedAt.String)
			if err != nil {
				return err
			}
			record.ClosedAt = &parsedClosedAt
		}
		record.PayloadDegraded = payloadDegraded != 0
		droppedEvents, err := strconv.ParseUint(droppedEventsKey, 10, 64)
		if err != nil {
			return err
		}
		record.DroppedEvents = droppedEvents
		if lastError.Valid {
			record.LastError = lastError.String
		}
		if err := visit(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (workspace *Workspace) ForEachTargetSummary(ctx context.Context, datasetID int64, visit func(TargetSummaryRecord) error) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT target, events, finished, errors, duration_total_ms, request_bytes, response_bytes
		FROM target_summaries
		WHERE dataset_id = ?
		ORDER BY target
	`, datasetID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		record := TargetSummaryRecord{DatasetID: datasetID}
		if err := rows.Scan(&record.Target, &record.Events, &record.Finished, &record.Errors, &record.DurationTotalMS, &record.RequestBytes, &record.ResponseBytes); err != nil {
			return err
		}
		if err := visit(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (workspace *Workspace) ForEachFinding(ctx context.Context, datasetID int64, visit func(FindingRecord) error) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT severity, severity_rank, code, message, trace_key, count, first_ingest_order
		FROM findings
		WHERE dataset_id = ?
		ORDER BY severity_rank DESC, first_ingest_order, code, message, trace_key
	`, datasetID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		record := FindingRecord{DatasetID: datasetID}
		if err := rows.Scan(&record.Severity, &record.SeverityRank, &record.Code, &record.Message, &record.TraceKey, &record.Count, &record.FirstIngestOrder); err != nil {
			return err
		}
		if err := visit(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (workspace *Workspace) ForEachTraceSummary(ctx context.Context, datasetID int64, visit func(TraceSummaryRecord) error) error {
	var after string
	for {
		records, err := workspace.ListTraceSummaries(ctx, datasetID, after, 2048)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			return nil
		}
		for _, record := range records {
			if err := visit(record); err != nil {
				return err
			}
		}
		after = records[len(records)-1].TraceKey
	}
}

func (workspace *Workspace) ListTraceSummaries(ctx context.Context, datasetID int64, after string, limit int) ([]TraceSummaryRecord, error) {
	if limit <= 0 {
		limit = 2048
	}
	db, err := workspace.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT trace_key, event_count, COALESCE(started_at, ''), COALESCE(finished_at, ''),
			duration_ms, has_error, first_ingest_order, last_ingest_order
		FROM trace_summaries
		WHERE dataset_id = ? AND trace_key > ?
		ORDER BY trace_key
		LIMIT ?
	`, datasetID, after, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]TraceSummaryRecord, 0)
	for rows.Next() {
		record := TraceSummaryRecord{DatasetID: datasetID}
		var hasError int
		if err := rows.Scan(&record.TraceKey, &record.EventCount, &record.StartedAt, &record.FinishedAt, &record.DurationMS, &hasError, &record.FirstIngestOrder, &record.LastIngestOrder); err != nil {
			return nil, err
		}
		record.HasError = hasError != 0
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (workspace *Workspace) ForEachTraceLayer(ctx context.Context, datasetID int64, traceKey string, visit func(string) error) error {
	return workspace.forEachString(ctx, `
		SELECT layer
		FROM trace_layers
		WHERE dataset_id = ? AND trace_key = ?
		ORDER BY layer
	`, visit, datasetID, traceKey)
}

func (workspace *Workspace) ForEachTraceTarget(ctx context.Context, datasetID int64, traceKey string, visit func(string) error) error {
	return workspace.forEachString(ctx, `
		SELECT target
		FROM trace_targets
		WHERE dataset_id = ? AND trace_key = ?
		ORDER BY target
	`, visit, datasetID, traceKey)
}

func (workspace *Workspace) ForEachComparison(ctx context.Context, visit func(ComparisonRecord) error) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT target, current_finished, baseline_finished, error_rate_delta, average_duration_delta_ms
		FROM comparisons
		ORDER BY target
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var record ComparisonRecord
		if err := rows.Scan(&record.Target, &record.CurrentFinished, &record.BaselineFinished, &record.ErrorRateDelta, &record.AverageDurationDeltaMS); err != nil {
			return err
		}
		if err := visit(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func SequenceKey(sequence uint64) string {
	return fmt.Sprintf("%020d", sequence)
}

func (workspace *Workspace) listEvents(ctx context.Context, query string, args ...any) ([]EventRow, error) {
	db, err := workspace.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]EventRow, 0)
	for rows.Next() {
		row := EventRow{}
		var sourceFileID sql.NullInt64
		var timestampText string
		var sequenceKey string
		var decodeError int
		var droppedEventsKey string
		var safeFields sql.NullString
		if err := rows.Scan(
			&sourceFileID, &row.LineNumber, &row.Cursor.TimestampSeconds, &row.Cursor.TimestampNanoseconds, &timestampText,
			&sequenceKey, &row.IngestOrder, &row.TraceKey, &row.SchemaVersion, &row.AppSessionID, &row.TraceID, &row.SpanID,
			&row.ParentSpanID, &row.HTTPRequestID, &row.CursorRequestID, &row.ConversationID, &row.ModelCallID, &row.ToolCallID,
			&row.Layer, &row.Event, &row.Route, &row.ExecutionTarget, &row.Protocol, &row.Status, &row.ErrorCategory, &row.DurationMS,
			&row.RequestBytes, &row.ResponseBytes, &decodeError, &droppedEventsKey, &safeFields,
		); err != nil {
			return nil, err
		}
		if sourceFileID.Valid {
			row.SourceFileID = sourceFileID.Int64
		}
		row.Timestamp = time.Unix(row.Cursor.TimestampSeconds, int64(row.Cursor.TimestampNanoseconds)).UTC()
		sequence, err := strconv.ParseUint(sequenceKey, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse sequence key %q: %w", sequenceKey, err)
		}
		row.Sequence = sequence
		droppedEvents, err := strconv.ParseUint(droppedEventsKey, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("parse dropped events key %q: %w", droppedEventsKey, err)
		}
		row.DroppedEvents = droppedEvents
		row.DecodeError = decodeError != 0
		if safeFields.Valid {
			row.SafeFieldsJSON = safeFields.String
		}
		row.DatasetID = args[0].(int64)
		row.Cursor.TraceKey = row.TraceKey
		row.Cursor.SequenceKey = sequenceKey
		row.Cursor.IngestOrder = row.IngestOrder
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (workspace *Workspace) upsertPairStates(ctx context.Context, query string, datasetID int64, traceKey string, records []PairStateRecord) error {
	if len(records) == 0 {
		return nil
	}
	db, err := workspace.database()
	if err != nil {
		return err
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	statement, err := tx.PrepareContext(ctx, query)
	if err != nil {
		return err
	}
	defer statement.Close()
	for _, record := range records {
		if strings.TrimSpace(record.Key) == "" {
			continue
		}
		if _, err := statement.ExecContext(ctx, datasetID, traceKey, record.Key, record.Starts, record.Finishes); err != nil {
			return err
		}
	}
	if err := statement.Close(); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	committed = true
	return nil
}

func (workspace *Workspace) listPairStates(ctx context.Context, query string, args ...any) ([]PairStateRecord, error) {
	db, err := workspace.database()
	if err != nil {
		return nil, err
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]PairStateRecord, 0)
	for rows.Next() {
		var record PairStateRecord
		if err := rows.Scan(&record.Key, &record.Starts, &record.Finishes); err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return result, nil
}

func (workspace *Workspace) forEachString(ctx context.Context, query string, visit func(string) error, args ...any) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, query, args...)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			return err
		}
		if err := visit(value); err != nil {
			return err
		}
	}
	return rows.Err()
}

func splitGroupConcat(value string) []string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	parts := strings.Split(value, string(rune(31)))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			result = append(result, part)
		}
	}
	return result
}

func (workspace *Workspace) database() (*sql.DB, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.closed || workspace.db == nil {
		return nil, sql.ErrConnDone
	}
	return workspace.db, nil
}

func (workspace *Workspace) initialize(ctx context.Context) error {
	if err := workspace.applyPragmas(ctx); err != nil {
		return err
	}
	if err := workspace.applySchema(ctx); err != nil {
		return err
	}
	return workspace.seedDatasets(ctx)
}

func (workspace *Workspace) applyPragmas(ctx context.Context) error {
	statements := []string{
		`PRAGMA journal_mode=DELETE`,
		`PRAGMA synchronous=NORMAL`,
		`PRAGMA foreign_keys=ON`,
		`PRAGMA temp_store=FILE`,
		`PRAGMA mmap_size=0`,
		`PRAGMA cache_size=-8192`,
	}
	for _, statement := range statements {
		if _, err := workspace.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply %s: %w", statement, err)
		}
	}
	return nil
}

func (workspace *Workspace) applySchema(ctx context.Context) error {
	_, err := workspace.db.ExecContext(ctx, schemaSQL)
	return err
}

func (workspace *Workspace) seedDatasets(ctx context.Context) error {
	for _, kind := range []DatasetKind{DatasetCurrent, DatasetBaseline} {
		if _, err := workspace.db.ExecContext(ctx, `
			INSERT INTO datasets(kind, status) VALUES (?, 'ready')
			ON CONFLICT(kind) DO NOTHING
		`, string(kind)); err != nil {
			return err
		}
	}
	return nil
}

func nullableID(value int64) any {
	if value == 0 {
		return nil
	}
	return value
}

func nullEmpty(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func validateEventRecord(record EventRecord) error {
	if strings.TrimSpace(record.TraceKey) == "" {
		return errors.New("trace key is required")
	}
	if strings.TrimSpace(record.Layer) == "" || strings.TrimSpace(record.Event) == "" {
		return errors.New("layer and event are required")
	}
	return nil
}

func eventValues(record EventRecord) []any {
	utc := record.Timestamp.UTC()
	return []any{
		record.DatasetID, nullableID(record.SourceFileID), record.LineNumber,
		utc.Unix(), utc.Nanosecond(), utc.Format(time.RFC3339Nano),
		SequenceKey(record.Sequence), record.IngestOrder, strings.TrimSpace(record.TraceKey), record.SchemaVersion,
		record.AppSessionID, record.TraceID, record.SpanID, record.ParentSpanID, record.HTTPRequestID,
		record.CursorRequestID, record.ConversationID, record.ModelCallID, record.ToolCallID,
		record.Layer, record.Event, record.Route, record.ExecutionTarget, record.Protocol, record.Status, record.ErrorCategory,
		record.DurationMS, record.RequestBytes, record.ResponseBytes, boolInt(record.DecodeError), SequenceKey(record.DroppedEvents), nullEmpty(record.SafeFieldsJSON),
	}
}

const insertEventSQL = `
INSERT INTO events(
	dataset_id, source_file_id, line_number,
	timestamp_seconds, timestamp_nanoseconds, timestamp_text,
	sequence_key, ingest_order, trace_key, schema_version,
	app_session_id, trace_id, span_id, parent_span_id, http_request_id,
	cursor_request_id, conversation_id, model_call_id, tool_call_id,
	layer, event, route, execution_target, protocol, status, error_category,
	duration_ms, request_bytes, response_bytes, decode_error, dropped_events_key, safe_fields_json
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`

const schemaSQL = `
CREATE TABLE IF NOT EXISTS schema_meta (
    version INTEGER NOT NULL
);
INSERT INTO schema_meta(version)
SELECT 1
WHERE NOT EXISTS (SELECT 1 FROM schema_meta);

CREATE TABLE IF NOT EXISTS datasets (
    id INTEGER PRIMARY KEY,
    kind TEXT NOT NULL UNIQUE CHECK(kind IN ('current', 'baseline')),
    status TEXT NOT NULL CHECK(status IN ('ready', 'ingesting', 'ingested', 'analyzing', 'analyzed')),
    event_count INTEGER NOT NULL DEFAULT 0,
    manifest_count INTEGER NOT NULL DEFAULT 0,
    warning_count INTEGER NOT NULL DEFAULT 0
);

CREATE TABLE IF NOT EXISTS input_arguments (
    id INTEGER PRIMARY KEY,
    dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    path TEXT NOT NULL,
    UNIQUE(dataset_id, ordinal)
);

CREATE TABLE IF NOT EXISTS input_files (
    id INTEGER PRIMARY KEY,
    dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    argument_id INTEGER REFERENCES input_arguments(id) ON DELETE SET NULL,
    canonical_path TEXT NOT NULL,
    file_type TEXT NOT NULL CHECK(file_type IN ('events', 'manifest')),
    first_argument_ordinal INTEGER NOT NULL,
    UNIQUE(dataset_id, canonical_path)
);
CREATE INDEX IF NOT EXISTS idx_input_files_dataset_type ON input_files(dataset_id, file_type, canonical_path);

CREATE TABLE IF NOT EXISTS manifests (
    id INTEGER PRIMARY KEY,
    dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    input_file_id INTEGER REFERENCES input_files(id) ON DELETE SET NULL,
    schema_version INTEGER NOT NULL,
    app_session_id TEXT NOT NULL,
    mode TEXT NOT NULL,
    status TEXT NOT NULL,
    started_at TEXT NOT NULL,
    closed_at TEXT,
    payload_degraded INTEGER NOT NULL DEFAULT 0,
    dropped_events_key TEXT NOT NULL DEFAULT '00000000000000000000',
    last_error TEXT
);

CREATE TABLE IF NOT EXISTS warnings (
    id INTEGER PRIMARY KEY,
    dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    ordinal INTEGER NOT NULL,
    message TEXT NOT NULL,
    UNIQUE(dataset_id, ordinal)
);

CREATE TABLE IF NOT EXISTS events (
    id INTEGER PRIMARY KEY,
    dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    source_file_id INTEGER REFERENCES input_files(id) ON DELETE SET NULL,
    line_number INTEGER NOT NULL,
    timestamp_seconds INTEGER NOT NULL,
    timestamp_nanoseconds INTEGER NOT NULL CHECK(timestamp_nanoseconds >= 0 AND timestamp_nanoseconds < 1000000000),
    timestamp_text TEXT NOT NULL,
    sequence_key TEXT NOT NULL CHECK(length(sequence_key) = 20),
    ingest_order INTEGER NOT NULL,
    trace_key TEXT NOT NULL,
    schema_version INTEGER NOT NULL,
    app_session_id TEXT NOT NULL,
    trace_id TEXT,
    span_id TEXT,
    parent_span_id TEXT,
    http_request_id TEXT,
    cursor_request_id TEXT,
    conversation_id TEXT,
    model_call_id TEXT,
    tool_call_id TEXT,
    layer TEXT NOT NULL,
    event TEXT NOT NULL,
    route TEXT,
    execution_target TEXT,
    protocol TEXT,
    status TEXT,
    error_category TEXT,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    request_bytes INTEGER NOT NULL DEFAULT 0,
    response_bytes INTEGER NOT NULL DEFAULT 0,
    decode_error INTEGER NOT NULL DEFAULT 0,
    dropped_events_key TEXT NOT NULL DEFAULT '00000000000000000000',
    safe_fields_json TEXT,
    UNIQUE(dataset_id, ingest_order)
);
CREATE INDEX IF NOT EXISTS idx_events_global_order ON events(dataset_id, timestamp_seconds, timestamp_nanoseconds, sequence_key, ingest_order);
CREATE INDEX IF NOT EXISTS idx_events_trace_order ON events(dataset_id, trace_key, timestamp_seconds, timestamp_nanoseconds, sequence_key, ingest_order);
CREATE INDEX IF NOT EXISTS idx_events_target ON events(dataset_id, execution_target);

CREATE TABLE IF NOT EXISTS trace_summaries (
    dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    trace_key TEXT NOT NULL,
    event_count INTEGER NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    has_error INTEGER NOT NULL DEFAULT 0,
    first_ingest_order INTEGER NOT NULL,
    last_ingest_order INTEGER NOT NULL,
    PRIMARY KEY(dataset_id, trace_key)
);

CREATE TABLE IF NOT EXISTS trace_layers (
    dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    trace_key TEXT NOT NULL,
    layer TEXT NOT NULL,
    PRIMARY KEY(dataset_id, trace_key, layer)
);

CREATE TABLE IF NOT EXISTS trace_targets (
    dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    trace_key TEXT NOT NULL,
    target TEXT NOT NULL,
    PRIMARY KEY(dataset_id, trace_key, target)
);

CREATE TABLE IF NOT EXISTS target_summaries (
    dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    target TEXT NOT NULL,
    events INTEGER NOT NULL,
    finished INTEGER NOT NULL,
    errors INTEGER NOT NULL,
    duration_total_ms INTEGER NOT NULL,
    request_bytes INTEGER NOT NULL,
    response_bytes INTEGER NOT NULL,
    PRIMARY KEY(dataset_id, target)
);

CREATE TABLE IF NOT EXISTS findings (
    dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    severity TEXT NOT NULL,
    severity_rank INTEGER NOT NULL,
    code TEXT NOT NULL,
    message TEXT NOT NULL,
    trace_key TEXT NOT NULL,
    count INTEGER NOT NULL DEFAULT 0,
    first_ingest_order INTEGER NOT NULL,
    UNIQUE(dataset_id, severity, code, message, trace_key)
);
CREATE INDEX IF NOT EXISTS idx_findings_order ON findings(dataset_id, severity_rank DESC, first_ingest_order, code, message, trace_key);

CREATE TABLE IF NOT EXISTS comparisons (
    target TEXT PRIMARY KEY,
    current_finished INTEGER NOT NULL,
    baseline_finished INTEGER NOT NULL,
    error_rate_delta REAL NOT NULL,
    average_duration_delta_ms REAL NOT NULL
);

CREATE TABLE IF NOT EXISTS trace_pair_state (
    dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    trace_key TEXT NOT NULL,
    pair_key TEXT NOT NULL,
    starts INTEGER NOT NULL DEFAULT 0,
    finishes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(dataset_id, trace_key, pair_key)
);

CREATE TABLE IF NOT EXISTS trace_tool_state (
    dataset_id INTEGER NOT NULL REFERENCES datasets(id) ON DELETE CASCADE,
    trace_key TEXT NOT NULL,
    tool_call_id TEXT NOT NULL,
    starts INTEGER NOT NULL DEFAULT 0,
    finishes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY(dataset_id, trace_key, tool_call_id)
);
`
