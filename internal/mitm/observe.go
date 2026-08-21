package mitm

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"reflect"
	"strings"
	"sync"
	"time"

	"cursor/internal/observability"

	"github.com/elazarl/goproxy"
	"golang.org/x/net/http2"
)

const (
	actionMITM           = "mitm"
	actionPassthrough    = "passthrough"
	actionBackendForward = "backend_forward"

	tlsRoleServer = "server"
	tlsRoleClient = "client"

	handshakeSourceGoproxyClient = "goproxy_client_handshake"
	handshakeSourceHTTPServer    = "http_server"
	handshakeSourceUpstream      = "upstream"
	handshakeSourceTLSConfig     = "mitm_tls_config"

	errorClientUnknownCA            = "client_unknown_ca"
	errorUpstreamUnknownCA          = "upstream_unknown_ca"
	errorUpstreamRemoteUnknownCert  = "upstream_remote_unknown_certificate"
	errorHandshakeMismatch          = "handshake_mismatch"
	errorHostnameMismatch           = "hostname_mismatch"
	errorClientTLSHandshakeFailed   = "client_tls_handshake_failed"
	errorUpstreamTLSHandshakeFailed = "upstream_tls_handshake_failed"
	errorUpstreamHTTP2              = "upstream_http2"
	errorMITMTLSConfigFailed        = "mitm_tls_config_failed"

	maxErrorMessageRunes = 240

	connectSessionStoreLimit = 2048
	handshakeAppSampleLimit  = 2
	handshakeAppLogWindow    = 30 * time.Second
	handshakeAppLogTTL       = 5 * time.Minute
	handshakeAppLogMaxKeys   = 1024
)

type connectState struct {
	ConnectionID string
	Action       string
	Host         string
}

type handshakeObservation struct {
	Source  string
	Host    string
	Remote  string
	Session string
	Action  string
	Err     error
	ErrText string
}

func selectConnectAction(host string, mitmAction *goproxy.ConnectAction) *goproxy.ConnectAction {
	if mitmAction == nil || !isWhitelistedRelayHost(host) {
		return goproxy.OkConnect
	}
	return mitmAction
}

func connectActionName(action *goproxy.ConnectAction) string {
	if action != nil && action.Action == goproxy.ConnectMitm {
		return actionMITM
	}
	return actionPassthrough
}

func connectStateFromCtx(ctx *goproxy.ProxyCtx) *connectState {
	if ctx == nil {
		return nil
	}
	state, _ := ctx.UserData.(*connectState)
	return state
}

func attachConnectState(ctx *goproxy.ProxyCtx, host, action string) *connectState {
	state := &connectState{
		ConnectionID: newConnectionID(),
		Action:       action,
		Host:         host,
	}
	if ctx != nil {
		ctx.UserData = state
	}
	return state
}

func newConnectionID() string {
	return observability.NewTrace().SpanID
}

func recordMitmCapture(capture captureRecorder, ctx context.Context, event observability.Event) {
	capture = resolveCapture(capture)
	if capture == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	defer func() { _ = recover() }()
	capture.Record(ctx, observability.Capture{Event: sanitizeMitmEvent(event)})
}

func resolveCapture(capture captureRecorder) captureRecorder {
	if !isNilCapture(capture) {
		return capture
	}
	if sink := observability.ProcessSink(); !isNilCapture(sink) {
		return sink
	}
	return nil
}

func isNilCapture(capture captureRecorder) bool {
	if capture == nil {
		return true
	}
	value := reflect.ValueOf(capture)
	switch value.Kind() {
	case reflect.Pointer, reflect.Interface, reflect.Map, reflect.Slice, reflect.Func:
		return value.IsNil()
	default:
		return false
	}
}

