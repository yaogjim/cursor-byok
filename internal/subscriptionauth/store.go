package subscriptionauth

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

const (
	schemaVersion  = 1
	dirPermission  = 0o700
	filePermission = 0o600
	codexFileName  = "codex-auth.json"
	grokFileName   = "grok-accounts.json"
	codexAuthMode  = "chatgpt"
)

type FileStore struct {
	dir string
}

type storedCodexAuth struct {
	SchemaVersion           int               `json:"schema_version"`
	AuthMode                string            `json:"auth_mode"`
	LastRefresh             time.Time         `json:"last_refresh"`
	Tokens                  storedTokenBundle `json:"tokens"`
	ChatGPTAccountID        string            `json:"chatgpt_account_id,omitempty"`
	Email                   string            `json:"email,omitempty"`
	PlanLabel               string            `json:"plan_label,omitempty"`
	RemainingPercent        float64           `json:"remaining_percent,omitempty"`
	UsedPercent             float64           `json:"used_percent,omitempty"`
	ResetAtMS               int64             `json:"reset_at_ms,omitempty"`
	SessionRemainingPercent float64           `json:"session_remaining_percent,omitempty"`
	SessionResetAtMS        int64             `json:"session_reset_at_ms,omitempty"`
	LimitReached            bool              `json:"limit_reached"`
	UpdatedAt               time.Time         `json:"updated_at"`
}

type storedTokenBundle struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	IDToken      string `json:"id_token,omitempty"`
}

type storedGrokFile struct {
	SchemaVersion int                 `json:"schema_version"`
	Accounts      []storedGrokAccount `json:"accounts"`
}

type storedGrokAccount struct {
	AccountID        string  `json:"account_id"`
	Provider         string  `json:"provider"`
	AccessToken      string  `json:"access_token"`
	RefreshToken     string  `json:"refresh_token,omitempty"`
	DisplayName      string  `json:"display_name,omitempty"`
	PlanLabel        string  `json:"plan_label,omitempty"`
	RemainingPercent float64 `json:"remaining_percent,omitempty"`
	UsedPercent      float64 `json:"used_percent,omitempty"`
	ResetAtMS        int64   `json:"reset_at_ms,omitempty"`
	LimitReached     bool    `json:"limit_reached"`
	Active           bool    `json:"active"`
	UpdatedAtMS      int64   `json:"updated_at_ms"`
}

func NewFileStore(dir string) *FileStore {
	return &FileStore{dir: stringsTrim(dir)}
}

func stringsTrim(value string) string {
	return trimSpace(value)
}

func (store *FileStore) Dir() string {
	if store == nil {
		return ""
	}
	return store.dir
}

func (store *FileStore) CodexPath() string {
	return filepath.Join(store.dir, codexFileName)
}

func (store *FileStore) GrokPath() string {
	return filepath.Join(store.dir, grokFileName)
}

func (store *FileStore) EnsureDir() error {
	if store == nil || store.dir == "" {
		return errors.New("subscription auth directory is empty")
	}
	if err := os.MkdirAll(store.dir, dirPermission); err != nil {
		return fmt.Errorf("create subscription auth directory: %w", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(store.dir, dirPermission); err != nil {
			return fmt.Errorf("secure subscription auth directory: %w", err)
		}
	}
	return nil
}

var renameFile = os.Rename

func (store *FileStore) writeJSON(path string, value any) error {
	if err := store.EnsureDir(); err != nil {
		return err
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()

	if runtime.GOOS != "windows" {
		if err := temp.Chmod(filePermission); err != nil {
			_ = temp.Close()
			return err
		}
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if err := replaceFile(tempPath, path); err != nil {
		return err
	}
	committed = true
	if runtime.GOOS != "windows" {
		if err := os.Chmod(path, filePermission); err != nil {
			return err
		}
	}
	return nil
}

func replaceFile(tempPath string, path string) error {
	err := renameFile(tempPath, path)
	if err == nil || runtime.GOOS != "windows" {
		return err
	}
	backupPath := path + ".bak"
	_ = os.Remove(backupPath)
	if _, statErr := os.Stat(path); statErr == nil {
		if err := renameFile(path, backupPath); err != nil {
			return err
		}
	} else if !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	if err := renameFile(tempPath, path); err != nil {
		if _, bakErr := os.Stat(backupPath); bakErr == nil {
			_ = renameFile(backupPath, path)
		}
		return err
	}
	_ = os.Remove(backupPath)
	return nil
}

func (store *FileStore) readJSON(path string, dest any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, filePermission)
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return err
	}
	return nil
}

func (store *FileStore) LoadCodex() (*storedCodexAuth, error) {
	var auth storedCodexAuth
	err := store.readJSON(store.CodexPath(), &auth)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if trimSpace(auth.Tokens.AccessToken) == "" {
		return nil, nil
	}
	return &auth, nil
}

func (store *FileStore) SaveCodex(auth storedCodexAuth) error {
	auth.SchemaVersion = schemaVersion
	if trimSpace(auth.AuthMode) == "" {
		auth.AuthMode = codexAuthMode
	}
	auth.UpdatedAt = time.Now().UTC()
	return store.writeJSON(store.CodexPath(), auth)
}

func (store *FileStore) ClearCodex() error {
	err := os.Remove(store.CodexPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (store *FileStore) LoadGrok() (storedGrokFile, error) {
	var file storedGrokFile
	err := store.readJSON(store.GrokPath(), &file)
	if errors.Is(err, os.ErrNotExist) {
		return storedGrokFile{SchemaVersion: schemaVersion}, nil
	}
	if err != nil {
		return storedGrokFile{}, err
	}
	if file.Accounts == nil {
		file.Accounts = []storedGrokAccount{}
	}
	return file, nil
}

func (store *FileStore) SaveGrok(file storedGrokFile) error {
	file.SchemaVersion = schemaVersion
	if file.Accounts == nil {
		file.Accounts = []storedGrokAccount{}
	}
	return store.writeJSON(store.GrokPath(), file)
}

func nowMS() int64 {
	return time.Now().UTC().UnixMilli()
}

func timeFromMS(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms).UTC()
}
