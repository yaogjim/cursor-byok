package config

import (
	"context"
	"testing"
)

func TestDefaultConfigKeepsRunningAfterWindowClose(t *testing.T) {
	cfg := DefaultConfig()
	if cfg.Desktop.ExitOnWindowClose {
		t.Fatal("默认应在关闭窗口后继续运行")
	}
	if cfg.Desktop.ShowDockIcon {
		t.Fatal("默认不应显示 Dock 图标")
	}
	if cfg.Desktop.HideWindowOnStartup {
		t.Fatal("默认不应在启动时隐藏主窗口")
	}
}

func TestNormalizeConfigPreservesDesktopExitOnWindowClose(t *testing.T) {
	input := DefaultConfig()
	input.Desktop.ExitOnWindowClose = true
	input.Desktop.ShowDockIcon = true
	input.Desktop.HideWindowOnStartup = true

	got, err := NormalizeConfig(input)
	if err != nil {
		t.Fatalf("NormalizeConfig() error = %v", err)
	}
	if !got.Desktop.ExitOnWindowClose {
		t.Fatal("显式退出设置被归一化丢弃")
	}
	if !got.Desktop.ShowDockIcon {
		t.Fatal("Dock 显示设置被归一化丢弃")
	}
	if !got.Desktop.HideWindowOnStartup {
		t.Fatal("启动隐藏设置被归一化丢弃")
	}

	legacy := Config{}
	normalizedLegacy, err := NormalizeConfig(legacy)
	if err != nil {
		t.Fatalf("NormalizeConfig(legacy) error = %v", err)
	}
	if normalizedLegacy.Desktop.ExitOnWindowClose {
		t.Fatal("缺失 desktop 段的旧配置应保持关闭窗口后继续运行")
	}
	if normalizedLegacy.Desktop.ShowDockIcon {
		t.Fatal("缺失 desktop 段的旧配置不应显示 Dock 图标")
	}
	if normalizedLegacy.Desktop.HideWindowOnStartup {
		t.Fatal("缺失 desktop 段的旧配置不应在启动时隐藏窗口")
	}
}

func TestSaveSystemSettingsPersistsDesktopExitOnWindowClose(t *testing.T) {
	store := newWriteTestStore(t)
	seed := seedWriteTestConfig(t, store, nil)

	exit := seed
	exit.Desktop.ExitOnWindowClose = true
	exit.Desktop.ShowDockIcon = true
	exit.Desktop.HideWindowOnStartup = true
	saved, err := store.SaveSystemSettings(context.Background(), exit)
	if err != nil {
		t.Fatalf("SaveSystemSettings() error = %v", err)
	}
	if !saved.Desktop.ExitOnWindowClose || !saved.Desktop.ShowDockIcon || !saved.Desktop.HideWindowOnStartup {
		t.Fatal("系统设置保存后未保留桌面设置")
	}

	loaded, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if !loaded.Desktop.ExitOnWindowClose || !loaded.Desktop.ShowDockIcon || !loaded.Desktop.HideWindowOnStartup {
		t.Fatal("桌面设置未持久化")
	}

	keep := loaded
	keep.Desktop.ExitOnWindowClose = false
	keep.Desktop.ShowDockIcon = false
	keep.Desktop.HideWindowOnStartup = false
	reverted, err := store.SaveSystemSettings(context.Background(), keep)
	if err != nil {
		t.Fatalf("SaveSystemSettings(revert) error = %v", err)
	}
	if reverted.Desktop.ExitOnWindowClose || reverted.Desktop.ShowDockIcon || reverted.Desktop.HideWindowOnStartup {
		t.Fatal("关闭桌面设置后仍保留为 true")
	}
}
