package mitm

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	stdlog "log"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"cursor/internal/certs"
	"cursor/internal/logger"
	"cursor/internal/netproxy"
	"cursor/internal/observability"

	"github.com/elazarl/goproxy"
)

const (
	// HeaderServerUpstreamURL 表示转发给 backend server 时携带的原始上游地址。
	HeaderServerUpstreamURL = "X-Server-Upstream-URL"
	HeaderTraceID           = "X-Cursor-BYOK-Trace-ID"
	HeaderParentSpanID      = "X-Cursor-BYOK-Parent-Span-ID"
)

type captureRecorder interface {
	Record(context.Context, observability.Capture) bool
}

// ProxyServer 定义了当前模块中的 ProxyServer 类型。
type ProxyServer struct {
	// addr 表示当前声明中的 addr。
	addr string
	// baseURL 表示当前声明中的 baseURL。
	baseURL string
	// certManager 表示当前声明中的 certManager。
	certManager *certs.Manager
	// baseEndpoint 表示当前声明中的 baseEndpoint。
	baseEndpoint *url.URL
	// baseMu 表示当前声明中的 baseMu。
	baseMu sync.RWMutex

	// upstreamClient 表示当前声明中的 upstreamClient。
	upstreamClient *http.Client
	capture        captureRecorder
	sessions       *connectSessionStore

	// proxy 表示当前声明中的 proxy。
	proxy *goproxy.ProxyHttpServer

	// runMu 表示当前声明中的 runMu。
	runMu sync.RWMutex
	// httpServer 表示当前声明中的 httpServer。
	httpServer *http.Server
	// serveErrCh 表示当前声明中的 serveErrCh。
	serveErrCh chan error
}

// Snapshot 定义了当前模块中的 Snapshot 类型。
type Snapshot struct {
	// ListenAddr 表示当前声明中的 ListenAddr。
	ListenAddr string `json:"listenAddr"`
	// BaseURL 表示当前声明中的 BaseURL。
	BaseURL string `json:"baseUrl"`
	// Running 表示当前声明中的 Running。
	Running bool `json:"running"`
}

// hopByHopHeaders 表示当前模块中的 hopByHopHeaders 状态值。
var hopByHopHeaders = map[string]struct{}{
	"Connection":          {},
	"Proxy-Connection":    {},
	"Keep-Alive":          {},
	"Proxy-Authenticate":  {},
	"Proxy-Authorization": {},
	"Te":                  {},
	"Trailer":             {},
	"Transfer-Encoding":   {},
	"Upgrade":             {},
}

const (
	proxyLogRateLimitWindow  = 30 * time.Second
	proxyLogRateLimitTTL     = 5 * time.Minute
	proxyLogRateLimitMaxKeys = 1024
)

var (
	proxyLogLimiter     = newLogLimiter(proxyLogRateLimitWindow)
	handshakeAppSampler = newHandshakeCountSampler(handshakeAppLogWindow, handshakeAppSampleLimit)
)

type logLimiter struct {
	mu      sync.Mutex
	window  time.Duration
	entries map[string]*logLimitEntry
}

type logLimitEntry struct {
	nextAllowed time.Time
	lastSeen    time.Time
	suppressed  int
}

func newLogLimiter(window time.Duration) *logLimiter {
	if window <= 0 {
		window = 30 * time.Second
	}
	return &logLimiter{
		window:  window,
		entries: make(map[string]*logLimitEntry),
	}
}

func (limiter *logLimiter) ShouldLog(key string) (suppressed int, ok bool) {
	if limiter == nil {
		return 0, true
	}
	key = strings.TrimSpace(key)
	if key == "" {
		return 0, true
	}

	now := time.Now()
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	limiter.pruneLocked(now)

	entry := limiter.entries[key]
	if entry == nil {
		limiter.entries[key] = &logLimitEntry{nextAllowed: now.Add(limiter.window), lastSeen: now}
		return 0, true
	}
	entry.lastSeen = now
	if now.Before(entry.nextAllowed) {
		entry.suppressed++
		return 0, false
	}

	suppressed = entry.suppressed
	entry.suppressed = 0
	entry.nextAllowed = now.Add(limiter.window)
	return suppressed, true
}

