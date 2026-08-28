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
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestClassifyCapabilityAndOperation(t *testing.T) {
	tests := []struct {
		path       string
		capability string
		operation  string
	}{
		{path: "/aiserver.v1.FileSyncService/UnknownOp", capability: "filesync", operation: "filesync.request"},
		{path: "/aiserver.v1.AiService/WriteGitCommitMessage", capability: "git", operation: "git.request"},
		{path: "/aiserver.v1.RepositoryService/FastUpdateFileV2", capability: "repository", operation: "repository.request"},
		{path: "/aiserver.v1.BidiService/BidiAppend", capability: "unknown", operation: "transport.forward"},
		{path: "/unknown/resource", capability: "unknown", operation: "transport.forward"},
	}
	for _, test := range tests {
		if got := ClassifyCapability(test.path); got != test.capability {
			t.Fatalf("ClassifyCapability(%q) = %q, want %q", test.path, got, test.capability)
		}
		if got := ClassifyOperation(test.capability); got != test.operation {
			t.Fatalf("ClassifyOperation(%q) = %q, want %q", test.capability, got, test.operation)
		}
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

func TestGoproxyHandshakeCorrelatesConnectionID(t *testing.T) {
	capture := &recordingCapture{}
	server, err := NewProxyServer("127.0.0.1:0", "http://127.0.0.1:9", "", "", nil, capture)
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}
	ctx := &goproxy.ProxyCtx{Session: 9}
	server.recordConnectDecided(ctx, "api2.cursor.sh:443", actionMITM)
	server.proxy.Logger.Printf("[%03d] WARN: Cannot handshake client %v %v\n", 9, "api2.cursor.sh:443", "remote error: tls: unknown certificate")

	events := capture.snapshot()
	var decided, handshake observability.Event
	for _, event := range events {
		switch event.Event {
		case "connect_decided":
			decided = event
		case "tls_handshake_failed":
			handshake = event
		}
	}
	connectionID := fieldString(decided, "connection_id")
	if connectionID == "" {
		t.Fatalf("connect_decided missing connection_id: %#v", decided.Fields)
	}
	if fieldString(handshake, "connection_id") != connectionID {
		t.Fatalf("handshake connection_id = %q, want %q", fieldString(handshake, "connection_id"), connectionID)
	}
	if handshake.Direction != observability.DirectionCursorToProxy || handshake.ErrorCategory != errorClientUnknownCA {
		t.Fatalf("handshake event = %+v", handshake)
	}
}

func TestTypedNilCaptureFallsBackToProcessSink(t *testing.T) {
	previous := observability.ProcessSink()
	t.Cleanup(func() { observability.SetProcessSink(previous) })

	recorder, err := observability.NewRecorder(t.TempDir(), observability.Settings{Mode: observability.ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	observability.SetProcessSink(recorder)
	status := recorder.Status()

	var typedNil *observability.Controller
	var capture captureRecorder = typedNil
	recordMitmCapture(capture, context.Background(), observability.Event{
		Layer:     "mitm",
		Event:     "connect_decided",
		Direction: observability.DirectionCursorToProxy,
		Fields:    mitmFields(map[string]any{"action": actionPassthrough, "host": "example.com"}),
	})
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	events := readTraceEvents(t, status.SessionPath)
	if len(events) != 1 || events[0].Event != "connect_decided" {
		t.Fatalf("events = %+v", events)
	}
	if fieldString(events[0], "action") != actionPassthrough {
		t.Fatalf("fields = %#v", events[0].Fields)
	}
}

func TestExplicitCaptureIsPreferredOverProcessSink(t *testing.T) {
	previous := observability.ProcessSink()
	t.Cleanup(func() { observability.SetProcessSink(previous) })

	recorder, err := observability.NewRecorder(t.TempDir(), observability.Settings{Mode: observability.ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	t.Cleanup(func() { _ = recorder.Close() })
	observability.SetProcessSink(recorder)
	status := recorder.Status()

	memory := &recordingCapture{}
	server := &ProxyServer{capture: memory, sessions: newConnectSessionStore()}
	server.recordConnectDecided(&goproxy.ProxyCtx{Session: 1}, "api2.cursor.sh:443", actionMITM)
	if events := memory.snapshot(); len(events) != 1 || events[0].Event != "connect_decided" {
		t.Fatalf("explicit capture events = %+v", events)
	}
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	if payload := readTraceFile(t, status.SessionPath); strings.Contains(payload, "connect_decided") {
		t.Fatalf("process sink received duplicate event: %s", payload)
	}
}

func TestHandshakeCountSamplerBoundsDuplicateAppText(t *testing.T) {
	sampler := newHandshakeCountSampler(time.Hour, 2)
	first := sampler.Observe("goproxy_client_handshake|api2.cursor.sh:443|client_unknown_ca")
	if !first.ShouldLog || first.Summary || first.Total != 1 || first.Sampled != 1 {
		t.Fatalf("first sample = %+v", first)
	}
	second := sampler.Observe("goproxy_client_handshake|api2.cursor.sh:443|client_unknown_ca")
	if !second.ShouldLog || second.Summary || second.Total != 2 {
		t.Fatalf("second sample = %+v", second)
	}
	third := sampler.Observe("goproxy_client_handshake|api2.cursor.sh:443|client_unknown_ca")
	if third.ShouldLog || third.Total != 3 {
		t.Fatalf("third sample should be suppressed: %+v", third)
	}
}

func TestProductionTraceRecorderReceivesMitmEventsWithoutExplicitCapture(t *testing.T) {
	previous := observability.ProcessSink()
	t.Cleanup(func() { observability.SetProcessSink(previous) })

	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("backend missing original authorization")
		}
		writer.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(writer, "ok")
	}))
	t.Cleanup(backend.Close)

	server, err := NewProxyServer("127.0.0.1:0", backend.URL, "", "", nil)
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}
	if selectConnectAction("api2.cursor.sh:443", &goproxy.ConnectAction{Action: goproxy.ConnectMitm}) == goproxy.OkConnect {
		t.Fatal("whitelist CONNECT action changed")
	}
	if selectConnectAction("example.com:443", &goproxy.ConnectAction{Action: goproxy.ConnectMitm}) != goproxy.OkConnect {
		t.Fatal("non-whitelist CONNECT action changed")
	}

	controller, err := observability.NewController(t.TempDir(), observability.Settings{
		Mode:          observability.ModeBasic,
		RetentionDays: 7,
		MaxDiskMB:     64,
	})
	if err != nil {
		t.Fatalf("NewController() error = %v", err)
	}
	t.Cleanup(func() { _ = controller.Close() })
	status := controller.Status()

	mitmCtx := &goproxy.ProxyCtx{Session: 42}
	server.recordConnectDecided(mitmCtx, "api2.cursor.sh:443", actionMITM)
	passCtx := &goproxy.ProxyCtx{Session: 43}
	server.recordConnectDecided(passCtx, "example.com:443", actionPassthrough)
	server.proxy.Logger.Printf("[%03d] WARN: Cannot handshake client %v %v\n", 42, "api2.cursor.sh:443", "remote error: tls: unknown certificate")
	server.proxy.Logger.Printf("[%03d] WARN: Cannot handshake client %v %v\n", 7, "api3.cursor.sh:443", "x509: certificate signed by unknown authority")

	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend?token=query-secret", strings.NewReader(`{"prompt":"body-secret"}`))
	req.Host = "api2.cursor.sh"
	req.Header.Set("Authorization", "Bearer secret-token")
	req.Header.Set("Cookie", "session=cookie-secret")
	resp, err := server.forwardToServer(req, connectStateFromCtx(mitmCtx))
	if err != nil {
		t.Fatalf("forwardToServer() error = %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	_ = resp.Body.Close()

	if err := controller.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	events := readTraceEvents(t, status.SessionPath)
	payload := readTraceFile(t, status.SessionPath)
	secrets := []string{"secret-token", "query-secret", "cookie-secret", "body-secret", "Authorization", "Cookie", "token="}
	for _, secret := range secrets {
		if strings.Contains(payload, secret) {
			t.Fatalf("events.jsonl leaked %q: %s", secret, payload)
		}
	}

	var (
		mitmConnect, passConnect, handshake observability.Event
		forwardStart, forwardFinish         int
	)
	for _, event := range events {
		switch {
		case event.Event == "connect_decided" && fieldString(event, "action") == actionMITM:
			mitmConnect = event
		case event.Event == "connect_decided" && fieldString(event, "action") == actionPassthrough:
			passConnect = event
		case event.Event == "tls_handshake_failed" && fieldString(event, "host") == "api2.cursor.sh":
			handshake = event
		case event.Event == "backend_forward_started":
			forwardStart++
		case event.Event == "backend_forward_finished":
			forwardFinish++
		}
	}
	if mitmConnect.Event == "" || fieldString(mitmConnect, "connection_id") == "" {
		t.Fatalf("missing mitm connect_decided: %+v", events)
	}
	if passConnect.Event == "" || fieldString(passConnect, "action") != actionPassthrough {
		t.Fatalf("missing passthrough connect_decided: %+v", events)
	}
	if handshake.Direction != observability.DirectionCursorToProxy || handshake.ErrorCategory != errorClientUnknownCA {
		t.Fatalf("client handshake = %+v", handshake)
	}
	if fieldString(handshake, "tls_role") != tlsRoleServer {
		t.Fatalf("handshake tls_role = %q", fieldString(handshake, "tls_role"))
	}
	if fieldString(handshake, "connection_id") != fieldString(mitmConnect, "connection_id") {
		t.Fatalf("handshake connection_id = %q, connect = %q", fieldString(handshake, "connection_id"), fieldString(mitmConnect, "connection_id"))
	}
	if forwardStart != 1 || forwardFinish != 1 {
		t.Fatalf("backend_forward start=%d finish=%d events=%+v", forwardStart, forwardFinish, events)
	}

	var upstreamUnknownCA bool
	for _, event := range events {
		if event.Event == "tls_handshake_failed" && event.ErrorCategory == errorUpstreamUnknownCA {
			upstreamUnknownCA = true
		}
		if event.Event == "tls_handshake_failed" && fieldString(event, "host") == "api3.cursor.sh" {
			if event.Direction != observability.DirectionCursorToProxy || event.ErrorCategory != errorClientUnknownCA {
				t.Fatalf("goproxy client unknown CA on api3 = %+v", event)
			}
		}
	}
	if upstreamUnknownCA {
		t.Fatal("client handshake was labeled upstream_unknown_ca")
	}
}

func TestUpstreamTLSHandshakeStaysDistinctInTrace(t *testing.T) {
	previous := observability.ProcessSink()
	t.Cleanup(func() { observability.SetProcessSink(previous) })

	recorder, err := observability.NewRecorder(t.TempDir(), observability.Settings{Mode: observability.ModeBasic, RetentionDays: 7, MaxDiskMB: 64})
	if err != nil {
		t.Fatalf("NewRecorder() error = %v", err)
	}
	observability.SetProcessSink(recorder)
	status := recorder.Status()

	recordMitmCapture(nil, context.Background(), handshakeEvent(handshakeObservation{
		Source: handshakeSourceUpstream,
		Host:   "api2.cursor.sh:443",
		Action: actionBackendForward,
		Err:    x509.UnknownAuthorityError{Cert: &x509.Certificate{}},
	}, &connectState{ConnectionID: "conn-up", Action: actionBackendForward, Host: "api2.cursor.sh:443"}))
	if err := recorder.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	events := readTraceEvents(t, status.SessionPath)
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	event := events[0]
	if event.Event != "tls_handshake_failed" || event.Direction != observability.DirectionProxyToUpstream || event.ErrorCategory != errorUpstreamUnknownCA {
		t.Fatalf("upstream event = %+v", event)
	}
	if fieldString(event, "tls_role") != tlsRoleClient || fieldString(event, "connection_id") != "conn-up" {
		t.Fatalf("fields = %#v", event.Fields)
	}
}

func readTraceEvents(t *testing.T, sessionPath string) []observability.Event {
	t.Helper()
	payload := strings.TrimSpace(readTraceFile(t, sessionPath))
	if payload == "" {
		return nil
	}
	lines := strings.Split(payload, "\n")
	events := make([]observability.Event, 0, len(lines))
	for _, line := range lines {
		var event observability.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			t.Fatalf("decode event %q: %v", line, err)
		}
		events = append(events, event)
	}
	return events
}

func readTraceFile(t *testing.T, sessionPath string) string {
	t.Helper()
	payload, err := os.ReadFile(filepath.Join(sessionPath, "events.jsonl"))
	if err != nil {
		t.Fatalf("read events.jsonl: %v", err)
	}
	return string(payload)
}

func TestHandshakeEventSeverityIsDecoupledFromStatus(t *testing.T) {
	client := handshakeEvent(handshakeObservation{
		Source:  handshakeSourceGoproxyClient,
		Host:    "api2.cursor.sh:443",
		ErrText: "remote error: tls: unknown certificate",
	}, &connectState{ConnectionID: "conn-client", Action: actionMITM, Host: "api2.cursor.sh:443"})
	if client.Status != "error" || client.ErrorCategory != errorClientUnknownCA || client.Severity != observability.SeverityWarning {
		t.Fatalf("client handshake = %+v", client)
	}
	if fieldString(client, "source") == "" || fieldString(client, "host") != "api2.cursor.sh" || fieldString(client, "connection_id") != "conn-client" {
		t.Fatalf("client handshake lost query fields: %#v", client.Fields)
	}

	mismatch := handshakeEvent(handshakeObservation{
		Source: handshakeSourceHTTPServer,
		Err:    tls.RecordHeaderError{Msg: "first record does not look like a TLS handshake"},
	}, nil)
	if mismatch.ErrorCategory != errorHandshakeMismatch || mismatch.Severity != observability.SeverityWarning || mismatch.Status != "error" {
		t.Fatalf("mismatch handshake = %+v", mismatch)
	}

	upstream := handshakeEvent(handshakeObservation{
		Source: handshakeSourceUpstream,
		Host:   "api2.cursor.sh:443",
		Action: actionBackendForward,
		Err:    x509.UnknownAuthorityError{Cert: &x509.Certificate{}},
	}, &connectState{ConnectionID: "conn-up", Action: actionBackendForward, Host: "api2.cursor.sh:443"})
	if upstream.ErrorCategory != errorUpstreamUnknownCA || upstream.Severity != observability.SeverityError || upstream.Status != "error" {
		t.Fatalf("upstream handshake = %+v", upstream)
	}
	if !isClientHandshakeNoise(errorClientUnknownCA) || !isClientHandshakeNoise(errorHandshakeMismatch) || isClientHandshakeNoise(errorUpstreamUnknownCA) || isClientHandshakeNoise("backend_unavailable") {
		t.Fatalf("client handshake sampling allowlist is wrong")
	}
}

func TestBackendForwardExpected404KeepsHTTPSemanticsAndUnknownPathStaysUnknown(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/aiserver.v1.FileSyncService/UnknownOp":
			writer.WriteHeader(http.StatusNotFound)
		case "/aiserver.v1.AiService/WriteGitCommitMessage":
			writer.WriteHeader(http.StatusNotFound)
		case "/aiserver.v1.BidiService/BidiAppend":
			writer.WriteHeader(http.StatusBadGateway)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(backend.Close)

	capture := &recordingCapture{}
	server, err := NewProxyServer("127.0.0.1:0", backend.URL, "", "", nil, capture)
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}

	forward := func(path string) observability.Event {
		req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh"+path, strings.NewReader("{}"))
		req.Host = "api2.cursor.sh"
		resp, err := server.forwardToServer(req, &connectState{ConnectionID: "conn-404", Action: actionMITM, Host: "api2.cursor.sh:443"})
		if err != nil {
			t.Fatalf("forwardToServer(%s) error = %v", path, err)
		}
		status := resp.StatusCode
		_ = resp.Body.Close()
		events := capture.snapshot()
		var finished observability.Event
		for _, event := range events {
			if event.Event == "backend_forward_finished" && event.Route == redactObservabilityPath(path) {
				finished = event
			}
		}
		if finished.Event == "" {
			t.Fatalf("missing backend_forward_finished for %s in %+v", path, events)
		}
		if statusCodeOf(finished) != status {
			t.Fatalf("HTTP status changed for %s: response=%d fields=%#v", path, status, finished.Fields)
		}
		return finished
	}

	filesync := forward("/aiserver.v1.FileSyncService/UnknownOp")
	if filesync.Status != "error" || filesync.ErrorCategory != "client_error" || filesync.Severity != observability.SeverityWarning {
		t.Fatalf("filesync 404 event = %+v", filesync)
	}
	if filesync.Capability != "filesync" || filesync.Operation != "filesync.request" || statusCodeOf(filesync) != http.StatusNotFound {
		t.Fatalf("filesync 404 classification = %+v fields=%#v", filesync, filesync.Fields)
	}

	scm := forward("/aiserver.v1.AiService/WriteGitCommitMessage")
	if scm.Status != "error" || scm.Capability != "git" || scm.Severity != observability.SeverityWarning || statusCodeOf(scm) != http.StatusNotFound {
		t.Fatalf("scm 404 event = %+v fields=%#v", scm, scm.Fields)
	}

	unknown := forward("/mystery/endpoint")
	if unknown.ImplementationState != observability.ImplementationUnknown || unknown.Capability != "unknown" {
		t.Fatalf("unknown path event = %+v", unknown)
	}
	if unknown.Severity != observability.SeverityWarning || unknown.Status != "error" {
		t.Fatalf("unknown 404 should stay warning without changing HTTP status: %+v", unknown)
	}

	serverError := forward("/aiserver.v1.BidiService/BidiAppend")
	if serverError.Severity != observability.SeverityError || serverError.ErrorCategory != "server_error" || statusCodeOf(serverError) != http.StatusBadGateway {
		t.Fatalf("5xx event = %+v fields=%#v", serverError, serverError.Fields)
	}
}

func TestBackendUnavailableStaysError(t *testing.T) {
	capture := &recordingCapture{}
	server, err := NewProxyServer("127.0.0.1:0", "http://127.0.0.1:1", "", "", nil, capture)
	if err != nil {
		t.Fatalf("NewProxyServer() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "https://api2.cursor.sh/aiserver.v1.BidiService/BidiAppend", strings.NewReader("{}"))
	req.Host = "api2.cursor.sh"
	_, err = server.forwardToServer(req, &connectState{ConnectionID: "conn-down"})
	if err == nil {
		t.Fatal("expected backend unavailable")
	}
	var finished observability.Event
	for _, event := range capture.snapshot() {
		if event.Event == "backend_forward_finished" {
			finished = event
		}
	}
	if finished.ErrorCategory != "backend_unavailable" || finished.Severity != observability.SeverityError || finished.Status != "error" {
		t.Fatalf("unavailable event = %+v", finished)
	}
}

func statusCodeOf(event observability.Event) int {
	if event.Fields == nil {
		return 0
	}
	switch value := event.Fields["status_code"].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	default:
		return 0
	}
}
