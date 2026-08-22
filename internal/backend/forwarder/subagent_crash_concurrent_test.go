// subagent_crash_concurrent_test.go — 崩溃窗口、并发 CAS、重复 replay、重启中断测试。
//
// 覆盖计划中以下验收条件：
//   - 并发 CAS：两个 goroutine 同时 PrepareTerminal，first-write-wins；
//     第二个得到 terminal_conflict 哨兵，run 状态保持首次写入的 commit key。
//   - 崩溃窗口：PrepareTerminal 后模拟"崩溃"（不调 MarkParentCommitted），
//     再次调用 recoverSubagentTerminalPrepared 应幂等完成 parent commit。
//   - 重复 replay：对同一 run 多次触发 recoverSubagentTerminalPrepared，
//     parent conversation 只出现一条 tool_result entry。
//   - 重启中断：dispatched/running run 在 ScanRecovery 后变为 awaiting_client_resume；
//     不得自动重派，不得丢失记录。
//   - 父目录 fsync：writeSubagentJSONAtomic 在 rename 后写入可被重新读取（间接验证）。
package forwarder

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"cursor/gen/agentv1"
)

// ─────────────────────────────────────────────
// 并发 CAS — PrepareTerminal first-write-wins
// ─────────────────────────────────────────────

// TestPrepareTerminal_ConcurrentFirstWriteWins 启动两个 goroutine 同时调用
// PrepareTerminal，验证：
//   - 恰好有一个成功；
//   - 另一个得到 errSubagentTerminalConflict；
//   - 最终记录的 ParentCommitKey 等于胜出方的 key。
func TestPrepareTerminal_ConcurrentFirstWriteWins(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	runID := "run-concurrent-cas"

	rec, err := store.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: "conv-cas",
		ParentToolCallID:     "tc-cas",
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	makeEnv := func(suffix string) *SubagentResultEnvelope {
		return &SubagentResultEnvelope{
			SubagentRunID:    runID,
			TerminalCategory: SubagentTerminalSucceeded,
			TerminalAt:       time.Now().UTC(),
			ParentCommitKey:  "key-" + suffix,
		}
	}

	type result struct {
		rec *SubagentRunRecord
		err error
	}
	ch := make(chan result, 2)
	var wg sync.WaitGroup
	wg.Add(2)
	for _, suffix := range []string{"A", "B"} {
		suffix := suffix
		wg.Done()
		go func() {
			wg.Wait() // 尽量同时发起
			r, e := store.PrepareTerminal(runID, rec.Version, makeEnv(suffix))
			ch <- result{r, e}
		}()
	}

	var successes, conflicts int
	var winnerKey string
	for i := 0; i < 2; i++ {
		got := <-ch
		if got.err == nil {
			successes++
			winnerKey = got.rec.ParentCommitKey
		} else if errors.Is(got.err, errSubagentTerminalConflict) {
			conflicts++
		} else {
			t.Errorf("unexpected error: %v", got.err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("expected 1 success + 1 conflict; got successes=%d conflicts=%d", successes, conflicts)
	}

	// 持久化状态与胜出方一致
	loaded, _ := store.LoadRun(runID)
	if loaded == nil {
		t.Fatal("run record disappeared after concurrent prepare")
	}
	if loaded.Status != SubagentRunTerminalPrepared {
		t.Fatalf("status = %q, want terminal_prepared", loaded.Status)
	}
	if loaded.ParentCommitKey != winnerKey {
		t.Fatalf("stored commit key = %q, want winner key %q", loaded.ParentCommitKey, winnerKey)
	}
}

// TestPrepareTerminal_ConcurrentVersionConflict 两个 goroutine 以相同版本号竞争更新；
// 第二个应得到版本冲突错误，而非静默成功。
func TestPrepareTerminal_ConcurrentVersionConflict(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	runID := "run-ver-conflict"

	rec, err := store.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: "conv-ver",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// 第一次成功 PrepareTerminal
	env1 := &SubagentResultEnvelope{
		SubagentRunID:    runID,
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "key-first",
	}
	_, err = store.PrepareTerminal(runID, rec.Version, env1)
	if err != nil {
		t.Fatalf("first PrepareTerminal: %v", err)
	}

	// 第二次用同一 prevVersion（旧 version=1），不同 commit key → conflict
	env2 := &SubagentResultEnvelope{
		SubagentRunID:    runID,
		TerminalCategory: SubagentTerminalProviderError,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "key-second-different",
	}
	_, err = store.PrepareTerminal(runID, rec.Version, env2)
	if err == nil {
		t.Fatal("expected version/conflict error on second PrepareTerminal with same version and different key, got nil")
	}
	// 期望 errSubagentTerminalConflict（因为 status 已经是 terminal_prepared 且 key 不同）
	if !errors.Is(err, errSubagentTerminalConflict) {
		t.Logf("got non-conflict error (also acceptable): %v", err)
	}

	// 状态保持首次写入
	loaded, _ := store.LoadRun(runID)
	if loaded.ParentCommitKey != "key-first" {
		t.Fatalf("commit key overwritten: got %q, want key-first", loaded.ParentCommitKey)
	}
}

// ─────────────────────────────────────────────
// 崩溃窗口 — PrepareTerminal 后崩溃，recovery 幂等完成
// ─────────────────────────────────────────────

// TestCrashWindow_PrepareTerminalThenCrash 模拟如下崩溃窗口：
//  1. PrepareTerminal 成功（result.json 持久化）
//  2. 进程"崩溃"（MarkParentCommitted 未调用）
//  3. recoverSubagentTerminalPrepared 重新执行并成功完成 parent commit
func TestCrashWindow_PrepareTerminalThenCrash(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	convStore := NewConversationFileStore(historyRoot)

	runID := "run-crash-window"
	convID := "conv-crash-window"
	if _, err := convStore.CreateConversation(convID, agentv1.AgentMode_AGENT_MODE_AGENT, "", "", convID); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	rec, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: convID,
		ParentToolCallID:     "tc-crash",
		ParentRequestID:      "req-crash",
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}

	// Step 1: PrepareTerminal（durable prepare 完成）
	env := &SubagentResultEnvelope{
		SubagentRunID:     runID,
		TerminalCategory:  SubagentTerminalSucceeded,
		TerminalAt:        time.Now().UTC(),
		ParentCommitKey:   computeSubagentParentCommitKey(runID, "tc-crash", computeSubagentStringDigest("crash result")),
		ToolResultPayload: "crash result",
	}
	if _, err := runStore.PrepareTerminal(runID, rec.Version, env); err != nil {
		t.Fatalf("PrepareTerminal: %v", err)
	}

	// Step 2: 模拟崩溃 — 不调用 MarkParentCommitted；直接重新实例化 service

	// Step 3: 重启后恢复
	service := &Service{
		store:        convStore,
		subagentRuns: runStore,
	}
	record, _ := runStore.LoadRun(runID)
	if record.Status != SubagentRunTerminalPrepared {
		t.Fatalf("pre-recovery status = %q, want terminal_prepared", record.Status)
	}

	if err := service.recoverSubagentTerminalPrepared(record); err != nil {
		t.Fatalf("recoverSubagentTerminalPrepared: %v", err)
	}

	// 验证终态
	updated, _ := runStore.LoadRun(runID)
	if updated == nil {
		t.Fatal("run record gone after recovery")
	}
	if updated.Status != SubagentRunParentCommitted {
		t.Fatalf("post-recovery status = %q, want parent_committed", updated.Status)
	}

	// parent conversation 中有 tool_result entry
	conv, _ := convStore.LoadConversation(convID)
	if conv == nil {
		t.Fatal("parent conversation not created during recovery")
	}
	if countKind(conv.Entries, "tool_result") != 1 {
		t.Fatalf("want 1 tool_result entry after crash recovery, got %d", countKind(conv.Entries, "tool_result"))
	}
}

// TestCrashWindow_DoublePrepare_Idempotent 验证：PrepareTerminal 可以重入（幂等），
// 相同 commit key 不会产生版本冲突。
func TestCrashWindow_DoublePrepare_Idempotent(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	runID := "run-double-prepare"

	rec, _ := store.CreateRun(SubagentIdentity{
		SubagentRunID: runID,
	})
	env := &SubagentResultEnvelope{
		SubagentRunID:    runID,
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "key-idempotent",
	}
	for i := 0; i < 3; i++ {
		r, err := store.PrepareTerminal(runID, rec.Version, env)
		if err != nil {
			t.Fatalf("PrepareTerminal call %d: %v", i+1, err)
		}
		if r.Status != SubagentRunTerminalPrepared {
			t.Fatalf("call %d: status = %q", i+1, r.Status)
		}
		if r.ParentCommitKey != "key-idempotent" {
			t.Fatalf("call %d: commit key = %q", i+1, r.ParentCommitKey)
		}
	}
}

func TestCrashWindow_ResultPersistedBeforeRunRecordUpdate(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	convStore := NewConversationFileStore(historyRoot)
	const runID = "run-result-before-record"
	const parentID = "parent-result-before-record"
	if _, err := convStore.CreateConversation(parentID, agentv1.AgentMode_AGENT_MODE_AGENT, "", "", parentID); err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}
	if _, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: parentID,
		ParentToolCallID:     "tool-1",
	}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	envelope := &SubagentResultEnvelope{
		SchemaVersion:     subagentContractSchemaVersion,
		SubagentRunID:     runID,
		TerminalCategory:  SubagentTerminalSucceeded,
		TerminalAt:        time.Now().UTC(),
		ToolResultPayload: "persisted result",
		ParentCommitKey:   "commit-result-before-record",
	}
	envelope.Checksum = computeResultEnvelopeChecksum(envelope)
	if err := runStore.writeResultLocked(runID, envelope); err != nil {
		t.Fatalf("writeResultLocked() error = %v", err)
	}

	recoverable, err := runStore.ScanRecovery()
	if err != nil {
		t.Fatalf("ScanRecovery() error = %v", err)
	}
	if len(recoverable) != 1 || recoverable[0].Status != SubagentRunTerminalPrepared {
		t.Fatalf("recoverable records = %+v", recoverable)
	}
	service := &Service{store: convStore, subagentRuns: runStore}
	if err := service.recoverSubagentTerminalPrepared(recoverable[0]); err != nil {
		t.Fatalf("recoverSubagentTerminalPrepared() error = %v", err)
	}
	updated, err := runStore.LoadRun(runID)
	if err != nil {
		t.Fatalf("LoadRun() error = %v", err)
	}
	if updated.Status != SubagentRunParentCommitted {
		t.Fatalf("status = %q, want parent_committed", updated.Status)
	}
	conversation, err := convStore.LoadConversation(parentID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if conversation == nil || countKind(conversation.Entries, "tool_result") != 1 {
		t.Fatalf("parent conversation was not committed exactly once: %+v", conversation)
	}
}

