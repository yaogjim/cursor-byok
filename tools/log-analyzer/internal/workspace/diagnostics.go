package workspace

import (
	"context"
	"database/sql"
	"errors"
	"strings"
)

type DiagnosticMetricRecord struct {
	DatasetID       int64
	Dimension       string
	Value           string
	EventCount      int
	CompletedCount  int
	FailedCount     int
	DegradedCount   int
	DurationSamples int
	DurationP50MS   int64
	DurationP95MS   int64
	DurationP99MS   int64
	TTFTSamples     int
	TTFTP50MS       int64
	TTFTP95MS       int64
	TTFTP99MS       int64
	RequestBytes    int64
	ResponseBytes   int64
}

type DiagnosticComparisonRecord struct {
	Dimension                string
	Value                    string
	CurrentCompleted         int
	BaselineCompleted        int
	SemanticFailureRateDelta float64
	DurationP95DeltaMS       int64
	CurrentFindingCount      int
	BaselineFindingCount     int
}

func (workspace *Workspace) InsertTraceIntegrityFindings(ctx context.Context, datasetID int64) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		WITH missing AS (
			SELECT child.trace_key, MIN(child.ingest_order) AS first_ingest_order,
				COUNT(DISTINCT child.parent_span_id) AS missing_count
			FROM events child
			WHERE child.dataset_id = ? AND COALESCE(child.parent_span_id, '') != ''
				AND NOT EXISTS (
					SELECT 1 FROM events parent
					WHERE parent.dataset_id = child.dataset_id
						AND parent.trace_key = child.trace_key
						AND parent.span_id = child.parent_span_id
				)
			GROUP BY child.trace_key
		)
		INSERT OR IGNORE INTO findings(dataset_id, severity, severity_rank, code, message, trace_key, count, first_ingest_order)
		SELECT ?, 'warning', 2, 'parent_span_missing',
			'缺少 ' || missing_count || ' 个 parent span', trace_key, missing_count, first_ingest_order
		FROM missing
	`, datasetID, datasetID)
	return err
}

func (workspace *Workspace) InsertSequenceFindings(ctx context.Context, datasetID int64) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, `
		WITH ordered AS (
			SELECT trace_key, ingest_order, sequence_key,
				LAG(sequence_key) OVER (PARTITION BY source_file_id ORDER BY ingest_order) AS previous_sequence
			FROM events
			WHERE dataset_id = ? AND sequence_key != '00000000000000000000'
		), classified AS (
			SELECT trace_key, MIN(ingest_order) AS first_ingest_order,
				CASE
					WHEN sequence_key = previous_sequence THEN 'sequence_duplicate'
					WHEN sequence_key < previous_sequence THEN 'sequence_reversed'
					WHEN CAST(sequence_key AS INTEGER) > CAST(previous_sequence AS INTEGER) + 1 THEN 'sequence_gap'
				END AS code,
				COUNT(*) AS issue_count
			FROM ordered
			WHERE previous_sequence IS NOT NULL
			GROUP BY trace_key, code
		)
		INSERT OR IGNORE INTO findings(dataset_id, severity, severity_rank, code, message, trace_key, count, first_ingest_order)
		SELECT ?, 'warning', 2, code,
			CASE code
				WHEN 'sequence_duplicate' THEN '事件 sequence 重复'
				WHEN 'sequence_reversed' THEN '事件 sequence 倒序'
				ELSE '事件 sequence 存在缺口'
			END,
			trace_key, issue_count, first_ingest_order
		FROM classified
		WHERE code IS NOT NULL
	`, datasetID, datasetID)
	return err
}

func (workspace *Workspace) RebuildDiagnosticMetrics(ctx context.Context, datasetID int64) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM diagnostic_metrics WHERE dataset_id = ?`, datasetID); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, diagnosticMetricsSQL, datasetID, datasetID, datasetID, datasetID, datasetID)
	return err
}

