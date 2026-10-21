package netproxy

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"strings"
	"sync"
	"time"

	"cursor/internal/logger"

	"golang.org/x/net/http/httpproxy"
)

const (
	proxyCacheTTL     = time.Second
	alwaysNoProxyList = "localhost,127.0.0.1,::1"

	ProviderTransportProfileEnv             = "CURSOR_BYOK_PROVIDER_TRANSPORT_PROFILE"
	ProviderTransportProfileAuto            = "auto"
	ProviderTransportProfileHTTP1           = "http1"
	ProviderTransportProfileNoCompression   = "no_compression"
	ProviderTransportProfileFreshConnection = "fresh_connection"
	ProviderTransportProfileDirect          = "direct"
)

var (
	installOnce             sync.Once
	initialDefaultTransport = cloneDefaultTransport()
	defaultResolver         = &proxyResolver{}
	proxyTransports         sync.Map
)

// InstallDefaultTransport makes clients with a nil Transport use the same
// proxy resolution as clients created through this package.
func InstallDefaultTransport() {
	installOnce.Do(func() {
		http.DefaultTransport = NewTransport(nil)
		defaultResolver.logCurrentSnapshot("default transport installed")
	})
}

// NewHTTPClient creates an HTTP client that follows environment and OS proxy
// settings while preserving the caller's timeout choice.
func NewHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: NewTransport(nil),
		Timeout:   timeout,
	}
}

// NewProviderHTTPClient creates a Provider-only client with one optional,
// default-off transport experiment selected by CURSOR_BYOK_PROVIDER_TRANSPORT_PROFILE.
// The profile is read when the client is built, so changing it requires rebuilding
// the adapter/client (normally an application restart).
func NewProviderHTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{
		Transport: NewProviderTransport(nil, os.Getenv(ProviderTransportProfileEnv)),
		Timeout:   timeout,
	}
}

// NormalizeProviderTransportProfile returns a stable allowlisted profile.
// Unknown values fail closed to auto instead of silently combining experiments.
func NormalizeProviderTransportProfile(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case ProviderTransportProfileHTTP1:
		return ProviderTransportProfileHTTP1
	case ProviderTransportProfileNoCompression:
		return ProviderTransportProfileNoCompression
	case ProviderTransportProfileFreshConnection:
		return ProviderTransportProfileFreshConnection
	case ProviderTransportProfileDirect:
		return ProviderTransportProfileDirect
	default:
		return ProviderTransportProfileAuto
	}
}

// NewProviderTransport applies exactly one Provider transport experiment.
// direct 只绕过 env/OS 自动代理；启用的模型/全局自定义代理仍生效。OS TUN 仍可能拦截最终连接。
func NewProviderTransport(base *http.Transport, profile string) *http.Transport {
	transport := NewTransport(base)
	switch NormalizeProviderTransportProfile(profile) {
	case ProviderTransportProfileHTTP1:
		protocols := new(http.Protocols)
		protocols.SetHTTP1(true)
		transport.Protocols = protocols
		transport.ForceAttemptHTTP2 = false
		transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
		tlsConfig := &tls.Config{}
		if transport.TLSClientConfig != nil {
			tlsConfig = transport.TLSClientConfig.Clone()
		}
		nextProtos := make([]string, 0, len(tlsConfig.NextProtos))
		for _, protocol := range tlsConfig.NextProtos {
			if protocol != "h2" {
				nextProtos = append(nextProtos, protocol)
			}
		}
		tlsConfig.NextProtos = nextProtos
		transport.TLSClientConfig = tlsConfig
	case ProviderTransportProfileNoCompression:
		transport.DisableCompression = true
	case ProviderTransportProfileFreshConnection:
		transport.DisableKeepAlives = true
	case ProviderTransportProfileDirect:
		// direct 只绕过 env/OS 自动代理；启用的模型/全局自定义代理仍生效。
		transport.Proxy = ProxyForRequestCustomOnly
	}
	return transport
}

