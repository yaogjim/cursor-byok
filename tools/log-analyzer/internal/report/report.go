package report

import (
	"archive/zip"
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cursor-log-analyzer/internal/analyze"
	"cursor-log-analyzer/internal/contract"
	"cursor-log-analyzer/internal/workspace"
)

type StagedReport struct {
	output string
	dir    string
}

type reportJSONOptions struct {
	IncludeBaseline bool
	IncludeInputs   bool
	Safe            bool
}

func StageWorkspace(ctx context.Context, output string, store *workspace.Workspace, includeBaseline bool) (*StagedReport, error) {
	output, err := filepath.Abs(strings.TrimSpace(output))
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(output, 0o700); err != nil {
		return nil, err
	}
	if err := os.Chmod(output, 0o700); err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp(output, ".log-analyzer-staging-")
	if err != nil {
		return nil, err
	}
	if err := os.Chmod(staging, 0o700); err != nil {
		_ = os.RemoveAll(staging)
		return nil, err
	}
	staged := &StagedReport{output: output, dir: staging}
	currentID, err := store.DatasetID(ctx, workspace.DatasetCurrent)
	if err != nil {
		_ = staged.Cleanup()
		return nil, err
	}
	generatedAt := time.Now().UTC().Format(time.RFC3339Nano)
	if err := writeReportJSONFile(ctx, filepath.Join(staging, "report.json"), store, currentID, generatedAt, reportJSONOptions{IncludeBaseline: includeBaseline, IncludeInputs: true}); err != nil {
		_ = staged.Cleanup()
		return nil, err
	}
	if err := writeHTML(ctx, filepath.Join(staging, "report.html"), store, currentID, generatedAt); err != nil {
		_ = staged.Cleanup()
		return nil, err
	}
	if err := writeDiagnosticBundle(ctx, filepath.Join(staging, "diagnostic-bundle.zip"), store, currentID, generatedAt, includeBaseline); err != nil {
		_ = staged.Cleanup()
		return nil, err
	}
	return staged, nil
}

func (staged *StagedReport) Cleanup() error {
	if staged == nil || staged.dir == "" {
		return nil
	}
	return os.RemoveAll(staged.dir)
}

func (staged *StagedReport) Publish() error {
	if staged == nil {
		return errors.New("staged report is required")
	}
	files := []string{"report.json", "report.html", "diagnostic-bundle.zip"}
	backups := make(map[string]string)
	published := make(map[string]bool)
	rollback := func(cause error) error {
		var result error = cause
		for _, name := range files {
			final := filepath.Join(staged.output, name)
			if published[name] {
				_ = os.Remove(final)
			}
			if backup := backups[name]; backup != "" {
				_ = os.Remove(final)
				if err := os.Rename(backup, final); err != nil && !errors.Is(err, os.ErrNotExist) {
					result = errors.Join(result, err)
				}
			}
		}
		result = errors.Join(result, staged.Cleanup())
		return result
	}
	for _, name := range files {
		final := filepath.Join(staged.output, name)
		if _, err := os.Stat(final); err == nil {
			backup := filepath.Join(staged.output, fmt.Sprintf(".log-analyzer-backup-%d-%s", os.Getpid(), name))
			_ = os.Remove(backup)
			if err := os.Rename(final, backup); err != nil {
				return rollback(err)
			}
			backups[name] = backup
		} else if !errors.Is(err, os.ErrNotExist) {
			return rollback(err)
		}
	}
	for _, name := range files {
		if err := os.Rename(filepath.Join(staged.dir, name), filepath.Join(staged.output, name)); err != nil {
			return rollback(err)
		}
		published[name] = true
	}
	for _, backup := range backups {
		_ = os.Remove(backup)
	}
	return staged.Cleanup()
}

func writeReportJSONFile(ctx context.Context, path string, store *workspace.Workspace, currentID int64, generatedAt string, options reportJSONOptions) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := writeReportJSON(ctx, file, store, currentID, generatedAt, options); err != nil {
		return errors.Join(err, file.Close())
	}
	return file.Close()
}