func (workspace *Workspace) RebuildDiagnosticComparisons(ctx context.Context, currentID int64, baselineID int64) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, `DELETE FROM diagnostic_comparisons`); err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, diagnosticComparisonSQL, baselineID, currentID, currentID, baselineID); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, findingComparisonSQL, baselineID, currentID, currentID, baselineID)
	return err
}

func (workspace *Workspace) ListFindings(ctx context.Context, datasetID int64, afterID int64, limit int) (FindingPage, error) {
	if datasetID <= 0 {
		return FindingPage{}, errors.New("dataset id is required")
	}
	if limit <= 0 {
		limit = 100
	} else if limit > 500 {
		limit = 500
	}
	db, err := workspace.database()
	if err != nil {
		return FindingPage{}, err
	}
	var total int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM findings WHERE dataset_id = ?`, datasetID).Scan(&total); err != nil {
		return FindingPage{}, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, severity, severity_rank, code, message, trace_key, count, first_ingest_order
		FROM findings
		WHERE dataset_id = ? AND id > ?
		ORDER BY id
		LIMIT ?
	`, datasetID, afterID, limit+1)
	if err != nil {
		return FindingPage{}, err
	}
	defer rows.Close()
	items := make([]FindingRow, 0, limit+1)
	for rows.Next() {
		item := FindingRow{FindingRecord: FindingRecord{DatasetID: datasetID}}
		if err := rows.Scan(&item.ID, &item.Severity, &item.SeverityRank, &item.Code, &item.Message, &item.TraceKey, &item.Count, &item.FirstIngestOrder); err != nil {
			return FindingPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return FindingPage{}, err
	}
	page := FindingPage{Findings: items, Total: total}
	if len(items) > limit {
		page.Findings = items[:limit]
		page.NextCursor = page.Findings[len(page.Findings)-1].ID
	}
	return page, nil
}

func (workspace *Workspace) ListDiagnosticMetrics(ctx context.Context, datasetID int64, after string, limit int) (DiagnosticMetricPage, error) {
	if datasetID <= 0 {
		return DiagnosticMetricPage{}, errors.New("dataset id is required")
	}
	if limit <= 0 {
		limit = 100
	} else if limit > 500 {
		limit = 500
	}
	afterDimension, afterValue := splitMetricCursor(after)
	db, err := workspace.database()
	if err != nil {
		return DiagnosticMetricPage{}, err
	}
	var total int64
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM diagnostic_metrics WHERE dataset_id = ?`, datasetID).Scan(&total); err != nil {
		return DiagnosticMetricPage{}, err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT dimension, value, event_count, completed_count, failed_count, degraded_count,
			duration_samples, duration_p50_ms, duration_p95_ms, duration_p99_ms,
			ttft_samples, ttft_p50_ms, ttft_p95_ms, ttft_p99_ms, request_bytes, response_bytes
		FROM diagnostic_metrics
		WHERE dataset_id = ? AND (dimension > ? OR (dimension = ? AND value > ?))
		ORDER BY dimension, value
		LIMIT ?
	`, datasetID, afterDimension, afterDimension, afterValue, limit+1)
	if err != nil {
		return DiagnosticMetricPage{}, err
	}
	defer rows.Close()
	items := make([]DiagnosticMetricRecord, 0, limit+1)
	for rows.Next() {
		item := DiagnosticMetricRecord{DatasetID: datasetID}
		if err := rows.Scan(
			&item.Dimension, &item.Value, &item.EventCount, &item.CompletedCount,
			&item.FailedCount, &item.DegradedCount, &item.DurationSamples,
			&item.DurationP50MS, &item.DurationP95MS, &item.DurationP99MS,
			&item.TTFTSamples, &item.TTFTP50MS, &item.TTFTP95MS, &item.TTFTP99MS,
			&item.RequestBytes, &item.ResponseBytes,
		); err != nil {
			return DiagnosticMetricPage{}, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return DiagnosticMetricPage{}, err
	}
	page := DiagnosticMetricPage{Metrics: items, Total: total}
	if len(items) > limit {
		page.Metrics = items[:limit]
		last := page.Metrics[len(page.Metrics)-1]
		page.NextCursor = last.Dimension + "\x1f" + last.Value
	}
	return page, nil
}

