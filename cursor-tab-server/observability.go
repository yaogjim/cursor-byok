package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const relaySchemaVersion = 1

type relayCorrelation struct {
	TraceID       string
	SpanID        string
	ParentSpanID  string
	HTTPRequestID string
}

type relayEvent struct {
	SchemaVersion   int            `json:"schema_version"`
	Timestamp       time.Time      `json:"timestamp"`
	Sequence        uint64         `json:"sequence"`
	AppSessionID    string         `json:"app_session_id"`
	TraceID         string         `json:"trace_id,omitempty"`
	SpanID          string         `json:"span_id,omitempty"`
	ParentSpanID    string         `json:"parent_span_id,omitempty"`
	HTTPRequestID   string         `json:"http_request_id,omitempty"`
	Layer           string         `json:"layer"`
	Event           string         `json:"event"`
	Route           string         `json:"route,omitempty"`
	ExecutionTarget string         `json:"execution_target,omitempty"`
	Protocol        string         `json:"protocol,omitempty"`
	Status          string         `json:"status,omitempty"`
	ErrorCategory   string         `json:"error_category,omitempty"`
	DurationMS      int64          `json:"duration_ms,omitempty"`
	RequestBytes    int64          `json:"request_bytes,omitempty"`
	ResponseBytes   int64          `json:"response_bytes,omitempty"`
	Fields          map[string]any `json:"fields,omitempty"`
}

type relayManifest struct {
	SchemaVersion int        `json:"schema_version"`
	AppSessionID  string     `json:"app_session_id"`
	Mode          string     `json:"mode"`
	Status        string     `json:"status"`
	StartedAt     time.Time  `json:"started_at"`
	ClosedAt      *time.Time `json:"closed_at,omitempty"`
}

type relayRecorder struct {
	mu        sync.Mutex
	sessionID string
	dir       string
	startedAt time.Time
	file      *os.File
	sequence  uint64
}

func openRelayRecorder(root string) (*relayRecorder, error) {
	root = filepath.Clean(strings.TrimSpace(root))
	if root == "" || root == "." {
		return nil, errors.New("relay log root is required")
	}
	tracesRoot := filepath.Join(root, "traces")
	if err := ensureRelayDirectory(tracesRoot); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	sessionID := now.Format("20060102T150405.000000000Z") + "-" + relayRandomID(6)
	dir := filepath.Join(tracesRoot, sessionID)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, err
	}
	manifest, err := json.MarshalIndent(relayManifest{
		SchemaVersion: relaySchemaVersion,
		AppSessionID:  sessionID,
		Mode:          "basic",
		Status:        "open",
		StartedAt:     now,
	}, "", "  ")
	if err != nil {
		_ = file.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(manifest, '\n'), 0o600); err != nil {
		_ = file.Close()
		_ = os.RemoveAll(dir)
		return nil, err
	}
	return &relayRecorder{sessionID: sessionID, dir: dir, startedAt: now, file: file}, nil
}

func ensureRelayDirectory(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func (recorder *relayRecorder) Close() error {
	if recorder == nil {
		return nil
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.file == nil {
		return nil
	}
	closeErr := recorder.file.Close()
	recorder.file = nil
	closedAt := time.Now().UTC()
	manifest, marshalErr := json.MarshalIndent(relayManifest{
		SchemaVersion: relaySchemaVersion,
		AppSessionID:  recorder.sessionID,
		Mode:          "basic",
		Status:        "closed",
		StartedAt:     recorder.startedAt,
		ClosedAt:      &closedAt,
	}, "", "  ")
	if marshalErr != nil {
		return errors.Join(closeErr, marshalErr)
	}
	writeErr := os.WriteFile(filepath.Join(recorder.dir, "manifest.json"), append(manifest, '\n'), 0o600)
	return errors.Join(closeErr, writeErr)
}

func (recorder *relayRecorder) Record(correlation relayCorrelation, event relayEvent) {
	if recorder == nil {
		return
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if recorder.file == nil {
		return
	}
	recorder.sequence++
	event.SchemaVersion = relaySchemaVersion
	event.Timestamp = time.Now().UTC()
	event.Sequence = recorder.sequence
	event.AppSessionID = recorder.sessionID
	event.TraceID = correlation.TraceID
	event.SpanID = correlation.SpanID
	event.ParentSpanID = correlation.ParentSpanID
	event.HTTPRequestID = correlation.HTTPRequestID
	payload, err := json.Marshal(event)
	if err != nil {
		return
	}
	_, _ = recorder.file.Write(append(payload, '\n'))
}

func relayCorrelationFromHeaders(traceID string, parentSpanID string, requestID string) relayCorrelation {
	traceID = safeRelayID(traceID, 128)
	parentSpanID = safeRelayID(parentSpanID, 64)
	requestID = safeRelayID(requestID, 128)
	if traceID == "" {
		traceID = relayRandomID(16)
	}
	if requestID == "" {
		requestID = traceID
	}
	return relayCorrelation{
		TraceID:       traceID,
		SpanID:        relayRandomID(8),
		ParentSpanID:  parentSpanID,
		HTTPRequestID: requestID,
	}
}

func safeRelayID(value string, maxLength int) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > maxLength {
		return ""
	}
	for _, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' || character == '_' || character == '.' {
			continue
		}
		return ""
	}
	return value
}

func relayRandomID(size int) string {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "")
	}
	return hex.EncodeToString(buffer)
}