func (limiter *logLimiter) pruneLocked(now time.Time) {
	if len(limiter.entries) == 0 {
		return
	}
	cutoff := now.Add(-proxyLogRateLimitTTL)
	for key, entry := range limiter.entries {
		if entry == nil || entry.lastSeen.Before(cutoff) {
			delete(limiter.entries, key)
		}
	}
	for len(limiter.entries) >= proxyLogRateLimitMaxKeys {
		oldestKey := ""
		var oldestSeen time.Time
		for key, entry := range limiter.entries {
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
		delete(limiter.entries, oldestKey)
	}
}

func logSuppressedProxyMessages(prefix string, suppressed int) {
	if suppressed <= 0 {
		return
	}
	logger.Infof("%s: suppressed %d repeated messages in last %s", prefix, suppressed, proxyLogRateLimitWindow)
}

// NewProxyServer 用于处理与 NewProxyServer 相关的逻辑。
func NewProxyServer(addr, baseURL, _ string, _ string, certManager *certs.Manager, captures ...captureRecorder) (*ProxyServer, error) {
	u, normalizedBaseURL, err := parseBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	var capture captureRecorder
	if len(captures) > 0 {
		capture = captures[0]
	}

	s := &ProxyServer{
		addr:         addr,
		baseURL:      normalizedBaseURL,
		certManager:  certManager,
		baseEndpoint: u,
		capture:      capture,
		sessions:     newConnectSessionStore(),
		upstreamClient: &http.Client{
			Transport: &http.Transport{
				DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				ForceAttemptHTTP2:     true,
				MaxIdleConns:          200,
				IdleConnTimeout:       90 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ExpectContinueTimeout: 1 * time.Second,
				ResponseHeaderTimeout: 60 * time.Second,
			},
		},
	}
	s.proxy = s.newGoproxyHandler()
	return s, nil
}

// parseBaseURL 用于处理与 parseBaseURL 相关的逻辑。
func parseBaseURL(baseURL string) (*url.URL, string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		return nil, "", errors.New("base URL is empty")
	}
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, "", fmt.Errorf("parse base URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, "", fmt.Errorf("unsupported base URL scheme %q", u.Scheme)
	}
	if strings.TrimSpace(u.Host) == "" {
		return nil, "", errors.New("base URL host is empty")
	}

	base := *u
	base.Path = ""
	base.RawPath = ""
	base.RawQuery = ""
	base.Fragment = ""
	base.RawFragment = ""
	normalizedBaseURL := strings.TrimRight(base.String(), "/")
	if normalizedBaseURL == "" {
		return nil, "", errors.New("base URL is empty")
	}
	return &base, normalizedBaseURL, nil
}

// UpdateBaseURL 用于处理与 UpdateBaseURL 相关的逻辑。
func (s *ProxyServer) UpdateBaseURL(baseURL string) error {
	u, normalizedBaseURL, err := parseBaseURL(baseURL)
	if err != nil {
		return err
	}
	s.baseMu.Lock()
	s.baseURL = normalizedBaseURL
	s.baseEndpoint = u
	s.baseMu.Unlock()
	return nil
}

// ListenAndServe 用于处理与 ListenAndServe 相关的逻辑。
func (s *ProxyServer) ListenAndServe() error {
	if err := s.Start(); err != nil {
		return err
	}
	s.runMu.RLock()
	errCh := s.serveErrCh
	s.runMu.RUnlock()
	if errCh == nil {
		return nil
	}
	return <-errCh
}

// Start 用于处理与 Start 相关的逻辑。
func (s *ProxyServer) Start() error {
	s.runMu.Lock()
	defer s.runMu.Unlock()
	if s.httpServer != nil {
		return errors.New("proxy is already running")
	}

	httpServer := &http.Server{
		Addr:     s.addr,
		Handler:  s.proxy,
		ErrorLog: stdlog.New(&httpErrorFilterWriter{capture: s.capture}, "", stdlog.LstdFlags),
	}

	ln, err := net.Listen("tcp", s.addr)
	if err != nil {
		return err
	}

	s.httpServer = httpServer
	s.serveErrCh = make(chan error, 1)

	go func() {
		var serveErr error
		err := httpServer.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			serveErr = err
		}

		s.runMu.Lock()
		if s.serveErrCh != nil {
			s.serveErrCh <- serveErr
			close(s.serveErrCh)
			s.serveErrCh = nil
		}
		if s.httpServer == httpServer {
			s.httpServer = nil
		}
		s.runMu.Unlock()
	}()

	return nil
}

