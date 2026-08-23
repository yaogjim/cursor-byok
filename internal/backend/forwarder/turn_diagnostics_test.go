package forwarder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

const diagnosticReasoningCanary = "STAGE5_PRIVATE_REASONING_CANARY_DO_NOT_EMIT"

func TestStableReasoningHashIsStableAndOmitsPlaintext(t *testing.T) {
	t.Parallel()
	texts := []string{diagnosticReasoningCanary, evidenceStdoutCanary}
	first := stableReasoningHash(texts)
	second := stableReasoningHash(append([]string(nil), texts...))
	if first != second || first == "" || first == turnDiagnosticReasoningAbsent {
		t.Fatalf("reasoning hash unstable or empty: %q %q", first, second)
	}
	if len(first) != 64 {
		t.Fatalf("reasoning hash length = %d, want 64 hex chars: %q", len(first), first)
	}
	if _, err := hex.DecodeString(first); err != nil {
		t.Fatalf("reasoning hash is not hex: %v", err)
	}
	sum := sha256.New()
	for _, text := range texts {
		_, _ = sum.Write([]byte(text))
		sum.Write([]byte{0})
	}
	if first != hex.EncodeToString(sum.Sum(nil)) {
		t.Fatalf("reasoning hash drifted from canonical SHA-256: %q", first)
	}
	blob := first + " " + strings.Join(texts, " ")
	if strings.Contains(first, diagnosticReasoningCanary) || strings.Contains(first, evidenceStdoutCanary) {
		t.Fatalf("reasoning hash retained plaintext: %q", first)
	}
	_ = blob
	if stableReasoningHash(nil) != turnDiagnosticReasoningAbsent {
		t.Fatalf("empty reasoning hash = %q, want %s", stableReasoningHash(nil), turnDiagnosticReasoningAbsent)
	}
	if stableReasoningHash([]string{"  ", ""}) != turnDiagnosticReasoningAbsent {
		t.Fatal("whitespace-only reasoning should be absent")
	}
}

func TestCountReasoningEmitsDetectsPublicTranscriptLeak(t *testing.T) {
	t.Parallel()
	texts := []string{diagnosticReasoningCanary}
	if got := countReasoningEmitsInTranscript([]byte(`{"role":"assistant","message":{"content":[{"type":"text","text":"safe"}]}}`), texts); got != 0 {
		t.Fatalf("clean transcript emit count = %d, want 0", got)
	}
	leaked := []byte(`{"role":"assistant","message":{"content":[{"type":"text","text":"` + diagnosticReasoningCanary + `"}]}}` + "\n")
	if got := countReasoningEmitsInTranscript(leaked, texts); got == 0 {
		t.Fatal("leaked transcript emit count stayed 0")
	}
}

func TestPublicTranscriptReasoningEmitCountMustBeZero(t *testing.T) {
	t.Parallel()
	conversation := transcriptTestConversation([]HistoryEntry{
		newAssistantTextEntry(1, "request-1", "visible answer", diagnosticReasoningCanary, "sig-1"),
		newToolCallEntry(1, "request-1", "call-1", "Write", diagnosticReasoningCanary, "sig-1", transcriptTestEditToolCall(t, evidencePathCanary)),
		newToolResultEntry(1, "request-1", "call-1", "Write", `{"path":"`+evidencePathCanary+`"}`, evidenceFileBodyCanary, diagnosticReasoningCanary, transcriptTestEditToolCall(t, evidencePathCanary)),
	})
	diag := buildTurnDiagnostics(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "what does this function do?",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries:        conversation.Entries,
	}, conversation)
	if diag.TranscriptReasoningEmitCount != 0 {
		t.Fatalf("public transcript reasoning emit count = %d, want 0", diag.TranscriptReasoningEmitCount)
	}
	data, err := projectCursorTranscriptJSONL(conversation)
	if err != nil {
		t.Fatalf("projectCursorTranscriptJSONL() error = %v", err)
	}
	assertPublicTranscriptOmitsInternalDiagnostics(t, data)
	assertPublicTranscriptKeepsStructuredToolUsePath(t, data, evidencePathCanary)
}