// NewTransport clones the given transport and installs proxy resolution on it.
// When base is nil, it clones Go's original default transport.
func NewTransport(base *http.Transport) *http.Transport {
	transport := initialDefaultTransport.Clone()
	if base != nil {
		transport = base.Clone()
	}
	transport.Proxy = ProxyForRequest
	proxyTransports.Store(transport, struct{}{})
	return transport
}

// ProxyForRequest resolves the proxy URL for a single request.
// 顺序：请求级自定义 > 全局自定义 > env/OS > 直连。显式自定义失败不回退。
func ProxyForRequest(req *http.Request) (*url.URL, error) {
	if req == nil || req.URL == nil {
		return nil, nil
	}
	if cfg, ok := RequestConfigFromContext(req.Context()); ok && cfg.Enabled {
		return proxyURLForCustom(req.URL, cfg)
	}
	return defaultResolver.proxyForURL(req.URL)
}

// ProxyForRequestCustomOnly 只应用请求级或全局自定义代理，忽略 env/OS 自动代理。
func ProxyForRequestCustomOnly(req *http.Request) (*url.URL, error) {
	if req == nil || req.URL == nil {
		return nil, nil
	}
	if cfg, ok := RequestConfigFromContext(req.Context()); ok && cfg.Enabled {
		return proxyURLForCustom(req.URL, cfg)
	}
	if global := GlobalConfig(); global.Enabled {
		return proxyURLForCustom(req.URL, global)
	}
	return nil, nil
}

// SetGlobal 用已保存的全局自定义代理更新解析器。非法启用配置不生效。
// 成功更新后关闭空闲连接，不中断在途请求。
func SetGlobal(cfg Config) {
	normalized, err := Normalize(cfg)
	if err != nil {
		return
	}
	defaultResolver.setGlobal(normalized)
}

// GlobalConfig 返回当前已应用的全局自定义代理（含关闭时保留的 URL）。
func GlobalConfig() Config {
	return defaultResolver.globalConfigCopy()
}

// CurrentStatus returns the latest proxy resolver snapshot without exposing
// proxy credentials.
func CurrentStatus() Status {
	return statusFromSnapshot(defaultResolver.currentSnapshot())
}

type proxyResolver struct {
	mu           sync.Mutex
	snapshot     proxySnapshot
	globalConfig Config
}

type proxySnapshot struct {
	expiresAt      time.Time
	source         string
	active         bool
	description    string
	key            string
	httpProxy      string
	httpsProxy     string
	proxyFunc      func(*url.URL) (*url.URL, error)
	systemBypass   []string
	excludeSimple  bool
	pacIgnored     bool
	loadErrMessage string
}

// Status is a sanitized summary of the proxy resolver's current decision.
type Status struct {
	Source           string `json:"source"`
	Active           bool   `json:"active"`
	UsingSystemProxy bool   `json:"usingSystemProxy"`
	UsingEnvProxy    bool   `json:"usingEnvProxy"`
	UsingCustomProxy bool   `json:"usingCustomProxy"`
	HTTPProxy        string `json:"httpProxy"`
	HTTPSProxy       string `json:"httpsProxy"`
	Description      string `json:"description"`
	PACIgnored       bool   `json:"pacIgnored"`
	LoadError        string `json:"loadError"`
}

type systemProxyConfig struct {
	Source         string
	HTTPProxy      string
	HTTPSProxy     string
	SOCKSProxy     string
	Bypass         []string
	ExcludeSimple  bool
	PACEnabled     bool
	PACURL         string
	LoadErrMessage string
}

func (resolver *proxyResolver) proxyForURL(reqURL *url.URL) (*url.URL, error) {
	snapshot := resolver.currentSnapshot()
	if snapshot.proxyFunc == nil {
		return nil, nil
	}
	if snapshot.source != "custom" && shouldBypassSystemProxy(reqURL, snapshot.systemBypass, snapshot.excludeSimple) {
		return nil, nil
	}
	return snapshot.proxyFunc(reqURL)
}

func (resolver *proxyResolver) currentSnapshot() proxySnapshot {
	now := time.Now()
	resolver.mu.Lock()
	defer resolver.mu.Unlock()

	if resolver.snapshotValidLocked(now) {
		return resolver.snapshot
	}
	next := resolver.buildSnapshotLocked(now)
	if next.key != resolver.snapshot.key {
		logProxySnapshot(next)
		closeIdleProxyConnections()
	}
	resolver.snapshot = next
	return next
}

