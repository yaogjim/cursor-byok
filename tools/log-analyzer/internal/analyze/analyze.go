package analyze

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"cursor-log-analyzer/internal/workspace"
)

const (
	eventBatchLimit = 2048
	stateFlushLimit = 4096
	timeFormat      = "2006-01-02T15:04:05.999999999Z07:00"
)

type Finding struct {
	Severity string `json:"severity"`
	Code     string `json:"code"`
	Message  string `json:"message"`
	TraceID  string `json:"trace_id,omitempty"`
	Count    int    `json:"count,omitempty"`
}

type TargetSummary struct {
	Target        string  `json:"target"`
	Events        int     `json:"events"`
	Finished      int     `json:"finished"`
	Errors        int     `json:"errors"`
	ErrorRate     float64 `json:"error_rate"`
	AverageMS     float64 `json:"average_ms"`
	RequestBytes  int64   `json:"request_bytes"`
	ResponseBytes int64   `json:"response_bytes"`
}

type TraceSummary struct {
	TraceID          string   `json:"trace_id"`
	EventCount       int      `json:"event_count"`
	Layers           []string `json:"layers"`
	ExecutionTargets []string `json:"execution_targets"`
	StartedAt        string   `json:"started_at,omitempty"`
	FinishedAt       string   `json:"finished_at,omitempty"`
	DurationMS       int64    `json:"duration_ms,omitempty"`
	HasError         bool     `json:"has_error"`
}

type DiagnosticMetric struct {
	Dimension       string  `json:"dimension"`
	Value           string  `json:"value"`
	EventCount      int     `json:"event_count"`
	CompletedCount  int     `json:"completed_count"`
	FailedCount     int     `json:"failed_count"`
	DegradedCount   int     `json:"degraded_count"`
	FailureRate     float64 `json:"semantic_failure_rate"`
	DurationSamples int     `json:"duration_samples"`
	DurationP50MS   int64   `json:"duration_p50_ms"`
	DurationP95MS   int64   `json:"duration_p95_ms"`
	DurationP99MS   int64   `json:"duration_p99_ms"`
	TTFTSamples     int     `json:"ttft_samples"`
	TTFTP50MS       int64   `json:"ttft_p50_ms"`
	TTFTP95MS       int64   `json:"ttft_p95_ms"`
	TTFTP99MS       int64   `json:"ttft_p99_ms"`
	RequestBytes    int64   `json:"request_bytes"`
	ResponseBytes   int64   `json:"response_bytes"`
}

type DiagnosticComparison struct {
	Dimension                string  `json:"dimension"`
	Value                    string  `json:"value"`
	CurrentCompleted         int     `json:"current_completed"`
	BaselineCompleted        int     `json:"baseline_completed"`
	SemanticFailureRateDelta float64 `json:"semantic_failure_rate_delta"`
	DurationP95DeltaMS       int64   `json:"duration_p95_delta_ms"`
	CurrentFindingCount      int     `json:"current_finding_count"`
	BaselineFindingCount     int     `json:"baseline_finding_count"`
}

type Comparison struct {
	Target                 string  `json:"target"`
	CurrentFinished        int     `json:"current_finished"`
	BaselineFinished       int     `json:"baseline_finished"`
	ErrorRateDelta         float64 `json:"error_rate_delta"`
	AverageDurationDeltaMS float64 `json:"average_duration_delta_ms"`
}

type Report struct {
	SchemaVersion int             `json:"schema_version"`
	GeneratedAt   string          `json:"generated_at"`
	Inputs        []string        `json:"inputs"`
	EventCount    int             `json:"event_count"`
	TraceCount    int             `json:"trace_count"`
	Warnings      []string        `json:"warnings,omitempty"`
	Findings      []Finding       `json:"findings,omitempty"`
	Targets       []TargetSummary `json:"targets"`
	Traces        []TraceSummary  `json:"traces"`
	Comparison    []Comparison    `json:"comparison,omitempty"`
}

type Summary struct {
	EventCount   int64
	TraceCount   int64
	FindingCount int64
}

