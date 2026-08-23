package forwarder

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

const (
	completionGateSchemaVersion = 1
	completionGatePayloadType   = "completion_gate"
)

const (
	completionGateStatusNotApplicable          = "not_applicable"
	completionGateStatusSatisfied              = "satisfied"
	completionGateStatusInsufficientFirst      = "insufficient_first"
	completionGateStatusInsufficientAfterRetry = "insufficient_after_retry"
)

const (
	completionGateGapMissingSuccessfulMutation = "missing_successful_mutation"
	completionGateGapMissingLaterVerification  = "missing_later_verification"
	completionGateGapPendingOrFailedResult     = "pending_or_failed_result"
	completionGateGapUnknownToolNotEvidence    = "unknown_tool_not_evidence"
)

const promptContextSourceCompletionEvidenceGate = "completion_evidence_gate"

const completionEvidenceGateReminderText = `Completion evidence is insufficient for this editing turn.
A successful mutation tool result is required.
A successful verification command must occur after the last successful mutation.
Pending or failed tool results are not completion evidence.
Unknown tools are not mutation or verification evidence.
Assistant text, thinking, plans, code blocks, and inline files are not execution evidence.
Use real available tools to fill the gaps, or clearly report that you cannot produce the missing evidence.`

const completionGateMaxIntentRunes = 800

var (
	completionGateEnglishInformationalPattern    = regexp.MustCompile(`(?i)\b(?:what does|how do i|how does|update me|more context|add context)\b`)
	completionGateEnglishEditPattern             = regexp.MustCompile(`(?i)^(?:please\s+)?(?:edit|change|update|fix|write|delete|patch)\s+\S`)
	completionGateEnglishAddFilePattern          = regexp.MustCompile(`(?i)^(?:please\s+)?add\s+(?:a\s+|an\s+|the\s+|this\s+)?(?:file|timeout|field|function|method|test|config|route|handler)\b`)
	completionGateChineseChangeToPattern         = regexp.MustCompile(`(?:请把|请将|把它|将其|把这).{0,32}改成`)
	completionGateChineseAddInFilePattern        = regexp.MustCompile(`请?在.{0,16}(?:文件|代码|配置|实现).{0,16}加上`)
	completionGateChineseAddObjectPattern        = regexp.MustCompile(`请加上.{0,24}(?:字段|文件|配置|超时|函数|方法|测试|参数|路由|变量)`)
	completionGateChineseChangeToQuestionPattern = regexp.MustCompile(`改成(?:什么|怎么|哪)`)
)

type completionGateRecord struct {
	SchemaVersion            int      `json:"schema_version"`
	Status                   string   `json:"status"`
	RetryCount               int      `json:"retry_count"`
	Gaps                     []string `json:"gaps,omitempty"`
	TurnSeq                  int64    `json:"turn_seq,omitempty"`
	RequestID                string   `json:"request_id,omitempty"`
	MutationToolCount        int      `json:"mutation_tool_count"`
	VerificationCommandCount int      `json:"verification_command_count"`
	VerificationStale        bool     `json:"verification_stale"`
	VerificationEvidence     string   `json:"verification_evidence,omitempty"`
	VerificationEvidenceID   string   `json:"verification_evidence_id,omitempty"`
}

type completionGateInput struct {
	Mode              agentv1.AgentMode
	LatestUserText    string
	TurnSeq           int64
	RequestID         string
	Entries           []HistoryEntry
	PendingExecCount  int
	PendingInterCount int
}

type completionGateDecision struct {
	Status     string
	RetryCount int
	Gaps       []string
	Summary    ExecutionEvidenceSummary
	Applicable bool
}

type completionGateScan struct {
	MutationAttempt bool
	UnknownTool     bool
	PendingOrFailed bool
	InFlightPending bool
	RetryCount      int
	HadRetry        bool
	Summary         ExecutionEvidenceSummary
}