func writeReportJSON(ctx context.Context, writer io.Writer, store *workspace.Workspace, currentID int64, generatedAt string, options reportJSONOptions) error {
	buffered := bufio.NewWriter(writer)
	stats, err := store.Stats(ctx, currentID)
	if err != nil {
		return err
	}
	traceCount, err := store.CountRows(ctx, "trace_summaries", currentID)
	if err != nil {
		return err
	}
	warningCount, err := store.CountRows(ctx, "warnings", currentID)
	if err != nil {
		return err
	}
	findingCount, err := store.CountRows(ctx, "findings", currentID)
	if err != nil {
		return err
	}
	comparisonCount, err := store.CountRows(ctx, "comparisons", currentID)
	if err != nil {
		return err
	}
	object := jsonObject{writer: buffered, first: true}
	if _, err := buffered.WriteString("{"); err != nil {
		return err
	}
	if err := object.scalar("schema_version", contract.ReportSchemaVersion); err != nil {
		return err
	}
	if err := object.scalar("generated_at", generatedAt); err != nil {
		return err
	}
	if options.IncludeInputs {
		if err := object.array("inputs", func(yield func(any) error) error {
			return store.ForEachInput(ctx, currentID, func(value string) error { return yield(value) })
		}); err != nil {
			return err
		}
	} else if err := object.scalar("inputs", nil); err != nil {
		return err
	}
	if err := object.scalar("event_count", int(stats.EventCount)); err != nil {
		return err
	}
	if err := object.scalar("trace_count", int(traceCount)); err != nil {
		return err
	}
	if warningCount > 0 {
		if err := object.array("warnings", func(yield func(any) error) error {
			return store.ForEachWarning(ctx, currentID, func(value string) error {
				if options.Safe {
					value = sanitizeText(value)
				}
				return yield(value)
			})
		}); err != nil {
			return err
		}
	}
	if findingCount > 0 {
		if err := object.array("findings", func(yield func(any) error) error {
			return store.ForEachFinding(ctx, currentID, func(record workspace.FindingRecord) error {
				item := analyze.Finding{Severity: record.Severity, Code: record.Code, Message: record.Message, TraceID: record.TraceKey, Count: record.Count}
				if options.Safe {
					item.TraceID = pseudonym(item.TraceID)
					item.Message = sanitizeText(item.Message)
				}
				return yield(item)
			})
		}); err != nil {
			return err
		}
	}
	if err := object.array("targets", func(yield func(any) error) error {
		return store.ForEachTargetSummary(ctx, currentID, func(record workspace.TargetSummaryRecord) error { return yield(targetDTO(record)) })
	}); err != nil {
		return err
	}
	if err := object.array("diagnostic_metrics", func(yield func(any) error) error {
		return store.ForEachDiagnosticMetric(ctx, currentID, func(record workspace.DiagnosticMetricRecord) error {
			value := record.Value
			if options.Safe {
				value = sanitizeText(value)
			}
			terminalCount := record.CompletedCount + record.FailedCount + record.DegradedCount
			failureRate := 0.0
			if terminalCount > 0 {
				failureRate = float64(record.FailedCount) / float64(terminalCount)
			}
			return yield(analyze.DiagnosticMetric{
				Dimension: record.Dimension, Value: value, EventCount: record.EventCount,
				CompletedCount: record.CompletedCount, FailedCount: record.FailedCount, DegradedCount: record.DegradedCount,
				FailureRate: failureRate, DurationSamples: record.DurationSamples,
				DurationP50MS: record.DurationP50MS, DurationP95MS: record.DurationP95MS, DurationP99MS: record.DurationP99MS,
				TTFTSamples: record.TTFTSamples, TTFTP50MS: record.TTFTP50MS, TTFTP95MS: record.TTFTP95MS, TTFTP99MS: record.TTFTP99MS,
				RequestBytes: record.RequestBytes, ResponseBytes: record.ResponseBytes,
			})
		})
	}); err != nil {
		return err
	}
	if err := object.field("traces", func() error {
		return writeTraceSummariesJSON(ctx, buffered, store, currentID, options.Safe)
	}); err != nil {
		return err
	}
	if options.IncludeBaseline && comparisonCount > 0 {
		if err := object.array("comparison", func(yield func(any) error) error {
			return store.ForEachComparison(ctx, func(record workspace.ComparisonRecord) error {
				return yield(analyze.Comparison{Target: record.Target, CurrentFinished: record.CurrentFinished, BaselineFinished: record.BaselineFinished, ErrorRateDelta: record.ErrorRateDelta, AverageDurationDeltaMS: record.AverageDurationDeltaMS})
			})
		}); err != nil {
			return err
		}
		if err := object.array("diagnostic_comparison", func(yield func(any) error) error {
			return store.ForEachDiagnosticComparison(ctx, func(record workspace.DiagnosticComparisonRecord) error {
				value := record.Value
				if options.Safe {
					value = sanitizeText(value)
				}
				return yield(analyze.DiagnosticComparison{
					Dimension: record.Dimension, Value: value,
					CurrentCompleted: record.CurrentCompleted, BaselineCompleted: record.BaselineCompleted,
					SemanticFailureRateDelta: record.SemanticFailureRateDelta, DurationP95DeltaMS: record.DurationP95DeltaMS,
					CurrentFindingCount: record.CurrentFindingCount, BaselineFindingCount: record.BaselineFindingCount,
				})
			})
		}); err != nil {
			return err
		}
	}
	if _, err := buffered.WriteString("\n}\n"); err != nil {
		return err
	}
	return buffered.Flush()
}