type targetAggregate struct {
	events          int
	finished        int
	errors          int
	durationTotalMS int64
	requestBytes    int64
	responseBytes   int64
}

type targetBuffer struct {
	datasetID int64
	values    map[string]*targetAggregate
}

type pairCounts struct {
	starts   int
	finishes int
}

type traceState struct {
	datasetID        int64
	traceKey         string
	eventCount       int
	layers           map[string]struct{}
	targets          map[string]struct{}
	startedAt        time.Time
	finishedAt       time.Time
	firstIngestOrder int64
	lastIngestOrder  int64
	hasError         bool
	pairs            map[string]*pairCounts
	tools            map[string]*pairCounts
	canceledOps      map[string]struct{}
	insufficient     int
	providerTerminal bool
	runsseStarted    bool
	runsseTerminal   bool
}

func Workspace(ctx context.Context, store *workspace.Workspace, includeBaseline bool) (Summary, error) {
	currentID, err := store.DatasetID(ctx, workspace.DatasetCurrent)
	if err != nil {
		return Summary{}, err
	}
	if err := store.ClearAnalysis(ctx, currentID); err != nil {
		return Summary{}, err
	}
	if err := store.ClearComparisons(ctx); err != nil {
		return Summary{}, err
	}
	if err := analyzeCurrent(ctx, store, currentID); err != nil {
		return Summary{}, err
	}
	if includeBaseline {
		baselineID, err := store.DatasetID(ctx, workspace.DatasetBaseline)
		if err != nil {
			return Summary{}, err
		}
		if err := store.ClearAnalysis(ctx, baselineID); err != nil {
			return Summary{}, err
		}
		if err := analyzeCurrent(ctx, store, baselineID); err != nil {
			return Summary{}, err
		}
		if err := compareTargets(ctx, store, currentID, baselineID); err != nil {
			return Summary{}, err
		}
		if err := store.RebuildDiagnosticComparisons(ctx, currentID, baselineID); err != nil {
			return Summary{}, err
		}
	}
	stats, err := store.Stats(ctx, currentID)
	if err != nil {
		return Summary{}, err
	}
	traceCount, err := store.CountRows(ctx, "trace_summaries", currentID)
	if err != nil {
		return Summary{}, err
	}
	findingCount, err := store.CountRows(ctx, "findings", currentID)
	if err != nil {
		return Summary{}, err
	}
	return Summary{EventCount: stats.EventCount, TraceCount: traceCount, FindingCount: findingCount}, nil
}

func analyzeCurrent(ctx context.Context, store *workspace.Workspace, datasetID int64) error {
	if err := insertManifestFindings(ctx, store, datasetID); err != nil {
		return err
	}
	targets := newTargetBuffer(datasetID)
	var after *workspace.EventCursor
	var current *traceState
	for {
		rows, err := store.ListTraceEvents(ctx, datasetID, after, eventBatchLimit)
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			break
		}
		for index := range rows {
			row := rows[index]
			if current == nil || current.traceKey != row.TraceKey {
				if current != nil {
					if err := finalizeTrace(ctx, store, current); err != nil {
						return err
					}
				}
				current = newTraceState(datasetID, row.TraceKey)
			}
			if err := current.addEvent(ctx, store, row); err != nil {
				return err
			}
			if err := targets.add(ctx, store, row.EventRecord); err != nil {
				return err
			}
		}
		last := rows[len(rows)-1].Cursor
		after = &last
	}
	if current != nil {
		if err := finalizeTrace(ctx, store, current); err != nil {
			return err
		}
	}
	if err := targets.flush(ctx, store); err != nil {
		return err
	}
	if err := store.InsertTraceIntegrityFindings(ctx, datasetID); err != nil {
		return err
	}
	if err := store.InsertSequenceFindings(ctx, datasetID); err != nil {
		return err
	}
	return store.RebuildDiagnosticMetrics(ctx, datasetID)
}