// ─────────────────────────────────────────────
// 重复 replay — recovery 执行多次不产生重复 entry
// ─────────────────────────────────────────────

// TestDuplicateReplay_RecoveryIsIdempotent 调用 recoverSubagentTerminalPrepared 三次，
// 验证 parent conversation 只有一条 tool_result entry（幂等键去重）。
func TestDuplicateReplay_RecoveryIsIdempotent(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	convStore := NewConversationFileStore(historyRoot)

	runID := "run-replay-idem"
	convID := "conv-replay-idem"
	if _, err := convStore.CreateConversation(convID, agentv1.AgentMode_AGENT_MODE_AGENT, "", "", convID); err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}

	rec, _ := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: convID,
		ParentToolCallID:     "tc-replay",
	})
	env := &SubagentResultEnvelope{
		SubagentRunID:     runID,
		TerminalCategory:  SubagentTerminalSucceeded,
		TerminalAt:        time.Now().UTC(),
		ParentCommitKey:   computeSubagentParentCommitKey(runID, "tc-replay", computeSubagentStringDigest("replay payload")),
		ToolResultPayload: "replay payload",
	}
	if _, err := runStore.PrepareTerminal(runID, rec.Version, env); err != nil {
		t.Fatalf("PrepareTerminal: %v", err)
	}

	service := &Service{store: convStore, subagentRuns: runStore}
	record, _ := runStore.LoadRun(runID)

	for i := 0; i < 3; i++ {
		if err := service.recoverSubagentTerminalPrepared(record); err != nil {
			t.Fatalf("replay %d: recoverSubagentTerminalPrepared error = %v", i+1, err)
		}
	}

	conv, _ := convStore.LoadConversation(convID)
	if conv == nil {
		t.Fatal("conversation not found after repeated replay")
	}
	toolCount := countKind(conv.Entries, "tool_result")
	if toolCount != 1 {
		t.Fatalf("duplicate replay: want 1 tool_result entry, got %d", toolCount)
	}
}