func TestBuildTurnDiagnosticsOldHistoryIsAbsentOrUnknown(t *testing.T) {
	t.Parallel()
	entries := []HistoryEntry{
		newAssistantTextEntry(1, "request-1", "hello", "", ""),
		newMetadataEntry(1, "request-1", "turn_completed", map[string]any{"model_call_id": "legacy-call"}),
	}
	diag := buildTurnDiagnostics(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "hello",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries:        entries,
	}, &ConversationFile{Entries: entries})
	if diag.ReasoningHash != turnDiagnosticReasoningAbsent {
		t.Fatalf("old history reasoning_hash = %q, want absent", diag.ReasoningHash)
	}
	if diag.VerificationEvidence != executionEvidenceVerificationUnknown {
		t.Fatalf("old history verification_evidence = %q, want unknown", diag.VerificationEvidence)
	}
	if diag.MutationToolCount != 0 || diag.VerificationCommandCount != 0 || diag.VerificationStale {
		t.Fatalf("old history counts = %+v", diag)
	}
	if diag.CompletionGateStatus != completionGateStatusNotApplicable {
		t.Fatalf("old history gate status = %q, want not_applicable", diag.CompletionGateStatus)
	}

	decoded := decodeTurnDiagnosticsFromValue(map[string]any{"model_call_id": "legacy-call"})
	if decoded.ReasoningHash != turnDiagnosticReasoningAbsent || decoded.VerificationEvidence != executionEvidenceVerificationUnknown || decoded.CompletionGateStatus != turnDiagnosticStatusAbsent {
		t.Fatalf("decoded legacy turn_completed = %+v", decoded)
	}
}

func TestBuildTurnDiagnosticsCountsAndGateStatus(t *testing.T) {
	t.Parallel()
	mutation := mustEvidence(t, executionEvidenceInput{
		TurnSeq:    1,
		RequestID:  "request-1",
		ToolCallID: "call-write-1",
		ToolName:   "Write",
		ToolCall:   evidenceEditSuccessToolCall(evidencePathCanary),
		Sequence:   10,
	})
	staleVerification := mustEvidence(t, executionEvidenceInput{
		TurnSeq:         1,
		RequestID:       "request-1",
		ToolCallID:      "call-test-early",
		ToolName:        "Shell",
		ArgsJSON:        []byte(`{"command":"` + evidenceCommandCanary + `"}`),
		ToolCall:        evidenceShellSuccessToolCall(evidenceCommandCanary, 0),
		CommandOverride: evidenceCommandCanary,
		Sequence:        9,
	})
	entries := []HistoryEntry{
		newAssistantTextEntry(1, "request-1", "I edited "+evidencePathCanary, diagnosticReasoningCanary, "sig"),
		stampExecutionEvidenceSequence(func() HistoryEntry {
			entry := newExecutionEvidenceEntry(staleVerification)
			entry.Seq = 9
			return entry
		}()),
		stampExecutionEvidenceSequence(func() HistoryEntry {
			entry := newExecutionEvidenceEntry(mutation)
			entry.Seq = 10
			return entry
		}()),
		newCompletionGateEntry(completionGateRecord{
			SchemaVersion:            completionGateSchemaVersion,
			Status:                   completionGateStatusInsufficientAfterRetry,
			RetryCount:               1,
			Gaps:                     []string{completionGateGapMissingLaterVerification},
			TurnSeq:                  1,
			RequestID:                "request-1",
			MutationToolCount:        1,
			VerificationCommandCount: 1,
			VerificationStale:        true,
			VerificationEvidence:     executionEvidenceVerificationStale,
			VerificationEvidenceID:   staleVerification.EvidenceID,
		}),
	}
	diag := buildTurnDiagnostics(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "请修改超时",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries:        entries,
	}, &ConversationFile{Entries: entries})
	if diag.MutationToolCount != 1 || diag.VerificationCommandCount != 1 {
		t.Fatalf("counts = mutation=%d verification=%d", diag.MutationToolCount, diag.VerificationCommandCount)
	}
	if !diag.VerificationStale || diag.VerificationEvidence != executionEvidenceVerificationStale {
		t.Fatalf("stale evidence = %+v", diag)
	}
	if diag.VerificationEvidenceID != staleVerification.EvidenceID {
		t.Fatalf("evidence id = %q, want %q", diag.VerificationEvidenceID, staleVerification.EvidenceID)
	}
	if diag.CompletionGateStatus != completionGateStatusInsufficientAfterRetry || diag.CompletionGateRetryCount != 1 {
		t.Fatalf("gate status = %s retry=%d", diag.CompletionGateStatus, diag.CompletionGateRetryCount)
	}
	if len(diag.CompletionGateGaps) != 1 || diag.CompletionGateGaps[0] != completionGateGapMissingLaterVerification {
		t.Fatalf("gaps = %v", diag.CompletionGateGaps)
	}
	if diag.ReasoningHash == turnDiagnosticReasoningAbsent || strings.Contains(diag.ReasoningHash, diagnosticReasoningCanary) {
		t.Fatalf("reasoning hash = %q", diag.ReasoningHash)
	}
	assertTurnDiagnosticWhitelist(t, turnDiagnosticValueMap(diag))
	assertNoDiagnosticSensitiveCanaries(t, "diagnostic value map", mustJSON(t, turnDiagnosticValueMap(diag)))
}

