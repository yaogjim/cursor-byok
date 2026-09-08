package cursor

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"cursor/internal/appdata"
	"cursor/internal/logger"
)

// injectedCursorSettingsKeys 表示当前模块中的 injectedCursorSettingsKeys 状态值。
var injectedCursorSettingsKeys = []string{
	"http.proxy",
	"http.proxyKerberosServicePrincipal",
	"http.proxySupport",
	"cursor.general.disableHttp2",
	"http.experimental.systemCertificatesV2",
}

// EnsureCACertFile 用于处理与 EnsureCACertFile 相关的逻辑。
func EnsureCACertFile(certPEM []byte, currentPath string) (string, error) {
	certPath := appdata.CACertFilePath()
	if samePath(strings.TrimSpace(currentPath), certPath) {
		if _, err := os.Stat(certPath); err == nil {
			logger.Infof("ensureCACertFile: reusing path=%s", certPath)
			return certPath, nil
		}
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return "", fmt.Errorf("创建证书配置目录失败: %w", err)
	}

	if existing, err := os.ReadFile(certPath); err == nil && bytes.Equal(existing, certPEM) {
		logger.Infof("ensureCACertFile: unchanged path=%s", certPath)
		return certPath, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("读取内置 CA 证书失败: %w", err)
	}

	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return "", fmt.Errorf("写入内置 CA 证书失败: %w", err)
	}
	sum := sha256.Sum256(certPEM)
	logger.Infof(
		"ensureCACertFile: wrote path=%s sha256=%s size=%d",
		certPath,
		strings.ToUpper(hex.EncodeToString(sum[:])),
		len(certPEM),
	)
	return certPath, nil
}

func samePath(left string, right string) bool {
	if strings.TrimSpace(left) == "" || strings.TrimSpace(right) == "" {
		return false
	}
	return filepath.Clean(left) == filepath.Clean(right)
}

