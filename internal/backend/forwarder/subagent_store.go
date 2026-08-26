// subagent_store.go 实现 SubagentRunStore：版本化持久化 subagent run 状态，
// 存储路径为 historyRoot/_subagents/<run_id>/。
//
// 设计约束：
//   - 文件权限 0600；原子替换（临时文件 + rename + fsync）
//   - checksum 校验防损坏；损坏 run 隔离到 _corrupt/ 不删除
//   - 进程内 per-runID 互斥锁防并发写冲突
//   - 重启扫描只读取并分类，不自行重派未完成 child
package forwarder

import (
	cryptorand "crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// cryptoRandRead is a package-level var so tests can replace it.
var cryptoRandRead = cryptorand.Read

// fallbackRunIDCounter is used when crypto/rand is unavailable.
var fallbackRunIDCounter atomic.Uint64

const (
	subagentStoreDirName   = "_subagents"
	subagentRunFileName    = "run.json"
	subagentResultFileName = "result.json"
	subagentCorruptDirName = "_corrupt"
)

// SubagentRunStore 管理 historyRoot/_subagents/<run_id>/ 下的版本化持久状态。
type SubagentRunStore struct {
	root    string // historyRoot/_subagents/
	locksMu sync.Mutex
	locks   map[string]*subagentRunLock
}

type subagentRunLock struct {
	mu   sync.Mutex
	refs int
}

// NewSubagentRunStore 创建一个以 historyRoot 为根的 SubagentRunStore。
func NewSubagentRunStore(historyRoot string) *SubagentRunStore {
	return &SubagentRunStore{
		root:  filepath.Join(strings.TrimSpace(historyRoot), subagentStoreDirName),
		locks: make(map[string]*subagentRunLock),
	}
}

// GenerateSubagentRunID 返回一个新的稳定 UUID v4，用于 subagent run。
func GenerateSubagentRunID() string {
	return generateUUIDv4()
}

// generateFallbackRunID 在 crypto/rand 不可用时降级生成 ID。
func generateFallbackRunID() string {
	seq := fallbackRunIDCounter.Add(1)
	now := time.Now().UnixNano()
	raw := fmt.Sprintf("fallback:%d:%d", now, seq)
	sum := sha256.Sum256([]byte(raw))
	b := sum[:]
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// runDir 返回指定 run 的目录。
func (s *SubagentRunStore) runDir(runID string) string {
	return filepath.Join(s.root, runID)
}

// runPath 返回 run.json 路径。
func (s *SubagentRunStore) runPath(runID string) string {
	return filepath.Join(s.runDir(runID), subagentRunFileName)
}

// resultPath 返回 result.json 路径。
func (s *SubagentRunStore) resultPath(runID string) string {
	return filepath.Join(s.runDir(runID), subagentResultFileName)
}

// acquireRunLock 获取指定 run 的进程内互斥锁。
func (s *SubagentRunStore) acquireRunLock(runID string) func() {
	s.locksMu.Lock()
	lock := s.locks[runID]
	if lock == nil {
		lock = &subagentRunLock{}
		s.locks[runID] = lock
	}
	lock.refs++
	s.locksMu.Unlock()
	lock.mu.Lock()
	return func() {
		lock.mu.Unlock()
		s.locksMu.Lock()
		lock.refs--
		if lock.refs <= 0 {
			delete(s.locks, runID)
		}
		s.locksMu.Unlock()
	}
}

// CreateRun 原子创建初始 run record。
// 若已存在且身份匹配则幂等返回（支持崩溃重试）。
// 若 Task 尚未派发且持久化失败，必须明确失败。
func (s *SubagentRunStore) CreateRun(identity SubagentIdentity) (*SubagentRunRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("subagent store is nil")
	}
	rawRunID := identity.SubagentRunID
	identity = normalizeSubagentIdentity(identity)
	runID := identity.SubagentRunID
	if rawRunID != runID {
		return nil, fmt.Errorf("invalid subagent_run_id %q", rawRunID)
	}
	if err := validateSubagentRunID(runID); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(s.runDir(runID), 0o700); err != nil {
		return nil, fmt.Errorf("create run directory: %w", err)
	}
	release := s.acquireRunLock(runID)
	defer release()

	// 幂等：已存在且身份完全匹配则返回现有记录；同一 run_id 的不同身份是冲突。
	existing, err := s.loadRunLocked(runID)
	if err == nil && existing != nil {
		if normalizeSubagentIdentity(existing.Identity) != identity {
			return nil, fmt.Errorf("subagent run identity conflict for %s", runID)
		}
		return existing, nil
	}
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	record := &SubagentRunRecord{
		SchemaVersion: subagentContractSchemaVersion,
		Identity:      identity,
		Status:        SubagentRunDispatched,
		Version:       1,
		CreatedAt:     now,
		UpdatedAt:     now,
	}
	record.Checksum = computeRunRecordChecksum(record)
	if err := s.writeRunLocked(runID, record); err != nil {
		return nil, fmt.Errorf("write initial run record: %w", err)
	}
	return record, nil
}

// BindChildIdentity persists child identifiers learned after dispatch. Existing
// non-empty values are immutable; conflicting values are rejected.
func (s *SubagentRunStore) BindChildIdentity(runID string, childConversationID string, agentID string) (*SubagentRunRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("subagent store is nil")
	}
	runID = strings.TrimSpace(runID)
	if err := validateSubagentRunID(runID); err != nil {
		return nil, err
	}
	childConversationID = strings.TrimSpace(childConversationID)
	agentID = strings.TrimSpace(agentID)
	if childConversationID == "" && agentID == "" {
		return s.LoadRun(runID)
	}
	release := s.acquireRunLock(runID)
	defer release()
	record, err := s.loadRunLocked(runID)
	if err != nil || record == nil {
		return record, err
	}
	if record.Identity.ChildConversationID != "" && childConversationID != "" && record.Identity.ChildConversationID != childConversationID {
		return nil, fmt.Errorf("child_conversation_id conflict for run %s", runID)
	}
	if record.Identity.AgentID != "" && agentID != "" && record.Identity.AgentID != agentID {
		return nil, fmt.Errorf("agent_id conflict for run %s", runID)
	}
	changed := false
	if record.Identity.ChildConversationID == "" && childConversationID != "" {
		record.Identity.ChildConversationID = childConversationID
		changed = true
	}
	if record.Identity.AgentID == "" && agentID != "" {
		record.Identity.AgentID = agentID
		changed = true
	}
	if !changed {
		return record, nil
	}
	record.Version++
	record.UpdatedAt = time.Now().UTC()
	record.Checksum = computeRunRecordChecksum(record)
	if err := s.writeRunLocked(runID, record); err != nil {
		return nil, err
	}
	return record, nil
}