func (resolver *proxyResolver) snapshotValidLocked(now time.Time) bool {
	if resolver.snapshot.expiresAt.IsZero() || !now.Before(resolver.snapshot.expiresAt) {
		return false
	}
	if resolver.globalConfig.Enabled {
		return resolver.snapshot.source == "custom"
	}
	return resolver.snapshot.source != "custom"
}

func (resolver *proxyResolver) buildSnapshotLocked(now time.Time) proxySnapshot {
	if resolver.globalConfig.Enabled {
		return snapshotFromCustom(now, resolver.globalConfig)
	}
	return buildProxySnapshot(now)
}

func (resolver *proxyResolver) setGlobal(cfg Config) {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	prevKey := resolver.snapshot.key
	resolver.globalConfig = cfg
	next := resolver.buildSnapshotLocked(time.Now())
	if next.key != prevKey {
		logProxySnapshot(next)
		closeIdleProxyConnections()
	}
	resolver.snapshot = next
}

func (resolver *proxyResolver) globalConfigCopy() Config {
	resolver.mu.Lock()
	defer resolver.mu.Unlock()
	return resolver.globalConfig
}

func (resolver *proxyResolver) logCurrentSnapshot(prefix string) {
	resolver.mu.Lock()
	next := resolver.buildSnapshotLocked(time.Now())
	if prefix != "" {
		next.description = strings.TrimSpace(prefix + "; " + next.description)
	}
	logProxySnapshot(next)
	resolver.snapshot = next
	closeIdleProxyConnections()
	resolver.mu.Unlock()
}

func snapshotFromCustom(now time.Time, cfg Config) proxySnapshot {
	parsed, parseErr := ParseURL(cfg.URL)
	httpDesc := sanitizeProxyValue(cfg.URL)
	proxyFunc := func(reqURL *url.URL) (*url.URL, error) {
		if isAlwaysDirectURL(reqURL) {
			return nil, nil
		}
		if parseErr != nil || parsed == nil {
			return nil, ErrInvalidURL
		}
		return parsed, nil
	}
	return proxySnapshot{
		expiresAt:   now.Add(24 * time.Hour),
		source:      "custom",
		active:      true,
		description: fmt.Sprintf("source=custom http=%s https=%s", displayProxyDesc(httpDesc), displayProxyDesc(httpDesc)),
		key:         strings.Join([]string{"custom", httpDesc}, "|"),
		httpProxy:   httpDesc,
		httpsProxy:  httpDesc,
		proxyFunc:   proxyFunc,
	}
}

func buildProxySnapshot(now time.Time) proxySnapshot {
	if cfg, ok := envProxyConfig(); ok {
		return snapshotFromConfig(now, "env", cfg, nil, false, false, "")
	}

	systemConfig := loadSystemProxyConfig()
	httpProxy := systemConfig.HTTPProxy
	httpsProxy := systemConfig.HTTPSProxy
	if systemConfig.SOCKSProxy != "" {
		if httpProxy == "" {
			httpProxy = systemConfig.SOCKSProxy
		}
		if httpsProxy == "" {
			httpsProxy = systemConfig.SOCKSProxy
		}
	}
	if httpProxy == "" && httpsProxy == "" {
		return proxySnapshot{
			expiresAt:      now.Add(proxyCacheTTL),
			source:         directSource(systemConfig),
			description:    directDescription(systemConfig),
			key:            directKey(systemConfig),
			pacIgnored:     systemConfig.PACEnabled,
			loadErrMessage: systemConfig.LoadErrMessage,
		}
	}

	cfg := httpproxy.Config{
		HTTPProxy:  httpProxy,
		HTTPSProxy: httpsProxy,
		NoProxy:    joinNoProxy(alwaysNoProxyList, envNoProxy(), strings.Join(systemConfig.Bypass, ",")),
	}
	return snapshotFromConfig(now, "system", cfg, systemConfig.Bypass, systemConfig.ExcludeSimple, systemConfig.PACEnabled, systemConfig.LoadErrMessage)
}

