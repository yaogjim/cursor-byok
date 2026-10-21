package sanitize

import (
	"strings"
	"testing"
)

func TestPathStripsQueryBodyAndDynamicIDs(t *testing.T) {
	got := Path("https://api2.cursor.sh/agent.v1.AgentService/RunSSE/1234567890123456?token=sk-secret#frag")
	if strings.Contains(got, "token") || strings.Contains(got, "sk-secret") || strings.Contains(got, "?") || strings.Contains(got, "#") {
		t.Fatalf("path leaked query or fragment: %q", got)
	}
	if got != "/agent.v1.AgentService/RunSSE/:id" {
		t.Fatalf("Path() = %q", got)
	}
}

func TestPathRedactsUUIDAndTruncates(t *testing.T) {
	got := Path("/aiserver.v1.BidiService/" + strings.Repeat("a", 32) + "/aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee")
	if !strings.Contains(got, ":id") || strings.Contains(got, "eeeeeeeeeeee") {
		t.Fatalf("dynamic segments not redacted: %q", got)
	}
	long := "/" + strings.Repeat("segment/", 40) + "end"
	truncated := Path(long)
	if len([]rune(truncated)) <= 160 {
		t.Fatalf("expected truncation marker on long path, got %q", truncated)
	}
	if !strings.HasSuffix(truncated, "...") {
		t.Fatalf("truncated path missing ellipsis: %q", truncated)
	}
}

func TestAllowlistedFieldsDropsSecretsAndKeepsMitmFacts(t *testing.T) {
	fields := AllowlistedFields(map[string]any{
		"authorization": "Bearer sk-secret",
		"Cookie":        "session=abc",
		"query":         "token=sk-secret",
		"body":          `{"api_key":"sk-secret"}`,
		"host":          "api2.cursor.sh:443",
		"connection_id": "conn-1",
		"traffic_class": "llm_relay",
		"action":        "mitm",
		"tls_role":      "server",
		"path":          "/aiserver.v1.BidiService/Run?key=secret",
		"method":        "POST",
		"status_code":   200,
		"unknown_field": "drop-me",
	})
	if fields["authorization"] != nil || fields["Cookie"] != nil || fields["query"] != nil || fields["body"] != nil || fields["unknown_field"] != nil {
		t.Fatalf("secret or unknown fields leaked: %#v", fields)
	}
	if fields["host"] != "api2.cursor.sh" {
		t.Fatalf("host = %#v", fields["host"])
	}
	path, _ := fields["path"].(string)
	if strings.Contains(path, "secret") || strings.Contains(path, "?") {
		t.Fatalf("path not sanitized: %q", path)
	}
	if fields["traffic_class"] != "llm_relay" || fields["connection_id"] != "conn-1" {
		t.Fatalf("mitm facts dropped: %#v", fields)
	}
}

func TestForbiddenKeyCoversAliases(t *testing.T) {
	for _, key := range []string{"Authorization", "x-api-key", "set_cookie", "raw_query", "request_body", "token", "header", "key", "Cookie"} {
		if !ForbiddenKey(key) {
			t.Fatalf("ForbiddenKey(%q) = false", key)
		}
	}
	if ForbiddenKey("traffic_class") || ForbiddenKey("host") || ForbiddenKey("status_code") {
		t.Fatal("diagnostic keys should be allowed")
	}
}

func TestAllowlistedFieldsKeepsStreamDiagnostics(t *testing.T) {
	fields := AllowlistedFields(map[string]any{
		"header_at":                  "2026-08-27T00:00:00Z",
		"first_byte_at":              "2026-08-27T00:00:01Z",
		"last_byte_at":               "2026-08-27T00:00:02Z",
		"body_end_at":                "2026-08-27T00:00:03Z",
		"first_event_at":             "2026-08-27T00:00:01Z",
		"last_effective_content_at":  "2026-08-27T00:00:02Z",
		"first_effective_content_at": "2026-08-27T00:00:01.5Z",
		"ttfr_ms":                    int64(40),
		"close_cause":                "unexpected_eof",
		"partial_boundary":           "text",
		"transport_outcome":          "succeeded",
		"completion_marker":          false,
		"http_status":                200,
		"artifact_model_call_id":     "call-1_fb0",
		"fallback_channel_index":     0,
		"payload_bytes":              16,
		"authorization":              "Bearer sk-secret",
		"headers":                    map[string]string{"Authorization": "Bearer sk-secret"},
		"body":                       `{"prompt":"secret"}`,
	})
	if fields["authorization"] != nil || fields["headers"] != nil || fields["body"] != nil {
		t.Fatalf("secret fields leaked: %#v", fields)
	}
	if fields["header_at"] != "2026-08-27T00:00:00Z" || fields["body_end_at"] != "2026-08-27T00:00:03Z" {
		t.Fatalf("timeline dropped: %#v", fields)
	}
	if fields["first_event_at"] != "2026-08-27T00:00:01Z" || fields["first_effective_content_at"] != "2026-08-27T00:00:01.5Z" || fields["ttfr_ms"] != int64(40) {
		t.Fatalf("ttfr fields dropped: %#v", fields)
	}
	if fields["close_cause"] != "unexpected_eof" || fields["partial_boundary"] != "text" {
		t.Fatalf("close/partial dropped: %#v", fields)
	}
	if fields["artifact_model_call_id"] != "call-1_fb0" || fields["payload_bytes"] != 16 {
		t.Fatalf("identity/payload facts dropped: %#v", fields)
	}
	if ForbiddenKey("header_at") || ForbiddenKey("body_end_at") || ForbiddenKey("first_effective_content_at") || ForbiddenKey("ttfr_ms") {
		t.Fatal("allowlisted stream diagnostic keys must not be treated as secret headers/bodies")
	}
	if !ForbiddenKey("header") || !ForbiddenKey("headers") || !ForbiddenKey("body") {
		t.Fatal("raw header/body keys must remain forbidden")
	}
}

