package paths

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	DirName         = ".cursor-local-assistant-v2"
	PoolFileName    = "cli-model-pool.yaml"
	BYOKFileName    = "config.yaml"
	JournalFileName = "cli-model-pool-journal.jsonl"
	AllowedEndpoint = "http://127.0.0.1:18090"
	CursorDirName   = ".cursor"
)

func HomeDir() (string, error) {
	return os.UserHomeDir()
}

func DataDir() (string, error) {
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, DirName), nil
}

func PoolConfigPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, PoolFileName), nil
}

func BYOKConfigPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, BYOKFileName), nil
}

func JournalPath() (string, error) {
	dir, err := DataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, JournalFileName), nil
}

func CursorWorktreePath(repoBasename, name string) (string, error) {
	home, err := HomeDir()
	if err != nil {
		return "", err
	}
	base := filepath.Base(strings.TrimSpace(repoBasename))
	name = strings.TrimSpace(name)
	if home == "" || base == "" || base == "." || name == "" {
		return "", fmt.Errorf("worktree 路径不完整")
	}
	if strings.ContainsAny(base, `/\`) || strings.ContainsAny(name, `/\`) {
		return "", fmt.Errorf("worktree 路径不完整")
	}
	return filepath.Join(home, CursorDirName, "worktrees", base, name), nil
}