func (s *ProxyServer) recordConnectDecided(ctx *goproxy.ProxyCtx, host, action string) {
	defer func() { _ = recover() }()
	state := attachConnectState(ctx, host, action)
	if s != nil && ctx != nil {
		s.sessions.Remember(ctx.Session, state)
	}
	hostname, port := splitConnectHostPort(host)
	tlsRole := ""
	if action == actionMITM {
		tlsRole = tlsRoleServer
	}
	s.recordCapture(requestContext(ctx), observability.Event{
		Layer:               "mitm",
		Event:               "connect_decided",
		Capability:          "unknown",
		Operation:           "mitm.connect",
		Direction:           observability.DirectionCursorToProxy,
		Protocol:            "connect",
		Status:              "ok",
		SemanticOutcome:     observability.OutcomeSucceeded,
		ImplementationState: observability.ImplementationImplemented,
		Fields: mitmFields(map[string]any{
			"connection_id": state.ConnectionID,
			"action":        action,
			"host":          hostname,
			"port":          port,
			"method":        "CONNECT",
			"traffic_class": ClassifyTraffic(hostname, "CONNECT", ""),
			"host_hint":     hostHint(host),
			"tls_role":      tlsRole,
			"source":        "connect",
		}),
	})
}

type handshakeClass struct {
	Direction     string
	TLSRole       string
	Action        string
	ErrorCategory string
	Source        string
}

func classifyHandshake(observation handshakeObservation) handshakeClass {
	source := strings.TrimSpace(observation.Source)
	class := handshakeClass{
		Source: source,
		Action: firstNonEmpty(observation.Action, defaultHandshakeAction(source)),
	}
	if isClientHandshakeSource(source) {
		class.Direction = observability.DirectionCursorToProxy
		class.TLSRole = tlsRoleServer
	} else {
		class.Direction = observability.DirectionProxyToUpstream
		class.TLSRole = tlsRoleClient
	}
	if source == handshakeSourceTLSConfig {
		class.Direction = observability.DirectionProxyInternal
		class.TLSRole = tlsRoleServer
		class.Action = actionMITM
	}
	class.ErrorCategory = classifyTLSErrorCategory(source, observation.Err, observation.ErrText)
	return class
}

func defaultHandshakeAction(source string) string {
	switch source {
	case handshakeSourceGoproxyClient, handshakeSourceTLSConfig:
		return actionMITM
	default:
		return actionPassthrough
	}
}

func isClientHandshakeSource(source string) bool {
	return source == handshakeSourceGoproxyClient || source == handshakeSourceHTTPServer
}

func classifyTLSErrorCategory(source string, err error, errText string) string {
	if source == handshakeSourceTLSConfig {
		return errorMITMTLSConfigFailed
	}
	if err != nil {
		var rec tls.RecordHeaderError
		if errors.As(err, &rec) {
			return errorHandshakeMismatch
		}
		var unknownAuth x509.UnknownAuthorityError
		if errors.As(err, &unknownAuth) {
			return errorUpstreamUnknownCA
		}
		var hostnameErr x509.HostnameError
		if errors.As(err, &hostnameErr) {
			return errorHostnameMismatch
		}
		var certInvalid x509.CertificateInvalidError
		if errors.As(err, &certInvalid) {
			if isClientHandshakeSource(source) {
				return errorClientTLSHandshakeFailed
			}
			return errorUpstreamTLSHandshakeFailed
		}
		if alertText, ok := peerTLSAlertText(err); ok {
			return tlsAlertCategory(source, alertText)
		}
		if isHTTP2Error(err) {
			return errorUpstreamHTTP2
		}
		errText = err.Error()
	}
	return tlsAlertCategory(source, errText)
}

func tlsAlertCategory(source, text string) string {
	lower := strings.ToLower(strings.TrimSpace(text))
	switch {
	case strings.Contains(lower, "first record does not look like a tls handshake"):
		return errorHandshakeMismatch
	case isUnknownCertificateText(lower):
		if isClientHandshakeSource(source) {
			return errorClientUnknownCA
		}
		if strings.Contains(lower, "certificate signed by unknown") || strings.Contains(lower, "unknown authority") {
			return errorUpstreamUnknownCA
		}
		return errorUpstreamRemoteUnknownCert
	case strings.Contains(lower, "http2") || strings.Contains(lower, "http/2"):
		if isClientHandshakeSource(source) {
			return errorClientTLSHandshakeFailed
		}
		return errorUpstreamHTTP2
	case isClientHandshakeSource(source):
		return errorClientTLSHandshakeFailed
	default:
		return errorUpstreamTLSHandshakeFailed
	}
}

func isUnknownCertificateText(lower string) bool {
	return strings.Contains(lower, "unknown certificate") ||
		strings.Contains(lower, "unknown certificate authority") ||
		strings.Contains(lower, "certificate signed by unknown")
}

