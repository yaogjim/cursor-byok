package interaction

import (
	"bytes"
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/netip"
	neturl "net/url"
	"strings"
	"time"

	readability "codeberg.org/readeck/go-readability/v2"
	htmlmarkdown "github.com/firecrawl/html-to-markdown"
	mdplugin "github.com/firecrawl/html-to-markdown/plugin"

	"cursor/internal/backend/agent/toolresult"
	"cursor/internal/netproxy"
)

const (
	webFetchBodyLimit     = 2 * 1024 * 1024
	webFetchMarkdownLimit = toolresult.WebFetchMarkdownBytes
	webFetchTimeout       = 15 * time.Second
	webFetchMaxRedirects  = 10
)

// webFetchNetwork 保存 WebFetch 专用的超时、解析、拨号与代理解析依赖。
type webFetchNetwork struct {
	timeout         time.Duration
	lookupIPAddr    func(ctx context.Context, host string) ([]net.IPAddr, error)
	dialContext     func(ctx context.Context, network, address string) (net.Conn, error)
	proxyForRequest func(*http.Request) (*neturl.URL, error)
	tlsClientConfig *tls.Config
}

func (network webFetchNetwork) withDefaults() webFetchNetwork {
	if network.timeout <= 0 {
		network.timeout = webFetchTimeout
	}
	if network.lookupIPAddr == nil {
		network.lookupIPAddr = net.DefaultResolver.LookupIPAddr
	}
	if network.dialContext == nil {
		network.dialContext = (&net.Dialer{Timeout: 30 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	}
	if network.proxyForRequest == nil {
		network.proxyForRequest = netproxy.ProxyForRequest
	}
	return network
}

var blockedWebFetchNetworks = mustParseWebFetchNetworks(
	"0.0.0.0/8",
	"100.64.0.0/10",
	"192.0.0.0/24",
	"192.0.2.0/24",
	"198.18.0.0/15",
	"198.51.100.0/24",
	"203.0.113.0/24",
	"240.0.0.0/4",
	"100::/64",
	"2001:2::/48",
	"2001:db8::/32",
	"3fff::/20",
	"fec0::/10",
)

var ipv6WebFetchGlobalUnicast = mustParseWebFetchNetworks("2000::/3")[0]

func mustParseWebFetchNetworks(cidrs ...string) []*net.IPNet {
	networks := make([]*net.IPNet, 0, len(cidrs))
	for _, cidr := range cidrs {
		_, network, err := net.ParseCIDR(cidr)
		if err != nil {
			panic(err)
		}
		networks = append(networks, network)
	}
	return networks
}

func (bridge *Bridge) executeWebFetch(rawURL string) (string, error) {
	parsedURL, err := validateWebFetchURL(rawURL)
	if err != nil {
		return "", err
	}
	network := bridge.webFetch.withDefaults()
	client := newWebFetchHTTPClient(network)
	request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, parsedURL.String(), nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("User-Agent", "cursor-local-agent/1.0")
	request.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain,application/xml,application/json;q=0.9,*/*;q=0.1")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return "", fmt.Errorf("web fetch http status %d", response.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, webFetchBodyLimit+1))
	if err != nil {
		return "", err
	}
	if len(body) == 0 {
		return "", fmt.Errorf("web fetch returned empty body")
	}
	if len(body) > webFetchBodyLimit {
		body = body[:webFetchBodyLimit]
	}
	contentType := response.Header.Get("Content-Type")
	if contentType == "" {
		contentType = http.DetectContentType(body)
	}
	if !isWebFetchTextContentType(contentType) {
		return "", fmt.Errorf("web fetch unsupported content type %q", contentType)
	}
	markdown, title, err := renderWebFetchMarkdown(parsedURL, body, contentType)
	if err != nil {
		return "", err
	}
	markdown = strings.TrimSpace(markdown)
	if markdown == "" {
		return "", fmt.Errorf("web fetch returned empty markdown")
	}
	title = strings.TrimSpace(title)
	if title == "" {
		title = parsedURL.String()
	}
	payload := fmt.Sprintf("Title: %s\nURL: %s\n\nContent:\n%s", title, parsedURL.String(), markdown)
	return truncateWebFetchMarkdown(payload), nil
}

func newWebFetchHTTPClient(network webFetchNetwork) *http.Client {
	return &http.Client{
		Timeout:   network.timeout,
		Transport: &webFetchTransport{network: network},
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) >= webFetchMaxRedirects {
				return fmt.Errorf("web fetch stopped after 10 redirects")
			}
			if _, err := validateWebFetchURL(request.URL.String()); err != nil {
				return err
			}
			return nil
		},
	}
}

type webFetchTransport struct {
	network webFetchNetwork
}

func (transport *webFetchTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, fmt.Errorf("web fetch request url is required")
	}
	if _, err := validateWebFetchURL(request.URL.String()); err != nil {
		return nil, err
	}
	proxyURL, err := transport.network.proxyForRequest(request)
	if err != nil {
		return nil, err
	}
	inner := newWebFetchInnerTransport(transport.network, proxyURL)
	if proxyURL == nil {
		if err := pinWebFetchDirectDial(request, transport.network, inner); err != nil {
			return nil, err
		}
	}
	return inner.RoundTrip(request)
}

func newWebFetchInnerTransport(network webFetchNetwork, proxyURL *neturl.URL) *http.Transport {
	transport := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		DialContext:           network.dialContext,
		TLSClientConfig:       network.tlsClientConfig,
	}
	if proxyURL != nil {
		pinned := proxyURL
		transport.Proxy = func(*http.Request) (*neturl.URL, error) {
			return pinned, nil
		}
	}
	return transport
}

func pinWebFetchDirectDial(request *http.Request, network webFetchNetwork, transport *http.Transport) error {
	ips, err := resolvePublicWebFetchIPs(request.Context(), network.lookupIPAddr, request.URL.Hostname())
	if err != nil {
		return err
	}
	port := webFetchURLPort(request.URL)
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, networkName, address string) (net.Conn, error) {
		_, dialPort, err := net.SplitHostPort(address)
		if err != nil {
			dialPort = port
		}
		var firstErr error
		for _, ip := range ips {
			conn, err := network.dialContext(ctx, networkName, net.JoinHostPort(ip.String(), dialPort))
			if err == nil {
				return conn, nil
			}
			if firstErr == nil {
				firstErr = err
			}
		}
		if firstErr == nil {
			return nil, fmt.Errorf("web fetch no verified address to dial")
		}
		return nil, firstErr
	}
	return nil
}

func resolvePublicWebFetchIPs(ctx context.Context, lookup func(context.Context, string) ([]net.IPAddr, error), host string) ([]net.IP, error) {
	host = normalizeWebFetchHost(host)
	if ip := parseWebFetchIP(host); ip != nil {
		if !isPublicWebFetchIP(ip) {
			return nil, fmt.Errorf("web fetch host is not public-web accessible")
		}
		return []net.IP{equivalentWebFetchIP(ip)}, nil
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	addrs, err := lookup(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("web fetch dns lookup failed: %w", err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("web fetch host is not public-web accessible")
	}
	ips := make([]net.IP, 0, len(addrs))
	for _, addr := range addrs {
		ip := equivalentWebFetchIP(addr.IP)
		if !isPublicWebFetchIP(ip) {
			return nil, fmt.Errorf("web fetch host is not public-web accessible")
		}
		ips = append(ips, ip)
	}
	return ips, nil
}

func webFetchURLPort(pageURL *neturl.URL) string {
	if pageURL == nil {
		return "80"
	}
	if port := pageURL.Port(); port != "" {
		return port
	}
	if strings.EqualFold(pageURL.Scheme, "https") {
		return "443"
	}
	return "80"
}

func validateWebFetchURL(rawURL string) (*neturl.URL, error) {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return nil, fmt.Errorf("web fetch url is required")
	}
	parsedURL, err := neturl.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("web fetch invalid url: %w", err)
	}
	switch strings.ToLower(parsedURL.Scheme) {
	case "http", "https":
	default:
		return nil, fmt.Errorf("web fetch only supports http and https urls")
	}
	if parsedURL.User != nil {
		return nil, fmt.Errorf("web fetch url must not include credentials")
	}
	host := normalizeWebFetchHost(parsedURL.Hostname())
	if host == "" {
		return nil, fmt.Errorf("web fetch url host is required")
	}
	if isBlockedWebFetchHost(host) {
		return nil, fmt.Errorf("web fetch host is not public-web accessible")
	}
	return parsedURL, nil
}

func isBlockedWebFetchHost(host string) bool {
	host = normalizeWebFetchHost(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}
	ip := parseWebFetchIP(host)
	if ip == nil {
		return false
	}
	return !isPublicWebFetchIP(ip)
}

func normalizeWebFetchHost(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	host = strings.Trim(host, "[]")
	return strings.TrimRight(host, ".")
}

func parseWebFetchIP(host string) net.IP {
	host = normalizeWebFetchHost(host)
	if host == "" {
		return nil
	}
	addr, err := netip.ParseAddr(host)
	if err != nil {
		return nil
	}
	return net.IP(addr.Unmap().AsSlice())
}

func isPublicWebFetchIP(ip net.IP) bool {
	ip = equivalentWebFetchIP(ip)
	if ip == nil {
		return false
	}
	if embedded := embeddedIPv4FromWebFetchIP(ip); embedded != nil {
		return isPublicWebFetchIP(embedded)
	}
	if ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsMulticast() ||
		ip.IsUnspecified() {
		return false
	}
	for _, network := range blockedWebFetchNetworks {
		if network.Contains(ip) {
			return false
		}
	}
	if ip.To4() == nil && !ipv6WebFetchGlobalUnicast.Contains(ip) {
		return false
	}
	return true
}

func equivalentWebFetchIP(ip net.IP) net.IP {
	if ip == nil {
		return nil
	}
	if ip4 := ip.To4(); ip4 != nil {
		return ip4
	}
	if ip16 := ip.To16(); ip16 != nil {
		return ip16
	}
	return nil
}

func embeddedIPv4FromWebFetchIP(ip net.IP) net.IP {
	if ip == nil || ip.To4() != nil {
		return nil
	}
	ip16 := ip.To16()
	if ip16 == nil {
		return nil
	}
	if ip16[0] == 0x20 && ip16[1] == 0x02 {
		return net.IPv4(ip16[2], ip16[3], ip16[4], ip16[5]).To4()
	}
	if ip16[0] == 0x00 && ip16[1] == 0x64 && ip16[2] == 0xff && ip16[3] == 0x9b &&
		ip16[4] == 0 && ip16[5] == 0 && ip16[6] == 0 && ip16[7] == 0 &&
		ip16[8] == 0 && ip16[9] == 0 && ip16[10] == 0 && ip16[11] == 0 {
		return net.IPv4(ip16[12], ip16[13], ip16[14], ip16[15]).To4()
	}
	return nil
}

func isWebFetchTextContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	}
	if strings.HasPrefix(mediaType, "text/") {
		return true
	}
	switch mediaType {
	case "application/xhtml+xml", "application/xml", "application/json", "application/ld+json", "application/rss+xml", "application/atom+xml":
		return true
	default:
		return strings.HasSuffix(mediaType, "+xml") || strings.HasSuffix(mediaType, "+json")
	}
}

func renderWebFetchMarkdown(pageURL *neturl.URL, body []byte, contentType string) (string, string, error) {
	if !isHTMLLikeContentType(contentType) {
		return string(body), "", nil
	}
	article, err := readability.FromReader(bytes.NewReader(body), pageURL)
	if err == nil {
		var articleHTML bytes.Buffer
		if renderErr := article.RenderHTML(&articleHTML); renderErr == nil && strings.TrimSpace(articleHTML.String()) != "" {
			if markdown, convertErr := convertHTMLToMarkdown(pageURL, articleHTML.String()); convertErr == nil && strings.TrimSpace(markdown) != "" {
				return markdown, article.Title(), nil
			}
		}
	}
	markdown, err := convertHTMLToMarkdown(pageURL, string(body))
	if err != nil {
		return "", "", fmt.Errorf("web fetch markdown conversion failed: %w", err)
	}
	return markdown, extractWebFetchHTMLTitle(string(body)), nil
}

func isHTMLLikeContentType(contentType string) bool {
	mediaType, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		mediaType = strings.ToLower(strings.TrimSpace(strings.Split(contentType, ";")[0]))
	}
	return mediaType == "text/html" || mediaType == "application/xhtml+xml" || mediaType == ""
}

func convertHTMLToMarkdown(pageURL *neturl.URL, htmlBody string) (string, error) {
	converter := htmlmarkdown.NewConverter(htmlmarkdown.DomainFromURL(pageURL.String()), true, nil)
	converter.Use(mdplugin.GitHubFlavored())
	return converter.ConvertString(htmlBody)
}

func extractWebFetchHTMLTitle(htmlBody string) string {
	matches := htmlTitlePattern.FindStringSubmatch(htmlBody)
	if len(matches) < 2 {
		return ""
	}
	return cleanupWebSearchHTML(matches[1])
}

func truncateWebFetchMarkdown(markdown string) string {
	limit, ok := toolresult.PayloadBytes("WebFetch", toolresult.PurposeDisplay)
	if !ok {
		limit = toolresult.WebFetchMarkdownBytes
	}
	return toolresult.TruncateTail("WebFetch", markdown, limit)
}
