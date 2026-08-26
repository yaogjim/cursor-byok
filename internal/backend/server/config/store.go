package config

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Store struct {
	path     string
	logsRoot string
	mu       sync.Mutex
}

type fileSnapshot struct {
	exists  bool
	modTime int64
	size    int64
}

func NewStore(path string, logsRoot string) *Store {
	return &Store{
		path:     strings.TrimSpace(path),
		logsRoot: strings.TrimSpace(logsRoot),
	}
}

func (store *Store) Path() string {
	if store == nil {
		return ""
	}
	return store.path
}

func (store *Store) LogsRoot() string {
	if store == nil {
		return ""
	}
	return store.logsRoot
}

func (store *Store) snapshot() fileSnapshot {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return fileSnapshot{}
	}
	info, err := os.Stat(store.path)
	if err != nil {
		return fileSnapshot{}
	}
	return fileSnapshot{
		exists:  true,
		modTime: info.ModTime().UnixNano(),
		size:    info.Size(),
	}
}

func (store *Store) Load(_ context.Context) (Config, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return DefaultConfig(), nil
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	data, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			defaultConfig := DefaultConfig()
			if err := store.saveLocked(defaultConfig); err != nil {
				return DefaultConfig(), err
			}
			return defaultConfig, nil
		}
		return DefaultConfig(), fmt.Errorf("读取用户配置失败: %w", err)
	}

	var current Config
	if err := yaml.Unmarshal(data, &current); err != nil {
		return DefaultConfig(), fmt.Errorf("解析用户配置失败: %w", err)
	}
	normalized, err := NormalizeConfig(current)
	if err != nil {
		return DefaultConfig(), err
	}
	if shouldPersistNormalizedConfig(data, current, normalized) {
		if err := store.saveLocked(normalized); err != nil {
			return DefaultConfig(), err
		}
	}
	return normalized, nil
}

func (store *Store) Save(_ context.Context, cfg Config) (Config, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return Config{}, errors.New("配置存储未初始化")
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	_, _ = store.readLatestLocked()
	normalized, err := NormalizeConfig(cfg)
	if err != nil {
		return Config{}, err
	}
	normalized, err = ensureGatewayToken(normalized)
	if err != nil {
		return Config{}, err
	}
	if err := store.saveLocked(normalized); err != nil {
		return Config{}, err
	}
	return normalized, nil
}

func (store *Store) SaveUserConfig(_ context.Context, cfg Config) (Config, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return Config{}, errors.New("配置存储未初始化")
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	disk, err := store.readLatestLocked()
	if err != nil {
		return Config{}, err
	}
	merged := OverlayGatewayToken(cfg, disk.Gateway.Token)
	merged.LastAgentModelHash = disk.LastAgentModelHash
	normalized, err := NormalizeConfig(merged)
	if err != nil {
		return Config{}, err
	}
	normalized, err = ensureGatewayToken(normalized)
	if err != nil {
		return Config{}, err
	}
	if err := store.saveLocked(normalized); err != nil {
		return Config{}, err
	}
	return normalized, nil
}

func (store *Store) SaveLastAgentModelHash(_ context.Context, value string) (Config, bool, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return Config{}, false, errors.New("配置存储未初始化")
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	disk, err := store.readLatestLocked()
	if err != nil {
		return Config{}, false, err
	}
	normalizedValue := strings.TrimSpace(value)
	if strings.TrimSpace(disk.LastAgentModelHash) == normalizedValue {
		return disk, false, nil
	}
	merged := disk
	merged.LastAgentModelHash = normalizedValue
	normalized, err := NormalizeConfig(merged)
	if err != nil {
		return Config{}, false, err
	}
	if err := store.saveLocked(normalized); err != nil {
		return Config{}, false, err
	}
	return normalized, true, nil
}

