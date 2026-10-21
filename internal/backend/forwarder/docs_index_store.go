package forwarder

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"cursor/internal/appdata"
	"cursor/internal/logger"
)

const (
	docsIndexStatusIndexed    = "indexed"
	docsIndexSourceLocal      = "local_docs"
	docsIndexSourceAdditional = "additional_docs"
	docsIndexSourceUserRules  = "user_rules"
	maxDocsIndexFileBytes     = 2 << 20
	maxDocsIndexRecords       = 256
	maxDocsIndexContentBytes  = 1 << 20
)

// persistFileOps 是原子写的最小可注入 seam。零值走生产实现。
type persistFileOps struct {
	rename            func(oldpath, newpath string) error
	ensurePrivateFile func(path string) error
	syncDir           func(path string) error
}

func (ops persistFileOps) Rename(oldpath, newpath string) error {
	if ops.rename != nil {
		return ops.rename(oldpath, newpath)
	}
	return os.Rename(oldpath, newpath)
}

func (ops persistFileOps) EnsurePrivateFile(path string) error {
	if ops.ensurePrivateFile != nil {
		return ops.ensurePrivateFile(path)
	}
	return appdata.EnsurePrivateFile(path)
}

func (ops persistFileOps) SyncDir(path string) error {
	if ops.syncDir != nil {
		return ops.syncDir(path)
	}
	return syncDirectory(path)
}

// persistDurabilityError 表示 rename 已提交后的权限/目录 sync 失败。
// 磁盘事实源已切换；调用方不得按提交前回滚。
type persistDurabilityError struct {
	op  string
	err error
}

func (e *persistDurabilityError) Error() string {
	if e == nil || e.err == nil {
		return "persist durability error"
	}
	if e.op == "" {
		return e.err.Error()
	}
	return e.op + " durability: " + e.err.Error()
}

func (e *persistDurabilityError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.err
}

func isPersistDurabilityError(err error) bool {
	var target *persistDurabilityError
	return errors.As(err, &target)
}

func canReplaceWithRuleProjection(source string) bool {
	switch strings.TrimSpace(source) {
	case "", docsIndexSourceLocal, docsIndexSourceUserRules:
		return true
	default:
		return false
	}
}

type DocsIndexStore struct {
	mu                          sync.Mutex
	root, path                  string
	loaded, ruleProjectionDirty bool
	state                       docsIndexState
	ops                         persistFileOps
}
type docsIndexState struct {
	SchemaVersion int                        `json:"schema_version"`
	Docs          map[string]DocsIndexRecord `json:"docs"`
}
type DocsIndexRecord struct {
	ID          string    `json:"id"`
	Identifier  string    `json:"identifier"`
	Title       string    `json:"title"`
	URL         string    `json:"url,omitempty"`
	Content     string    `json:"content,omitempty"`
	GitOrigin   string    `json:"git_origin,omitempty"`
	Status      string    `json:"status"`
	Source      string    `json:"source"`
	CreatedAt   time.Time `json:"created_at"`
	UpdatedAt   time.Time `json:"updated_at"`
	LastCrawlAt time.Time `json:"last_crawl_at,omitempty"`
	LastIndexAt time.Time `json:"last_index_at,omitempty"`
}

func NewDocsIndexStore(root string) *DocsIndexStore {
	root = strings.TrimSpace(root)
	if root == "" {
		root = "docs-index"
	}
	return &DocsIndexStore{root: root, path: filepath.Join(root, "index.json"), ruleProjectionDirty: true, state: newDocsIndexState()}
}
func newDocsIndexState() docsIndexState {
	return docsIndexState{SchemaVersion: 1, Docs: make(map[string]DocsIndexRecord)}
}
func cloneDocsIndexState(s docsIndexState) docsIndexState {
	c := docsIndexState{SchemaVersion: s.SchemaVersion, Docs: make(map[string]DocsIndexRecord, len(s.Docs))}
	for k, v := range s.Docs {
		c.Docs[k] = v
	}
	return c
}

