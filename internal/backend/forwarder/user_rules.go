package forwarder

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"github.com/google/uuid"

	"cursor/gen/aiserverv1"
	"cursor/internal/appdata"
	"cursor/internal/logger"
)

const (
	sharedUserRuleExtension = ".md"
	maxUserRuleBytes        = 128 << 10
	maxUserRuleCount        = 128
	maxUserRulesTotalBytes  = 1 << 20
	maxUserRulesPromptBytes = 256 << 10
)

type UserRuleRecord struct {
	ID          string
	Title       string
	Filename    string
	FullPath    string
	Knowledge   string
	CreatedAt   string
	IsGenerated bool
	ModifiedAt  time.Time
	ContentHash string
}

type UserRuleStore struct {
	root string
	mu   sync.Mutex
	ops  persistFileOps
}

func NewUserRuleStore(root string) *UserRuleStore {
	return &UserRuleStore{root: strings.TrimSpace(root)}
}

func (store *UserRuleStore) List() ([]UserRuleRecord, error) {
	if store == nil {
		return nil, nil
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	records, err := store.scanRuleFilesLocked()
	if err != nil {
		return nil, err
	}
	sort.SliceStable(records, func(i, j int) bool {
		if records[i].ModifiedAt.Equal(records[j].ModifiedAt) {
			return records[i].Filename < records[j].Filename
		}
		return records[i].ModifiedAt.After(records[j].ModifiedAt)
	})
	return records, nil
}

func (store *UserRuleStore) Add(knowledge string) (UserRuleRecord, error) {
	if store == nil {
		return UserRuleRecord{}, fmt.Errorf("user rule store is nil")
	}
	if err := validateRuleContent(knowledge); err != nil {
		return UserRuleRecord{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	records, err := store.scanRuleFilesLocked()
	if err != nil {
		return UserRuleRecord{}, err
	}
	if len(records) >= maxUserRuleCount {
		return UserRuleRecord{}, fmt.Errorf("user rule count exceeds %d", maxUserRuleCount)
	}
	if ruleContentBytes(records)+len([]byte(knowledge)) > maxUserRulesTotalBytes {
		return UserRuleRecord{}, fmt.Errorf("user rule content exceeds %d bytes", maxUserRulesTotalBytes)
	}
	id := uuid.NewString()
	if err := store.writeRuleLocked(id, knowledge); err != nil {
		if !isPersistDurabilityError(err) {
			return UserRuleRecord{}, err
		}
		logger.Errorf("user rule durability failed operation=add id=%s err_type=%T", id, err)
	}
	return store.loadRuleByIDLocked(id)
}

func (store *UserRuleStore) Update(id, knowledge string) (UserRuleRecord, bool, error) {
	if store == nil {
		return UserRuleRecord{}, false, fmt.Errorf("user rule store is nil")
	}
	if err := validateRuleContent(knowledge); err != nil {
		return UserRuleRecord{}, false, err
	}
	normalizedID, err := normalizeUserRuleID(id)
	if err != nil {
		return UserRuleRecord{}, false, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	current, err := store.loadRuleByIDLocked(normalizedID)
	if errors.Is(err, os.ErrNotExist) {
		return UserRuleRecord{}, false, nil
	}
	if err != nil {
		return UserRuleRecord{}, false, err
	}
	records, err := store.scanRuleFilesLocked()
	if err != nil {
		return UserRuleRecord{}, false, err
	}
	if ruleContentBytes(records)-len([]byte(current.Knowledge))+len([]byte(knowledge)) > maxUserRulesTotalBytes {
		return UserRuleRecord{}, false, fmt.Errorf("user rule content exceeds %d bytes", maxUserRulesTotalBytes)
	}
	if err := store.writeRuleLocked(normalizedID, knowledge); err != nil {
		if !isPersistDurabilityError(err) {
			return UserRuleRecord{}, false, err
		}
		logger.Errorf("user rule durability failed operation=update id=%s err_type=%T", normalizedID, err)
	}
	record, err := store.loadRuleByIDLocked(normalizedID)
	return record, true, err
}

func (store *UserRuleStore) Remove(id string) error {
	if store == nil {
		return nil
	}
	normalizedID, err := normalizeUserRuleID(id)
	if err != nil {
		return err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	path, err := store.safeRulePathLocked(normalizedID)
	if err != nil {
		return err
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("user rule is not a regular file")
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove user rule %q: %w", normalizedID, err)
	}
	if err := store.ops.SyncDir(store.root); err != nil {
		logger.Errorf("user rule durability failed operation=remove id=%s err_type=%T", normalizedID, err)
	}
	return nil
}

func (store *UserRuleStore) BuildSystemPromptSection() (string, int, int, error) {
	records, err := store.List()
	if err != nil {
		return "", 0, 0, err
	}
	if len(records) == 0 {
		return "", 0, 0, nil
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].Filename < records[j].Filename })
	lines := []string{`<shared_user_rules description="These shared local rules are loaded from the backend configuration directory and apply to every local conversation. Follow them when relevant.">`}
	for _, r := range records {
		lines = append(lines, fmt.Sprintf(`<rule file="%s">`, escapeSharedRulePromptText(r.Filename)), escapeSharedRulePromptText(r.Knowledge), "</rule>")
	}
	lines = append(lines, "</shared_user_rules>")
	prompt := strings.Join(lines, "\n")
	if len([]byte(prompt)) > maxUserRulesPromptBytes {
		return "", len(records), 0, fmt.Errorf("user rules prompt exceeds %d bytes", maxUserRulesPromptBytes)
	}
	return prompt, len(records), len(records), nil
}

func (store *UserRuleStore) scanRuleFilesLocked() ([]UserRuleRecord, error) {
	if err := store.ensureRootLocked(); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(store.root)
	if err != nil {
		return nil, fmt.Errorf("read user rules directory: %w", err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	records := make([]UserRuleRecord, 0, len(entries))
	total := 0
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) != sharedUserRuleExtension {
			continue
		}
		path := filepath.Join(store.root, entry.Name())
		record, err := store.readRuleFileLocked(path)
		if err != nil {
			logger.Infof("skip unavailable user rule file name=%q err_type=%T", entry.Name(), err)
			continue
		}
		size := len([]byte(record.Knowledge))
		if len(records) >= maxUserRuleCount || total+size > maxUserRulesTotalBytes {
			logger.Infof("isolate legacy user rule file name=%q reason=aggregate_limit", entry.Name())
			continue
		}
		records = append(records, record)
		total += size
	}
	return records, nil
}

func (store *UserRuleStore) loadRuleByIDLocked(id string) (UserRuleRecord, error) {
	path, err := store.safeRulePathLocked(id)
	if err != nil {
		return UserRuleRecord{}, err
	}
	return store.readRuleFileLocked(path)
}
func (store *UserRuleStore) readRuleFileLocked(path string) (UserRuleRecord, error) {
	if err := store.validateContainedPathLocked(path); err != nil {
		return UserRuleRecord{}, err
	}
	info, err := os.Lstat(path)
	if err != nil {
		return UserRuleRecord{}, err
	}
	if !info.Mode().IsRegular() {
		return UserRuleRecord{}, fmt.Errorf("user rule is not a regular file")
	}
	if err := appdata.EnsurePrivateFile(path); err != nil {
		return UserRuleRecord{}, fmt.Errorf("secure user rule file: %w", err)
	}
	if info.Size() > maxUserRuleBytes {
		return UserRuleRecord{}, fmt.Errorf("user rule exceeds %d bytes", maxUserRuleBytes)
	}
	f, err := os.Open(path)
	if err != nil {
		return UserRuleRecord{}, err
	}
	data, readErr := io.ReadAll(io.LimitReader(f, maxUserRuleBytes+1))
	closeErr := f.Close()
	if readErr != nil {
		return UserRuleRecord{}, readErr
	}
	if closeErr != nil {
		return UserRuleRecord{}, closeErr
	}
	if len(data) > maxUserRuleBytes {
		return UserRuleRecord{}, fmt.Errorf("user rule exceeds %d bytes", maxUserRuleBytes)
	}
	if strings.TrimSpace(string(data)) == "" {
		return UserRuleRecord{}, fmt.Errorf("user rule file is empty")
	}
	filename := filepath.Base(path)
	id, err := normalizeUserRuleID(strings.TrimSuffix(filename, sharedUserRuleExtension))
	if err != nil {
		return UserRuleRecord{}, err
	}
	modified := info.ModTime().UTC()
	return UserRuleRecord{ID: id, Title: id, Filename: filename, FullPath: path, Knowledge: string(data), CreatedAt: modified.Format(time.RFC3339Nano), ModifiedAt: modified, ContentHash: hashSharedUserRuleContent(data)}, nil
}
func (store *UserRuleStore) writeRuleLocked(id, knowledge string) error {
	if err := validateRuleContent(knowledge); err != nil {
		return err
	}
	if err := store.ensureRootLocked(); err != nil {
		return err
	}
	path, err := store.safeRulePathLocked(id)
	if err != nil {
		return err
	}
	if info, statErr := os.Lstat(path); statErr == nil && !info.Mode().IsRegular() {
		return fmt.Errorf("user rule target is not a regular file")
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return statErr
	}
	tmp, err := os.CreateTemp(store.root, ".rule-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	ok := false
	defer func() {
		if !ok {
			_ = tmp.Close()
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := store.ops.EnsurePrivateFile(tmpPath); err != nil {
		return err
	}
	n, err := io.Copy(tmp, io.LimitReader(strings.NewReader(knowledge), maxUserRuleBytes+1))
	if err != nil {
		return err
	}
	if n > maxUserRuleBytes {
		return fmt.Errorf("user rule exceeds %d bytes", maxUserRuleBytes)
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := store.ops.Rename(tmpPath, path); err != nil {
		return err
	}
	ok = true
	var durability error
	if err := store.ops.EnsurePrivateFile(path); err != nil {
		durability = &persistDurabilityError{op: "user_rule_chmod", err: err}
	}
	if err := store.ops.SyncDir(store.root); err != nil && durability == nil {
		durability = &persistDurabilityError{op: "user_rule_dirsync", err: err}
	}
	return durability
}
func (store *UserRuleStore) ensureRootLocked() error {
	if strings.TrimSpace(store.root) == "" {
		return fmt.Errorf("user rules root is empty")
	}
	if err := appdata.EnsurePrivateDir(store.root); err != nil {
		return err
	}
	info, err := os.Lstat(store.root)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("user rules root is not a regular directory")
	}
	return nil
}
func (store *UserRuleStore) safeRulePathLocked(id string) (string, error) {
	normalized, err := normalizeUserRuleID(id)
	if err != nil {
		return "", err
	}
	path := filepath.Join(store.root, normalized+sharedUserRuleExtension)
	if err := store.validateContainedPathLocked(path); err != nil {
		return "", err
	}
	return path, nil
}
func (store *UserRuleStore) validateContainedPathLocked(path string) error {
	root, err := filepath.Abs(store.root)
	if err != nil {
		return err
	}
	candidate, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(root, candidate)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("user rule path escapes root")
	}
	return nil
}
func validateRuleContent(v string) error {
	if strings.TrimSpace(v) == "" {
		return fmt.Errorf("knowledge is required")
	}
	if len([]byte(v)) > maxUserRuleBytes {
		return fmt.Errorf("user rule exceeds %d bytes", maxUserRuleBytes)
	}
	return nil
}
func ruleContentBytes(rs []UserRuleRecord) int {
	n := 0
	for _, r := range rs {
		n += len([]byte(r.Knowledge))
	}
	return n
}
func normalizeUserRuleID(raw string) (string, error) {
	id := strings.TrimSuffix(strings.TrimSpace(raw), sharedUserRuleExtension)
	switch {
	case id == "":
		return "", fmt.Errorf("user rule id is required")
	case strings.Contains(id, "/"), strings.Contains(id, "\\"), strings.Contains(id, ".."):
		return "", fmt.Errorf("invalid user rule id %q", raw)
	default:
		return id, nil
	}
}
func hashSharedUserRuleContent(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
func escapeSharedRulePromptText(value string) string {
	return strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(value)
}

func (service *Service) reconcileRuleProjection(operation, id string) {
	if service == nil || service.docsIndexStore == nil || service.rules == nil {
		return
	}
	records, err := service.rules.List()
	if err == nil {
		err = service.docsIndexStore.ReconcileRuleProjection(records)
	}
	if err != nil {
		service.docsIndexStore.MarkRuleProjectionDirty()
		logger.Errorf("docs index rule projection failed operation=%s id=%s err_type=%T", operation, id, err)
	}
}

func (service *Service) reconcileDirtyRuleProjection() error {
	if service == nil || service.docsIndexStore == nil || service.rules == nil || !service.docsIndexStore.RuleProjectionDirty() {
		return nil
	}
	records, err := service.rules.List()
	if err != nil {
		return err
	}
	return service.docsIndexStore.ReconcileRuleProjection(records)
}

func (service *Service) KnowledgeBaseAdd(_ context.Context, req *connect.Request[aiserverv1.KnowledgeBaseAddRequest]) (*connect.Response[aiserverv1.KnowledgeBaseAddResponse], error) {
	if service == nil || service.rules == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("user rule store is not initialized"))
	}
	if strings.TrimSpace(req.Msg.GetKnowledge()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("knowledge is required"))
	}

	record, err := service.rules.Add(req.Msg.GetKnowledge())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	service.reconcileRuleProjection("add", record.ID)

	return connect.NewResponse(&aiserverv1.KnowledgeBaseAddResponse{
		Success: true,
		Id:      record.ID,
	}), nil
}

func (service *Service) KnowledgeBaseList(_ context.Context, req *connect.Request[aiserverv1.KnowledgeBaseListRequest]) (*connect.Response[aiserverv1.KnowledgeBaseListResponse], error) {
	if service == nil || service.rules == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("user rule store is not initialized"))
	}

	records, err := service.rules.List()
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if limit := req.Msg.GetLimit(); limit > 0 && len(records) > int(limit) {
		records = records[:limit]
	}

	items := make([]*aiserverv1.KnowledgeBaseListResponse_Item, 0, len(records))
	for _, record := range records {
		items = append(items, &aiserverv1.KnowledgeBaseListResponse_Item{
			Id:          record.ID,
			Knowledge:   record.Knowledge,
			Title:       record.Title,
			CreatedAt:   record.CreatedAt,
			IsGenerated: false,
		})
	}
	return connect.NewResponse(&aiserverv1.KnowledgeBaseListResponse{
		Success:    true,
		AllResults: items,
	}), nil
}

func (service *Service) KnowledgeBaseUpdate(_ context.Context, req *connect.Request[aiserverv1.KnowledgeBaseUpdateRequest]) (*connect.Response[aiserverv1.KnowledgeBaseUpdateResponse], error) {
	if service == nil || service.rules == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("user rule store is not initialized"))
	}
	if _, err := normalizeUserRuleID(req.Msg.GetId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if strings.TrimSpace(req.Msg.GetKnowledge()) == "" {
		return nil, connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("knowledge is required"))
	}

	_, exists, err := service.rules.Update(req.Msg.GetId(), req.Msg.GetKnowledge())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !exists {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("user rule %q not found", strings.TrimSpace(req.Msg.GetId())))
	}
	service.reconcileRuleProjection("update", strings.TrimSpace(req.Msg.GetId()))

	return connect.NewResponse(&aiserverv1.KnowledgeBaseUpdateResponse{Success: true}), nil
}

func (service *Service) KnowledgeBaseRemove(_ context.Context, req *connect.Request[aiserverv1.KnowledgeBaseRemoveRequest]) (*connect.Response[aiserverv1.KnowledgeBaseRemoveResponse], error) {
	if service == nil || service.rules == nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("user rule store is not initialized"))
	}
	if _, err := normalizeUserRuleID(req.Msg.GetId()); err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	if err := service.rules.Remove(req.Msg.GetId()); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	service.reconcileRuleProjection("remove", strings.TrimSpace(req.Msg.GetId()))

	return connect.NewResponse(&aiserverv1.KnowledgeBaseRemoveResponse{Success: true}), nil
}
