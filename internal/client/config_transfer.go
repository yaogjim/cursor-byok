package client

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	serverconfig "cursor/internal/backend/server/config"

	"gopkg.in/yaml.v3"
)

const maxConfigTransferFileSize = 4 << 20

type importedUserConfigDocument struct {
	serverconfig.Config `yaml:",inline"`
	LegacyRouting       any `yaml:"routing,omitempty"`
}

// ExportUserConfig 将当前完整配置导出为 YAML 文件。
func (s *ProxyService) ExportUserConfig(path string) (string, error) {
	if s == nil {
		return "", errors.New("配置服务未初始化")
	}
	targetPath := normalizeConfigExportPath(path)
	if targetPath == "" {
		return "", errors.New("导出路径不能为空")
	}

	s.configMu.Lock()
	defer s.configMu.Unlock()
	cfg, err := s.loadUserConfig()
	if err != nil {
		return "", fmt.Errorf("读取当前配置失败: %w", err)
	}
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("序列化导出配置失败: %w", err)
	}
	if err := writeExportedUserConfig(targetPath, data); err != nil {
		return "", err
	}
	return targetPath, nil
}

// ImportUserConfig 从 YAML 文件校验并替换当前完整配置。
func (s *ProxyService) ImportUserConfig(path string) (UserConfig, error) {
	if s == nil {
		return serverconfig.Config{}, errors.New("配置服务未初始化")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if err := ensureConfigImportAllowed(s.GetState()); err != nil {
		return serverconfig.Config{}, err
	}
	data, err := readImportedUserConfig(path)
	if err != nil {
		return serverconfig.Config{}, err
	}
	cfg, err := decodeImportedUserConfig(data)
	if err != nil {
		return serverconfig.Config{}, err
	}
	if err := s.saveUserConfig(cfg); err != nil {
		return serverconfig.Config{}, fmt.Errorf("保存导入配置失败: %w", err)
	}
	persisted, err := s.loadUserConfig()
	if err != nil {
		return serverconfig.Config{}, fmt.Errorf("重新读取导入配置失败: %w", err)
	}
	return persisted, nil
}

func normalizeConfigExportPath(path string) string {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return ""
	}
	extension := strings.ToLower(filepath.Ext(trimmed))
	if extension != ".yaml" && extension != ".yml" {
		trimmed += ".yaml"
	}
	return filepath.Clean(trimmed)
}

func writeExportedUserConfig(path string, data []byte) error {
	directory := filepath.Dir(path)
	file, err := os.CreateTemp(directory, ".cursor-byok-config-*.tmp")
	if err != nil {
		return fmt.Errorf("创建导出配置临时文件失败: %w", err)
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return fmt.Errorf("设置导出配置权限失败: %w", err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("写入导出配置失败: %w", err)
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return fmt.Errorf("同步导出配置失败: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("关闭导出配置失败: %w", err)
	}
	if err := replaceExportFile(temporaryPath, path); err != nil {
		return fmt.Errorf("替换导出配置失败: %w", err)
	}
	return nil
}

func ensureConfigImportAllowed(state ProxyState) error {
	if state.BackendRunning || state.ProxyRunning || state.Running {
		return errors.New("服务运行中不能导入完整配置，请先停止服务")
	}
	return nil
}

func readImportedUserConfig(path string) ([]byte, error) {
	sourcePath := strings.TrimSpace(path)
	if sourcePath == "" {
		return nil, errors.New("导入路径不能为空")
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("打开导入配置失败: %w", err)
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("读取导入配置信息失败: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("导入配置必须是普通文件")
	}
	if info.Size() > maxConfigTransferFileSize {
		return nil, fmt.Errorf("导入配置不能超过 %d MiB", maxConfigTransferFileSize>>20)
	}
	data, err := io.ReadAll(io.LimitReader(file, maxConfigTransferFileSize+1))
	if err != nil {
		return nil, fmt.Errorf("读取导入配置失败: %w", err)
	}
	if len(data) > maxConfigTransferFileSize {
		return nil, fmt.Errorf("导入配置不能超过 %d MiB", maxConfigTransferFileSize>>20)
	}
	return data, nil
}

func decodeImportedUserConfig(data []byte) (serverconfig.Config, error) {
	if err := validateImportedUserConfigDocument(data); err != nil {
		return serverconfig.Config{}, err
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	decoder.KnownFields(true)
	var document importedUserConfigDocument
	if err := decoder.Decode(&document); err != nil {
		return serverconfig.Config{}, fmt.Errorf("导入配置包含未知字段或无效 YAML: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err == nil {
		return serverconfig.Config{}, errors.New("导入配置只能包含单个 YAML 文档")
	} else if !errors.Is(err, io.EOF) {
		return serverconfig.Config{}, fmt.Errorf("解析导入配置尾部失败: %w", err)
	}
	normalized, err := serverconfig.NormalizeConfig(document.Config)
	if err != nil {
		return serverconfig.Config{}, fmt.Errorf("导入配置校验失败: %w", err)
	}
	return normalized, nil
}

func validateImportedUserConfigDocument(data []byte) error {
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var document yaml.Node
	if err := decoder.Decode(&document); err != nil {
		if errors.Is(err, io.EOF) {
			return errors.New("导入配置不能为空")
		}
		return fmt.Errorf("导入配置不是有效 YAML: %w", err)
	}
	if len(document.Content) != 1 || document.Content[0].Kind != yaml.MappingNode {
		return errors.New("导入配置顶层必须是 YAML 对象")
	}
	return nil
}