func TestAllowlistedFieldsKeepsProviderFallbackBudgetFacts(t *testing.T) {
	fields := AllowlistedFields(map[string]any{
		"chain_max_attempts":              5,
		"chain_max_wait_ms":               8000,
		"chain_attempts_used":             3,
		"chain_attempts_remaining":        2,
		"chain_wait_used_ms":              1000,
		"chain_wait_remaining_ms":         7000,
		"channel_allocation_max_attempts": 3,
		"retry_delay_ms":                  0,
		"channel_attempt":                 1,
		"channel_id":                      "ch-a",
		"fallback_from":                   "ch-a",
		"fallback_to":                     "ch-b",
		"fallback_reason":                 "rate_limited",
		"fallback_suppressed_reason":      "wait_budget_exhausted",
		"authorization":                   "Bearer sk-secret",
		"headers":                         map[string]string{"Authorization": "Bearer sk-secret"},
		"body":                            `{"prompt":"secret"}`,
		"query":                           "api_key=sk-secret",
		"key":                             "sk-secret",
	})
	if fields["authorization"] != nil || fields["headers"] != nil || fields["body"] != nil || fields["query"] != nil || fields["key"] != nil {
		t.Fatalf("secret fields leaked: %#v", fields)
	}
	if fields["chain_max_attempts"] != 5 || fields["chain_max_wait_ms"] != 8000 {
		t.Fatalf("budget caps dropped: %#v", fields)
	}
	if fields["chain_attempts_used"] != 3 || fields["chain_attempts_remaining"] != 2 {
		t.Fatalf("attempt remaining dropped: %#v", fields)
	}
	if fields["chain_wait_used_ms"] != 1000 || fields["chain_wait_remaining_ms"] != 7000 {
		t.Fatalf("wait remaining dropped: %#v", fields)
	}
	if fields["channel_allocation_max_attempts"] != 3 || fields["retry_delay_ms"] != 0 {
		t.Fatalf("allocation/delay dropped: %#v", fields)
	}
	if fields["channel_id"] != "ch-a" || fields["fallback_to"] != "ch-b" {
		t.Fatalf("channel ids dropped: %#v", fields)
	}
}

func TestAllowlistedFieldsKeepsAttemptPolicyFacts(t *testing.T) {
	fields := AllowlistedFields(map[string]any{
		"failure_class": "transport_interrupted", "failure_origin": "provider_stream",
		"reschedule_decision": "reschedule", "reschedule_suppressed_reason": "not_recorded",
		"attempts_used": 2, "attempts_remaining": 1, "last_event_sequence": 17,
		"last_event_at": "2026-08-30T10:00:00Z", "bytes_received": 4096, "events_received": 17,
		"completion_marker_seen": false, "close_cause": "unexpected_eof",
		"terminal_prepare_state": "prepared", "terminal_commit_state": "not_recorded",
		"prompt": "secret", "task_args": "secret", "result": "secret", "transcript": "secret", "error": "complete error",
	})
	for _, key := range []string{"failure_class", "failure_origin", "reschedule_decision", "reschedule_suppressed_reason", "attempts_used", "attempts_remaining", "last_event_sequence", "last_event_at", "bytes_received", "events_received", "completion_marker_seen", "close_cause", "terminal_prepare_state", "terminal_commit_state"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("attempt policy field %q dropped: %#v", key, fields)
		}
	}
	for _, key := range []string{"prompt", "task_args", "result", "transcript", "error"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("sensitive field %q retained: %#v", key, fields)
		}
	}
}