// Stop 用于处理与 Stop 相关的逻辑。
func (s *ProxyServer) Stop(ctx context.Context) error {
	s.runMu.Lock()
	httpServer := s.httpServer
	if httpServer == nil {
		s.runMu.Unlock()
		return nil
	}
	s.httpServer = nil
	s.runMu.Unlock()
	return httpServer.Shutdown(ctx)
}

// IsRunning 用于处理与 IsRunning 相关的逻辑。
func (s *ProxyServer) IsRunning() bool {
	s.runMu.RLock()
	running := s.httpServer != nil
	s.runMu.RUnlock()
	return running
}

// Snapshot 用于处理与 Snapshot 相关的逻辑。
func (s *ProxyServer) Snapshot() Snapshot {
	baseURL := s.currentBaseURL()
	return Snapshot{
		ListenAddr: s.addr,
		BaseURL:    baseURL,
		Running:    s.IsRunning(),
	}
}

// currentBaseURL 用于处理与 currentBaseURL 相关的逻辑。
func (s *ProxyServer) currentBaseURL() string {
	s.baseMu.RLock()
	baseURL := s.baseURL
	s.baseMu.RUnlock()
	return baseURL
}

// currentBaseEndpoint 用于处理与 currentBaseEndpoint 相关的逻辑。
func (s *ProxyServer) currentBaseEndpoint() *url.URL {
	s.baseMu.RLock()
	endpoint := s.baseEndpoint
	s.baseMu.RUnlock()
	if endpoint == nil {
		return nil
	}
	clone := *endpoint
	return &clone
}

// newGoproxyHandler 用于处理与 newGoproxyHandler 相关的逻辑。
func (s *ProxyServer) newGoproxyHandler() *goproxy.ProxyHttpServer {
	proxy := goproxy.NewProxyHttpServer()
	proxy.Verbose = false
	proxy.AllowHTTP2 = true
	proxy.Logger = &goproxyLogAdapter{capture: s.capture, sessions: s.sessions}
	proxy.CertStore = newMITMCertStore()
	proxy.Tr = netproxy.NewTransport(&http.Transport{
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          200,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
	})
	proxy.ConnectionErrHandler = func(conn io.Writer, ctx *goproxy.ProxyCtx, err error) {
		host := requestHost(ctx)
		remoteAddr := requestRemoteAddr(ctx)
		ua := trimForLog(requestUserAgent(ctx), 160)
		msg := fmt.Sprintf("proxy connect error: host=%s remote=%s ua=%q err=%v", host, remoteAddr, ua, err)
		key := "proxy-connect-error|" + strings.ToLower(strings.TrimSpace(host)) + "|" + errorRateLimitKey(err)
		if suppressed, ok := proxyLogLimiter.ShouldLog(key); ok {
			logSuppressedProxyMessages("proxy connect error", suppressed)
			logger.Errorf("%s", msg)
		}
		if isTLSRelated(err, "") {
			recordMitmCapture(s.capture, requestContext(ctx), handshakeEvent(handshakeObservation{
				Source:  handshakeSourceUpstream,
				Host:    host,
				Remote:  remoteAddr,
				Action:  connectActionNameFromState(ctx),
				Err:     err,
				ErrText: errorText(err),
			}, connectStateFromCtx(ctx)))
		}
	}

	var mitmAction *goproxy.ConnectAction
	if s.certManager != nil {
		caCert, err := s.certManager.CATLSCertificate()
		if err != nil {
			logger.Errorf("MITM disabled: invalid CA config err=%v", err)
		} else {
			logMITMCAInfo(caCert)
			baseTLSConfigFromCA := goproxy.TLSConfigFromCA(caCert)
			mitmAction = &goproxy.ConnectAction{
				Action: goproxy.ConnectMitm,
				TLSConfig: func(host string, ctx *goproxy.ProxyCtx) (*tls.Config, error) {
					cfg, err := baseTLSConfigFromCA(host, ctx)
					connectHost := normalizeConnectHost(host)
					remoteAddr := requestRemoteAddr(ctx)
					ua := trimForLog(requestUserAgent(ctx), 160)
					if err != nil {
						logger.Errorf(
							"MITM tls config failed: connect_host=%s remote=%s ua=%q err=%v",
							connectHost,
							remoteAddr,
							ua,
							err,
						)
						recordMitmCapture(s.capture, requestContext(ctx), handshakeEvent(handshakeObservation{
							Source:  handshakeSourceTLSConfig,
							Host:    connectHost,
							Remote:  remoteAddr,
							Action:  actionMITM,
							Err:     err,
							ErrText: errorText(err),
						}, connectStateFromCtx(ctx)))
						return nil, err
					}

					return cfg, nil
				},
			}
		}
	}
	proxy.OnRequest().HandleConnect(goproxy.FuncHttpsHandler(func(host string, ctx *goproxy.ProxyCtx) (*goproxy.ConnectAction, string) {
		action := selectConnectAction(host, mitmAction)
		s.recordConnectDecided(ctx, host, connectActionName(action))
		return action, host
	}))

	// MITM 解密后：Cursor 白名单域名转发到 backend server，其余请求由 goproxy 直连回源。
	proxy.OnRequest().DoFunc(func(req *http.Request, ctx *goproxy.ProxyCtx) (*http.Request, *http.Response) {
		req.Header.Del(HeaderServerUpstreamURL)

		host := hostFromHTTPRequest(req)
		if isWhitelistedRelayHost(host) {
			if shouldHandleLocalCORSPreflight(req) {
				return req, buildLocalCORSPreflightResponse(req)
			}
			raw := requestURL(req)
			if parsedRaw, rawErr := rawURLForRelay(req); rawErr == nil {
				raw = parsedRaw
			}
			resp, err := s.forwardToServer(req, connectStateFromCtx(ctx))
			if err != nil {
				logger.Errorf("转发失败： %s %s %v", req.Method, raw, err)
				return req, goproxy.NewResponse(req, goproxy.ContentTypeText, http.StatusBadGateway, "bad gateway")
			}

			return req, resp
		}

		return req, nil
	})
	return proxy
}