func peerTLSAlertText(err error) (string, bool) {
	if err == nil {
		return "", false
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) && opErr != nil && opErr.Op == "remote error" && opErr.Err != nil {
		return opErr.Err.Error(), true
	}
	var alert tls.AlertError
	if errors.As(err, &alert) {
		return alert.Error(), true
	}
	return "", false
}

func isHTTP2Error(err error) bool {
	if err == nil {
		return false
	}
	var streamErr http2.StreamError
	var goAway http2.GoAwayError
	var connErr http2.ConnectionError
	return errors.As(err, &streamErr) || errors.As(err, &goAway) || errors.As(err, &connErr)
}

func isTLSRelated(err error, errText string) bool {
	if err != nil {
		var rec tls.RecordHeaderError
		var unknownAuth x509.UnknownAuthorityError
		var hostnameErr x509.HostnameError
		var certInvalid x509.CertificateInvalidError
		if errors.As(err, &rec) || errors.As(err, &unknownAuth) || errors.As(err, &hostnameErr) || errors.As(err, &certInvalid) {
			return true
		}
		if _, ok := peerTLSAlertText(err); ok {
			return true
		}
		if isHTTP2Error(err) {
			return true
		}
		errText = err.Error()
	}
	lower := strings.ToLower(strings.TrimSpace(errText))
	if lower == "" {
		return false
	}
	return strings.Contains(lower, "tls:") ||
		strings.Contains(lower, "x509:") ||
		strings.Contains(lower, "http2") ||
		strings.Contains(lower, "handshake")
}

func parseGoproxyTLSLog(msg string) (handshakeObservation, bool) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return handshakeObservation{}, false
	}
	session := ""
	if strings.HasPrefix(msg, "[") {
		end := strings.Index(msg, "]")
		if end > 0 {
			session = strings.TrimSpace(msg[1:end])
			msg = strings.TrimSpace(msg[end+1:])
		}
	}
	msg = strings.TrimSpace(strings.TrimPrefix(msg, "WARN:"))
	msg = strings.TrimSpace(strings.TrimPrefix(msg, "INFO:"))

	const clientPrefix = "Cannot handshake client "
	if strings.HasPrefix(msg, clientPrefix) {
		host, errText := splitHostAndRemainder(strings.TrimSpace(msg[len(clientPrefix):]))
		return handshakeObservation{
			Source:  handshakeSourceGoproxyClient,
			Host:    host,
			Session: session,
			Action:  actionMITM,
			ErrText: errText,
		}, true
	}
	return handshakeObservation{}, false
}

func parseHTTPServerTLSLog(msg string) (handshakeObservation, bool) {
	original := strings.TrimSpace(msg)
	lower := strings.ToLower(original)
	const marker = "tls handshake error from "
	idx := strings.Index(lower, marker)
	if idx < 0 {
		return handshakeObservation{}, false
	}
	rest := strings.TrimSpace(original[idx+len(marker):])
	remote, errText := splitHostAndRemainder(rest)
	return handshakeObservation{
		Source:  handshakeSourceHTTPServer,
		Remote:  remote,
		ErrText: errText,
		Action:  actionMITM,
	}, true
}

func splitHostAndRemainder(value string) (host, remainder string) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", ""
	}
	host, remainder, ok := strings.Cut(value, " ")
	if !ok {
		return strings.TrimRight(value, ":"), ""
	}
	return strings.TrimRight(host, ":"), strings.TrimSpace(remainder)
}

func observeGoproxyLog(capture captureRecorder, sessions *connectSessionStore, msg string) (handshakeObservation, bool) {
	observation, ok := parseGoproxyTLSLog(msg)
	if !ok {
		return handshakeObservation{}, false
	}
	recordMitmCapture(capture, context.Background(), handshakeEvent(observation, sessions.Lookup(observation.Session, observation.Host)))
	return observation, true
}

func observeHTTPServerLog(capture captureRecorder, msg string) {
	observation, ok := parseHTTPServerTLSLog(msg)
	if !ok {
		return
	}
	recordMitmCapture(capture, context.Background(), handshakeEvent(observation, nil))
}

