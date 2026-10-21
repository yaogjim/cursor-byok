package forwarder

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

const (
	turnDiagnosticReasoningAbsent                 = "absent"
	turnDiagnosticStatusAbsent                    = "absent"
	turnDiagnosticTranscriptProjectionFailedCount = 1
)

type turnDiagnosticRecord struct {
	ReasoningHash                string
	TranscriptReasoningEmitCount int
	MutationToolCount            int
	VerificationCommandCount     int
	VerificationStale            bool
	VerificationEvidence         string
	VerificationEvidenceID       string
	CompletionGateStatus         string
	CompletionGateRetryCount     int
	CompletionGateGaps           []string
}

func turnDiagnosticFieldKeys() []string {
	return []string{
		"reasoning_hash",
		"transcript_reasoning_emit_count",
		"mutation_tool_count",
		"verification_command_count",
		"verification_stale",
		"verification_evidence",
		"verification_evidence_id",
		"completion_gate_status",
		"completion_gate_retry_count",
		"completion_gate_gaps",
	}
}

func streamDiagnosticFieldKeys() []string {
	return []string{
		"header_at",
		"first_byte_at",
		"last_byte_at",
		"body_end_at",
		"last_effective_content_at",
		"first_effective_content_at",
		"close_cause",
		"partial_boundary",
		"transport_outcome",
		"http_protocol",
		"content_encoding",
		"auto_decompressed",
		"content_length",
		"connection_observed",
		"connection_reused",
		"connection_was_idle",
		"raw_byte_count",
		"last_error_type",
		"last_sse_event_type",
		"last_sse_event_id_hash",
		"last_sse_sequence",
		"last_response_status",
		"stream_recovery_attempts",
		"provider_error_summary",
		"provider_error_summary_type",
	}
}

func stableReasoningHash(texts []string) string {
	sum := sha256.New()
	wrote := false
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			continue
		}
		_, _ = sum.Write([]byte(text))
		sum.Write([]byte{0})
		wrote = true
	}
	if !wrote {
		return turnDiagnosticReasoningAbsent
	}
	return hex.EncodeToString(sum.Sum(nil))
}

func countReasoningEmitsInTranscript(transcript []byte, texts []string) int {
	if len(transcript) == 0 {
		return 0
	}
	count := 0
	for _, text := range texts {
		if strings.TrimSpace(text) == "" {
			continue
		}
		count += bytes.Count(transcript, []byte(text))
	}
	return count
}

func transcriptReasoningEmitCount(conversation *ConversationFile, texts []string) int {
	if conversation == nil {
		return 0
	}
	transcript, err := projectCursorTranscriptJSONL(conversation)
	if err != nil {
		// Fail closed: a projection error must not look like a clean zero-emit transcript.
		return turnDiagnosticTranscriptProjectionFailedCount
	}
	return countReasoningEmitsInTranscript(transcript, texts)
}

func collectHistoryReasoningTexts(entries []HistoryEntry) []string {
	texts := make([]string, 0, len(entries))
	for _, entry := range entries {
		for _, text := range historyEntryReasoningTexts(entry) {
			if strings.TrimSpace(text) == "" {
				continue
			}
			texts = append(texts, text)
		}
	}
	return texts
}

func historyEntryReasoningTexts(entry HistoryEntry) []string {
	switch strings.TrimSpace(entry.Kind) {
	case "assistant_text":
		var payload assistantTextPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil
		}
		return []string{payload.ReasoningContent}
	case "tool_call":
		var payload toolCallEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil
		}
		return []string{payload.ReasoningContent}
	case "tool_result":
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil
		}
		return []string{payload.ReasoningContent}
	case "model_message":
		var payload modelMessageEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil
		}
		return []string{payload.Message.ReasoningContent}
	default:
		return nil
	}
}

func buildTurnDiagnostics(input completionGateInput, conversation *ConversationFile) turnDiagnosticRecord {
	decision := evaluateCompletionEvidenceGate(input)
	entries := filterHistoryForTurnRequest(input.Entries, input.TurnSeq, input.RequestID)
	texts := collectHistoryReasoningTexts(entries)
	status := strings.TrimSpace(decision.Status)
	if status == "" {
		status = completionGateStatusNotApplicable
	}
	evidence := strings.TrimSpace(decision.Summary.VerificationEvidence)
	if evidence == "" {
		evidence = executionEvidenceVerificationUnknown
	}
	return turnDiagnosticRecord{
		ReasoningHash:                stableReasoningHash(texts),
		TranscriptReasoningEmitCount: transcriptReasoningEmitCount(conversation, texts),
		MutationToolCount:            decision.Summary.MutationToolCount,
		VerificationCommandCount:     decision.Summary.VerificationCommandCount,
		VerificationStale:            decision.Summary.VerificationStale,
		VerificationEvidence:         evidence,
		VerificationEvidenceID:       strings.TrimSpace(decision.Summary.VerificationEvidenceID),
		CompletionGateStatus:         status,
		CompletionGateRetryCount:     decision.RetryCount,
		CompletionGateGaps:           append([]string(nil), decision.Gaps...),
	}
}