func connectActionNameFromState(ctx *goproxy.ProxyCtx) string {
	if state := connectStateFromCtx(ctx); state != nil && state.Action != "" {
		return state.Action
	}
	return actionPassthrough
}

// forwardToServer 用于处理与 forwardToServer 相关的逻辑。
func (s *ProxyServer) forwardToServer(incoming *http.Request, state *connectState) (*http.Response, error) {
	if incoming == nil {
		return nil, errors.New("nil request")
	}
	startedAt := time.Now()
	correlation := observability.NewTrace()
	correlation.HTTPRequestID = strings.TrimSpace(incoming.Header.Get("x-request-id"))
	if correlation.HTTPRequestID == "" {
		correlation.HTTPRequestID = correlation.TraceID
	}
	captureContext := observability.WithCorrelation(incoming.Context(), correlation)
	requestBytes := incoming.ContentLength
	if requestBytes < 0 {
		requestBytes = 0
	}
	requestPath := "/"
	if incoming.URL != nil && strings.TrimSpace(incoming.URL.Path) != "" {
		requestPath = incoming.URL.Path
	}
	redactedPath := redactObservabilityPath(requestPath)
	targetHost := incoming.Host
	if strings.TrimSpace(targetHost) == "" && incoming.URL != nil {
		targetHost = incoming.URL.Host
	}

	rawURL, err := rawURLForRelay(incoming)
	if err != nil {
		return nil, err
	}
	endpoint := s.currentBaseEndpoint()
	if endpoint == nil {
		return nil, errors.New("server endpoint is not configured")
	}
	forwardURL := *endpoint
	if incoming.URL != nil {
		forwardURL.Path = incoming.URL.Path
		forwardURL.RawPath = incoming.URL.RawPath
		forwardURL.RawQuery = incoming.URL.RawQuery
	}
	serverReq, err := http.NewRequestWithContext(incoming.Context(), incoming.Method, forwardURL.String(), incoming.Body)
	if err != nil {
		return nil, err
	}
	serverReq.ContentLength = incoming.ContentLength
	copyHeaders(serverReq.Header, incoming.Header)
	serverReq.Header.Set(HeaderServerUpstreamURL, rawURL)
	serverReq.Header.Set(HeaderTraceID, correlation.TraceID)
	serverReq.Header.Set(HeaderParentSpanID, correlation.SpanID)
	removeHopByHop(serverReq.Header)
	s.recordCapture(captureContext, observability.Event{
		Layer:               "mitm",
		Event:               "backend_forward_started",
		Capability:          "unknown",
		Operation:           "transport.forward",
		Direction:           observability.DirectionCursorToProxy,
		Route:               redactedPath,
		ExecutionTarget:     "backend",
		Protocol:            "http",
		Status:              "started",
		SemanticOutcome:     observability.OutcomeStarted,
		ImplementationState: observability.ImplementationImplemented,
		RequestBytes:        requestBytes,
		Fields: backendForwardFields(targetHost, incoming.Method, requestPath, actionBackendForward, state, map[string]any{
			"method":      incoming.Method,
			"target_host": hostOnly(targetHost),
		}),
	})

	resp, err := s.upstreamClient.Do(serverReq)
	if err != nil {
		if isTLSRelated(err, "") {
			s.recordCapture(captureContext, handshakeEvent(handshakeObservation{
				Source:  handshakeSourceUpstream,
				Host:    targetHost,
				Action:  actionBackendForward,
				Err:     err,
				ErrText: errorText(err),
			}, state))
		}
		s.recordCapture(captureContext, observability.Event{
			Layer:               "mitm",
			Event:               "backend_forward_finished",
			Capability:          "unknown",
			Operation:           "transport.forward",
			Direction:           observability.DirectionProxyInternal,
			Route:               redactedPath,
			ExecutionTarget:     "backend",
			Protocol:            "http",
			Status:              "error",
			SemanticOutcome:     observability.OutcomeFailed,
			ImplementationState: observability.ImplementationImplemented,
			ErrorCategory:       "backend_unavailable",
			DurationMS:          time.Since(startedAt).Milliseconds(),
			RequestBytes:        requestBytes,
			Fields: backendForwardFields(targetHost, incoming.Method, requestPath, actionBackendForward, state, map[string]any{
				"method":      incoming.Method,
				"target_host": hostOnly(targetHost),
			}),
		})
		return nil, fmt.Errorf("forward to backend server: %w", err)
	}
	responseBytes := resp.ContentLength
	if responseBytes < 0 {
		responseBytes = 0
	}
	s.recordCapture(captureContext, observability.Event{
		Layer:               "mitm",
		Event:               "backend_forward_finished",
		Capability:          "unknown",
		Operation:           "transport.forward",
		Direction:           observability.DirectionProxyInternal,
		Route:               redactedPath,
		ExecutionTarget:     "backend",
		Protocol:            "http",
		Status:              httpStatus(resp.StatusCode),
		SemanticOutcome:     forwardSemanticOutcome(resp.StatusCode),
		ImplementationState: observability.ImplementationImplemented,
		ErrorCategory:       httpErrorCategory(resp.StatusCode),
		DurationMS:          time.Since(startedAt).Milliseconds(),
		RequestBytes:        requestBytes,
		ResponseBytes:       responseBytes,
		Fields: backendForwardFields(targetHost, incoming.Method, requestPath, actionBackendForward, state, map[string]any{
			"status_code": resp.StatusCode,
			"method":      incoming.Method,
			"target_host": hostOnly(targetHost),
		}),
	})
	removeHopByHop(resp.Header)
	return resp, nil
}

