package sanitize

import (
	"regexp"
	"strings"
	"unicode"
)

const maxPathRunes = 160

var (
	identifierSegment = regexp.MustCompile(`(?i)^(?:[0-9a-f]{16,}|[0-9a-f]{8}-[0-9a-f-]{27,}|\d{8,})$`)
	allowedFields     = map[string]struct{}{
		"method": {}, "status_code": {}, "client_kind": {}, "client_protocol": {}, "public_model_id": {}, "message_case": {},
		"kind": {}, "finish_reason": {}, "ttft_ms": {}, "ttfr_ms": {}, "append_seqno": {},
		"host": {}, "connection_id": {}, "traffic_class": {}, "action": {},
		"tls_role": {}, "path": {}, "host_hint": {}, "source": {}, "port": {},
		"target_host":     {},
		"channel_attempt": {}, "channel_id": {}, "fallback_from": {}, "fallback_to": {},
		"fallback_reason": {}, "fallback_suppressed_reason": {},
		"chain_max_attempts": {}, "chain_max_wait_ms": {},
		"chain_attempts_used": {}, "chain_attempts_remaining": {},
		"chain_wait_used_ms": {}, "chain_wait_remaining_ms": {},
		"channel_allocation_max_attempts": {}, "retry_delay_ms": {},
		"header_at": {}, "first_byte_at": {}, "last_byte_at": {}, "body_end_at": {},
		"first_event_at": {}, "last_effective_content_at": {}, "first_effective_content_at": {},
		"close_cause": {}, "partial_boundary": {}, "transport_outcome": {},
		"completion_marker": {}, "http_status": {},
		"artifact_model_call_id": {}, "fallback_channel_index": {}, "payload_bytes": {},
	}
)

func AllowlistedFields(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	output := make(map[string]any)
	for key, value := range input {
		if ForbiddenKey(key) {
			continue
		}
		if _, ok := allowedFields[key]; !ok {
			continue
		}
		switch typed := value.(type) {
		case string:
			cleaned := strings.TrimSpace(typed)
			if cleaned == "" {
				continue
			}
			if key == "path" || key == "route" {
				cleaned = Path(cleaned)
			}
			if key == "host" || key == "target_host" {
				cleaned = Host(cleaned)
			}
			if cleaned == "" {
				continue
			}
			output[key] = cleaned
		case nil:
			continue
		default:
			output[key] = value
		}
	}
	if len(output) == 0 {
		return nil
	}
	return output
}

func ForbiddenKey(key string) bool {
	trimmed := strings.TrimSpace(key)
	if _, ok := allowedFields[trimmed]; ok {
		return false
	}
	normalized := normalizeFieldKey(key)
	switch normalized {
	case "authorization", "proxyauthorization", "cookie", "setcookie", "apikey", "xapikey",
		"token", "bearertoken", "query", "rawquery", "body", "url", "rawurl", "fullurl",
		"header", "headers", "requestbody", "responsebody", "cookieheader", "key":
		return true
	default:
		return strings.Contains(normalized, "authorization") ||
			strings.Contains(normalized, "cookie") ||
			strings.Contains(normalized, "apikey") ||
			strings.Contains(normalized, "token") ||
			strings.Contains(normalized, "query") ||
			strings.Contains(normalized, "body") ||
			strings.Contains(normalized, "header")
	}
}

func Path(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.Contains(value, "://") {
		if index := strings.Index(value, "://"); index >= 0 {
			remainder := value[index+3:]
			if slash := strings.IndexByte(remainder, '/'); slash >= 0 {
				value = remainder[slash:]
			} else {
				return "/"
			}
		}
	}
	if query := strings.IndexAny(value, "?#"); query >= 0 {
		value = value[:query]
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if !strings.HasPrefix(value, "/") && !strings.Contains(value, "://") {
		value = "/" + value
	}
	segments := strings.Split(value, "/")
	for index, segment := range segments {
		if identifierSegment.MatchString(segment) {
			segments[index] = ":id"
		}
	}
	return truncateRunes(strings.Join(segments, "/"), maxPathRunes)
}

func Host(value string) string {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" || value == "-" {
		return ""
	}
	if strings.HasPrefix(value, "[") {
		value = strings.TrimPrefix(value, "[")
		if end := strings.IndexByte(value, ']'); end >= 0 {
			return strings.ToLower(strings.TrimSpace(value[:end]))
		}
	}
	if host, _, ok := strings.Cut(value, ":"); ok && !strings.Contains(value, "::") {
		return strings.TrimSpace(host)
	}
	return value
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
