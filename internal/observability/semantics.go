package observability

import (
	"regexp"
	"strings"
)

const (
	SeverityInfo    = "info"
	SeverityWarning = "warning"
	SeverityError   = "error"

	OutcomeStarted     = "started"
	OutcomeSucceeded   = "succeeded"
	OutcomeFailed      = "failed"
	OutcomeCanceled    = "canceled"
	OutcomeTimeout     = "timeout"
	OutcomeDegraded    = "degraded"
	OutcomeUnsupported = "unsupported"
	OutcomePartial     = "partial"
	OutcomeCompatOnly  = "compat_only"
	OutcomeUnknown     = "unknown"

	ImplementationImplemented = "implemented"
	ImplementationPartial     = "partial"
	ImplementationCompat      = "compat"
	ImplementationUnsupported = "unsupported"
	ImplementationUnknown     = "unknown"

	DirectionCursorToProxy   = "cursor_to_proxy"
	DirectionProxyInternal   = "proxy_internal"
	DirectionProxyToProvider = "proxy_to_provider"
	DirectionProxyToUpstream = "proxy_to_upstream"
	DirectionProxyToCursor   = "proxy_to_cursor"
)

var operationPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

var capabilities = enumSet(
	"agent", "provider", "tool", "repository", "docs", "upload", "tab", "filesync", "git", "config", "update", "unknown",
)

var directions = enumSet(
	DirectionCursorToProxy, DirectionProxyInternal, DirectionProxyToProvider, DirectionProxyToUpstream, DirectionProxyToCursor,
)

var outcomes = enumSet(
	OutcomeStarted, OutcomeSucceeded, OutcomeFailed, OutcomeCanceled, OutcomeTimeout, OutcomeDegraded,
	OutcomeUnsupported, OutcomePartial, OutcomeCompatOnly, OutcomeUnknown,
)

var implementationStates = enumSet(
	ImplementationImplemented, ImplementationPartial, ImplementationCompat, ImplementationUnsupported, ImplementationUnknown,
)

func normalizeEventSemantics(event Event) Event {
	event.Capability = normalizeEnum(event.Capability, capabilities, "unknown")
	event.Operation = normalizeOperation(event.Operation)
	event.Direction = normalizeEnum(event.Direction, directions, "")
	event.SemanticOutcome = normalizeEnum(event.SemanticOutcome, outcomes, OutcomeUnknown)
	event.ImplementationState = normalizeEnum(event.ImplementationState, implementationStates, ImplementationUnknown)
	event.Severity = projectSeverity(event)
	return event
}

func projectSeverity(event Event) string {
	if severity := strings.ToLower(strings.TrimSpace(event.Severity)); severity == SeverityInfo || severity == SeverityWarning || severity == SeverityError {
		return severity
	}
	outcome := strings.ToLower(strings.TrimSpace(event.SemanticOutcome))
	implementation := strings.ToLower(strings.TrimSpace(event.ImplementationState))
	status := strings.ToLower(strings.TrimSpace(event.Status))
	switch {
	case strings.TrimSpace(event.ErrorCategory) != "", outcome == OutcomeFailed, outcome == OutcomeTimeout, status == "error", status == "failed":
		return SeverityError
	case outcome == OutcomeDegraded, outcome == OutcomeUnsupported, outcome == OutcomePartial, outcome == OutcomeCompatOnly,
		implementation == ImplementationPartial, implementation == ImplementationCompat, implementation == ImplementationUnsupported,
		status == "degraded", status == "warning", status == "retrying":
		return SeverityWarning
	default:
		return SeverityInfo
	}
}

func normalizeOperation(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if operationPattern.MatchString(value) {
		return value
	}
	return "unknown.operation"
}

func normalizeEnum(value string, allowed map[string]struct{}, fallback string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	if _, ok := allowed[value]; ok {
		return value
	}
	return fallback
}

func enumSet(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