func TestAllowlistedFieldsKeepsDiagnosticSummaryFacts(t *testing.T) {
	fields := AllowlistedFields(map[string]any{
		"error_summary":               "upstream unavailable",
		"provider_error_summary":      "status_4xx status=401",
		"provider_error_summary_type": "json_error",
		"skip_reason":                 "blob_sync",
		"missing_blob_key_count":      3,
		"attempt":                     1,
		"max_attempts":                2,
		"retry_decision":              "retry",
		"retryable":                   true,
		"failure_category":            "transport",
		"failure_cause":               "unexpected_eof",
		"failure_phase":               "read",
		"failure_stage":               "response_body",
		"recovery_action":             "fallback",
		"business_outcome":            "failed",
		"protocol_final_status":       "error",
		"model_call_final_status":     "failed",
		"retry_suppression_reason":    "output_or_tool_progress",
		"status":                      "error",
		"provider_pass":               0,
		"http_attempt":                2,
		"checkpoint_result":           "unmatched",
	})
	for _, key := range []string{"error_summary", "provider_error_summary", "provider_error_summary_type", "skip_reason", "missing_blob_key_count", "attempt", "max_attempts", "retry_decision", "retryable", "failure_category", "failure_cause", "failure_phase", "failure_stage", "recovery_action", "business_outcome", "protocol_final_status", "model_call_final_status", "retry_suppression_reason", "status", "provider_pass", "http_attempt", "checkpoint_result"} {
		if _, ok := fields[key]; !ok {
			t.Fatalf("diagnostic summary field %q dropped: %#v", key, fields)
		}
	}
}

func TestAllowlistedFieldsRejectsRawKeysAndKeepsSummaryBounded(t *testing.T) {
	fields := AllowlistedFields(map[string]any{
		"missing_blob_keys": "blob-a,blob-b",
		"body":              `{"prompt":"secret"}`,
		"url":               "https://api.example.test/v1/messages?token=sk-secret",
		"raw_url":           "https://api.example.test/v1/messages?token=sk-secret",
		"authorization":     "Bearer sk-secret",
		"credential":        "sk-secret",
		"message":           "arbitrary free text with /secret/path",
		"error_summary":     "upstream https://api.example.test/v1/messages?token=sk-secret",
	})
	for _, key := range []string{"missing_blob_keys", "body", "url", "raw_url", "authorization", "credential", "message"} {
		if _, ok := fields[key]; ok {
			t.Fatalf("raw key %q leaked: %#v", key, fields)
		}
	}
	summary, _ := fields["error_summary"].(string)
	if summary == "" {
		t.Fatalf("error_summary dropped: %#v", fields)
	}
	for _, leaked := range []string{"sk-secret", "token=", "?", "https://"} {
		if strings.Contains(summary, leaked) {
			t.Fatalf("error_summary leaked %q: %q", leaked, summary)
		}
	}
}

func TestSummaryCapsRunesAndStillStripsSecrets(t *testing.T) {
	long := strings.Repeat("错误详情", 200) + " https://api.example.test/v1/messages?token=sk-secret"
	summary := Summary(long)
	if !strings.HasSuffix(summary, "...") {
		t.Fatalf("Summary() did not truncate long text: %q", summary)
	}
	body := strings.TrimSuffix(summary, "...")
	if runes := len([]rune(body)); runes > 512 {
		t.Fatalf("Summary() body runes = %d, want <= 512", runes)
	}
	if strings.Contains(summary, "sk-secret") || strings.Contains(summary, "token=") || strings.Contains(summary, "https://") {
		t.Fatalf("Summary() leaked secret or URL: %q", summary)
	}
	if got := Summary("upstream unavailable"); got != "upstream unavailable" {
		t.Fatalf("Summary() short text = %q", got)
	}
	if got := Summary(""); got != "" {
		t.Fatalf("Summary() empty = %q", got)
	}
}

func TestPathStripsQueryFragmentAndKeepsDeterministicShape(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"", ""},
		{"/aiserver.v1.BidiService/Run?token=sk-secret&cookie=abc", "/aiserver.v1.BidiService/Run"},
		{"/v1#token=abc", "/v1"},
		{"relative/path?x=1", "/relative/path"},
		{"https://api2.cursor.sh/agent.v1.AgentService/RunSSE", "/agent.v1.AgentService/RunSSE"},
		{"https://api2.cursor.sh", "/"},
		{"/oauth/callback?code=secret", "/oauth/callback"},
	}
	for _, test := range cases {
		got := Path(test.in)
		if got != test.want {
			t.Fatalf("Path(%q) = %q, want %q", test.in, got, test.want)
		}
		if Path(test.in) != got {
			t.Fatalf("Path(%q) is not deterministic", test.in)
		}
		for _, leaked := range []string{"token", "sk-secret", "cookie=", "?", "#", "code=secret"} {
			if strings.Contains(got, leaked) {
				t.Fatalf("Path(%q) leaked %q: %q", test.in, leaked, got)
			}
		}
	}
}