func handshakeEvent(observation handshakeObservation, state *connectState) observability.Event {
	classified := classifyHandshake(observation)
	hostname, port := splitConnectHostPort(observation.Host)
	fields := map[string]any{
		"action":          classified.Action,
		"host":            hostname,
		"port":            port,
		"tls_role":        classified.TLSRole,
		"traffic_class":   ClassifyTraffic(hostname, "", ""),
		"host_hint":       hostHint(observation.Host),
		"source":          classified.Source,
		"error_message":   redactErrorMessage(firstNonEmpty(observation.ErrText, errorText(observation.Err))),
		"goproxy_session": observation.Session,
		"remote":          observation.Remote,
	}
	if state != nil {
		fields["connection_id"] = state.ConnectionID
		if hostname == "" {
			host, portFromState := splitConnectHostPort(state.Host)
			fields["host"] = host
			if port == "" {
				fields["port"] = portFromState
			}
			fields["host_hint"] = hostHint(state.Host)
			fields["traffic_class"] = ClassifyTraffic(host, "", "")
		}
		if state.Action != "" {
			fields["action"] = state.Action
		}
	}
	return observability.Event{
		Layer:               "mitm",
		Event:               "tls_handshake_failed",
		Capability:          "unknown",
		Operation:           "mitm.tls_handshake",
		Direction:           classified.Direction,
		Protocol:            "tls",
		Status:              "error",
		SemanticOutcome:     observability.OutcomeFailed,
		ImplementationState: observability.ImplementationImplemented,
		ErrorCategory:       classified.ErrorCategory,
		Fields:              mitmFields(fields),
	}
}

func backendForwardFields(incomingHost, method, path, action string, state *connectState, extra map[string]any) map[string]any {
	hostname, port := splitConnectHostPort(incomingHost)
	redactedPath := redactObservabilityPath(path)
	fields := map[string]any{
		"action":        firstNonEmpty(action, actionBackendForward),
		"host":          hostname,
		"port":          port,
		"method":        method,
		"path":          redactedPath,
		"traffic_class": ClassifyTraffic(hostname, method, path),
		"host_hint":     hostHint(incomingHost),
		"tls_role":      tlsRoleClient,
	}
	if state != nil {
		fields["connection_id"] = state.ConnectionID
	}
	for key, value := range extra {
		fields[key] = value
	}
	return mitmFields(fields)
}

func sanitizeMitmEvent(event observability.Event) observability.Event {
	event.Route = redactObservabilityPath(event.Route)
	event.ErrorCategory = strings.TrimSpace(event.ErrorCategory)
	event.Fields = mitmFields(event.Fields)
	return event
}

