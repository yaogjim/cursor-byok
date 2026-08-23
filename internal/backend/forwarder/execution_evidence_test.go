package forwarder

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
)

const (
	evidenceReasoningCanary  = "EVIDENCE_REASONING_CANARY_DO_NOT_PERSIST"
	evidenceStdoutCanary     = "EVIDENCE_STDOUT_CANARY_PASSING_TESTS"
	evidenceFileBodyCanary   = "package secret\nfunc leakCredentials() {}"
	evidenceCredentialCanary = "Authorization: Bearer sk-live-evidence-canary"
	evidencePathCanary       = "/Users/secret/project/credentials.env"
	evidenceCommandCanary    = "go test ./internal/backend/forwarder -count=1 -timeout 600s"
	evidenceCookieCanary     = "Set-Cookie: session=evidence-cookie-canary"
)

func TestBuildExecutionEvidenceTypedSuccess(t *testing.T) {
	record, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:     1,
		RequestID:   "request-1",
		ModelCallID: "model-call-1",
		ToolCallID:  "call-write-1",
		ToolName:    "Write",
		ArgsJSON:    []byte(`{"path":"` + evidencePathCanary + `","contents":"` + evidenceFileBodyCanary + `"}`),
		ToolCall:    evidenceEditSuccessToolCall(evidencePathCanary),
		Sequence:    7,
	})
	if !ok {
		t.Fatal("typed Write success should produce evidence")
	}
	if record.SchemaVersion != executionEvidenceSchemaVersion {
		t.Fatalf("schema_version = %d, want %d", record.SchemaVersion, executionEvidenceSchemaVersion)
	}
	if record.ToolCategory != executionEvidenceCategoryMutation || record.ToolKind != executionEvidenceToolKindWrite {
		t.Fatalf("category/kind = %s/%s, want mutation/write", record.ToolCategory, record.ToolKind)
	}
	if record.TerminalStatus != executionEvidenceTerminalSuccess || !record.Successful {
		t.Fatalf("terminal = %s successful=%v, want success/true", record.TerminalStatus, record.Successful)
	}
	if record.EvidenceID == "" || record.Sequence != 7 {
		t.Fatalf("identity missing: id=%q seq=%d", record.EvidenceID, record.Sequence)
	}
}

func TestBuildExecutionEvidenceTypedFailure(t *testing.T) {
	record, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-write-fail",
		ToolName:   "Write",
		ToolCall:   evidenceEditErrorToolCall(evidencePathCanary, "disk full"),
		Sequence:   8,
	})
	if !ok {
		t.Fatal("typed Write failure should produce diagnostic evidence")
	}
	if record.Successful || record.TerminalStatus != executionEvidenceTerminalFailed {
		t.Fatalf("failed mutation terminal = %s successful=%v", record.TerminalStatus, record.Successful)
	}
	if record.ToolCategory != executionEvidenceCategoryMutation {
		t.Fatalf("category = %s, want mutation", record.ToolCategory)
	}
	summary := summarizeExecutionEvidence(applyEvidence(record))
	if summary.MutationToolCount != 0 {
		t.Fatalf("failed mutation counted as success: %+v", summary)
	}
}

func TestBuildExecutionEvidencePendingDoesNotRecord(t *testing.T) {
	_, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:      1,
		RequestID:    "request-1",
		ToolCallID:   "call-pending",
		ToolName:     "Write",
		ToolCall:     evidenceEditSuccessToolCall(evidencePathCanary),
		TerminalHint: executionEvidenceHintPending,
		ResultText:   "I successfully wrote " + evidencePathCanary,
		Reasoning:    evidenceReasoningCanary,
	})
	if ok {
		t.Fatal("pending tool must not produce evidence")
	}
}

func TestBuildExecutionEvidenceCanceledRecordsUnsuccessful(t *testing.T) {
	record, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-canceled",
		ToolName:   "Shell",
		ArgsJSON:   []byte(`{"command":"` + evidenceCommandCanary + `"}`),
		ToolCall:   evidenceShellAbortedToolCall(evidenceCommandCanary, evidenceStdoutCanary),
	})
	if !ok {
		t.Fatal("canceled shell should produce unsuccessful evidence")
	}
	if record.Successful || record.TerminalStatus != executionEvidenceTerminalCanceled {
		t.Fatalf("canceled terminal = %s successful=%v", record.TerminalStatus, record.Successful)
	}
	if record.ToolCategory != executionEvidenceCategoryVerification {
		t.Fatalf("category = %s, want verification", record.ToolCategory)
	}
}

func TestBuildExecutionEvidenceStartedAndTransportClosedDoNotRecord(t *testing.T) {
	for _, hint := range []string{executionEvidenceHintStarted, executionEvidenceHintTransportClosed} {
		_, ok := buildExecutionEvidence(executionEvidenceInput{
			TurnSeq:      1,
			RequestID:    "request-1",
			ToolCallID:   "call-" + hint,
			ToolName:     "Write",
			ToolCall:     evidenceEditSuccessToolCall(evidencePathCanary),
			TerminalHint: hint,
		})
		if ok {
			t.Fatalf("hint %q must not produce evidence", hint)
		}
	}
}

func TestBuildExecutionEvidenceAwaitPatternMatchWithoutExitIsNonterminal(t *testing.T) {
	record, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-await-matched",
		ToolName:   "AwaitShell",
		ArgsJSON:   []byte(`{"shell_id":"shell-matched"}`),
		ToolCall:   evidenceAwaitCompleteToolCall("shell-matched", nil),
		ResultText: "pattern matched, process still running",
	})
	if ok {
		t.Fatalf("AwaitResult_Complete without exit_code recorded evidence: %+v", record)
	}

	record, ok = buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-await-running",
		ToolName:   "AwaitShell",
		ToolCall:   evidenceAwaitStillRunningToolCall("shell-running"),
		ResultText: "still running",
	})
	if ok {
		t.Fatalf("AwaitResult_StillRunning recorded evidence: %+v", record)
	}
}

func TestBuildExecutionEvidenceAwaitExitZeroIsSuccessfulVerification(t *testing.T) {
	exit := int32(0)
	record, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:         1,
		RequestID:       "request-1",
		ToolCallID:      "call-await-zero",
		ToolName:        "AwaitShell",
		ArgsJSON:        []byte(`{"shell_id":"shell-complete-zero"}`),
		CommandOverride: "go test ./internal/backend/forwarder",
		ToolCall:        evidenceAwaitCompleteToolCall("shell-complete-zero", &exit),
		ResultText:      "ok",
	})
	if !ok {
		t.Fatal("AwaitShell exit_code=0 should record successful verification")
	}
	if record.ToolCategory != executionEvidenceCategoryVerification || record.VerificationKind != executionEvidenceVerificationTest {
		t.Fatalf("category/kind = %s/%s, want verification/test", record.ToolCategory, record.VerificationKind)
	}
	if !record.Successful || record.TerminalStatus != executionEvidenceTerminalSuccess {
		t.Fatalf("terminal = %s successful=%v, want success/true", record.TerminalStatus, record.Successful)
	}
}

func TestBuildExecutionEvidenceAwaitExitNonZeroIsFailed(t *testing.T) {
	exit := int32(1)
	record, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:         1,
		RequestID:       "request-1",
		ToolCallID:      "call-await-fail",
		ToolName:        "AwaitShell",
		CommandOverride: "go test ./internal/backend/forwarder",
		ToolCall:        evidenceAwaitCompleteToolCall("shell-complete-fail", &exit),
	})
	if !ok {
		t.Fatal("AwaitShell non-zero exit should record unsuccessful verification")
	}
	if record.Successful || record.TerminalStatus != executionEvidenceTerminalFailed {
		t.Fatalf("terminal = %s successful=%v, want failed/false", record.TerminalStatus, record.Successful)
	}
	summary := summarizeExecutionEvidence(applyEvidence(record))
	if summary.VerificationCommandCount != 0 {
		t.Fatalf("failed AwaitShell counted as verification: %+v", summary)
	}
}