func hostOnly(hostport string) string {
	host, _ := splitConnectHostPort(hostport)
	return host
}

func forwardSemanticOutcome(statusCode int) string {
	if statusCode >= http.StatusBadRequest {
		return observability.OutcomeFailed
	}
	return observability.OutcomeSucceeded
}

func (s *ProxyServer) recordCapture(ctx context.Context, event observability.Event) {
	if s == nil {
		return
	}
	recordMitmCapture(s.capture, ctx, event)
}

func httpStatus(statusCode int) string {
	if statusCode >= http.StatusBadRequest {
		return "error"
	}
	return "ok"
}

func httpErrorCategory(statusCode int) string {
	switch {
	case statusCode >= http.StatusInternalServerError:
		return "server_error"
	case statusCode >= http.StatusBadRequest:
		return "client_error"
	default:
		return ""
	}
}

// requestHost 用于处理与 requestHost 相关的逻辑。
func requestHost(ctx *goproxy.ProxyCtx) string {
	if ctx == nil || ctx.Req == nil {
		return ""
	}
	if strings.TrimSpace(ctx.Req.Host) != "" {
		return ctx.Req.Host
	}
	if ctx.Req.URL != nil {
		return ctx.Req.URL.Host
	}
	return ""
}

// requestRemoteAddr 用于处理与 requestRemoteAddr 相关的逻辑。
func requestRemoteAddr(ctx *goproxy.ProxyCtx) string {
	if ctx == nil || ctx.Req == nil {
		return "-"
	}
	remoteAddr := strings.TrimSpace(ctx.Req.RemoteAddr)
	if remoteAddr == "" {
		return "-"
	}
	return remoteAddr
}

