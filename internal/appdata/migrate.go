package appdata

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	maxLegacyConfigBytes = 2 << 20
	maxLegacyRuleBytes   = 128 << 10
)

func ensureAssistantHome() error {
	migrateLegacyAssistantHome()
	for _, path := range PrivateDirPaths() {
		if err := ensurePrivateDir(path); err != nil {
			return fmt.Errorf("secure assistant directory: %w", err)
		}
	}
	return nil
}

func EnsureAssistantHome() error {
	return ensureAssistantHome()
}

func migrateLegacyAssistantHome() {
	legacyRoot := legacyRootDir()
	configOK := copyLegacyFile(filepath.Join(legacyRoot, "config.yaml"), filepath.Join(RootDir(), "config.yaml"), maxLegacyConfigBytes)
	rulesOK := copyLegacyRules(filepath.Join(legacyRoot, "rules"), RulesRootPath())
	if configOK && rulesOK {
		_ = os.RemoveAll(legacyRoot)
	}
}

func copyLegacyRules(sourceRoot string, targetRoot string) bool {
	info, err := os.Lstat(sourceRoot)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false
	}
	entries, err := os.ReadDir(sourceRoot)
	if err != nil {
		return false
	}
	complete := true
	for _, entry := range entries {
		name := entry.Name()
		if !canonicalLegacyChildName(name) {
			complete = false
			continue
		}
		sourcePath := filepath.Join(sourceRoot, name)
		child, err := os.Lstat(sourcePath)
		if err != nil || !child.Mode().IsRegular() {
			complete = false
			continue
		}
		if !copyLegacyFile(sourcePath, filepath.Join(targetRoot, name), maxLegacyRuleBytes) {
			complete = false
		}
	}
	return complete
}

func canonicalLegacyChildName(name string) bool {
	if name == "" || name == "." || name == ".." || filepath.Base(name) != name {
		return false
	}
	return !strings.ContainsAny(name, `/\`)
}

func copyLegacyFile(sourcePath string, targetPath string, maxBytes int64) bool {
	info, err := os.Lstat(sourcePath)
	if errors.Is(err, os.ErrNotExist) {
		return true
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxBytes {
		return false
	}
	if targetInfo, err := os.Lstat(targetPath); err == nil {
		if !targetInfo.Mode().IsRegular() {
			return false
		}
		return ensurePrivateFile(targetPath) == nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false
	}
	sourceFile, err := os.Open(sourcePath)
	if err != nil {
		return false
	}
	defer sourceFile.Close()

	if err := ensurePrivateDir(filepath.Dir(targetPath)); err != nil {
		return false
	}
	targetFile, err := os.CreateTemp(filepath.Dir(targetPath), ".migrate-*.tmp")
	if err != nil {
		return false
	}
	tempPath := targetFile.Name()
	complete := false
	defer func() {
		if !complete {
			_ = targetFile.Close()
			_ = os.Remove(tempPath)
		}
	}()
	if err := targetFile.Chmod(0o600); err != nil {
		return false
	}
	if err := ensurePrivateFile(tempPath); err != nil {
		return false
	}
	written, err := io.Copy(targetFile, io.LimitReader(sourceFile, maxBytes+1))
	if err != nil || written > maxBytes {
		return false
	}
	if err := targetFile.Sync(); err != nil {
		return false
	}
	if err := targetFile.Close(); err != nil {
		return false
	}
	if err := os.Rename(tempPath, targetPath); err != nil {
		return false
	}
	if err := syncPrivateDir(filepath.Dir(targetPath)); err != nil {
		return false
	}
	complete = true
	return true
}