func TestBuildExecutionEvidenceAssistantTextNeverRecords(t *testing.T) {
	_, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ResultText: "I edited " + evidencePathCanary + " and ran " + evidenceCommandCanary,
		Reasoning:  evidenceReasoningCanary,
	})
	if ok {
		t.Fatal("assistant text must never produce execution evidence")
	}
}

func TestBuildExecutionEvidenceWithoutTypedTerminalIsUnknownUnsuccessful(t *testing.T) {
	record, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-untyped",
		ToolName:   "Write",
		ResultText: "successfully wrote " + evidencePathCanary,
		Reasoning:  evidenceReasoningCanary,
	})
	if !ok {
		t.Fatal("untyped terminal should still record unsuccessful/unknown evidence")
	}
	if record.Successful || record.TerminalStatus != executionEvidenceTerminalUnknown {
		t.Fatalf("untyped terminal = %s successful=%v, want unknown/false", record.TerminalStatus, record.Successful)
	}
	summary := summarizeExecutionEvidence(applyEvidence(record))
	if summary.MutationToolCount != 0 {
		t.Fatalf("natural-language success was upgraded: %+v", summary)
	}
}

func TestBuildExecutionEvidenceNeutralUntypedDoesNotRecordFailure(t *testing.T) {
	for _, name := range []string{"Read", "Grep", "Glob", "Ls", "WebSearch", "GenerateImage", "TodoWrite"} {
		record, ok := buildExecutionEvidence(executionEvidenceInput{
			TurnSeq:    1,
			RequestID:  "request-1",
			ToolCallID: "call-neutral-" + name,
			ToolName:   name,
			ResultText: "successfully completed " + evidencePathCanary,
		})
		if ok {
			t.Fatalf("%s without typed terminal recorded evidence: %+v", name, record)
		}
	}

	record, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:      1,
		RequestID:    "request-1",
		ToolCallID:   "call-read-failed-hint",
		ToolName:     "Read",
		ResultText:   "read failed",
		TerminalHint: executionEvidenceHintFailed,
	})
	if !ok {
		t.Fatal("explicit failed hint on Read should still record unsuccessful evidence")
	}
	if record.Successful || record.TerminalStatus != executionEvidenceTerminalFailed {
		t.Fatalf("failed Read hint = %s successful=%v, want failed/false", record.TerminalStatus, record.Successful)
	}
	summary := summarizeExecutionEvidence(applyEvidence(record))
	if summary.MutationToolCount != 0 || summary.VerificationCommandCount != 0 {
		t.Fatalf("neutral failed hint must not count as mutation/verification: %+v", summary)
	}
}

func TestExecutionEvidenceMutationThenVerificationIsPresent(t *testing.T) {
	index := applyEvidence(
		mustEvidence(t, executionEvidenceInput{
			TurnSeq:    1,
			RequestID:  "request-1",
			ToolCallID: "call-write",
			ToolName:   "Write",
			ToolCall:   evidenceEditSuccessToolCall("main.go"),
			Sequence:   4,
		}),
		mustEvidence(t, executionEvidenceInput{
			TurnSeq:    1,
			RequestID:  "request-1",
			ToolCallID: "call-test",
			ToolName:   "Shell",
			ArgsJSON:   []byte(`{"command":"go test ./internal/backend/forwarder"}`),
			ToolCall:   evidenceShellSuccessToolCall("go test ./internal/backend/forwarder", 0),
			Sequence:   6,
		}),
	)
	summary := summarizeExecutionEvidence(index)
	if summary.MutationToolCount != 1 || summary.VerificationCommandCount != 1 {
		t.Fatalf("counts = %+v", summary)
	}
	if summary.VerificationStale || summary.VerificationEvidence != executionEvidenceVerificationPresent {
		t.Fatalf("verification = %+v", summary)
	}
	if summary.VerificationEvidenceID == "" {
		t.Fatal("present verification missing stable evidence id")
	}
}

func TestExecutionEvidenceVerificationThenMutationIsStale(t *testing.T) {
	index := applyEvidence(
		mustEvidence(t, executionEvidenceInput{
			TurnSeq:    1,
			RequestID:  "request-1",
			ToolCallID: "call-test",
			ToolName:   "Shell",
			ArgsJSON:   []byte(`{"command":"go test ./..."}`),
			ToolCall:   evidenceShellSuccessToolCall("go test ./...", 0),
			Sequence:   3,
		}),
		mustEvidence(t, executionEvidenceInput{
			TurnSeq:    1,
			RequestID:  "request-1",
			ToolCallID: "call-write",
			ToolName:   "PatchEdit",
			ToolCall:   evidenceEditSuccessToolCall("main.go"),
			Sequence:   5,
		}),
	)
	summary := summarizeExecutionEvidence(index)
	if !summary.VerificationStale || summary.VerificationEvidence != executionEvidenceVerificationStale {
		t.Fatalf("expected stale after later mutation: %+v", summary)
	}
	if summary.MutationToolCount != 1 || summary.VerificationCommandCount != 1 {
		t.Fatalf("counts = %+v", summary)
	}
}

func TestExecutionEvidenceSameOrEarlierSequenceVerificationIsInvalid(t *testing.T) {
	same := applyEvidence(
		mustEvidence(t, executionEvidenceInput{
			TurnSeq: 1, RequestID: "request-1", ToolCallID: "mut", ToolName: "Delete",
			ToolCall: evidenceDeleteSuccessToolCall("gone.go"), Sequence: 9,
		}),
		mustEvidence(t, executionEvidenceInput{
			TurnSeq: 1, RequestID: "request-1", ToolCallID: "ver", ToolName: "Shell",
			ArgsJSON: []byte(`{"command":"go vet ./..."}`),
			ToolCall: evidenceShellSuccessToolCall("go vet ./...", 0), Sequence: 9,
		}),
	)
	summary := summarizeExecutionEvidence(same)
	if summary.VerificationEvidence != executionEvidenceVerificationStale || !summary.VerificationStale {
		t.Fatalf("same-sequence verification must be stale: %+v", summary)
	}

	earlier := applyEvidence(
		mustEvidence(t, executionEvidenceInput{
			TurnSeq: 1, RequestID: "request-1", ToolCallID: "ver-early", ToolName: "Shell",
			ArgsJSON: []byte(`{"command":"npm run lint"}`),
			ToolCall: evidenceShellSuccessToolCall("npm run lint", 0), Sequence: 2,
		}),
		mustEvidence(t, executionEvidenceInput{
			TurnSeq: 1, RequestID: "request-1", ToolCallID: "mut-late", ToolName: "Write",
			ToolCall: evidenceEditSuccessToolCall("main.go"), Sequence: 8,
		}),
	)
	summary = summarizeExecutionEvidence(earlier)
	if summary.VerificationEvidence != executionEvidenceVerificationStale {
		t.Fatalf("earlier verification must be stale: %+v", summary)
	}
}

