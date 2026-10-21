package load

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"cursor-log-analyzer/internal/contract"
	"cursor-log-analyzer/internal/sanitize"
	"cursor-log-analyzer/internal/workspace"
)

const (
	defaultBatchEventLimit = 5000
	defaultBatchByteLimit  = 32 << 20
	defaultEventLineLimit  = 8 << 20
	defaultManifestLimit   = 1 << 20
	defaultAppLogLineLimit = 1 << 20
)

var errLineTooLarge = errors.New("line exceeds limit")

type Options struct {
	AllowUnknownSchema bool
	BatchEventLimit    int
	BatchByteLimit     int
	MaxEventLineBytes  int
	MaxManifestBytes   int
	MaxAppLogLineBytes int
}

type ingestOptions struct {
	allowUnknownSchema bool
	batchEventLimit    int
	batchByteLimit     int
	maxEventLineBytes  int
	maxManifestBytes   int
	maxAppLogLineBytes int
}

type ingestState struct {
	store          *workspace.Workspace
	datasetID      int64
	options        ingestOptions
	batch          []workspace.EventRecord
	batchBytes     int
	ingestOrder    int64
	warningOrdinal int
	seen           map[string][32]byte
	// traceSessions and diagnosticSessions track which app sessions contributed
	// trace events versus diagnostics-only copies, so a session known only from
	// diagnostics shards can be flagged as partial material. Diagnostics entries
	// without an app_session_id cannot be matched to any trace, so they are
	// tracked as a single conservative unknown bucket.
	traceSessions        map[string]struct{}
	diagnosticSessions   map[string]struct{}
	diagnosticUnassigned bool
}

func IntoWorkspace(ctx context.Context, store *workspace.Workspace, kind workspace.DatasetKind, inputs []string, options Options) error {
	if store == nil {
		return errors.New("workspace is required")
	}
	if len(inputs) == 0 {
		return errors.New("at least one input is required")
	}
	datasetID, err := store.DatasetID(ctx, kind)
	if err != nil {
		return err
	}
	state := &ingestState{
		store:              store,
		datasetID:          datasetID,
		options:            normalizeOptions(options),
		seen:               make(map[string][32]byte),
		traceSessions:      make(map[string]struct{}),
		diagnosticSessions: make(map[string]struct{}),
	}
	// Collect every discovered file across all input arguments before ingesting
	// anything, preserving each file's owning argument for UpsertInputFile
	// dedup. Sorting then applies the trace-before-diagnostics preference
	// dataset-wide, so a diagnostics copy from an earlier argument can no
	// longer fold away the trace copy's payload_ref.
	files := make([]discoveredFile, 0)
	for ordinal, input := range inputs {
		path, info, err := resolveInput(input)
		if err != nil {
			return err
		}
		argumentID, err := store.InsertInputArgument(ctx, datasetID, ordinal, path)
		if err != nil {
			return err
		}
		start := len(files)
		if err := discoverFiles(path, info, &files); err != nil {
			return err
		}
		for index := start; index < len(files); index++ {
			files[index].argumentID = argumentID
		}
	}
	sort.SliceStable(files, func(left int, right int) bool {
		leftPriority := discoveryPriority(files[left])
		rightPriority := discoveryPriority(files[right])
		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}
		return files[left].path < files[right].path
	})
	for _, file := range files {
		fileID, inserted, err := store.UpsertInputFile(ctx, datasetID, file.argumentID, file.path, file.kind)
		if err != nil {
			return err
		}
		if !inserted {
			continue
		}
		switch file.kind {
		case workspace.FileEvents:
			if err := state.ingestEvents(ctx, fileID, file.path); err != nil {
				return err
			}
		case workspace.FileManifest:
			if err := state.ingestManifest(ctx, fileID, file.path); err != nil {
				return err
			}
		case workspace.FileAppLog:
			if err := state.ingestAppLog(ctx, fileID, file.path); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported input file %s", file.path)
		}
	}
	if err := state.flush(ctx); err != nil {
		return err
	}
	if err := state.warnIncompleteDiagnostics(ctx); err != nil {
		return err
	}
	count, err := store.EventCount(ctx, datasetID)
	if err != nil {
		return err
	}
	if count == 0 {
		return errors.New("no events.jsonl records found")
	}
	return nil
}

