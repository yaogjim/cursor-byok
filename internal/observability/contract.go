// Package observability provides local, versioned diagnostic capture.
package observability

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"strings"
	"time"
)

const SchemaVersion = 2

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
	Metadata      SessionMetadata
}

type SessionMetadata struct {
	SourceKind        string `json:"source_kind,omitempty"`
	AppVersion        string `json:"app_version,omitempty"`
	BuildID           string `json:"build_id,omitempty"`
	Platform          string `json:"platform,omitempty"`
	ConfigFingerprint string `json:"config_fingerprint,omitempty"`
}

type Correlation struct {
	ProjectID       string `json:"project_id,omitempty"`
	TraceID         string `json:"trace_id,omitempty"`
	SpanID          string `json:"span_id,omitempty"`
	ParentSpanID    string `json:"parent_span_id,omitempty"`
	HTTPRequestID   string `json:"http_request_id,omitempty"`
	CursorRequestID string `json:"cursor_request_id,omitempty"`
	ConversationID  string `json:"conversation_id,omitempty"`
	TurnID          string `json:"turn_id,omitempty"`
	TurnSequence    uint64 `json:"turn_sequence,omitempty"`
	ModelCallID     string `json:"model_call_id,omitempty"`
	ToolCallID      string `json:"tool_call_id,omitempty"`
}

type Event struct {
	SchemaVersion       int            `json:"schema_version"`
	Timestamp           time.Time      `json:"timestamp"`
	Sequence            uint64         `json:"sequence"`
	AppSessionID        string         `json:"app_session_id"`
	ProjectID           string         `json:"project_id,omitempty"`
	TraceID             string         `json:"trace_id,omitempty"`
	SpanID              string         `json:"span_id,omitempty"`
	ParentSpanID        string         `json:"parent_span_id,omitempty"`
	HTTPRequestID       string         `json:"http_request_id,omitempty"`
	CursorRequestID     string         `json:"cursor_request_id,omitempty"`
	ConversationID      string         `json:"conversation_id,omitempty"`
	TurnID              string         `json:"turn_id,omitempty"`
	TurnSequence        uint64         `json:"turn_sequence,omitempty"`
	ModelCallID         string         `json:"model_call_id,omitempty"`
	ToolCallID          string         `json:"tool_call_id,omitempty"`
	Layer               string         `json:"layer"`
	Event               string         `json:"event"`
	Capability          string         `json:"capability,omitempty"`
	Operation           string         `json:"operation,omitempty"`
	Direction           string         `json:"direction,omitempty"`
	Route               string         `json:"route,omitempty"`
	ExecutionTarget     string         `json:"execution_target,omitempty"`
	Protocol            string         `json:"protocol,omitempty"`
	Status              string         `json:"status,omitempty"`
	SemanticOutcome     string         `json:"semantic_outcome,omitempty"`
	ImplementationState string         `json:"implementation_state,omitempty"`
	Severity            string         `json:"severity,omitempty"`
	ErrorCategory       string         `json:"error_category,omitempty"`
	DurationMS          int64          `json:"duration_ms,omitempty"`
	RequestBytes        int64          `json:"request_bytes,omitempty"`
	ResponseBytes       int64          `json:"response_bytes,omitempty"`
	DecodeError         bool           `json:"decode_error,omitempty"`
	DroppedEvents       uint64         `json:"dropped_events,omitempty"`
	Fields              map[string]any `json:"fields,omitempty"`
	PayloadRef          string         `json:"payload_ref,omitempty"`
}

type Payload struct {
	Name        string
	ContentType string
	Data        any
}

type Capture struct {
	Event        Event
	Payload      *Payload
	ProjectPaths []string
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
	SchemaVersion     int        `json:"schema_version"`
	AppSessionID      string     `json:"app_session_id"`
	Mode              string     `json:"mode"`
	Status            string     `json:"status"`
	StartedAt         time.Time  `json:"started_at"`
	ClosedAt          *time.Time `json:"closed_at,omitempty"`
	PayloadDegraded   bool       `json:"payload_degraded,omitempty"`
	DroppedEvents     uint64     `json:"dropped_events,omitempty"`
	LastError         string     `json:"last_error,omitempty"`
	SourceKind        string     `json:"source_kind,omitempty"`
	AppVersion        string     `json:"app_version,omitempty"`
	BuildID           string     `json:"build_id,omitempty"`
	Platform          string     `json:"platform,omitempty"`
	ConfigFingerprint string     `json:"config_fingerprint,omitempty"`
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
		ProjectID:       parent.ProjectID,
		TraceID:         firstNonEmpty(parent.TraceID, randomID(16)),
		SpanID:          randomID(8),
		ParentSpanID:    parent.SpanID,
		HTTPRequestID:   parent.HTTPRequestID,
		CursorRequestID: parent.CursorRequestID,
		ConversationID:  parent.ConversationID,
		TurnID:          parent.TurnID,
		TurnSequence:    parent.TurnSequence,
		ModelCallID:     parent.ModelCallID,
		ToolCallID:      parent.ToolCallID,
	}
}

func applyCorrelation(event *Event, correlation Correlation) {
	if event == nil {
		return
	}
	correlation = normalizeCorrelation(correlation)
	event.ProjectID = firstNonEmpty(event.ProjectID, correlation.ProjectID)
	event.TraceID = firstNonEmpty(event.TraceID, correlation.TraceID)
	event.SpanID = firstNonEmpty(event.SpanID, correlation.SpanID)
	event.ParentSpanID = firstNonEmpty(event.ParentSpanID, correlation.ParentSpanID)
	event.HTTPRequestID = firstNonEmpty(event.HTTPRequestID, correlation.HTTPRequestID)
	event.CursorRequestID = firstNonEmpty(event.CursorRequestID, correlation.CursorRequestID)
	event.ConversationID = firstNonEmpty(event.ConversationID, correlation.ConversationID)
	event.TurnID = firstNonEmpty(event.TurnID, correlation.TurnID)
	if event.TurnSequence == 0 {
		event.TurnSequence = correlation.TurnSequence
	}
	event.ModelCallID = firstNonEmpty(event.ModelCallID, correlation.ModelCallID)
	event.ToolCallID = firstNonEmpty(event.ToolCallID, correlation.ToolCallID)
}

func normalizeCorrelation(value Correlation) Correlation {
	value.ProjectID = strings.TrimSpace(value.ProjectID)
	value.TraceID = strings.TrimSpace(value.TraceID)
	value.SpanID = strings.TrimSpace(value.SpanID)
	value.ParentSpanID = strings.TrimSpace(value.ParentSpanID)
	value.HTTPRequestID = strings.TrimSpace(value.HTTPRequestID)
	value.CursorRequestID = strings.TrimSpace(value.CursorRequestID)
	value.ConversationID = strings.TrimSpace(value.ConversationID)
	value.TurnID = strings.TrimSpace(value.TurnID)
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