func TestExecutionEvidenceNewVerificationRestoresPresent(t *testing.T) {
	index := applyEvidence(
		mustEvidence(t, executionEvidenceInput{
			TurnSeq: 1, RequestID: "request-1", ToolCallID: "ver-old", ToolName: "Shell",
			ArgsJSON: []byte(`{"command":"go test ./..."}`),
			ToolCall: evidenceShellSuccessToolCall("go test ./...", 0), Sequence: 2,
		}),
		mustEvidence(t, executionEvidenceInput{
			TurnSeq: 1, RequestID: "request-1", ToolCallID: "mut", ToolName: "Write",
			ToolCall: evidenceEditSuccessToolCall("main.go"), Sequence: 4,
		}),
		mustEvidence(t, executionEvidenceInput{
			TurnSeq: 1, RequestID: "request-1", ToolCallID: "ver-new", ToolName: "AwaitShell",
			ArgsJSON:        []byte(`{"shell_id":"shell-1"}`),
			CommandOverride: "go test ./internal/backend/forwarder",
			ToolCall:        evidenceAwaitSuccessToolCall(0),
			Sequence:        6,
		}),
	)
	summary := summarizeExecutionEvidence(index)
	if summary.VerificationStale || summary.VerificationEvidence != executionEvidenceVerificationPresent {
		t.Fatalf("new verification should restore present: %+v", summary)
	}
	if summary.VerificationCommandCount != 2 || summary.MutationToolCount != 1 {
		t.Fatalf("counts = %+v", summary)
	}
	if !strings.Contains(summary.VerificationEvidenceID, "ver-new") {
		t.Fatalf("stable verification id missing: %+v", summary)
	}
}

func TestExecutionEvidenceUnknownMCPDoesNotUpgrade(t *testing.T) {
	record, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-mcp",
		ToolName:   "CallMcpTool",
		ArgsJSON:   []byte(`{"server":"fs","toolName":"write_file","arguments":{"path":"` + evidencePathCanary + `"}}`),
		ToolCall:   evidenceMCPSuccessToolCall(),
	})
	if !ok {
		t.Fatal("MCP terminal should record unknown evidence")
	}
	if record.ToolCategory != executionEvidenceCategoryUnknown {
		t.Fatalf("MCP upgraded: category=%s", record.ToolCategory)
	}
	summary := summarizeExecutionEvidence(applyEvidence(record))
	if summary.MutationToolCount != 0 || summary.VerificationCommandCount != 0 {
		t.Fatalf("unknown MCP counted as mutation/verification: %+v", summary)
	}
}

func TestExecutionEvidenceUnknownToolDoesNotUpgrade(t *testing.T) {
	record, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-mystery",
		ToolName:   "MysteryMutate",
		ToolCall:   evidenceEditSuccessToolCall(evidencePathCanary),
	})
	if !ok {
		t.Fatal("unknown tool should still record unknown evidence")
	}
	if record.ToolCategory != executionEvidenceCategoryUnknown {
		t.Fatalf("unknown tool upgraded: %+v", record)
	}
}

func TestExecutionEvidenceShellWithoutControlledCommandIsNeutral(t *testing.T) {
	record, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-ls",
		ToolName:   "Shell",
		ArgsJSON:   []byte(`{"command":"ls -la"}`),
		ToolCall:   evidenceShellSuccessToolCall("ls -la", 0),
	})
	if !ok {
		t.Fatal("successful ls should record evidence")
	}
	if record.ToolCategory != executionEvidenceCategoryNeutral || record.VerificationKind != "" {
		t.Fatalf("ls upgraded to verification: %+v", record)
	}
	summary := summarizeExecutionEvidence(applyEvidence(record))
	if summary.VerificationCommandCount != 0 {
		t.Fatalf("neutral shell counted as verification: %+v", summary)
	}
}

func TestExecutionEvidenceDuplicateResultIsIdempotent(t *testing.T) {
	first := mustEvidence(t, executionEvidenceInput{
		TurnSeq: 1, RequestID: "request-1", ToolCallID: "call-write", ToolName: "Write",
		ToolCall: evidenceEditSuccessToolCall("main.go"), Sequence: 3,
	})
	dup := mustEvidence(t, executionEvidenceInput{
		TurnSeq: 1, RequestID: "request-1", ToolCallID: "call-write", ToolName: "Write",
		ToolCall: evidenceEditSuccessToolCall("main.go"), Sequence: 9,
	})
	if first.EvidenceID != dup.EvidenceID {
		t.Fatalf("evidence id unstable: %q vs %q", first.EvidenceID, dup.EvidenceID)
	}
	if executionEvidenceIdempotencyKey(first.TurnSeq, first.RequestID, first.ToolCallID) != executionEvidenceIdempotencyKey(dup.TurnSeq, dup.RequestID, dup.ToolCallID) {
		t.Fatal("idempotency key unstable within turn/request/tool-call")
	}
	index := applyEvidence(first, dup)
	summary := summarizeExecutionEvidence(index)
	if summary.MutationToolCount != 1 {
		t.Fatalf("duplicate success counted twice: %+v", summary)
	}
}

func TestExecutionEvidenceIDDoesNotDedupeAcrossTurns(t *testing.T) {
	turn1 := mustEvidence(t, executionEvidenceInput{
		TurnSeq: 1, RequestID: "request-1", ToolCallID: "shared-call", ToolName: "Write",
		ToolCall: evidenceEditSuccessToolCall("main.go"), Sequence: 3,
	})
	turn2 := mustEvidence(t, executionEvidenceInput{
		TurnSeq: 2, RequestID: "request-2", ToolCallID: "shared-call", ToolName: "Write",
		ToolCall: evidenceEditSuccessToolCall("main.go"), Sequence: 8,
	})
	if turn1.EvidenceID == turn2.EvidenceID {
		t.Fatalf("cross-turn evidence id collided: %q", turn1.EvidenceID)
	}
	key1 := executionEvidenceIdempotencyKey(turn1.TurnSeq, turn1.RequestID, turn1.ToolCallID)
	key2 := executionEvidenceIdempotencyKey(turn2.TurnSeq, turn2.RequestID, turn2.ToolCallID)
	if key1 == key2 {
		t.Fatalf("cross-turn idempotency key collided: %q", key1)
	}
	summary := summarizeExecutionEvidence(applyEvidence(turn1, turn2))
	if summary.MutationToolCount != 2 {
		t.Fatalf("cross-turn false dedupe: %+v", summary)
	}
}

func TestRebuildExecutionEvidenceFromHistoryMatchesSummary(t *testing.T) {
	mutation := mustEvidence(t, executionEvidenceInput{
		TurnSeq: 2, RequestID: "request-2", ModelCallID: "mc-2", ToolCallID: "call-write",
		ToolName: "Write", ToolCall: evidenceEditSuccessToolCall("main.go"), Sequence: 11,
	})
	verification := mustEvidence(t, executionEvidenceInput{
		TurnSeq: 2, RequestID: "request-2", ModelCallID: "mc-2", ToolCallID: "call-test",
		ToolName: "Shell", ArgsJSON: []byte(`{"command":"go test ./..."}`),
		ToolCall: evidenceShellSuccessToolCall("go test ./...", 0), Sequence: 13,
	})
	live := summarizeExecutionEvidence(applyEvidence(mutation, verification))
	entries := []HistoryEntry{
		newAssistantTextEntry(2, "request-2", "I edited files", evidenceReasoningCanary, "sig"),
		newExecutionEvidenceEntry(mutation),
		newExecutionEvidenceEntry(verification),
	}
	rebuilt := summarizeExecutionEvidence(rebuildExecutionEvidenceIndex(entries, 2))
	if live != rebuilt {
		t.Fatalf("restart summary mismatch\nlive=%+v\nrebuilt=%+v", live, rebuilt)
	}
}

func TestRebuildExecutionEvidenceOldHistoryIsUnknown(t *testing.T) {
	entries := []HistoryEntry{
		newAssistantTextEntry(1, "request-1", "done", "", ""),
		newToolResultEntry(1, "request-1", "call-1", "Write", `{"path":"main.go"}`, "wrote file", "", nil),
	}
	summary := summarizeExecutionEvidence(rebuildExecutionEvidenceIndex(entries, 1))
	if summary.VerificationEvidence != executionEvidenceVerificationUnknown {
		t.Fatalf("legacy history without ledger = %q, want unknown", summary.VerificationEvidence)
	}
	if summary.MutationToolCount != 0 || summary.VerificationCommandCount != 0 || summary.VerificationEvidenceID != "" {
		t.Fatalf("legacy history forged evidence: %+v", summary)
	}
}

