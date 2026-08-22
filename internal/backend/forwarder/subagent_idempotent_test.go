// subagent_idempotent_test.go — appendSubagentToolResultIdempotent、
// recoverSubagentTerminalPrepared 和辅助函数的测试。
// 覆盖：happy path（内存路径）、重复提交幂等、fallback（空 RunID/nil store）、
// store 路径幂等（第二次调用不产生重复 entry）、恢复路径。
package forwarder

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

// ─────────────────────────────────────────────
// computeSubagentParentCommitKey
// ─────────────────────────────────────────────

func TestComputeSubagentParentCommitKey_Stable(t *testing.T) {
	k1 := computeSubagentParentCommitKey("run-1", "tc-1", "digest-abc")
	k2 := computeSubagentParentCommitKey("run-1", "tc-1", "digest-abc")
	if k1 != k2 {
		t.Fatalf("commit key is not stable: %q vs %q", k1, k2)
	}
	if k1 == "" {
		t.Fatal("commit key is empty")
	}
	// 不同输入必须产生不同 key
	k3 := computeSubagentParentCommitKey("run-2", "tc-1", "digest-abc")
	if k1 == k3 {
		t.Fatal("different inputs produced same commit key")
	}
}

func TestComputeSubagentParentCommitKey_Length(t *testing.T) {
	k := computeSubagentParentCommitKey("r", "t", "d")
	// 实现返回 sha256 前 16 字节的 hex = 32 字符
	if len(k) != 32 {
		t.Fatalf("commit key length = %d, want 32: %q", len(k), k)
	}
}

// ─────────────────────────────────────────────
// parseArgsJSONSubagentType
// ─────────────────────────────────────────────