// SetSystemNodeExtraCACerts 用于处理与 SetSystemNodeExtraCACerts 相关的逻辑。
func SetSystemNodeExtraCACerts(caCertPath string) error {
	caCertPath = strings.TrimSpace(caCertPath)
	if caCertPath == "" {
		return errors.New("CA 证书路径为空")
	}
	if err := os.Setenv("NODE_EXTRA_CA_CERTS", caCertPath); err != nil {
		return fmt.Errorf("设置进程环境变量失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "setenv", "NODE_EXTRA_CA_CERTS", caCertPath).CombinedOutput()
		if err != nil {
			return fmt.Errorf("写入 macOS 用户环境变量失败: %v: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		// Linux 发行版环境变量持久化方式差异较大，这里先确保当前进程生效。
		logger.Infof("setSystemNodeExtraCACerts: linux detected, applied to current process only")
	default:
		return fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}

	logger.Infof("setSystemNodeExtraCACerts: NODE_EXTRA_CA_CERTS=%s", caCertPath)
	return nil
}

// ClearSystemNodeExtraCACerts 用于处理与 ClearSystemNodeExtraCACerts 相关的逻辑。
func ClearSystemNodeExtraCACerts() error {
	if err := os.Unsetenv("NODE_EXTRA_CA_CERTS"); err != nil {
		return fmt.Errorf("清理进程环境变量失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("launchctl", "unsetenv", "NODE_EXTRA_CA_CERTS").CombinedOutput()
		if err != nil {
			return fmt.Errorf("清理 macOS 用户环境变量失败: %v: %s", err, strings.TrimSpace(string(out)))
		}
	case "linux":
		logger.Infof("clearSystemNodeExtraCACerts: linux detected, cleared in current process only")
	default:
		return fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}

	logger.Infof("clearSystemNodeExtraCACerts: NODE_EXTRA_CA_CERTS cleared")
	return nil
}

// UserProxySettingsStore 负责按实例所有权写入和清理 Cursor 用户代理设置。
type UserProxySettingsStore struct {
	settingsPath string
}

// NewUserProxySettingsStore 创建使用指定 Cursor settings.json 的隔离存储。
func NewUserProxySettingsStore(settingsPath string) *UserProxySettingsStore {
	return &UserProxySettingsStore{settingsPath: filepath.Clean(strings.TrimSpace(settingsPath))}
}

// DefaultUserProxySettingsStore 创建使用当前用户 Cursor settings.json 的存储。
func DefaultUserProxySettingsStore() (*UserProxySettingsStore, error) {
	settingsPath, err := resolveCursorSettingsPath()
	if err != nil {
		return nil, err
	}
	return NewUserProxySettingsStore(settingsPath), nil
}

// SettingValue 保存单个设置键的旧值。Present=false 表示该键原先不存在。
type SettingValue struct {
	Present bool
	Value   any
}

// SettingsSnapshot 仅包含本次将修改的设置键旧值，以及所有权文件旧值。
type SettingsSnapshot struct {
	Keys    map[string]SettingValue
	Owner   SettingValue
	desired map[string]any
}

func desiredUserProxySettings(proxyURL string) map[string]any {
	return map[string]any{
		"http.proxy":                             proxyURL,
		"http.proxyKerberosServicePrincipal":     proxyURL,
		"http.proxySupport":                      "on",
		"cursor.general.disableHttp2":            true,
		"http.experimental.systemCertificatesV2": true,
	}
}

func jsonValuesEqual(left any, right any) bool {
	encodedLeft, errLeft := json.Marshal(left)
	encodedRight, errRight := json.Marshal(right)
	if errLeft != nil || errRight != nil {
		return false
	}
	return bytes.Equal(encodedLeft, encodedRight)
}

func (s *UserProxySettingsStore) readSettingsMapUnlocked() (map[string]any, error) {
	settings := make(map[string]any)
	data, err := os.ReadFile(s.settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return settings, nil
		}
		return nil, fmt.Errorf("读取 Cursor 配置失败: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return settings, nil
	}
	parsed, err := decodeCursorSettingsJSONC(data)
	if err != nil {
		return nil, fmt.Errorf("解析 Cursor 配置失败: %w", err)
	}
	return parsed, nil
}

func (s *UserProxySettingsStore) readOwnerUnlocked() (SettingValue, error) {
	data, err := os.ReadFile(s.ownerPath())
	if errors.Is(err, os.ErrNotExist) {
		return SettingValue{}, nil
	}
	if err != nil {
		return SettingValue{}, fmt.Errorf("读取 Cursor 配置所有者失败: %w", err)
	}
	return SettingValue{Present: true, Value: strings.TrimSpace(string(data))}, nil
}

func snapshotChangingKeys(current map[string]any, desired map[string]any) SettingsSnapshot {
	snap := SettingsSnapshot{Keys: make(map[string]SettingValue), desired: desired}
	for _, key := range injectedCursorSettingsKeys {
		want, ok := desired[key]
		if !ok {
			continue
		}
		got, present := current[key]
		if present && jsonValuesEqual(got, want) {
			continue
		}
		snap.Keys[key] = SettingValue{Present: present, Value: got}
	}
	return snap
}

func (snap SettingsSnapshot) NeedsChange() bool {
	return len(snap.Keys) > 0
}

// Plan 只快照将要改动的注入键旧值（区分未设置），不写入、不记录键内容。
func (s *UserProxySettingsStore) Plan(proxyURL string) (SettingsSnapshot, error) {
	var snap SettingsSnapshot
	if s == nil || strings.TrimSpace(s.settingsPath) == "" || s.settingsPath == "." {
		return snap, errors.New("Cursor 配置路径为空")
	}
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return snap, errors.New("代理地址为空")
	}
	if _, err := os.Stat(filepath.Dir(s.settingsPath)); errors.Is(err, os.ErrNotExist) {
		snap = snapshotChangingKeys(map[string]any{}, desiredUserProxySettings(proxyURL))
		return snap, nil
	}
	err := s.withOwnershipLock(func() error {
		current, err := s.readSettingsMapUnlocked()
		if err != nil {
			return err
		}
		snap = snapshotChangingKeys(current, desiredUserProxySettings(proxyURL))
		snap.Owner, err = s.readOwnerUnlocked()
		return err
	})
	return snap, err
}

// Restore 只恢复本实例仍持有的设置；未转移所有权的写入失败允许恢复原 owner。
func (s *UserProxySettingsStore) Restore(snapshot SettingsSnapshot, ownerID string) error {
	if s == nil || strings.TrimSpace(s.settingsPath) == "" || s.settingsPath == "." {
		return errors.New("Cursor 配置路径为空")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return errors.New("Cursor 配置所有者为空")
	}
	return s.withOwnershipLock(func() error {
		currentOwner, err := s.readOwnerUnlocked()
		if err != nil {
			return err
		}
		if !jsonValuesEqual(currentOwner, snapshot.Owner) &&
			(!currentOwner.Present || currentOwner.Value != ownerID) {
			return errors.New("Cursor 配置所有权已变化，保留当前设置并停止回滚")
		}
		settings, err := s.readSettingsMapUnlocked()
		if err != nil {
			return err
		}
		unchanged := true
		for key, old := range snapshot.Keys {
			value, present := settings[key]
			if jsonValuesEqual(SettingValue{Present: present, Value: value}, old) {
				continue
			}
			unchanged = false
			want, planned := snapshot.desired[key]
			if !planned || !present || !jsonValuesEqual(value, want) {
				return errors.New("Cursor 配置已被其他操作修改，保留当前设置并停止回滚")
			}
		}
		if unchanged && jsonValuesEqual(currentOwner, snapshot.Owner) {
			return nil
		}
		if len(snapshot.Keys) > 0 {
			for key, old := range snapshot.Keys {
				if old.Present {
					settings[key] = old.Value
				} else {
					delete(settings, key)
				}
			}
			if err := writeSettingsMapAt(s.settingsPath, settings); err != nil {
				return err
			}
		}
		if snapshot.Owner.Present {
			ownerID, _ := snapshot.Owner.Value.(string)
			return writeSettingsOwner(s.ownerPath(), ownerID)
		}
		if err := os.Remove(s.ownerPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除 Cursor 配置所有者失败: %w", err)
		}
		return nil
	})
}

func writeSettingsMapAt(settingsPath string, settings map[string]any) error {
	if settings == nil {
		settings = make(map[string]any)
	}
	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Cursor 配置失败: %w", err)
	}
	encoded = append(encoded, '\n')
	tempPath := settingsPath + ".tmp"
	if err := os.WriteFile(tempPath, encoded, 0o644); err != nil {
		return fmt.Errorf("写入 Cursor 配置临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, settingsPath); err != nil {
		return fmt.Errorf("保存 Cursor 配置失败: %w", err)
	}
	return nil
}

// ApplyPlanned 在同一所有权锁内检查旧值并写入，拒绝过期计划。
func (s *UserProxySettingsStore) ApplyPlanned(proxyURL, ownerID string, snapshot SettingsSnapshot) error {
	return s.apply(proxyURL, ownerID, &snapshot)
}

// Apply 写入代理设置，并将清理所有权原子转移给 ownerID。
func (s *UserProxySettingsStore) Apply(proxyURL string, ownerID string) error {
	return s.apply(proxyURL, ownerID, nil)
}

func (s *UserProxySettingsStore) apply(proxyURL, ownerID string, snapshot *SettingsSnapshot) error {
	if s == nil || strings.TrimSpace(s.settingsPath) == "" || s.settingsPath == "." {
		return errors.New("Cursor 配置路径为空")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return errors.New("Cursor 配置所有者为空")
	}
	if err := os.MkdirAll(filepath.Dir(s.settingsPath), 0o755); err != nil {
		return fmt.Errorf("创建 Cursor 配置目录失败: %w", err)
	}
	return s.withOwnershipLock(func() error {
		if snapshot != nil {
			if !jsonValuesEqual(snapshot.desired, desiredUserProxySettings(strings.TrimSpace(proxyURL))) {
				return errors.New("Cursor 配置计划与目标设置不一致")
			}
			owner, err := s.readOwnerUnlocked()
			if err != nil {
				return err
			}
			if !jsonValuesEqual(owner, snapshot.Owner) {
				return errors.New("Cursor 配置所有权已变化，请重新检查后重试")
			}
			current, err := s.readSettingsMapUnlocked()
			if err != nil {
				return err
			}
			for key, desired := range snapshot.desired {
				old, changing := snapshot.Keys[key]
				if !changing {
					old = SettingValue{Present: true, Value: desired}
				}
				value, present := current[key]
				if !jsonValuesEqual(SettingValue{Present: present, Value: value}, old) {
					return errors.New("Cursor 配置已变化，请重新检查后重试")
				}
			}
		}
		if err := writeUserProxySettingsAt(s.settingsPath, proxyURL); err != nil {
			return err
		}
		return writeSettingsOwner(s.ownerPath(), ownerID)
	})
}

// ClearOwned 仅在 ownerID 仍是最新所有者时执行 beforeClear 并清理代理设置。
func (s *UserProxySettingsStore) ClearOwned(ownerID string, beforeClear func() error) (bool, error) {
	if s == nil || strings.TrimSpace(s.settingsPath) == "" || s.settingsPath == "." {
		return false, errors.New("Cursor 配置路径为空")
	}
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return false, nil
	}
	cleared := false
	err := s.withOwnershipLock(func() error {
		currentOwner, err := os.ReadFile(s.ownerPath())
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				return nil
			}
			return fmt.Errorf("读取 Cursor 配置所有者失败: %w", err)
		}
		if strings.TrimSpace(string(currentOwner)) != ownerID {
			return nil
		}
		if beforeClear != nil {
			if err := beforeClear(); err != nil {
				return err
			}
		}
		if err := clearUserProxySettingsAt(s.settingsPath); err != nil {
			return err
		}
		if err := os.Remove(s.ownerPath()); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("删除 Cursor 配置所有者失败: %w", err)
		}
		cleared = true
		return nil
	})
	return cleared, err
}