// UpdateRunStatus 对 run 状态执行 CAS 更新。
// prevVersion 必须与当前版本匹配；成功时版本号递增。
func (s *SubagentRunStore) UpdateRunStatus(runID string, prevVersion int64, newStatus SubagentRunStatus) (*SubagentRunRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("subagent store is nil")
	}
	release := s.acquireRunLock(runID)
	defer release()
	return s.updateRunStatusLocked(runID, prevVersion, newStatus)
}

// PrepareTerminal 将终态结果写入 result.json 并将 run 状态更新为 terminal_prepared。
// 这是 durable prepare 步骤；必须在任何 parent history 写入前完成。
// 幂等：已是 terminal_prepared 且 parent_commit_key 匹配时直接返回。
func (s *SubagentRunStore) PrepareTerminal(runID string, prevVersion int64, envelope *SubagentResultEnvelope) (*SubagentRunRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("subagent store is nil")
	}
	if envelope == nil {
		return nil, fmt.Errorf("result envelope is required")
	}
	runID = strings.TrimSpace(runID)
	if err := validateSubagentRunID(runID); err != nil {
		return nil, err
	}
	if envelopeRunID := strings.TrimSpace(envelope.SubagentRunID); envelopeRunID != "" && envelopeRunID != runID {
		return nil, fmt.Errorf("result envelope run_id %q does not match %q", envelopeRunID, runID)
	}
	envelope = cloneSubagentResultEnvelope(envelope)
	envelope.SchemaVersion = subagentContractSchemaVersion
	envelope.SubagentRunID = runID
	if strings.TrimSpace(envelope.ParentCommitKey) == "" {
		return nil, fmt.Errorf("parent_commit_key is required for run %s", runID)
	}
	release := s.acquireRunLock(runID)
	defer release()

	record, err := s.loadRunLocked(runID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, fmt.Errorf("run record not found: %s", runID)
	}
	if isTerminalRunStatus(record.Status) {
		if record.ParentCommitKey == envelope.ParentCommitKey {
			return record, nil
		}
		return nil, errSubagentTerminalConflict
	}
	// result.json 先于 run.json 落盘。若上次在两次原子写之间崩溃，首个已持久化
	// 信封仍是胜出终态；修复 run.json，禁止后来的 completion 覆盖它。
	existingEnvelope, resultErr := s.loadResultLocked(runID)
	if resultErr != nil {
		return nil, resultErr
	}
	if existingEnvelope != nil && record.Status != SubagentRunTerminalPrepared {
		if existingEnvelope.ParentCommitKey != envelope.ParentCommitKey {
			return nil, errSubagentTerminalConflict
		}
		record.Status = SubagentRunTerminalPrepared
		record.TerminalCategory = existingEnvelope.TerminalCategory
		terminalAt := existingEnvelope.TerminalAt
		record.TerminalAt = &terminalAt
		record.HandoffState = SubagentHandoffPrepared
		record.ParentCommitKey = existingEnvelope.ParentCommitKey
		record.Version++
		record.UpdatedAt = time.Now().UTC()
		record.Checksum = computeRunRecordChecksum(record)
		if err := s.writeRunLocked(runID, record); err != nil {
			return nil, err
		}
		return record, nil
	}
	// 幂等：已是 terminal_prepared 且 commit key 匹配
	if record.Status == SubagentRunTerminalPrepared && record.ParentCommitKey == envelope.ParentCommitKey {
		return record, nil
	}
	// 终态冲突：已是 terminal_prepared 但 commit key 不同 → first-write-wins
	if record.Status == SubagentRunTerminalPrepared && record.ParentCommitKey != envelope.ParentCommitKey {
		log.Printf("subagent_store terminal_conflict run_id=%s existing_category=%s new_category=%s existing_digest=%.12s",
			runID, record.TerminalCategory, envelope.TerminalCategory, envelope.ResultDigest)
		return nil, errSubagentTerminalConflict
	}
	// 版本冲突检查（崩溃重试时允许 terminal_prepared 重入）
	if record.Version != prevVersion && record.Status != SubagentRunTerminalPrepared {
		return nil, fmt.Errorf("version conflict during prepare: expected %d got %d for run %s", prevVersion, record.Version, runID)
	}
	// 截断超限字段
	envelope = truncateResultEnvelope(envelope)
	envelope.Checksum = computeResultEnvelopeChecksum(envelope)
	// 先写 result.json（durable prepare）
	if err := s.writeResultLocked(runID, envelope); err != nil {
		return nil, fmt.Errorf("write result envelope: %w", err)
	}
	// 更新 run record 状态
	now := time.Now().UTC()
	record.Status = SubagentRunTerminalPrepared
	record.TerminalCategory = envelope.TerminalCategory
	terminalAt := envelope.TerminalAt
	record.TerminalAt = &terminalAt
	record.HandoffState = SubagentHandoffPrepared
	record.ParentCommitKey = envelope.ParentCommitKey
	record.Version++
	record.UpdatedAt = now
	record.Checksum = computeRunRecordChecksum(record)
	if err := s.writeRunLocked(runID, record); err != nil {
		return nil, err
	}
	return record, nil
}