func (state *ingestState) ingestEvents(ctx context.Context, fileID int64, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	lineNumber := 0
	for {
		line, readErr := readLine(reader, state.options.maxEventLineBytes)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if errors.Is(readErr, errLineTooLarge) {
				return fmt.Errorf("read %s:%d: line exceeds %d bytes", path, lineNumber+1, state.options.maxEventLineBytes)
			}
			return fmt.Errorf("read %s:%d: %w", path, lineNumber+1, readErr)
		}
		lineNumber++
		trimmed := bytes.TrimSpace(line)
		if len(trimmed) == 0 {
			continue
		}
		var event contract.Event
		if err := json.Unmarshal(trimmed, &event); err != nil {
			return fmt.Errorf("decode %s:%d: %w", path, lineNumber, err)
		}
		if err := validateVersion(event.SchemaVersion, state.options.allowUnknownSchema); err != nil {
			return fmt.Errorf("validate %s:%d: %w", path, lineNumber, err)
		}
		if !contract.IsSupportedSchemaVersion(event.SchemaVersion) {
			if err := state.addWarning(ctx, fmt.Sprintf("%s:%d uses unknown schema_version=%d", path, lineNumber, event.SchemaVersion)); err != nil {
				return err
			}
		}
		if strings.TrimSpace(event.Layer) == "" || strings.TrimSpace(event.Event) == "" {
			return fmt.Errorf("validate %s:%d: layer and event are required", path, lineNumber)
		}
		if err := contract.ValidateEventSemantics(event); err != nil {
			return fmt.Errorf("validate %s:%d: %w", path, lineNumber, err)
		}
		if sessionID := strings.TrimSpace(event.AppSessionID); sessionID != "" {
			if isDiagnosticsEventsFile(path) {
				state.diagnosticSessions[sessionID] = struct{}{}
			} else {
				state.traceSessions[sessionID] = struct{}{}
			}
		} else if isDiagnosticsEventsFile(path) {
			state.diagnosticUnassigned = true
		}
		safeFields, err := safeFieldsJSON(event.Fields)
		if err != nil {
			return fmt.Errorf("encode safe fields %s:%d: %w", path, lineNumber, err)
		}
		record := eventRecord(state.datasetID, fileID, lineNumber, 0, event, safeFields)
		duplicate, err := state.duplicate(path, lineNumber, event)
		if err != nil {
			return err
		}
		if duplicate {
			continue
		}
		state.ingestOrder++
		record.IngestOrder = state.ingestOrder
		if err := state.queue(ctx, record, len(line)); err != nil {
			return err
		}
	}
	return nil
}

func (state *ingestState) ingestManifest(ctx context.Context, fileID int64, path string) error {
	manifest, warning, err := readManifest(path, state.options)
	if err != nil {
		return err
	}
	if warning != "" {
		if err := state.addWarning(ctx, warning); err != nil {
			return err
		}
	}
	_, err = state.store.InsertManifest(ctx, workspace.ManifestRecord{
		DatasetID:         state.datasetID,
		InputFileID:       fileID,
		SchemaVersion:     manifest.SchemaVersion,
		AppSessionID:      manifest.AppSessionID,
		Mode:              manifest.Mode,
		Status:            manifest.Status,
		StartedAt:         manifest.StartedAt,
		ClosedAt:          manifest.ClosedAt,
		PayloadDegraded:   manifest.PayloadDegraded,
		DroppedEvents:     manifest.DroppedEvents,
		LastError:         manifest.LastError,
		SourceKind:        manifest.SourceKind,
		AppVersion:        manifest.AppVersion,
		BuildID:           manifest.BuildID,
		Platform:          manifest.Platform,
		ConfigFingerprint: manifest.ConfigFingerprint,
	})
	return err
}

func (state *ingestState) ingestAppLog(ctx context.Context, fileID int64, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	reader := bufio.NewReader(file)
	batch := make([]workspace.AppLogRecord, 0, state.options.batchEventLimit)
	lineNumber := 0
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if err := state.store.InsertAppLogLines(ctx, batch); err != nil {
			return err
		}
		batch = batch[:0]
		return nil
	}
	for {
		line, readErr := readLine(reader, state.options.maxAppLogLineBytes)
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			if errors.Is(readErr, errLineTooLarge) {
				return fmt.Errorf("read %s:%d: line exceeds %d bytes", path, lineNumber+1, state.options.maxAppLogLineBytes)
			}
			return fmt.Errorf("read %s:%d: %w", path, lineNumber+1, readErr)
		}
		lineNumber++
		message := strings.TrimSpace(string(line))
		if message == "" {
			continue
		}
		timestamp, severity := appLogMetadata(message)
		batch = append(batch, workspace.AppLogRecord{
			DatasetID: state.datasetID, InputFileID: fileID, LineNumber: lineNumber,
			TimestampText: timestamp, Severity: severity, Message: message,
		})
		if len(batch) >= state.options.batchEventLimit {
			if err := flush(); err != nil {
				return err
			}
		}
	}
	return flush()
}

