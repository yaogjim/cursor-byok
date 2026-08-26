package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

// GatewayPublicModel 是外部客户端使用的稳定公开别名，只经显式映射解析。
type GatewayPublicModel struct {
	ID              string `json:"id" yaml:"id"`
	TargetAdapterID string `json:"targetAdapterID" yaml:"targetAdapterID"`
}

// GatewayConfig 是独立 Chat Gateway 的最小配置块。
// Token 只写入 YAML；普通 JSON/Wails 投影不得包含明文 token。
type GatewayConfig struct {
	Enabled         bool                 `json:"enabled" yaml:"enabled"`
	ListenAddr      string               `json:"listenAddr" yaml:"listenAddr"`
	Token           string               `json:"-" yaml:"token,omitempty"`
	TokenConfigured bool                 `json:"tokenConfigured" yaml:"-"`
	PublicModels    []GatewayPublicModel `json:"publicModels" yaml:"publicModels"`
}

func DefaultGatewayConfig() GatewayConfig {
	return GatewayConfig{
		Enabled:      false,
		ListenAddr:   DefaultGatewayListenAddr,
		PublicModels: []GatewayPublicModel{},
	}
}

func NormalizeGatewayConfig(input GatewayConfig) (GatewayConfig, error) {
	output := DefaultGatewayConfig()
	output.Enabled = input.Enabled
	listenAddr, err := normalizeGatewayListenAddr(input.ListenAddr)
	if err != nil {
		return GatewayConfig{}, err
	}
	output.ListenAddr = listenAddr
	output.Token = strings.TrimSpace(input.Token)
	output.TokenConfigured = output.Token != ""
	models, err := normalizeGatewayPublicModels(input.PublicModels)
	if err != nil {
		return GatewayConfig{}, err
	}
	output.PublicModels = models
	return output, nil
}

func normalizeGatewayListenAddr(value string) (string, error) {
	addr, err := normalizeListenAddr(value, DefaultGatewayListenAddr, "gateway.listenAddr")
	if err != nil {
		return "", err
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", errors.New("gateway.listenAddr 必须是 host:port 格式")
	}
	if !isLoopbackHost(host) {
		return "", errors.New("gateway.listenAddr 只允许 loopback 地址")
	}
	parsedPort, err := strconv.Atoi(port)
	if err != nil || parsedPort < 1 || parsedPort > 65535 {
		return "", errors.New("gateway.listenAddr port 必须在 1-65535 之间")
	}
	if isReservedCursorListenPort(parsedPort) {
		return "", fmt.Errorf("gateway.listenAddr 不能占用 Cursor 端口 %s/%s", DefaultProxyListenAddr, DefaultBackendListenAddr)
	}
	return net.JoinHostPort(host, strconv.Itoa(parsedPort)), nil
}

func isReservedCursorListenPort(port int) bool {
	for _, addr := range []string{DefaultProxyListenAddr, DefaultBackendListenAddr} {
		_, reservedPort, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		parsed, err := strconv.Atoi(reservedPort)
		if err == nil && parsed == port {
			return true
		}
	}
	return false
}

func isLoopbackHost(host string) bool {
	trimmed := strings.TrimSpace(host)
	if trimmed == "" {
		return false
	}
	if strings.EqualFold(trimmed, "localhost") {
		return true
	}
	ip := net.ParseIP(trimmed)
	return ip != nil && ip.IsLoopback()
}

func normalizeGatewayPublicModels(input []GatewayPublicModel) ([]GatewayPublicModel, error) {
	if len(input) == 0 {
		return []GatewayPublicModel{}, nil
	}
	if len(input) > MaxGatewayPublicModels {
		return nil, fmt.Errorf("gateway.publicModels 最多 %d 个", MaxGatewayPublicModels)
	}
	output := make([]GatewayPublicModel, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, item := range input {
		id := strings.TrimSpace(item.ID)
		target := strings.TrimSpace(item.TargetAdapterID)
		switch {
		case id == "":
			return nil, errors.New("gateway.publicModels.id 不能为空")
		case strings.ContainsAny(id, " \t\r\n"):
			return nil, errors.New("gateway.publicModels.id 不能包含空白")
		case len(id) > 128:
			return nil, errors.New("gateway.publicModels.id 不能超过 128 个字符")
		case target == "":
			return nil, errors.New("gateway.publicModels.targetAdapterID 不能为空")
		}
		if _, exists := seen[id]; exists {
			return nil, fmt.Errorf("gateway.publicModels.id %q 重复", id)
		}
		seen[id] = struct{}{}
		output = append(output, GatewayPublicModel{ID: id, TargetAdapterID: target})
	}
	return output, nil
}

func GenerateGatewayToken() (string, error) {
	buffer := make([]byte, GatewayTokenByteLength)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("生成 Gateway token 失败: %w", err)
	}
	return hex.EncodeToString(buffer), nil
}

func ensureGatewayToken(cfg Config) (Config, error) {
	if strings.TrimSpace(cfg.Gateway.Token) != "" {
		cfg.Gateway.TokenConfigured = true
		return cfg, nil
	}
	if !cfg.Gateway.Enabled {
		cfg.Gateway.TokenConfigured = false
		return cfg, nil
	}
	token, err := GenerateGatewayToken()
	if err != nil {
		return Config{}, err
	}
	cfg.Gateway.Token = token
	cfg.Gateway.TokenConfigured = true
	return cfg, nil
}

// OverlayGatewayToken copies the persisted token onto a user-submitted config.
// JSON/Wails payloads never carry the token; ordinary saves must keep it.
func OverlayGatewayToken(cfg Config, diskToken string) Config {
	cfg.Gateway.Token = strings.TrimSpace(diskToken)
	return cfg
}

// StripGatewayToken removes the token for default export and public copies.
func StripGatewayToken(cfg Config) Config {
	cfg.Gateway.Token = ""
	cfg.Gateway.TokenConfigured = false
	if cfg.Gateway.PublicModels == nil {
		cfg.Gateway.PublicModels = []GatewayPublicModel{}
	}
	return cfg
}

func knownAdapterIDs(adapters []ModelAdapterConfig) map[string]struct{} {
	ids := make(map[string]struct{}, len(adapters))
	for _, adapter := range adapters {
		id := strings.TrimSpace(adapter.ID)
		if id == "" {
			continue
		}
		ids[id] = struct{}{}
	}
	return ids
}

// ResolveGatewayPublicModel maps a public alias to a target adapter ID.
// It never falls back to provider modelID or an implicit 16-character hash.
func ResolveGatewayPublicModel(cfg Config, publicID string) (targetAdapterID string, stale bool, ok bool) {
	alias := strings.TrimSpace(publicID)
	if alias == "" {
		return "", false, false
	}
	known := knownAdapterIDs(cfg.ModelAdapters)
	for _, item := range cfg.Gateway.PublicModels {
		if item.ID != alias {
			continue
		}
		target := strings.TrimSpace(item.TargetAdapterID)
		if target == "" {
			return "", true, true
		}
		if _, exists := known[target]; !exists {
			return target, true, true
		}
		return target, false, true
	}
	return "", false, false
}

func PublicGatewayModels(cfg Config) []GatewayPublicModel {
	known := knownAdapterIDs(cfg.ModelAdapters)
	output := make([]GatewayPublicModel, 0, len(cfg.Gateway.PublicModels))
	for _, item := range cfg.Gateway.PublicModels {
		if _, exists := known[strings.TrimSpace(item.TargetAdapterID)]; !exists {
			continue
		}
		output = append(output, item)
	}
	return output
}