func TestParseArgsJSONSubagentType(t *testing.T) {
	cases := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "subagent_type key",
			input:    `{"subagent_type":"generalPurpose","description":"do stuff"}`,
			expected: "generalPurpose",
		},
		{
			name:     "subagentType camelCase key",
			input:    `{"subagentType":"explore"}`,
			expected: "explore",
		},
		{
			name:     "missing key",
			input:    `{"description":"no type here"}`,
			expected: "",
		},
		{
			name:     "empty JSON",
			input:    `{}`,
			expected: "",
		},
		{
			name:     "invalid JSON",
			input:    `NOT JSON`,
			expected: "",
		},
		{
			name:     "nil input",
			input:    "",
			expected: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var input []byte
			if tc.input != "" {
				input = []byte(tc.input)
			}
			got := parseArgsJSONSubagentType(input)
			if got != tc.expected {
				t.Fatalf("parseArgsJSONSubagentType(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

// ─────────────────────────────────────────────
// computeSubagentStringDigest
// ─────────────────────────────────────────────

func TestComputeSubagentStringDigest_Stable(t *testing.T) {
	d1 := computeSubagentStringDigest("hello")
	d2 := computeSubagentStringDigest("hello")
	if d1 != d2 {
		t.Fatalf("digest not stable: %q vs %q", d1, d2)
	}
	d3 := computeSubagentStringDigest("world")
	if d1 == d3 {
		t.Fatal("different inputs produced same digest")
	}
}

// ─────────────────────────────────────────────
// appendSubagentToolResultIdempotent – 内存路径（store=nil）
// ─────────────────────────────────────────────

func makeTestStreamWithConversation(convID string) *ActiveStream {
	conv := &ConversationFile{
		ConversationID:     convID,
		RootConversationID: convID,
		Mode:               "agent",
		NextTurnSeq:        1,
		NextEntrySeq:       1,
		Entries:            make([]HistoryEntry, 0),
	}
	return &ActiveStream{
		RequestID:              "req-test",
		ConversationID:         convID,
		TurnSeq:                1,
		CheckpointConversation: conv,
	}
}

func makeTestPendingExec(runID string) runtimecore.PendingExec {
	args, _ := json.Marshal(map[string]any{
		"subagent_type": "generalPurpose",
		"description":   "do work",
	})
	return runtimecore.PendingExec{
		ExecID:        "exec-1",
		ToolCallID:    "tc-test-1",
		ExecKind:      "subagent",
		ArgsJSON:      args,
		SubagentRunID: runID,
	}
}

func TestAppendSubagentToolResultIdempotent_InMemoryHappyPath(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	runID := "run-happy"

	// 预先创建 run record
	_, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: "conv-happy",
		ParentToolCallID:     "tc-test-1",
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	service := &Service{
		subagentRuns: runStore,
		// store=nil → 走内存路径
	}
	stream := makeTestStreamWithConversation("conv-happy")
	pending := makeTestPendingExec(runID)

	if err := service.appendSubagentToolResultIdempotent(
		stream, pending, "tc-test-1",
		"task completed successfully",
		nil,
		SubagentTerminalSucceeded,
	); err != nil {
		t.Fatalf("appendSubagentToolResultIdempotent() error = %v", err)
	}

	// stream.CheckpointConversation should have entries
	stream.mu.Lock()
	entryCount := len(stream.CheckpointConversation.Entries)
	stream.mu.Unlock()
	if entryCount == 0 {
		t.Fatal("no entries written to checkpoint conversation")
	}

	// run should be in parent_committed state
	rec, _ := runStore.LoadRun(runID)
	if rec == nil {
		t.Fatal("run record missing after commit")
	}
	if rec.Status != SubagentRunParentCommitted {
		t.Fatalf("run status = %q, want parent_committed", rec.Status)
	}
}

func TestAppendSubagentToolResultIdempotent_InMemoryDuplicateIsNoop(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	runID := "run-dup"

	_, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: "conv-dup",
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	service := &Service{subagentRuns: runStore}
	stream := makeTestStreamWithConversation("conv-dup")
	pending := makeTestPendingExec(runID)

	for i := 0; i < 3; i++ {
		if err := service.appendSubagentToolResultIdempotent(
			stream, pending, "tc-dup",
			"result text", nil, SubagentTerminalSucceeded,
		); err != nil {
			t.Fatalf("call %d: appendSubagentToolResultIdempotent() error = %v", i, err)
		}
	}

	// Should only have entries from first call (idempotency key deduplication)
	stream.mu.Lock()
	defer stream.mu.Unlock()

	// Count entries with idempotency keys matching tool_result
	var toolResultCount int
	for _, e := range stream.CheckpointConversation.Entries {
		if e.Kind == "tool_result" {
			toolResultCount++
		}
	}
	if toolResultCount > 1 {
		t.Fatalf("duplicate tool_result entries: found %d, want 1", toolResultCount)
	}
}

func TestAppendSubagentToolResultIdempotent_FastPath_AlreadyTerminal(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	runID := "run-fast"

	rec, _ := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: "conv-fast",
		ParentToolCallID:     "tc-fast",
	})
	env := &SubagentResultEnvelope{
		SubagentRunID:    runID,
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "ck-fast",
	}
	_, _ = runStore.PrepareTerminal(runID, rec.Version, env)
	_, _ = runStore.MarkParentCommitted(runID) // already parent_committed

	service := &Service{subagentRuns: runStore}
	stream := makeTestStreamWithConversation("conv-fast")
	pending := makeTestPendingExec(runID)

	if err := service.appendSubagentToolResultIdempotent(
		stream, pending, "tc-fast", "payload", nil, SubagentTerminalSucceeded,
	); err != nil {
		t.Fatalf("fast-path idempotent error = %v", err)
	}

	// No entries should have been written (fast path returns early)
	stream.mu.Lock()
	count := len(stream.CheckpointConversation.Entries)
	stream.mu.Unlock()
	if count != 0 {
		t.Fatalf("fast path should write no entries, got %d", count)
	}
}

// ─────────────────────────────────────────────
// Fallback: empty SubagentRunID → ordinary appendToolResult
// ─────────────────────────────────────────────

func TestAppendSubagentToolResultIdempotent_FallbackEmptyRunID(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)

	service := &Service{subagentRuns: runStore}
	stream := makeTestStreamWithConversation("conv-fallback")

	pending := makeTestPendingExec("") // empty SubagentRunID → fallback
	pending.ExecKind = "subagent"
	pending.ToolCallID = "tc-fallback"

	// No run record exists; should fall through to ordinary appendToolResult.
	// store=nil so appendToolResult uses in-memory path.
	if err := service.appendSubagentToolResultIdempotent(
		stream, pending, "tc-fallback",
		"fallback result", nil, SubagentTerminalSucceeded,
	); err != nil {
		t.Fatalf("fallback appendSubagentToolResultIdempotent() error = %v", err)
	}

	stream.mu.Lock()
	count := len(stream.CheckpointConversation.Entries)
	stream.mu.Unlock()
	// appendToolResult writes one tool_result entry
	if count == 0 {
		t.Fatal("no entries written by fallback path")
	}
}