func appLogMetadata(message string) (string, string) {
	fields := strings.Fields(message)
	severity := ""
	for _, field := range fields {
		switch strings.ToUpper(strings.Trim(field, "[]:")) {
		case "DBG", "DEBUG":
			severity = "debug"
		case "INF", "INFO":
			severity = "info"
		case "WRN", "WARN", "WARNING":
			severity = "warning"
		case "ERR", "ERROR":
			severity = "error"
		}
		if severity != "" {
			break
		}
	}
	timestamp := ""
	for length := 1; length <= 2 && length <= len(fields); length++ {
		candidate := strings.Join(fields[:length], " ")
		for _, layout := range []string{time.RFC3339Nano, "2006/01/02 15:04:05.000", "2006/01/02 15:04:05", "15:04:05.000", "15:04:05"} {
			if _, err := time.Parse(layout, candidate); err == nil {
				timestamp = candidate
			}
		}
	}
	return timestamp, severity
}

func (state *ingestState) addWarning(ctx context.Context, message string) error {
	_, err := state.store.InsertWarning(ctx, workspace.WarningRecord{DatasetID: state.datasetID, Ordinal: state.warningOrdinal, Message: message})
	if err != nil {
		return err
	}
	state.warningOrdinal++
	return nil
}

// warnIncompleteDiagnostics records one dataset warning when any app session was
// seen only in diagnostics shards, or when diagnostics entries had no
// app_session_id at all. Diagnostics material omits the trace and payload
// context, so counts derived from it are partial; the warning states that
// explicitly and reuses the existing dataset warnings table rather than adding a
// schema field. A session with any trace event is left alone. The message is
// aggregated and carries no raw session identifiers, and it is bounded by the
// existing sanitize.Summary rules (URL/credential stripping and a 512-rune cap).
// The warning never claims a full success rate or an inferred recovery.
func (state *ingestState) warnIncompleteDiagnostics(ctx context.Context) error {
	if len(state.diagnosticSessions) == 0 && !state.diagnosticUnassigned {
		return nil
	}
	affected := 0
	for session := range state.diagnosticSessions {
		if _, traced := state.traceSessions[session]; traced {
			continue
		}
		affected++
	}
	if affected == 0 && !state.diagnosticUnassigned {
		return nil
	}
	message := fmt.Sprintf("检测到 %d 个仅提供异常诊断分片的会话", affected)
	if state.diagnosticUnassigned {
		message += "（另含无法归属会话的诊断记录）"
	}
	message += "，材料不完整：统计仅覆盖已提供的事件，不推断全量成功率或恢复"
	return state.addWarning(ctx, sanitize.Summary(message))
}

// duplicate folds only copies of the same event, identified strictly by
// (app_session_id, sequence) within the dataset. Records missing either half of
// that identity keep their original behavior. The fingerprint is computed from
// the raw contract.Event before the consumer's lossy sanitize, so a policy
// projection difference cannot hide a real content conflict. Only payload_ref
// is normalized away, matching the approved rule that a diagnostics copy (a full
// event copy minus payload_ref) may fold into its trace copy. Only the digest is
// retained in memory; raw fields never persist beyond the line being read.
func (state *ingestState) duplicate(path string, lineNumber int, event contract.Event) (bool, error) {
	sessionID := strings.TrimSpace(event.AppSessionID)
	if sessionID == "" || event.Sequence == 0 {
		return false, nil
	}
	key := sessionID + "\x00" + strconv.FormatUint(event.Sequence, 10)
	fingerprint := eventFingerprint(event)
	previous, ok := state.seen[key]
	if !ok {
		state.seen[key] = fingerprint
		return false, nil
	}
	if previous != fingerprint {
		return false, fmt.Errorf("conflicting duplicate event %s:%d: app_session_id=%q sequence=%d event=%q already loaded with different content", path, lineNumber, sessionID, event.Sequence, event.Event)
	}
	return true, nil
}

// eventFingerprint hashes the raw event metadata and fields. PayloadRef is
// cleared first because the diagnostics producer stores a full copy of the event
// with payload_ref removed. json.Marshal sorts map keys, so field order in the
// source JSON is irrelevant. The digest is one-way, so a conflicting secret or
// URL is detected by comparison without being stored or reported.
func eventFingerprint(event contract.Event) [32]byte {
	event.PayloadRef = ""
	payload, err := json.Marshal(event)
	if err != nil {
		return sha256.Sum256([]byte(fmt.Sprintf("%+v", event)))
	}
	return sha256.Sum256(payload)
}