func insertManifestFindings(ctx context.Context, store *workspace.Workspace, datasetID int64) error {
	findings := make([]workspace.FindingRecord, 0)
	if err := store.ForEachManifest(ctx, datasetID, func(manifest workspace.ManifestRecord) error {
		if manifest.Status != "closed" {
			findings = append(findings, workspace.FindingRecord{DatasetID: datasetID, Severity: "warning", SeverityRank: severityRank("warning"), Code: "session_not_closed", Message: "采集 session 未正常关闭", TraceKey: manifest.AppSessionID, FirstIngestOrder: 0})
		}
		if manifest.PayloadDegraded || manifest.DroppedEvents > 0 {
			findings = append(findings, workspace.FindingRecord{DatasetID: datasetID, Severity: "error", SeverityRank: severityRank("error"), Code: "capture_degraded", Message: "采集发生降级或事件丢失", TraceKey: manifest.AppSessionID, Count: int(manifest.DroppedEvents), FirstIngestOrder: 0})
		}
		return nil
	}); err != nil {
		return err
	}
	for _, finding := range findings {
		if err := store.InsertFinding(ctx, finding); err != nil {
			return err
		}
	}
	return nil
}

func newTraceState(datasetID int64, traceKey string) *traceState {
	return &traceState{
		datasetID:   datasetID,
		traceKey:    traceKey,
		layers:      make(map[string]struct{}),
		targets:     make(map[string]struct{}),
		pairs:       make(map[string]*pairCounts),
		tools:       make(map[string]*pairCounts),
		canceledOps: make(map[string]struct{}),
	}
}

func (state *traceState) addEvent(ctx context.Context, store *workspace.Workspace, row workspace.EventRow) error {
	event := row.EventRecord
	state.eventCount++
	if state.eventCount == 1 {
		state.startedAt = event.Timestamp
		state.firstIngestOrder = event.IngestOrder
	}
	state.finishedAt = event.Timestamp
	state.lastIngestOrder = event.IngestOrder
	if strings.TrimSpace(event.Layer) != "" {
		state.layers[event.Layer] = struct{}{}
	}
	if strings.TrimSpace(event.ExecutionTarget) != "" {
		state.targets[event.ExecutionTarget] = struct{}{}
	}
	if len(state.layers) >= stateFlushLimit || len(state.targets) >= stateFlushLimit {
		if err := state.flushLabels(ctx, store); err != nil {
			return err
		}
	}
	state.hasError = state.hasError || event.Status == "error" || event.ErrorCategory != "" || isSemanticFailure(event.SemanticOutcome)
	state.addSemanticState(event)
	if err := state.insertSemanticFindings(ctx, store, event); err != nil {
		return err
	}
	pairKey := event.Layer + ":" + event.Route
	switch event.Event {
	case "request_started", "backend_forward_started":
		state.addPair(pairKey, 1, 0)
	case "request_finished", "backend_forward_finished":
		state.addPair(pairKey, 0, 1)
	case "llm_request":
		state.addPair("provider:"+event.ModelCallID, 1, 0)
	case "llm_summary":
		state.addPair("provider:"+event.ModelCallID, 0, 1)
	case "subscribe":
		state.addPair("runsse", 1, 0)
	case "terminal", "terminal_after_context_done":
		state.addPair("runsse", 0, 1)
	}
	name := strings.ToLower(event.Event)
	if strings.Contains(name, "tool_call") && (strings.Contains(name, "start") || strings.Contains(name, "dispatch")) {
		state.addTool(event.ToolCallID, 1, 0)
	}
	if strings.Contains(name, "tool_call") && (strings.Contains(name, "complete") || strings.Contains(name, "result")) {
		state.addTool(event.ToolCallID, 0, 1)
	}
	if event.DecodeError {
		if err := store.InsertFinding(ctx, workspace.FindingRecord{DatasetID: state.datasetID, Severity: "warning", SeverityRank: severityRank("warning"), Code: "decode_error", Message: "存在无法安全解析的请求正文，仅保留元数据", TraceKey: state.traceKey, FirstIngestOrder: event.IngestOrder}); err != nil {
			return err
		}
	}
	if event.ErrorCategory != "" || event.Status == "error" {
		if err := store.InsertFinding(ctx, workspace.FindingRecord{DatasetID: state.datasetID, Severity: "error", SeverityRank: severityRank("error"), Code: "request_error", Message: firstNonEmpty(event.ErrorCategory, "请求以错误状态结束"), TraceKey: state.traceKey, FirstIngestOrder: event.IngestOrder}); err != nil {
			return err
		}
	}
	if event.DurationMS >= 30_000 {
		if err := store.InsertFinding(ctx, workspace.FindingRecord{DatasetID: state.datasetID, Severity: "warning", SeverityRank: severityRank("warning"), Code: "slow_stage", Message: fmt.Sprintf("%s/%s 耗时 %dms", event.Layer, event.Event, event.DurationMS), TraceKey: state.traceKey, FirstIngestOrder: event.IngestOrder}); err != nil {
			return err
		}
	}
	if len(state.pairs) >= stateFlushLimit {
		if err := state.flushPairs(ctx, store); err != nil {
			return err
		}
	}
	if len(state.tools) >= stateFlushLimit {
		if err := state.flushTools(ctx, store); err != nil {
			return err
		}
	}
	return nil
}