func TestTurnDiagnosticValueMapEnumeratesWhitelistFields(t *testing.T) {
	t.Parallel()
	values := turnCompletedValueMap("model-call-1", turnDiagnosticRecord{
		ReasoningHash:                "abc",
		TranscriptReasoningEmitCount: 0,
		MutationToolCount:            2,
		VerificationCommandCount:     1,
		VerificationStale:            false,
		VerificationEvidence:         executionEvidenceVerificationPresent,
		VerificationEvidenceID:       "ev:1:request-1:call-test",
		CompletionGateStatus:         completionGateStatusSatisfied,
		CompletionGateRetryCount:     0,
	})
	if values["model_call_id"] != "model-call-1" {
		t.Fatalf("model_call_id = %v", values["model_call_id"])
	}
	assertTurnCompletedWhitelist(t, values)
}

func TestCopyTurnDiagnosticFieldsDoesNotPassthroughMaps(t *testing.T) {
	t.Parallel()
	dst := map[string]any{
		"reasoning": evidenceReasoningCanary,
		"stdout":    evidenceStdoutCanary,
	}
	copyTurnDiagnosticFields(dst, turnDiagnosticRecord{
		ReasoningHash:            "deadbeef",
		VerificationEvidence:     executionEvidenceVerificationPresent,
		CompletionGateStatus:     completionGateStatusSatisfied,
		VerificationEvidenceID:   "ev:1:request-1:call-test",
		MutationToolCount:        1,
		VerificationCommandCount: 1,
	})
	if dst["reasoning"] != evidenceReasoningCanary {
		t.Fatal("copy mutated unrelated keys; it should only set whitelist fields")
	}
	if dst["reasoning_hash"] != "deadbeef" {
		t.Fatalf("reasoning_hash not copied field-by-field: %#v", dst)
	}
	for _, forbidden := range []string{"reasoning_content", "stdout", "stderr", "body", "payload"} {
		if _, ok := dst[forbidden]; ok && forbidden != "stdout" {
			t.Fatalf("copied unexpected key %q", forbidden)
		}
	}
}