func (state *ingestState) queue(ctx context.Context, record workspace.EventRecord, lineBytes int) error {
	state.batch = append(state.batch, record)
	state.batchBytes += lineBytes
	if len(state.batch) >= state.options.batchEventLimit || state.batchBytes >= state.options.batchByteLimit {
		return state.flush(ctx)
	}
	return nil
}

func (state *ingestState) flush(ctx context.Context) error {
	if len(state.batch) == 0 {
		return nil
	}
	if err := state.store.InsertEvents(ctx, state.batch); err != nil {
		return err
	}
	state.batch = state.batch[:0]
	state.batchBytes = 0
	return nil
}

type discoveredFile struct {
	argumentID int64
	path       string
	kind       workspace.FileKind
}

// discoverFiles appends every supported file under path to files, tagged with
// the owning input argument. Ordering and ingestion happen in IntoWorkspace once
// all arguments are known, so the trace-before-diagnostics preference applies
// dataset-wide.
func discoverFiles(path string, info fs.FileInfo, files *[]discoveredFile) error {
	if !info.IsDir() {
		kind, ok := inputFileKind(path)
		if !ok {
			return fmt.Errorf("unsupported input file %s", path)
		}
		*files = append(*files, discoveredFile{path: path, kind: kind})
		return nil
	}
	return filepath.WalkDir(path, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Type()&os.ModeSymlink != 0 {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.IsDir() {
			return nil
		}
		kind, ok := inputFileKind(current)
		if ok {
			*files = append(*files, discoveredFile{path: current, kind: kind})
		}
		return nil
	})
}

func discoveryPriority(file discoveredFile) int {
	if file.kind == workspace.FileEvents && !isDiagnosticsEventsFile(file.path) {
		return 0
	}
	if file.kind == workspace.FileManifest {
		return 1
	}
	if file.kind == workspace.FileAppLog {
		return 2
	}
	return 3
}

// isDiagnosticsEventsFile reports whether a FileEvents path is a diagnostics
// shard, which the consumer treats as a partial copy of a trace event stream.
func isDiagnosticsEventsFile(path string) bool {
	name := filepath.Base(path)
	return strings.HasPrefix(name, "diagnostics-") && strings.HasSuffix(name, ".jsonl")
}

func readManifest(path string, options ingestOptions) (contract.Manifest, string, error) {
	payload, err := readBoundedFile(path, options.maxManifestBytes)
	if err != nil {
		return contract.Manifest{}, "", err
	}
	var manifest contract.Manifest
	if err := json.Unmarshal(payload, &manifest); err != nil {
		return contract.Manifest{}, "", fmt.Errorf("decode %s: %w", path, err)
	}
	if err := validateVersion(manifest.SchemaVersion, options.allowUnknownSchema); err != nil {
		return contract.Manifest{}, "", fmt.Errorf("validate %s: %w", path, err)
	}
	warning := ""
	if !contract.IsSupportedSchemaVersion(manifest.SchemaVersion) {
		warning = fmt.Sprintf("%s uses unknown schema_version=%d", path, manifest.SchemaVersion)
	}
	if err := contract.ValidateManifestSemantics(manifest); err != nil {
		return contract.Manifest{}, "", fmt.Errorf("validate %s: %w", path, err)
	}
	return manifest, warning, nil
}

func readLine(reader *bufio.Reader, limit int) ([]byte, error) {
	line := make([]byte, 0)
	for {
		fragment, err := reader.ReadSlice('\n')
		if len(fragment) > 0 {
			if len(line)+len(fragment) > limit {
				return nil, errLineTooLarge
			}
			line = append(line, fragment...)
		}
		switch {
		case err == nil:
			return line, nil
		case errors.Is(err, bufio.ErrBufferFull):
			continue
		case errors.Is(err, io.EOF):
			if len(line) == 0 {
				return nil, io.EOF
			}
			return line, nil
		default:
			return nil, err
		}
	}
}

func readBoundedFile(path string, limit int) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	payload, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(payload) > limit {
		return nil, fmt.Errorf("read %s: file exceeds %d bytes", path, limit)
	}
	return payload, nil
}