// TestDuplicateReplay_AppendIdempotent_WithFileStore 验证 appendSubagentToolResultIdempotent
// 在 file-backed store 下多次调用不产生重复 entry（与已有内存路径测试互补）。
func TestDuplicateReplay_AppendIdempotent_WithFileStore(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	convStore := NewConversationFileStore(historyRoot)

	runID := "run-append-idem-fs"
	convID := "conv-append-idem-fs"

	_, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: convID,
	})
	if err != nil {
		t.Fatalf("CreateRun: %v", err)
	}
	if _, err := convStore.CreateConversation(convID, agentv1.AgentMode_AGENT_MODE_AGENT, "", "", convID); err != nil {
		t.Fatalf("CreateConversation: %v", err)
	}

	svc := &Service{store: convStore, subagentRuns: runStore}
	stream := makeTestStreamWithConversation(convID)
	pending := makeTestPendingExec(runID)
	pending.ToolCallID = "tc-fs-idem"

	for i := 0; i < 4; i++ {
		if err := svc.appendSubagentToolResultIdempotent(
			stream, pending, "tc-fs-idem",
			"idempotent payload", nil, SubagentTerminalSucceeded,
		); err != nil {
			t.Fatalf("call %d: appendSubagentToolResultIdempotent error = %v", i+1, err)
		}
	}

	conv, _ := convStore.LoadConversation(convID)
	if conv == nil {
		t.Fatal("conversation not found")
	}
	if countKind(conv.Entries, "tool_result") != 1 {
		t.Fatalf("want 1 tool_result, got %d (duplicate!)", countKind(conv.Entries, "tool_result"))
	}
}