func splitMetricCursor(cursor string) (string, string) {
	parts := strings.SplitN(cursor, "\x1f", 2)
	if len(parts) == 2 {
		return parts[0], parts[1]
	}
	return "", ""
}

func (workspace *Workspace) ForEachDiagnosticMetric(ctx context.Context, datasetID int64, visit func(DiagnosticMetricRecord) error) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT dimension, value, event_count, completed_count, failed_count, degraded_count,
			duration_samples, duration_p50_ms, duration_p95_ms, duration_p99_ms,
			ttft_samples, ttft_p50_ms, ttft_p95_ms, ttft_p99_ms, request_bytes, response_bytes
		FROM diagnostic_metrics
		WHERE dataset_id = ?
		ORDER BY dimension, value
	`, datasetID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		record := DiagnosticMetricRecord{DatasetID: datasetID}
		if err := rows.Scan(
			&record.Dimension, &record.Value, &record.EventCount, &record.CompletedCount,
			&record.FailedCount, &record.DegradedCount, &record.DurationSamples,
			&record.DurationP50MS, &record.DurationP95MS, &record.DurationP99MS,
			&record.TTFTSamples, &record.TTFTP50MS, &record.TTFTP95MS, &record.TTFTP99MS,
			&record.RequestBytes, &record.ResponseBytes,
		); err != nil {
			return err
		}
		if err := visit(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (workspace *Workspace) ForEachDiagnosticComparison(ctx context.Context, visit func(DiagnosticComparisonRecord) error) error {
	db, err := workspace.database()
	if err != nil {
		return err
	}
	rows, err := db.QueryContext(ctx, `
		SELECT dimension, value, current_completed, baseline_completed,
			semantic_failure_rate_delta, duration_p95_delta_ms,
			current_finding_count, baseline_finding_count
		FROM diagnostic_comparisons
		ORDER BY dimension, value
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var record DiagnosticComparisonRecord
		if err := rows.Scan(
			&record.Dimension, &record.Value, &record.CurrentCompleted, &record.BaselineCompleted,
			&record.SemanticFailureRateDelta, &record.DurationP95DeltaMS,
			&record.CurrentFindingCount, &record.BaselineFindingCount,
		); err != nil {
			return err
		}
		if err := visit(record); err != nil {
			return err
		}
	}
	return rows.Err()
}

func (workspace *Workspace) DiagnosticMetric(ctx context.Context, datasetID int64, dimension string, value string) (DiagnosticMetricRecord, error) {
	var result DiagnosticMetricRecord
	errStop := errors.New("metric found")
	err := workspace.ForEachDiagnosticMetric(ctx, datasetID, func(record DiagnosticMetricRecord) error {
		if record.Dimension == dimension && record.Value == value {
			result = record
			return errStop
		}
		return nil
	})
	if errors.Is(err, errStop) {
		return result, nil
	}
	if err != nil {
		return DiagnosticMetricRecord{}, err
	}
	return DiagnosticMetricRecord{}, sql.ErrNoRows
}

