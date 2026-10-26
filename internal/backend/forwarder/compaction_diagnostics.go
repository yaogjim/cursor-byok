package forwarder

import (
	"context"
	"encoding/json"
	"math"
	"strings"
)

// Compaction decision diagnostics give the log analyzer a stable, versioned
// view of prefix selection without recording conversation content. They are
// emitted for every decision made inside one RequestID/TurnSeq, so a turn may
// legitimately produce several planned/applied/blocked records.
const (
	compactionDiagnosticEventPlanned = "compaction_planned"
	compactionDiagnosticEventApplied = "compaction_applied"
	compactionDiagnosticEventBlocked = "compaction_blocked"

	// Status is assigned explicitly instead of being inferred by the recorder.
	// blocked is an expected budget/policy decision, not an error: it never
	// synthesizes error_category and never emits a terminal event.
	compactionDiagnosticStatusPlanned = "started"
	compactionDiagnosticStatusApplied = "completed"
	compactionDiagnosticStatusBlocked = "blocked"
)

var compactionDiagnosticNumericFields = map[string]struct{}{
	"compaction_sequence":       {},
	"compaction_layout_version": {},
	"prefix_start":              {},
	"prefix_end":                {},
	"tail_start":                {},
	"pinned_input_count":        {},
	"completed_batch_count":     {},
	"compiled_tokens_before":    {},
	"compiled_tokens_after":     {},
	"budget_tokens":             {},
	"pinned_input_tokens":       {},
	"current_input_tokens":      {},
	"tail_tokens":               {},
}

var compactionDiagnosticEnumFields = map[string]struct{}{
	"blocked_reason": {},
	"reason":         {},
	"phase":          {},
	"error_category": {},
}

// compactionDiagnosticFieldKeys is the single field whitelist used by the
// helper and the debug recorder capture projection. The log analyzer mirrors
// these names in tools/log-analyzer/internal/sanitize so export does not drop
// the counter fields that contain "token" in their names.
func compactionDiagnosticFieldKeys() []string {
	return []string{
		"compaction_sequence",
		"compaction_layout_version",
		"prefix_start",
		"prefix_end",
		"tail_start",
		"pinned_input_count",
		"completed_batch_count",
		"compiled_tokens_before",
		"compiled_tokens_after",
		"budget_tokens",
		"pinned_input_tokens",
		"current_input_tokens",
		"tail_tokens",
		"blocked_reason",
		"reason",
		"phase",
		"error_category",
	}
}

// recordCompactionDecision records one compaction decision for the analyzer.
// The stream lock is released before LogRuntime, because the recorder resolves
// the conversation through the broker and re-locks the stream. Callers pass
// only counters and enum reasons; body, user text, summaries, tool arguments
// and credentials are dropped even if present.
func (service *Service) recordCompactionDecision(stream *ActiveStream, event string, fields map[string]any) {
	if service == nil || stream == nil || service.debug == nil {
		return
	}
	event = strings.ToLower(strings.TrimSpace(event))
	status, ok := compactionDiagnosticStatus(event)
	if !ok {
		return
	}
	stream.mu.Lock()
	requestID := stream.RequestID
	conversationID := stream.ConversationID
	turnSeq := stream.TurnSeq
	stream.mu.Unlock()

	values := compactionDiagnosticSafeFields(fields)
	values["status"] = status
	if turnSeq > 0 {
		// Correlation input only; it is not part of the exported field whitelist.
		values["turn_seq"] = turnSeq
	}
	service.debug.LogRuntime(context.Background(), requestID, conversationID, event, values)
}

func compactionDiagnosticStatus(event string) (string, bool) {
	switch event {
	case compactionDiagnosticEventPlanned:
		return compactionDiagnosticStatusPlanned, true
	case compactionDiagnosticEventApplied:
		return compactionDiagnosticStatusApplied, true
	case compactionDiagnosticEventBlocked:
		return compactionDiagnosticStatusBlocked, true
	default:
		return "", false
	}
}