func mitmFields(input map[string]any) map[string]any {
	if len(input) == 0 {
		return nil
	}
	result := make(map[string]any, len(input))
	for key, value := range input {
		if isForbiddenObservabilityKey(key) {
			continue
		}
		switch typed := value.(type) {
		case string:
			trimmed := strings.TrimSpace(typed)
			if trimmed == "" {
				continue
			}
			result[key] = observability.SanitizeText(trimmed)
		case nil:
			continue
		default:
			result[key] = value
		}
	}
	if message, ok := result["error_message"].(string); ok {
		result["error_message"] = truncateRunes(message, maxErrorMessageRunes)
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

func isForbiddenObservabilityKey(key string) bool {
	normalized := normalizeFieldKey(key)
	switch normalized {
	case "authorization", "proxyauthorization", "cookie", "setcookie", "apikey", "xapikey",
		"token", "bearertoken", "query", "rawquery", "body", "url", "rawurl", "fullurl",
		"header", "headers", "requestbody", "responsebody", "cookieheader":
		return true
	default:
		return strings.Contains(normalized, "authorization") ||
			strings.Contains(normalized, "cookie") ||
			strings.Contains(normalized, "apikey")
	}
}

func redactErrorMessage(value string) string {
	return truncateRunes(observability.SanitizeText(strings.TrimSpace(value)), maxErrorMessageRunes)
}

func errorText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func requestContext(ctx *goproxy.ProxyCtx) context.Context {
	if ctx == nil || ctx.Req == nil {
		return context.Background()
	}
	return ctx.Req.Context()
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if normalized := strings.TrimSpace(value); normalized != "" {
			return normalized
		}
	}
	return ""
}

type connectSessionStore struct {
	mu      sync.Mutex
	entries map[string]*connectState
	order   []string
}

func newConnectSessionStore() *connectSessionStore {
	return &connectSessionStore{entries: make(map[string]*connectState)}
}

func goproxySessionLabel(session int64) string {
	return fmt.Sprintf("%03d", session&0xFFFF)
}

func connectSessionKey(session, host string) string {
	session = strings.TrimSpace(session)
	if session == "" {
		return ""
	}
	host = strings.ToLower(strings.TrimSpace(host))
	return session + "|" + host
}

func (store *connectSessionStore) Remember(session int64, state *connectState) {
	if store == nil || state == nil {
		return
	}
	key := connectSessionKey(goproxySessionLabel(session), state.Host)
	if key == "" {
		return
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if _, exists := store.entries[key]; !exists {
		store.order = append(store.order, key)
	}
	store.entries[key] = state
	for len(store.entries) > connectSessionStoreLimit && len(store.order) > 0 {
		oldest := store.order[0]
		store.order = store.order[1:]
		delete(store.entries, oldest)
	}
}

func (store *connectSessionStore) Lookup(session, host string) *connectState {
	if store == nil {
		return nil
	}
	key := connectSessionKey(session, host)
	if key == "" {
		return nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.entries[key]
}

type handshakeSampleDecision struct {
	Total     int
	Sampled   int
	ShouldLog bool
	Summary   bool
}

type handshakeCountSampler struct {
	mu      sync.Mutex
	window  time.Duration
	limit   int
	entries map[string]*handshakeSampleEntry
}

type handshakeSampleEntry struct {
	total       int
	sampled     int
	nextSummary time.Time
	lastSeen    time.Time
}

func newHandshakeCountSampler(window time.Duration, limit int) *handshakeCountSampler {
	if window <= 0 {
		window = handshakeAppLogWindow
	}
	if limit <= 0 {
		limit = handshakeAppSampleLimit
	}
	return &handshakeCountSampler{
		window:  window,
		limit:   limit,
		entries: make(map[string]*handshakeSampleEntry),
	}
}

func (sampler *handshakeCountSampler) Observe(key string) handshakeSampleDecision {
	if sampler == nil {
		return handshakeSampleDecision{Total: 1, Sampled: 1, ShouldLog: true}
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return handshakeSampleDecision{Total: 1, Sampled: 1, ShouldLog: true}
	}
	now := time.Now()
	sampler.mu.Lock()
	defer sampler.mu.Unlock()
	sampler.pruneLocked(now)

	entry := sampler.entries[key]
	if entry == nil {
		entry = &handshakeSampleEntry{}
		sampler.entries[key] = entry
	}
	entry.total++
	entry.lastSeen = now
	decision := handshakeSampleDecision{Total: entry.total, Sampled: entry.sampled}
	if entry.sampled < sampler.limit {
		entry.sampled++
		decision.Sampled = entry.sampled
		decision.ShouldLog = true
		entry.nextSummary = now.Add(sampler.window)
		return decision
	}
	if !now.Before(entry.nextSummary) {
		decision.ShouldLog = true
		decision.Summary = true
		entry.nextSummary = now.Add(sampler.window)
		return decision
	}
	return decision
}

func (sampler *handshakeCountSampler) pruneLocked(now time.Time) {
	if len(sampler.entries) == 0 {
		return
	}
	cutoff := now.Add(-handshakeAppLogTTL)
	for key, entry := range sampler.entries {
		if entry == nil || entry.lastSeen.Before(cutoff) {
			delete(sampler.entries, key)
		}
	}
	for len(sampler.entries) >= handshakeAppLogMaxKeys {
		oldestKey := ""
		var oldestSeen time.Time
		for key, entry := range sampler.entries {
			if entry == nil {
				oldestKey = key
				break
			}
			if oldestKey == "" || entry.lastSeen.Before(oldestSeen) {
				oldestKey = key
				oldestSeen = entry.lastSeen
			}
		}
		if oldestKey == "" {
			return
		}
		delete(sampler.entries, oldestKey)
	}
}

func handshakeAppLogKey(observation handshakeObservation) string {
	classified := classifyHandshake(observation)
	host := strings.ToLower(strings.TrimSpace(observation.Host))
	return classified.Source + "|" + host + "|" + classified.ErrorCategory
}