func eventRecord(datasetID int64, fileID int64, lineNumber int, ingestOrder int64, event contract.Event, safeFields string) workspace.EventRecord {
	return workspace.EventRecord{
		DatasetID:           datasetID,
		SourceFileID:        fileID,
		LineNumber:          lineNumber,
		Timestamp:           event.Timestamp,
		Sequence:            event.Sequence,
		IngestOrder:         ingestOrder,
		TraceKey:            traceKey(event),
		SchemaVersion:       event.SchemaVersion,
		AppSessionID:        event.AppSessionID,
		ProjectID:           event.ProjectID,
		TraceID:             event.TraceID,
		SpanID:              event.SpanID,
		ParentSpanID:        event.ParentSpanID,
		HTTPRequestID:       event.HTTPRequestID,
		CursorRequestID:     event.CursorRequestID,
		ConversationID:      event.ConversationID,
		SubagentRunID:       event.SubagentRunID,
		SubagentAttemptID:   event.SubagentAttemptID,
		SubagentAttemptNo:   event.SubagentAttemptNo,
		TurnID:              event.TurnID,
		TurnSequence:        event.TurnSequence,
		ModelCallID:         event.ModelCallID,
		ToolCallID:          event.ToolCallID,
		Layer:               event.Layer,
		Event:               event.Event,
		Capability:          event.Capability,
		Operation:           event.Operation,
		Direction:           event.Direction,
		Route:               event.Route,
		ExecutionTarget:     event.ExecutionTarget,
		Protocol:            event.Protocol,
		Status:              event.Status,
		SemanticOutcome:     event.SemanticOutcome,
		ImplementationState: event.ImplementationState,
		Severity:            event.Severity,
		ErrorCategory:       event.ErrorCategory,
		DurationMS:          event.DurationMS,
		RequestBytes:        event.RequestBytes,
		ResponseBytes:       event.ResponseBytes,
		DecodeError:         event.DecodeError,
		DroppedEvents:       event.DroppedEvents,
		SafeFieldsJSON:      safeFields,
		PayloadRef:          event.PayloadRef,
	}
}

func traceKey(event contract.Event) string {
	if value := strings.TrimSpace(event.TraceID); value != "" {
		return value
	}
	return fmt.Sprintf("orphan:%s:%d", event.AppSessionID, event.Sequence)
}

func safeFieldsJSON(input map[string]any) (string, error) {
	fields := allowlistedFields(input)
	if len(fields) == 0 {
		return "", nil
	}
	payload, err := json.Marshal(fields)
	if err != nil {
		return "", err
	}
	return string(payload), nil
}

func allowlistedFields(input map[string]any) map[string]any {
	return sanitize.AllowlistedFields(input)
}

func inputFileKind(path string) (workspace.FileKind, bool) {
	name := filepath.Base(path)
	switch name {
	case "events.jsonl":
		return workspace.FileEvents, true
	case "manifest.json":
		return workspace.FileManifest, true
	case "app.log":
		return workspace.FileAppLog, true
	default:
		if strings.HasPrefix(name, "app-") && strings.HasSuffix(name, ".log") {
			return workspace.FileAppLog, true
		}
		if strings.HasPrefix(name, "diagnostics-") && strings.HasSuffix(name, ".jsonl") {
			return workspace.FileEvents, true
		}
		return "", false
	}
}

func resolveInput(input string) (string, fs.FileInfo, error) {
	path, err := filepath.Abs(strings.TrimSpace(input))
	if err != nil {
		return "", nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", nil, fmt.Errorf("read input %s: %w", path, err)
	}
	return path, info, nil
}

func normalizeOptions(options Options) ingestOptions {
	result := ingestOptions{
		allowUnknownSchema: options.AllowUnknownSchema,
		batchEventLimit:    options.BatchEventLimit,
		batchByteLimit:     options.BatchByteLimit,
		maxEventLineBytes:  options.MaxEventLineBytes,
		maxManifestBytes:   options.MaxManifestBytes,
		maxAppLogLineBytes: options.MaxAppLogLineBytes,
	}
	if result.batchEventLimit <= 0 {
		result.batchEventLimit = defaultBatchEventLimit
	}
	if result.batchByteLimit <= 0 {
		result.batchByteLimit = defaultBatchByteLimit
	}
	if result.maxEventLineBytes <= 0 {
		result.maxEventLineBytes = defaultEventLineLimit
	}
	if result.maxManifestBytes <= 0 {
		result.maxManifestBytes = defaultManifestLimit
	}
	if result.maxAppLogLineBytes <= 0 {
		result.maxAppLogLineBytes = defaultAppLogLineLimit
	}
	return result
}

func validateVersion(version int, allowUnknown bool) error {
	if contract.IsSupportedSchemaVersion(version) {
		return nil
	}
	if allowUnknown && version > 0 {
		return nil
	}
	return fmt.Errorf("unsupported schema_version=%d (supported=%d..%d)", version, contract.MinimumSupportedSchemaVersion, contract.SupportedSchemaVersion)
}
