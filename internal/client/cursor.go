package client

import (
	"fmt"
	goruntime "runtime"

	"cursor/internal/cursor"
	"cursor/internal/logger"
)

// ApplyCursorSettings 用于处理与 ApplyCursorSettings 相关的逻辑。
func (s *ProxyService) ApplyCursorSettings() error {
	if s == nil || s.proxy == nil {
		return fmt.Errorf("proxy is not initialized")
	}
	s.caFileMu.Lock()
	caCertPath, err := cursor.EnsureCACertFile(s.caCertPEM, s.caFilePath)
	if err == nil {
		s.caFilePath = caCertPath
	}
	s.caFileMu.Unlock()
	if err != nil {
		return fmt.Errorf("ensure ca cert file: %w", err)
	}
	if err := cursor.EnsureLegacySharedCACertRemoved(); err != nil {
		logger.Errorf("remove legacy shared ca cert failed, continuing with installation CA: %v", err)
	}

	switch goruntime.GOOS {
	case "windows":
		if err := cursor.EnsureCACertInstalled(s.caCertPEM, caCertPath); err != nil {
			return fmt.Errorf("install ca cert: %w", err)
		}
	case "darwin":
		if err := cursor.EnsureCACertInstalled(s.caCertPEM, caCertPath); err != nil {
			return fmt.Errorf("install ca cert: %w", err)
		}
	}

	if s.cursorSettingsStore == nil {
		return fmt.Errorf("Cursor settings store is not initialized")
	}
	if err := s.cursorSettingsStore.Apply(
		cursor.ProxyURLFromListenAddr(s.proxy.Snapshot().ListenAddr),
		s.cursorSettingsOwnerID,
	); err != nil {
		return err
	}
	if goruntime.GOOS == "darwin" {
		if err := cursor.SetSystemNodeExtraCACerts(caCertPath); err != nil {
			_, _ = s.cursorSettingsStore.ClearOwned(s.cursorSettingsOwnerID, nil)
			return fmt.Errorf("set node extra ca certs: %w", err)
		}
	}
	s.setCursorSettingsApplied(true)
	return nil
}

// ClearCursorSettings 仅清理当前实例成功注入且仍持有所有权的 Cursor 设置。
func (s *ProxyService) ClearCursorSettings() error {
	if s == nil || !s.ownsAppliedCursorSettings() {
		return nil
	}
	if s.cursorSettingsStore == nil {
		return fmt.Errorf("Cursor settings store is not initialized")
	}
	var beforeClear func() error
	if goruntime.GOOS == "darwin" {
		beforeClear = cursor.ClearSystemNodeExtraCACerts
	}
	cleared, err := s.cursorSettingsStore.ClearOwned(s.cursorSettingsOwnerID, beforeClear)
	if err != nil {
		return err
	}
	if !cleared {
		logger.Infof("clearCursorSettings skipped: ownership transferred to another instance")
		s.setCursorSettingsApplied(false)
		return nil
	}
	s.setCursorSettingsApplied(false)
	return nil
}

func (s *ProxyService) ownsAppliedCursorSettings() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.cursorSettingsApplied
}

// GetDeviceID 用于处理与 GetDeviceID 相关的逻辑。
func (s *ProxyService) GetDeviceID() (string, error) {
	return cursor.GetDeviceID()
}
