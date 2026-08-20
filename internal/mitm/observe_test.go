package mitm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"cursor/internal/observability"

	"github.com/elazarl/goproxy"
	"golang.org/x/net/http2"
)

type recordingCapture struct {
	mu     sync.Mutex
	events []observability.Event
}

func (r *recordingCapture) Record(_ context.Context, capture observability.Capture) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, capture.Event)
	return true
}

func (r *recordingCapture) snapshot() []observability.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]observability.Event, len(r.events))
	copy(out, r.events)
	return out
}

type panickingCapture struct{}

func (panickingCapture) Record(context.Context, observability.Capture) bool {
	panic("capture unavailable")
}

func TestIsWhitelistedRelayHostUnchanged(t *testing.T) {
	tests := []struct {
		host string
		want bool
	}{
		{host: "api2.cursor.sh", want: true},
		{host: "api2.cursor.sh:443", want: true},
		{host: "api3.cursor.sh", want: true},
		{host: "API3.CURSOR.SH:443", want: true},
		{host: "metrics.cursor.sh", want: true},
		{host: "repo42.cursor.sh", want: true},
		{host: "cursor.sh", want: false},
		{host: "example.com", want: false},
		{host: "api2.cursor.sh.evil.test", want: false},
		{host: "", want: false},
		{host: "127.0.0.1:443", want: false},
	}
	for _, test := range tests {
		if got := isWhitelistedRelayHost(test.host); got != test.want {
			t.Fatalf("isWhitelistedRelayHost(%q) = %v, want %v", test.host, got, test.want)
		}
	}
}

func TestSelectConnectActionPreservesGoproxyPointers(t *testing.T) {
	mitmAction := &goproxy.ConnectAction{Action: goproxy.ConnectMitm}
	if got := selectConnectAction("example.com:443", mitmAction); got != goproxy.OkConnect {
		t.Fatalf("non-whitelist action = %v, want OkConnect", got)
	}
	if got := selectConnectAction("metrics.cursor.sh:443", mitmAction); got != mitmAction {
		t.Fatalf("whitelist action = %p, want mitmAction %p", got, mitmAction)
	}
	if got := selectConnectAction("api2.cursor.sh:443", nil); got != goproxy.OkConnect {
		t.Fatalf("nil mitm action = %v, want OkConnect", got)
	}
	if connectActionName(goproxy.OkConnect) != actionPassthrough {
		t.Fatalf("OkConnect name = %q", connectActionName(goproxy.OkConnect))
	}
	if connectActionName(mitmAction) != actionMITM {
		t.Fatalf("mitm action name = %q", connectActionName(mitmAction))
	}
}

