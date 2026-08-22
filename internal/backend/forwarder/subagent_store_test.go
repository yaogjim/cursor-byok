// subagent_store_test.go — SubagentRunStore 的表驱动单元测试。
// 覆盖：CreateRun（幂等/必填校验）、PrepareTerminal（幂等/版本冲突）、
// MarkParentCommitted（幂等/状态校验）、MarkAcknowledged（幂等）、
// checksum 损坏检测、损坏隔离、ScanRecovery 分类、
// TruncateResultEnvelope 大小上限和 UUID 唯一性。
package forwarder

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// ─────────────────────────────────────────────
// CreateRun
// ─────────────────────────────────────────────

func TestSubagentRunStore_CreateRun_Basic(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{
		SubagentRunID:        "run-1",
		ParentConversationID: "conv-parent",
		ParentToolCallID:     "tc-1",
	}
	rec, err := store.CreateRun(id)
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if rec.Identity.SubagentRunID != "run-1" {
		t.Fatalf("run_id = %q, want %q", rec.Identity.SubagentRunID, "run-1")
	}
	if rec.Status != SubagentRunDispatched {
		t.Fatalf("status = %q, want dispatched", rec.Status)
	}
	if rec.Version != 1 {
		t.Fatalf("version = %d, want 1", rec.Version)
	}
	if rec.SchemaVersion != subagentContractSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", rec.SchemaVersion, subagentContractSchemaVersion)
	}
}

func TestSubagentRunStore_CreateRun_Idempotent(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-idem", ParentConversationID: "conv-1"}
	_, err := store.CreateRun(id)
	if err != nil {
		t.Fatalf("first CreateRun() error = %v", err)
	}
	rec2, err := store.CreateRun(id)
	if err != nil {
		t.Fatalf("second CreateRun() error = %v", err)
	}
	if rec2.Version != 1 {
		t.Fatalf("idempotent CreateRun changed version: got %d want 1", rec2.Version)
	}
}

