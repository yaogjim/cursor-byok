package netproxy

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// ErrInvalidURL 表示启用的自定义代理 URL 非法。错误文本不得包含原始 URL 或凭据。
var ErrInvalidURL = errors.New("outboundProxy.url 仅支持 http、https 或 socks5，且必须包含有效主机")

type requestConfigKey struct{}

// Config 是全局或模型级自定义出站代理。关闭时保留 URL 但不使用。
type Config struct {
	Enabled bool   `json:"enabled" yaml:"enabled"`
	URL     string `json:"url" yaml:"url"`
}

// Normalize 修剪 URL；仅在 Enabled 时校验协议与主机。关闭时原样保留 URL。
func Normalize(cfg Config) (Config, error) {
	out := Config{
		Enabled: cfg.Enabled,
		URL:     strings.TrimSpace(cfg.URL),
	}
	if !out.Enabled {
		return out, nil
	}
	if _, err := ParseURL(out.URL); err != nil {
		return Config{}, err
	}
	return out, nil
}

// ParseURL 校验自定义代理 URL：http/https/socks5 且主机非空。不做网络探测。
func ParseURL(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, ErrInvalidURL
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed == nil {
		return nil, ErrInvalidURL
	}
	switch strings.ToLower(strings.TrimSpace(parsed.Scheme)) {
	case "http", "https", "socks5":
	default:
		return nil, ErrInvalidURL
	}
	if strings.TrimSpace(parsed.Hostname()) == "" {
		return nil, ErrInvalidURL
	}
	return parsed, nil
}

// Effective 解析模型自定义 > 已保存全局自定义 > 关闭。关闭时 URL 为空，供 hash 使用。
func Effective(request, global Config) Config {
	if request.Enabled {
		return Config{Enabled: true, URL: strings.TrimSpace(request.URL)}
	}
	if global.Enabled {
		return Config{Enabled: true, URL: strings.TrimSpace(global.URL)}
	}
	return Config{}
}

// WithRequestConfig 把请求级自定义代理附着到 context。Enabled=false 表示继承，不强制直连。
func WithRequestConfig(ctx context.Context, cfg Config) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, requestConfigKey{}, cfg)
}

// RequestConfigFromContext 读取请求级自定义代理。
func RequestConfigFromContext(ctx context.Context) (Config, bool) {
	if ctx == nil {
		return Config{}, false
	}
	cfg, ok := ctx.Value(requestConfigKey{}).(Config)
	return cfg, ok
}

// AttachRequest 把请求级自定义代理写入 HTTP 请求 context。
func AttachRequest(req *http.Request, cfg Config) *http.Request {
	if req == nil {
		return nil
	}
	return req.WithContext(WithRequestConfig(req.Context(), cfg))
}

func isAlwaysDirectURL(reqURL *url.URL) bool {
	if reqURL == nil {
		return false
	}
	host := strings.ToLower(strings.TrimSuffix(strings.TrimSpace(reqURL.Hostname()), "."))
	if host == "" {
		return false
	}
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func proxyURLForCustom(target *url.URL, cfg Config) (*url.URL, error) {
	if isAlwaysDirectURL(target) {
		return nil, nil
	}
	parsed, err := ParseURL(cfg.URL)
	if err != nil {
		return nil, err
	}
	return parsed, nil
}