func TestDebugRecorderTurnDiagnosticsWhitelistAndPrivacy(t *testing.T) {
	root := t.TempDir()
	capture := &debugRecorderTestCapture{}
	recorder := newDebugRecorder(root, nil, debugRecorderTestConfig("basic"), capture)
	diag := turnDiagnosticRecord{
		ReasoningHash:                stableReasoningHash([]string{diagnosticReasoningCanary}),
		TranscriptReasoningEmitCount: 0,
		MutationToolCount:            1,
		VerificationCommandCount:     1,
		VerificationStale:            false,
		VerificationEvidence:         executionEvidenceVerificationPresent,
		VerificationEvidenceID:       "ev:1:request-1:call-test",
		CompletionGateStatus:         completionGateStatusSatisfied,
		CompletionGateRetryCount:     0,
	}
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)
	recorder.LogTurnDiagnostics(context.Background(), "request-1", "conversation-1", diag)
	recorder.LogRuntime(context.Background(), "request-1", "conversation-1", "model_call_final", map[string]any{
		"business_outcome": "succeeded",
		"reasoning_bytes":  12,
	})
	recorder.Close()

	if len(capture.captures) == 0 {
		t.Fatal("turn diagnostics were not captured")
	}
	foundDiagnostics := false
	for _, captured := range capture.captures {
		raw, _ := captured.Payload.Data.(map[string]any)
		blob := mustJSON(t, raw)
		assertNoDiagnosticSensitiveCanaries(t, "debug capture", blob)
		if captured.Event.Event == "turn_diagnostics" {
			foundDiagnostics = true
			assertTurnDiagnosticEventKeys(t, raw)
			if captured.Event.Fields["reasoning_hash"] != diag.ReasoningHash {
				t.Fatalf("capture fields missing reasoning_hash: %#v", captured.Event.Fields)
			}
			if _, ok := captured.Event.Fields["reasoning_content"]; ok {
				t.Fatal("capture fields leaked reasoning_content")
			}
		}
	}
	if !foundDiagnostics {
		t.Fatal("capture missing turn_diagnostics event")
	}

	var persisted strings.Builder
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info == nil || info.IsDir() {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		persisted.Write(data)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	assertNoDiagnosticSensitiveCanaries(t, "debug artifact", []byte(persisted.String()))
	if !strings.Contains(persisted.String(), `"event":"turn_diagnostics"`) && !strings.Contains(persisted.String(), `"event": "turn_diagnostics"`) {
		if !strings.Contains(persisted.String(), "turn_diagnostics") {
			t.Fatalf("debug artifact missing turn_diagnostics event: %s", persisted.String())
		}
	}
	assertNoDiagnosticSensitiveCanaries(t, "ordinary logs", logs.Bytes())
}