const diagnosticMetricsSQL = `
WITH dimensions AS (
	SELECT dataset_id, 'project' AS dimension, COALESCE(NULLIF(project_id, ''), 'unspecified') AS value,
		semantic_outcome, implementation_state, status, error_category, duration_ms, request_bytes, response_bytes,
		CASE WHEN json_valid(COALESCE(safe_fields_json, '')) AND json_type(safe_fields_json, '$.ttft_ms') IN ('integer', 'real')
			THEN CAST(json_extract(safe_fields_json, '$.ttft_ms') AS INTEGER) END AS ttft_ms
	FROM events WHERE dataset_id = ?
	UNION ALL
	SELECT dataset_id, 'capability', COALESCE(NULLIF(capability, ''), 'unspecified'), semantic_outcome, implementation_state, status, error_category, duration_ms, request_bytes, response_bytes,
		CASE WHEN json_valid(COALESCE(safe_fields_json, '')) AND json_type(safe_fields_json, '$.ttft_ms') IN ('integer', 'real') THEN CAST(json_extract(safe_fields_json, '$.ttft_ms') AS INTEGER) END
	FROM events WHERE dataset_id = ?
	UNION ALL
	SELECT dataset_id, 'operation', COALESCE(NULLIF(operation, ''), 'unspecified'), semantic_outcome, implementation_state, status, error_category, duration_ms, request_bytes, response_bytes,
		CASE WHEN json_valid(COALESCE(safe_fields_json, '')) AND json_type(safe_fields_json, '$.ttft_ms') IN ('integer', 'real') THEN CAST(json_extract(safe_fields_json, '$.ttft_ms') AS INTEGER) END
	FROM events WHERE dataset_id = ?
	UNION ALL
	SELECT dataset_id, 'route', COALESCE(NULLIF(route, ''), 'unspecified'), semantic_outcome, implementation_state, status, error_category, duration_ms, request_bytes, response_bytes,
		CASE WHEN json_valid(COALESCE(safe_fields_json, '')) AND json_type(safe_fields_json, '$.ttft_ms') IN ('integer', 'real') THEN CAST(json_extract(safe_fields_json, '$.ttft_ms') AS INTEGER) END
	FROM events WHERE dataset_id = ?
	UNION ALL
	SELECT dataset_id, 'target', COALESCE(NULLIF(execution_target, ''), 'unspecified'), semantic_outcome, implementation_state, status, error_category, duration_ms, request_bytes, response_bytes,
		CASE WHEN json_valid(COALESCE(safe_fields_json, '')) AND json_type(safe_fields_json, '$.ttft_ms') IN ('integer', 'real') THEN CAST(json_extract(safe_fields_json, '$.ttft_ms') AS INTEGER) END
	FROM events WHERE dataset_id = ?
), ranked AS (
	SELECT *,
		CASE WHEN duration_ms > 0 THEN ROW_NUMBER() OVER (PARTITION BY dimension, value, duration_ms > 0 ORDER BY duration_ms) END AS duration_rank,
		SUM(CASE WHEN duration_ms > 0 THEN 1 ELSE 0 END) OVER (PARTITION BY dimension, value) AS duration_total,
		CASE WHEN ttft_ms >= 0 THEN ROW_NUMBER() OVER (PARTITION BY dimension, value, ttft_ms IS NOT NULL ORDER BY ttft_ms) END AS ttft_rank,
		SUM(CASE WHEN ttft_ms >= 0 THEN 1 ELSE 0 END) OVER (PARTITION BY dimension, value) AS ttft_total
	FROM dimensions
)
INSERT INTO diagnostic_metrics(
	dataset_id, dimension, value, event_count, completed_count, failed_count, degraded_count,
	duration_samples, duration_p50_ms, duration_p95_ms, duration_p99_ms,
	ttft_samples, ttft_p50_ms, ttft_p95_ms, ttft_p99_ms, request_bytes, response_bytes
)
SELECT dataset_id, dimension, value, COUNT(*),
	SUM(CASE WHEN semantic_outcome = 'succeeded' AND implementation_state = 'implemented' THEN 1 ELSE 0 END),
	SUM(CASE WHEN semantic_outcome IN ('failed', 'timeout', 'unsupported') OR status IN ('error', 'failed') OR COALESCE(error_category, '') != '' THEN 1 ELSE 0 END),
	SUM(CASE
		WHEN semantic_outcome IN ('failed', 'timeout', 'unsupported') OR status IN ('error', 'failed') OR COALESCE(error_category, '') != '' THEN 0
		WHEN semantic_outcome IN ('degraded', 'partial', 'compat_only') OR implementation_state IN ('partial', 'compat') THEN 1
		ELSE 0 END),
	MAX(duration_total),
	COALESCE(MIN(CASE WHEN duration_ms > 0 AND duration_rank * 100 >= duration_total * 50 THEN duration_ms END), 0),
	COALESCE(MIN(CASE WHEN duration_ms > 0 AND duration_rank * 100 >= duration_total * 95 THEN duration_ms END), 0),
	COALESCE(MIN(CASE WHEN duration_ms > 0 AND duration_rank * 100 >= duration_total * 99 THEN duration_ms END), 0),
	MAX(ttft_total),
	COALESCE(MIN(CASE WHEN ttft_ms >= 0 AND ttft_rank * 100 >= ttft_total * 50 THEN ttft_ms END), 0),
	COALESCE(MIN(CASE WHEN ttft_ms >= 0 AND ttft_rank * 100 >= ttft_total * 95 THEN ttft_ms END), 0),
	COALESCE(MIN(CASE WHEN ttft_ms >= 0 AND ttft_rank * 100 >= ttft_total * 99 THEN ttft_ms END), 0),
	SUM(request_bytes), SUM(response_bytes)
FROM ranked
GROUP BY dataset_id, dimension, value
`