func (state *traceState) addSemanticState(event workspace.EventRecord) {
	operation := strings.TrimSpace(event.Operation)
	outcome := strings.TrimSpace(event.SemanticOutcome)
	implementation := strings.TrimSpace(event.ImplementationState)
	if event.SchemaVersion >= 2 && strings.TrimSpace(event.Capability) != "" && event.Capability != "unknown" && operation != "" && (outcome == "unknown" || implementation == "unknown") {
		state.insufficient++
	}
	if event.Capability == "provider" && isSemanticTerminal(outcome) && outcome != "canceled" {
		state.providerTerminal = true
	}
	if event.Event == "subscribe" {
		state.runsseStarted = true
	}
	if event.Event == "terminal" || event.Event == "terminal_after_context_done" {
		state.runsseTerminal = true
	}
	key := semanticOperationKey(event)
	if key == "" {
		return
	}
	if _, canceled := state.canceledOps[key]; canceled && outcome != "canceled" && (isSemanticTerminal(outcome) || event.ResponseBytes > 0) {
		state.addPair("continued_after_cancel:"+key, 1, 0)
	}
	if outcome == "started" {
		state.addPair("operation:"+key, 1, 0)
	}
	if isSemanticTerminal(outcome) {
		state.addPair("operation:"+key, 0, 1)
	}
	if outcome == "canceled" {
		if len(state.canceledOps) >= stateFlushLimit {
			state.insufficient++
			state.canceledOps = make(map[string]struct{})
		}
		state.canceledOps[key] = struct{}{}
	}
}

func (state *traceState) insertSemanticFindings(ctx context.Context, store *workspace.Workspace, event workspace.EventRecord) error {
	implementation := strings.TrimSpace(event.ImplementationState)
	outcome := strings.TrimSpace(event.SemanticOutcome)
	if implementation == "partial" || implementation == "compat" || implementation == "unsupported" {
		severity := "warning"
		if implementation == "unsupported" {
			severity = "error"
		}
		if err := store.InsertFinding(ctx, workspace.FindingRecord{
			DatasetID: state.datasetID, Severity: severity, SeverityRank: severityRank(severity),
			Code: "capability_limitation", Message: fmt.Sprintf("%s/%s implementation=%s", firstNonEmpty(event.Capability, "unknown"), firstNonEmpty(event.Operation, "unknown"), implementation),
			TraceKey: state.traceKey, FirstIngestOrder: event.IngestOrder,
		}); err != nil {
			return err
		}
	}
	if outcome == "partial" || outcome == "compat_only" || outcome == "unsupported" || outcome == "degraded" {
		severity := "warning"
		if outcome == "unsupported" {
			severity = "error"
		}
		if err := store.InsertFinding(ctx, workspace.FindingRecord{
			DatasetID: state.datasetID, Severity: severity, SeverityRank: severityRank(severity),
			Code: "semantic_outcome_gap", Message: fmt.Sprintf("%s/%s outcome=%s", firstNonEmpty(event.Capability, "unknown"), firstNonEmpty(event.Operation, "unknown"), outcome),
			TraceKey: state.traceKey, FirstIngestOrder: event.IngestOrder,
		}); err != nil {
			return err
		}
	}
	if isTechnicalSuccess(event.Status) && implementation != "" && implementation != "implemented" && implementation != "unknown" {
		return store.InsertFinding(ctx, workspace.FindingRecord{
			DatasetID: state.datasetID, Severity: "warning", SeverityRank: severityRank("warning"),
			Code: "technical_success_without_capability_success", Message: fmt.Sprintf("%s/%s 技术状态成功，但 implementation=%s", firstNonEmpty(event.Capability, "unknown"), firstNonEmpty(event.Operation, "unknown"), implementation),
			TraceKey: state.traceKey, FirstIngestOrder: event.IngestOrder,
		})
	}
	return nil
}