func (s *UserProxySettingsStore) ownerPath() string {
	return s.settingsPath + ".cursor-byok-owner"
}

func (s *UserProxySettingsStore) withOwnershipLock(fn func() error) error {
	lockPath := s.ownerPath() + ".lock"
	deadline := time.Now().Add(2 * time.Second)
	for {
		err := os.Mkdir(lockPath, 0o700)
		if err == nil {
			defer os.Remove(lockPath)
			return fn()
		}
		if !errors.Is(err, os.ErrExist) {
			return fmt.Errorf("锁定 Cursor 配置所有权失败: %w", err)
		}
		if info, statErr := os.Stat(lockPath); statErr == nil && time.Since(info.ModTime()) > 30*time.Second {
			_ = os.Remove(lockPath)
			continue
		}
		if time.Now().After(deadline) {
			return errors.New("等待 Cursor 配置所有权锁超时")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func writeSettingsOwner(ownerPath string, ownerID string) error {
	tempPath := ownerPath + ".tmp"
	if err := os.WriteFile(tempPath, []byte(ownerID+"\n"), 0o600); err != nil {
		return fmt.Errorf("写入 Cursor 配置所有者失败: %w", err)
	}
	if err := os.Rename(tempPath, ownerPath); err != nil {
		return fmt.Errorf("保存 Cursor 配置所有者失败: %w", err)
	}
	return nil
}

// WriteUserProxySettings 用于处理与 WriteUserProxySettings 相关的逻辑。
func writeUserProxySettingsAt(settingsPath string, proxyURL string) error {
	proxyURL = strings.TrimSpace(proxyURL)
	if proxyURL == "" {
		return errors.New("代理地址为空")
	}

	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		return fmt.Errorf("创建 Cursor 配置目录失败: %w", err)
	}

	settings := make(map[string]any)
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("读取 Cursor 配置失败: %w", err)
		}
	} else if len(bytes.TrimSpace(data)) > 0 {
		parsed, err := decodeCursorSettingsJSONC(data)
		if err != nil {
			if removeErr := os.Remove(settingsPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				return fmt.Errorf("解析 Cursor 配置失败，且删除损坏配置失败: %w", removeErr)
			}
			logger.Infof("writeCursorUserProxySettings: removed invalid settings path=%s err=%v", settingsPath, err)
			data = nil
		} else {
			settings = parsed
		}
	}

	for key, value := range desiredUserProxySettings(proxyURL) {
		settings[key] = value
	}

	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Cursor 配置失败: %w", err)
	}
	encoded = append(encoded, '\n')

	if len(bytes.TrimSpace(data)) > 0 && bytes.Equal(data, encoded) {
		logger.Infof("writeCursorUserProxySettings: unchanged path=%s proxy=%s", settingsPath, proxyURL)
		return nil
	}

	tempPath := settingsPath + ".tmp"
	if err := os.WriteFile(tempPath, encoded, 0o644); err != nil {
		return fmt.Errorf("写入 Cursor 配置临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, settingsPath); err != nil {
		return fmt.Errorf("保存 Cursor 配置失败: %w", err)
	}

	logger.Infof("writeCursorUserProxySettings: path=%s proxy=%s", settingsPath, proxyURL)
	return nil
}