func TestSubagentRunStore_CreateRun_RejectsIdentityConflictAndUnsafeID(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	if _, err := store.CreateRun(SubagentIdentity{SubagentRunID: "run-conflict", ParentConversationID: "parent-a"}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	if _, err := store.CreateRun(SubagentIdentity{SubagentRunID: "run-conflict", ParentConversationID: "parent-b"}); err == nil {
		t.Fatal("same run_id with a different identity should fail")
	}
	for _, runID := range []string{"../escape", "nested/run", "..", " leading"} {
		if _, err := store.CreateRun(SubagentIdentity{SubagentRunID: runID}); err == nil {
			t.Fatalf("unsafe run_id %q should fail", runID)
		}
	}
}

func TestSubagentRunStore_BindChildIdentity_IsIdempotentAndImmutable(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	if _, err := store.CreateRun(SubagentIdentity{SubagentRunID: "run-bind"}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	bound, err := store.BindChildIdentity("run-bind", "child-1", "agent-1")
	if err != nil {
		t.Fatalf("BindChildIdentity() error = %v", err)
	}
	if bound.Identity.ChildConversationID != "child-1" || bound.Identity.AgentID != "agent-1" {
		t.Fatalf("bound identity = %+v", bound.Identity)
	}
	repeated, err := store.BindChildIdentity("run-bind", "child-1", "agent-1")
	if err != nil {
		t.Fatalf("idempotent BindChildIdentity() error = %v", err)
	}
	if repeated.Version != bound.Version {
		t.Fatalf("idempotent bind changed version: %d -> %d", bound.Version, repeated.Version)
	}
	if _, err := store.BindChildIdentity("run-bind", "child-2", "agent-1"); err == nil {
		t.Fatal("conflicting child conversation should fail")
	}
	if _, err := store.BindChildIdentity("run-bind", "child-1", "agent-2"); err == nil {
		t.Fatal("conflicting agent should fail")
	}
}

func TestSubagentRunStore_CreateRun_RequiresRunID(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	_, err := store.CreateRun(SubagentIdentity{SubagentRunID: ""})
	if err == nil {
		t.Fatal("expected error for empty run_id, got nil")
	}
}

func TestSubagentRunStore_CreateRun_NilStore(t *testing.T) {
	var store *SubagentRunStore
	_, err := store.CreateRun(SubagentIdentity{SubagentRunID: "x"})
	if err == nil {
		t.Fatal("nil store should return error")
	}
}

// ─────────────────────────────────────────────
// PrepareTerminal
// ─────────────────────────────────────────────

func TestSubagentRunStore_PrepareTerminal_Basic(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-pt", ParentConversationID: "conv-1"}
	rec, _ := store.CreateRun(id)

	now := time.Now().UTC()
	env := &SubagentResultEnvelope{
		SchemaVersion:     subagentContractSchemaVersion,
		SubagentRunID:     "run-pt",
		TerminalCategory:  SubagentTerminalSucceeded,
		TerminalAt:        now,
		ResultDigest:      "digest-1",
		ToolResultPayload: "result text",
		ParentCommitKey:   "commit-key-1",
	}
	updated, err := store.PrepareTerminal("run-pt", rec.Version, env)
	if err != nil {
		t.Fatalf("PrepareTerminal() error = %v", err)
	}
	if updated.Status != SubagentRunTerminalPrepared {
		t.Fatalf("status = %q, want terminal_prepared", updated.Status)
	}
	if updated.HandoffState != SubagentHandoffPrepared {
		t.Fatalf("handoff_state = %q, want prepared", updated.HandoffState)
	}
	if updated.ParentCommitKey != "commit-key-1" {
		t.Fatalf("parent_commit_key = %q", updated.ParentCommitKey)
	}
	if updated.TerminalCategory != SubagentTerminalSucceeded {
		t.Fatalf("terminal_category = %q", updated.TerminalCategory)
	}

	// result.json should exist
	resultLoaded, err := store.LoadResult("run-pt")
	if err != nil {
		t.Fatalf("LoadResult() error = %v", err)
	}
	if resultLoaded == nil {
		t.Fatal("result.json not written")
	}
	if resultLoaded.ToolResultPayload != "result text" {
		t.Fatalf("result payload = %q", resultLoaded.ToolResultPayload)
	}
}

func TestSubagentRunStore_PrepareTerminal_Idempotent(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-pt-idem"}
	rec, _ := store.CreateRun(id)
	now := time.Now().UTC()
	env := &SubagentResultEnvelope{
		SubagentRunID:    "run-pt-idem",
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       now,
		ParentCommitKey:  "ck-idem",
	}
	first, err := store.PrepareTerminal("run-pt-idem", rec.Version, env)
	if err != nil {
		t.Fatalf("first PrepareTerminal() error = %v", err)
	}
	// second call with same commit key should be idempotent
	second, err := store.PrepareTerminal("run-pt-idem", first.Version, env)
	if err != nil {
		t.Fatalf("second PrepareTerminal() error = %v", err)
	}
	if second.Status != SubagentRunTerminalPrepared {
		t.Fatalf("idempotent PrepareTerminal: status = %q", second.Status)
	}
}

func TestSubagentRunStore_PrepareTerminal_VersionConflict(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-vc"}
	_, _ = store.CreateRun(id)
	env := &SubagentResultEnvelope{
		SubagentRunID:    "run-vc",
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "ck-vc",
	}
	// stale version → conflict
	_, err := store.PrepareTerminal("run-vc", 999, env)
	if err == nil {
		t.Fatal("expected version conflict error, got nil")
	}
}

func TestSubagentRunStore_PrepareTerminal_NilEnvelope(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-nil-env"}
	rec, _ := store.CreateRun(id)
	_, err := store.PrepareTerminal("run-nil-env", rec.Version, nil)
	if err == nil {
		t.Fatal("nil envelope should return error")
	}
}

// ─────────────────────────────────────────────
// MarkParentCommitted
// ─────────────────────────────────────────────

func TestSubagentRunStore_MarkParentCommitted_Basic(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-mpc"}
	rec, _ := store.CreateRun(id)
	env := &SubagentResultEnvelope{
		SubagentRunID:    "run-mpc",
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "ck-mpc",
	}
	after, _ := store.PrepareTerminal("run-mpc", rec.Version, env)
	_ = after

	committed, err := store.MarkParentCommitted("run-mpc")
	if err != nil {
		t.Fatalf("MarkParentCommitted() error = %v", err)
	}
	if committed.Status != SubagentRunParentCommitted {
		t.Fatalf("status = %q, want parent_committed", committed.Status)
	}
	if committed.HandoffState != SubagentHandoffParentCommitted {
		t.Fatalf("handoff_state = %q", committed.HandoffState)
	}
}

func TestSubagentRunStore_MarkParentCommitted_Idempotent(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-mpc-idem"}
	rec, _ := store.CreateRun(id)
	env := &SubagentResultEnvelope{
		SubagentRunID:    "run-mpc-idem",
		TerminalCategory: SubagentTerminalCanceled,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "ck-mpc-idem",
	}
	_, _ = store.PrepareTerminal("run-mpc-idem", rec.Version, env)
	first, _ := store.MarkParentCommitted("run-mpc-idem")
	// second call should be idempotent
	second, err := store.MarkParentCommitted("run-mpc-idem")
	if err != nil {
		t.Fatalf("idempotent MarkParentCommitted() error = %v", err)
	}
	if second.Version != first.Version {
		t.Fatalf("version changed on idempotent call: %d → %d", first.Version, second.Version)
	}
}

func TestSubagentRunStore_MarkParentCommitted_WrongStatus(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-mpc-ws"}
	_, _ = store.CreateRun(id)
	// run is still 'dispatched', not 'terminal_prepared'
	_, err := store.MarkParentCommitted("run-mpc-ws")
	if err == nil {
		t.Fatal("expected error for wrong status, got nil")
	}
}

// ─────────────────────────────────────────────
// MarkAcknowledged
// ─────────────────────────────────────────────

func TestSubagentRunStore_MarkAcknowledged_Basic(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-ack"}
	rec, _ := store.CreateRun(id)
	env := &SubagentResultEnvelope{
		SubagentRunID:    "run-ack",
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "ck-ack",
	}
	_, _ = store.PrepareTerminal("run-ack", rec.Version, env)
	_, _ = store.MarkParentCommitted("run-ack")

	acked, err := store.MarkAcknowledged("run-ack")
	if err != nil {
		t.Fatalf("MarkAcknowledged() error = %v", err)
	}
	if acked.Status != SubagentRunAcknowledged {
		t.Fatalf("status = %q, want acknowledged", acked.Status)
	}
}

func TestSubagentRunStore_MarkAcknowledged_Idempotent(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-ack-idem"}
	rec, _ := store.CreateRun(id)
	env := &SubagentResultEnvelope{
		SubagentRunID:    "run-ack-idem",
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "ck-ack-idem",
	}
	_, _ = store.PrepareTerminal("run-ack-idem", rec.Version, env)
	_, _ = store.MarkParentCommitted("run-ack-idem")
	first, _ := store.MarkAcknowledged("run-ack-idem")
	second, err := store.MarkAcknowledged("run-ack-idem")
	if err != nil {
		t.Fatalf("idempotent MarkAcknowledged() error = %v", err)
	}
	if second.Version != first.Version {
		t.Fatalf("version changed on idempotent call: %d → %d", first.Version, second.Version)
	}
}

// ─────────────────────────────────────────────
// isTerminalRunStatus
// ─────────────────────────────────────────────

func TestIsTerminalRunStatus(t *testing.T) {
	cases := []struct {
		status   SubagentRunStatus
		terminal bool
	}{
		{SubagentRunParentCommitted, true},
		{SubagentRunAcknowledged, true},
		{SubagentRunDispatched, false},
		{SubagentRunRunning, false},
		{SubagentRunTerminalPrepared, false},
		{SubagentRunAwaitingClientResume, false},
		{SubagentRunAwaitingParentResume, false},
	}
	for _, tc := range cases {
		got := isTerminalRunStatus(tc.status)
		if got != tc.terminal {
			t.Errorf("isTerminalRunStatus(%q) = %v, want %v", tc.status, got, tc.terminal)
		}
	}
}

// ─────────────────────────────────────────────
// Checksum 损坏检测
// ─────────────────────────────────────────────

func TestSubagentRunStore_ChecksumValidation_DetectsCorruption(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-checksum"}
	rec, err := store.CreateRun(id)
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	// Verify checksum is stored
	if rec.Checksum == "" {
		t.Fatal("run record has no checksum")
	}

	// 直接篡改 run.json 的 status 字段
	runPath := filepath.Join(store.root, "run-checksum", subagentRunFileName)
	data, err := os.ReadFile(runPath)
	if err != nil {
		t.Fatalf("read run.json: %v", err)
	}
	corrupted := strings.Replace(string(data), `"dispatched"`, `"running"`, 1)
	if err := os.WriteFile(runPath, []byte(corrupted), 0o600); err != nil {
		t.Fatalf("write corrupted run.json: %v", err)
	}

	// loadAndValidateRun should detect the corruption
	_, validateErr := store.loadAndValidateRun("run-checksum")
	if validateErr == nil {
		t.Fatal("expected checksum mismatch error, got nil")
	}
	if !strings.Contains(validateErr.Error(), "checksum") {
		t.Fatalf("expected 'checksum' in error, got: %v", validateErr)
	}
}

func TestSubagentRunStore_ResultChecksum_DetectsCorruption(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-res-checksum"}
	rec, _ := store.CreateRun(id)
	env := &SubagentResultEnvelope{
		SubagentRunID:    "run-res-checksum",
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "ck-res",
	}
	_, err := store.PrepareTerminal("run-res-checksum", rec.Version, env)
	if err != nil {
		t.Fatalf("PrepareTerminal() error = %v", err)
	}

	// 篡改 result.json 的 terminal_category 字段
	resultPath := filepath.Join(store.root, "run-res-checksum", subagentResultFileName)
	data, err := os.ReadFile(resultPath)
	if err != nil {
		t.Fatalf("read result.json: %v", err)
	}
	corrupted := strings.Replace(string(data), `"succeeded"`, `"canceled"`, 1)
	if err := os.WriteFile(resultPath, []byte(corrupted), 0o600); err != nil {
		t.Fatalf("write corrupted result.json: %v", err)
	}

	_, loadErr := store.LoadResult("run-res-checksum")
	if loadErr == nil {
		t.Fatal("expected checksum mismatch error for result, got nil")
	}
}

// ─────────────────────────────────────────────
// 损坏 run 隔离（_corrupt/）
// ─────────────────────────────────────────────

func TestSubagentRunStore_CorruptRunIsolation(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())

	// 手动创建一个 run 目录，内含无效 JSON
	runDir := filepath.Join(store.root, "run-corrupt")
	if err := os.MkdirAll(runDir, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(runDir, subagentRunFileName), []byte("NOT JSON"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	_, err := store.ScanRecovery()
	if err != nil {
		t.Fatalf("ScanRecovery() error = %v", err)
	}

	// 损坏的 run 应被移到 _corrupt/
	corruptPath := filepath.Join(store.root, subagentCorruptDirName, "run-corrupt")
	if _, statErr := os.Stat(corruptPath); os.IsNotExist(statErr) {
		t.Fatalf("corrupt run not isolated to _corrupt/: %s", corruptPath)
	}
	// 原目录应消失
	if _, statErr := os.Stat(runDir); !os.IsNotExist(statErr) {
		t.Fatalf("original corrupt run dir still exists after isolation")
	}
}

func TestSubagentRunStore_UnknownStatusIsolation(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	record, err := store.CreateRun(SubagentIdentity{SubagentRunID: "run-unknown-status"})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	record.Status = SubagentRunStatus("future_unknown_status")
	record.Checksum = computeRunRecordChecksum(record)
	if err := store.writeRunLocked("run-unknown-status", record); err != nil {
		t.Fatalf("write unknown status run: %v", err)
	}

	if _, err := store.ScanRecovery(); err != nil {
		t.Fatalf("ScanRecovery() error = %v", err)
	}
	corruptPath := filepath.Join(store.root, subagentCorruptDirName, "run-unknown-status")
	if _, err := os.Stat(corruptPath); err != nil {
		t.Fatalf("unknown status run not isolated: %v", err)
	}
}

// ─────────────────────────────────────────────
// ScanRecovery 分类
// ─────────────────────────────────────────────

func TestSubagentRunStore_ScanRecovery_Classification(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())

	// 创建各种状态的 run
	makeRun := func(runID string) *SubagentRunRecord {
		rec, err := store.CreateRun(SubagentIdentity{SubagentRunID: runID})
		if err != nil {
			t.Fatalf("CreateRun(%s) error = %v", runID, err)
		}
		return rec
	}

	// dispatched → 应转为 awaiting_client_resume
	makeRun("scan-dispatched")

	// running → 应转为 awaiting_client_resume
	recRunning := makeRun("scan-running")
	_, _ = store.UpdateRunStatus("scan-running", recRunning.Version, SubagentRunRunning)

	// terminal_prepared → 应返回在 recoverable 列表
	recPrepare := makeRun("scan-terminal-prepared")
	env := &SubagentResultEnvelope{
		SubagentRunID:    "scan-terminal-prepared",
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "ck-scan",
	}
	_, _ = store.PrepareTerminal("scan-terminal-prepared", recPrepare.Version, env)

	// parent_committed → 无操作，不进 recoverable
	recCommit := makeRun("scan-parent-committed")
	envCommit := &SubagentResultEnvelope{
		SubagentRunID:    "scan-parent-committed",
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "ck-committed",
	}
	_, _ = store.PrepareTerminal("scan-parent-committed", recCommit.Version, envCommit)
	_, _ = store.MarkParentCommitted("scan-parent-committed")

	// 执行扫描
	recoverable, err := store.ScanRecovery()
	if err != nil {
		t.Fatalf("ScanRecovery() error = %v", err)
	}

	// terminal_prepared 必须在 recoverable 列表
	var foundPrepared bool
	for _, r := range recoverable {
		if r.Identity.SubagentRunID == "scan-terminal-prepared" {
			foundPrepared = true
		}
		// parent_committed 不应出现在 recoverable
		if r.Identity.SubagentRunID == "scan-parent-committed" {
			t.Errorf("scan-parent-committed should not be in recoverable list")
		}
	}
	if !foundPrepared {
		t.Error("scan-terminal-prepared not found in recoverable list")
	}

	// dispatched/running 应已转为 awaiting_client_resume
	for _, runID := range []string{"scan-dispatched", "scan-running"} {
		rec, loadErr := store.LoadRun(runID)
		if loadErr != nil {
			t.Fatalf("LoadRun(%s) error = %v", runID, loadErr)
		}
		if rec == nil {
			t.Fatalf("LoadRun(%s) returned nil", runID)
		}
		if rec.Status != SubagentRunAwaitingClientResume {
			t.Errorf("%s status = %q, want awaiting_client_resume", runID, rec.Status)
		}
	}
}

func TestSubagentRunStore_ScanRecovery_EmptyDir(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	records, err := store.ScanRecovery()
	if err != nil {
		t.Fatalf("ScanRecovery() on empty dir error = %v", err)
	}
	if len(records) != 0 {
		t.Fatalf("expected 0 recoverable, got %d", len(records))
	}
}

func TestSubagentRunStore_ScanRecovery_NonExistentDir(t *testing.T) {
	// store root doesn't exist yet
	store := NewSubagentRunStore(filepath.Join(t.TempDir(), "nonexistent"))
	records, err := store.ScanRecovery()
	if err != nil {
		t.Fatalf("ScanRecovery() on nonexistent dir should not error: %v", err)
	}
	if records != nil && len(records) != 0 {
		t.Fatalf("expected nil/empty, got %v", records)
	}
}

// ─────────────────────────────────────────────
// TruncateResultEnvelope
// ─────────────────────────────────────────────

func TestTruncateResultEnvelope_PayloadLimit(t *testing.T) {
	large := strings.Repeat("x", subagentResultPayloadLimit+100)
	env := &SubagentResultEnvelope{
		ToolResultPayload: large,
		ResultSummary:     "summary",
	}
	result := truncateResultEnvelope(env)
	if len(result.ToolResultPayload) > subagentResultPayloadLimit {
		t.Fatalf("ToolResultPayload not truncated: len=%d", len(result.ToolResultPayload))
	}
	if len(result.ToolResultPayload) != subagentResultPayloadLimit {
		t.Fatalf("ToolResultPayload truncated to wrong length: %d", len(result.ToolResultPayload))
	}
}

func TestTruncateResultEnvelope_ToolCallLimit(t *testing.T) {
	large := make([]byte, subagentResultToolCallLimit+100)
	env := &SubagentResultEnvelope{
		ToolCallEncoded: large,
	}
	result := truncateResultEnvelope(env)
	if result.ToolCallEncoded != nil {
		t.Fatalf("ToolCallEncoded should be nil when over limit, got len=%d", len(result.ToolCallEncoded))
	}
}

func TestTruncateResultEnvelope_SummaryLimit(t *testing.T) {
	large := strings.Repeat("s", subagentResultSummaryLimit+10)
	env := &SubagentResultEnvelope{
		ResultSummary: large,
	}
	result := truncateResultEnvelope(env)
	if len(result.ResultSummary) > subagentResultSummaryLimit {
		t.Fatalf("ResultSummary not truncated: len=%d", len(result.ResultSummary))
	}
}

func TestTruncateResultEnvelope_UnderLimit(t *testing.T) {
	payload := "short payload"
	env := &SubagentResultEnvelope{
		ToolResultPayload: payload,
		ToolCallEncoded:   []byte("encoded"),
		ResultSummary:     "summary",
	}
	result := truncateResultEnvelope(env)
	if result.ToolResultPayload != payload {
		t.Fatalf("ToolResultPayload changed when under limit")
	}
	if string(result.ToolCallEncoded) != "encoded" {
		t.Fatalf("ToolCallEncoded changed when under limit")
	}
}

func TestTruncateResultEnvelope_Nil(t *testing.T) {
	if truncateResultEnvelope(nil) != nil {
		t.Fatal("truncateResultEnvelope(nil) should return nil")
	}
}

// ─────────────────────────────────────────────
// UUID 唯一性
// ─────────────────────────────────────────────

func TestGenerateSubagentRunID_Unique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id := GenerateSubagentRunID()
		if id == "" {
			t.Fatal("generated empty run ID")
		}
		if seen[id] {
			t.Fatalf("duplicate run ID generated: %s", id)
		}
		seen[id] = true
	}
}

