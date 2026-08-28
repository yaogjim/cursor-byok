package config

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	legacyruntime "cursor/internal/runtime"
)

const configHotReloadMinInterval = 500 * time.Millisecond

type Manager struct {
	store       *Store
	current     atomic.Pointer[Config]
	listenersMu sync.RWMutex
	listeners   []func(Config)
	writeMu     sync.Mutex
	reloadMu    sync.Mutex
	snapshot    fileSnapshot
	lastReload  time.Time
	reloadError string
}

func NewManager(ctx context.Context, store *Store) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("config store is required")
	}
	cfg, err := store.Load(ctx)
	if err != nil {
		return nil, err
	}
	manager := &Manager{
		store:    store,
		snapshot: store.snapshot(),
	}
	manager.setCurrent(cfg)
	return manager, nil
}

func (manager *Manager) Current() Config {
	if manager == nil {
		return DefaultConfig()
	}
	manager.reloadIfChanged(context.Background())
	return manager.currentConfig()
}

func (manager *Manager) currentConfig() Config {
	if manager == nil {
		return DefaultConfig()
	}
	if current := manager.current.Load(); current != nil {
		return *current
	}
	return DefaultConfig()
}

func (manager *Manager) Load(ctx context.Context) (Config, error) {
	if manager == nil {
		return DefaultConfig(), nil
	}
	manager.reloadIfChanged(ctx)
	return manager.currentConfig(), nil
}

func (manager *Manager) Save(ctx context.Context, cfg Config) (Config, error) {
	return manager.commitStoreWrite(true, func() (Config, bool, error) {
		saved, err := manager.store.Save(ctx, cfg)
		if err != nil {
			return Config{}, false, err
		}
		return saved, true, nil
	})
}

func (manager *Manager) SaveUserConfig(ctx context.Context, cfg Config) (Config, error) {
	return manager.commitStoreWrite(true, func() (Config, bool, error) {
		saved, err := manager.store.SaveUserConfig(ctx, cfg)
		if err != nil {
			return Config{}, false, err
		}
		return saved, true, nil
	})
}

func (manager *Manager) SaveGatewayConfig(ctx context.Context, gateway GatewayConfig) (Config, error) {
	return manager.commitStoreWrite(true, func() (Config, bool, error) {
		saved, err := manager.store.SaveGatewayConfig(ctx, gateway)
		return saved, err == nil, err
	})
}

func (manager *Manager) SaveModelAdapters(ctx context.Context, adapters []ModelAdapterConfig) (Config, error) {
	return manager.commitStoreWrite(true, func() (Config, bool, error) {
		saved, err := manager.store.SaveModelAdapters(ctx, adapters)
		return saved, err == nil, err
	})
}

func (manager *Manager) SaveCursorConfig(ctx context.Context, cfg Config) (Config, error) {
	return manager.commitStoreWrite(true, func() (Config, bool, error) {
		saved, err := manager.store.SaveCursorConfig(ctx, cfg)
		return saved, err == nil, err
	})
}

func (manager *Manager) SaveSystemSettings(ctx context.Context, cfg Config) (Config, error) {
	return manager.commitStoreWrite(true, func() (Config, bool, error) {
		saved, err := manager.store.SaveSystemSettings(ctx, cfg)
		return saved, err == nil, err
	})
}

func (manager *Manager) SaveHomeMetrics(ctx context.Context, cfg Config) (Config, error) {
	return manager.commitStoreWrite(true, func() (Config, bool, error) {
		saved, err := manager.store.SaveHomeMetrics(ctx, cfg)
		return saved, err == nil, err
	})
}

func (manager *Manager) LastAgentModelHash() string {
	if manager == nil {
		return ""
	}
	return strings.TrimSpace(manager.Current().LastAgentModelHash)
}

func (manager *Manager) SaveLastAgentModelHash(ctx context.Context, value string) error {
	_, err := manager.commitStoreWrite(false, func() (Config, bool, error) {
		return manager.store.SaveLastAgentModelHash(ctx, value)
	})
	return err
}