type jsonObject struct {
	writer *bufio.Writer
	first  bool
}

func (object *jsonObject) scalar(name string, value any) error {
	return object.field(name, func() error {
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = object.writer.Write(payload)
		return err
	})
}

func (object *jsonObject) array(name string, produce func(func(any) error) error) error {
	return object.field(name, func() error {
		if _, err := object.writer.WriteString("["); err != nil {
			return err
		}
		index := 0
		if err := produce(func(value any) error {
			if index > 0 {
				if _, err := object.writer.WriteString(","); err != nil {
					return err
				}
			}
			if _, err := object.writer.WriteString("\n    "); err != nil {
				return err
			}
			payload, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if _, err := object.writer.Write(payload); err != nil {
				return err
			}
			index++
			return nil
		}); err != nil {
			return err
		}
		if index > 0 {
			if _, err := object.writer.WriteString("\n  "); err != nil {
				return err
			}
		}
		_, err := object.writer.WriteString("]")
		return err
	})
}

func (object *jsonObject) field(name string, writeValue func() error) error {
	if !object.first {
		if _, err := object.writer.WriteString(","); err != nil {
			return err
		}
	}
	object.first = false
	namePayload, err := json.Marshal(name)
	if err != nil {
		return err
	}
	if _, err := object.writer.WriteString("\n  "); err != nil {
		return err
	}
	if _, err := object.writer.Write(namePayload); err != nil {
		return err
	}
	if _, err := object.writer.WriteString(": "); err != nil {
		return err
	}
	return writeValue()
}