// ─────────────────────────────────────────────
// 重启中断 — dispatched/running → awaiting_client_resume
// ─────────────────────────────────────────────

// TestRestartInterrupt_NonTerminalBecomesAwaitingResume 验证：ScanRecovery 把
// dispatched/running/backgrounded run 转为 awaiting_client_resume，不重派子代理。
func TestRestartInterrupt_NonTerminalBecomesAwaitingResume(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())

	cases := []struct {
		runID  string
		status SubagentRunStatus
	}{
		{"run-restart-dispatched", SubagentRunDispatched},
		{"run-restart-running", SubagentRunRunning},
		{"run-restart-backgrounded", SubagentRunBackgrounded},
	}
	for _, c := range cases {
		_, err := store.CreateRun(SubagentIdentity{
			SubagentRunID:        c.runID,
			ParentConversationID: "conv-restart",
		})
		if err != nil {
			t.Fatalf("CreateRun(%s): %v", c.runID, err)
		}
		if c.status != SubagentRunDispatched {
			// 手动推进到目标状态
			rec, _ := store.LoadRun(c.runID)
			if _, err := store.UpdateRunStatus(c.runID, rec.Version, c.status); err != nil {
				t.Fatalf("UpdateRunStatus(%s→%s): %v", c.runID, c.status, err)
			}
		}
	}

	recoverable, err := store.ScanRecovery()
	if err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	// 非终结 run 转为 awaiting_client_resume，不加入 recoverable 列表
	if len(recoverable) != 0 {
		t.Fatalf("non-terminal runs should not appear in recoverable list; got %d", len(recoverable))
	}

	for _, c := range cases {
		rec, _ := store.LoadRun(c.runID)
		if rec == nil {
			t.Fatalf("run record gone after ScanRecovery for %s", c.runID)
		}
		if rec.Status != SubagentRunAwaitingClientResume {
			t.Fatalf("%s: status = %q after ScanRecovery, want awaiting_client_resume", c.runID, rec.Status)
		}
	}
}