const diagnosticComparisonSQL = `
INSERT INTO diagnostic_comparisons(
	dimension, value, current_completed, baseline_completed,
	semantic_failure_rate_delta, duration_p95_delta_ms,
	current_finding_count, baseline_finding_count
)
SELECT current.dimension, current.value, current.completed_count, COALESCE(baseline.completed_count, 0),
	(CASE WHEN current.completed_count + current.failed_count + current.degraded_count = 0 THEN 0.0 ELSE CAST(current.failed_count AS REAL) / (current.completed_count + current.failed_count + current.degraded_count) END) -
		(CASE WHEN COALESCE(baseline.completed_count, 0) + COALESCE(baseline.failed_count, 0) + COALESCE(baseline.degraded_count, 0) = 0 THEN 0.0 ELSE CAST(baseline.failed_count AS REAL) / (baseline.completed_count + baseline.failed_count + baseline.degraded_count) END),
	current.duration_p95_ms - COALESCE(baseline.duration_p95_ms, 0), 0, 0
FROM diagnostic_metrics current
LEFT JOIN diagnostic_metrics baseline
	ON baseline.dataset_id = ? AND baseline.dimension = current.dimension AND baseline.value = current.value
WHERE current.dataset_id = ?
UNION ALL
SELECT baseline.dimension, baseline.value, 0, baseline.completed_count,
	0.0 - (CASE WHEN baseline.completed_count + baseline.failed_count + baseline.degraded_count = 0 THEN 0.0 ELSE CAST(baseline.failed_count AS REAL) / (baseline.completed_count + baseline.failed_count + baseline.degraded_count) END),
	0 - baseline.duration_p95_ms, 0, 0
FROM diagnostic_metrics baseline
LEFT JOIN diagnostic_metrics current
	ON current.dataset_id = ? AND current.dimension = baseline.dimension AND current.value = baseline.value
WHERE baseline.dataset_id = ? AND current.value IS NULL
`

const findingComparisonSQL = `
INSERT INTO diagnostic_comparisons(
	dimension, value, current_completed, baseline_completed,
	semantic_failure_rate_delta, duration_p95_delta_ms,
	current_finding_count, baseline_finding_count
)
SELECT 'finding', current.code, 0, 0, 0.0, 0, SUM(CASE WHEN current.count > 0 THEN current.count ELSE 1 END),
	COALESCE((SELECT SUM(CASE WHEN baseline.count > 0 THEN baseline.count ELSE 1 END)
		FROM findings baseline WHERE baseline.dataset_id = ? AND baseline.code = current.code), 0)
FROM findings current
WHERE current.dataset_id = ?
GROUP BY current.code
UNION ALL
SELECT 'finding', baseline.code, 0, 0, 0.0, 0, 0,
	SUM(CASE WHEN baseline.count > 0 THEN baseline.count ELSE 1 END)
FROM findings baseline
LEFT JOIN findings current ON current.dataset_id = ? AND current.code = baseline.code
WHERE baseline.dataset_id = ? AND current.code IS NULL
GROUP BY baseline.code
`