func TestGenerateSubagentRunID_Format(t *testing.T) {
	id := GenerateSubagentRunID()
	// UUID v4 format: xxxxxxxx-xxxx-4xxx-yxxx-xxxxxxxxxxxx
	if len(id) != 36 {
		t.Fatalf("ID length = %d, want 36: %q", len(id), id)
	}
	parts := strings.Split(id, "-")
	if len(parts) != 5 {
		t.Fatalf("ID has %d hyphen-separated parts, want 5: %q", len(parts), id)
	}
	// version nibble should be '4'
	if len(id) > 14 && id[14] != '4' {
		t.Fatalf("UUID version nibble = %c, want '4': %q", id[14], id)
	}
}

// ─────────────────────────────────────────────
// Atomic write + 文件权限
// ─────────────────────────────────────────────

func TestSubagentRunStore_FilePermissions(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	id := SubagentIdentity{SubagentRunID: "run-perms"}
	_, err := store.CreateRun(id)
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	runPath := filepath.Join(store.root, "run-perms", subagentRunFileName)
	info, err := os.Stat(runPath)
	if err != nil {
		t.Fatalf("stat run.json: %v", err)
	}
	perm := info.Mode().Perm()
	if perm != 0o600 {
		t.Fatalf("run.json permissions = %o, want 0600", perm)
	}
}