func evaluateCompletionEvidenceGate(input completionGateInput) completionGateDecision {
	scan := scanCompletionGateHistory(input)
	decision := completionGateDecision{
		RetryCount: scan.RetryCount,
		Summary:    scan.Summary,
		Applicable: completionGateApplies(input, scan.MutationAttempt),
	}
	if !decision.Applicable {
		decision.Status = completionGateStatusNotApplicable
		return decision
	}
	decision.Gaps = completionGateGaps(scan)
	satisfied := scan.Summary.MutationToolCount > 0 &&
		scan.Summary.VerificationEvidence == executionEvidenceVerificationPresent &&
		!scan.InFlightPending
	if satisfied {
		decision.Status = completionGateStatusSatisfied
		decision.Gaps = nil
		return decision
	}
	if scan.HadRetry {
		decision.Status = completionGateStatusInsufficientAfterRetry
		if decision.RetryCount < 1 {
			decision.RetryCount = 1
		}
		return decision
	}
	decision.Status = completionGateStatusInsufficientFirst
	decision.RetryCount = 1
	return decision
}

func completionGateApplies(input completionGateInput, mutationAttempt bool) bool {
	mode := input.Mode
	if normalized, err := validateSupportedActiveMode(mode); err == nil {
		mode = normalized
	}
	switch mode {
	case agentv1.AgentMode_AGENT_MODE_ASK, agentv1.AgentMode_AGENT_MODE_PLAN:
		return false
	}
	if mutationAttempt {
		return true
	}
	return hasExplicitImperativeEditIntent(input.LatestUserText)
}

func hasExplicitImperativeEditIntent(text string) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" || runeLen(trimmed) > completionGateMaxIntentRunes {
		return false
	}
	if hasExplicitAdviceOnlyOrDoNotModify(trimmed) {
		return false
	}
	if hasNarrowChineseEditImperative(trimmed) {
		return true
	}
	if completionGateEnglishEditPattern.MatchString(trimmed) || completionGateEnglishAddFilePattern.MatchString(trimmed) {
		return !completionGateEnglishInformationalPattern.MatchString(trimmed)
	}
	return false
}