func TestExecutionEvidencePrivacyCanary(t *testing.T) {
	record := mustEvidence(t, executionEvidenceInput{
		TurnSeq:         1,
		RequestID:       "request-1",
		ModelCallID:     "model-call-1",
		ToolCallID:      "call-private",
		ToolName:        "Write",
		ArgsJSON:        []byte(`{"path":"` + evidencePathCanary + `","contents":"` + evidenceFileBodyCanary + `","token":"` + evidenceCredentialCanary + `"}`),
		ResultText:      evidenceStdoutCanary + "\n" + evidenceCookieCanary,
		Reasoning:       evidenceReasoningCanary,
		ToolCall:        evidenceEditSuccessToolCall(evidencePathCanary),
		Sequence:        4,
		CommandOverride: evidenceCommandCanary,
	})
	entry := newExecutionEvidenceEntry(record)
	encoded, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal evidence entry: %v", err)
	}
	payload, err := json.Marshal(record)
	if err != nil {
		t.Fatalf("marshal evidence record: %v", err)
	}
	blob := string(encoded) + string(payload)
	for _, canary := range []string{
		evidenceReasoningCanary,
		evidenceStdoutCanary,
		evidenceFileBodyCanary,
		evidenceCredentialCanary,
		evidencePathCanary,
		evidenceCommandCanary,
		evidenceCookieCanary,
		"sk-live-evidence-canary",
	} {
		if strings.Contains(blob, canary) {
			t.Fatalf("evidence JSON leaked %q: %s", canary, blob)
		}
	}
	assertExecutionEvidenceWhitelist(t, entry)
}

func TestAppendToolResultWritesEvidenceInSameTransaction(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	toolCall := evidenceEditSuccessToolCall(evidencePathCanary)
	if err := service.appendToolResult(stream, "call-write", "Write", []byte(`{"path":"`+evidencePathCanary+`","contents":"`+evidenceFileBodyCanary+`"}`), evidenceFileBodyCanary, evidenceReasoningCanary, toolCall, "model-call-1"); err != nil {
		t.Fatalf("appendToolResult() error = %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if countKind(conversation.Entries, "tool_result") != 1 {
		t.Fatalf("tool_result count = %d, want 1", countKind(conversation.Entries, "tool_result"))
	}
	evidenceEntries := executionEvidenceEntries(conversation.Entries)
	if len(evidenceEntries) != 1 {
		t.Fatalf("evidence count = %d, want 1", len(evidenceEntries))
	}
	assertExecutionEvidenceWhitelist(t, evidenceEntries[0])
	summary := summarizeExecutionEvidence(stream.ExecutionEvidence)
	if summary.MutationToolCount != 1 || summary.VerificationEvidence != executionEvidenceVerificationAbsent {
		t.Fatalf("live summary = %+v", summary)
	}

	if err := service.appendToolResult(stream, "call-write", "Write", []byte(`{"path":"`+evidencePathCanary+`"}`), "duplicate", evidenceReasoningCanary, toolCall, "model-call-1"); err != nil {
		t.Fatalf("duplicate appendToolResult() error = %v", err)
	}
	conversation, _, _, err = service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("duplicate snapshot error = %v", err)
	}
	if len(executionEvidenceEntries(conversation.Entries)) != 1 {
		t.Fatalf("duplicate evidence count = %d, want 1", len(executionEvidenceEntries(conversation.Entries)))
	}

	rebuilt := rebuildExecutionEvidenceIndex(conversation.Entries, stream.TurnSeq)
	if summarizeExecutionEvidence(rebuilt) != summarizeExecutionEvidence(stream.ExecutionEvidence) {
		t.Fatalf("restart rebuild mismatch live=%+v rebuilt=%+v", stream.ExecutionEvidence, rebuilt)
	}
	assertPersistedEvidenceSequence(t, executionEvidenceEntries(conversation.Entries)[0])
}

