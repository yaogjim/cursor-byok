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
	// RuntimeFingerprint carries routing/model metadata for events only.
	// It must not participate in recorder rotation.
	RuntimeFingerprint string
}

type SessionMetadata struct {
	SourceKind        string `json:"source_kind,omitempty"`
	AppVersion        string `json:"app_version,omitempty"`
	BuildID           string `json:"build_id,omitempty"`
	Platform          string `json:"platform,omitempty"`
	ConfigFingerprint string `json:"config_fingerprint,omitempty"`
}

type Correlation struct {
	ProjectID            string `json:"project_id,omitempty"`
	TraceID              string `json:"trace_id,omitempty"`
	SpanID               string `json:"span_id,omitempty"`
	ParentSpanID         string `json:"parent_span_id,omitempty"`
	HTTPRequestID        string `json:"http_request_id,omitempty"`
	CursorRequestID      string `json:"cursor_request_id,omitempty"`
	ConversationID       string `json:"conversation_id,omitempty"`
	RootConversationID   string `json:"root_conversation_id,omitempty"`
	ParentConversationID string `json:"parent_conversation_id,omitempty"`
	ParentToolCallID     string `json:"parent_tool_call_id,omitempty"`
	SubagentRunID        string `json:"subagent_run_id,omitempty"`
	SubagentAttemptID    string `json:"subagent_attempt_id,omitempty"`
	SubagentAttemptNo    int    `json:"subagent_attempt_no,omitempty"`
	ChildConversationID  string `json:"child_conversation_id,omitempty"`
	AgentID              string `json:"agent_id,omitempty"`
	ParentModelCallID    string `json:"parent_model_call_id,omitempty"`
	TurnID               string `json:"turn_id,omitempty"`
	TurnSequence         uint64 `json:"turn_sequence,omitempty"`
	ModelCallID          string `json:"model_call_id,omitempty"`
	ToolCallID           string `json:"tool_call_id,omitempty"`
	ProviderPass         int    `json:"provider_pass,omitempty"`
	HTTPAttempt          int    `json:"http_attempt,omitempty"`
}

type Event struct {
	SchemaVersion        int            `json:"schema_version"`
	Timestamp            time.Time      `json:"timestamp"`
	Sequence             uint64         `json:"sequence"`
	AppSessionID         string         `json:"app_session_id"`
	ProjectID            string         `json:"project_id,omitempty"`
	TraceID              string         `json:"trace_id,omitempty"`
	SpanID               string         `json:"span_id,omitempty"`
	ParentSpanID         string         `json:"parent_span_id,omitempty"`
	HTTPRequestID        string         `json:"http_request_id,omitempty"`
	CursorRequestID      string         `json:"cursor_request_id,omitempty"`
	ConversationID       string         `json:"conversation_id,omitempty"`
	RootConversationID   string         `json:"root_conversation_id,omitempty"`
	ParentConversationID string         `json:"parent_conversation_id,omitempty"`
	ParentToolCallID     string         `json:"parent_tool_call_id,omitempty"`
	SubagentRunID        string         `json:"subagent_run_id,omitempty"`
	SubagentAttemptID    string         `json:"subagent_attempt_id,omitempty"`
	SubagentAttemptNo    int            `json:"subagent_attempt_no,omitempty"`
	ChildConversationID  string         `json:"child_conversation_id,omitempty"`
	AgentID              string         `json:"agent_id,omitempty"`
	ParentModelCallID    string         `json:"parent_model_call_id,omitempty"`
	TurnID               string         `json:"turn_id,omitempty"`
	TurnSequence         uint64         `json:"turn_sequence,omitempty"`
	ModelCallID          string         `json:"model_call_id,omitempty"`
	ToolCallID           string         `json:"tool_call_id,omitempty"`
	ProviderPass         int            `json:"provider_pass,omitempty"`
	HTTPAttempt          int            `json:"http_attempt,omitempty"`
	Layer                string         `json:"layer"`
	Event                string         `json:"event"`
	Capability           string         `json:"capability,omitempty"`
	Operation            string         `json:"operation,omitempty"`
	Direction            string         `json:"direction,omitempty"`
	Route                string         `json:"route,omitempty"`
	ExecutionTarget      string         `json:"execution_target,omitempty"`
	Protocol             string         `json:"protocol,omitempty"`
	Status               string         `json:"status,omitempty"`
	SemanticOutcome      string         `json:"semantic_outcome,omitempty"`
	ImplementationState  string         `json:"implementation_state,omitempty"`
	Severity             string         `json:"severity,omitempty"`
	ErrorCategory        string         `json:"error_category,omitempty"`
	DurationMS           int64          `json:"duration_ms,omitempty"`
	RequestBytes         int64          `json:"request_bytes,omitempty"`
	ResponseBytes        int64          `json:"response_bytes,omitempty"`
	DecodeError          bool           `json:"decode_error,omitempty"`
	DroppedEvents        uint64         `json:"dropped_events,omitempty"`
	Fields               map[string]any `json:"fields,omitempty"`
	PayloadRef           string         `json:"payload_ref,omitempty"`
}