// MarkParentCommitted 从 terminal_prepared 转为 parent_committed。
// 必须在 parent AppendEntriesWithUpdate 成功后调用。幂等。
func (s *SubagentRunStore) MarkParentCommitted(runID string) (*SubagentRunRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("subagent store is nil")
	}
	release := s.acquireRunLock(runID)
	defer release()

	record, err := s.loadRunLocked(runID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, fmt.Errorf("run record not found: %s", runID)
	}
	// 幂等
	if record.Status == SubagentRunParentCommitted || record.Status == SubagentRunAcknowledged {
		return record, nil
	}
	if record.Status != SubagentRunTerminalPrepared && record.Status != SubagentRunAwaitingParentResume {
		return nil, fmt.Errorf("unexpected status for mark_parent_committed: %s (run_id=%s)", record.Status, runID)
	}
	record.Status = SubagentRunParentCommitted
	record.HandoffState = SubagentHandoffParentCommitted
	record.Version++
	record.UpdatedAt = time.Now().UTC()
	record.Checksum = computeRunRecordChecksum(record)
	if err := s.writeRunLocked(runID, record); err != nil {
		return nil, err
	}
	return record, nil
}

// MarkAcknowledged 转为 acknowledged（checkpoint + publish 完成）。幂等。
// 合法前置状态：parent_committed。禁止从 dispatched/running/terminal_prepared 直接 ack。
func (s *SubagentRunStore) MarkAcknowledged(runID string) (*SubagentRunRecord, error) {
	if s == nil {
		return nil, fmt.Errorf("subagent store is nil")
	}
	release := s.acquireRunLock(runID)
	defer release()

	record, err := s.loadRunLocked(runID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, fmt.Errorf("run record not found: %s", runID)
	}
	if record.Status == SubagentRunAcknowledged {
		return record, nil
	}
	// 只允许从 parent_committed 转为 acknowledged；其他状态属于非法跳跃。
	if record.Status != SubagentRunParentCommitted {
		return nil, fmt.Errorf("unexpected status for mark_acknowledged: %s (run_id=%s); must be parent_committed", record.Status, runID)
	}
	record.Status = SubagentRunAcknowledged
	record.HandoffState = SubagentHandoffAcknowledged
	record.Version++
	record.UpdatedAt = time.Now().UTC()
	record.Checksum = computeRunRecordChecksum(record)
	if err := s.writeRunLocked(runID, record); err != nil {
		return nil, err
	}
	return record, nil
}