func TestCompleteSuccessfulTurnWritesMetadataOnlyDiagnostics(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	if err := service.appendToolResult(stream, "call-write-1", "Write", []byte(`{"path":"`+evidencePathCanary+`"}`), evidenceFileBodyCanary, diagnosticReasoningCanary, evidenceEditSuccessToolCall(evidencePathCanary), "model-call-1"); err != nil {
		t.Fatalf("append mutation: %v", err)
	}
	if err := service.appendToolResult(stream, "call-test-1", "Shell", []byte(`{"command":"`+evidenceCommandCanary+`"}`), evidenceStdoutCanary, "", evidenceShellSuccessToolCall(evidenceCommandCanary, 0), "model-call-1"); err != nil {
		t.Fatalf("append verification: %v", err)
	}
	var logs bytes.Buffer
	prev := log.Writer()
	log.SetOutput(&logs)
	defer log.SetOutput(prev)
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	completed := metadataEntriesOfType(conversation.Entries, "turn_completed")
	if len(completed) != 1 {
		t.Fatalf("turn_completed count = %d, want 1", len(completed))
	}
	var payload metadataPayload
	if err := json.Unmarshal(completed[0].Payload, &payload); err != nil {
		t.Fatalf("decode turn_completed: %v", err)
	}
	assertTurnCompletedWhitelist(t, payload.Value)
	diag := decodeTurnDiagnosticsFromValue(payload.Value)
	if diag.CompletionGateStatus != completionGateStatusSatisfied {
		t.Fatalf("completion_gate_status = %q, want satisfied", diag.CompletionGateStatus)
	}
	if diag.MutationToolCount != 1 || diag.VerificationCommandCount != 1 || diag.VerificationEvidence != executionEvidenceVerificationPresent {
		t.Fatalf("turn diagnostic counts = %+v", diag)
	}
	if diag.ReasoningHash == turnDiagnosticReasoningAbsent || strings.Contains(diag.ReasoningHash, diagnosticReasoningCanary) {
		t.Fatalf("reasoning_hash = %q", diag.ReasoningHash)
	}
	if diag.TranscriptReasoningEmitCount != 0 {
		t.Fatalf("transcript_reasoning_emit_count = %d, want 0", diag.TranscriptReasoningEmitCount)
	}
	historyJSON, err := json.Marshal(conversation.Entries)
	if err != nil {
		t.Fatalf("marshal history: %v", err)
	}
	// Canonical history still stores reasoning on assistant/tool entries; the diagnostic
	// payload and ordinary logs must not copy it.
	assertNoDiagnosticSensitiveCanaries(t, "turn_completed payload", mustJSON(t, payload.Value))
	assertNoDiagnosticSensitiveCanaries(t, "ordinary logs", logs.Bytes())
	transcript, err := projectCursorTranscriptJSONL(conversation)
	if err != nil {
		t.Fatalf("projectCursorTranscriptJSONL() error = %v", err)
	}
	assertPublicTranscriptOmitsInternalDiagnostics(t, transcript)
	_ = historyJSON
}

func TestBuildTurnDiagnosticsMalformedTranscriptProjectionIsFailClosed(t *testing.T) {
	t.Parallel()
	conversation := &ConversationFile{
		ConversationID: "conversation-malformed",
		Entries: []HistoryEntry{{
			TurnSeq:   1,
			RequestID: "request-1",
			Role:      "assistant",
			Kind:      "assistant_text",
			Payload:   json.RawMessage(`{not-json`),
		}},
	}
	if _, err := projectCursorTranscriptJSONL(conversation); err == nil {
		t.Fatal("malformed transcript fixture did not fail projection")
	}
	diag := buildTurnDiagnostics(completionGateInput{
		Mode:           agentv1.AgentMode_AGENT_MODE_AGENT,
		LatestUserText: "hello",
		TurnSeq:        1,
		RequestID:      "request-1",
		Entries:        conversation.Entries,
	}, conversation)
	if diag.TranscriptReasoningEmitCount == 0 {
		t.Fatal("malformed transcript projection marked transcript_reasoning_emit_count=0; want fail-closed non-zero")
	}
}

