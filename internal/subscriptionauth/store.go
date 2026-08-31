package subscriptionauth

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"
)

const (
	schemaVersion       = 1
	codexSchemaVersion  = 2
	dirPermission       = 0o700
	filePermission      = 0o600
	codexFileName       = "codex-auth.json"
	grokFileName        = "grok-accounts.json"
	affinityKeyFileName = "codex-affinity.key"
	codexAuthMode       = "chatgpt"
	AffinityKeySize     = 32
)

// ErrAffinityKeyLength is returned when the on-disk affinity key is not 32 bytes.
var ErrAffinityKeyLength = errors.New("codex affinity key must be 32 bytes")

type FileStore struct {
	dir        string
	affinityMu sync.Mutex
}

type storedCodexFile struct {
	SchemaVersion int               `json:"schema_version"`
	Accounts      []storedCodexAuth `json:"accounts"`
}

type storedCodexAuth struct {
	Active                  bool              `json:"active"`
	AuthRequired            bool              `json:"auth_required"`
	SchemaVersion           int               `json:"schema_version,omitempty"`
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

func (store *FileStore) AffinityKeyPath() string {
	return filepath.Join(store.dir, affinityKeyFileName)
}

func (store *FileStore) LoadOrCreateAffinityKey() ([]byte, error) {
	if store == nil {
		return nil, errors.New("subscription auth store is unavailable")
	}
	store.affinityMu.Lock()
	defer store.affinityMu.Unlock()
	if key, err := store.readAffinityKeyLocked(); err == nil {
		return key, nil
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	key := make([]byte, AffinityKeySize)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate codex affinity key: %w", err)
	}
	if err := store.writeAffinityKeyLocked(key); err != nil {
		if existing, readErr := store.readAffinityKeyLocked(); readErr == nil {
			return existing, nil
		}
		return nil, err
	}
	return key, nil
}

func (store *FileStore) readAffinityKeyLocked() ([]byte, error) {
	path := store.AffinityKeyPath()
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("codex affinity key is not a regular file")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if runtime.GOOS != "windows" {
		_ = os.Chmod(path, filePermission)
	}
	if len(data) != AffinityKeySize {
		return nil, ErrAffinityKeyLength
	}
	out := make([]byte, AffinityKeySize)
	copy(out, data)
	return out, nil
}

func (store *FileStore) writeAffinityKeyLocked(key []byte) error {
	if len(key) != AffinityKeySize {
		return ErrAffinityKeyLength
	}
	if info, err := os.Lstat(store.AffinityKeyPath()); err == nil {
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("codex affinity key is not a regular file")
		}
		return errors.New("codex affinity key already exists")
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := store.EnsureDir(); err != nil {
		return err
	}
	path := store.AffinityKeyPath()
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, "."+affinityKeyFileName+"-*.tmp")
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
	if _, err := temp.Write(key); err != nil {
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
	return syncStoreDir(dir)
}

func syncStoreDir(path string) error {
	if runtime.GOOS == "windows" {
		return nil
	}
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
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

func (store *FileStore) LoadCodexFile() (storedCodexFile, error) {
	var file storedCodexFile
	err := store.readJSON(store.CodexPath(), &file)
	if errors.Is(err, os.ErrNotExist) {
		return storedCodexFile{SchemaVersion: codexSchemaVersion, Accounts: []storedCodexAuth{}}, nil
	}
	if err != nil {
		return storedCodexFile{}, err
	}
	if file.Accounts != nil {
		file.SchemaVersion = codexSchemaVersion
		return file, nil
	}

	// schema v1 stored one account at the document root. Read it again as the
	// legacy shape and keep its credentials unchanged while migrating in memory.
	var legacy storedCodexAuth
	if err := store.readJSON(store.CodexPath(), &legacy); err != nil {
		return storedCodexFile{}, err
	}
	file = storedCodexFile{SchemaVersion: codexSchemaVersion, Accounts: []storedCodexAuth{}}
	if trimSpace(legacy.Tokens.AccessToken) != "" {
		legacy.Active = true
		legacy.AuthRequired = false
		legacy.SchemaVersion = 0
		file.Accounts = append(file.Accounts, legacy)
	}
	if err := store.SaveCodexFile(file); err != nil {
		return storedCodexFile{}, err
	}
	return file, nil
}

func (store *FileStore) SaveCodexFile(file storedCodexFile) error {
	file.SchemaVersion = codexSchemaVersion
	if file.Accounts == nil {
		file.Accounts = []storedCodexAuth{}
	}
	for i := range file.Accounts {
		file.Accounts[i].SchemaVersion = 0
		if trimSpace(file.Accounts[i].AuthMode) == "" {
			file.Accounts[i].AuthMode = codexAuthMode
		}
		file.Accounts[i].UpdatedAt = time.Now().UTC()
	}
	return store.writeJSON(store.CodexPath(), file)
}

func (store *FileStore) LoadCodex() (*storedCodexAuth, error) {
	file, err := store.LoadCodexFile()
	if err != nil {
		return nil, err
	}
	for _, account := range file.Accounts {
		if account.Active && trimSpace(account.Tokens.AccessToken) != "" {
			copy := account
			return &copy, nil
		}
	}
	for _, account := range file.Accounts {
		if trimSpace(account.Tokens.AccessToken) != "" {
			copy := account
			return &copy, nil
		}
	}
	return nil, nil
}

func (store *FileStore) SaveCodex(auth storedCodexAuth) error {
	file, err := store.LoadCodexFile()
	if err != nil {
		return err
	}
	accountID, _, _ := accountIdentity(ProviderCodex, auth.Tokens.AccessToken, auth.Tokens.IDToken)
	updated := false
	for i := range file.Accounts {
		existingID, _, _ := accountIdentity(ProviderCodex, file.Accounts[i].Tokens.AccessToken, file.Accounts[i].Tokens.IDToken)
		if existingID != accountID {
			continue
		}
		existing := file.Accounts[i]
		auth.Active = existing.Active
		auth.AuthRequired = false
		auth.PlanLabel = firstNonEmpty(auth.PlanLabel, existing.PlanLabel)
		auth.RemainingPercent = existing.RemainingPercent
		auth.UsedPercent = existing.UsedPercent
		auth.ResetAtMS = existing.ResetAtMS
		auth.SessionRemainingPercent = existing.SessionRemainingPercent
		auth.SessionResetAtMS = existing.SessionResetAtMS
		auth.LimitReached = existing.LimitReached
		auth.ChatGPTAccountID = firstNonEmpty(auth.ChatGPTAccountID, existing.ChatGPTAccountID)
		auth.Email = firstNonEmpty(auth.Email, existing.Email)
		file.Accounts[i] = auth
		updated = true
		break
	}
	if !updated {
		auth.Active = len(file.Accounts) == 0
		file.Accounts = append(file.Accounts, auth)
	}
	return store.SaveCodexFile(file)
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