// LoadRun 加载 run record（未找到返回 nil）。
func (s *SubagentRunStore) LoadRun(runID string) (*SubagentRunRecord, error) {
	if s == nil {
		return nil, nil
	}
	release := s.acquireRunLock(runID)
	defer release()
	return s.loadRunLocked(runID)
}

// LoadResult 加载 result envelope（未找到或尚未 prepare 返回 nil）。
func (s *SubagentRunStore) LoadResult(runID string) (*SubagentResultEnvelope, error) {
	if s == nil {
		return nil, nil
	}
	release := s.acquireRunLock(runID)
	defer release()
	return s.loadResultLocked(runID)
}

// ScanRecovery 扫描所有 runs，并在单写者约束下完成必要的恢复分类状态更新：
//   - dispatched/running/backgrounded → 转 awaiting_client_resume（禁止重派）
//   - terminal_prepared/awaiting_parent_resume → 返回等待 parent commit 重试
//   - parent_committed/acknowledged/awaiting_client_resume → 无操作
//   - 损坏或版本不兼容 → 隔离到 _corrupt/，生成结构化日志
//
// 每个 run 在单次锁内完成 load/validate/update，消除 TOCTOU。
func (s *SubagentRunStore) ScanRecovery() ([]*SubagentRunRecord, error) {
	if s == nil {
		return nil, nil
	}
	entries, err := os.ReadDir(s.root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scan subagent store: %w", err)
	}
	var recoverable []*SubagentRunRecord
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		runID := entry.Name()
		if strings.HasPrefix(runID, "_") {
			continue // 跳过 _corrupt 等系统目录
		}
		record, needIsolate, isolateErr := s.scanProcessRun(runID)
		if needIsolate {
			log.Printf("subagent_store corrupt_run run_id=%s err=%v", runID, isolateErr)
			s.isolateCorruptRun(runID, isolateErr)
			continue
		}
		if record != nil {
			recoverable = append(recoverable, record)
		}
	}
	return recoverable, nil
}

