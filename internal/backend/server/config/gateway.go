package config

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"unicode/utf8"
)

// GatewayPublicModel 是外部客户端使用的公开别名偏好。
// 缺映射表示随启用模型自动公开；Published=false 表示显式取消公开。
type GatewayPublicModel struct {
	ID              string `json:"id,omitempty" yaml:"id,omitempty"`
	TargetAdapterID string `json:"targetAdapterID" yaml:"targetAdapterID"`
	Published       *bool  `json:"published,omitempty" yaml:"published,omitempty"`
}

func GatewayPublicModelPublished(item GatewayPublicModel) bool {
	return optionalBoolOrTrue(item.Published)
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
	output := make([]GatewayPublicModel, 0, len(input))
	seen := make(map[string]struct{}, len(input))
	for _, item := range input {
		id := strings.TrimSpace(item.ID)
		target := strings.TrimSpace(item.TargetAdapterID)
		published := copyOptionalBool(item.Published)
		unpublished := !optionalBoolOrTrue(published)
		switch {
		case target == "":
			return nil, errors.New("gateway.publicModels.targetAdapterID 不能为空")
		case len(id) > MaxGatewayPublicModelIDLength:
			return nil, fmt.Errorf("gateway.publicModels.id 不能超过 %d 字节", MaxGatewayPublicModelIDLength)
		case id == "" && !unpublished:
			return nil, errors.New("gateway.publicModels.id 不能为空")
		}
		if id != "" {
			if _, exists := seen[id]; exists {
				return nil, fmt.Errorf("gateway.publicModels.id %q 重复", id)
			}
			seen[id] = struct{}{}
		}
		output = append(output, GatewayPublicModel{ID: id, TargetAdapterID: target, Published: published})
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

// RedactGatewayTokenForUI clears plaintext token for LoadUserConfig/events.
// TokenConfigured is preserved so the UI can still copy/rotate via dedicated APIs.
func RedactGatewayTokenForUI(cfg Config) Config {
	cfg.Gateway.Token = ""
	if cfg.Gateway.PublicModels == nil {
		cfg.Gateway.PublicModels = []GatewayPublicModel{}
	}
	return cfg
}

func validateGatewayPublicModelTargets(cfg Config) error {
	known := knownAdapterIDs(cfg.ModelAdapters)
	for _, item := range cfg.Gateway.PublicModels {
		target := strings.TrimSpace(item.TargetAdapterID)
		id := strings.TrimSpace(item.ID)
		label := id
		if label == "" {
			label = target
		}
		if target == "" {
			return fmt.Errorf("gateway.publicModels %q 未选择目标适配器", label)
		}
		if _, exists := known[target]; !exists {
			return fmt.Errorf("gateway.publicModels %q 指向的适配器将失效，请先更新网关公开模型映射", label)
		}
	}
	return nil
}

func pruneStaleGatewayPublicModels(cfg *Config) {
	if cfg == nil {
		return
	}
	known := knownAdapterIDs(cfg.ModelAdapters)
	kept := make([]GatewayPublicModel, 0, len(cfg.Gateway.PublicModels))
	for _, item := range cfg.Gateway.PublicModels {
		if _, exists := known[strings.TrimSpace(item.TargetAdapterID)]; !exists {
			continue
		}
		kept = append(kept, item)
	}
	cfg.Gateway.PublicModels = kept
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
	for _, item := range PublicGatewayModels(cfg) {
		if item.ID == alias {
			return item.TargetAdapterID, false, true
		}
	}
	known := knownAdapterIDs(cfg.ModelAdapters)
	for _, item := range cfg.Gateway.PublicModels {
		if strings.TrimSpace(item.ID) != alias {
			continue
		}
		if !GatewayPublicModelPublished(item) {
			return "", false, false
		}
		target := strings.TrimSpace(item.TargetAdapterID)
		if target == "" {
			return "", true, true
		}
		if _, exists := known[target]; !exists {
			return target, true, true
		}
		return "", false, false
	}
	return "", false, false
}

type plannedPublicModel struct {
	adapter ModelAdapterConfig
	custom  bool
	id      string
}

func PublicGatewayModels(cfg Config) []GatewayPublicModel {
	prefsByTarget := make(map[string][]GatewayPublicModel, len(cfg.Gateway.PublicModels))
	for _, item := range cfg.Gateway.PublicModels {
		target := strings.TrimSpace(item.TargetAdapterID)
		if target == "" {
			continue
		}
		prefsByTarget[target] = append(prefsByTarget[target], item)
	}

	planned := make([]plannedPublicModel, 0, len(cfg.ModelAdapters)+len(cfg.Gateway.PublicModels))
	for _, adapter := range cfg.ModelAdapters {
		if !ModelAdapterEnabled(adapter) {
			continue
		}
		adapterID := strings.TrimSpace(adapter.ID)
		if adapterID == "" {
			continue
		}
		prefs := prefsByTarget[adapterID]
		publishedCustoms := make([]GatewayPublicModel, 0, len(prefs))
		covered := false
		for _, pref := range prefs {
			covered = true
			if GatewayPublicModelPublished(pref) && strings.TrimSpace(pref.ID) != "" {
				publishedCustoms = append(publishedCustoms, pref)
			}
		}
		if len(publishedCustoms) > 0 {
			for _, pref := range publishedCustoms {
				planned = append(planned, plannedPublicModel{
					adapter: adapter,
					custom:  true,
					id:      strings.TrimSpace(pref.ID),
				})
			}
			continue
		}
		if covered {
			continue
		}
		planned = append(planned, plannedPublicModel{
			adapter: adapter,
			custom:  false,
			id:      normalizeDefaultPublicModelID(adapter.DisplayName),
		})
	}

	counts := make(map[string]int, len(planned))
	customNames := make(map[string]struct{}, len(planned))
	for _, item := range planned {
		if item.id == "" {
			continue
		}
		counts[item.id]++
		if item.custom {
			customNames[item.id] = struct{}{}
		}
	}

	needsSuffix := make([]bool, len(planned))
	used := make(map[string]struct{}, len(planned))
	for index, item := range planned {
		if item.id == "" {
			continue
		}
		if !item.custom && (counts[item.id] > 1 || nameInSet(item.id, customNames)) {
			needsSuffix[index] = true
			continue
		}
		used[item.id] = struct{}{}
	}

	output := make([]GatewayPublicModel, 0, len(planned))
	for index, item := range planned {
		id := item.id
		if id == "" {
			id = uniqueSuffixedPublicID(normalizeDefaultPublicModelID(item.adapter.DisplayName), item.adapter.ID, used)
		} else if needsSuffix[index] {
			id = uniqueSuffixedPublicID(item.id, item.adapter.ID, used)
		}
		if id == "" {
			continue
		}
		output = append(output, GatewayPublicModel{ID: id, TargetAdapterID: strings.TrimSpace(item.adapter.ID)})
	}
	return output
}

func nameInSet(name string, names map[string]struct{}) bool {
	_, exists := names[name]
	return exists
}

func normalizeDefaultPublicModelID(displayName string) string {
	return truncatePublicModelID(strings.TrimSpace(displayName))
}

func truncatePublicModelID(value string) string {
	return truncateUTF8Bytes(value, MaxGatewayPublicModelIDLength)
}

func truncateUTF8Bytes(value string, max int) string {
	if max <= 0 {
		return ""
	}
	if len(value) <= max {
		return value
	}
	truncated := value[:max]
	for len(truncated) > 0 && !utf8.ValidString(truncated) {
		truncated = truncated[:len(truncated)-1]
	}
	return truncated
}

func joinPublicModelID(base, suffix string) string {
	suffix = strings.TrimSpace(suffix)
	if suffix == "" {
		return truncatePublicModelID(base)
	}
	const sep = "-"
	room := MaxGatewayPublicModelIDLength - len(sep) - len(suffix)
	if room < 0 {
		return truncatePublicModelID(suffix)
	}
	return truncateUTF8Bytes(base, room) + sep + suffix
}

func uniqueSuffixedPublicID(base, adapterID string, used map[string]struct{}) string {
	id := strings.TrimSpace(adapterID)
	start := 4
	if len(id) > 0 && len(id) < start {
		start = len(id)
	}
	for width := start; width <= len(id); width++ {
		candidate := joinPublicModelID(base, id[:width])
		if candidate == "" {
			continue
		}
		if _, exists := used[candidate]; exists {
			continue
		}
		used[candidate] = struct{}{}
		return candidate
	}
	for n := 2; n < 1000; n++ {
		candidate := joinPublicModelID(base, fmt.Sprintf("%s-%d", id, n))
		if candidate == "" {
			continue
		}
		if _, exists := used[candidate]; exists {
			continue
		}
		used[candidate] = struct{}{}
		return candidate
	}
	candidate := joinPublicModelID(base, id)
	used[candidate] = struct{}{}
	return candidate
}