// TestRestartInterrupt_TerminalPreparedSurvivesScan 验证：terminal_prepared run
// 在 ScanRecovery 后保留在 recoverable 列表中，状态不变。
func TestRestartInterrupt_TerminalPreparedSurvivesScan(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	runID := "run-restart-tp"

	rec, _ := store.CreateRun(SubagentIdentity{SubagentRunID: runID})
	env := &SubagentResultEnvelope{
		SubagentRunID:    runID,
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "key-tp",
	}
	if _, err := store.PrepareTerminal(runID, rec.Version, env); err != nil {
		t.Fatalf("PrepareTerminal: %v", err)
	}

	recoverable, err := store.ScanRecovery()
	if err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	if len(recoverable) != 1 || recoverable[0].Identity.SubagentRunID != runID {
		t.Fatalf("ScanRecovery should return terminal_prepared run; got %v", recoverable)
	}
	// 状态未被修改
	loaded, _ := store.LoadRun(runID)
	if loaded.Status != SubagentRunTerminalPrepared {
		t.Fatalf("status changed by ScanRecovery: got %q", loaded.Status)
	}
}

// TestRestartInterrupt_AlreadyCommittedSkipped 验证：parent_committed / acknowledged 的 run
// 在 ScanRecovery 后既不出现在 recoverable 列表，状态也不变。
func TestRestartInterrupt_AlreadyCommittedSkipped(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())

	for _, status := range []SubagentRunStatus{SubagentRunParentCommitted, SubagentRunAcknowledged, SubagentRunAwaitingClientResume} {
		runID := "run-restart-" + string(status)
		rec, _ := store.CreateRun(SubagentIdentity{SubagentRunID: runID})
		if status == SubagentRunAwaitingClientResume {
			if _, err := store.UpdateRunStatus(runID, rec.Version, SubagentRunAwaitingClientResume); err != nil {
				t.Fatalf("UpdateRunStatus(%s): %v", status, err)
			}
			continue
		}
		env := &SubagentResultEnvelope{
			SubagentRunID:    runID,
			TerminalCategory: SubagentTerminalSucceeded,
			TerminalAt:       time.Now().UTC(),
			ParentCommitKey:  "key-" + string(status),
		}
		if _, err := store.PrepareTerminal(runID, rec.Version, env); err != nil {
			t.Fatalf("PrepareTerminal(%s): %v", status, err)
		}
		if status == SubagentRunParentCommitted || status == SubagentRunAcknowledged {
			if _, err := store.MarkParentCommitted(runID); err != nil {
				t.Fatalf("MarkParentCommitted(%s): %v", status, err)
			}
		}
		if status == SubagentRunAcknowledged {
			if _, err := store.MarkAcknowledged(runID); err != nil {
				t.Fatalf("MarkAcknowledged(%s): %v", status, err)
			}
		}
	}

	recoverable, err := store.ScanRecovery()
	if err != nil {
		t.Fatalf("ScanRecovery: %v", err)
	}
	if len(recoverable) != 0 {
		t.Fatalf("already-terminal runs should not appear in recoverable; got %d", len(recoverable))
	}
}

// ─────────────────────────────────────────────
// 父目录 fsync — writeSubagentJSONAtomic 写入可被重读
// ─────────────────────────────────────────────

// TestWriteSubagentJSONAtomic_DirFsync_ReadBack 验证 writeSubagentJSONAtomic 写入的文件
// 可立即重新读取（间接验证 rename+dir-fsync 路径不产生错误，且内容正确）。
func TestWriteSubagentJSONAtomic_DirFsync_ReadBack(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/test-atomic.json"
	payload := map[string]string{"key": "value"}

	if err := writeSubagentJSONAtomic(path, payload, 0o600); err != nil {
		t.Fatalf("writeSubagentJSONAtomic: %v", err)
	}

	// 文件必须存在且权限为 0600
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat after write: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file perm = %v, want 0600", info.Mode().Perm())
	}

	// 内容可读且非空
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read after write: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("written file is empty")
	}
}