// scanProcessRun 在单次锁内完成 load/validate/transition，消除 TOCTOU。
// 返回 (record, needIsolate, isolateErr)：
//   - record != nil 表示该 run 需要 parent commit 重试（terminal_prepared/awaiting_parent_resume）
//   - needIsolate = true 表示文件损坏需隔离
func (s *SubagentRunStore) scanProcessRun(runID string) (record *SubagentRunRecord, needIsolate bool, isolateErr error) {
	release := s.acquireRunLock(runID)
	defer release()

	rec, err := s.loadRunLocked(runID)
	if err != nil {
		return nil, true, err
	}
	if rec == nil {
		return nil, false, nil
	}
	// 校验 checksum（与 loadAndValidateRun 逻辑对齐）
	if rec.Checksum != "" {
		expected := computeRunRecordChecksum(rec)
		if rec.Checksum != expected {
			return nil, true, fmt.Errorf("checksum mismatch: stored=%s computed=%s", rec.Checksum, expected)
		}
	}
	if !isTerminalRunStatus(rec.Status) && rec.Status != SubagentRunTerminalPrepared && rec.Status != SubagentRunAwaitingParentResume {
		envelope, resultErr := s.loadResultLocked(runID)
		if resultErr != nil {
			return nil, true, resultErr
		}
		if envelope != nil {
			rec.Status = SubagentRunTerminalPrepared
			rec.TerminalCategory = envelope.TerminalCategory
			terminalAt := envelope.TerminalAt
			rec.TerminalAt = &terminalAt
			rec.HandoffState = SubagentHandoffPrepared
			rec.ParentCommitKey = envelope.ParentCommitKey
			rec.Version++
			rec.UpdatedAt = time.Now().UTC()
			rec.Checksum = computeRunRecordChecksum(rec)
			if writeErr := s.writeRunLocked(runID, rec); writeErr != nil {
				return nil, false, writeErr
			}
			return rec, false, nil
		}
	}
	switch rec.Status {
	case SubagentRunDispatched, SubagentRunRunning, SubagentRunBackgrounded:
		// 在同一把锁内完成状态转移，消除 TOCTOU
		updated, updateErr := s.updateRunStatusLocked(runID, rec.Version, SubagentRunAwaitingClientResume)
		if updateErr != nil {
			log.Printf("subagent_store recovery_transition_failed run_id=%s err=%v", runID, updateErr)
		} else if updated != nil {
			log.Printf("subagent_store recovery_awaiting_resume run_id=%s prev_status=%s", runID, rec.Status)
		}
		return nil, false, nil
	case SubagentRunTerminalPrepared, SubagentRunAwaitingParentResume:
		// 需要 parent commit 重试
		return rec, false, nil
	case SubagentRunParentCommitted, SubagentRunAcknowledged, SubagentRunAwaitingClientResume:
		// 无需操作
		return nil, false, nil
	default:
		return nil, true, fmt.Errorf("unsupported subagent run status %q for %s", rec.Status, runID)
	}
}

// ──────────────────────────────────────────────────────────────────────────────
// 内部实现
// ──────────────────────────────────────────────────────────────────────────────

func (s *SubagentRunStore) loadAndValidateRun(runID string) (*SubagentRunRecord, error) {
	release := s.acquireRunLock(runID)
	defer release()
	record, err := s.loadRunLocked(runID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	// 校验 checksum
	if record.Checksum != "" {
		expected := computeRunRecordChecksum(record)
		if record.Checksum != expected {
			return nil, fmt.Errorf("checksum mismatch: stored=%s computed=%s", record.Checksum, expected)
		}
	}
	return record, nil
}

func (s *SubagentRunStore) isolateCorruptRun(runID string, reason error) {
	corruptParent := filepath.Join(s.root, subagentCorruptDirName)
	if err := os.MkdirAll(corruptParent, 0o700); err != nil {
		log.Printf("subagent_store corrupt_isolate_mkdir_failed run_id=%s err=%v", runID, err)
		return
	}
	corruptDest := filepath.Join(corruptParent, runID)
	if err := os.Rename(s.runDir(runID), corruptDest); err != nil {
		log.Printf("subagent_store corrupt_isolate_rename_failed run_id=%s dest=%s err=%v", runID, corruptDest, err)
		return
	}
	log.Printf("subagent_store corrupt_isolated run_id=%s reason=%v", runID, reason)
}

func (s *SubagentRunStore) loadRunLocked(runID string) (*SubagentRunRecord, error) {
	data, err := os.ReadFile(s.runPath(runID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read run record: %w", err)
	}
	var record SubagentRunRecord
	if err := json.Unmarshal(data, &record); err != nil {
		return nil, fmt.Errorf("decode run record %s: %w", runID, err)
	}
	if record.SchemaVersion != subagentContractSchemaVersion {
		return nil, fmt.Errorf("unsupported run record schema_version %d for %s", record.SchemaVersion, runID)
	}
	if strings.TrimSpace(record.Identity.SubagentRunID) != runID {
		return nil, fmt.Errorf("run record identity mismatch: path=%s record=%s", runID, strings.TrimSpace(record.Identity.SubagentRunID))
	}
	if strings.TrimSpace(record.Checksum) == "" {
		return nil, fmt.Errorf("run record checksum missing for %s", runID)
	}
	if expected := computeRunRecordChecksum(&record); record.Checksum != expected {
		return nil, fmt.Errorf("checksum mismatch: stored=%s computed=%s", record.Checksum, expected)
	}
	return &record, nil
}

func (s *SubagentRunStore) loadResultLocked(runID string) (*SubagentResultEnvelope, error) {
	data, err := os.ReadFile(s.resultPath(runID))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read result envelope: %w", err)
	}
	var envelope SubagentResultEnvelope
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, fmt.Errorf("decode result envelope %s: %w", runID, err)
	}
	if envelope.SchemaVersion != subagentContractSchemaVersion {
		return nil, fmt.Errorf("unsupported result envelope schema_version %d for %s", envelope.SchemaVersion, runID)
	}
	if strings.TrimSpace(envelope.SubagentRunID) != runID {
		return nil, fmt.Errorf("result envelope run_id mismatch: path=%s envelope=%s", runID, strings.TrimSpace(envelope.SubagentRunID))
	}
	if strings.TrimSpace(envelope.Checksum) == "" {
		return nil, fmt.Errorf("result envelope checksum missing for %s", runID)
	}
	// 校验 checksum
	expected := computeResultEnvelopeChecksum(&envelope)
	if envelope.Checksum != expected {
		return nil, fmt.Errorf("result envelope checksum mismatch for %s: stored=%s computed=%s", runID, envelope.Checksum, expected)
	}
	return &envelope, nil
}