func TestCompleteSuccessfulTurnAfterRetryWritesInsufficientDiagnosticsOnce(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改超时")
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("first complete: %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 0 {
		t.Fatal("first insufficient wrote turn_completed")
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("first reminder prompt count = %d, want 1", len(completionGatePromptEntries(conversation.Entries)))
	}
	invalidateGateResumeTimer(stream)

	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("second complete: %v", err)
	}
	conversation = snapshotGateConversation(t, service, stream)
	completed := metadataEntriesOfType(conversation.Entries, "turn_completed")
	if len(completed) != 1 {
		t.Fatalf("turn_completed count = %d, want 1", len(completed))
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("prompt count = %d, want 1 (no third pass reminder)", len(completionGatePromptEntries(conversation.Entries)))
	}
	var payload metadataPayload
	if err := json.Unmarshal(completed[0].Payload, &payload); err != nil {
		t.Fatalf("decode turn_completed: %v", err)
	}
	assertTurnCompletedWhitelist(t, payload.Value)
	assertNoDiagnosticSensitiveCanaries(t, "turn_completed payload", mustJSON(t, payload.Value))
	diag := decodeTurnDiagnosticsFromValue(payload.Value)
	if diag.CompletionGateStatus != completionGateStatusInsufficientAfterRetry {
		t.Fatalf("completion_gate_status = %q, want insufficient_after_retry", diag.CompletionGateStatus)
	}
	if diag.CompletionGateRetryCount != 1 {
		t.Fatalf("completion_gate_retry_count = %d, want 1", diag.CompletionGateRetryCount)
	}
	assertControlledCompletionGateGaps(t, diag.CompletionGateGaps)
	if len(diag.CompletionGateGaps) != 1 || diag.CompletionGateGaps[0] != completionGateGapMissingSuccessfulMutation {
		t.Fatalf("gaps = %v, want [%s]", diag.CompletionGateGaps, completionGateGapMissingSuccessfulMutation)
	}
	stream.mu.Lock()
	action := stream.PendingProviderAction
	stream.mu.Unlock()
	if action == providerActionResume {
		t.Fatal("second insufficient scheduled a third provider resume")
	}

	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("third complete: %v", err)
	}
	conversation = snapshotGateConversation(t, service, stream)
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 1 {
		t.Fatalf("third complete duplicated turn_completed: %d", len(metadataEntriesOfType(conversation.Entries, "turn_completed")))
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("third complete wrote another reminder: %d", len(completionGatePromptEntries(conversation.Entries)))
	}
	stream.mu.Lock()
	action = stream.PendingProviderAction
	stream.mu.Unlock()
	if action == providerActionResume {
		t.Fatal("third complete scheduled another provider resume")
	}
}

func TestCompleteSuccessfulTurnFirstInsufficientDoesNotProjectGateIntoPublicTranscript(t *testing.T) {
	const visibleAnswer = "visible answer"
	service, stream := testCompletionGateStream(t, "请修改超时")
	if _, err := service.appendConversationEntries(stream, stream.ConversationID, []HistoryEntry{
		newAssistantTextEntry(stream.TurnSeq, stream.RequestID, visibleAnswer, diagnosticReasoningCanary, "sig-1"),
		newToolCallEntry(stream.TurnSeq, stream.RequestID, "call-write-1", "Write", diagnosticReasoningCanary, "sig-1", transcriptTestEditToolCall(t, evidencePathCanary)),
	}); err != nil {
		t.Fatalf("append visible transcript entries: %v", err)
	}
	if err := service.appendToolResult(stream, "call-write-1", "Write", []byte(`{"path":"`+evidencePathCanary+`"}`), evidenceFileBodyCanary, diagnosticReasoningCanary, evidenceEditSuccessToolCall(evidencePathCanary), "model-call-1"); err != nil {
		t.Fatalf("append mutation: %v", err)
	}
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	t.Cleanup(func() { invalidateGateResumeTimer(stream) })

	conversation := snapshotGateConversation(t, service, stream)
	if len(completionGateMetadataEntries(conversation.Entries)) != 1 {
		t.Fatalf("completion_gate metadata count = %d, want 1", len(completionGateMetadataEntries(conversation.Entries)))
	}
	if len(completionGatePromptEntries(conversation.Entries)) != 1 {
		t.Fatalf("completion_evidence_gate prompt count = %d, want 1", len(completionGatePromptEntries(conversation.Entries)))
	}
	if len(metadataEntriesOfType(conversation.Entries, "turn_completed")) != 0 {
		t.Fatal("first insufficient wrote turn_completed")
	}
	historyJSON, err := json.Marshal(conversation.Entries)
	if err != nil {
		t.Fatalf("marshal history: %v", err)
	}
	for _, needle := range []string{
		"Completion evidence is insufficient",
		promptContextSourceCompletionEvidenceGate,
		`"execution_evidence"`,
		`"completion_gate"`,
		diagnosticReasoningCanary,
	} {
		if !bytes.Contains(historyJSON, []byte(needle)) {
			t.Fatalf("canonical history missing %q; fixture is too weak to prove transcript isolation", needle)
		}
	}

	transcript, err := projectCursorTranscriptJSONL(conversation)
	if err != nil {
		t.Fatalf("projectCursorTranscriptJSONL() error = %v", err)
	}
	assertPublicTranscriptOmitsInternalDiagnostics(t, transcript)
	for _, needle := range []string{
		"Completion evidence is insufficient",
		promptContextSourceCompletionEvidenceGate,
	} {
		if bytes.Contains(transcript, []byte(needle)) {
			t.Fatalf("public transcript leaked %q: %s", needle, transcript)
		}
	}
	if !bytes.Contains(transcript, []byte(visibleAnswer)) {
		t.Fatalf("public transcript dropped visible assistant text: %s", transcript)
	}
	assertPublicTranscriptKeepsStructuredToolUsePath(t, transcript, evidencePathCanary)
}