func (manager *Manager) SaveGatewayToken(ctx context.Context, value string) (Config, error) {
	return manager.commitStoreWrite(true, func() (Config, bool, error) {
		return manager.store.SaveGatewayToken(ctx, value)
	})
}

func (manager *Manager) commitStoreWrite(notify bool, write func() (Config, bool, error)) (Config, error) {
	if manager == nil || manager.store == nil {
		return Config{}, fmt.Errorf("config manager is not initialized")
	}
	manager.writeMu.Lock()
	cfg, changed, err := write()
	if err != nil {
		manager.writeMu.Unlock()
		return Config{}, err
	}
	manager.setCurrent(cfg)
	manager.reloadMu.Lock()
	manager.snapshot = manager.store.snapshot()
	manager.lastReload = time.Now()
	manager.reloadError = ""
	manager.reloadMu.Unlock()
	manager.writeMu.Unlock()
	if notify && changed {
		manager.notify(cfg)
	}
	return cfg, nil
}

func (manager *Manager) ProviderStreamIdleTimeout(ctx context.Context) time.Duration {
	if manager == nil {
		return time.Duration(DefaultProviderStreamIdleTimeoutSeconds) * time.Second
	}
	manager.reloadIfChanged(ctx)
	seconds := normalizeProviderStreamIdleTimeout(manager.currentConfig().ProviderStreamIdleTimeout)
	return time.Duration(seconds) * time.Second
}

func (manager *Manager) StreamContinuationSettings(ctx context.Context) (enabled bool, maxPerTurn int, deadline time.Duration, overlapChars int) {
	cfg := StreamContinuationConfig{}
	if manager != nil {
		manager.reloadIfChanged(ctx)
		cfg = manager.currentConfig().StreamContinuation
	}
	normalized := normalizeStreamContinuationConfig(cfg)
	maxPerTurn = normalized.MaxPerTurn
	if maxPerTurn <= 0 {
		maxPerTurn = DefaultStreamContinuationMaxPerTurn
	}
	if maxPerTurn > MaxStreamContinuationMaxPerTurn {
		maxPerTurn = MaxStreamContinuationMaxPerTurn
	}
	deadlineSeconds := normalized.TotalDeadlineSeconds
	if deadlineSeconds <= 0 {
		deadlineSeconds = DefaultStreamContinuationDeadlineSeconds
	}
	overlapChars = normalized.OverlapWindowChars
	if overlapChars <= 0 {
		overlapChars = DefaultStreamContinuationOverlapWindowChars
	}
	return normalized.Enabled, maxPerTurn, time.Duration(deadlineSeconds) * time.Second, overlapChars
}

func (manager *Manager) Observability(ctx context.Context) ObservabilityConfig {
	if manager == nil {
		return DefaultConfig().Observability
	}
	manager.reloadIfChanged(ctx)
	return normalizeObservabilityConfig(manager.currentConfig().Observability, nil)
}

func (manager *Manager) ObservabilityLogMode(ctx context.Context) string {
	return manager.Observability(ctx).Mode
}

func (manager *Manager) IsObservabilityLogEnabled(ctx context.Context) bool {
	return isFullObservabilityMode(manager.ObservabilityLogMode(ctx))
}

func (manager *Manager) Subscribe(listener func(Config)) func() {
	if manager == nil || listener == nil {
		return func() {}
	}
	manager.listenersMu.Lock()
	manager.listeners = append(manager.listeners, listener)
	index := len(manager.listeners) - 1
	manager.listenersMu.Unlock()
	return func() {
		manager.listenersMu.Lock()
		defer manager.listenersMu.Unlock()
		if index < 0 || index >= len(manager.listeners) {
			return
		}
		manager.listeners[index] = nil
	}
}