func TestAppendToolResultPendingHintDoesNotWriteEvidence(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	if err := service.appendToolResultWithEvidence(stream, "call-pending", "Write", nil, "pending", "", evidenceEditSuccessToolCall("main.go"), "model-call-1", executionEvidenceHintPending, ""); err != nil {
		t.Fatalf("appendToolResultWithEvidence() error = %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if countKind(conversation.Entries, "tool_result") != 1 {
		t.Fatalf("tool_result should still persist, got %d", countKind(conversation.Entries, "tool_result"))
	}
	if len(executionEvidenceEntries(conversation.Entries)) != 0 {
		t.Fatalf("pending result wrote evidence: %+v", conversation.Entries)
	}
}

func TestAppendToolResultTransportClosedDoesNotWriteEvidence(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	if err := service.appendToolResultWithEvidence(stream, "call-closed", "Write", []byte(`{"path":"`+evidencePathCanary+`"}`), "transport closed before terminal result arrived", evidenceReasoningCanary, evidenceEditSuccessToolCall(evidencePathCanary), "model-call-1", executionEvidenceHintTransportClosed, ""); err != nil {
		t.Fatalf("appendToolResultWithEvidence() error = %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if countKind(conversation.Entries, "tool_result") != 1 {
		t.Fatalf("tool_result should still persist, got %d", countKind(conversation.Entries, "tool_result"))
	}
	if len(executionEvidenceEntries(conversation.Entries)) != 0 {
		t.Fatalf("transport-closed result wrote evidence: %+v", conversation.Entries)
	}
	if summarizeExecutionEvidence(stream.ExecutionEvidence).MutationToolCount != 0 {
		t.Fatalf("transport-closed counted as successful mutation: %+v", stream.ExecutionEvidence)
	}
}

func TestRecoverNonStreamingExecAfterStreamCloseDoesNotWriteEvidence(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	pending := runtimecore.PendingExec{
		ToolCallID:  "call-closed-write",
		ExecKind:    "write",
		ArgsJSON:    []byte(`{"path":"` + evidencePathCanary + `","contents":"` + evidenceFileBodyCanary + `"}`),
		ModelCallID: "model-call-1",
	}
	if err := service.recoverNonStreamingExecAfterStreamClose(stream, pending); err != nil {
		t.Fatalf("recoverNonStreamingExecAfterStreamClose() error = %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if countKind(conversation.Entries, "tool_result") != 1 {
		t.Fatalf("tool_result count = %d, want 1", countKind(conversation.Entries, "tool_result"))
	}
	if len(executionEvidenceEntries(conversation.Entries)) != 0 {
		t.Fatalf("transport-closed recovery wrote evidence: %+v", conversation.Entries)
	}
	if summarizeExecutionEvidence(stream.ExecutionEvidence).MutationToolCount != 0 {
		t.Fatalf("transport-closed recovery counted success: %+v", stream.ExecutionEvidence)
	}
}

func TestRecoverShellWithoutTerminalDoesNotWriteSuccessEvidence(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	pending := runtimecore.PendingExec{
		ToolCallID:  "call-shell-stalled",
		ExecKind:    "shell",
		ArgsJSON:    []byte(`{"command":"` + evidenceCommandCanary + `"}`),
		ModelCallID: "model-call-1",
	}
	if err := service.recoverShellWithoutTerminal(stream, pending, shellRecoveryReasonTransportClosed); err != nil {
		t.Fatalf("recoverShellWithoutTerminal() error = %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if len(executionEvidenceEntries(conversation.Entries)) != 0 {
		t.Fatalf("stalled shell wrote evidence: %+v", conversation.Entries)
	}
	summary := summarizeExecutionEvidence(stream.ExecutionEvidence)
	if summary.VerificationCommandCount != 0 || summary.MutationToolCount != 0 {
		t.Fatalf("stalled shell counted as success: %+v", summary)
	}
}

func TestAppendEntriesStampsExecutionEvidenceSequence(t *testing.T) {
	record := mustEvidence(t, executionEvidenceInput{
		TurnSeq: 1, RequestID: "request-1", ToolCallID: "call-write", ToolName: "Write",
		ToolCall: evidenceEditSuccessToolCall("main.go"), Sequence: 99,
	})
	conversation := &ConversationFile{NextTurnSeq: 2, NextEntrySeq: 11}
	assigned := appendEntriesInPlace(conversation, []HistoryEntry{newExecutionEvidenceEntry(record)})
	if len(assigned) != 1 {
		t.Fatalf("assigned count = %d, want 1", len(assigned))
	}
	if assigned[0].Seq != 11 {
		t.Fatalf("entry.Seq = %d, want 11", assigned[0].Seq)
	}
	assertPersistedEvidenceSequence(t, assigned[0])
}

func TestAppendToolResultSequenceMatchesPersistedMetadataSeq(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	if err := service.appendToolResult(stream, "call-write", "Write", []byte(`{"path":"main.go"}`), "ok", "", evidenceEditSuccessToolCall("main.go"), "model-call-1"); err != nil {
		t.Fatalf("appendToolResult() error = %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	evidenceEntries := executionEvidenceEntries(conversation.Entries)
	if len(evidenceEntries) != 1 {
		t.Fatalf("evidence count = %d, want 1", len(evidenceEntries))
	}
	assertPersistedEvidenceSequence(t, evidenceEntries[0])
	var toolResultSeq int64
	for _, entry := range conversation.Entries {
		if entry.Kind == "tool_result" && entry.ToolCallID == "call-write" {
			toolResultSeq = entry.Seq
		}
	}
	if toolResultSeq == 0 || evidenceEntries[0].Seq != toolResultSeq+1 {
		t.Fatalf("evidence seq %d must be the next monotonic seq after tool_result %d", evidenceEntries[0].Seq, toolResultSeq)
	}
	decoded, ok := decodeExecutionEvidence(evidenceEntries[0])
	if !ok {
		t.Fatal("decode persisted evidence failed")
	}
	if decoded.Sequence != evidenceEntries[0].Seq {
		t.Fatalf("decoded sequence = %d, want entry.Seq %d", decoded.Sequence, evidenceEntries[0].Seq)
	}
}

func TestAppendToolResultShellVerificationOmitsCommandFromJSON(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	if err := service.appendToolResult(stream, "call-test", "Shell", []byte(`{"command":"`+evidenceCommandCanary+`"}`), evidenceStdoutCanary, evidenceReasoningCanary, evidenceShellSuccessToolCall(evidenceCommandCanary, 0), "model-call-1"); err != nil {
		t.Fatalf("appendToolResult() error = %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	evidenceEntries := executionEvidenceEntries(conversation.Entries)
	if len(evidenceEntries) != 1 {
		t.Fatalf("evidence count = %d, want 1", len(evidenceEntries))
	}
	record, ok := decodeExecutionEvidence(evidenceEntries[0])
	if !ok {
		t.Fatal("decode shell evidence failed")
	}
	if record.ToolCategory != executionEvidenceCategoryVerification || record.VerificationKind != executionEvidenceVerificationTest {
		t.Fatalf("shell command not classified in memory: %+v", record)
	}
	assertExecutionEvidenceWhitelist(t, evidenceEntries[0])
	blob := string(evidenceEntries[0].Payload)
	for _, leaked := range []string{evidenceCommandCanary, evidenceStdoutCanary, evidenceReasoningCanary, `"command"`, `"args"`, `"result"`, `"reasoning"`, `"path"`} {
		if strings.Contains(blob, leaked) {
			t.Fatalf("persistent evidence leaked %q: %s", leaked, blob)
		}
	}
}

func TestAppendToolResultCrossTurnSameToolCallIDIsNotDeduped(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	if err := service.appendToolResult(stream, "shared-call", "Write", []byte(`{"path":"a.go"}`), "ok", "", evidenceEditSuccessToolCall("a.go"), "model-call-1"); err != nil {
		t.Fatalf("turn 1 append: %v", err)
	}
	stream.TurnSeq = 2
	stream.RequestID = "request-2"
	if err := service.appendToolResult(stream, "shared-call", "Write", []byte(`{"path":"b.go"}`), "ok", "", evidenceEditSuccessToolCall("b.go"), "model-call-2"); err != nil {
		t.Fatalf("turn 2 append: %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	allEvidence := executionEvidenceEntries(conversation.Entries)
	if len(allEvidence) != 2 {
		t.Fatalf("cross-turn evidence count = %d, want 2", len(allEvidence))
	}
	first, _ := decodeExecutionEvidence(allEvidence[0])
	second, _ := decodeExecutionEvidence(allEvidence[1])
	if first.EvidenceID == second.EvidenceID {
		t.Fatalf("persisted evidence id collided across turns: %q", first.EvidenceID)
	}
	turn2 := summarizeExecutionEvidence(rebuildExecutionEvidenceIndex(conversation.Entries, 2))
	if turn2.MutationToolCount != 1 {
		t.Fatalf("turn 2 rebuild = %+v, want one mutation", turn2)
	}
}

func TestAppendToolResultMutationThenVerificationStaleAndRestore(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	if err := service.appendToolResult(stream, "call-test-1", "Shell", []byte(`{"command":"go test ./..."}`), evidenceStdoutCanary, evidenceReasoningCanary, evidenceShellSuccessToolCall("go test ./...", 0), "model-call-1"); err != nil {
		t.Fatalf("first verification: %v", err)
	}
	summary := summarizeExecutionEvidence(stream.ExecutionEvidence)
	if summary.VerificationEvidence != executionEvidenceVerificationPresent {
		t.Fatalf("initial verification = %+v", summary)
	}
	if err := service.appendToolResult(stream, "call-write", "Write", []byte(`{"path":"main.go"}`), "ok", "", evidenceEditSuccessToolCall("main.go"), "model-call-1"); err != nil {
		t.Fatalf("mutation: %v", err)
	}
	summary = summarizeExecutionEvidence(stream.ExecutionEvidence)
	if summary.VerificationEvidence != executionEvidenceVerificationStale || !summary.VerificationStale {
		t.Fatalf("after mutation want stale, got %+v", summary)
	}
	if err := service.appendToolResult(stream, "call-test-2", "Shell", []byte(`{"command":"go test ./internal/backend/forwarder"}`), "ok", "", evidenceShellSuccessToolCall("go test ./internal/backend/forwarder", 0), "model-call-1"); err != nil {
		t.Fatalf("restoring verification: %v", err)
	}
	summary = summarizeExecutionEvidence(stream.ExecutionEvidence)
	if summary.VerificationEvidence != executionEvidenceVerificationPresent || summary.VerificationStale {
		t.Fatalf("restored verification = %+v", summary)
	}
	if summary.MutationToolCount != 1 || summary.VerificationCommandCount != 2 {
		t.Fatalf("counts = %+v", summary)
	}

	cloned := cloneConversationFile(stream.CheckpointConversation)
	restart := &ActiveStream{TurnSeq: stream.TurnSeq}
	if err := service.replaceCheckpointConversation(restart, cloned); err != nil {
		t.Fatalf("replaceCheckpointConversation() error = %v", err)
	}
	if summarizeExecutionEvidence(restart.ExecutionEvidence) != summary {
		t.Fatalf("restart summary = %+v want %+v", restart.ExecutionEvidence, summary)
	}
}

func TestAppendSubagentReplayDoesNotDuplicateEvidence(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	runID := "run-evidence-replay"
	if _, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: "conv-evidence-replay",
		ParentToolCallID:     "tc-task",
		ParentRequestID:      "req-test",
		ParentTurnSeq:        1,
		ParentModelCallID:    "model-call-1",
	}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	service := &Service{subagentRuns: runStore}
	stream := makeTestStreamWithConversation("conv-evidence-replay")
	pending := makeTestPendingExec(runID)
	pending.ToolCallID = "tc-task"
	pending.ModelCallID = "model-call-1"
	payload := "subagent completed without " + evidenceStdoutCanary
	if err := service.appendSubagentToolResultIdempotent(stream, pending, "tc-task", payload, nil, SubagentTerminalSucceeded); err != nil {
		t.Fatalf("first subagent append: %v", err)
	}
	if err := service.appendSubagentToolResultIdempotent(stream, pending, "tc-task", payload, nil, SubagentTerminalSucceeded); err != nil {
		t.Fatalf("duplicate subagent append: %v", err)
	}
	stream.mu.Lock()
	conversation := cloneConversationFile(stream.CheckpointConversation)
	stream.mu.Unlock()
	if countKind(conversation.Entries, "tool_result") != 1 {
		t.Fatalf("tool_result count = %d, want 1", countKind(conversation.Entries, "tool_result"))
	}
	evidenceEntries := executionEvidenceEntries(conversation.Entries)
	if len(evidenceEntries) != 1 {
		t.Fatalf("subagent evidence count = %d, want 1", len(evidenceEntries))
	}
	record, ok := decodeExecutionEvidence(evidenceEntries[0])
	if !ok {
		t.Fatal("decode subagent evidence failed")
	}
	if record.ToolCategory != executionEvidenceCategoryNeutral {
		t.Fatalf("Task evidence = %+v, want neutral", record)
	}
	assertExecutionEvidenceWhitelist(t, evidenceEntries[0])
	assertPersistedEvidenceSequence(t, evidenceEntries[0])
}

func TestRecoverSubagentReplayWritesEvidenceOnce(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	convStore := NewConversationFileStore(historyRoot)
	runID := "run-evidence"
	convID := "conv-evidence"
	if _, err := convStore.CreateConversation(convID, agentv1.AgentMode_AGENT_MODE_AGENT, "", "", convID); err != nil {
		t.Fatalf("CreateConversation() error = %v", err)
	}
	rec, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: convID,
		ParentToolCallID:     "tc-recover-ev",
		ParentRequestID:      "req-recover-ev",
		ParentTurnSeq:        1,
		ParentModelCallID:    "mc-recover",
	})
	if err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	env := &SubagentResultEnvelope{
		SubagentRunID:     runID,
		TerminalCategory:  SubagentTerminalSucceeded,
		ToolName:          "Task",
		ParentCommitKey:   computeSubagentParentCommitKey(runID, "tc-recover-ev", "digest-ev"),
		ToolResultPayload: "recovery " + evidenceStdoutCanary,
	}
	if _, err := runStore.PrepareTerminal(runID, rec.Version, env); err != nil {
		t.Fatalf("PrepareTerminal() error = %v", err)
	}
	service := &Service{store: convStore, subagentRuns: runStore}
	record, _ := runStore.LoadRun(runID)
	for i := 0; i < 2; i++ {
		if err := service.recoverSubagentTerminalPrepared(record); err != nil {
			t.Fatalf("recover %d: %v", i+1, err)
		}
	}
	conv, err := convStore.LoadConversation(convID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if countKind(conv.Entries, "tool_result") != 1 {
		t.Fatalf("tool_result count = %d, want 1", countKind(conv.Entries, "tool_result"))
	}
	evidenceEntries := executionEvidenceEntries(conv.Entries)
	if len(evidenceEntries) != 1 {
		t.Fatalf("recovery evidence count = %d, want 1", len(evidenceEntries))
	}
	assertExecutionEvidenceWhitelist(t, evidenceEntries[0])
	assertPersistedEvidenceSequence(t, evidenceEntries[0])
}

func TestClassifyVerificationCommandControlledKinds(t *testing.T) {
	cases := map[string]string{
		"go test ./internal/backend/forwarder -count=1": executionEvidenceVerificationTest,
		"go build ./...":          executionEvidenceVerificationBuild,
		"go vet ./internal/...":   executionEvidenceVerificationVet,
		"golangci-lint run":       executionEvidenceVerificationLint,
		"npm run check":           executionEvidenceVerificationCheck,
		"cd tmp && go test ./...": executionEvidenceVerificationTest,
		"test":                    executionEvidenceVerificationTest,
		"test:unit":               executionEvidenceVerificationTest,
		"npm test":                executionEvidenceVerificationTest,
		"npm run test":            executionEvidenceVerificationTest,
		"npm run test:unit":       executionEvidenceVerificationTest,
		"make test":               executionEvidenceVerificationTest,
		"task test:unit":          executionEvidenceVerificationTest,
		"build":                   executionEvidenceVerificationBuild,
		"npm run build":           executionEvidenceVerificationBuild,
		"lint":                    executionEvidenceVerificationLint,
		"npm run lint":            executionEvidenceVerificationLint,
		"vet":                     executionEvidenceVerificationVet,
		"check":                   executionEvidenceVerificationCheck,
		"ls -la":                  "",
		"echo go test passed":     "",
		"test-setup":              "",
		"test-data":               "",
		"npm run test-setup":      "",
		"npm run test-data":       "",
		"make test-setup":         "",
		"task test-data":          "",
		"build-cache":             "",
		"npm run build-cache":     "",
		"make build-cache":        "",
		"lint-config":             "",
		"npm run lint-config":     "",
		"vet-helper":              "",
		"check-env":               "",
		"pytest-cache":            "",
	}
	for command, want := range cases {
		if got := classifyVerificationCommand(command); got != want {
			t.Fatalf("classifyVerificationCommand(%q) = %q, want %q", command, got, want)
		}
	}
}

func TestBuildExecutionEvidenceMCPIsErrorIsFailed(t *testing.T) {
	record, ok := buildExecutionEvidence(executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-mcp-error",
		ToolName:   "CallMcpTool",
		ToolCall:   evidenceMCPErrorToolCall(),
	})
	if !ok {
		t.Fatal("MCP is_error should record failed unknown evidence")
	}
	if record.Successful || record.TerminalStatus != executionEvidenceTerminalFailed {
		t.Fatalf("MCP is_error terminal = %s successful=%v", record.TerminalStatus, record.Successful)
	}
	if record.ToolCategory != executionEvidenceCategoryUnknown {
		t.Fatalf("MCP is_error upgraded: category=%s", record.ToolCategory)
	}
	summary := summarizeExecutionEvidence(applyEvidence(record))
	if summary.MutationToolCount != 0 || summary.VerificationCommandCount != 0 {
		t.Fatalf("MCP is_error counted as mutation/verification: %+v", summary)
	}
}

func TestAppendToolResultMCPIsErrorDoesNotCountAsVerification(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	if err := service.appendToolResult(stream, "call-mcp-error", "CallMcpTool", []byte(`{"server":"fs","toolName":"write_file"}`), evidenceStdoutCanary, evidenceReasoningCanary, evidenceMCPErrorToolCall(), "model-call-1"); err != nil {
		t.Fatalf("appendToolResult() error = %v", err)
	}
	summary := summarizeExecutionEvidence(stream.ExecutionEvidence)
	if summary.MutationToolCount != 0 || summary.VerificationCommandCount != 0 {
		t.Fatalf("MCP is_error live counts = %+v", summary)
	}
	if summary.VerificationEvidence != executionEvidenceVerificationAbsent {
		t.Fatalf("MCP is_error verification = %q, want absent", summary.VerificationEvidence)
	}
}

func TestAppendAwaitShellPatternMatchDoesNotWriteEvidence(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	setBackgroundShellCommand(stream, "shell-await-1", "go test ./internal/backend/forwarder", backgroundShellStatusRunning, nil)
	if err := service.appendToolResult(stream, "call-await-matched", "AwaitShell", []byte(`{"shell_id":"shell-await-1"}`), "pattern matched", "", evidenceAwaitCompleteToolCall("shell-await-1", nil), "model-call-1"); err != nil {
		t.Fatalf("appendToolResult() error = %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if countKind(conversation.Entries, "tool_result") != 1 {
		t.Fatalf("tool_result count = %d, want 1", countKind(conversation.Entries, "tool_result"))
	}
	if len(executionEvidenceEntries(conversation.Entries)) != 0 {
		t.Fatalf("pattern-matched AwaitShell wrote evidence: %+v", conversation.Entries)
	}
	summary := summarizeExecutionEvidence(stream.ExecutionEvidence)
	if summary.VerificationCommandCount != 0 || summary.MutationToolCount != 0 {
		t.Fatalf("pattern-matched AwaitShell counted as success: %+v", summary)
	}
}

func TestAppendAwaitShellExitZeroUsesBackgroundCommand(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	exit := int32(0)
	setBackgroundShellCommand(stream, "shell-await-2", "go test ./internal/backend/forwarder", backgroundShellStatusCompleted, &exit)
	if err := service.appendToolResult(stream, "call-await-zero", "AwaitShell", []byte(`{"shell_id":"shell-await-2"}`), "ok", "", evidenceAwaitCompleteToolCall("shell-await-2", &exit), "model-call-1"); err != nil {
		t.Fatalf("appendToolResult() error = %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	evidenceEntries := executionEvidenceEntries(conversation.Entries)
	if len(evidenceEntries) != 1 {
		t.Fatalf("evidence count = %d, want 1", len(evidenceEntries))
	}
	record, ok := decodeExecutionEvidence(evidenceEntries[0])
	if !ok {
		t.Fatal("decode AwaitShell evidence failed")
	}
	if record.ToolCategory != executionEvidenceCategoryVerification || record.VerificationKind != executionEvidenceVerificationTest {
		t.Fatalf("background command not classified: %+v", record)
	}
	if !record.Successful || record.TerminalStatus != executionEvidenceTerminalSuccess {
		t.Fatalf("exit 0 AwaitShell = %+v, want success", record)
	}
	assertExecutionEvidenceWhitelist(t, evidenceEntries[0])
}

func TestAppendNeutralUntypedDoesNotWriteFailedEvidence(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	if err := service.appendToolResult(stream, "call-read-1", "Read", []byte(`{"path":"main.go"}`), "file contents", "", nil, "model-call-1"); err != nil {
		t.Fatalf("appendToolResult() error = %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if countKind(conversation.Entries, "tool_result") != 1 {
		t.Fatalf("tool_result count = %d, want 1", countKind(conversation.Entries, "tool_result"))
	}
	if len(executionEvidenceEntries(conversation.Entries)) != 0 {
		t.Fatalf("untyped Read wrote evidence: %+v", conversation.Entries)
	}
	if stream.ExecutionEvidence.HasLedger {
		t.Fatalf("untyped Read opened a ledger: %+v", stream.ExecutionEvidence)
	}
}

func TestHandleAwaitShellPatternMatchDoesNotWriteEvidence(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	stream.mu.Lock()
	stream.Status = StreamStatusCompleted
	stream.mu.Unlock()
	setBackgroundShellCommand(stream, "7", "go test ./internal/backend/forwarder", backgroundShellStatusRunning, nil)
	stream.mu.Lock()
	stream.BackgroundShells["7"].StdoutBuffer = "ok\nPASS\n"
	stream.mu.Unlock()

	err := service.handleAwaitShellToolInvocation(stream, runtimecore.ToolInvocation{
		CallID:   "call-await-pattern",
		ToolName: "AwaitShell",
		ArgsJSON: []byte(`{"shell_id":"7","pattern":"PASS","block_until_ms":0}`),
	})
	if err != nil {
		t.Fatalf("handleAwaitShellToolInvocation() error = %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	if len(executionEvidenceEntries(conversation.Entries)) != 0 {
		t.Fatalf("pattern-matched AwaitShell invocation wrote evidence: %+v", conversation.Entries)
	}
}

func TestHandleAwaitShellExitZeroRecordsVerification(t *testing.T) {
	service, stream := testExecutionEvidenceStream(t)
	stream.mu.Lock()
	stream.Status = StreamStatusCompleted
	stream.mu.Unlock()
	exit := int32(0)
	setBackgroundShellCommand(stream, "8", "go test ./internal/backend/forwarder", backgroundShellStatusCompleted, &exit)
	stream.mu.Lock()
	stream.BackgroundShells["8"].StdoutBuffer = "ok\nPASS\n"
	stream.mu.Unlock()

	err := service.handleAwaitShellToolInvocation(stream, runtimecore.ToolInvocation{
		CallID:   "call-await-complete",
		ToolName: "AwaitShell",
		ArgsJSON: []byte(`{"shell_id":"8","block_until_ms":0}`),
	})
	if err != nil {
		t.Fatalf("handleAwaitShellToolInvocation() error = %v", err)
	}
	conversation, _, _, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		t.Fatalf("snapshotCheckpointConversation() error = %v", err)
	}
	evidenceEntries := executionEvidenceEntries(conversation.Entries)
	if len(evidenceEntries) != 1 {
		t.Fatalf("completed AwaitShell evidence count = %d, want 1", len(evidenceEntries))
	}
	record, ok := decodeExecutionEvidence(evidenceEntries[0])
	if !ok {
		t.Fatal("decode completed AwaitShell evidence failed")
	}
	if record.ToolCategory != executionEvidenceCategoryVerification || record.VerificationKind != executionEvidenceVerificationTest {
		t.Fatalf("completed AwaitShell = %+v, want verification/test", record)
	}
	if !record.Successful {
		t.Fatalf("exit 0 AwaitShell was not successful: %+v", record)
	}
}

func TestBuildAwaitShellProtoResultPatternMatchLeavesExitNil(t *testing.T) {
	result := buildAwaitShellProtoResult(awaitShellResult{
		ShellID:  "shell-matched",
		Status:   backgroundShellStatusRunning,
		Matched:  true,
		ExitCode: nil,
	})
	complete := result.GetComplete()
	if complete == nil {
		t.Fatalf("pattern match proto result = %T, want Complete", result.GetResult())
	}
	if complete.ExitCode != nil {
		t.Fatalf("pattern match Complete.ExitCode = %v, want nil", *complete.ExitCode)
	}
}

func TestAppendSubagentCanceledEvidenceIsUnsuccessful(t *testing.T) {
	historyRoot := t.TempDir()
	runStore := NewSubagentRunStore(historyRoot)
	runID := "run-evidence-canceled"
	if _, err := runStore.CreateRun(SubagentIdentity{
		SubagentRunID:        runID,
		ParentConversationID: "conv-evidence-canceled",
		ParentToolCallID:     "tc-task-canceled",
		ParentRequestID:      "req-test",
		ParentTurnSeq:        1,
		ParentModelCallID:    "model-call-1",
	}); err != nil {
		t.Fatalf("CreateRun() error = %v", err)
	}
	service := &Service{subagentRuns: runStore}
	stream := makeTestStreamWithConversation("conv-evidence-canceled")
	pending := makeTestPendingExec(runID)
	pending.ToolCallID = "tc-task-canceled"
	pending.ModelCallID = "model-call-1"
	if err := service.appendSubagentToolResultIdempotent(stream, pending, "tc-task-canceled", "canceled", nil, SubagentTerminalCanceled); err != nil {
		t.Fatalf("append canceled subagent: %v", err)
	}
	stream.mu.Lock()
	conversation := cloneConversationFile(stream.CheckpointConversation)
	stream.mu.Unlock()
	evidenceEntries := executionEvidenceEntries(conversation.Entries)
	if len(evidenceEntries) != 1 {
		t.Fatalf("canceled subagent evidence count = %d, want 1", len(evidenceEntries))
	}
	record, ok := decodeExecutionEvidence(evidenceEntries[0])
	if !ok {
		t.Fatal("decode canceled subagent evidence failed")
	}
	if record.Successful || record.TerminalStatus != executionEvidenceTerminalCanceled {
		t.Fatalf("canceled subagent = %+v", record)
	}
	if record.ToolCategory != executionEvidenceCategoryNeutral {
		t.Fatalf("Task canceled category = %s, want neutral", record.ToolCategory)
	}
	assertExecutionEvidenceWhitelist(t, evidenceEntries[0])
}

func mustEvidence(t *testing.T, input executionEvidenceInput) executionEvidenceRecord {
	t.Helper()
	record, ok := buildExecutionEvidence(input)
	if !ok {
		t.Fatalf("expected evidence for %+v", input)
	}
	return record
}

func applyEvidence(records ...executionEvidenceRecord) executionEvidenceIndex {
	var index executionEvidenceIndex
	for _, record := range records {
		applyExecutionEvidence(&index, record)
	}
	return index
}

func evidenceEditSuccessToolCall(path string) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_EditToolCall{
			EditToolCall: &agentv1.EditToolCall{
				Result: &agentv1.EditResult{
					Result: &agentv1.EditResult_Success{
						Success: &agentv1.EditSuccess{Path: path, AfterFullFileContent: evidenceFileBodyCanary},
					},
				},
			},
		},
	}
}

func evidenceEditErrorToolCall(path string, message string) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_EditToolCall{
			EditToolCall: &agentv1.EditToolCall{
				Result: buildEditErrorResult(path, message),
			},
		},
	}
}

func evidenceDeleteSuccessToolCall(path string) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_DeleteToolCall{
			DeleteToolCall: &agentv1.DeleteToolCall{
				Result: &agentv1.DeleteResult{
					Result: &agentv1.DeleteResult_Success{
						Success: &agentv1.DeleteSuccess{Path: path, DeletedFile: evidenceFileBodyCanary},
					},
				},
			},
		},
	}
}

func evidenceShellSuccessToolCall(command string, exitCode int32) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ShellToolCall{
			ShellToolCall: &agentv1.ShellToolCall{
				Args: &agentv1.ShellArgs{Command: command},
				Result: &agentv1.ShellResult{
					Result: &agentv1.ShellResult_Success{
						Success: &agentv1.ShellSuccess{Command: command, ExitCode: exitCode, Stdout: evidenceStdoutCanary},
					},
				},
			},
		},
	}
}

func evidenceShellAbortedToolCall(command string, stdout string) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ShellToolCall{
			ShellToolCall: &agentv1.ShellToolCall{
				Args: &agentv1.ShellArgs{Command: command},
				Result: &agentv1.ShellResult{
					Result: &agentv1.ShellResult_Failure{
						Failure: &agentv1.ShellFailure{Command: command, ExitCode: 130, Stdout: stdout, Aborted: true},
					},
				},
			},
		},
	}
}

func evidenceAwaitSuccessToolCall(exitCode int32) *agentv1.ToolCall {
	return evidenceAwaitCompleteToolCall("shell-1", &exitCode)
}

func evidenceAwaitCompleteToolCall(taskID string, exitCode *int32) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_AwaitToolCall{
			AwaitToolCall: &agentv1.AwaitToolCall{
				Args: &agentv1.AwaitArgs{TaskId: taskID},
				Result: &agentv1.AwaitResult{
					Result: &agentv1.AwaitResult_Complete{
						Complete: &agentv1.AwaitTaskComplete{TaskId: taskID, ExitCode: exitCode},
					},
				},
			},
		},
	}
}