func TestAppendSubagentToolResultIdempotent_FallbackNilStore(t *testing.T) {
	service := &Service{
		subagentRuns: nil, // nil store → fallback
	}
	stream := makeTestStreamWithConversation("conv-nil-store")
	pending := makeTestPendingExec("run-nil-store-test")
	pending.ExecKind = "subagent"

	// Should fall through to ordinary appendToolResult (in-memory)
	if err := service.appendSubagentToolResultIdempotent(
		stream, pending, "tc-nil-store",
		"nil store result", nil, SubagentTerminalSucceeded,
	); err != nil {
		t.Fatalf("nil-store appendSubagentToolResultIdempotent() error = %v", err)
	}
}

// ─────────────────────────────────────────────
// appendSubagentToolResultIdempotent – store-backed path
// ─────────────────────────────────────────────

func TestAppendSubagentToolResultIdempotent_StoreBackedIdempotent(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	convStore := NewConversationFileStore(historyRoot)

	runID := "run-store-idem"
	convID := "conv-store-idem"

	_, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: convID,
		ParentToolCallID:     "tc-store-idem",
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	// Durable handoff 只能追加到已经存在的 parent conversation。
	if _, err := convStore.CreateConversation(convID, agentv1.AgentMode_AGENT_MODE_AGENT, "", "", convID); err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}

	service := &Service{
		store:        convStore,
		subagentRuns: runStore,
	}
	stream := makeTestStreamWithConversation(convID)
	pending := makeTestPendingExec(runID)
	pending.ToolCallID = "tc-store-idem"

	const resultPayload = "store-backed result"

	// First call
	if err := service.appendSubagentToolResultIdempotent(
		stream, pending, "tc-store-idem",
		resultPayload, nil, SubagentTerminalSucceeded,
	); err != nil {
		t.Fatalf("first appendSubagentToolResultIdempotent() error = %v", err)
	}

	// Verify parent_committed
	rec1, _ := runStore.LoadRun(runID)
	if rec1 == nil || rec1.Status != SubagentRunParentCommitted {
		t.Fatalf("run status after first call = %v", rec1)
	}

	// Verify persisted in file store
	conv, _ := convStore.LoadConversation(convID)
	if conv == nil {
		t.Fatal("conversation not persisted after first call")
	}
	if len(conv.Entries) == 0 {
		t.Fatal("no entries persisted after first call")
	}

	// Count tool_result entries
	toolResultCount := countKind(conv.Entries, "tool_result")
	if toolResultCount != 1 {
		t.Fatalf("want 1 tool_result entry, got %d", toolResultCount)
	}

	// Second call – must be idempotent
	if err := service.appendSubagentToolResultIdempotent(
		stream, pending, "tc-store-idem",
		resultPayload, nil, SubagentTerminalSucceeded,
	); err != nil {
		t.Fatalf("second appendSubagentToolResultIdempotent() error = %v", err)
	}

	// Re-load from file; should still have exactly one tool_result
	conv2, _ := convStore.LoadConversation(convID)
	toolResultCount2 := countKind(conv2.Entries, "tool_result")
	if toolResultCount2 != 1 {
		t.Fatalf("after second call: want 1 tool_result entry, got %d (duplicate!)", toolResultCount2)
	}
}