func (manager *Manager) LegacyRuntimeSnapshot(_ context.Context) (legacyruntime.RuntimeConfigSnapshot, error) {
	cfg := manager.Current()
	adapters := make([]legacyruntime.ModelAdapterConfig, 0, len(cfg.ModelAdapters))
	for _, item := range cfg.ModelAdapters {
		adapters = append(adapters, legacyruntime.ModelAdapterConfig{
			ID:                          item.ID,
			Sort:                        item.Sort,
			DisplayName:                 item.DisplayName,
			Type:                        item.Type,
			BaseURL:                     item.BaseURL,
			APIKey:                      item.APIKey,
			CredentialSource:            item.CredentialSource,
			TooltipData:                 item.TooltipData,
			ModelID:                     item.ModelID,
			ReasoningEffort:             item.ReasoningEffort,
			OpenAIEndpoint:              item.OpenAIEndpoint,
			OpenAIExtraParamsEnabled:    item.OpenAIExtraParamsEnabled,
			OpenAIExtraParamsJSON:       item.OpenAIExtraParamsJSON,
			CustomHeadersEnabled:        item.CustomHeadersEnabled,
			CustomHeadersJSON:           item.CustomHeadersJSON,
			AnthropicExtraParamsEnabled: item.AnthropicExtraParamsEnabled,
			AnthropicExtraParamsJSON:    item.AnthropicExtraParamsJSON,
			ContextWindowTokens:         item.ContextWindowTokens,
			MaxCompletionTokens:         item.MaxCompletionTokens,
			AnthropicMaxTokens:          item.AnthropicMaxTokens,
			AnthropicThinkingEffort:     item.AnthropicThinkingEffort,
			ThinkingBudgetTokens:        item.ThinkingBudgetTokens,
			MaxConcurrentRequests:       item.MaxConcurrentRequests,
		})
	}
	return legacyruntime.RuntimeConfigSnapshot{
		ObservabilityLogEnabled:   isFullObservabilityMode(cfg.Observability.Mode),
		ProviderStreamIdleTimeout: cfg.ProviderStreamIdleTimeout,
		ModelAdapters:             adapters,
	}, nil
}

func (manager *Manager) RouteMode(hasUpstreamURL bool) string {
	if !hasUpstreamURL {
		return DefaultRoutingMode
	}
	if manager == nil {
		return DefaultRoutingMode
	}
	mode := normalizeRoutingMode(manager.Current().Routing.Mode)
	if mode == "" {
		return DefaultRoutingMode
	}
	return mode
}

func (manager *Manager) setCurrent(cfg Config) {
	next := cfg
	manager.current.Store(&next)
}

func (manager *Manager) reloadIfChanged(ctx context.Context) {
	if manager == nil || manager.store == nil {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if !manager.writeMu.TryLock() {
		return
	}
	cfg, changed := manager.reloadIfChangedLocked(ctx)
	manager.writeMu.Unlock()
	if changed {
		manager.notify(cfg)
	}
}

func (manager *Manager) reloadIfChangedLocked(ctx context.Context) (Config, bool) {
	now := time.Now()
	manager.reloadMu.Lock()
	if !manager.lastReload.IsZero() && now.Sub(manager.lastReload) < configHotReloadMinInterval {
		manager.reloadMu.Unlock()
		return Config{}, false
	}
	manager.lastReload = now
	nextSnapshot := manager.store.snapshot()
	if nextSnapshot == manager.snapshot {
		manager.reloadMu.Unlock()
		return Config{}, false
	}
	cfg, err := manager.store.Load(ctx)
	if err != nil {
		errText := err.Error()
		if errText != manager.reloadError {
			log.Printf("config hot reload skipped path=%s error=%v", manager.store.Path(), err)
			manager.reloadError = errText
		}
		manager.reloadMu.Unlock()
		return Config{}, false
	}
	manager.snapshot = nextSnapshot
	manager.reloadError = ""
	manager.setCurrent(cfg)
	manager.reloadMu.Unlock()
	return cfg, true
}

func (manager *Manager) notify(cfg Config) {
	manager.listenersMu.RLock()
	listeners := append([]func(Config){}, manager.listeners...)
	manager.listenersMu.RUnlock()
	for _, listener := range listeners {
		if listener != nil {
			listener(cfg)
		}
	}
}