func evidenceAwaitStillRunningToolCall(taskID string) *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_AwaitToolCall{
			AwaitToolCall: &agentv1.AwaitToolCall{
				Args: &agentv1.AwaitArgs{TaskId: taskID},
				Result: &agentv1.AwaitResult{
					Result: &agentv1.AwaitResult_StillRunning{
						StillRunning: &agentv1.AwaitTaskStillRunning{TaskId: taskID},
					},
				},
			},
		},
	}
}

func setBackgroundShellCommand(stream *ActiveStream, shellID string, command string, status string, exitCode *int32) {
	if stream == nil {
		return
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.BackgroundShells == nil {
		stream.BackgroundShells = make(map[string]*BackgroundShellState)
	}
	state := stream.BackgroundShells[shellID]
	if state == nil {
		state = &BackgroundShellState{ShellID: shellID}
		stream.BackgroundShells[shellID] = state
	}
	state.Command = command
	state.Status = status
	state.ExitCode = exitCode
}

func evidenceMCPSuccessToolCall() *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_McpToolCall{
			McpToolCall: &agentv1.McpToolCall{
				Result: &agentv1.McpToolResult{
					Result: &agentv1.McpToolResult_Success{
						Success: &agentv1.McpSuccess{},
					},
				},
			},
		},
	}
}