func compactionPlanDiagnosticFields(conversation *ConversationFile, plan *PendingCompaction) map[string]any {
	fields := map[string]any{"phase": "compaction"}
	if plan == nil || conversation == nil {
		return fields
	}
	sequence := 1
	for _, entry := range conversation.Entries {
		if isCompactionSummaryKind(entry.Kind) && entry.TurnSeq == plan.CurrentTurnSeq && strings.TrimSpace(entry.RequestID) == strings.TrimSpace(plan.CurrentRequestID) {
			sequence++
		}
	}
	fields["compaction_sequence"] = sequence
	fields["compiled_tokens_before"] = plan.CompiledTokensBefore
	fields["budget_tokens"] = compactionBudgetTokens(plan.ContextWindowSize)
	fields["current_input_tokens"] = compactionCurrentInputTokens(conversation.Entries, plan.CurrentTurnSeq, plan.CurrentRequestID)
	if plan.HomologousPrefixFrozen && plan.NativeArchiveSource == nil {
		fields["reason"] = "native_archive_unavailable"
	}
	if plan.Layout == nil {
		return fields
	}
	layout := plan.Layout
	fields["compaction_layout_version"] = layout.Version
	fields["prefix_start"] = layout.PrefixStart
	fields["prefix_end"] = layout.PrefixEnd
	fields["tail_start"] = layout.TailStart
	fields["pinned_input_count"] = len(layout.PinnedInputs)
	pinnedTokens, tailTokens := int64(0), int64(0)
	pinned := make(map[int64]compactionEntryReference, len(layout.PinnedInputs))
	for _, ref := range layout.PinnedInputs {
		pinned[ref.Seq] = ref
	}
	dropped := canceledReplayDroppedIndexes(conversation.Entries)
	batches := map[string]struct{}{}
	for index, entry := range conversation.Entries {
		if _, canceled := dropped[index]; canceled {
			continue
		}
		if ref, ok := pinned[entry.Seq]; ok && index < layout.TailStart && compactionEntryRef(entry) == ref {
			pinnedTokens += estimateTextTokens(string(entry.Payload))
		}
		if index >= layout.TailStart && isOrdinaryPromptReplayEntryKind(entry.Kind) {
			tailTokens += estimateTextTokens(string(entry.Payload))
		}
		if index >= layout.PrefixStart && index < layout.PrefixEnd && entry.Kind == "tool_result" && strings.TrimSpace(entry.ModelCallID) != "" {
			batches[entry.RequestID+"/"+entry.ModelCallID] = struct{}{}
		}
	}
	fields["pinned_input_tokens"] = pinnedTokens
	fields["tail_tokens"] = tailTokens
	fields["completed_batch_count"] = len(batches)
	return fields
}

// compactionCurrentInputTokens 是当前输入原始载荷的保守估算，与是否已有冻结引用无关。
// pinned_input_tokens 只统计布局实际独立重放的输入，避免把连续尾中的输入重复归类。
func compactionCurrentInputTokens(entries []HistoryEntry, turnSeq int64, requestID string) int64 {
	dropped := canceledReplayDroppedIndexes(entries)
	var tokens int64
	for index, entry := range entries {
		if _, canceled := dropped[index]; canceled {
			continue
		}
		if entry.TurnSeq == turnSeq && strings.TrimSpace(entry.RequestID) == strings.TrimSpace(requestID) && isCompactionInput(entry) {
			tokens += estimateTextTokens(string(entry.Payload))
		}
	}
	return tokens
}

func compactionDiagnosticSafeFields(fields map[string]any) map[string]any {
	safe := make(map[string]any, len(fields))
	for key, value := range fields {
		if _, ok := compactionDiagnosticNumericFields[key]; ok {
			if number, ok := compactionDiagnosticNumber(value); ok {
				safe[key] = number
			}
			continue
		}
		if _, ok := compactionDiagnosticEnumFields[key]; ok {
			if enum, ok := compactionDiagnosticEnum(value); ok {
				safe[key] = enum
			}
		}
	}
	return safe
}

func compactionDiagnosticNumber(value any) (int64, bool) {
	switch typed := value.(type) {
	case int:
		return compactionDiagnosticNonNegative(int64(typed))
	case int8:
		return compactionDiagnosticNonNegative(int64(typed))
	case int16:
		return compactionDiagnosticNonNegative(int64(typed))
	case int32:
		return compactionDiagnosticNonNegative(int64(typed))
	case int64:
		return compactionDiagnosticNonNegative(typed)
	case uint:
		return compactionDiagnosticUnsigned(uint64(typed))
	case uint8:
		return compactionDiagnosticUnsigned(uint64(typed))
	case uint16:
		return compactionDiagnosticUnsigned(uint64(typed))
	case uint32:
		return compactionDiagnosticUnsigned(uint64(typed))
	case uint64:
		return compactionDiagnosticUnsigned(typed)
	case float32:
		return compactionDiagnosticFloat(float64(typed))
	case float64:
		return compactionDiagnosticFloat(typed)
	case json.Number:
		number, err := typed.Int64()
		if err != nil {
			return 0, false
		}
		return compactionDiagnosticNonNegative(number)
	default:
		return 0, false
	}
}

func compactionDiagnosticFloat(value float64) (int64, bool) {
	if math.IsNaN(value) || math.IsInf(value, 0) || value != math.Trunc(value) || value > math.MaxInt64 {
		return 0, false
	}
	return compactionDiagnosticNonNegative(int64(value))
}

func compactionDiagnosticUnsigned(value uint64) (int64, bool) {
	if value > math.MaxInt64 {
		return 0, false
	}
	return int64(value), true
}

func compactionDiagnosticNonNegative(value int64) (int64, bool) {
	if value < 0 {
		return 0, false
	}
	return value, true
}

// compactionDiagnosticEnum keeps only short snake_case identifiers. Anything
// that could carry free text, URLs, paths or credentials is rejected as a
// whole instead of being partially rewritten.
func compactionDiagnosticEnum(value any) (string, bool) {
	text, ok := value.(string)
	if !ok {
		return "", false
	}
	text = strings.ToLower(strings.TrimSpace(text))
	if text == "" || len([]rune(text)) > 64 {
		return "", false
	}
	for _, character := range text {
		switch {
		case character >= 'a' && character <= 'z':
		case character >= '0' && character <= '9':
		case character == '_', character == '-', character == '.':
		default:
			return "", false
		}
	}
	return text, true
}