func TestCompleteSuccessfulTurnAskModeDiagnosticsAreNotApplicable(t *testing.T) {
	service, stream := testCompletionGateStream(t, "请修改 main.go")
	stream.mu.Lock()
	stream.Mode = agentv1.AgentMode_AGENT_MODE_ASK
	stream.mu.Unlock()
	if err := service.completeSuccessfulTurn(stream, gateCompletion(stream)); err != nil {
		t.Fatalf("completeSuccessfulTurn() error = %v", err)
	}
	conversation := snapshotGateConversation(t, service, stream)
	completed := metadataEntriesOfType(conversation.Entries, "turn_completed")
	if len(completed) != 1 {
		t.Fatal("Ask mode did not write turn_completed")
	}
	var payload metadataPayload
	if err := json.Unmarshal(completed[0].Payload, &payload); err != nil {
		t.Fatalf("decode turn_completed: %v", err)
	}
	diag := decodeTurnDiagnosticsFromValue(payload.Value)
	if diag.CompletionGateStatus != completionGateStatusNotApplicable {
		t.Fatalf("Ask mode completion_gate_status = %q, want not_applicable", diag.CompletionGateStatus)
	}
}

func assertTurnDiagnosticWhitelist(t *testing.T, values map[string]any) {
	t.Helper()
	allowed := turnDiagnosticAllowedKeys()
	for key := range values {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("unexpected diagnostic field %q in %#v", key, values)
		}
	}
	required := []string{
		"reasoning_hash",
		"transcript_reasoning_emit_count",
		"mutation_tool_count",
		"verification_command_count",
		"verification_stale",
		"verification_evidence",
		"completion_gate_status",
		"completion_gate_retry_count",
	}
	for _, key := range required {
		if _, ok := values[key]; !ok {
			t.Fatalf("missing required diagnostic field %q in %#v", key, values)
		}
	}
}

func assertTurnCompletedWhitelist(t *testing.T, values map[string]any) {
	t.Helper()
	allowed := turnDiagnosticAllowedKeys()
	allowed["model_call_id"] = struct{}{}
	for key := range values {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("unexpected turn_completed field %q in %#v", key, values)
		}
	}
	assertTurnDiagnosticWhitelist(t, valuesWithout(values, "model_call_id"))
}

func valuesWithout(values map[string]any, drop string) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		if key == drop {
			continue
		}
		out[key] = value
	}
	return out
}

func turnDiagnosticAllowedKeys() map[string]struct{} {
	return map[string]struct{}{
		"reasoning_hash":                  {},
		"transcript_reasoning_emit_count": {},
		"mutation_tool_count":             {},
		"verification_command_count":      {},
		"verification_stale":              {},
		"verification_evidence":           {},
		"verification_evidence_id":        {},
		"completion_gate_status":          {},
		"completion_gate_retry_count":     {},
		"completion_gate_gaps":            {},
	}
}