func hasExplicitAdviceOnlyOrDoNotModify(text string) bool {
	for _, marker := range []string{
		"不要修改文件", "不要改文件", "不用修改文件", "无需修改文件",
		"只需说明", "只需要说明", "不要改代码", "不用改文件",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func hasNarrowChineseEditImperative(text string) bool {
	for _, marker := range []string{
		"请修改", "请编辑", "请写入", "请删除", "请删掉", "请修复",
		"改一下", "编辑这个", "修改这个", "写入文件",
		"删掉这个", "删除这个", "加上这个", "修复这个",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	if strings.Contains(text, "请改") && !completionGateChineseChangeToQuestionPattern.MatchString(text) {
		return true
	}
	if completionGateChineseChangeToPattern.MatchString(text) {
		return true
	}
	if completionGateChineseAddInFilePattern.MatchString(text) {
		return true
	}
	return completionGateChineseAddObjectPattern.MatchString(text)
}

func runeLen(text string) int {
	count := 0
	for range text {
		count++
	}
	return count
}

func completionGateGaps(scan completionGateScan) []string {
	gaps := make([]string, 0, 4)
	if scan.Summary.MutationToolCount == 0 {
		gaps = append(gaps, completionGateGapMissingSuccessfulMutation)
	}
	if scan.Summary.MutationToolCount > 0 && scan.Summary.VerificationEvidence != executionEvidenceVerificationPresent {
		gaps = append(gaps, completionGateGapMissingLaterVerification)
	}
	if scan.PendingOrFailed {
		gaps = append(gaps, completionGateGapPendingOrFailedResult)
	}
	if scan.UnknownTool && (scan.Summary.MutationToolCount == 0 || scan.Summary.VerificationEvidence != executionEvidenceVerificationPresent) {
		gaps = append(gaps, completionGateGapUnknownToolNotEvidence)
	}
	return gaps
}

func scanCompletionGateHistory(input completionGateInput) completionGateScan {
	entries := filterHistoryForTurnRequest(input.Entries, input.TurnSeq, input.RequestID)
	index := rebuildExecutionEvidenceIndex(entries, input.TurnSeq)
	inFlightPending := input.PendingExecCount+input.PendingInterCount > 0
	scan := completionGateScan{
		Summary:         summarizeExecutionEvidence(index),
		PendingOrFailed: inFlightPending,
		InFlightPending: inFlightPending,
	}
	pendingCalls := make(map[string]struct{})
	resultCalls := make(map[string]struct{})
	failedMutation := false
	failedVerification := false
	for _, entry := range entries {
		if record, ok := decodeCompletionGate(entry); ok {
			if record.RetryCount > scan.RetryCount {
				scan.RetryCount = record.RetryCount
			}
			switch strings.TrimSpace(record.Status) {
			case completionGateStatusInsufficientFirst, completionGateStatusInsufficientAfterRetry:
				scan.HadRetry = true
			}
			if record.RetryCount >= 1 {
				scan.HadRetry = true
			}
			continue
		}
		if record, ok := decodeExecutionEvidence(entry); ok {
			if record.ToolCategory == executionEvidenceCategoryMutation {
				scan.MutationAttempt = true
			}
			if record.ToolCategory == executionEvidenceCategoryUnknown {
				scan.UnknownTool = true
			}
			if !record.Successful && record.ToolCategory == executionEvidenceCategoryMutation {
				failedMutation = true
			}
			if !record.Successful && record.ToolCategory == executionEvidenceCategoryVerification {
				failedVerification = true
			}
			continue
		}
		toolCallID, toolName, kind, ok := decodeHistoryToolIdentity(entry)
		if !ok {
			continue
		}
		category, _ := classifyExecutionEvidenceTool(toolName)
		if kind == "tool_call" {
			if category == executionEvidenceCategoryMutation {
				scan.MutationAttempt = true
			}
			if category == executionEvidenceCategoryUnknown {
				scan.UnknownTool = true
			}
			if toolCallID != "" {
				pendingCalls[toolCallID] = struct{}{}
			}
			continue
		}
		if kind == "tool_result" && toolCallID != "" {
			resultCalls[toolCallID] = struct{}{}
		}
	}
	for toolCallID := range pendingCalls {
		if _, ok := resultCalls[toolCallID]; !ok {
			scan.PendingOrFailed = true
			scan.InFlightPending = true
			break
		}
	}
	if failedMutation && scan.Summary.MutationToolCount == 0 {
		scan.PendingOrFailed = true
	}
	if failedVerification && scan.Summary.VerificationEvidence != executionEvidenceVerificationPresent {
		scan.PendingOrFailed = true
	}
	if scan.RetryCount >= 1 {
		scan.HadRetry = true
	}
	return scan
}

func conversationHasTurnCompleted(entries []HistoryEntry, turnSeq int64, requestID string) bool {
	for _, entry := range filterHistoryForTurnRequest(entries, turnSeq, requestID) {
		if strings.TrimSpace(entry.Kind) != "metadata" {
			continue
		}
		var payload metadataPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			continue
		}
		if strings.TrimSpace(payload.Type) == "turn_completed" {
			return true
		}
	}
	return false
}

func turnCompletedIdempotencyKey(turnSeq int64, requestID string) string {
	return strings.Join([]string{
		"turn_completed",
		strconv.FormatInt(turnSeq, 10),
		strings.TrimSpace(requestID),
	}, ":")
}

func filterHistoryForTurnRequest(entries []HistoryEntry, turnSeq int64, requestID string) []HistoryEntry {
	if len(entries) == 0 {
		return nil
	}
	requestID = strings.TrimSpace(requestID)
	filtered := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if turnSeq > 0 && entry.TurnSeq != turnSeq {
			continue
		}
		entryRequestID := strings.TrimSpace(entry.RequestID)
		if requestID != "" && entryRequestID != "" && entryRequestID != requestID {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func decodeHistoryToolIdentity(entry HistoryEntry) (string, string, string, bool) {
	kind := strings.TrimSpace(entry.Kind)
	switch kind {
	case "tool_call":
		var payload toolCallEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return "", "", "", false
		}
		toolCallID := firstNonEmpty(strings.TrimSpace(payload.ToolCallID), strings.TrimSpace(entry.ToolCallID))
		return toolCallID, strings.TrimSpace(payload.ToolName), kind, strings.TrimSpace(payload.ToolName) != ""
	case "tool_result":
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return "", "", "", false
		}
		toolCallID := firstNonEmpty(strings.TrimSpace(payload.ToolCallID), strings.TrimSpace(entry.ToolCallID))
		return toolCallID, strings.TrimSpace(payload.ToolName), kind, toolCallID != "" || strings.TrimSpace(payload.ToolName) != ""
	default:
		return "", "", "", false
	}
}

func decodeCompletionGate(entry HistoryEntry) (completionGateRecord, bool) {
	if strings.TrimSpace(entry.Kind) != "metadata" {
		return completionGateRecord{}, false
	}
	var payload metadataPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return completionGateRecord{}, false
	}
	if strings.TrimSpace(payload.Type) != completionGatePayloadType {
		return completionGateRecord{}, false
	}
	encoded, err := json.Marshal(payload.Value)
	if err != nil {
		return completionGateRecord{}, false
	}
	var record completionGateRecord
	if err := json.Unmarshal(encoded, &record); err != nil {
		return completionGateRecord{}, false
	}
	if record.SchemaVersion == 0 {
		record.SchemaVersion = completionGateSchemaVersion
	}
	if record.TurnSeq == 0 {
		record.TurnSeq = entry.TurnSeq
	}
	if record.RequestID == "" {
		record.RequestID = strings.TrimSpace(entry.RequestID)
	}
	return record, strings.TrimSpace(record.Status) != ""
}

func newCompletionGateEntry(record completionGateRecord) HistoryEntry {
	values := completionGateValueMap(record)
	entry := newMetadataEntry(record.TurnSeq, record.RequestID, completionGatePayloadType, values)
	entry.IdempotencyKey = completionGateIdempotencyKey(record.TurnSeq, record.RequestID, record.Status)
	return entry
}

func completionGateValueMap(record completionGateRecord) map[string]any {
	encoded, err := json.Marshal(completionGateRecord{
		SchemaVersion:            completionGateSchemaVersion,
		Status:                   strings.TrimSpace(record.Status),
		RetryCount:               record.RetryCount,
		Gaps:                     append([]string(nil), record.Gaps...),
		TurnSeq:                  record.TurnSeq,
		RequestID:                strings.TrimSpace(record.RequestID),
		MutationToolCount:        record.MutationToolCount,
		VerificationCommandCount: record.VerificationCommandCount,
		VerificationStale:        record.VerificationStale,
		VerificationEvidence:     strings.TrimSpace(record.VerificationEvidence),
		VerificationEvidenceID:   strings.TrimSpace(record.VerificationEvidenceID),
	})
	if err != nil {
		return map[string]any{"schema_version": completionGateSchemaVersion, "status": strings.TrimSpace(record.Status)}
	}
	var values map[string]any
	if err := json.Unmarshal(encoded, &values); err != nil {
		return map[string]any{"schema_version": completionGateSchemaVersion, "status": strings.TrimSpace(record.Status)}
	}
	return values
}

func completionGateIdempotencyKey(turnSeq int64, requestID string, status string) string {
	normalizedStatus := strings.TrimSpace(status)
	if normalizedStatus == completionGateStatusInsufficientFirst {
		normalizedStatus = "retry"
	}
	return strings.Join([]string{
		"completion_gate",
		strconv.Itoa(completionGateSchemaVersion),
		strconv.FormatInt(turnSeq, 10),
		strings.TrimSpace(requestID),
		normalizedStatus,
	}, ":")
}

func completionGatePromptIdempotencyKey(turnSeq int64, requestID string) string {
	return strings.Join([]string{
		"completion_gate_prompt",
		strconv.Itoa(completionGateSchemaVersion),
		strconv.FormatInt(turnSeq, 10),
		strings.TrimSpace(requestID),
	}, ":")
}

func newCompletionGatePromptContext(turnSeq int64, requestID string) HistoryEntry {
	context := newPromptContextMessage(
		promptContextSourceCompletionEvidenceGate,
		modeladapter.Message{
			Role:    "user",
			Content: wrapSystemReminder(completionEvidenceGateReminderText),
		},
		true,
	)
	entry := newPromptContextEntry(turnSeq, requestID, context)
	entry.IdempotencyKey = completionGatePromptIdempotencyKey(turnSeq, requestID)
	return entry
}

func gateRecordFromDecision(turnSeq int64, requestID string, decision completionGateDecision) completionGateRecord {
	return completionGateRecord{
		SchemaVersion:            completionGateSchemaVersion,
		Status:                   decision.Status,
		RetryCount:               decision.RetryCount,
		Gaps:                     append([]string(nil), decision.Gaps...),
		TurnSeq:                  turnSeq,
		RequestID:                strings.TrimSpace(requestID),
		MutationToolCount:        decision.Summary.MutationToolCount,
		VerificationCommandCount: decision.Summary.VerificationCommandCount,
		VerificationStale:        decision.Summary.VerificationStale,
		VerificationEvidence:     decision.Summary.VerificationEvidence,
		VerificationEvidenceID:   decision.Summary.VerificationEvidenceID,
	}
}

func (service *Service) applyCompletionEvidenceGate(stream *ActiveStream, completion pendingTurnCompletion) (bool, error) {
	if stream == nil || service == nil {
		return false, nil
	}
	if pendingBridgeCount(stream) > 0 {
		return false, nil
	}
	requestID := firstNonEmpty(strings.TrimSpace(completion.RequestID), strings.TrimSpace(stream.RequestID))
	conversationID := firstNonEmpty(strings.TrimSpace(completion.ConversationID), strings.TrimSpace(stream.ConversationID))
	modelCallID := firstNonEmpty(strings.TrimSpace(completion.ModelCallID), strings.TrimSpace(stream.CurrentModelCallID))
	turnSeq := completion.TurnSeq
	if turnSeq <= 0 {
		turnSeq = stream.TurnSeq
	}
	conversation, pendingExecs, pendingInteractions, err := service.snapshotCheckpointConversation(stream)
	if err != nil {
		return false, err
	}
	if conversationHasTurnCompleted(conversation.Entries, turnSeq, requestID) {
		return true, nil
	}
	stream.mu.Lock()
	latestUserText := stream.LatestUserText
	mode := stream.Mode
	resumePending := stream.PendingProviderAction == providerActionResume
	stream.mu.Unlock()
	decision := evaluateCompletionEvidenceGate(completionGateInput{
		Mode:              mode,
		LatestUserText:    latestUserText,
		TurnSeq:           turnSeq,
		RequestID:         requestID,
		Entries:           conversation.Entries,
		PendingExecCount:  len(pendingExecs),
		PendingInterCount: len(pendingInteractions),
	})
	switch decision.Status {
	case completionGateStatusInsufficientFirst:
		if err := service.persistCompletionGateRetry(stream, conversationID, turnSeq, requestID, decision); err != nil {
			return true, err
		}
		if err := service.syncSummaryCarryForward(conversationID, requestID, modelCallID); err != nil {
			return true, err
		}
		if err := service.publishCheckpoint(requestID, conversationID); err != nil {
			return true, err
		}
		if err := service.requestProviderAction(stream, providerActionResume); err != nil {
			return true, err
		}
		service.recordModelCallFinal(stream, "succeeded")
		return true, nil
	case completionGateStatusInsufficientAfterRetry:
		if resumePending {
			return true, nil
		}
		if err := service.persistCompletionGateDiagnostic(stream, conversationID, turnSeq, requestID, decision); err != nil {
			return false, err
		}
		return false, nil
	default:
		return false, nil
	}
}

func (service *Service) persistCompletionGateRetry(stream *ActiveStream, conversationID string, turnSeq int64, requestID string, decision completionGateDecision) error {
	entries := []HistoryEntry{
		newCompletionGateEntry(gateRecordFromDecision(turnSeq, requestID, decision)),
		newCompletionGatePromptContext(turnSeq, requestID),
	}
	_, err := service.appendConversationEntries(stream, conversationID, entries)
	return err
}

func (service *Service) persistCompletionGateDiagnostic(stream *ActiveStream, conversationID string, turnSeq int64, requestID string, decision completionGateDecision) error {
	_, err := service.appendConversationEntries(stream, conversationID, []HistoryEntry{
		newCompletionGateEntry(gateRecordFromDecision(turnSeq, requestID, decision)),
	})
	return err
}