func semanticOperationKey(event workspace.EventRecord) string {
	operation := strings.TrimSpace(event.Operation)
	if operation == "" || strings.TrimSpace(event.SemanticOutcome) == "" || event.SemanticOutcome == "unknown" {
		return ""
	}
	capability := firstNonEmpty(event.Capability, "unknown")
	correlation := firstNonEmpty(event.ToolCallID, event.ModelCallID, event.TurnID, event.HTTPRequestID, event.CursorRequestID)
	if correlation != "" {
		return capability + ":" + correlation
	}
	return capability + ":" + operation + ":" + firstNonEmpty(event.Route, "trace")
}

func isSemanticTerminal(outcome string) bool {
	switch outcome {
	case "succeeded", "failed", "canceled", "timeout", "degraded", "unsupported", "partial", "compat_only":
		return true
	default:
		return false
	}
}

func isSemanticFailure(outcome string) bool {
	return outcome == "failed" || outcome == "timeout" || outcome == "unsupported"
}

func isTechnicalSuccess(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "ok", "success", "succeeded", "completed", "complete", "finished":
		return true
	default:
		return false
	}
}

func (state *traceState) addPair(key string, starts int, finishes int) {
	counts := state.pairs[key]
	if counts == nil {
		counts = &pairCounts{}
		state.pairs[key] = counts
	}
	counts.starts += starts
	counts.finishes += finishes
}

func (state *traceState) addTool(key string, starts int, finishes int) {
	counts := state.tools[key]
	if counts == nil {
		counts = &pairCounts{}
		state.tools[key] = counts
	}
	counts.starts += starts
	counts.finishes += finishes
}

func (state *traceState) flushPairs(ctx context.Context, store *workspace.Workspace) error {
	records := pairRecords(state.pairs)
	state.pairs = make(map[string]*pairCounts)
	return store.UpsertTracePairStates(ctx, state.datasetID, state.traceKey, records)
}

func (state *traceState) flushTools(ctx context.Context, store *workspace.Workspace) error {
	records := pairRecords(state.tools)
	state.tools = make(map[string]*pairCounts)
	return store.UpsertTraceToolStates(ctx, state.datasetID, state.traceKey, records)
}

func (state *traceState) flushLabels(ctx context.Context, store *workspace.Workspace) error {
	layers := sortedKeys(state.layers)
	targets := sortedKeys(state.targets)
	state.layers = make(map[string]struct{})
	state.targets = make(map[string]struct{})
	return store.InsertTraceLabels(ctx, state.datasetID, state.traceKey, layers, targets)
}