func (s *SubagentRunStore) writeRunLocked(runID string, record *SubagentRunRecord) error {
	if err := os.MkdirAll(s.runDir(runID), 0o700); err != nil {
		return fmt.Errorf("mkdir run dir: %w", err)
	}
	return writeSubagentJSONAtomic(s.runPath(runID), record, 0o600)
}

func (s *SubagentRunStore) writeResultLocked(runID string, envelope *SubagentResultEnvelope) error {
	if err := os.MkdirAll(s.runDir(runID), 0o700); err != nil {
		return fmt.Errorf("mkdir run dir: %w", err)
	}
	return writeSubagentJSONAtomic(s.resultPath(runID), envelope, 0o600)
}

func (s *SubagentRunStore) updateRunStatusLocked(runID string, prevVersion int64, newStatus SubagentRunStatus) (*SubagentRunRecord, error) {
	record, err := s.loadRunLocked(runID)
	if err != nil {
		return nil, err
	}
	if record == nil {
		return nil, nil
	}
	if record.Version != prevVersion {
		return nil, fmt.Errorf("version conflict: expected %d got %d for run %s", prevVersion, record.Version, runID)
	}
	switch newStatus {
	case SubagentRunAwaitingParentResume:
		if record.Status != SubagentRunTerminalPrepared && record.Status != SubagentRunAwaitingParentResume {
			return nil, fmt.Errorf("invalid transition %s -> %s for run %s", record.Status, newStatus, runID)
		}
		record.HandoffState = SubagentHandoffAwaitingResume
	case SubagentRunAwaitingClientResume:
		if record.Status != SubagentRunDispatched && record.Status != SubagentRunRunning && record.Status != SubagentRunBackgrounded && record.Status != SubagentRunAwaitingClientResume {
			return nil, fmt.Errorf("invalid transition %s -> %s for run %s", record.Status, newStatus, runID)
		}
	}
	record.Status = newStatus
	record.Version++
	record.UpdatedAt = time.Now().UTC()
	record.Checksum = computeRunRecordChecksum(record)
	if err := s.writeRunLocked(runID, record); err != nil {
		return nil, err
	}
	return record, nil
}

// ──────────────────────────────────────────────────────────────────────────────
// 工具函数
// ──────────────────────────────────────────────────────────────────────────────

func normalizeSubagentIdentity(identity SubagentIdentity) SubagentIdentity {
	identity.SubagentRunID = strings.TrimSpace(identity.SubagentRunID)
	identity.ParentConversationID = strings.TrimSpace(identity.ParentConversationID)
	identity.RootConversationID = strings.TrimSpace(identity.RootConversationID)
	identity.ParentToolCallID = strings.TrimSpace(identity.ParentToolCallID)
	identity.ParentRequestID = strings.TrimSpace(identity.ParentRequestID)
	identity.ParentModelCallID = strings.TrimSpace(identity.ParentModelCallID)
	identity.ChildConversationID = strings.TrimSpace(identity.ChildConversationID)
	identity.AgentID = strings.TrimSpace(identity.AgentID)
	identity.SubagentType = strings.TrimSpace(identity.SubagentType)
	identity.ModelID = strings.TrimSpace(identity.ModelID)
	return identity
}

