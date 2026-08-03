package observability

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"sort"
	"strings"
)

const projectKeyFilename = ".project-hmac-key"

func RuntimeMetadata(appVersion string, configFingerprint string) SessionMetadata {
	return SessionMetadata{
		SourceKind:        "client",
		AppVersion:        strings.TrimSpace(appVersion),
		BuildID:           currentBuildID(),
		Platform:          runtime.GOOS + "-" + runtime.GOARCH,
		ConfigFingerprint: strings.TrimSpace(configFingerprint),
	}
}

func ConfigFingerprint(values ...string) string {
	hash := sha256.New()
	for _, value := range values {
		normalized := strings.TrimSpace(value)
		_, _ = fmt.Fprintf(hash, "%d:%s\x00", len(normalized), normalized)
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func deriveProjectID(key []byte, paths []string) string {
	normalized := normalizeProjectPaths(paths)
	if len(key) == 0 || len(normalized) == 0 {
		return ""
	}
	mac := hmac.New(sha256.New, key)
	for _, path := range normalized {
		_, _ = fmt.Fprintf(mac, "%d:%s\x00", len(path), path)
	}
	return "project_" + hex.EncodeToString(mac.Sum(nil)[:20])
}

func normalizeProjectPaths(paths []string) []string {
	unique := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		absolute, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			continue
		}
		if resolved, err := filepath.EvalSymlinks(absolute); err == nil {
			absolute = resolved
		}
		normalized := filepath.ToSlash(filepath.Clean(absolute))
		if runtime.GOOS == "windows" {
			normalized = strings.ToLower(normalized)
		}
		unique[normalized] = struct{}{}
	}
	result := make([]string, 0, len(unique))
	for path := range unique {
		result = append(result, path)
	}
	sort.Strings(result)
	return result
}

func loadOrCreateProjectKey(root string) ([]byte, error) {
	if err := ensurePrivateDir(root); err != nil {
		return nil, err
	}
	path := filepath.Join(root, projectKeyFilename)
	key, err := os.ReadFile(path)
	if err == nil {
		return validateProjectKey(key)
	}
	if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return loadProjectKey(path)
	}
	if err != nil {
		return nil, err
	}
	written, writeErr := file.Write(key)
	closeErr := file.Close()
	if writeErr != nil || written != len(key) {
		_ = os.Remove(path)
		if writeErr != nil {
			return nil, writeErr
		}
		return nil, fmt.Errorf("write project key: wrote %d bytes, want %d", written, len(key))
	}
	if closeErr != nil {
		_ = os.Remove(path)
		return nil, closeErr
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return key, nil
}

func loadProjectKey(path string) ([]byte, error) {
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return validateProjectKey(key)
}

func validateProjectKey(key []byte) ([]byte, error) {
	if len(key) != 32 {
		return nil, fmt.Errorf("invalid project key length %d", len(key))
	}
	return append([]byte(nil), key...), nil
}

func currentBuildID() string {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return ""
	}
	var revision string
	var modified string
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			modified = strings.TrimSpace(setting.Value)
		}
	}
	if revision == "" {
		return ""
	}
	if modified == "true" {
		return revision + "+dirty"
	}
	return revision
}
