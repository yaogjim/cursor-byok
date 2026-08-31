package forwarder

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSubagentAttemptsStoreLifecycleAndChecksum(t *testing.T) {
	root := t.TempDir()
	store := NewSubagentRunStore(root)
	runID := "logical-run-attempts"
	created, err := store.CreateInitialAttempt(runID, SubagentSafetySnapshot{Readonly: true, SameProcess: true})
	if err != nil {
		t.Fatal(err)
	}
	if created.Version != 1 || len(created.Attempts) != 1 || created.Attempts[0].AttemptNo != 1 {
		t.Fatalf("initial attempts = %+v", created)
	}
	bound, err := store.BindAttempt(runID, created.Version, created.ActiveAttemptID, "exec-1", 11)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadAttempts(runID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Checksum == "" || loaded.Checksum != computeAttemptsChecksum(loaded) || loaded.Attempts[0].Status != SubagentAttemptBound {
		t.Fatalf("loaded attempts = %+v", loaded)
	}
	info, err := os.Stat(filepath.Join(root, subagentStoreDirName, runID, subagentAttemptsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("attempts.json mode = %o", info.Mode().Perm())
	}
	if _, err := store.BindAttempt(runID, created.Version, created.ActiveAttemptID, "exec-1", 11); err == nil {
		t.Fatal("stale CAS version unexpectedly succeeded")
	}
	if bound.Version != 2 {
		t.Fatalf("bound version = %d", bound.Version)
	}
}

func TestSubagentAttemptFenceRejectsLateResultAndHonorsBudget(t *testing.T) {
	store := NewSubagentRunStore(t.TempDir())
	record, err := store.CreateInitialAttempt("logical-run-fence", SubagentSafetySnapshot{Readonly: true, SameProcess: true})
	if err != nil {
		t.Fatal(err)
	}
	oldAttemptID := record.ActiveAttemptID
	for attemptNo := 1; attemptNo <= SubagentMaxTotalAttempts; attemptNo++ {
		execID := "exec-" + string(rune('0'+attemptNo))
		record, err = store.BindAttempt(record.SubagentRunID, record.Version, record.ActiveAttemptID, execID, uint32(attemptNo))
		if err != nil {
			t.Fatal(err)
		}
		failure := SubagentTypedFailure{SubagentRunID: record.SubagentRunID, AttemptID: record.ActiveAttemptID, ExecID: execID, Kind: SubagentFailureStreamDecode, ObservedAt: time.Now().UTC()}
		record, err = store.RecordAttemptFailure(record.SubagentRunID, record.Version, failure)
		if err != nil {
			t.Fatal(err)
		}
		if attemptNo < SubagentMaxTotalAttempts {
			record, err = store.SupersedeAttempt(record.SubagentRunID, record.Version, record.ActiveAttemptID)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := store.PrepareTerminalForAttempt(record.SubagentRunID, record.Version, oldAttemptID); !errors.Is(err, errSubagentAttemptStale) {
		t.Fatalf("late result fence error = %v", err)
	}
	if _, err := store.SupersedeAttempt(record.SubagentRunID, record.Version, record.ActiveAttemptID); !errors.Is(err, errSubagentAttemptBudget) {
		t.Fatalf("budget error = %v", err)
	}
}

func TestDefaultSubagentHandoffDoesNotCreateOrFenceAttempts(t *testing.T) {
	root := t.TempDir()
	store := NewSubagentRunStore(root)
	runID := "run-default-no-attempts"
	if _, err := store.CreateRun(SubagentIdentity{SubagentRunID: runID, ParentConversationID: "conv-default", ParentToolCallID: "tc-test-1"}); err != nil {
		t.Fatal(err)
	}
	if attempts, err := store.LoadAttempts(runID); err != nil || attempts != nil {
		t.Fatalf("default CreateRun attempts = %+v, err=%v", attempts, err)
	}
	service := &Service{subagentRuns: store}
	stream := makeTestStreamWithConversation("conv-default")
	pending := makeTestPendingExec(runID)
	pending.SubagentAttemptID = "untrusted-attempt"
	pending.SubagentAttemptNo = 2
	if err := service.appendSubagentToolResultIdempotent(stream, pending, pending.ToolCallID, "done", nil, SubagentTerminalSucceeded); err != nil {
		t.Fatalf("default handoff unexpectedly used attempt fence: %v", err)
	}
	if attempts, err := store.LoadAttempts(runID); err != nil || attempts != nil {
		t.Fatalf("default handoff attempts = %+v, err=%v", attempts, err)
	}
}

func TestEvaluateSubagentRescheduleFailsClosedAndAllowsTypedReadonly(t *testing.T) {
	attempts := &SubagentAttemptsRecord{
		SubagentRunID:   "run-policy",
		MaxAttempts:     SubagentMaxTotalAttempts,
		ActiveAttemptID: "attempt-1",
		Safety:          SubagentSafetySnapshot{Readonly: true, SameProcess: true},
		Attempts:        []SubagentAttemptRecord{{AttemptID: "attempt-1", AttemptNo: 1, Status: SubagentAttemptBound, ExecID: "exec-1"}},
	}
	if got := EvaluateSubagentReschedule(attempts, nil); got.Allowed || got.Reason != "typed_evidence_unavailable" {
		t.Fatalf("missing evidence decision = %+v", got)
	}
	failure := &SubagentTypedFailure{SubagentRunID: "run-policy", AttemptID: "attempt-1", ExecID: "exec-1", Kind: SubagentFailureStreamDecode}
	if got := EvaluateSubagentReschedule(attempts, failure); !got.Allowed {
		t.Fatalf("typed stream_decode decision = %+v", got)
	}
	idleFailure := *failure
	idleFailure.Kind = SubagentFailureStreamIdleTimeout
	if got := EvaluateSubagentReschedule(attempts, &idleFailure); !got.Allowed {
		t.Fatalf("typed stream_idle_timeout decision = %+v", got)
	}
	broadFailure := *failure
	broadFailure.Kind = SubagentFailureKind("transport")
	if got := EvaluateSubagentReschedule(attempts, &broadFailure); got.Allowed || got.Reason != "failure_kind_not_retryable" {
		t.Fatalf("broad failure decision = %+v", got)
	}
	attempts.Safety.Readonly = false
	if got := EvaluateSubagentReschedule(attempts, failure); got.Allowed || got.Reason != "unsafe_task_snapshot" {
		t.Fatalf("mutable task decision = %+v", got)
	}
}

func TestSubagentFailureRegistryRequiresExactTypedCorrelation(t *testing.T) {
	registry := NewSubagentFailureRegistry()
	failure := SubagentTypedFailure{SubagentRunID: "run-registry", AttemptID: "attempt-1", ExecID: "exec-1", Kind: SubagentFailureStreamIdleTimeout}
	if !registry.Register(failure) || registry.Register(failure) {
		t.Fatal("registry did not enforce one typed evidence item per exact key")
	}
	if registry.Register(SubagentTypedFailure{SubagentRunID: "run-registry", AttemptID: "attempt-wide", ExecID: "exec-wide", Kind: SubagentFailureKind("transport")}) {
		t.Fatal("registry accepted broad transport failure kind")
	}
	if _, ok := registry.Consume("run-registry", "attempt-1", "wrong-exec"); ok {
		t.Fatal("registry matched an inexact correlation")
	}
	got, ok := registry.Consume("run-registry", "attempt-1", "exec-1")
	if !ok || got.Kind != SubagentFailureStreamIdleTimeout {
		t.Fatalf("consume = %+v, %v", got, ok)
	}
	if _, ok := registry.Consume("run-registry", "attempt-1", "exec-1"); ok {
		t.Fatal("typed evidence was not consumed exactly once")
	}
}

func TestSubagentAttemptsChecksumTamperFailsClosed(t *testing.T) {
	root := t.TempDir()
	store := NewSubagentRunStore(root)
	runID := "logical-run-corrupt-attempts"
	if _, err := store.CreateInitialAttempt(runID, SubagentSafetySnapshot{Readonly: true, SameProcess: true}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, subagentStoreDirName, runID, subagentAttemptsFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	data[len(data)-2] ^= 1
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadAttempts(runID); err == nil {
		t.Fatal("tampered attempts.json unexpectedly loaded")
	}
}

func TestPrepareSubagentRelaunchArgsRemovesResumeIdentity(t *testing.T) {
	got, ok := prepareSubagentRelaunchArgs([]byte(`{"readonly":true,"prompt":"p","resume":"agent-old"}`))
	if !ok {
		t.Fatal("readonly relaunch args rejected")
	}
	snapshot := subagentSafetySnapshotFromArgs(got)
	if !snapshot.Readonly || snapshot.HasResumeID {
		t.Fatalf("relaunch snapshot = %+v; args=%s", snapshot, got)
	}
	if _, ok := prepareSubagentRelaunchArgs([]byte(`{"readonly":false}`)); ok {
		t.Fatal("mutable relaunch args accepted")
	}
}