func finalizeTrace(ctx context.Context, store *workspace.Workspace, state *traceState) error {
	if err := state.flushLabels(ctx, store); err != nil {
		return err
	}
	if err := state.flushPairs(ctx, store); err != nil {
		return err
	}
	if err := state.flushTools(ctx, store); err != nil {
		return err
	}
	if err := insertPairStateFindings(ctx, store, state); err != nil {
		return err
	}
	if err := insertToolStateFindings(ctx, store, state); err != nil {
		return err
	}
	if err := insertTraceStateFindings(ctx, store, state); err != nil {
		return err
	}
	if err := store.InsertTraceSummary(ctx, workspace.TraceSummaryRecord{
		DatasetID:        state.datasetID,
		TraceKey:         state.traceKey,
		EventCount:       state.eventCount,
		StartedAt:        state.startedAt.UTC().Format(timeFormat),
		FinishedAt:       state.finishedAt.UTC().Format(timeFormat),
		DurationMS:       state.finishedAt.Sub(state.startedAt).Milliseconds(),
		HasError:         state.hasError,
		FirstIngestOrder: state.firstIngestOrder,
		LastIngestOrder:  state.lastIngestOrder,
	}); err != nil {
		return err
	}
	return store.DeleteTraceScratch(ctx, state.datasetID, state.traceKey)
}

func insertTraceStateFindings(ctx context.Context, store *workspace.Workspace, state *traceState) error {
	if strings.HasPrefix(state.traceKey, "orphan:") {
		if err := store.InsertFinding(ctx, workspace.FindingRecord{DatasetID: state.datasetID, Severity: "warning", SeverityRank: severityRank("warning"), Code: "orphan_event", Message: "事件缺少 trace_id，无法重建跨层链路", TraceKey: state.traceKey, Count: state.eventCount, FirstIngestOrder: state.firstIngestOrder}); err != nil {
			return err
		}
	}
	if state.providerTerminal && state.runsseStarted && !state.runsseTerminal {
		if err := store.InsertFinding(ctx, workspace.FindingRecord{DatasetID: state.datasetID, Severity: "error", SeverityRank: severityRank("error"), Code: "runsse_terminal_missing", Message: "provider 已产生终态，但 RunSSE 未产生终态", TraceKey: state.traceKey, Count: 1, FirstIngestOrder: state.firstIngestOrder}); err != nil {
			return err
		}
	}
	if state.insufficient > 0 {
		if err := store.InsertFinding(ctx, workspace.FindingRecord{DatasetID: state.datasetID, Severity: "info", SeverityRank: severityRank("info"), Code: "insufficient_evidence", Message: fmt.Sprintf("%d 个语义事件缺少可判定 outcome，需补采集终态", state.insufficient), TraceKey: state.traceKey, Count: state.insufficient, FirstIngestOrder: state.firstIngestOrder}); err != nil {
			return err
		}
	}
	return nil
}

func insertPairStateFindings(ctx context.Context, store *workspace.Workspace, state *traceState) error {
	var after string
	for {
		pairs, err := store.ListTracePairStates(ctx, state.datasetID, state.traceKey, after, eventBatchLimit)
		if err != nil {
			return err
		}
		if len(pairs) == 0 {
			return nil
		}
		for _, item := range pairs {
			if item.Finishes < item.Starts {
				missing := item.Starts - item.Finishes
				code := "missing_terminal"
				message := fmt.Sprintf("%s 缺少 %d 个终态事件", item.Key, missing)
				if strings.HasPrefix(item.Key, "operation:") {
					code = "operation_terminal_missing"
					message = fmt.Sprintf("%s 开始后缺少语义终态", strings.TrimPrefix(item.Key, "operation:"))
				}
				if strings.HasPrefix(item.Key, "continued_after_cancel:") {
					code = "continued_after_cancel"
					message = fmt.Sprintf("%s 取消后仍继续输出", strings.TrimPrefix(item.Key, "continued_after_cancel:"))
				}
				if err := store.InsertFinding(ctx, workspace.FindingRecord{DatasetID: state.datasetID, Severity: "error", SeverityRank: severityRank("error"), Code: code, Message: message, TraceKey: state.traceKey, Count: missing, FirstIngestOrder: state.firstIngestOrder}); err != nil {
					return err
				}
			}
			if item.Starts > 1 && strings.HasPrefix(item.Key, "upstream:") {
				if err := store.InsertFinding(ctx, workspace.FindingRecord{DatasetID: state.datasetID, Severity: "info", SeverityRank: severityRank("info"), Code: "retry_or_duplicate", Message: fmt.Sprintf("%s 出现 %d 次开始事件", item.Key, item.Starts), TraceKey: state.traceKey, Count: item.Starts, FirstIngestOrder: state.firstIngestOrder}); err != nil {
					return err
				}
			}
		}
		after = pairs[len(pairs)-1].Key
	}
}

