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
	if isRealFailureCategory(event.ErrorCategory) {
		return SeverityError
	}
	if severity := strings.ToLower(strings.TrimSpace(event.Severity)); severity == SeverityInfo || severity == SeverityWarning || severity == SeverityError {
		return severity
	}
	outcome := strings.ToLower(strings.TrimSpace(event.SemanticOutcome))
	implementation := strings.ToLower(strings.TrimSpace(event.ImplementationState))
	status := strings.ToLower(strings.TrimSpace(event.Status))
	switch {
	case isExpectedNoiseEvent(event):
		return SeverityWarning
	case outcome == OutcomeFailed, outcome == OutcomeTimeout:
		return SeverityError
	case outcome == OutcomeDegraded, outcome == OutcomeUnsupported, outcome == OutcomePartial, outcome == OutcomeCompatOnly,
		implementation == ImplementationPartial, implementation == ImplementationCompat, implementation == ImplementationUnsupported,
		status == "degraded", status == "warning", status == "retrying":
		return SeverityWarning
	default:
		return SeverityInfo
	}
}

func isExpectedNoiseEvent(event Event) bool {
	if isRealFailureCategory(event.ErrorCategory) {
		return false
	}
	if isClientHandshakeNoiseCategory(strings.ToLower(strings.TrimSpace(event.ErrorCategory))) {
		return true
	}
	return eventStatusCode(event) == 404 && isSCMOrFileSyncEvent(event)
}

// ClassifyRequestCapability 仅识别可聚合的 SCM/FSSync/repository 路径，其余保持 unknown。
func ClassifyRequestCapability(parts ...string) string {
	blob := strings.ToLower(strings.Join(parts, " "))
	switch {
	case strings.Contains(blob, "filesync") || strings.Contains(blob, "file_sync"):
		return "filesync"
	case strings.Contains(blob, "writegit") || strings.Contains(blob, "write_git") || strings.Contains(blob, "gitservice"):
		return "git"
	case strings.Contains(blob, "repositoryservice") || strings.Contains(blob, "repository_"):
		return "repository"
	default:
		return "unknown"
	}
}

func ClassifyRequestOperation(capability string, fallback string) string {
	switch strings.TrimSpace(capability) {
	case "filesync":
		return "filesync.request"
	case "git":
		return "git.request"
	case "repository":
		return "repository.request"
	default:
		fallback = strings.TrimSpace(fallback)
		if fallback == "" {
			return "unknown.operation"
		}
		return fallback
	}
}

func isClientHandshakeNoiseCategory(category string) bool {
	switch strings.ToLower(strings.TrimSpace(category)) {
	case "client_unknown_ca", "handshake_mismatch", "client_tls_handshake_failed":
		return true
	default:
		return false
	}
}

func isRealFailureCategory(category string) bool {
	category = strings.ToLower(strings.TrimSpace(category))
	switch category {
	case "upstream_unknown_ca", "upstream_remote_unknown_certificate", "upstream_tls_handshake_failed",
		"hostname_mismatch", "upstream_http2", "backend_unavailable", "server_error", "timeout",
		"server_5xx", "transport", "stream_idle_timeout", "stream_decode", "provider_error",
		"mitm_tls_config_failed", "handler_error":
		return true
	default:
		return strings.HasPrefix(category, "upstream_") || strings.Contains(category, "provider")
	}
}

func isSCMOrFileSyncEvent(event Event) bool {
	switch strings.ToLower(strings.TrimSpace(event.Capability)) {
	case "git", "filesync", "repository":
		return true
	}
	blob := strings.ToLower(strings.Join([]string{
		event.Route,
		event.Operation,
		eventFieldString(event, "path"),
		eventFieldString(event, "traffic_class"),
	}, " "))
	return strings.Contains(blob, "filesync") ||
		strings.Contains(blob, "file_sync") ||
		strings.Contains(blob, "writegit") ||
		strings.Contains(blob, "gitservice") ||
		strings.Contains(blob, "repositoryservice") ||
		strings.Contains(blob, "scm")
}

func eventStatusCode(event Event) int {
	if event.Fields == nil {
		return 0
	}
	switch value := event.Fields["status_code"].(type) {
	case int:
		return value
	case int32:
		return int(value)
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}

func eventFieldString(event Event, key string) string {
	if event.Fields == nil {
		return ""
	}
	value, _ := event.Fields[key].(string)
	return strings.TrimSpace(value)
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
