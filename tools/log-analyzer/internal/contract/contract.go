package contract

import "time"

const (
	MinimumSupportedSchemaVersion = 1
	SupportedSchemaVersion        = 2
	ReportSchemaVersion           = 1
)

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
	SubagentRunID       string         `json:"subagent_run_id,omitempty"`
	SubagentAttemptID   string         `json:"subagent_attempt_id,omitempty"`
	SubagentAttemptNo   int            `json:"subagent_attempt_no,omitempty"`
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

	Source string `json:"-"`
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