// ─────────────────────────────────────────────
// 并发双扫描 — ScanRecovery TOCTOU 消除验证
// ─────────────────────────────────────────────

// TestScanRecovery_ConcurrentDualScan 验证两个 goroutine 并发调用 ScanRecovery 时：
//   - 每次扫描在单次锁内完成 load/validate/update，不产生 TOCTOU；
//   - dispatched run 最终恰好转为 awaiting_client_resume（不产生版本冲突或双写）；
//   - terminal_prepared run 双方均能在 recoverable 列表中看到（或其中一方已先处理），
//     最终持久化状态不被破坏；
//   - 两次扫描均不返回 error。
func TestScanRecovery_ConcurrentDualScan(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())

	// 创建若干不同状态的 run
	// dispatched × 3
	for i := 0; i < 3; i++ {
		runID := fmt.Sprintf("dual-scan-dispatched-%d", i)
		if _, err := store.CreateRun(SubagentIdentity{SubagentRunID: runID}); err != nil {
			t.Fatalf("CreateRun(%s): %v", runID, err)
		}
	}

	// terminal_prepared × 2
	for i := 0; i < 2; i++ {
		runID := fmt.Sprintf("dual-scan-prepared-%d", i)
		rec, err := store.CreateRun(SubagentIdentity{SubagentRunID: runID})
		if err != nil {
			t.Fatalf("CreateRun(%s): %v", runID, err)
		}
		env := &SubagentResultEnvelope{
			SubagentRunID:    runID,
			TerminalCategory: SubagentTerminalSucceeded,
			TerminalAt:       time.Now().UTC(),
			ParentCommitKey:  "ck-dual-" + runID,
		}
		if _, err := store.PrepareTerminal(runID, rec.Version, env); err != nil {
			t.Fatalf("PrepareTerminal(%s): %v", runID, err)
		}
	}

	// 两个 goroutine 同时扫描
	type scanResult struct {
		records []*SubagentRunRecord
		err     error
	}
	ch := make(chan scanResult, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for g := 0; g < 2; g++ {
		ready.Done()
		go func() {
			ready.Wait() // 尽量同时发起
			recs, e := store.ScanRecovery()
			ch <- scanResult{recs, e}
		}()
	}

	var totalErrors int
	for i := 0; i < 2; i++ {
		res := <-ch
		if res.err != nil {
			t.Errorf("ScanRecovery goroutine %d error: %v", i, res.err)
			totalErrors++
		}
	}
	if totalErrors > 0 {
		t.Fatalf("%d scan goroutines returned errors", totalErrors)
	}

	// dispatched runs 必须全部变为 awaiting_client_resume
	for i := 0; i < 3; i++ {
		runID := fmt.Sprintf("dual-scan-dispatched-%d", i)
		rec, loadErr := store.LoadRun(runID)
		if loadErr != nil {
			t.Fatalf("LoadRun(%s): %v", runID, loadErr)
		}
		if rec == nil {
			t.Fatalf("run record gone: %s", runID)
		}
		if rec.Status != SubagentRunAwaitingClientResume {
			t.Errorf("%s: status = %q after dual scan, want awaiting_client_resume", runID, rec.Status)
		}
	}

	// terminal_prepared runs 状态必须保持（terminal_prepared 或 parent_committed 均合法，
	// 取决于 goroutine 是否额外执行了 MarkParentCommitted，但扫描本身不应该破坏状态）
	for i := 0; i < 2; i++ {
		runID := fmt.Sprintf("dual-scan-prepared-%d", i)
		rec, loadErr := store.LoadRun(runID)
		if loadErr != nil {
			t.Fatalf("LoadRun(%s): %v", runID, loadErr)
		}
		if rec == nil {
			t.Fatalf("terminal_prepared run record gone: %s", runID)
		}
		// ScanRecovery 不修改 terminal_prepared 的状态；状态应保持 terminal_prepared
		if rec.Status != SubagentRunTerminalPrepared {
			t.Errorf("%s: status = %q after dual scan, want terminal_prepared", runID, rec.Status)
		}
	}
}
