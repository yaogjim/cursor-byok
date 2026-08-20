package analyze

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"cursor-log-analyzer/internal/workspace"
)

func TestReclassifyTLSBuckets(t *testing.T) {
	cases := map[string]string{
		"client_unknown_ca":                   "client_unknown_ca",
		"upstream_unknown_ca":                 "upstream_tls",
		"upstream_remote_unknown_certificate": "upstream_tls",
		"upstream_tls_handshake_failed":       "upstream_tls",
		"hostname_mismatch":                   "upstream_tls",
		"handshake_mismatch":                  "protocol",
		"upstream_http2":                      "protocol",
		"mitm_tls_config_failed":              "protocol",
		"backend_unavailable":                 "backend",
		"backend":                             "backend",
		"client_tls_handshake_failed":         "other",
		"":                                    "other",
	}
	for in, want := range cases {
		if got := ReclassifyTLS(in); got != want {
			t.Fatalf("ReclassifyTLS(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMitmFromEventsAggregatesObservedRelatedAndUnknown(t *testing.T) {
	when := time.Date(2026, 3, 14, 0, 0, 0, 0, time.UTC)
	events := []workspace.EventRecord{
		mitmEvent(when, "tls_handshake_failed", "cursor_to_proxy", "client_unknown_ca", "", `{"host":"api2.cursor.sh","tls_role":"server","action":"mitm","connection_id":"conn-tls","traffic_class":"unknown"}`),
		mitmEvent(when, "connect_decided", "cursor_to_proxy", "", "", `{"host":"api2.cursor.sh","action":"mitm","tls_role":"server","connection_id":"conn-gap","traffic_class":"unknown"}`),
		mitmEvent(when, "connect_decided", "cursor_to_proxy", "", "", `{"host":"other.example","action":"passthrough","connection_id":"conn-pass","traffic_class":"unknown"}`),
		mitmEvent(when, "connect_decided", "cursor_to_proxy", "", "", `{"host":"legacy.example","action":"mitm","traffic_class":"unknown"}`),
		mitmEvent(when.Add(time.Second), "backend_forward_finished", "proxy_internal", "", "ok", `{"host":"api2.cursor.sh","action":"backend_forward","connection_id":"conn-ok","traffic_class":"llm_relay","path":"/aiserver.v1.BidiService/Run?token=sk-secret","status_code":200}`),
		mitmEvent(when.Add(2*time.Second), "backend_forward_finished", "proxy_internal", "", "error", `{"host":"api2.cursor.sh","action":"backend_forward","connection_id":"conn-fail","traffic_class":"control_plane","path":"/auth.v1.AuthService/Get","status_code":502}`),
		mitmEvent(when.Add(3*time.Second), "backend_forward_finished", "proxy_internal", "", "ok", `{"host":"mixed.example","connection_id":"conn-mixed","traffic_class":"unknown","path":"/mystery/endpoint"}`),
		{
			Timestamp: when.Add(4 * time.Second), Layer: "provider", Event: "llm_summary", Capability: "provider",
			Status: "error", SemanticOutcome: "failed", ErrorCategory: "provider_error",
			SafeFieldsJSON: `{"host":"api2.cursor.sh"}`,
		},
	}
	events[4].HTTPRequestID = "http-1"
	events[4].TraceID = "trace-1"
	events[4].TraceKey = "trace-1"
	events[4].Route = "/aiserver.v1.BidiService/Run"
	events[4].DurationMS = 10
	events[5].HTTPRequestID = "http-2"
	events[5].TraceID = "trace-2"
	events[5].TraceKey = "trace-2"
	events[5].Route = "/auth.v1.AuthService/Get"
	events[5].DurationMS = 40
	events[5].SemanticOutcome = "failed"
	events[6].DurationMS = 20

	got := MitmFromEvents(events)
	if got.Observed.ConnectDecided.Mitm != 2 || got.Observed.ConnectDecided.Passthrough != 1 || got.Observed.ConnectDecided.Total != 3 {
		t.Fatalf("connect stats = %+v", got.Observed.ConnectDecided)
	}
	if got.Observed.ConnectDecided.MitmRatio < 0.6 || got.Observed.ConnectDecided.PassthroughRatio <= 0 {
		t.Fatalf("connect ratios = %+v", got.Observed.ConnectDecided)
	}
	if len(got.Observed.TLSHandshakeFailed) != 1 || got.Observed.TLSHandshakeFailed[0].Host != "api2.cursor.sh" || got.Observed.TLSHandshakeFailed[0].TLSRole != "server" {
		t.Fatalf("tls aggregate = %#v", got.Observed.TLSHandshakeFailed)
	}
	if len(got.Observed.TLSReclassified) == 0 || got.Observed.TLSReclassified[0].Bucket != "client_unknown_ca" {
		t.Fatalf("tls buckets = %#v", got.Observed.TLSReclassified)
	}
	if len(got.Observed.MitmWithoutBackendForward) != 2 {
		t.Fatalf("observed mitm without forward = %#v", got.Observed.MitmWithoutBackendForward)
	}
	seenGap := false
	for _, item := range got.Observed.MitmWithoutBackendForward {
		if item.ConnectionID == "conn-gap" && item.Host == "api2.cursor.sh" {
			seenGap = true
		}
	}
	if !seenGap {
		t.Fatalf("observed mitm without forward missing conn-gap: %#v", got.Observed.MitmWithoutBackendForward)
	}
	if len(got.RelatedUnconfirmed.HostOnlyMitmWithoutForward) != 1 || got.RelatedUnconfirmed.HostOnlyMitmWithoutForward[0].Host != "legacy.example" {
		t.Fatalf("host-only related = %#v", got.RelatedUnconfirmed.HostOnlyMitmWithoutForward)
	}
	if !strings.Contains(got.RelatedUnconfirmed.Note, "不能据此归因") {
		t.Fatalf("missing non-causal note: %q", got.RelatedUnconfirmed.Note)
	}
	if len(got.RelatedUnconfirmed.TLSNearProviderFailures) == 0 {
		t.Fatal("expected time-adjacent TLS/provider pair in related_unconfirmed only")
	}

	var llm, control *BackendForwardStat
	for index := range got.Observed.BackendForwardFinished {
		item := got.Observed.BackendForwardFinished[index]
		switch item.TrafficClass {
		case "llm_relay":
			llm = &got.Observed.BackendForwardFinished[index]
		case "control_plane":
			control = &got.Observed.BackendForwardFinished[index]
		}
	}
	if llm == nil || llm.StatusCode != 200 || llm.SuccessRate != 1 || llm.DurationP50MS != 10 {
		t.Fatalf("llm forward = %+v", llm)
	}
	if control == nil || control.StatusCode != 502 || control.SuccessRate != 0 || control.DurationP95MS != 40 {
		t.Fatalf("control forward = %+v", control)
	}

	var mixed, api2 *HostTrafficClass
	for index := range got.Observed.HostTrafficClasses {
		item := got.Observed.HostTrafficClasses[index]
		switch item.Host {
		case "mixed.example":
			mixed = &got.Observed.HostTrafficClasses[index]
		case "api2.cursor.sh":
			api2 = &got.Observed.HostTrafficClasses[index]
		}
	}
	if api2 == nil || len(api2.TrafficClasses) < 2 {
		t.Fatalf("host-class many-to-many missing: %#v", got.Observed.HostTrafficClasses)
	}
	if mixed == nil || !containsString(got.Observed.MixedHosts, "mixed.example") && len(mixed.TrafficClasses) == 0 {
		t.Fatalf("mixed host missing: classes=%#v mixed=%#v", mixed, got.Observed.MixedHosts)
	}
	if !containsString(got.Observed.MixedHosts, "api2.cursor.sh") {
		t.Fatalf("api2 should be mixed, got %#v", got.Observed.MixedHosts)
	}

	if got.Observed.CorrelationChains.Complete != 2 {
		t.Fatalf("complete chains = %+v", got.Observed.CorrelationChains)
	}
	if got.Observed.UnknownTraffic.UnknownEvents == 0 || got.Observed.UnknownTraffic.UnknownRatio == 0 {
		t.Fatalf("unknown traffic = %+v", got.Observed.UnknownTraffic)
	}
	if len(got.Observed.UnknownTraffic.SanitizedPathSamples) == 0 {
		t.Fatal("expected sanitized unknown path samples")
	}
	for _, sample := range got.Observed.UnknownTraffic.SanitizedPathSamples {
		if strings.Contains(sample, "?") || strings.Contains(sample, "token") || strings.Contains(sample, "sk-secret") {
			t.Fatalf("path sample leaked secret: %q", sample)
		}
	}
	if got.Unknown.HandshakeWithoutHTTPPath == 0 || got.Unknown.ConnectUnknownClass == 0 {
		t.Fatalf("unknown counters = %+v", got.Unknown)
	}

	payload, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, leaked := range []string{"sk-secret", "token=", "Bearer", `"query"`, `"body"`, `"header"`, `"cookie"`, `"key"`} {
		if strings.Contains(text, leaked) {
			t.Fatalf("diagnostics leaked %q: %s", leaked, text)
		}
	}
}

func TestMitmFromEventsAcceptsLegacyLogsWithoutNewFields(t *testing.T) {
	when := time.Date(2026, 3, 14, 1, 0, 0, 0, time.UTC)
	got := MitmFromEvents([]workspace.EventRecord{
		{Timestamp: when, Layer: "mitm", Event: "tls_handshake_failed", Status: "error"},
		{Timestamp: when, Layer: "mitm", Event: "connect_decided"},
		{Timestamp: when, Layer: "mitm", Event: "backend_forward_finished", Status: "ok", DurationMS: 5},
	})
	if got.Unknown.EventsMissingNewFields == 0 {
		t.Fatalf("legacy missing fields not recorded: %+v", got.Unknown)
	}
	if got.Observed.CorrelationChains.Complete != 0 {
		t.Fatalf("legacy logs should not fabricate complete chains: %+v", got.Observed.CorrelationChains)
	}
	if got.Observed.ConnectDecided.Mitm != 0 || got.Observed.ConnectDecided.Passthrough != 0 {
		t.Fatalf("legacy connect without action should not be classified as mitm/passthrough: %+v", got.Observed.ConnectDecided)
	}
}

func TestMitmPathSamplesAreBoundedAndDeterministic(t *testing.T) {
	when := time.Date(2026, 3, 14, 2, 0, 0, 0, time.UTC)
	var first []workspace.EventRecord
	for index := 40; index >= 0; index-- {
		first = append(first, workspace.EventRecord{
			Timestamp: when, Layer: "mitm", Event: "backend_forward_finished", Status: "ok",
			SafeFieldsJSON: `{"host":"sample.example","traffic_class":"unknown","path":"/z` + strings.Repeat("x", index) + `"}`,
		})
	}
	var second []workspace.EventRecord
	for index := 0; index <= 40; index++ {
		second = append(second, workspace.EventRecord{
			Timestamp: when, Layer: "mitm", Event: "backend_forward_finished", Status: "ok",
			SafeFieldsJSON: `{"host":"sample.example","traffic_class":"unknown","path":"/z` + strings.Repeat("x", index) + `"}`,
		})
	}
	left := MitmFromEvents(first).Observed.UnknownTraffic.SanitizedPathSamples
	right := MitmFromEvents(second).Observed.UnknownTraffic.SanitizedPathSamples
	if len(left) == 0 || len(left) > mitmSampleLimit {
		t.Fatalf("sample bound = %d", len(left))
	}
	if strings.Join(left, ",") != strings.Join(right, ",") {
		t.Fatalf("path samples are not deterministic:\n%s\n%s", left, right)
	}
}

func TestRedactMitmDiagnosticsPseudonymizesConnectionIDs(t *testing.T) {
	input := MitmDiagnostics{
		Observed: MitmObserved{
			MitmWithoutBackendForward: []MitmWithoutForward{{Host: "h", ConnectionID: "conn-secret"}},
			UnknownTraffic:            UnknownTrafficStats{SanitizedPathSamples: []string{"/aiserver.v1.BidiService/Run?token=sk"}},
		},
	}
	got := RedactMitmDiagnostics(input, func(value string) string {
		if value == "" {
			return ""
		}
		return "id_x"
	})
	if got.Observed.MitmWithoutBackendForward[0].ConnectionID != "id_x" {
		t.Fatalf("connection id not redacted: %#v", got.Observed.MitmWithoutBackendForward)
	}
	if strings.Contains(got.Observed.UnknownTraffic.SanitizedPathSamples[0], "token") {
		t.Fatalf("redacted path still has query: %#v", got.Observed.UnknownTraffic.SanitizedPathSamples)
	}
}

func mitmEvent(when time.Time, name, direction, category, status, fields string) workspace.EventRecord {
	return workspace.EventRecord{
		Timestamp: when, Layer: "mitm", Event: name, Direction: direction,
		ErrorCategory: category, Status: status, SafeFieldsJSON: fields,
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