func TestAppendSubagentToolResultIdempotent_MissingParentConversationPreserved(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	convStore := NewConversationFileStore(historyRoot)

	runID := "run-online-missing-parent"
	convID := "conv-online-missing-parent"
	if _, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: convID,
		ParentToolCallID:     "tc-online-missing-parent",
	}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	service := &Service{store: convStore, subagentRuns: runStore}
	stream := makeTestStreamWithConversation(convID)
	pending := makeTestPendingExec(runID)
	pending.ToolCallID = "tc-online-missing-parent"
	err := service.appendSubagentToolResultIdempotent(
		stream, pending, pending.ToolCallID,
		"durable result", nil, SubagentTerminalSucceeded,
	)
	if err == nil || !errors.Is(err, errConversationNotFound) {
		t.Fatalf("expected conversation-not-found error, got %v", err)
	}

	updated, _ := runStore.LoadRun(runID)
	if updated == nil || updated.Status != SubagentRunAwaitingParentResume {
		t.Fatalf("run status = %v, want awaiting_parent_resume", updated)
	}
	parent, loadErr := convStore.LoadConversation(convID)
	if loadErr != nil {
		t.Fatalf("LoadConversation() error = %v", loadErr)
	}
	if parent != nil {
		t.Fatalf("missing parent conversation was recreated: %+v", parent)
	}
}

// ─────────────────────────────────────────────
// recoverSubagentTerminalPrepared
// ─────────────────────────────────────────────

func TestRecoverSubagentTerminalPrepared_Basic(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	convStore := NewConversationFileStore(historyRoot)

	runID := "run-recover"
	convID := "conv-recover"
	if _, err := convStore.CreateConversation(convID, agentv1.AgentMode_AGENT_MODE_AGENT, "", "", convID); err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}

	rec, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: convID,
		ParentToolCallID:     "tc-recover",
		ParentRequestID:      "req-recover",
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}

	// PrepareTerminal – simulate terminal state before crash
	env := &SubagentResultEnvelope{
		SubagentRunID:     runID,
		TerminalCategory:  SubagentTerminalSucceeded,
		TerminalAt:        time.Now().UTC(),
		ParentCommitKey:   computeSubagentParentCommitKey(runID, "tc-recover", "digest-recover"),
		ToolResultPayload: "recovery result",
	}
	_, err = runStore.PrepareTerminal(runID, rec.Version, env)
	if err != nil {
		t.Fatalf("PrepareTerminal() error = %v", err)
	}

	// Simulate recovery (backend restarted – no active stream)
	service := &Service{
		store:        convStore,
		subagentRuns: runStore,
	}

	loadedRecord, _ := runStore.LoadRun(runID)
	if err := service.recoverSubagentTerminalPrepared(loadedRecord); err != nil {
		t.Fatalf("recoverSubagentTerminalPrepared() error = %v", err)
	}

	// run should be parent_committed
	updated, _ := runStore.LoadRun(runID)
	if updated == nil {
		t.Fatal("run record missing after recovery")
	}
	if updated.Status != SubagentRunParentCommitted {
		t.Fatalf("run status = %q, want parent_committed", updated.Status)
	}

	// parent conversation should have a tool_result entry
	conv, _ := convStore.LoadConversation(convID)
	if conv == nil {
		t.Fatal("parent conversation not created during recovery")
	}
	toolCount := countKind(conv.Entries, "tool_result")
	if toolCount == 0 {
		t.Fatal("no tool_result entry written during recovery")
	}
}

func TestRecoverSubagentTerminalPrepared_Idempotent(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	convStore := NewConversationFileStore(historyRoot)

	runID := "run-recover-idem"
	convID := "conv-recover-idem"
	if _, err := convStore.CreateConversation(convID, agentv1.AgentMode_AGENT_MODE_AGENT, "", "", convID); err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}

	rec, _ := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: convID,
		ParentToolCallID:     "tc-ri",
	})
	env := &SubagentResultEnvelope{
		SubagentRunID:     runID,
		TerminalCategory:  SubagentTerminalSucceeded,
		TerminalAt:        time.Now().UTC(),
		ParentCommitKey:   "ck-recover-idem",
		ToolResultPayload: "idempotent payload",
	}
	_, _ = runStore.PrepareTerminal(runID, rec.Version, env)

	service := &Service{store: convStore, subagentRuns: runStore}
	record, _ := runStore.LoadRun(runID)

	// Call recovery twice
	for i := 0; i < 2; i++ {
		if err := service.recoverSubagentTerminalPrepared(record); err != nil {
			t.Fatalf("call %d: recoverSubagentTerminalPrepared() error = %v", i+1, err)
		}
	}

	conv, _ := convStore.LoadConversation(convID)
	if conv == nil {
		t.Fatal("conversation not found after recovery")
	}
	toolCount := countKind(conv.Entries, "tool_result")
	if toolCount != 1 {
		t.Fatalf("idempotent recovery: want 1 tool_result, got %d", toolCount)
	}
}