// requestUserAgent 用于处理与 requestUserAgent 相关的逻辑。
func requestUserAgent(ctx *goproxy.ProxyCtx) string {
	if ctx == nil || ctx.Req == nil {
		return "-"
	}
	ua := strings.TrimSpace(ctx.Req.UserAgent())
	if ua == "" {
		return "-"
	}
	return ua
}

// requestURL 用于处理与 requestURL 相关的逻辑。
func requestURL(req *http.Request) string {
	if req == nil || req.URL == nil {
		return ""
	}
	u := req.URL.String()
	if strings.TrimSpace(u) == "" {
		return "/"
	}
	return u
}

// rawURLForRelay 用于处理与 rawURLForRelay 相关的逻辑。
func rawURLForRelay(r *http.Request) (string, error) {
	if r == nil {
		return "", errors.New("nil request")
	}
	if r.URL != nil && r.URL.IsAbs() {
		return r.URL.String(), nil
	}

	host := strings.TrimSpace(r.Host)
	if host == "" && r.URL != nil {
		host = strings.TrimSpace(r.URL.Host)
	}
	if host == "" {
		return "", errors.New("missing host")
	}

	scheme := "https"
	if r.TLS == nil {
		scheme = "http"
	}
	path := "/"
	if r.URL != nil {
		if strings.TrimSpace(r.URL.RequestURI()) != "" {
			path = r.URL.RequestURI()
		}
	}
	return scheme + "://" + host + path, nil
}

// copyHeaders 用于处理与 copyHeaders 相关的逻辑。
func copyHeaders(dst, src http.Header) {
	removeHopByHop(src)
	for k, vv := range src {
		for _, v := range vv {
			dst.Add(k, v)
		}
	}
}

// removeHopByHop 用于处理与 removeHopByHop 相关的逻辑。
func removeHopByHop(h http.Header) {
	if h == nil {
		return
	}
	connectionVals := h.Values("Connection")
	for _, line := range connectionVals {
		for _, token := range strings.Split(line, ",") {
			name := http.CanonicalHeaderKey(strings.TrimSpace(token))
			if name != "" {
				h.Del(name)
			}
		}
	}
	for k := range hopByHopHeaders {
		h.Del(k)
	}
}

// trimForLog 用于处理与 trimForLog 相关的逻辑。
func trimForLog(text string, limit int) string {
	text = strings.TrimSpace(text)
	if limit <= 0 {
		limit = 160
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit]) + "..."
}

// normalizeConnectHost 用于处理与 normalizeConnectHost 相关的逻辑。
func normalizeConnectHost(host string) string {
	host = strings.TrimSpace(host)
	if host == "" {
		return "-"
	}
	return host
}

// hostFromHTTPRequest 用于处理与 hostFromHTTPRequest 相关的逻辑。
func hostFromHTTPRequest(req *http.Request) string {
	if req == nil {
		return ""
	}
	if strings.TrimSpace(req.Host) != "" {
		return req.Host
	}
	if req.URL != nil {
		return req.URL.Host
	}
	return ""
}