func (service *Service) buildTurnDiagnosticsForCompletion(stream *ActiveStream, completion pendingTurnCompletion) (turnDiagnosticRecord, error) {
	if service == nil || stream == nil {
		return turnDiagnosticRecord{}, nil
	}
	conversation, pendingExecs, pendingInteractions, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return turnDiagnosticRecord{}, err
	}
	requestID := firstNonEmpty(strings.TrimSpace(completion.RequestID), strings.TrimSpace(stream.RequestID))
	turnSeq := completion.TurnSeq
	if turnSeq <= 0 {
		turnSeq = stream.TurnSeq
	}
	stream.mu.Lock()
	mode := stream.Mode
	latestUserText := stream.LatestUserText
	stream.mu.Unlock()
	return buildTurnDiagnostics(completionGateInput{
		Mode:              mode,
		LatestUserText:    latestUserText,
		TurnSeq:           turnSeq,
		RequestID:         requestID,
		Entries:           conversation.Entries,
		PendingExecCount:  len(pendingExecs),
		PendingInterCount: len(pendingInteractions),
	}, conversation), nil
}

func copyTurnDiagnosticFields(dst map[string]any, diag turnDiagnosticRecord) {
	if dst == nil {
		return
	}
	dst["reasoning_hash"] = firstNonEmpty(strings.TrimSpace(diag.ReasoningHash), turnDiagnosticReasoningAbsent)
	dst["transcript_reasoning_emit_count"] = diag.TranscriptReasoningEmitCount
	dst["mutation_tool_count"] = diag.MutationToolCount
	dst["verification_command_count"] = diag.VerificationCommandCount
	dst["verification_stale"] = diag.VerificationStale
	dst["verification_evidence"] = firstNonEmpty(strings.TrimSpace(diag.VerificationEvidence), executionEvidenceVerificationUnknown)
	if id := strings.TrimSpace(diag.VerificationEvidenceID); id != "" {
		dst["verification_evidence_id"] = id
	}
	dst["completion_gate_status"] = firstNonEmpty(strings.TrimSpace(diag.CompletionGateStatus), turnDiagnosticStatusAbsent)
	dst["completion_gate_retry_count"] = diag.CompletionGateRetryCount
	if len(diag.CompletionGateGaps) > 0 {
		dst["completion_gate_gaps"] = append([]string(nil), diag.CompletionGateGaps...)
	}
}

func turnDiagnosticValueMap(diag turnDiagnosticRecord) map[string]any {
	values := make(map[string]any, 12)
	copyTurnDiagnosticFields(values, diag)
	return values
}

func turnCompletedValueMap(modelCallID string, diag turnDiagnosticRecord) map[string]any {
	values := turnDiagnosticValueMap(diag)
	values["model_call_id"] = strings.TrimSpace(modelCallID)
	return values
}

func decodeTurnDiagnosticsFromValue(values map[string]any) turnDiagnosticRecord {
	if values == nil {
		values = map[string]any{}
	}
	return turnDiagnosticRecord{
		ReasoningHash:                firstNonEmpty(readStringValue(values["reasoning_hash"]), turnDiagnosticReasoningAbsent),
		TranscriptReasoningEmitCount: int(readInt64Value(values["transcript_reasoning_emit_count"])),
		MutationToolCount:            int(readInt64Value(values["mutation_tool_count"])),
		VerificationCommandCount:     int(readInt64Value(values["verification_command_count"])),
		VerificationStale:            readBoolValue(values["verification_stale"]),
		VerificationEvidence:         firstNonEmpty(readStringValue(values["verification_evidence"]), executionEvidenceVerificationUnknown),
		VerificationEvidenceID:       readStringValue(values["verification_evidence_id"]),
		CompletionGateStatus:         firstNonEmpty(readStringValue(values["completion_gate_status"]), turnDiagnosticStatusAbsent),
		CompletionGateRetryCount:     int(readInt64Value(values["completion_gate_retry_count"])),
		CompletionGateGaps:           readDiagnosticStringSlice(values["completion_gate_gaps"]),
	}
}

func readDiagnosticStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := strings.TrimSpace(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			if trimmed := readStringValue(item); trimmed != "" {
				out = append(out, trimmed)
			}
		}
		return out
	default:
		return nil
	}
}