func TestClassifyTrafficPathFirst(t *testing.T) {
	tests := []struct {
		name   string
		host   string
		method string
		path   string
		want   string
	}{
		{name: "connect no path", host: "api2.cursor.sh", method: http.MethodConnect, path: "", want: TrafficClassUnknown},
		{name: "connect slash only", host: "api2.cursor.sh", method: http.MethodConnect, path: "/", want: TrafficClassUnknown},
		{name: "host hint ignored", host: "api2.cursor.sh", method: http.MethodPost, path: "", want: TrafficClassUnknown},
		{name: "llm bidi", host: "metrics.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.BidiService/BidiAppend", want: TrafficClassLLMRelay},
		{name: "llm agent", host: "api2.cursor.sh", method: http.MethodPost, path: "/agent.v1.AgentService/RunSSE", want: TrafficClassLLMRelay},
		{name: "llm aiservice leftover", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.AiService/StreamChat", want: TrafficClassLLMRelay},
		{name: "control plane models", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.AiService/AvailableModels", want: TrafficClassControlPlane},
		{name: "telemetry", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.AnalyticsService/TrackEvents", want: TrafficClassTelemetry},
		{name: "filesync", host: "api4.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.FileSyncService/FSSyncFile", want: TrafficClassFileSync},
		{name: "dashboard mcp", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.DashboardService/GetAvailableMcpServers", want: TrafficClassDashboardMCP},
		{name: "tab cpp", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.AiService/StreamCpp", want: TrafficClassTabCPPRepo},
		{name: "repo", host: "api2.cursor.sh", method: http.MethodPost, path: "/aiserver.v1.RepositoryService/FastUpdateFileV2", want: TrafficClassTabCPPRepo},
		{name: "unknown path", host: "api2.cursor.sh", method: http.MethodGet, path: "/unknown/resource", want: TrafficClassUnknown},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ClassifyTraffic(test.host, test.method, test.path); got != test.want {
				t.Fatalf("ClassifyTraffic() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestRedactObservabilityPathStripsQueryAndDynamicSegments(t *testing.T) {
	got := redactObservabilityPath("/aiserver.v1.AiService/GetDoc/550e8400-e29b-41d4-a716-446655440000?token=secret-token#frag")
	if strings.Contains(got, "secret-token") || strings.Contains(got, "550e8400") || strings.Contains(got, "?") {
		t.Fatalf("redacted path leaked sensitive data: %q", got)
	}
	if !strings.Contains(got, "/aiserver.v1.AiService/GetDoc/:id") {
		t.Fatalf("redacted path = %q", got)
	}
}

func TestClientUnknownCAAndUpstreamTLSAreDistinct(t *testing.T) {
	clientErr := &net.OpError{Op: "remote error", Err: errors.New("tls: unknown certificate")}
	client := classifyHandshake(handshakeObservation{
		Source:  handshakeSourceGoproxyClient,
		Host:    "api2.cursor.sh:443",
		Action:  actionMITM,
		Err:     clientErr,
		ErrText: clientErr.Error(),
	})
	if client.Direction != observability.DirectionCursorToProxy || client.TLSRole != tlsRoleServer || client.ErrorCategory != errorClientUnknownCA {
		t.Fatalf("client handshake class = %+v", client)
	}

	sameTextUpstream := classifyHandshake(handshakeObservation{
		Source:  handshakeSourceUpstream,
		Host:    "api2.cursor.sh:443",
		Action:  actionPassthrough,
		ErrText: "remote error: tls: unknown certificate",
	})
	if sameTextUpstream.Direction != observability.DirectionProxyToUpstream || sameTextUpstream.TLSRole != tlsRoleClient {
		t.Fatalf("upstream same-text class = %+v", sameTextUpstream)
	}
	if sameTextUpstream.ErrorCategory == errorClientUnknownCA {
		t.Fatalf("upstream unknown certificate was mislabeled as client_unknown_ca")
	}

	upstream := classifyHandshake(handshakeObservation{
		Source: handshakeSourceUpstream,
		Host:   "api3.cursor.sh:443",
		Action: actionBackendForward,
		Err:    x509.UnknownAuthorityError{Cert: &x509.Certificate{}},
	})
	if upstream.Direction != observability.DirectionProxyToUpstream || upstream.TLSRole != tlsRoleClient || upstream.ErrorCategory != errorUpstreamUnknownCA {
		t.Fatalf("upstream authority class = %+v", upstream)
	}

	http2Class := classifyHandshake(handshakeObservation{
		Source: handshakeSourceUpstream,
		Err:    http2.ConnectionError(http2.ErrCodeProtocol),
	})
	if http2Class.Direction != observability.DirectionProxyToUpstream || http2Class.TLSRole != tlsRoleClient || http2Class.ErrorCategory != errorUpstreamHTTP2 {
		t.Fatalf("upstream http2 class = %+v", http2Class)
	}
}

func TestParseGoproxyClientHandshakeLog(t *testing.T) {
	observation, ok := parseGoproxyTLSLog("[001] WARN: Cannot handshake client api2.cursor.sh:443 remote error: tls: unknown certificate")
	if !ok {
		t.Fatal("expected goproxy client handshake parse")
	}
	classified := classifyHandshake(observation)
	if observation.Source != handshakeSourceGoproxyClient || observation.Host != "api2.cursor.sh:443" {
		t.Fatalf("parsed observation = %+v", observation)
	}
	if classified.ErrorCategory != errorClientUnknownCA || classified.Direction != observability.DirectionCursorToProxy || classified.TLSRole != tlsRoleServer {
		t.Fatalf("classified = %+v", classified)
	}
}

func TestGoproxyCopyErrorIsNotClientUnknownCA(t *testing.T) {
	if _, ok := parseGoproxyTLSLog("[001] WARN: Error copying to client: remote error: tls: unknown certificate"); ok {
		t.Fatal("copy error must not be parsed as client handshake")
	}
}

func TestGoproxyAdapterEmitsClientHandshakeEvent(t *testing.T) {
	capture := &recordingCapture{}
	adapter := &goproxyLogAdapter{capture: capture}
	adapter.Printf("[%03d] WARN: Cannot handshake client %v %v\n", 1, "api2.cursor.sh:443", "remote error: tls: unknown certificate")
	events := capture.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %d, want 1", len(events))
	}
	event := events[0]
	if event.Event != "tls_handshake_failed" || event.Direction != observability.DirectionCursorToProxy || event.ErrorCategory != errorClientUnknownCA {
		t.Fatalf("event = %+v", event)
	}
	if fieldString(event, "tls_role") != tlsRoleServer || fieldString(event, "action") != actionMITM {
		t.Fatalf("fields = %#v", event.Fields)
	}
	if fieldString(event, "traffic_class") != TrafficClassUnknown {
		t.Fatalf("connect-stage traffic_class = %q", fieldString(event, "traffic_class"))
	}
}

func TestConnectDecidedUsesUnknownTrafficClass(t *testing.T) {
	capture := &recordingCapture{}
	server := &ProxyServer{capture: capture}
	ctx := &goproxy.ProxyCtx{}
	server.recordConnectDecided(ctx, "api2.cursor.sh:443", actionMITM)
	events := capture.snapshot()
	if len(events) != 1 || events[0].Event != "connect_decided" {
		t.Fatalf("events = %+v", events)
	}
	if fieldString(events[0], "traffic_class") != TrafficClassUnknown {
		t.Fatalf("traffic_class = %q", fieldString(events[0], "traffic_class"))
	}
	if fieldString(events[0], "action") != actionMITM || fieldString(events[0], "connection_id") == "" {
		t.Fatalf("fields = %#v", events[0].Fields)
	}
	if _, ok := ctx.UserData.(*connectState); !ok {
		t.Fatal("expected connect state on ctx")
	}
}

func TestForwardToServerOmitsSensitiveFields(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("backend missing original authorization")
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "ok")
	}))
	t.Cleanup(backend.Close)

	capture := &recordingCapture{}
	server, err := NewProxyServer("127.0.0.1:0", backend.URL, "", "", nil, capture)
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend?api_key=query-secret", strings.NewReader(`{"prompt":"body-secret"}`))
	req.Host = "api2.cursor.sh"
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Cookie", "session=cookie-secret")
	req.Header.Set("X-API-Key", "header-secret")
	resp, err := server.forwardToServer(req, &connectState{ConnectionID: "conn-1", Action: actionMITM, Host: "api2.cursor.sh:443"})
	if err != nil {
		t.Fatalf("forwardToServer() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	events := capture.snapshot()
	if len(events) < 2 {
		t.Fatalf("expected start/finish events, got %d", len(events))
	}
	secrets := []string{"secret-token", "query-secret", "cookie-secret", "header-secret", "body-secret", "Authorization", "Cookie", "api_key="}
	for _, event := range events {
		if eventContainsSensitivePayload(event, secrets...) {
			t.Fatalf("event leaked sensitive data: %+v", event)
		}
		if event.Event == "backend_forward_started" || event.Event == "backend_forward_finished" {
			if fieldString(event, "traffic_class") != TrafficClassLLMRelay {
				t.Fatalf("traffic_class = %q", fieldString(event, "traffic_class"))
			}
			if fieldString(event, "action") != actionBackendForward || fieldString(event, "connection_id") != "conn-1" {
				t.Fatalf("fields = %#v", event.Fields)
			}
			if strings.Contains(event.Route, "?") {
				t.Fatalf("route contained query: %q", event.Route)
			}
		}
	}
}

func TestForwardToServerContinuesWhenCapturePanics(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusAccepted)
		_, _ = io.WriteString(writer, "ok")
	}))
	t.Cleanup(backend.Close)

	server, err := NewProxyServer("127.0.0.1:0", backend.URL, "", "", nil, panickingCapture{})
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://api2.cursor.sh/aiserver.v1.AiService/ServerTime", nil)
	req.Host = "api2.cursor.sh"
	resp, err := server.forwardToServer(req, nil)
	if err != nil {
		t.Fatalf("forwardToServer() error = %v", err)
	}
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestForwardToServerContinuesWhenCaptureNil(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(backend.Close)
	server, err := NewProxyServer("127.0.0.1:0", backend.URL, "", "", nil)
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "https://api2.cursor.sh/healthz", nil)
	req.Host = "api2.cursor.sh"
	resp, err := server.forwardToServer(req, nil)
	if err != nil {
		t.Fatalf("forwardToServer() error = %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestRecordHeaderMismatchIsNotUnknownCA(t *testing.T) {
	classified := classifyHandshake(handshakeObservation{
		Source: handshakeSourceHTTPServer,
		Err:    tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"},
	})
	if classified.ErrorCategory == errorClientUnknownCA || classified.Direction != observability.DirectionCursorToProxy {
		t.Fatalf("mismatch class = %+v", classified)
	}
}

func fieldString(event observability.Event, key string) string {
	if event.Fields == nil {
		return ""
	}
	value, _ := event.Fields[key].(string)
	return value
}

func eventContainsSensitivePayload(event observability.Event, secrets ...string) bool {
	payload, err := json.Marshal(event)
	if err != nil {
		return true
	}
	text := string(payload)
	for _, secret := range secrets {
		if secret != "" && strings.Contains(text, secret) {
			return true
		}
	}
	return false
}