// Optional stream diagnostic keys may appear in Event.Fields:
// header_at, first_byte_at, last_byte_at, body_end_at, last_effective_content_at,
// close_cause, partial_boundary, transport_outcome. Missing values are unknown/not_recorded.

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
	merged := mergeCorrelation(CorrelationFromContext(ctx), correlation)
	return context.WithValue(ctx, correlationContextKey{}, merged)
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
		ProjectID:            parent.ProjectID,
		TraceID:              firstNonEmpty(parent.TraceID, randomID(16)),
		SpanID:               randomID(8),
		ParentSpanID:         parent.SpanID,
		HTTPRequestID:        parent.HTTPRequestID,
		CursorRequestID:      parent.CursorRequestID,
		ConversationID:       parent.ConversationID,
		RootConversationID:   parent.RootConversationID,
		ParentConversationID: parent.ParentConversationID,
		ParentToolCallID:     parent.ParentToolCallID,
		SubagentRunID:        parent.SubagentRunID,
		SubagentAttemptID:    parent.SubagentAttemptID,
		SubagentAttemptNo:    parent.SubagentAttemptNo,
		ChildConversationID:  parent.ChildConversationID,
		AgentID:              parent.AgentID,
		ParentModelCallID:    parent.ParentModelCallID,
		TurnID:               parent.TurnID,
		TurnSequence:         parent.TurnSequence,
		ModelCallID:          parent.ModelCallID,
		ToolCallID:           parent.ToolCallID,
		ProviderPass:         parent.ProviderPass,
		HTTPAttempt:          parent.HTTPAttempt,
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
	event.RootConversationID = firstNonEmpty(event.RootConversationID, correlation.RootConversationID)
	event.ParentConversationID = firstNonEmpty(event.ParentConversationID, correlation.ParentConversationID)
	event.ParentToolCallID = firstNonEmpty(event.ParentToolCallID, correlation.ParentToolCallID)
	event.SubagentRunID = firstNonEmpty(event.SubagentRunID, correlation.SubagentRunID)
	event.SubagentAttemptID = firstNonEmpty(event.SubagentAttemptID, correlation.SubagentAttemptID)
	if event.SubagentAttemptNo == 0 {
		event.SubagentAttemptNo = correlation.SubagentAttemptNo
	}
	event.ChildConversationID = firstNonEmpty(event.ChildConversationID, correlation.ChildConversationID)
	event.AgentID = firstNonEmpty(event.AgentID, correlation.AgentID)
	event.ParentModelCallID = firstNonEmpty(event.ParentModelCallID, correlation.ParentModelCallID)
	event.TurnID = firstNonEmpty(event.TurnID, correlation.TurnID)
	if event.TurnSequence == 0 {
		event.TurnSequence = correlation.TurnSequence
	}
	event.ModelCallID = firstNonEmpty(event.ModelCallID, correlation.ModelCallID)
	event.ToolCallID = firstNonEmpty(event.ToolCallID, correlation.ToolCallID)
	if event.ProviderPass == 0 {
		event.ProviderPass = correlation.ProviderPass
	}
	if event.HTTPAttempt == 0 {
		event.HTTPAttempt = correlation.HTTPAttempt
	}
}

func mergeCorrelation(base Correlation, overlay Correlation) Correlation {
	base = normalizeCorrelation(base)
	overlay = normalizeCorrelation(overlay)
	mergeString := func(target *string, value string) {
		if value != "" {
			*target = value
		}
	}
	mergeString(&base.ProjectID, overlay.ProjectID)
	mergeString(&base.TraceID, overlay.TraceID)
	mergeString(&base.SpanID, overlay.SpanID)
	mergeString(&base.ParentSpanID, overlay.ParentSpanID)
	mergeString(&base.HTTPRequestID, overlay.HTTPRequestID)
	mergeString(&base.CursorRequestID, overlay.CursorRequestID)
	mergeString(&base.ConversationID, overlay.ConversationID)
	mergeString(&base.RootConversationID, overlay.RootConversationID)
	mergeString(&base.ParentConversationID, overlay.ParentConversationID)
	mergeString(&base.ParentToolCallID, overlay.ParentToolCallID)
	mergeString(&base.SubagentRunID, overlay.SubagentRunID)
	mergeString(&base.SubagentAttemptID, overlay.SubagentAttemptID)
	if overlay.SubagentAttemptNo != 0 {
		base.SubagentAttemptNo = overlay.SubagentAttemptNo
	}
	mergeString(&base.ChildConversationID, overlay.ChildConversationID)
	mergeString(&base.AgentID, overlay.AgentID)
	mergeString(&base.ParentModelCallID, overlay.ParentModelCallID)
	mergeString(&base.TurnID, overlay.TurnID)
	mergeString(&base.ModelCallID, overlay.ModelCallID)
	mergeString(&base.ToolCallID, overlay.ToolCallID)
	if overlay.TurnSequence != 0 {
		base.TurnSequence = overlay.TurnSequence
	}
	if overlay.ProviderPass != 0 {
		base.ProviderPass = overlay.ProviderPass
	}
	if overlay.HTTPAttempt != 0 {
		base.HTTPAttempt = overlay.HTTPAttempt
	}
	return base
}

func normalizeCorrelation(value Correlation) Correlation {
	value.ProjectID = strings.TrimSpace(value.ProjectID)
	value.TraceID = strings.TrimSpace(value.TraceID)
	value.SpanID = strings.TrimSpace(value.SpanID)
	value.ParentSpanID = strings.TrimSpace(value.ParentSpanID)
	value.HTTPRequestID = strings.TrimSpace(value.HTTPRequestID)
	value.CursorRequestID = strings.TrimSpace(value.CursorRequestID)
	value.ConversationID = strings.TrimSpace(value.ConversationID)
	value.RootConversationID = strings.TrimSpace(value.RootConversationID)
	value.ParentConversationID = strings.TrimSpace(value.ParentConversationID)
	value.ParentToolCallID = strings.TrimSpace(value.ParentToolCallID)
	value.SubagentRunID = strings.TrimSpace(value.SubagentRunID)
	value.SubagentAttemptID = strings.TrimSpace(value.SubagentAttemptID)
	value.ChildConversationID = strings.TrimSpace(value.ChildConversationID)
	value.AgentID = strings.TrimSpace(value.AgentID)
	value.ParentModelCallID = strings.TrimSpace(value.ParentModelCallID)
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