// clearUserProxySettingsAt 删除指定 settings.json 中由本程序注入的代理键。
func clearUserProxySettingsAt(settingsPath string) error {
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("读取 Cursor 配置失败: %w", err)
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return nil
	}

	settings := make(map[string]any)
	parsed, err := decodeCursorSettingsJSONC(data)
	if err != nil {
		return fmt.Errorf("解析 Cursor 配置失败: %w", err)
	}
	settings = parsed

	changed := false
	for _, key := range injectedCursorSettingsKeys {
		if _, exists := settings[key]; exists {
			delete(settings, key)
			changed = true
		}
	}
	if !changed {
		return nil
	}

	encoded, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 Cursor 配置失败: %w", err)
	}
	encoded = append(encoded, '\n')

	tempPath := settingsPath + ".tmp"
	if err := os.WriteFile(tempPath, encoded, 0o644); err != nil {
		return fmt.Errorf("写入 Cursor 配置临时文件失败: %w", err)
	}
	if err := os.Rename(tempPath, settingsPath); err != nil {
		return fmt.Errorf("保存 Cursor 配置失败: %w", err)
	}

	logger.Infof("clearCursorUserProxySettings: path=%s", settingsPath)
	return nil
}

// resolveCursorSettingsPath 用于处理与 resolveCursorSettingsPath 相关的逻辑。
func resolveCursorSettingsPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("获取用户目录失败: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "Cursor", "User", "settings.json"), nil
	case "windows":
		appData := os.Getenv("APPDATA")
		if strings.TrimSpace(appData) == "" {
			appData = filepath.Join(homeDir, "AppData", "Roaming")
		}
		return filepath.Join(appData, "Cursor", "User", "settings.json"), nil
	case "linux":
		configDir := os.Getenv("XDG_CONFIG_HOME")
		if strings.TrimSpace(configDir) == "" {
			configDir = filepath.Join(homeDir, ".config")
		}
		return filepath.Join(configDir, "Cursor", "User", "settings.json"), nil
	default:
		return "", fmt.Errorf("不支持的系统: %s", runtime.GOOS)
	}
}