func snapshotFromConfig(now time.Time, source string, cfg httpproxy.Config, bypass []string, excludeSimple bool, pacIgnored bool, loadErr string) proxySnapshot {
	proxyFunc := cfg.ProxyFunc()
	httpDesc := sanitizeProxyValue(cfg.HTTPProxy)
	httpsDesc := sanitizeProxyValue(cfg.HTTPSProxy)
	active := httpDesc != "" || httpsDesc != ""
	description := fmt.Sprintf("source=%s http=%s https=%s", source, displayProxyDesc(httpDesc), displayProxyDesc(httpsDesc))
	if cfg.NoProxy != "" {
		description += " no_proxy=configured"
	}
	if excludeSimple {
		description += " exclude_simple=true"
	}
	if pacIgnored {
		description += " pac=ignored"
	}
	if loadErr != "" {
		description += " load_error=" + loadErr
	}
	return proxySnapshot{
		expiresAt:      now.Add(proxyCacheTTL),
		source:         source,
		active:         active,
		description:    description,
		key:            strings.Join([]string{source, httpDesc, httpsDesc, cfg.NoProxy, fmt.Sprint(excludeSimple), fmt.Sprint(pacIgnored), loadErr}, "|"),
		httpProxy:      httpDesc,
		httpsProxy:     httpsDesc,
		proxyFunc:      proxyFunc,
		systemBypass:   cleanBypassRules(bypass),
		excludeSimple:  excludeSimple,
		pacIgnored:     pacIgnored,
		loadErrMessage: loadErr,
	}
}

func envProxyConfig() (httpproxy.Config, bool) {
	allProxy := firstEnv("ALL_PROXY", "all_proxy")
	httpProxy := firstEnv("HTTP_PROXY", "http_proxy")
	httpsProxy := firstEnv("HTTPS_PROXY", "https_proxy")
	if httpProxy == "" {
		httpProxy = allProxy
	}
	if httpsProxy == "" {
		httpsProxy = allProxy
	}
	hasProxy := httpProxy != "" || httpsProxy != "" || allProxy != ""
	return httpproxy.Config{
		HTTPProxy:  httpProxy,
		HTTPSProxy: httpsProxy,
		NoProxy:    joinNoProxy(alwaysNoProxyList, envNoProxy()),
		CGI:        os.Getenv("REQUEST_METHOD") != "",
	}, hasProxy
}

func envNoProxy() string {
	return firstEnv("NO_PROXY", "no_proxy")
}

func firstEnv(names ...string) string {
	for _, name := range names {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			return value
		}
	}
	return ""
}

func directSource(cfg systemProxyConfig) string {
	if cfg.PACEnabled {
		return "system-pac-ignored"
	}
	if cfg.LoadErrMessage != "" {
		return "direct"
	}
	return "none"
}

func directDescription(cfg systemProxyConfig) string {
	parts := []string{"source=direct"}
	if cfg.PACEnabled {
		parts = append(parts, "pac=ignored")
	}
	if cfg.LoadErrMessage != "" {
		parts = append(parts, "load_error="+cfg.LoadErrMessage)
	}
	return strings.Join(parts, " ")
}

func directKey(cfg systemProxyConfig) string {
	return strings.Join([]string{"direct", fmt.Sprint(cfg.PACEnabled), cfg.PACURL, cfg.LoadErrMessage}, "|")
}

func logProxySnapshot(snapshot proxySnapshot) {
	if snapshot.description == "" {
		snapshot.description = "source=direct"
	}
	logger.Infof("net proxy: %s", snapshot.description)
}

func statusFromSnapshot(snapshot proxySnapshot) Status {
	source := strings.TrimSpace(snapshot.source)
	if source == "" || source == "none" || source == "system-pac-ignored" {
		source = "direct"
	}
	return Status{
		Source:           source,
		Active:           snapshot.active,
		UsingSystemProxy: snapshot.active && snapshot.source == "system",
		UsingEnvProxy:    snapshot.active && snapshot.source == "env",
		UsingCustomProxy: snapshot.active && snapshot.source == "custom",
		HTTPProxy:        snapshot.httpProxy,
		HTTPSProxy:       snapshot.httpsProxy,
		Description:      snapshot.description,
		PACIgnored:       snapshot.pacIgnored,
		LoadError:        snapshot.loadErrMessage,
	}
}