func TestRecoverSubagentTerminalPrepared_MissingParentConvID(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)

	runID := "run-recover-no-conv"
	rec, _ := runStore.CreateRun(SubagentIdentity{
		SubagentRunID: runID,
		// ParentConversationID intentionally empty
	})
	env := &SubagentResultEnvelope{
		SubagentRunID:    runID,
		TerminalCategory: SubagentTerminalSucceeded,
		TerminalAt:       time.Now().UTC(),
		ParentCommitKey:  "ck-no-conv",
	}
	_, _ = runStore.PrepareTerminal(runID, rec.Version, env)

	service := &Service{subagentRuns: runStore}
	record, _ := runStore.LoadRun(runID)

	// Should not return an error – missing parent conv is a degraded but non-fatal case
	if err := service.recoverSubagentTerminalPrepared(record); err != nil {
		t.Fatalf("missing parent conv should not be fatal: %v", err)
	}
}

func TestRecoverSubagentTerminalPrepared_MissingParentConversationPreserved(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	convStore := NewConversationFileStore(historyRoot)

	runID := "run-recover-missing-parent"
	convID := "conv-does-not-exist"
	rec, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: convID,
		ParentToolCallID:     "tc-missing-parent",
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	_, err = runStore.PrepareTerminal(runID, rec.Version, &SubagentResultEnvelope{
		SubagentRunID:     runID,
		TerminalCategory:  SubagentTerminalSucceeded,
		TerminalAt:        time.Now().UTC(),
		ParentCommitKey:   "ck-missing-parent",
		ToolResultPayload: "durable result",
	})
	if err != nil {
		t.Fatalf("PrepareTerminal() error = %v", err)
	}

	service := &Service{store: convStore, subagentRuns: runStore}
	loaded, _ := runStore.LoadRun(runID)
	if err := service.recoverSubagentTerminalPrepared(loaded); err != nil {
		t.Fatalf("missing parent conversation should be preserved without fatal error: %v", err)
	}

	updated, _ := runStore.LoadRun(runID)
	if updated == nil || updated.Status != SubagentRunAwaitingParentResume {
		t.Fatalf("run status = %v, want awaiting_parent_resume", updated)
	}
	parent, err := convStore.LoadConversation(convID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if parent != nil {
		t.Fatalf("missing parent conversation was recreated: %+v", parent)
	}
}

func TestRecoverSubagentTerminalPrepared_NilRecord(t *testing.T) {
	service := &Service{}
	if err := service.recoverSubagentTerminalPrepared(nil); err != nil {
		t.Fatalf("nil record should not return error: %v", err)
	}
}

// ─────────────────────────────────────────────
// startSubagentRecovery wiring
// ─────────────────────────────────────────────

func TestStartSubagentRecovery_NilServiceNoPanic(t *testing.T) {
	// Should not panic on nil service
	var s *Service
	s.startSubagentRecovery() // must not panic
}

func TestStartSubagentRecovery_NilStoreNoPanic(t *testing.T) {
	s := &Service{subagentRuns: nil}
	s.startSubagentRecovery() // must not panic
}

// ─────────────────────────────────────────────
// Full recovery cycle via ScanRecovery
// ─────────────────────────────────────────────

func TestFullRecoveryCycle_ScanAndCommit(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	convStore := NewConversationFileStore(historyRoot)

	runID := "run-full-cycle"
	convID := "conv-full-cycle"
	if _, err := convStore.CreateConversation(convID, agentv1.AgentMode_AGENT_MODE_AGENT, "", "", convID); err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}

	rec, _ := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: convID,
		ParentToolCallID:     "tc-full",
	})
	env := &SubagentResultEnvelope{
		SubagentRunID:     runID,
		TerminalCategory:  SubagentTerminalSucceeded,
		TerminalAt:        time.Now().UTC(),
		ParentCommitKey:   computeSubagentParentCommitKey(runID, "tc-full", computeSubagentStringDigest("full result")),
		ToolResultPayload: "full result",
	}
	_, _ = runStore.PrepareTerminal(runID, rec.Version, env)

	// ScanRecovery finds the terminal_prepared run
	recoverable, err := runStore.ScanRecovery()
	if err != nil {
		t.Fatalf("ScanRecovery() error = %v", err)
	}
	if len(recoverable) != 1 || recoverable[0].Identity.SubagentRunID != runID {
		t.Fatalf("ScanRecovery returned unexpected results: %v", recoverable)
	}

	// Run recovery
	service := &Service{store: convStore, subagentRuns: runStore}
	for _, r := range recoverable {
		if err := service.recoverSubagentTerminalPrepared(r); err != nil {
			t.Fatalf("recoverSubagentTerminalPrepared() error = %v", err)
		}
	}

	// Verify end state
	runRec, _ := runStore.LoadRun(runID)
	if runRec.Status != SubagentRunParentCommitted {
		t.Fatalf("run status = %q, want parent_committed", runRec.Status)
	}
	conv, _ := convStore.LoadConversation(convID)
	if conv == nil || countKind(conv.Entries, "tool_result") != 1 {
		t.Fatalf("parent conversation entry not written correctly")
	}
}