// isWhitelistedRelayHost 用于处理与 isWhitelistedRelayHost 相关的逻辑。
func isWhitelistedRelayHost(host string) bool {
	host = strings.TrimSpace(strings.ToLower(host))
	if host == "" {
		return false
	}
	if strings.HasPrefix(host, "[") {
		host = strings.TrimPrefix(host, "[")
		host = strings.TrimSuffix(host, "]")
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = strings.ToLower(strings.TrimSpace(h))
	} else if strings.Count(host, ":") > 1 {
		// IPv6 without port
		host = strings.Trim(host, "[]")
	}
	if host == "api2.cursor.sh" || host == "api3.cursor.sh" {
		return true
	}
	if strings.HasSuffix(host, ".cursor.sh") {
		return true
	}
	return false
}

// mitmCertStore 缓存 goproxy 为站点动态签发的证书，避免同一 host 重复执行 RSA/x509 签发。
type mitmCertStore struct {
	mu    sync.Mutex
	certs map[string]*tls.Certificate
}

func newMITMCertStore() *mitmCertStore {
	return &mitmCertStore{certs: make(map[string]*tls.Certificate)}
}

func (store *mitmCertStore) Fetch(hostname string, gen func() (*tls.Certificate, error)) (*tls.Certificate, error) {
	hostname = strings.TrimSpace(strings.ToLower(hostname))
	if hostname == "" {
		return gen()
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	if cert, ok := store.certs[hostname]; ok {
		return cert, nil
	}
	cert, err := gen()
	if err != nil {
		return nil, err
	}
	store.certs[hostname] = cert
	return cert, nil
}

// shouldHandleLocalCORSPreflight 用于处理与 shouldHandleLocalCORSPreflight 相关的逻辑。
func shouldHandleLocalCORSPreflight(req *http.Request) bool {
	if req == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(req.Method), http.MethodOptions) {
		return false
	}
	if strings.TrimSpace(req.Header.Get("Origin")) == "" {
		return false
	}
	if strings.TrimSpace(req.Header.Get("Access-Control-Request-Method")) == "" {
		return false
	}
	return true
}

// buildLocalCORSPreflightResponse 用于处理与 buildLocalCORSPreflightResponse 相关的逻辑。
func buildLocalCORSPreflightResponse(req *http.Request) *http.Response {
	allowOrigin := "*"
	if req != nil {
		origin := strings.TrimSpace(req.Header.Get("Origin"))
		if origin != "" {
			allowOrigin = origin
		}
	}

	headers := make(http.Header)
	headers.Set("Access-Control-Allow-Origin", allowOrigin)
	headers.Set("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
	headers.Set("Access-Control-Allow-Headers", "*")
	headers.Set("Access-Control-Allow-Credentials", "true")
	headers.Set("Access-Control-Max-Age", "86400")
	if allowOrigin != "*" {
		headers.Set("Vary", "Origin")
	}

	body := io.NopCloser(strings.NewReader(""))
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Status:     "204 No Content",
		Header:     headers,
		Body:       body,
		Request:    req,
	}
}

// httpErrorFilterWriter 定义了当前模块中的 httpErrorFilterWriter 类型。
type httpErrorFilterWriter struct {
	capture captureRecorder
}

// Write 用于处理与 Write 相关的逻辑。
func (w *httpErrorFilterWriter) Write(p []byte) (int, error) {
	msg := strings.TrimSpace(string(p))
	if msg == "" {
		return len(p), nil
	}

	lower := strings.ToLower(msg)
	if strings.Contains(lower, "tls: first record does not look like a tls handshake") {
		logger.Infof("proxy ignore handshake mismatch: %s", msg)
		observeHTTPServerLog(captureFromWriter(w), msg)
		return len(p), nil
	}
	if strings.Contains(lower, "tls handshake error") {
		logger.Errorf("proxy tls handshake error: %s", msg)
		observeHTTPServerLog(captureFromWriter(w), msg)
		return len(p), nil
	}

	logger.Infof("proxy http server: %s", msg)
	return len(p), nil
}

func captureFromWriter(w *httpErrorFilterWriter) captureRecorder {
	if w == nil {
		return nil
	}
	return w.capture
}

// goproxyLogAdapter 定义了当前模块中的 goproxyLogAdapter 类型。
type goproxyLogAdapter struct {
	capture  captureRecorder
	sessions *connectSessionStore
}