func closeIdleProxyConnections() {
	proxyTransports.Range(func(key any, _ any) bool {
		if transport, ok := key.(*http.Transport); ok && transport != nil {
			transport.CloseIdleConnections()
		}
		return true
	})
}

func cloneDefaultTransport() *http.Transport {
	if transport, ok := http.DefaultTransport.(*http.Transport); ok {
		clone := transport.Clone()
		clone.Proxy = nil
		return clone
	}
	return &http.Transport{
		DialContext:           (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}
}

func joinNoProxy(parts ...string) string {
	items := make([]string, 0, len(parts))
	for _, part := range parts {
		for _, value := range strings.FieldsFunc(part, func(r rune) bool {
			return r == ',' || r == ';'
		}) {
			value = strings.TrimSpace(value)
			if value == "" || strings.EqualFold(value, "<local>") {
				continue
			}
			items = append(items, value)
		}
	}
	return strings.Join(items, ",")
}

func cleanBypassRules(rules []string) []string {
	cleaned := make([]string, 0, len(rules))
	for _, rule := range rules {
		for _, value := range strings.FieldsFunc(rule, func(r rune) bool {
			return r == ',' || r == ';'
		}) {
			value = strings.ToLower(strings.TrimSpace(value))
			if value == "" {
				continue
			}
			cleaned = append(cleaned, value)
		}
	}
	return cleaned
}

func shouldBypassSystemProxy(reqURL *url.URL, rules []string, excludeSimple bool) bool {
	if reqURL == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(reqURL.Hostname()), "."))
	if host == "" {
		return false
	}
	if excludeSimple && isSimpleHostname(host) {
		return true
	}
	for _, rule := range rules {
		if ruleMatchesHost(rule, host) {
			return true
		}
	}
	return false
}

func isSimpleHostname(host string) bool {
	if host == "" || strings.Contains(host, ".") {
		return false
	}
	if ip := net.ParseIP(host); ip != nil {
		return false
	}
	return !strings.Contains(host, ":")
}

func ruleMatchesHost(rule string, host string) bool {
	rule = strings.TrimSpace(strings.TrimSuffix(rule, "."))
	if rule == "" {
		return false
	}
	if rule == "*" {
		return true
	}
	if strings.EqualFold(rule, "<local>") {
		return isSimpleHostname(host)
	}
	if strings.Contains(rule, "://") {
		if parsed, err := url.Parse(rule); err == nil {
			rule = strings.TrimSpace(parsed.Hostname())
		}
	}
	if strings.Contains(rule, ":") {
		if ruleHost, _, err := net.SplitHostPort(rule); err == nil {
			rule = ruleHost
		}
	}
	rule = strings.ToLower(strings.TrimSuffix(rule, "."))
	if strings.ContainsAny(rule, "*?") {
		matched, err := path.Match(rule, host)
		return err == nil && matched
	}
	if strings.HasPrefix(rule, ".") {
		return strings.HasSuffix(host, rule)
	}
	if strings.HasPrefix(rule, "*.") {
		return strings.HasSuffix(host, strings.TrimPrefix(rule, "*"))
	}
	return host == rule || strings.HasSuffix(host, "."+rule)
}

func sanitizeProxyValue(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		parsed, err = url.Parse("http://" + raw)
	}
	if err != nil || parsed == nil || parsed.Host == "" {
		return "invalid"
	}
	scheme := strings.TrimSpace(parsed.Scheme)
	if scheme == "" {
		scheme = "http"
	}
	return scheme + "://" + parsed.Host
}

func displayProxyDesc(value string) string {
	if value == "" {
		return "none"
	}
	return value
}

func normalizeProxyAddress(defaultScheme string, raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if strings.Contains(raw, "://") {
		return raw
	}
	if defaultScheme == "" {
		defaultScheme = "http"
	}
	return defaultScheme + "://" + raw
}