func assertTurnDiagnosticEventKeys(t *testing.T, event map[string]any) {
	t.Helper()
	allowed := turnDiagnosticAllowedKeys()
	for _, key := range []string{"schema_version", "at", "layer", "request_id", "conversation_id", "event", "status"} {
		allowed[key] = struct{}{}
	}
	for key := range event {
		if _, ok := allowed[key]; !ok {
			t.Fatalf("turn_diagnostics event has unexpected key %q in %#v", key, event)
		}
	}
}

func assertNoDiagnosticSensitiveCanaries(t *testing.T, label string, blob []byte) {
	t.Helper()
	for _, canary := range []string{
		diagnosticReasoningCanary,
		evidenceReasoningCanary,
		evidenceStdoutCanary,
		evidenceFileBodyCanary,
		evidenceCredentialCanary,
		evidencePathCanary,
		evidenceCommandCanary,
		evidenceCookieCanary,
		"sk-live-evidence-canary",
	} {
		if bytes.Contains(blob, []byte(canary)) {
			t.Fatalf("%s leaked %q: %s", label, canary, blob)
		}
	}
}

func assertPublicTranscriptOmitsInternalDiagnostics(t *testing.T, data []byte) {
	t.Helper()
	for _, canary := range []string{diagnosticReasoningCanary, evidenceReasoningCanary} {
		if bytes.Contains(data, []byte(canary)) {
			t.Fatalf("public transcript leaked reasoning canary %q: %s", canary, data)
		}
	}
	blob := string(data)
	for _, needle := range []string{
		`"reasoning_hash"`,
		`"transcript_reasoning_emit_count"`,
		`"mutation_tool_count"`,
		`"verification_command_count"`,
		`"verification_stale"`,
		`"verification_evidence"`,
		`"verification_evidence_id"`,
		`"completion_gate_status"`,
		`"completion_gate_retry_count"`,
		`"completion_gate_gaps"`,
		`"execution_evidence"`,
		`"completion_gate"`,
		`"turn_completed"`,
	} {
		if strings.Contains(blob, needle) {
			t.Fatalf("public transcript extra-projected internal diagnostic %s: %s", needle, data)
		}
	}
	for _, line := range decodeCursorTranscriptLines(t, data) {
		switch strings.TrimSpace(line.Type) {
		case "", "turn_ended":
		default:
			t.Fatalf("public transcript extra-projected internal type %q: %#v", line.Type, line)
		}
	}
}

func assertPublicTranscriptKeepsStructuredToolUsePath(t *testing.T, data []byte, wantPath string) {
	t.Helper()
	found := false
	for _, line := range decodeCursorTranscriptLines(t, data) {
		if line.Message == nil {
			continue
		}
		for _, content := range line.Message.Content {
			if strings.TrimSpace(content.Type) != "tool_use" {
				continue
			}
			input, _ := content.Input.(map[string]any)
			if input == nil {
				continue
			}
			if readStringValue(input["path"]) == wantPath {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("public transcript hid legitimate tool_use.path %q: %s", wantPath, data)
	}
}

func assertControlledCompletionGateGaps(t *testing.T, gaps []string) {
	t.Helper()
	if len(gaps) == 0 {
		t.Fatal("expected controlled completion_gate_gaps, got none")
	}
	allowed := map[string]struct{}{
		completionGateGapMissingSuccessfulMutation: {},
		completionGateGapMissingLaterVerification:  {},
		completionGateGapPendingOrFailedResult:     {},
		completionGateGapUnknownToolNotEvidence:    {},
	}
	for _, gap := range gaps {
		if _, ok := allowed[gap]; !ok {
			t.Fatalf("uncontrolled completion_gate gap %q in %v", gap, gaps)
		}
	}
}
