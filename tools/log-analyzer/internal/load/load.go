package load

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"cursor-log-analyzer/internal/contract"
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
	state := &ingestState{store: store, datasetID: datasetID, options: normalizeOptions(options)}
	for ordinal, input := range inputs {
		path, info, err := resolveInput(input)
		if err != nil {
			return err
		}
		argumentID, err := store.InsertInputArgument(ctx, datasetID, ordinal, path)
		if err != nil {
			return err
		}
		if err := discoverFiles(path, info, func(file string, fileKind workspace.FileKind) error {
			fileID, inserted, err := store.UpsertInputFile(ctx, datasetID, argumentID, file, fileKind)
			if err != nil {
				return err
			}
			if !inserted {
				return nil
			}
			switch fileKind {
			case workspace.FileEvents:
				return state.ingestEvents(ctx, fileID, file)
			case workspace.FileManifest:
				return state.ingestManifest(ctx, fileID, file)
			case workspace.FileAppLog:
				return state.ingestAppLog(ctx, fileID, file)
			default:
				return fmt.Errorf("unsupported input file %s", file)
			}
		}); err != nil {
			return err
		}
	}
	if err := state.flush(ctx); err != nil {
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
		safeFields, err := safeFieldsJSON(event.Fields)
		if err != nil {
			return fmt.Errorf("encode safe fields %s:%d: %w", path, lineNumber, err)
		}
		state.ingestOrder++
		record := eventRecord(state.datasetID, fileID, lineNumber, state.ingestOrder, event, safeFields)
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

func discoverFiles(path string, info fs.FileInfo, visit func(string, workspace.FileKind) error) error {
	if !info.IsDir() {
		kind, ok := inputFileKind(path)
		if !ok {
			return fmt.Errorf("unsupported input file %s", path)
		}
		return visit(path, kind)
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
		if !ok {
			return nil
		}
		return visit(current, kind)
	})
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