func writeTraceSummariesJSON(ctx context.Context, writer *bufio.Writer, store *workspace.Workspace, datasetID int64, safe bool) error {
	if _, err := writer.WriteString("["); err != nil {
		return err
	}
	index := 0
	var after string
	for {
		records, err := store.ListTraceSummaries(ctx, datasetID, after, 2048)
		if err != nil {
			return err
		}
		if len(records) == 0 {
			break
		}
		for _, record := range records {
			if index > 0 {
				if _, err := writer.WriteString(","); err != nil {
					return err
				}
			}
			if _, err := writer.WriteString("\n    {"); err != nil {
				return err
			}
			traceID := record.TraceKey
			if safe {
				traceID = pseudonym(traceID)
			}
			field := traceJSONField{writer: writer, first: true}
			if err := field.scalar("trace_id", traceID); err != nil {
				return err
			}
			if err := field.scalar("event_count", record.EventCount); err != nil {
				return err
			}
			if err := field.stringArray("layers", func(visit func(string) error) error {
				return store.ForEachTraceLayer(ctx, datasetID, record.TraceKey, visit)
			}); err != nil {
				return err
			}
			if err := field.stringArray("execution_targets", func(visit func(string) error) error {
				return store.ForEachTraceTarget(ctx, datasetID, record.TraceKey, visit)
			}); err != nil {
				return err
			}
			if err := field.scalar("started_at", record.StartedAt); err != nil {
				return err
			}
			if err := field.scalar("finished_at", record.FinishedAt); err != nil {
				return err
			}
			if err := field.scalar("duration_ms", record.DurationMS); err != nil {
				return err
			}
			if err := field.scalar("has_error", record.HasError); err != nil {
				return err
			}
			if _, err := writer.WriteString("\n    }"); err != nil {
				return err
			}
			index++
		}
		after = records[len(records)-1].TraceKey
	}
	if index > 0 {
		if _, err := writer.WriteString("\n  "); err != nil {
			return err
		}
	}
	_, err := writer.WriteString("]")
	return err
}

type traceJSONField struct {
	writer *bufio.Writer
	first  bool
}

func (field *traceJSONField) scalar(name string, value any) error {
	return field.write(name, func() error {
		payload, err := json.Marshal(value)
		if err != nil {
			return err
		}
		_, err = field.writer.Write(payload)
		return err
	})
}

func (field *traceJSONField) stringArray(name string, produce func(func(string) error) error) error {
	return field.write(name, func() error {
		if _, err := field.writer.WriteString("["); err != nil {
			return err
		}
		index := 0
		if err := produce(func(value string) error {
			if index > 0 {
				if _, err := field.writer.WriteString(","); err != nil {
					return err
				}
			}
			if _, err := field.writer.WriteString("\n        "); err != nil {
				return err
			}
			payload, err := json.Marshal(value)
			if err != nil {
				return err
			}
			if _, err := field.writer.Write(payload); err != nil {
				return err
			}
			index++
			return nil
		}); err != nil {
			return err
		}
		if index > 0 {
			if _, err := field.writer.WriteString("\n      "); err != nil {
				return err
			}
		}
		_, err := field.writer.WriteString("]")
		return err
	})
}

func (field *traceJSONField) write(name string, writeValue func() error) error {
	if !field.first {
		if _, err := field.writer.WriteString(","); err != nil {
			return err
		}
	}
	field.first = false
	namePayload, err := json.Marshal(name)
	if err != nil {
		return err
	}
	if _, err := field.writer.WriteString("\n      "); err != nil {
		return err
	}
	if _, err := field.writer.Write(namePayload); err != nil {
		return err
	}
	if _, err := field.writer.WriteString(": "); err != nil {
		return err
	}
	return writeValue()
}