// ─────────────────────────────────────────────
// Checksum 函数正确性
// ─────────────────────────────────────────────

func TestComputeRunRecordChecksum_DetectsChange(t *testing.T) {
	rec := &SubagentRunRecord{
		Identity: SubagentIdentity{SubagentRunID: "run-cs-test"},
		Status:   SubagentRunDispatched,
		Version:  1,
	}
	cs1 := computeRunRecordChecksum(rec)
	rec.Status = SubagentRunRunning
	cs2 := computeRunRecordChecksum(rec)
	if cs1 == cs2 {
		t.Fatal("checksum unchanged after field modification")
	}
}

func TestComputeRunRecordChecksum_ChecksumFieldExcluded(t *testing.T) {
	rec := &SubagentRunRecord{
		Identity: SubagentIdentity{SubagentRunID: "run-cs-excl"},
		Status:   SubagentRunDispatched,
		Version:  1,
	}
	cs1 := computeRunRecordChecksum(rec)
	rec.Checksum = cs1 // inject the checksum into the record
	cs2 := computeRunRecordChecksum(rec)
	if cs1 != cs2 {
		t.Fatal("checksum changed when only Checksum field was set (should be excluded from computation)")
	}
}

// ─────────────────────────────────────────────
// LoadRun / LoadResult – not found returns nil
// ─────────────────────────────────────────────