// decodeCursorSettingsJSONC 用于处理与 decodeCursorSettingsJSONC 相关的逻辑。
func decodeCursorSettingsJSONC(data []byte) (map[string]any, error) {
	result := make(map[string]any)
	normalized, err := normalizeJSONC(data)
	if err != nil {
		return nil, err
	}
	normalized = bytes.TrimSpace(normalized)
	if len(normalized) == 0 {
		return result, nil
	}
	if err := json.Unmarshal(normalized, &result); err != nil {
		return nil, err
	}
	return result, nil
}

// normalizeJSONC 用于处理与 normalizeJSONC 相关的逻辑。
func normalizeJSONC(data []byte) ([]byte, error) {
	if len(data) >= 3 && data[0] == 0xEF && data[1] == 0xBB && data[2] == 0xBF {
		data = data[3:]
	}
	withoutComments, err := stripJSONCComments(data)
	if err != nil {
		return nil, err
	}
	return stripJSONCTrailingCommas(withoutComments), nil
}

// stripJSONCComments 用于处理与 stripJSONCComments 相关的逻辑。
func stripJSONCComments(data []byte) ([]byte, error) {
	out := make([]byte, 0, len(data))
	inString := false
	inLineComment := false
	inBlockComment := false
	escaped := false

	for i := 0; i < len(data); i++ {
		ch := data[i]

		if inLineComment {
			if ch == '\n' {
				inLineComment = false
				out = append(out, ch)
			}
			continue
		}
		if inBlockComment {
			if ch == '*' && i+1 < len(data) && data[i+1] == '/' {
				inBlockComment = false
				i++
				continue
			}
			if ch == '\n' {
				out = append(out, ch)
			}
			continue
		}
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}
		if ch == '/' && i+1 < len(data) {
			next := data[i+1]
			if next == '/' {
				inLineComment = true
				i++
				continue
			}
			if next == '*' {
				inBlockComment = true
				i++
				continue
			}
		}
		out = append(out, ch)
	}

	if inBlockComment {
		return nil, errors.New("JSONC 块注释未闭合")
	}
	return out, nil
}

// stripJSONCTrailingCommas 用于处理与 stripJSONCTrailingCommas 相关的逻辑。
func stripJSONCTrailingCommas(data []byte) []byte {
	out := make([]byte, 0, len(data))
	inString := false
	escaped := false

	for i := 0; i < len(data); i++ {
		ch := data[i]
		if inString {
			out = append(out, ch)
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}

		if ch == '"' {
			inString = true
			out = append(out, ch)
			continue
		}

		if ch == ',' {
			j := i + 1
			for j < len(data) && isJSONWhitespace(data[j]) {
				j++
			}
			if j < len(data) && (data[j] == '}' || data[j] == ']') {
				continue
			}
		}

		out = append(out, ch)
	}

	return out
}

// isJSONWhitespace 用于处理与 isJSONWhitespace 相关的逻辑。
func isJSONWhitespace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\r' || ch == '\n'
}

// ProxyURLFromListenAddr 用于处理与 ProxyURLFromListenAddr 相关的逻辑。
func ProxyURLFromListenAddr(listenAddr string) string {
	addr := strings.TrimSpace(listenAddr)
	if addr == "" {
		return "http://127.0.0.1:8080"
	}

	// :8189 -> 127.0.0.1:8189
	if strings.HasPrefix(addr, ":") {
		return "http://127.0.0.1" + addr
	}

	host, port, err := net.SplitHostPort(addr)
	if err == nil {
		if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
			host = "127.0.0.1"
		}
		return "http://" + net.JoinHostPort(host, port)
	}

	return "http://" + addr
}