func evidenceMCPErrorToolCall() *agentv1.ToolCall {
	return &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_McpToolCall{
			McpToolCall: &agentv1.McpToolCall{
				Result: &agentv1.McpToolResult{
					Result: &agentv1.McpToolResult_Success{
						Success: &agentv1.McpSuccess{IsError: true},
					},
				},
			},
		},
	}
}

func testExecutionEvidenceStream(t *testing.T) (*Service, *ActiveStream) {
	t.Helper()
	service, stream, _ := testCheckpointBlobProjection(t)
	return service, stream
}

func executionEvidenceEntries(entries []HistoryEntry) []HistoryEntry {
	found := make([]HistoryEntry, 0)
	for _, entry := range entries {
		if _, ok := decodeExecutionEvidence(entry); ok {
			found = append(found, entry)
		}
	}
	return found
}

func assertExecutionEvidenceWhitelist(t *testing.T, entry HistoryEntry) {
	t.Helper()
	var payload metadataPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("decode metadata payload: %v", err)
	}
	if payload.Type != executionEvidencePayloadType {
		t.Fatalf("payload type = %q, want %s", payload.Type, executionEvidencePayloadType)
	}
	allowed := map[string]struct{}{
		"schema_version":    {},
		"evidence_id":       {},
		"turn_seq":          {},
		"request_id":        {},
		"model_call_id":     {},
		"tool_call_id":      {},
		"sequence":          {},
		"tool_category":     {},
		"tool_kind":         {},
		"verification_kind": {},
		"terminal_status":   {},
		"successful":        {},
	}
	for key := range payload.Value {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("unexpected evidence field %q in %s", key, entry.Payload)
		}
	}
	encoded, err := json.Marshal(payload.Value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	if bytes.Contains(encoded, []byte(evidenceCommandCanary)) {
		t.Fatalf("command leaked in evidence value: %s", encoded)
	}
}

func assertPersistedEvidenceSequence(t *testing.T, entry HistoryEntry) {
	t.Helper()
	if entry.Seq <= 0 {
		t.Fatalf("persisted evidence missing seq: %+v", entry)
	}
	var payload metadataPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		t.Fatalf("decode metadata payload: %v", err)
	}
	raw, ok := payload.Value["sequence"]
	if !ok {
		t.Fatalf("persisted evidence missing sequence field: %s", entry.Payload)
	}
	got, ok := jsonNumberAsInt64(raw)
	if !ok {
		t.Fatalf("persisted sequence %v (%T) is not a number", raw, raw)
	}
	if got != entry.Seq {
		t.Fatalf("payload sequence = %d, want entry.Seq %d", got, entry.Seq)
	}
}

func jsonNumberAsInt64(value any) (int64, bool) {
	switch typed := value.(type) {
	case float64:
		return int64(typed), true
	case json.Number:
		parsed, err := typed.Int64()
		return parsed, err == nil
	case int64:
		return typed, true
	case int:
		return int64(typed), true
	default:
		return 0, false
	}
}