func TestSubagentRunStore_LoadRun_NotFound(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	rec, err := store.LoadRun("nonexistent-run")
	if err != nil {
		t.Fatalf("LoadRun(nonexistent) should not error: %v", err)
	}
	if rec != nil {
		t.Fatalf("LoadRun(nonexistent) = %v, want nil", rec)
	}
}

func TestSubagentRunStore_LoadResult_NotFound(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	env, err := store.LoadResult("nonexistent-run")
	if err != nil {
		t.Fatalf("LoadResult(nonexistent) should not error: %v", err)
	}
	if env != nil {
		t.Fatalf("LoadResult(nonexistent) = %v, want nil", env)
	}
}

// ─────────────────────────────────────────────
// JSON 序列化往返
// ─────────────────────────────────────────────

func TestSubagentRunRecord_JSONRoundTrip(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	term := now
	rec := SubagentRunRecord{
		SchemaVersion: subagentContractSchemaVersion,
		Identity: SubagentIdentity{
			SubagentRunID:        "rr-json",
			ParentConversationID: "conv-1",
			ParentToolCallID:     "tc-1",
			SubagentType:         "generalPurpose",
		},
		Status:           SubagentRunTerminalPrepared,
		Version:          3,
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       &term,
		HandoffState:     SubagentHandoffPrepared,
		ParentCommitKey:  "ck-json",
		CreatedAt:        now,
		UpdatedAt:        now,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatalf("Marshal() error = %v", err)
	}
	var decoded SubagentRunRecord
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if decoded.Identity.SubagentRunID != rec.Identity.SubagentRunID {
		t.Fatalf("run_id mismatch: got %q", decoded.Identity.SubagentRunID)
	}
	if decoded.Status != rec.Status {
		t.Fatalf("status mismatch: got %q", decoded.Status)
	}
	if decoded.TerminalCategory != rec.TerminalCategory {
		t.Fatalf("terminal_category mismatch: got %q", decoded.TerminalCategory)
	}
}

// ─────────────────────────────────────────────
// Fallback run ID (crypto/rand mock failure)
// ─────────────────────────────────────────────

func TestGenerateFallbackRunID_UsedWhenRandFails(t *testing.T) {
	// Replace crypto/rand.Read with a failing mock
	orig := cryptoRandRead
	defer func() { cryptoRandRead = orig }()
	cryptoRandRead = func(b []byte) (int, error) {
		return 0, errors.New("rand unavailable")
	}
	id1 := GenerateSubagentRunID()
	id2 := GenerateSubagentRunID()
	if id1 == "" || id2 == "" {
		t.Fatal("fallback run ID should not be empty")
	}
	if id1 == id2 {
		t.Fatal("fallback run IDs should be unique")
	}
}