// Printf 用于处理与 Printf 相关的逻辑。
func (l *goproxyLogAdapter) Printf(format string, args ...interface{}) {
	msg := strings.TrimSpace(fmt.Sprintf(format, args...))
	if msg == "" {
		return
	}
	lower := strings.ToLower(msg)
	if observation, ok := observeGoproxyLog(captureFromAdapter(l), sessionsFromAdapter(l), msg); ok {
		logSampledGoproxyHandshake(observation, msg)
		return
	}
	if strings.Contains(lower, "tls: first record does not look like a tls handshake") {
		return
	}
	if strings.Contains(lower, "broken pipe") || strings.Contains(lower, "connection reset by peer") {
		return
	}
	if suppressed, ok := proxyLogLimiter.ShouldLog("goproxy|" + goproxyMessageRateLimitKey(msg)); ok {
		logSuppressedProxyMessages("goproxy", suppressed)
		logger.Infof("goproxy: %s", msg)
	}
}

func captureFromAdapter(l *goproxyLogAdapter) captureRecorder {
	if l == nil {
		return nil
	}
	return l.capture
}

func sessionsFromAdapter(l *goproxyLogAdapter) *connectSessionStore {
	if l == nil {
		return nil
	}
	return l.sessions
}

func logSampledGoproxyHandshake(observation handshakeObservation, msg string) {
	decision := handshakeAppSampler.Observe(handshakeAppLogKey(observation))
	if !decision.ShouldLog {
		return
	}
	if !decision.Summary {
		logger.Infof("goproxy: %s", msg)
		return
	}
	classified := classifyHandshake(observation)
	logger.Infof(
		"goproxy tls_handshake_failed: host=%s category=%s direction=%s total=%d sampled=%d",
		firstNonEmpty(observation.Host, "-"),
		firstNonEmpty(classified.ErrorCategory, "-"),
		firstNonEmpty(classified.Direction, "-"),
		decision.Total,
		decision.Sampled,
	)
}

func errorRateLimitKey(err error) string {
	if err == nil {
		return ""
	}
	return normalizeRateLimitKey(err.Error())
}

func goproxyMessageRateLimitKey(msg string) string {
	return normalizeRateLimitKey(msg)
}

func normalizeRateLimitKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return ""
	}
	value = normalizeAddrPortsForLogKey(value)
	value = normalizeGoproxyRequestIDForLogKey(value)
	return value
}

func normalizeAddrPortsForLogKey(value string) string {
	parts := strings.Fields(value)
	for index, part := range parts {
		parts[index] = normalizeAddrPortToken(part)
	}
	return strings.Join(parts, " ")
}

func normalizeAddrPortToken(token string) string {
	trimmedRight := strings.TrimRight(token, ",;)]}")
	suffix := strings.TrimPrefix(token[len(trimmedRight):], "")
	host, port, err := net.SplitHostPort(trimmedRight)
	if err != nil || host == "" || port == "" {
		return token
	}
	return net.JoinHostPort(host, "*") + suffix
}

func normalizeGoproxyRequestIDForLogKey(value string) string {
	start := strings.Index(value, "[")
	end := strings.Index(value, "]")
	if start < 0 || end <= start {
		return value
	}
	prefix := strings.TrimSpace(value[:start])
	if prefix != "" {
		return value
	}
	return "[*]" + value[end+1:]
}

// logMITMCAInfo 用于处理与 logMITMCAInfo 相关的逻辑。
func logMITMCAInfo(caTLS *tls.Certificate) {
	if caTLS == nil || len(caTLS.Certificate) == 0 {
		logger.Errorf("MITM CA info unavailable: empty certificate chain")
		return
	}

	leaf, err := x509.ParseCertificate(caTLS.Certificate[0])
	if err != nil {
		logger.Errorf("MITM CA parse failed: %v", err)
		return
	}

	sum := sha256.Sum256(leaf.Raw)
	logger.Infof(
		"MITM enabled: sha256=%s subject=%s valid=%s~%s",
		strings.ToUpper(hex.EncodeToString(sum[:])),
		leaf.Subject.String(),
		leaf.NotBefore.Format(time.RFC3339),
		leaf.NotAfter.Format(time.RFC3339),
	)
}