func writeHTML(ctx context.Context, path string, store *workspace.Workspace, currentID int64, generatedAt string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	buffered := bufio.NewWriter(file)
	closeWith := func(cause error) error { return errors.Join(cause, buffered.Flush(), file.Close()) }
	stats, err := store.Stats(ctx, currentID)
	if err != nil {
		return closeWith(err)
	}
	traceCount, err := store.CountRows(ctx, "trace_summaries", currentID)
	if err != nil {
		return closeWith(err)
	}
	findingCount, err := store.CountRows(ctx, "findings", currentID)
	if err != nil {
		return closeWith(err)
	}
	if _, err := fmt.Fprintf(buffered, `<!doctype html>
<html lang="zh-CN">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>Cursor BYOK 日志分析报告</title>
<style>
:root{color-scheme:light dark;font-family:Inter,ui-sans-serif,system-ui,sans-serif}body{max-width:1120px;margin:0 auto;padding:32px 20px;background:#f6f8fb;color:#172033}h1,h2{margin:0 0 16px}.cards{display:grid;grid-template-columns:repeat(auto-fit,minmax(180px,1fr));gap:12px;margin:20px 0}.card,section{background:#fff;border:1px solid #dfe5ee;border-radius:12px;padding:16px;box-shadow:0 4px 16px #2030500a}.value{font-size:28px;font-weight:700}.muted{color:#67748a;font-size:13px}section{margin-top:16px;overflow:auto}table{width:100%%;border-collapse:collapse;font-size:14px}th,td{text-align:left;padding:9px;border-bottom:1px solid #e7ebf1;vertical-align:top}.error{color:#c62828}.warning{color:#a15c00}.info{color:#2359a7}code{overflow-wrap:anywhere}@media(prefers-color-scheme:dark){body{background:#11151c;color:#e8edf6}.card,section{background:#181e28;border-color:#303949}.muted{color:#9aa8bc}th,td{border-color:#303949}}
</style>
</head>
<body>
<h1>Cursor BYOK 日志分析报告</h1><div class="muted">生成时间 %s</div>
<div class="cards"><div class="card"><div class="value">%d</div><div class="muted">事件</div></div><div class="card"><div class="value">%d</div><div class="muted">Trace</div></div><div class="card"><div class="value">%d</div><div class="muted">发现</div></div></div>
`, html.EscapeString(generatedAt), stats.EventCount, traceCount, findingCount); err != nil {
		return closeWith(err)
	}
	if _, err := buffered.WriteString(`<section><h2>执行目标</h2><table><thead><tr><th>目标</th><th>完成</th><th>错误率</th><th>平均耗时</th><th>响应字节</th></tr></thead><tbody>`); err != nil {
		return closeWith(err)
	}
	if err := store.ForEachTargetSummary(ctx, currentID, func(record workspace.TargetSummaryRecord) error {
		item := targetDTO(record)
		_, err := fmt.Fprintf(buffered, `<tr><td>%s</td><td>%d</td><td>%s</td><td>%s</td><td>%d</td></tr>`, html.EscapeString(item.Target), item.Finished, percent(item.ErrorRate), millis(item.AverageMS), item.ResponseBytes)
		return err
	}); err != nil {
		return closeWith(err)
	}
	if _, err := buffered.WriteString(`</tbody></table></section><section><h2>发现</h2><table><thead><tr><th>级别</th><th>规则</th><th>说明</th><th>Trace</th></tr></thead><tbody>`); err != nil {
		return closeWith(err)
	}
	if findingCount == 0 {
		if _, err := buffered.WriteString(`<tr><td colspan="4">未发现异常</td></tr>`); err != nil {
			return closeWith(err)
		}
	} else if err := store.ForEachFinding(ctx, currentID, func(record workspace.FindingRecord) error {
		_, err := fmt.Fprintf(buffered, `<tr><td class="%s">%s</td><td>%s</td><td>%s</td><td><code>%s</code></td></tr>`, html.EscapeString(record.Severity), html.EscapeString(record.Severity), html.EscapeString(record.Code), html.EscapeString(record.Message), html.EscapeString(record.TraceKey))
		return err
	}); err != nil {
		return closeWith(err)
	}
	if _, err := buffered.WriteString(`</tbody></table></section><section><h2>Trace</h2><table><thead><tr><th>Trace</th><th>事件数</th><th>层</th><th>目标</th><th>耗时</th><th>错误</th></tr></thead><tbody>`); err != nil {
		return closeWith(err)
	}
	if err := store.ForEachTraceSummary(ctx, currentID, func(record workspace.TraceSummaryRecord) error {
		if _, err := fmt.Fprintf(buffered, `<tr><td><code>%s</code></td><td>%d</td><td>`, html.EscapeString(record.TraceKey), record.EventCount); err != nil {
			return err
		}
		if err := writeTraceLabelsHTML(buffered, func(visit func(string) error) error {
			return store.ForEachTraceLayer(ctx, currentID, record.TraceKey, visit)
		}); err != nil {
			return err
		}
		if _, err := buffered.WriteString(`</td><td>`); err != nil {
			return err
		}
		if err := writeTraceLabelsHTML(buffered, func(visit func(string) error) error {
			return store.ForEachTraceTarget(ctx, currentID, record.TraceKey, visit)
		}); err != nil {
			return err
		}
		_, err := fmt.Fprintf(buffered, `</td><td>%d ms</td><td>%v</td></tr>`, record.DurationMS, record.HasError)
		return err
	}); err != nil {
		return closeWith(err)
	}
	if _, err := buffered.WriteString(`</tbody></table></section></body></html>`); err != nil {
		return closeWith(err)
	}
	return closeWith(nil)
}

