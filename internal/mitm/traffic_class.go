package mitm

import (
	"net"
	"strings"
	"unicode"

	"cursor/internal/observability"
)

const (
	TrafficClassLLMRelay     = "llm_relay"
	TrafficClassControlPlane = "control_plane"
	TrafficClassTelemetry    = "telemetry"
	TrafficClassFileSync     = "filesync"
	TrafficClassDashboardMCP = "dashboard_mcp"
	TrafficClassTabCPPRepo   = "tab_cpp_repo"
	TrafficClassUnknown      = "unknown"

	maxObservabilityPathRunes = 160
)

// ClassifyTraffic 按 path 优先、host 仅提示的规则划分流量。CONNECT 或缺少 path 时返回 unknown。
func ClassifyTraffic(host, method, path string) string {
	_ = host
	_ = method
	normalized := observabilityPath(path)
	if normalized == "" || normalized == "/" {
		return TrafficClassUnknown
	}
	lower := strings.ToLower(normalized)

	switch {
	case containsAny(lower, "filesyncservice/", "/file_sync"):
		return TrafficClassFileSync
	case containsAny(lower, "dashboardservice/", "mcpregistryservice/"):
		return TrafficClassDashboardMCP
	case containsAny(lower, "analyticsservice/", "inappadservice/") ||
		strings.HasPrefix(lower, "/v1/traces") ||
		strings.Contains(lower, "reportaicodechangemetrics"):
		return TrafficClassTelemetry
	case containsAny(lower, "cppservice/", "repositoryservice/", "uploadservice/") ||
		containsAny(lower,
			"/streamcpp",
			"/streamnextcursorprediction",
			"/getcppeditclassification",
			"/refreshtabcontext",
			"/cppconfig",
			"/cppedithistory",
			"/cppappend",
			"/writegitcommitmessage",
			"/writegitbranchname",
		):
		return TrafficClassTabCPPRepo
	case containsAny(lower, "authservice/", "serverconfigservice/", "networkservice/") ||
		strings.HasPrefix(lower, "/oauth/") ||
		containsAny(lower,
			"/servertime",
			"/getserverconfig",
			"/availablemodels",
			"/getusablemodels",
			"/getdefaultmodel",
			"/healthz",
		):
		return TrafficClassControlPlane
	case containsAny(lower, "bidiservice/", "agentservice/", "aiservice/"):
		return TrafficClassLLMRelay
	default:
		return TrafficClassUnknown
	}
}

// ClassifyCapability 仅识别可聚合的 SCM/FSSync/repository 路径；其余保持 unknown，避免发明能力。
func ClassifyCapability(path string) string {
	return observability.ClassifyRequestCapability(observabilityPath(path))
}

func ClassifyOperation(capability string) string {
	return observability.ClassifyRequestOperation(capability, "transport.forward")
}

func observabilityPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	if idx := strings.IndexAny(path, "?#"); idx >= 0 {
		path = path[:idx]
	}
	if path == "" {
		return ""
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return path
}

func redactObservabilityPath(path string) string {
	path = observabilityPath(path)
	if path == "" {
		return ""
	}
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if isDynamicPathSegment(part) {
			parts[i] = ":id"
		}
	}
	return truncateRunes(strings.Join(parts, "/"), maxObservabilityPathRunes)
}

func hostHint(hostport string) string {
	host, _ := splitConnectHostPort(hostport)
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return ""
	}
	if strings.HasSuffix(host, ".cursor.sh") || host == "cursor.sh" {
		return "cursor"
	}
	return "other"
}

func splitConnectHostPort(hostport string) (host, port string) {
	hostport = strings.TrimSpace(hostport)
	if hostport == "" || hostport == "-" {
		return "", ""
	}
	if strings.HasPrefix(hostport, "[") {
		hostport = strings.TrimPrefix(hostport, "[")
		hostport = strings.TrimSuffix(hostport, "]")
	}
	h, p, err := net.SplitHostPort(hostport)
	if err != nil {
		return strings.ToLower(hostport), ""
	}
	return strings.ToLower(strings.TrimSpace(h)), strings.TrimSpace(p)
}

func isDynamicPathSegment(segment string) bool {
	segment = strings.TrimSpace(segment)
	if segment == "" {
		return false
	}
	if looksLikeUUID(segment) {
		return true
	}
	if len(segment) >= 16 && isHexString(segment) {
		return true
	}
	if len(segment) >= 8 && isDigits(segment) {
		return true
	}
	return false
}

func looksLikeUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, char := range value {
		switch i {
		case 8, 13, 18, 23:
			if char != '-' {
				return false
			}
		default:
			if !isHexRune(char) {
				return false
			}
		}
	}
	return true
}

func isHexString(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if !isHexRune(char) {
			return false
		}
	}
	return true
}

func isDigits(value string) bool {
	if value == "" {
		return false
	}
	for _, char := range value {
		if char < '0' || char > '9' {
			return false
		}
	}
	return true
}

func isHexRune(char rune) bool {
	return (char >= '0' && char <= '9') || (char >= 'a' && char <= 'f') || (char >= 'A' && char <= 'F')
}

func containsAny(value string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(value, needle) {
			return true
		}
	}
	return false
}

func truncateRunes(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || value == "" {
		return value
	}
	runes := []rune(value)
	if len(runes) <= limit {
		return value
	}
	return string(runes[:limit]) + "..."
}

func normalizeFieldKey(value string) string {
	return strings.Map(func(character rune) rune {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			return unicode.ToLower(character)
		}
		return -1
	}, strings.TrimSpace(value))
}