func (store *DocsIndexStore) Upsert(record DocsIndexRecord) (DocsIndexRecord, error) {
	if store == nil {
		return DocsIndexRecord{}, fmt.Errorf("docs index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return DocsIndexRecord{}, err
	}
	next := cloneDocsIndexState(store.state)
	now := time.Now().UTC()
	id := strings.TrimSpace(record.Identifier)
	if id == "" {
		id = stableDocsIdentifier(record.Title, record.URL, record.Content, record.GitOrigin)
	}
	existing, ok := next.Docs[id]
	if ok {
		existing.Title = firstNonEmptyDocs(record.Title, existing.Title, id)
		existing.URL = firstNonEmptyDocs(record.URL, existing.URL)
		existing.Content = firstNonEmptyDocs(record.Content, existing.Content)
		existing.GitOrigin = firstNonEmptyDocs(record.GitOrigin, existing.GitOrigin)
		existing.Status = docsIndexStatusIndexed
		existing.Source = firstNonEmptyDocs(record.Source, existing.Source, docsIndexSourceLocal)
		existing.UpdatedAt = now
		existing.LastCrawlAt = now
		existing.LastIndexAt = now
		record = existing
	} else {
		record.ID = firstNonEmptyDocs(record.ID, id)
		record.Identifier = id
		record.Title = firstNonEmptyDocs(record.Title, id)
		record.Status = docsIndexStatusIndexed
		record.Source = firstNonEmptyDocs(record.Source, docsIndexSourceLocal)
		record.CreatedAt = now
		record.UpdatedAt = now
		record.LastCrawlAt = now
		record.LastIndexAt = now
	}
	next.Docs[id] = record
	if err := validateDocsIndexState(next); err != nil {
		return DocsIndexRecord{}, err
	}
	if err := store.commitStateLocked("upsert", next); err != nil {
		return DocsIndexRecord{}, err
	}
	return record, nil
}
func (store *DocsIndexStore) List(origin string, limit int32) ([]DocsIndexRecord, error) {
	if store == nil {
		return nil, fmt.Errorf("docs index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return nil, err
	}
	origin = strings.TrimSpace(origin)
	out := make([]DocsIndexRecord, 0, len(store.state.Docs))
	for _, r := range store.state.Docs {
		if origin == "" || strings.TrimSpace(r.GitOrigin) == origin {
			out = append(out, r)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].Identifier < out[j].Identifier
		}
		return out[i].UpdatedAt.After(out[j].UpdatedAt)
	})
	if limit > 0 && len(out) > int(limit) {
		out = out[:limit]
	}
	return out, nil
}
func (store *DocsIndexStore) Get(id string) (DocsIndexRecord, bool, error) {
	if store == nil {
		return DocsIndexRecord{}, false, fmt.Errorf("docs index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return DocsIndexRecord{}, false, err
	}
	r, ok := store.state.Docs[strings.TrimSpace(id)]
	return r, ok, nil
}
func (store *DocsIndexStore) Remove(id string) error {
	if store == nil {
		return fmt.Errorf("docs index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		return err
	}
	next := cloneDocsIndexState(store.state)
	delete(next.Docs, strings.TrimSpace(id))
	return store.commitStateLocked("remove", next)
}
func (store *DocsIndexStore) MarkRuleProjectionDirty() {
	if store == nil {
		return
	}
	store.mu.Lock()
	store.ruleProjectionDirty = true
	store.mu.Unlock()
}
func (store *DocsIndexStore) RuleProjectionDirty() bool {
	if store == nil {
		return false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.ruleProjectionDirty
}
func (store *DocsIndexStore) ReconcileRuleProjection(rules []UserRuleRecord) error {
	if store == nil {
		return fmt.Errorf("docs index store is nil")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := store.loadLocked(); err != nil {
		store.ruleProjectionDirty = true
		return err
	}
	next := cloneDocsIndexState(store.state)
	live := make(map[string]UserRuleRecord, len(rules))
	for _, rule := range rules {
		id := strings.TrimSpace(rule.ID)
		if id == "" {
			continue
		}
		live[id] = rule
	}
	for id, r := range next.Docs {
		if r.Source == docsIndexSourceUserRules {
			if _, ok := live[id]; !ok {
				delete(next.Docs, id)
			}
		}
	}
	now := time.Now().UTC()
	for id, rule := range live {
		existing, ok := next.Docs[id]
		if ok && !canReplaceWithRuleProjection(existing.Source) {
			continue
		}
		created := rule.ModifiedAt.UTC()
		if created.IsZero() {
			created = now
		}
		if ok && !existing.CreatedAt.IsZero() {
			created = existing.CreatedAt
		}
		next.Docs[id] = DocsIndexRecord{
			ID:          id,
			Identifier:  id,
			Title:       id,
			Content:     rule.Knowledge,
			Status:      docsIndexStatusIndexed,
			Source:      docsIndexSourceUserRules,
			CreatedAt:   created,
			UpdatedAt:   now,
			LastCrawlAt: now,
			LastIndexAt: now,
		}
	}
	if err := validateDocsIndexState(next); err != nil {
		store.ruleProjectionDirty = true
		return err
	}
	if err := store.commitStateLocked("reconcile", next); err != nil {
		store.ruleProjectionDirty = true
		return err
	}
	store.ruleProjectionDirty = false
	return nil
}
func (store *DocsIndexStore) loadLocked() error {
	if store.loaded {
		return nil
	}
	state := newDocsIndexState()
	if err := ensurePrivateStoreDir(store.root); err != nil {
		return err
	}
	info, err := os.Lstat(store.path)
	if errors.Is(err, os.ErrNotExist) {
		store.state = state
		store.loaded = true
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("docs index is not a regular file")
	}
	if err := appdata.EnsurePrivateFile(store.path); err != nil {
		return fmt.Errorf("secure docs index file: %w", err)
	}
	if info.Size() > maxDocsIndexFileBytes {
		return store.isolateCorruptLocked(fmt.Errorf("docs index exceeds %d bytes", maxDocsIndexFileBytes))
	}
	f, err := os.Open(store.path)
	if err != nil {
		return err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxDocsIndexFileBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return readErr
	}
	if closeErr != nil {
		return closeErr
	}
	if len(data) > maxDocsIndexFileBytes {
		return store.isolateCorruptLocked(fmt.Errorf("docs index exceeds %d bytes", maxDocsIndexFileBytes))
	}
	if len(data) > 0 {
		if err := json.Unmarshal(data, &state); err != nil {
			return store.isolateCorruptLocked(err)
		}
	}
	if state.SchemaVersion <= 0 {
		state.SchemaVersion = 1
	}
	if state.Docs == nil {
		state.Docs = make(map[string]DocsIndexRecord)
	}
	if err := validateDocsIndexState(state); err != nil {
		return store.isolateCorruptLocked(err)
	}
	store.state = state
	store.loaded = true
	return nil
}
func (store *DocsIndexStore) isolateCorruptLocked(cause error) error {
	quarantine := fmt.Sprintf("%s.corrupt-%d", store.path, time.Now().UTC().UnixNano())
	if err := os.Rename(store.path, quarantine); err != nil {
		return fmt.Errorf("invalid docs index: %v; quarantine: %w", cause, err)
	}
	store.state = newDocsIndexState()
	store.loaded = true
	store.ruleProjectionDirty = true
	return nil
}
func validateDocsIndexState(state docsIndexState) error {
	if len(state.Docs) > maxDocsIndexRecords {
		return fmt.Errorf("docs index exceeds %d records", maxDocsIndexRecords)
	}
	total := 0
	for id, r := range state.Docs {
		if strings.TrimSpace(id) == "" || strings.TrimSpace(r.Identifier) == "" {
			return fmt.Errorf("docs index contains empty identifier")
		}
		total += len([]byte(r.Content))
		if total > maxDocsIndexContentBytes {
			return fmt.Errorf("docs index content exceeds %d bytes", maxDocsIndexContentBytes)
		}
	}
	return nil
}
func (store *DocsIndexStore) saveStateLocked(state docsIndexState) error {
	if err := ensurePrivateStoreDir(store.root); err != nil {
		return err
	}
	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	if len(data) > maxDocsIndexFileBytes {
		return fmt.Errorf("docs index exceeds %d bytes", maxDocsIndexFileBytes)
	}
	tmp, err := os.CreateTemp(store.root, ".index-*.tmp")
	if err != nil {
		return err
	}
	path := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(path)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := store.ops.EnsurePrivateFile(path); err != nil {
		return err
	}
	n, err := io.Copy(tmp, io.LimitReader(strings.NewReader(string(data)), maxDocsIndexFileBytes+1))
	if err != nil {
		return err
	}
	if n > maxDocsIndexFileBytes {
		return fmt.Errorf("docs index exceeds %d bytes", maxDocsIndexFileBytes)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if info, err := os.Lstat(store.path); err == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("docs index target is not a regular file")
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := store.ops.Rename(path, store.path); err != nil {
		return err
	}
	ok = true
	var durability error
	if err := store.ops.EnsurePrivateFile(store.path); err != nil {
		durability = &persistDurabilityError{op: "docs_index_chmod", err: err}
	}
	if err := store.ops.SyncDir(store.root); err != nil && durability == nil {
		durability = &persistDurabilityError{op: "docs_index_dirsync", err: err}
	}
	return durability
}

func (store *DocsIndexStore) commitStateLocked(operation string, next docsIndexState) error {
	err := store.saveStateLocked(next)
	if err != nil && !isPersistDurabilityError(err) {
		return err
	}
	store.state = next
	if isPersistDurabilityError(err) {
		logger.Errorf("docs index durability failed operation=%s err_type=%T", operation, err)
	}
	return nil
}
func ensurePrivateStoreDir(path string) error {
	if err := appdata.EnsurePrivateDir(path); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("docs index root is not a directory")
	}
	return nil
}
func stableDocsIdentifier(values ...string) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			parts = append(parts, v)
		}
	}
	if len(parts) == 0 {
		return "doc_local"
	}
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return "doc_" + hex.EncodeToString(sum[:])[:24]
}
func firstNonEmptyDocs(values ...string) string {
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}