func validateSubagentRunID(runID string) error {
	if runID == "" {
		return fmt.Errorf("subagent_run_id is required")
	}
	if len(runID) > 128 || runID == "." || runID == ".." {
		return fmt.Errorf("invalid subagent_run_id %q", runID)
	}
	for _, char := range runID {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return fmt.Errorf("invalid subagent_run_id %q", runID)
	}
	return nil
}

func cloneSubagentResultEnvelope(envelope *SubagentResultEnvelope) *SubagentResultEnvelope {
	if envelope == nil {
		return nil
	}
	clone := *envelope
	clone.ToolCallEncoded = append([]byte(nil), envelope.ToolCallEncoded...)
	clone.ArgsJSON = append([]byte(nil), envelope.ArgsJSON...)
	return &clone
}

// writeSubagentJSONAtomic 以指定权限原子写入 JSON 文件（temp + fsync + rename）。
func writeSubagentJSONAtomic(path string, payload any, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create parent directory: %w", err)
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal json: %w", err)
	}
	dir := filepath.Dir(path)
	tmpFile, err := os.CreateTemp(dir, ".tmp-subagent-")
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	renamed := false
	defer func() {
		if !renamed {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmpFile.Chmod(perm); err != nil {
		return errors.Join(fmt.Errorf("chmod temp file: %w", err), closeSubagentTempFile(tmpFile))
	}
	if _, err := tmpFile.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write temp file: %w", err), closeSubagentTempFile(tmpFile))
	}
	if err := tmpFile.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync temp file: %w", err), closeSubagentTempFile(tmpFile))
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("rename temp file: %w", err)
	}
	renamed = true
	// fsync 父目录以确保 rename 在支持目录同步的平台上持久化。
	// Windows 不支持对目录句柄执行 Sync；syncDirectory 会按平台跳过。
	if err := syncDirectory(dir); err != nil {
		return fmt.Errorf("fsync parent dir: %w", err)
	}
	return nil
}

func closeSubagentTempFile(file *os.File) error {
	if file == nil {
		return nil
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close temp file: %w", err)
	}
	return nil
}

// computeRunRecordChecksum 返回记录（Checksum 字段置零）的 sha256 十六进制。
func computeRunRecordChecksum(record *SubagentRunRecord) string {
	if record == nil {
		return ""
	}
	c := *record
	c.Checksum = ""
	data, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// computeResultEnvelopeChecksum 返回信封（Checksum 字段置零）的 sha256 十六进制。
func computeResultEnvelopeChecksum(envelope *SubagentResultEnvelope) string {
	if envelope == nil {
		return ""
	}
	c := *envelope
	c.Checksum = ""
	data, err := json.Marshal(c)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// truncateResultEnvelope 确保信封内各字段不超过大小上限。
func truncateResultEnvelope(envelope *SubagentResultEnvelope) *SubagentResultEnvelope {
	if envelope == nil {
		return nil
	}
	result := *envelope
	if len(result.ToolResultPayload) > subagentResultPayloadLimit {
		result.ToolResultPayload = result.ToolResultPayload[:subagentResultPayloadLimit]
	}
	if len(result.ToolCallEncoded) > subagentResultToolCallLimit {
		result.ToolCallEncoded = nil // 超限则丢弃，parent 只写文本
	}
	if len(result.ResultSummary) > subagentResultSummaryLimit {
		result.ResultSummary = result.ResultSummary[:subagentResultSummaryLimit]
	}
	return &result
}

// generateUUIDv4 生成一个随机 UUID v4 字符串。
func generateUUIDv4() string {
	// 使用 crypto/rand 生成 16 字节随机数
	var b [16]byte
	if _, err := cryptoRandRead(b[:]); err != nil {
		// 降级：使用时间戳 + 计数器
		return generateFallbackRunID()
	}
	// Set version 4 and variant bits
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%08x-%04x-%04x-%04x-%012x",
		b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