// ─────────────────────────────────────────────
// 重复 terminal category 测试
// ─────────────────────────────────────────────

func TestAppendSubagentToolResultIdempotent_TerminalCategories(t *testing.T) {
	categories := []SubagentTerminalCategory{
		SubagentTerminalSucceeded,
		SubagentTerminalCanceled,
		SubagentTerminalTimeout,
		SubagentTerminalProviderError,
		SubagentTerminalToolError,
		SubagentTerminalParentUnavailable,
		SubagentTerminalTruncated,
		SubagentTerminalProtocolError,
	}
	for _, cat := range categories {
		cat := cat
		t.Run(string(cat), func(t *testing.T) {
			historyRoot := t.TempDir()
			runStore := NewSubagentRunStore(historyRoot)
			runID := "run-" + strings.ReplaceAll(string(cat), "_", "-")

			_, err := runStore.CreateRun(SubagentIdentity{
				SubagentRunID:        runID,
				ParentConversationID: "conv-cat",
			})
			if err != nil {
				t.Fatalf("CreateRun() error = %v", err)
			}

			service := &Service{subagentRuns: runStore}
			stream := makeTestStreamWithConversation("conv-cat")
			pending := makeTestPendingExec(runID)

			if err := service.appendSubagentToolResultIdempotent(
				stream, pending, "tc-cat",
				"category result", nil, cat,
			); err != nil {
				t.Fatalf("category %q error = %v", cat, err)
			}

			rec, _ := runStore.LoadRun(runID)
			if rec.Status != SubagentRunParentCommitted {
				t.Fatalf("category %q: run status = %q", cat, rec.Status)
			}
			result, _ := runStore.LoadResult(runID)
			if result == nil {
				t.Fatalf("category %q: result.json not written", cat)
			}
			if result.TerminalCategory != cat {
				t.Fatalf("category %q: stored terminal_category = %q", cat, result.TerminalCategory)
			}
		})
	}
}

// ─────────────────────────────────────────────
// categorizeSubagentResultMsg
// ─────────────────────────────────────────────

func TestCategorizeSubagentResultMsg_Nil(t *testing.T) {
	if got := categorizeSubagentResultMsg(nil); got != SubagentTerminalProtocolError {
		t.Fatalf("nil msg: got %q, want protocol_error", got)
	}
}

func TestCategorizeSubagentResultMsg_NoSubagentResult(t *testing.T) {
	// ExecClientMessage without SubagentResult or ForceBackground → protocol_error
	msg := &agentv1.ExecClientMessage{}
	if got := categorizeSubagentResultMsg(msg); got != SubagentTerminalProtocolError {
		t.Fatalf("empty msg: got %q, want protocol_error", got)
	}
}