func writeTraceLabelsHTML(writer *bufio.Writer, produce func(func(string) error) error) error {
	first := true
	return produce(func(value string) error {
		if !first {
			if _, err := writer.WriteString(" "); err != nil {
				return err
			}
		}
		first = false
		_, err := writer.WriteString(html.EscapeString(value))
		return err
	})
}

func writeDiagnosticBundle(ctx context.Context, path string, store *workspace.Workspace, currentID int64, generatedAt string, includeBaseline bool) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(file)
	closeWith := func(cause error) error {
		return errors.Join(cause, archive.Close(), file.Close())
	}
	reportWriter, err := archive.Create("report.json")
	if err != nil {
		return closeWith(err)
	}
	if err := writeReportJSON(ctx, reportWriter, store, currentID, generatedAt, reportJSONOptions{IncludeBaseline: includeBaseline, IncludeInputs: false, Safe: true}); err != nil {
		return closeWith(err)
	}
	eventsWriter, err := archive.Create("events.jsonl")
	if err != nil {
		return closeWith(err)
	}
	buffered := bufio.NewWriter(eventsWriter)
	var after *workspace.EventCursor
	for {
		rows, err := store.ListGlobalEvents(ctx, currentID, after, 2048)
		if err != nil {
			return closeWith(err)
		}
		if len(rows) == 0 {
			break
		}
		for index := range rows {
			safe, err := sanitizeEvent(eventFromRecord(rows[index].EventRecord))
			if err != nil {
				return closeWith(err)
			}
			payload, err := json.Marshal(safe)
			if err != nil {
				return closeWith(err)
			}
			if _, err := buffered.Write(append(payload, '\n')); err != nil {
				return closeWith(err)
			}
		}
		last := rows[len(rows)-1].Cursor
		after = &last
	}
	if err := buffered.Flush(); err != nil {
		return closeWith(err)
	}
	return closeWith(nil)
}

func targetDTO(record workspace.TargetSummaryRecord) analyze.TargetSummary {
	item := analyze.TargetSummary{Target: record.Target, Events: record.Events, Finished: record.Finished, Errors: record.Errors, RequestBytes: record.RequestBytes, ResponseBytes: record.ResponseBytes}
	if record.Finished > 0 {
		item.ErrorRate = float64(record.Errors) / float64(record.Finished)
		item.AverageMS = float64(record.DurationTotalMS) / float64(record.Finished)
	}
	return item
}

