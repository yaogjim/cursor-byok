// Package observability provides local, versioned diagnostic capture.
package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

const SchemaVersion = 1

const (
	ModeOff   = "off"
	ModeBasic = "basic"
	ModeFull  = "full"
)

type Settings struct {
	Mode          string
	RetentionDays int
	MaxDiskMB     int
	QueueSize     int
}

type Correlation struct {
	TraceID         string `json:"trace_id,omitempty"`
	SpanID          string `json:"span_id,omitempty"`
	ParentSpanID    string `json:"parent_span_id,omitempty"`
	HTTPRequestID   string `json:"http_request_id,omitempty"`
	CursorRequestID string `json:"cursor_request_id,omitempty"`
	ConversationID  string `json:"conversation_id,omitempty"`
	ModelCallID     string `json:"model_call_id,omitempty"`
	ToolCallID      string `json:"tool_call_id,omitempty"`
}

type Event struct {
	SchemaVersion   int            `json:"schema_version"`
	Timestamp       time.Time      `json:"timestamp"`
	Sequence        uint64         `json:"sequence"`
	AppSessionID    string         `json:"app_session_id"`
	TraceID         string         `json:"trace_id,omitempty"`
	SpanID          string         `json:"span_id,omitempty"`
	ParentSpanID    string         `json:"parent_span_id,omitempty"`
	HTTPRequestID   string         `json:"http_request_id,omitempty"`
	CursorRequestID string         `json:"cursor_request_id,omitempty"`
	ConversationID  string         `json:"conversation_id,omitempty"`
	ModelCallID     string         `json:"model_call_id,omitempty"`
	ToolCallID      string         `json:"tool_call_id,omitempty"`
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
	DecodeError     bool           `json:"decode_error,omitempty"`
	DroppedEvents   uint64         `json:"dropped_events,omitempty"`
	Fields          map[string]any `json:"fields,omitempty"`
	PayloadRef      string         `json:"payload_ref,omitempty"`
}

type Payload struct {
	Name        string
	ContentType string
	Data        any
}

type Capture struct {
	Event   Event
	Payload *Payload
}

type Status struct {
	Enabled         bool   `json:"enabled"`
	Mode            string `json:"mode"`
	SessionID       string `json:"session_id,omitempty"`
	SessionPath     string `json:"session_path,omitempty"`
	PayloadDegraded bool   `json:"payload_degraded"`
	DroppedEvents   uint64 `json:"dropped_events"`
	LastError       string `json:"last_error,omitempty"`
}

type Manifest struct {
	SchemaVersion   int        `json:"schema_version"`
	AppSessionID    string     `json:"app_session_id"`
	Mode            string     `json:"mode"`
	Status          string     `json:"status"`
	StartedAt       time.Time  `json:"started_at"`
	ClosedAt        *time.Time `json:"closed_at,omitempty"`
	PayloadDegraded bool       `json:"payload_degraded,omitempty"`
	DroppedEvents   uint64     `json:"dropped_events,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
}

type correlationContextKey struct{}

func WithCorrelation(ctx context.Context, correlation Correlation) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, correlationContextKey{}, normalizeCorrelation(correlation))
}

func CorrelationFromContext(ctx context.Context) Correlation {
	if ctx == nil {
		return Correlation{}
	}
	value, _ := ctx.Value(correlationContextKey{}).(Correlation)
	return normalizeCorrelation(value)
}

func NewTrace() Correlation {
	return Correlation{
		TraceID: randomID(16),
		SpanID:  randomID(8),
	}
}

func ChildSpan(parent Correlation) Correlation {
	parent = normalizeCorrelation(parent)
	return Correlation{
		TraceID:         firstNonEmpty(parent.TraceID, randomID(16)),
		SpanID:          randomID(8),
		ParentSpanID:    parent.SpanID,
		HTTPRequestID:   parent.HTTPRequestID,
		CursorRequestID: parent.CursorRequestID,
		ConversationID:  parent.ConversationID,
		ModelCallID:     parent.ModelCallID,
		ToolCallID:      parent.ToolCallID,
	}
}

func applyCorrelation(event *Event, correlation Correlation) {
	if event == nil {
		return
	}
	correlation = normalizeCorrelation(correlation)
	event.TraceID = firstNonEmpty(event.TraceID, correlation.TraceID)
	event.SpanID = firstNonEmpty(event.SpanID, correlation.SpanID)
	event.ParentSpanID = firstNonEmpty(event.ParentSpanID, correlation.ParentSpanID)
	event.HTTPRequestID = firstNonEmpty(event.HTTPRequestID, correlation.HTTPRequestID)
	event.CursorRequestID = firstNonEmpty(event.CursorRequestID, correlation.CursorRequestID)
	event.ConversationID = firstNonEmpty(event.ConversationID, correlation.ConversationID)
	event.ModelCallID = firstNonEmpty(event.ModelCallID, correlation.ModelCallID)
	event.ToolCallID = firstNonEmpty(event.ToolCallID, correlation.ToolCallID)
}

func normalizeCorrelation(value Correlation) Correlation {
	value.TraceID = strings.TrimSpace(value.TraceID)
	value.SpanID = strings.TrimSpace(value.SpanID)
	value.ParentSpanID = strings.TrimSpace(value.ParentSpanID)
	value.HTTPRequestID = strings.TrimSpace(value.HTTPRequestID)
	value.CursorRequestID = strings.TrimSpace(value.CursorRequestID)
	value.ConversationID = strings.TrimSpace(value.ConversationID)
	value.ModelCallID = strings.TrimSpace(value.ModelCallID)
	value.ToolCallID = strings.TrimSpace(value.ToolCallID)
	return value
}

func randomID(size int) string {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return strings.ReplaceAll(time.Now().UTC().Format("20060102T150405.000000000"), ".", "")
	}
	return hex.EncodeToString(buffer)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if normalized := strings.TrimSpace(value); normalized != "" {
			return normalized
		}
	}
	return ""
}