func TestCategorizeSubagentResultMsg_ForceBackground(t *testing.T) {
	msg := &agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_ForceBackgroundSubagentResult{
			ForceBackgroundSubagentResult: &agentv1.ForceBackgroundSubagentResult{},
		},
	}
	if got := categorizeSubagentResultMsg(msg); got != SubagentTerminalCanceled {
		t.Fatalf("force_background: got %q, want canceled", got)
	}
}

func TestCategorizeSubagentResultMsg_SuccessNormalCompletion(t *testing.T) {
	msg := &agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_SubagentResult{
			SubagentResult: &agentv1.SubagentResult{
				Result: &agentv1.SubagentResult_Success{
					Success: &agentv1.SubagentSuccess{
						BackgroundReason: agentv1.SubagentBackgroundReason_SUBAGENT_BACKGROUND_REASON_UNSPECIFIED,
					},
				},
			},
		},
	}
	if got := categorizeSubagentResultMsg(msg); got != SubagentTerminalSucceeded {
		t.Fatalf("success unspecified background: got %q, want succeeded", got)
	}
}

func TestCategorizeSubagentResultMsg_SuccessWithBackgroundReason(t *testing.T) {
	// BackgroundReason != UNSPECIFIED → treated as canceled (child was backgrounded)
	msg := &agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_SubagentResult{
			SubagentResult: &agentv1.SubagentResult{
				Result: &agentv1.SubagentResult_Success{
					Success: &agentv1.SubagentSuccess{
						BackgroundReason: agentv1.SubagentBackgroundReason_SUBAGENT_BACKGROUND_REASON_AGENT_REQUEST,
					},
				},
			},
		},
	}
	if got := categorizeSubagentResultMsg(msg); got != SubagentTerminalCanceled {
		t.Fatalf("success with background reason: got %q, want canceled", got)
	}
}

func TestCategorizeSubagentResultMsg_Error_ReturnsProtocolError_NotProviderError(t *testing.T) {
	// SubagentResult_Error has no typed source → must be protocol_error, not provider_error.
	// This is the key invariant: we do NOT guess provider_error.
	msg := &agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_SubagentResult{
			SubagentResult: &agentv1.SubagentResult{
				Result: &agentv1.SubagentResult_Error{
					Error: &agentv1.SubagentError{
						Error: "provider returned 500",
					},
				},
			},
		},
	}
	got := categorizeSubagentResultMsg(msg)
	if got == SubagentTerminalProviderError {
		t.Fatalf("SubagentResult_Error must NOT map to provider_error (got %q); use protocol_error", got)
	}
	if got != SubagentTerminalProtocolError {
		t.Fatalf("SubagentResult_Error: got %q, want protocol_error", got)
	}
}

func TestExtractSubagentErrorSummaryDoesNotPersistRawError(t *testing.T) {
	secret := "provider body contains sk-secret and user prompt"
	msg := &agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_SubagentResult{
			SubagentResult: &agentv1.SubagentResult{
				Result: &agentv1.SubagentResult_Error{
					Error: &agentv1.SubagentError{Error: secret},
				},
			},
		},
	}
	summary := extractSubagentErrorSummary(msg)
	if summary == "" {
		t.Fatal("expected controlled error summary")
	}
	if strings.Contains(summary, secret) || strings.Contains(summary, "sk-secret") || strings.Contains(summary, "user prompt") {
		t.Fatalf("summary leaked raw error: %q", summary)
	}
	if summary != "subagent_error category=protocol_error" {
		t.Fatalf("summary = %q, want controlled category", summary)
	}
}

func TestCategorizeSubagentResultMsg_Error_NilErrorField(t *testing.T) {
	// Error oneof present but inner Error pointer is nil → still protocol_error
	msg := &agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_SubagentResult{
			SubagentResult: &agentv1.SubagentResult{
				Result: &agentv1.SubagentResult_Error{
					Error: nil,
				},
			},
		},
	}
	if got := categorizeSubagentResultMsg(msg); got != SubagentTerminalProtocolError {
		t.Fatalf("nil error field: got %q, want protocol_error", got)
	}
}

// ─────────────────────────────────────────────
// 辅助函数
// ─────────────────────────────────────────────

func countKind(entries []HistoryEntry, kind string) int {
	count := 0
	for _, e := range entries {
		if e.Kind == kind {
			count++
		}
	}
	return count
}
