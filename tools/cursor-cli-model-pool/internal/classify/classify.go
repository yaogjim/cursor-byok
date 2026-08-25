package classify

import (
	"encoding/json"
	"strings"
)

type Phase string

const (
	PhasePreflight   Phase = "preflight"
	PhaseLaunching   Phase = "launching"
	PhasePreOutput   Phase = "pre_output"
	PhaseObserved    Phase = "observed"
	PhaseMutated     Phase = "mutated"
	PhaseTerminal    Phase = "terminal"
	PhaseNeedsReview Phase = "needs_review"
)

type Category string

const (
	CatNone         Category = ""
	CatTransport    Category = "transport"
	CatHTTP429      Category = "http_429"
	CatHTTP502      Category = "http_502"
	CatHTTP503      Category = "http_503"
	CatHTTP504      Category = "http_504"
	CatHTTP500      Category = "http_500"
	CatHTTP4xx      Category = "http_4xx"
	CatHTTP5xx      Category = "http_5xx"
	CatAuth         Category = "auth"
	CatSpawnFailure Category = "spawn_failure"
	CatCancel       Category = "cancel"
	CatUnknown      Category = "unknown"
	CatNDJSON       Category = "ndjson_parse"
	CatConfig       Category = "config"
	CatUnsupported  Category = "unsupported"
)

type Event struct {
	Type         string
	Subtype      string
	SessionID    string
	RequestID    string
	Category     Category
	HTTPStatus   int
	HasResult    bool
	UnknownType  bool
	RawTypeKnown bool
}

type rawEvent struct {
	Type          string          `json:"type"`
	Subtype       string          `json:"subtype"`
	SessionID     string          `json:"session_id"`
	SessionIDAlt  string          `json:"sessionId"`
	RequestID     string          `json:"request_id"`
	RequestIDAlt  string          `json:"requestId"`
	ErrorCategory string          `json:"error_category"`
	HTTPStatus    int             `json:"http_status"`
	StatusCode    int             `json:"status_code"`
	Error         json.RawMessage `json:"error"`
}

func ParseLine(line []byte) (Event, error) {
	var raw rawEvent
	if err := json.Unmarshal(line, &raw); err != nil {
		return Event{}, err
	}
	ev := Event{
		Type:         strings.TrimSpace(raw.Type),
		Subtype:      strings.TrimSpace(raw.Subtype),
		SessionID:    firstNonEmpty(raw.SessionID, raw.SessionIDAlt),
		RequestID:    firstNonEmpty(raw.RequestID, raw.RequestIDAlt),
		HTTPStatus:   raw.HTTPStatus,
		RawTypeKnown: true,
	}
	if ev.HTTPStatus == 0 {
		ev.HTTPStatus = raw.StatusCode
	}
	cat := NormalizeCategory(raw.ErrorCategory)
	nestedCat, nestedStatus := parseStructuredError(raw.Error)
	if cat == CatUnknown && raw.ErrorCategory == "" {
		cat = CatNone
	}
	if cat == CatNone {
		cat = nestedCat
	}
	if ev.HTTPStatus == 0 {
		ev.HTTPStatus = nestedStatus
	}
	if cat == CatNone && ev.HTTPStatus >= 400 {
		cat = CategoryFromStatus(ev.HTTPStatus)
	}
	ev.Category = cat
	switch ev.Type {
	case "system", "user", "retry", "connection", "thinking", "assistant", "tool_call", "result":
		ev.HasResult = ev.Type == "result"
	default:
		ev.UnknownType = true
		ev.RawTypeKnown = false
	}
	return ev, nil
}

func parseStructuredError(raw json.RawMessage) (Category, int) {
	if len(raw) == 0 {
		return CatNone, 0
	}
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed[0] != '{' {
		return CatNone, 0
	}
	var obj struct {
		Category   string `json:"category"`
		HTTPStatus int    `json:"http_status"`
		StatusCode int    `json:"status_code"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return CatNone, 0
	}
	cat := NormalizeCategory(obj.Category)
	if cat == CatUnknown && obj.Category == "" {
		cat = CatNone
	}
	status := obj.HTTPStatus
	if status == 0 {
		status = obj.StatusCode
	}
	return cat, status
}

func NormalizeCategory(raw string) Category {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "":
		return CatNone
	case "transport":
		return CatTransport
	case "http_429", "429":
		return CatHTTP429
	case "http_502", "502":
		return CatHTTP502
	case "http_503", "503":
		return CatHTTP503
	case "http_504", "504":
		return CatHTTP504
	case "http_500", "500":
		return CatHTTP500
	case "http_4xx":
		return CatHTTP4xx
	case "http_5xx":
		return CatHTTP5xx
	case "auth", "http_401", "401", "http_403", "403":
		return CatAuth
	case "spawn_failure":
		return CatSpawnFailure
	case "cancel":
		return CatCancel
	default:
		return CatUnknown
	}
}

func CategoryFromStatus(code int) Category {
	switch code {
	case 429:
		return CatHTTP429
	case 502:
		return CatHTTP502
	case 503:
		return CatHTTP503
	case 504:
		return CatHTTP504
	case 500:
		return CatHTTP500
	case 401, 403:
		return CatAuth
	default:
		if code >= 400 && code < 500 {
			return CatHTTP4xx
		}
		if code >= 500 && code < 600 {
			return CatHTTP5xx
		}
		return CatUnknown
	}
}

func ClosesSwitchWindow(ev Event) bool {
	if ev.UnknownType {
		return true
	}
	switch ev.Type {
	case "thinking", "assistant", "tool_call":
		return true
	default:
		return false
	}
}

func StayPreOutput(ev Event) bool {
	if ev.UnknownType {
		return false
	}
	switch ev.Type {
	case "system", "user", "retry", "connection":
		return true
	default:
		return false
	}
}

func Switchable(phase Phase, cat Category) bool {
	switch phase {
	case PhasePreflight, PhaseLaunching, PhasePreOutput:
	default:
		return false
	}
	switch cat {
	case CatTransport, CatHTTP429, CatHTTP502, CatHTTP503, CatHTTP504, CatSpawnFailure:
		return true
	default:
		return false
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
