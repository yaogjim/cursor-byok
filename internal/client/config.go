package client

import (
	"context"

	"cursor/internal/appdata"
	serverconfig "cursor/internal/backend/server/config"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// UserConfig 定义了当前模块中的 UserConfig 类型。
type UserConfig = serverconfig.Config

// LoadUserConfig 用于处理与 LoadUserConfig 相关的逻辑。
func (s *ProxyService) LoadUserConfig() (UserConfig, error) {
	if s == nil {
		return serverconfig.DefaultConfig(), nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.loadUserConfig()
}

func (s *ProxyService) loadUserConfig() (UserConfig, error) {
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	if s.backendHost != nil {
		return s.backendHost.LoadConfig(ctx)
	}
	if s.store == nil {
		return serverconfig.DefaultConfig(), nil
	}
	return s.store.Load(ctx)
}

// SaveUserConfig 用于处理与 SaveUserConfig 相关的逻辑。
// SaveGatewayConfig 只保存 Gateway 配置块，保留其他页面和 token 字段。
func (s *ProxyService) SaveGatewayConfig(cfg UserConfig) error {
	if s == nil {
		return nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	var (
		normalized UserConfig
		err        error
	)
	if s.backendHost != nil {
		normalized, err = s.backendHost.ConfigManager().SaveGatewayConfig(ctx, cfg.Gateway)
	} else if s.store != nil {
		normalized, err = s.store.SaveGatewayConfig(ctx, cfg.Gateway)
	} else {
		return nil
	}
	if err != nil {
		return err
	}
	s.emitUserConfigChanged(normalized)
	s.reconcileGatewayIfServiceRunning(normalized)
	return nil
}

// SaveModelAdapters 只保存模型适配器配置块。
func (s *ProxyService) SaveModelAdapters(cfg UserConfig) error {
	if s == nil {
		return nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	var (
		normalized UserConfig
		err        error
	)
	if s.backendHost != nil {
		normalized, err = s.backendHost.ConfigManager().SaveModelAdapters(ctx, cfg.ModelAdapters)
	} else if s.store != nil {
		normalized, err = s.store.SaveModelAdapters(ctx, cfg.ModelAdapters)
	} else {
		return nil
	}
	if err != nil {
		return err
	}
	s.emitUserConfigChanged(normalized)
	return nil
}

// SaveCursorConfig 只保存 Cursor 集成配置块。
func (s *ProxyService) SaveCursorConfig(cfg UserConfig) error {
	if s == nil {
		return nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	var (
		normalized UserConfig
		err        error
	)
	if s.backendHost != nil {
		normalized, err = s.backendHost.ConfigManager().SaveCursorConfig(ctx, cfg)
	} else if s.store != nil {
		normalized, err = s.store.SaveCursorConfig(ctx, cfg)
	} else {
		return nil
	}
	if err != nil {
		return err
	}
	s.emitUserConfigChanged(normalized)
	return nil
}

// SaveSystemSettings 只保存系统设置配置块。
func (s *ProxyService) SaveSystemSettings(cfg UserConfig) error {
	if s == nil {
		return nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	var (
		normalized UserConfig
		err        error
	)
	if s.backendHost != nil {
		normalized, err = s.backendHost.ConfigManager().SaveSystemSettings(ctx, cfg)
	} else if s.store != nil {
		normalized, err = s.store.SaveSystemSettings(ctx, cfg)
	} else {
		return nil
	}
	if err != nil {
		return err
	}
	s.emitUserConfigChanged(normalized)
	return nil
}

// SaveHomeMetrics 只保存首页缓存命中率口径，避免覆盖其他页面草稿。
func (s *ProxyService) SaveHomeMetrics(cfg UserConfig) error {
	if s == nil {
		return nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	var (
		normalized UserConfig
		err        error
	)
	if s.backendHost != nil {
		normalized, err = s.backendHost.ConfigManager().SaveHomeMetrics(ctx, cfg)
	} else if s.store != nil {
		normalized, err = s.store.SaveHomeMetrics(ctx, cfg)
	} else {
		return nil
	}
	if err != nil {
		return err
	}
	s.emitUserConfigChanged(normalized)
	return nil
}

func (s *ProxyService) SaveUserConfig(cfg UserConfig) error {
	if s == nil {
		return nil
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	return s.saveUserConfig(cfg)
}

func (s *ProxyService) saveUserConfig(cfg UserConfig) error {
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	var (
		normalized UserConfig
		err        error
	)
	if s.backendHost != nil {
		normalized, err = s.backendHost.SaveConfig(ctx, cfg)
	} else if s.store != nil {
		normalized, err = s.store.SaveUserConfig(ctx, cfg)
	} else {
		return nil
	}
	if err != nil {
		return err
	}
	s.emitUserConfigChanged(normalized)
	s.reconcileGatewayIfServiceRunning(normalized)
	return nil
}

func (s *ProxyService) replaceUserConfig(cfg UserConfig) error {
	app := application.Get()
	ctx := context.Background()
	if app != nil {
		ctx = app.Context()
	}
	var (
		normalized UserConfig
		err        error
	)
	if s.backendHost != nil {
		normalized, err = s.backendHost.ReplaceConfig(ctx, cfg)
	} else if s.store != nil {
		normalized, err = s.store.Save(ctx, cfg)
	} else {
		return nil
	}
	if err != nil {
		return err
	}
	s.emitUserConfigChanged(normalized)
	s.reconcileGatewayIfServiceRunning(normalized)
	return nil
}

func (s *ProxyService) reconcileGatewayIfServiceRunning(cfg UserConfig) {
	if s == nil || s.backendHost == nil {
		return
	}
	// 独立 Gateway 运行时不依赖 Cursor backend 已监听。配置关闭必须能停止
	// 已独立运行的 Gateway；启用配置仍由显式 StartGateway 或 Cursor 启动触发。
	if !cfg.Gateway.Enabled {
		s.reconcileGateway(cfg)
		return
	}
	if s.backendHost.IsRunning() {
		s.reconcileGateway(cfg)
	}
}

func (s *ProxyService) emitUserConfigChanged(cfg UserConfig) {
	app := application.Get()
	if app == nil {
		return
	}
	app.Event.Emit("user-config:changed", serverconfig.RedactGatewayTokenForUI(cfg))
}

// resolveUserConfigPath 用于处理与 resolveUserConfigPath 相关的逻辑。
func resolveUserConfigPath() string {
	return appdata.ConfigFilePath()
}

// resolveLogsRootPath 用于处理与 resolveLogsRootPath 相关的逻辑。
func resolveLogsRootPath() string {
	return appdata.LogsRootPath()
}

// ResolveLogsRootPath 用于处理与 ResolveLogsRootPath 相关的逻辑。
func ResolveLogsRootPath() string {
	return resolveLogsRootPath()
}

// ResolveSettingsRootPath 用于处理与 ResolveSettingsRootPath 相关的逻辑。
func ResolveSettingsRootPath() string {
	return appdata.RootDir()
}