func insertToolStateFindings(ctx context.Context, store *workspace.Workspace, state *traceState) error {
	var after string
	for {
		tools, err := store.ListTraceToolStates(ctx, state.datasetID, state.traceKey, after, eventBatchLimit)
		if err != nil {
			return err
		}
		if len(tools) == 0 {
			return nil
		}
		for _, item := range tools {
			if item.Key != "" && item.Finishes < item.Starts {
				missing := item.Starts - item.Finishes
				if err := store.InsertFinding(ctx, workspace.FindingRecord{DatasetID: state.datasetID, Severity: "error", SeverityRank: severityRank("error"), Code: "tool_result_missing", Message: "工具调用缺少结果", TraceKey: state.traceKey, Count: missing, FirstIngestOrder: state.firstIngestOrder}); err != nil {
					return err
				}
			}
		}
		after = tools[len(tools)-1].Key
	}
}

func pairRecords(input map[string]*pairCounts) []workspace.PairStateRecord {
	records := make([]workspace.PairStateRecord, 0, len(input))
	for key, counts := range input {
		records = append(records, workspace.PairStateRecord{Key: key, Starts: counts.starts, Finishes: counts.finishes})
	}
	return records
}

func newTargetBuffer(datasetID int64) *targetBuffer {
	return &targetBuffer{datasetID: datasetID, values: make(map[string]*targetAggregate)}
}

func (buffer *targetBuffer) add(ctx context.Context, store *workspace.Workspace, event workspace.EventRecord) error {
	target := firstNonEmpty(event.ExecutionTarget, "unspecified")
	aggregate := buffer.values[target]
	if aggregate == nil {
		aggregate = &targetAggregate{}
		buffer.values[target] = aggregate
	}
	aggregate.events++
	aggregate.requestBytes += event.RequestBytes
	aggregate.responseBytes += event.ResponseBytes
	if isFinished(event.Event) {
		aggregate.finished++
		aggregate.durationTotalMS += event.DurationMS
		if event.Status == "error" || event.ErrorCategory != "" {
			aggregate.errors++
		}
	}
	if len(buffer.values) >= stateFlushLimit {
		return buffer.flush(ctx, store)
	}
	return nil
}

func (buffer *targetBuffer) flush(ctx context.Context, store *workspace.Workspace) error {
	keys := make([]string, 0, len(buffer.values))
	for key := range buffer.values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		aggregate := buffer.values[key]
		if err := store.InsertTargetSummary(ctx, workspace.TargetSummaryRecord{DatasetID: buffer.datasetID, Target: key, Events: aggregate.events, Finished: aggregate.finished, Errors: aggregate.errors, DurationTotalMS: aggregate.durationTotalMS, RequestBytes: aggregate.requestBytes, ResponseBytes: aggregate.responseBytes}); err != nil {
			return err
		}
	}
	buffer.values = make(map[string]*targetAggregate)
	return nil
}

func compareTargets(ctx context.Context, store *workspace.Workspace, currentID int64, baselineID int64) error {
	return store.InsertComparisonsFromTargetSummaries(ctx, currentID, baselineID)
}

func errorRate(record workspace.TargetSummaryRecord) float64 {
	if record.Finished == 0 {
		return 0
	}
	return float64(record.Errors) / float64(record.Finished)
}

func averageDuration(record workspace.TargetSummaryRecord) float64 {
	if record.Finished == 0 {
		return 0
	}
	return float64(record.DurationTotalMS) / float64(record.Finished)
}

func isFinished(name string) bool {
	switch name {
	case "request_finished", "backend_forward_finished", "llm_summary", "terminal", "terminal_after_context_done":
		return true
	default:
		return false
	}
}

func sortedKeys(values map[string]struct{}) []string {
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func severityRank(value string) int {
	switch value {
	case "error":
		return 3
	case "warning":
		return 2
	case "info":
		return 1
	default:
		return 0
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