func eventFromRecord(record workspace.EventRecord) contract.Event {
	event := contract.Event{
		SchemaVersion:       record.SchemaVersion,
		Timestamp:           record.Timestamp,
		Sequence:            record.Sequence,
		AppSessionID:        record.AppSessionID,
		ProjectID:           record.ProjectID,
		TraceID:             record.TraceID,
		SpanID:              record.SpanID,
		ParentSpanID:        record.ParentSpanID,
		HTTPRequestID:       record.HTTPRequestID,
		CursorRequestID:     record.CursorRequestID,
		ConversationID:      record.ConversationID,
		TurnID:              record.TurnID,
		TurnSequence:        record.TurnSequence,
		ModelCallID:         record.ModelCallID,
		ToolCallID:          record.ToolCallID,
		Layer:               record.Layer,
		Event:               record.Event,
		Capability:          record.Capability,
		Operation:           record.Operation,
		Direction:           record.Direction,
		Route:               record.Route,
		ExecutionTarget:     record.ExecutionTarget,
		Protocol:            record.Protocol,
		Status:              record.Status,
		SemanticOutcome:     record.SemanticOutcome,
		ImplementationState: record.ImplementationState,
		ErrorCategory:       record.ErrorCategory,
		DurationMS:          record.DurationMS,
		RequestBytes:        record.RequestBytes,
		ResponseBytes:       record.ResponseBytes,
		DecodeError:         record.DecodeError,
		DroppedEvents:       record.DroppedEvents,
		PayloadRef:          record.PayloadRef,
	}
	if strings.TrimSpace(record.SafeFieldsJSON) != "" {
		var fields map[string]any
		if err := json.Unmarshal([]byte(record.SafeFieldsJSON), &fields); err == nil {
			event.Fields = fields
		}
	}
	return event
}

func sanitizeEvent(event contract.Event) (contract.Event, error) {
	event.AppSessionID = pseudonym(event.AppSessionID)
	event.ProjectID = pseudonym(event.ProjectID)
	event.TraceID = pseudonym(event.TraceID)
	event.SpanID = pseudonym(event.SpanID)
	event.ParentSpanID = pseudonym(event.ParentSpanID)
	event.HTTPRequestID = pseudonym(event.HTTPRequestID)
	event.CursorRequestID = pseudonym(event.CursorRequestID)
	event.ConversationID = pseudonym(event.ConversationID)
	event.TurnID = pseudonym(event.TurnID)
	event.ModelCallID = pseudonym(event.ModelCallID)
	event.ToolCallID = pseudonym(event.ToolCallID)
	event.Route = sanitizeRoute(event.Route)
	event.PayloadRef = ""
	event.Source = ""
	event.Fields = allowlistedFields(event.Fields)
	return event, nil
}

func percent(value float64) string { return fmt.Sprintf("%.1f%%", value*100) }
func millis(value float64) string  { return fmt.Sprintf("%.1f ms", value) }

var identifierSegment = regexp.MustCompile(`(?i)^(?:[0-9a-f]{16,}|[0-9a-f]{8}-[0-9a-f-]{27,}|\d{8,})$`)
var urlPattern = regexp.MustCompile(`(?i)https?://[^\s]+`)
var pathPattern = regexp.MustCompile(`(?:[A-Za-z]:[\\/][^\s]+|/(?:[^/\s]+/)+[^\s]+)`)

func sanitizeRoute(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		if index := strings.Index(value, "://"); index >= 0 {
			remainder := value[index+3:]
			if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
				value = remainder[slash:]
			} else {
				return "/"
			}
		}
	}
	if query := strings.IndexByte(value, '?'); query >= 0 {
		value = value[:query]
	}
	segments := strings.Split(value, "/")
	for index, segment := range segments {
		if identifierSegment.MatchString(segment) {
			segments[index] = ":id"
		}
	}
	return strings.Join(segments, "/")
}

func allowlistedFields(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	allowed := map[string]struct{}{
		"method": {}, "status_code": {}, "client_kind": {}, "message_case": {},
		"kind": {}, "finish_reason": {}, "ttft_ms": {}, "append_seqno": {},
	}
	output := make(map[string]any)
	for key, value := range input {
		if _, ok := allowed[key]; ok {
			output[key] = value
		}
	}
	if len(output) == 0 {
		return nil
	}
	return output
}

func sanitizeText(value string) string {
	value = urlPattern.ReplaceAllString(value, "[url]")
	return pathPattern.ReplaceAllString(value, "[path]")
}

func pseudonym(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	digest := sha256.Sum256([]byte(value))
	return "id_" + hex.EncodeToString(digest[:8])
}