func (store *Store) SaveGatewayToken(_ context.Context, value string) (Config, bool, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return Config{}, false, errors.New("配置存储未初始化")
	}

	store.mu.Lock()
	defer store.mu.Unlock()

	disk, err := store.readLatestLocked()
	if err != nil {
		return Config{}, false, err
	}
	normalizedValue := strings.TrimSpace(value)
	if normalizedValue == "" {
		return Config{}, false, errors.New("Gateway token 不能为空")
	}
	if strings.TrimSpace(disk.Gateway.Token) == normalizedValue {
		return disk, false, nil
	}
	merged := disk
	merged.Gateway.Token = normalizedValue
	normalized, err := NormalizeConfig(merged)
	if err != nil {
		return Config{}, false, err
	}
	if err := store.saveLocked(normalized); err != nil {
		return Config{}, false, err
	}
	return normalized, true, nil
}

func (store *Store) readLatestLocked() (Config, error) {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return DefaultConfig(), nil
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return DefaultConfig(), nil
		}
		return Config{}, fmt.Errorf("读取用户配置失败: %w", err)
	}
	var current Config
	if err := yaml.Unmarshal(data, &current); err != nil {
		return Config{}, fmt.Errorf("解析用户配置失败: %w", err)
	}
	return NormalizeConfig(current)
}

func (store *Store) saveLocked(normalized Config) error {
	if err := os.MkdirAll(filepath.Dir(store.path), 0o755); err != nil {
		return fmt.Errorf("创建用户配置目录失败: %w", err)
	}
	if err := store.backupPreGatewayLocked(); err != nil {
		return err
	}

	data, err := yaml.Marshal(normalized)
	if err != nil {
		return fmt.Errorf("序列化用户配置失败: %w", err)
	}
	if err := writeFileMode(store.path, data, configFilePerm); err != nil {
		return err
	}
	return nil
}

func (store *Store) backupPreGatewayLocked() error {
	if store == nil || strings.TrimSpace(store.path) == "" {
		return nil
	}
	data, err := os.ReadFile(store.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读取用户配置失败: %w", err)
	}
	if yamlHasKey(data, "gateway") {
		return nil
	}
	backupPath := store.path + PreGatewayBackupSuffix
	if _, err := os.Stat(backupPath); err == nil {
		return nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("检查 Gateway schema 升级备份失败: %w", err)
	}
	if err := writeFileMode(backupPath, data, configFilePerm); err != nil {
		return fmt.Errorf("创建 Gateway schema 升级备份失败: %w", err)
	}
	return nil
}

func writeFileMode(path string, data []byte, perm os.FileMode) error {
	tempPath := path + ".tmp"
	if err := os.WriteFile(tempPath, data, perm); err != nil {
		return fmt.Errorf("写入临时配置失败: %w", err)
	}
	if err := os.Chmod(tempPath, perm); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("设置临时配置权限失败: %w", err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		_ = os.Remove(tempPath)
		return fmt.Errorf("保存用户配置失败: %w", err)
	}
	if err := os.Chmod(path, perm); err != nil {
		return fmt.Errorf("设置用户配置权限失败: %w", err)
	}
	return nil
}

func shouldPersistNormalizedConfig(raw []byte, current Config, normalized Config) bool {
	for _, key := range []string{"observability", "backendListenAddr", "proxyListenAddr", "routing", "appearance", "advertising", "updates", "gateway"} {
		if !yamlHasKey(raw, key) {
			return true
		}
	}
	if yamlHasKey(raw, "log") || current.Observability != normalized.Observability {
		return true
	}
	if current.Routing != normalized.Routing {
		return true
	}
	if current.BackendListenAddr != normalized.BackendListenAddr || current.ProxyListenAddr != normalized.ProxyListenAddr {
		return true
	}
	if current.Appearance.Theme != normalized.Appearance.Theme {
		return true
	}
	if current.ProviderStreamIdleTimeout == normalized.ProviderStreamIdleTimeout {
		return false
	}
	return yamlHasKey(raw, "providerStreamIdleTimeout")
}

func yamlHasKey(raw []byte, key string) bool {
	var root yaml.Node
	if err := yaml.Unmarshal(raw, &root); err != nil {
		return false
	}
	if len(root.Content) == 0 || root.Content[0].Kind != yaml.MappingNode {
		return false
	}
	mapping := root.Content[0]
	for index := 0; index+1 < len(mapping.Content); index += 2 {
		if mapping.Content[index].Value == key {
			return true
		}
	}
	return false
}
