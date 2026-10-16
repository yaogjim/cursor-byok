package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

func TestCompactionFailureDoesNotPersistProviderResponseBody(t *testing.T) {
	secret := "provider response contains sk-secret and user prompt"
	entry := newCompactionFailedEntry(nil, providerTerminalError{cause: &modeladapter.HTTPStatusError{
		StatusCode: 500,
		Body:       secret,
	}})
	payload := string(entry.Payload)
	if strings.Contains(payload, secret) || strings.Contains(payload, "sk-secret") || strings.Contains(payload, "user prompt") {
		t.Fatalf("compaction failure leaked provider response: %s", payload)
	}
	if !strings.Contains(payload, "server_5xx status=500") {
		t.Fatalf("compaction failure missing controlled provider category: %s", payload)
	}
}

func TestApplyCompactionToConversationPreservesCanonicalHistory(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	originalEntries := append([]HistoryEntry(nil), conversation.Entries...)
	plan := &PendingCompaction{
		Trigger:          "manual",
		CurrentTurnSeq:   2,
		CurrentRequestID: "request-2",
	}

	if err := applyCompactionToConversation(conversation, plan, "earlier context summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}
	if len(conversation.Entries) <= len(originalEntries) {
		t.Fatalf("entries after compaction = %d, want the %d original entries plus a summary marker", len(conversation.Entries), len(originalEntries))
	}
	if !reflect.DeepEqual(conversation.Entries[:len(originalEntries)], originalEntries) {
		t.Fatal("compaction changed the canonical history prefix")
	}

	projector := NewHistoryProjector()
	projection, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if len(projection.State.GetTurns()) != 2 {
		t.Fatalf("checkpoint turns after compaction = %d, want 2 visible turns", len(projection.State.GetTurns()))
	}
	replay, err := projector.ProjectPromptReplay(conversation)
	if err != nil {
		t.Fatalf("ProjectPromptReplay() error = %v", err)
	}
	if len(replay) != 1 || replay[0].Role != "user" || !strings.Contains(replay[0].Content, "earlier context summary") {
		t.Fatalf("prompt replay after compaction = %#v, want only the compacted summary", replay)
	}
}

func TestCompactedPromptProjectionPlacesSummaryBeforePreservedCurrentTurn(t *testing.T) {
	conversation := &ConversationFile{
		ConversationID:     "conversation-1",
		RootConversationID: "conversation-1",
		Mode:               "agent",
		NextTurnSeq:        1,
		NextEntrySeq:       1,
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "current question", "message-1"),
		newToolCallEntry(1, "request-1", "call-1", "Read", "", "", checkpointTestReadToolCall(t, nil)),
		newToolResultEntry(1, "request-1", "call-1", "Read", `{"path":"/tmp/example.txt"}`, "file contents", "", checkpointTestReadToolCall(t, nil)),
	})
	plan := &PendingCompaction{
		Trigger:                   "auto",
		CurrentTurnSeq:            1,
		CurrentRequestID:          "request-1",
		PreserveCurrentTurnInputs: true,
	}
	if err := applyCompactionToConversation(conversation, plan, "current progress summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}

	projected := compactedPromptProjectionEntries(conversation.Entries)
	promptKinds := make([]string, 0, len(projected))
	for _, entry := range projected {
		if isPromptReplayEntryKind(entry.Kind) {
			promptKinds = append(promptKinds, entry.Kind)
		}
	}
	want := []string{"compacted_summary", "user_message", "tool_call", "tool_result"}
	if !reflect.DeepEqual(promptKinds, want) {
		t.Fatalf("compacted prompt entry order = %#v, want %#v", promptKinds, want)
	}
}

func TestCompactionPlanningDoesNotRecompactArchivedHistory(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	if err := applyCompactionToConversation(conversation, &PendingCompaction{
		Trigger:          "manual",
		CurrentTurnSeq:   2,
		CurrentRequestID: "request-2",
	}, "archived history summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 3, "request-3", "new question", "message-3"),
	})

	plan, err := (&Service{}).buildLegacyCompactionPlan(&compactionPlan{
		CurrentTurnSeq:   3,
		CurrentRequestID: "request-3",
	}, conversation, false, 0)
	if err != nil {
		t.Fatalf("buildLegacyCompactionPlan() error = %v", err)
	}
	if plan == nil || plan.NativeArchiveSource == nil {
		t.Fatal("生产 plan 未冻结摘要后的新增前缀")
	}
	if plan.NativeArchiveSource.PrefixStart < 0 || plan.NativeArchiveSource.PrefixEnd > len(conversation.Entries) {
		t.Fatalf("冻结区间越界: %#v", plan.NativeArchiveSource)
	}
	prefix := conversation.Entries[plan.NativeArchiveSource.PrefixStart:plan.NativeArchiveSource.PrefixEnd]
	for _, entry := range prefix {
		if strings.TrimSpace(entry.Kind) == "user_message" {
			user := &agentv1.UserMessage{}
			if protojson.Unmarshal(entry.Payload, user) != nil {
				continue
			}
			if strings.Contains(user.GetText(), "first question") || strings.Contains(user.GetText(), "second question") {
				t.Fatalf("新增归档前缀重收已归档历史: %q", user.GetText())
			}
		}
	}
	if len(plan.CompactedTurns) != 1 || plan.CompactedTurns[0].UserText != "new question" {
		t.Fatalf("CompactedTurns = %#v, want only the new question", plan.CompactedTurns)
	}
}

func TestCompactionReserveUsesProportionalWindowBudget(t *testing.T) {
	if got := compactionReserveTokens(200_000); got != 20_000 {
		t.Fatalf("200k reserve = %d, want 20000", got)
	}
	if got := compactionReserveTokens(1_000_000); got != 100_000 {
		t.Fatalf("1m reserve = %d, want 100000", got)
	}
	if got := compactionReserveTokens(50_000); got != compactionAutoReserveTokens {
		t.Fatalf("50k reserve = %d, want floor %d", got, compactionAutoReserveTokens)
	}
	if got := compactionReserveTokens(5_000); got != compactionAutoReserveTokens {
		t.Fatalf("small window reserve = %d, want floor %d", got, compactionAutoReserveTokens)
	}
	if got := compactionBudgetTokens(5_000); got != 0 {
		t.Fatalf("small window budget = %d, want 0", got)
	}
	if got := compactionBudgetTokens(200_000); got != 180_000 {
		t.Fatalf("200k budget = %d, want 180000", got)
	}
}

func TestCompactionSummaryRequestTrimsOldestCompleteTurns(t *testing.T) {
	service := &Service{}
	oldest := compactedTurnSummary{UserText: strings.Repeat("oldest-turn-content ", 800)}
	middle := compactedTurnSummary{UserText: strings.Repeat("middle-turn-content ", 800)}
	newest := compactedTurnSummary{UserText: "newest turn must remain"}
	plan := &PendingCompaction{
		ContextWindowSize: 20_000,
		ExistingSummary:   "keep existing summary",
		HookMessage:       "hook guidance",
		ManualInstruction: "manual emphasis",
		CompactedTurns:    []compactedTurnSummary{oldest, middle, newest},
	}
	originalTurns := append([]compactedTurnSummary(nil), plan.CompactedTurns...)
	messages, useFallback, err := service.prepareCompactionSummaryMessages(plan)
	if err != nil {
		t.Fatalf("prepareCompactionSummaryMessages() error = %v", err)
	}
	if useFallback {
		t.Fatal("expected a fitted summary request, got local fallback")
	}
	if len(messages) != 2 || messages[len(messages)-1].Role != "user" {
		t.Fatalf("summary messages = %#v, want system+user", messages)
	}
	userText := messages[1].Content
	if strings.Contains(userText, "oldest-turn-content") {
		t.Fatalf("oldest complete turn was not trimmed: %s", userText)
	}
	if !strings.Contains(userText, "newest turn must remain") {
		t.Fatal("newest complete turn was trimmed")
	}
	if !strings.Contains(userText, "keep existing summary") || !strings.Contains(userText, "hook guidance") {
		t.Fatal("fixed summary inputs were dropped")
	}
	budget := compactionBudgetTokens(plan.ContextWindowSize)
	if estimateModelMessagesTokens(messages)+compactionSummaryOutputMaxTokens > budget {
		t.Fatalf("fitted request still exceeds budget estimated=%d budget=%d", estimateModelMessagesTokens(messages)+compactionSummaryOutputMaxTokens, budget)
	}
	if !reflect.DeepEqual(plan.CompactedTurns, originalTurns) {
		t.Fatal("summary trimming mutated PendingCompaction turns")
	}
}

func TestCompactionSummaryFallsBackWhenFixedInputDoesNotFit(t *testing.T) {
	service := &Service{}
	plan := &PendingCompaction{
		ContextWindowSize: 12_000,
		ExistingSummary:   strings.Repeat("fixed-summary ", 4000),
		CompactedTurns:    []compactedTurnSummary{{UserText: strings.Repeat("only-turn ", 4000)}},
	}
	originalTurns := append([]compactedTurnSummary(nil), plan.CompactedTurns...)
	messages, useFallback, err := service.prepareCompactionSummaryMessages(plan)
	if err != nil {
		t.Fatalf("prepareCompactionSummaryMessages() error = %v", err)
	}
	if !useFallback {
		t.Fatalf("got messages=%#v, want local fallback without sending empty history", messages)
	}
	if len(messages) != 0 {
		t.Fatalf("fallback still produced a summary request: %#v", messages)
	}
	if !reflect.DeepEqual(plan.CompactedTurns, originalTurns) {
		t.Fatal("fallback mutated PendingCompaction turns")
	}
}

func TestCompactionSummaryOverflowUsesLocalFallbackOnce(t *testing.T) {
	plan := &PendingCompaction{
		ExistingSummary: "keep this",
		CompactedTurns:  []compactedTurnSummary{{UserText: "turn one"}},
	}
	overflow := providerTerminalError{cause: &modeladapter.HTTPStatusError{
		StatusCode: 400,
		Code:       "context_length_exceeded",
		Body:       "prompt is too long",
	}}
	got, err := resolveCompactionSummaryResult(plan, "", overflow)
	if err != nil {
		t.Fatalf("overflow summary result error = %v", err)
	}
	if !strings.Contains(got, "keep this") || !strings.Contains(got, "turn one") {
		t.Fatalf("fallback summary = %q", got)
	}
	canceled := context.Canceled
	if _, err := resolveCompactionSummaryResult(plan, "", canceled); err == nil {
		t.Fatal("non-overflow summary errors must not fall back")
	}
}

func TestApplyCompactionPlanPersistsHistoryAppendOnly(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	conversation := compactionAppendOnlyConversation(t)
	if _, _, err := store.AppendEntries(conversation.ConversationID, resetEntrySequences(conversation.Entries)); err != nil {
		t.Fatalf("AppendEntries() error = %v", err)
	}
	persisted, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatalf("initial LoadConversation() error = %v", err)
	}
	originalEntries := append([]HistoryEntry(nil), persisted.Entries...)
	projector := NewHistoryProjector()
	service := &Service{
		store:     store,
		projector: projector,
		compiler:  compactionProjectionCompiler{projector: projector},
	}
	stream := &ActiveStream{
		RequestID:              "request-2",
		ConversationID:         conversation.ConversationID,
		TurnSeq:                2,
		Mode:                   agentv1.AgentMode_AGENT_MODE_AGENT,
		CheckpointConversation: persisted,
	}
	plan := &PendingCompaction{
		Trigger:           "manual",
		CurrentTurnSeq:    2,
		CurrentRequestID:  "request-2",
		ContextWindowSize: 1_000_000,
	}
	if err := service.applyCompactionPlan(stream, conversation.ConversationID, plan, "persisted summary"); err != nil {
		t.Fatalf("applyCompactionPlan() error = %v", err)
	}

	loaded, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(loaded.Entries) <= len(originalEntries) {
		t.Fatalf("persisted entries after compaction = %d, want more than %d", len(loaded.Entries), len(originalEntries))
	}
	for index := range originalEntries {
		if !reflect.DeepEqual(loaded.Entries[index], originalEntries[index]) {
			t.Fatalf("persisted history entry %d changed after compaction:\ngot  %#v\nwant %#v", index, loaded.Entries[index], originalEntries[index])
		}
	}
}

type compactionProjectionCompiler struct {
	projector *HistoryProjector
}

func (compiler compactionProjectionCompiler) Compile(conversation *ConversationFile, _ agentv1.AgentMode, _ string, _ string) (CompiledConversation, error) {
	messages, err := compiler.projector.ProjectPromptReplay(conversation)
	return CompiledConversation{Messages: messages}, err
}

func (compactionProjectionCompiler) DerivePromptContexts(*ConversationFile, agentv1.AgentMode, string) ([]PromptContextMessage, error) {
	return nil, nil
}

func compactionAppendOnlyConversation(t *testing.T) *ConversationFile {
	t.Helper()
	conversation := &ConversationFile{
		ConversationID:         "conversation-1",
		RootConversationID:     "conversation-1",
		Mode:                   "agent",
		NextTurnSeq:            1,
		NextEntrySeq:           1,
		TokenDetailsUsedTokens: 42_000,
		TokenDetailsMaxTokens:  50_000,
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "first question", "message-1"),
		newAssistantTextEntry(1, "request-1", "first answer", "", ""),
		compactionTestUserEntry(t, 2, "request-2", "second question", "message-2"),
		newAssistantTextEntry(2, "request-2", "second answer", "", ""),
	})
	return conversation
}

func compactionTestUserEntry(t *testing.T, turnSeq int64, requestID string, text string, messageID string) HistoryEntry {
	t.Helper()
	payload, err := protojson.Marshal(&agentv1.UserMessage{Text: text, MessageId: messageID})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: requestID,
		Role:      "user",
		Kind:      "user_message",
		Payload:   payload,
	}
}

var _ PromptCompiler = compactionProjectionCompiler{}

// 以下为 §19.3g「连续尾段最小实施合同」的永久回归。
// 精确规则：只以 entries[:summaryIndex] 计算保留集合，取其中最早普通 prompt 条目为
// 连续尾段起点，必要时扩展到完整工具批次首项；输出最新摘要→尾段→摘要后新增，
// 旧摘要不混入尾段，非 prompt 元数据保留且不可重复，原始 Entries 不变。

func projectedPromptEntryKinds(entries []HistoryEntry) []string {
	kinds := make([]string, 0, len(entries))
	for _, entry := range entries {
		if isPromptReplayEntryKind(entry.Kind) {
			kinds = append(kinds, strings.TrimSpace(entry.Kind))
		}
	}
	return kinds
}

func tailReadToolCallEntry(t *testing.T, turnSeq int64, requestID string, toolCallID string, modelCallID string) HistoryEntry {
	t.Helper()
	return withHistoryModelCallID(newToolCallEntry(turnSeq, requestID, toolCallID, "Read", "", "", checkpointTestReadToolCall(t, nil)), modelCallID)
}

func tailReadToolResultEntry(t *testing.T, turnSeq int64, requestID string, toolCallID string, modelCallID string, path string, result string) HistoryEntry {
	t.Helper()
	return withHistoryModelCallID(newToolResultEntry(turnSeq, requestID, toolCallID, "Read", `{"path":"`+path+`"}`, result, "", nil), modelCallID)
}

func appendTailSummary(conversation *ConversationFile, plan *PendingCompaction, summaryText string) {
	appendEntriesInPlace(conversation, []HistoryEntry{newCompactionSummaryEntry(plan, summaryText)})
}

// TestCompactedPromptProjectionKeepsContinuousTailTools 固化：原稀疏算法只保留最新一组工具，
// 会漏掉同一连续尾段中的中间工具；连续尾段必须保序包含它们。
func TestCompactedPromptProjectionKeepsContinuousTailTools(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation-tail", Mode: "agent", NextTurnSeq: 3}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "old question", "message-1"),
		newAssistantTextEntry(1, "request-1", "old answer", "", ""),
		compactionTestUserEntry(t, 2, "request-2", "current question", "message-2"),
		tailReadToolCallEntry(t, 2, "request-2", "call-1", "model-call-1"),
		tailReadToolResultEntry(t, 2, "request-2", "call-1", "model-call-1", "/tmp/one.txt", "one contents"),
		tailReadToolCallEntry(t, 2, "request-2", "call-2", "model-call-2"),
		tailReadToolResultEntry(t, 2, "request-2", "call-2", "model-call-2", "/tmp/two.txt", "two contents"),
	})
	appendTailSummary(conversation, &PendingCompaction{Trigger: "auto", CurrentTurnSeq: 2, CurrentRequestID: "request-2", PreserveCurrentTurnInputs: true}, "current summary")

	projected := compactedPromptProjectionEntries(conversation.Entries)
	want := []string{"compacted_summary", "user_message", "tool_call", "tool_result", "tool_call", "tool_result"}
	if got := projectedPromptEntryKinds(projected); !reflect.DeepEqual(got, want) {
		t.Fatalf("continuous tail kinds = %#v, want %#v", got, want)
	}
	if len(conversation.Entries) != 8 {
		t.Fatalf("projection mutated original entries: %d", len(conversation.Entries))
	}
}

// TestCompactedPromptProjectionFreezesBoundaryAgainstPostSummaryTools 固化：摘要之后新增的
// 工具不能再改变压缩时点算出的旧边界。
func TestCompactedPromptProjectionFreezesBoundaryAgainstPostSummaryTools(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation-tail-freeze", Mode: "agent", NextTurnSeq: 3}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "old question", "message-1"),
		newAssistantTextEntry(1, "request-1", "old answer", "", ""),
		compactionTestUserEntry(t, 2, "request-2", "current question", "message-2"),
		tailReadToolCallEntry(t, 2, "request-2", "call-1", "model-call-1"),
		tailReadToolResultEntry(t, 2, "request-2", "call-1", "model-call-1", "/tmp/one.txt", "one contents"),
		tailReadToolCallEntry(t, 2, "request-2", "call-2", "model-call-2"),
		tailReadToolResultEntry(t, 2, "request-2", "call-2", "model-call-2", "/tmp/two.txt", "two contents"),
		newCompactionSummaryEntry(&PendingCompaction{Trigger: "auto", CurrentTurnSeq: 2, CurrentRequestID: "request-2", PreserveCurrentTurnInputs: true}, "current summary"),
	})
	appendEntriesInPlace(conversation, []HistoryEntry{
		tailReadToolCallEntry(t, 2, "request-2", "call-3", "model-call-3"),
		tailReadToolResultEntry(t, 2, "request-2", "call-3", "model-call-3", "/tmp/three.txt", "three contents"),
	})

	projected := compactedPromptProjectionEntries(conversation.Entries)
	want := []string{"compacted_summary", "user_message", "tool_call", "tool_result", "tool_call", "tool_result", "tool_call", "tool_result"}
	if got := projectedPromptEntryKinds(projected); !reflect.DeepEqual(got, want) {
		t.Fatalf("frozen boundary kinds = %#v, want %#v", got, want)
	}
	// 压缩前的中间工具必须仍在边界内，摘要后新增的工具只是追加。
	toolCallIDs := make([]string, 0, 3)
	for _, entry := range projected {
		if strings.TrimSpace(entry.Kind) == "tool_call" {
			toolCallIDs = append(toolCallIDs, strings.TrimSpace(entry.ToolCallID))
		}
	}
	if !reflect.DeepEqual(toolCallIDs, []string{"call-1", "call-2", "call-3"}) {
		t.Fatalf("frozen tool call order = %#v, want call-1/call-2 before appended call-3", toolCallIDs)
	}
}

// TestCompactedPromptProjectionSecondCompactionKeepsTailAndDuplicates 固化：两次压缩后仍是
// 最新摘要+连续尾段，旧摘要不混入尾段，重复实例按内容保留不哈希去重。
func TestCompactedPromptProjectionSecondCompactionKeepsTailAndDuplicates(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation-twice", Mode: "agent", NextTurnSeq: 3}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "first question", "message-1"),
		newAssistantTextEntry(1, "request-1", "first answer", "", ""),
		newCompactionSummaryEntry(&PendingCompaction{Trigger: "manual", CurrentTurnSeq: 1, CurrentRequestID: "request-1"}, "first summary"),
		compactionTestUserEntry(t, 2, "request-2", "second question", "message-2"),
		tailReadToolCallEntry(t, 2, "request-2", "call-1", "model-call-1"),
		tailReadToolResultEntry(t, 2, "request-2", "call-1", "model-call-1", "/tmp/dup.txt", "duplicate content"),
		tailReadToolCallEntry(t, 2, "request-2", "call-2", "model-call-2"),
		tailReadToolResultEntry(t, 2, "request-2", "call-2", "model-call-2", "/tmp/dup.txt", "duplicate content"),
		newCompactionSummaryEntry(&PendingCompaction{Trigger: "auto", CurrentTurnSeq: 2, CurrentRequestID: "request-2", PreserveCurrentTurnInputs: true}, "second summary"),
	})

	projected := compactedPromptProjectionEntries(conversation.Entries)
	want := []string{"compacted_summary", "user_message", "tool_call", "tool_result", "tool_call", "tool_result"}
	if got := projectedPromptEntryKinds(projected); !reflect.DeepEqual(got, want) {
		t.Fatalf("second compaction kinds = %#v, want %#v", got, want)
	}
	var summaries int
	resultTexts := make([]string, 0, 2)
	for _, entry := range projected {
		switch strings.TrimSpace(entry.Kind) {
		case "compacted_summary", "compaction_summary":
			summaries++
			if text, ok := decodeCompactionSummaryEntry(entry); !ok || text != "second summary" {
				t.Fatalf("projected summary = %q, want only the latest summary", text)
			}
		case "tool_result":
			var payload toolResultEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				t.Fatalf("decode projected tool_result: %v", err)
			}
			resultTexts = append(resultTexts, payload.ResultText)
		}
	}
	if summaries != 1 {
		t.Fatalf("projected summaries = %d, want only the latest", summaries)
	}
	if !reflect.DeepEqual(resultTexts, []string{"duplicate content", "duplicate content"}) {
		t.Fatalf("duplicate tool results = %#v, want both instances retained", resultTexts)
	}

	// 该纯投影同时供下一次压缩候选使用：归档前缀（含旧摘要）不得重新进入候选。
	candidates := buildContextCompactionCandidates(replayablePromptProjectionEntries(conversation.Entries), 3, "request-3")
	if len(candidates) != 1 {
		t.Fatalf("next compaction candidates = %d, want only the surviving turn-2 tail", len(candidates))
	}
	if candidates[0].ReplayCount != 5 {
		t.Fatalf("next compaction candidate replay count = %d, want the full continuous tail 5", candidates[0].ReplayCount)
	}
}

// TestCompactedPromptProjectionResumeKeepsSiblingToolCalls 固化：resume 后本轮 user 仍带较早
// requestID，保留集合可能从批次里后一个 tool_call 起步；必须扩展到完整批次首项，
// 保留同批兄弟调用，且不凭 ID 前缀猜批次。
func TestCompactedPromptProjectionResumeKeepsSiblingToolCalls(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation-resume", Mode: "agent", NextTurnSeq: 2}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-original", "resumed question", "message-1"),
		tailReadToolCallEntry(t, 1, "request-1", "call-1", "model-call-1"),
		tailReadToolCallEntry(t, 1, "request-1", "call-2", "model-call-1"),
		tailReadToolResultEntry(t, 1, "request-1", "call-1", "model-call-1", "/tmp/one.txt", "one contents"),
		tailReadToolResultEntry(t, 1, "request-1", "call-2", "model-call-1", "/tmp/two.txt", "two contents"),
		newCompactionSummaryEntry(&PendingCompaction{Trigger: "auto", CurrentTurnSeq: 1, CurrentRequestID: "request-1", PreserveCurrentTurnInputs: true}, "resume summary"),
	})

	projected := compactedPromptProjectionEntries(conversation.Entries)
	want := []string{"compacted_summary", "tool_call", "tool_call", "tool_result", "tool_result"}
	if got := projectedPromptEntryKinds(projected); !reflect.DeepEqual(got, want) {
		t.Fatalf("resume batch kinds = %#v, want %#v", got, want)
	}
	toolCallIDs := make([]string, 0, 2)
	for _, entry := range projected {
		if strings.TrimSpace(entry.Kind) == "tool_call" {
			toolCallIDs = append(toolCallIDs, strings.TrimSpace(entry.ToolCallID))
		}
	}
	if !reflect.DeepEqual(toolCallIDs, []string{"call-1", "call-2"}) {
		t.Fatalf("sibling tool calls = %#v, want both siblings retained", toolCallIDs)
	}
}

// TestCompactedPromptProjectionTruncatesOnlyPreservedToolResults 固化：结果展示裁剪只作用于
// 原保留集合中的条目，新纳入连续尾段的其它工具结果不额外裁剪。
func TestCompactedPromptProjectionTruncatesOnlyPreservedToolResults(t *testing.T) {
	largeResult := strings.Repeat("x", autoCompactionPreservedToolResultLimitBytes+2048)
	conversation := &ConversationFile{ConversationID: "conversation-truncate", Mode: "agent", NextTurnSeq: 3}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 2, "request-2", "current question", "message-1"),
		tailReadToolCallEntry(t, 2, "request-2", "call-1", "model-call-1"),
		tailReadToolResultEntry(t, 2, "request-2", "call-1", "model-call-1", "/tmp/one.txt", largeResult),
		tailReadToolCallEntry(t, 2, "request-2", "call-2", "model-call-2"),
		tailReadToolResultEntry(t, 2, "request-2", "call-2", "model-call-2", "/tmp/two.txt", largeResult),
	})
	appendTailSummary(conversation, &PendingCompaction{Trigger: "auto", CurrentTurnSeq: 2, CurrentRequestID: "request-2", PreserveCurrentTurnInputs: true}, "current summary")

	projected := compactedPromptProjectionEntries(conversation.Entries)
	results := make(map[string]string)
	for _, entry := range projected {
		if strings.TrimSpace(entry.Kind) != "tool_result" {
			continue
		}
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatalf("decode projected tool_result: %v", err)
		}
		results[strings.TrimSpace(payload.ToolCallID)] = payload.ResultText
	}
	if len(results) != 2 {
		for id, text := range results {
			t.Logf("projected tool result %s len=%d", id, len(text))
		}
		t.Fatalf("projected tool results = %d, want both continuous tail results", len(results))
	}
	if got := results["call-1"]; len(got) != len(largeResult) {
		t.Fatalf("newly included tail result was rewritten: len=%d, want untouched len=%d", len(got), len(largeResult))
	}
	if got := results["call-2"]; len(got) >= len(largeResult) {
		t.Fatalf("preserved result was not truncated: len=%d", len(got))
	}
}

// TestCompactedPromptProjectionStillAppliesCancelFiltering 固化：连续尾段投影后仍复用既有
// 取消过滤，取消政策不变。
func TestCompactedPromptProjectionStillAppliesCancelFiltering(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation-cancel", Mode: "agent", NextTurnSeq: 3}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "old question", "message-1"),
		newAssistantTextEntry(1, "request-1", "old answer", "", ""),
		newCompactionSummaryEntry(&PendingCompaction{Trigger: "manual", CurrentTurnSeq: 1, CurrentRequestID: "request-1"}, "archived summary"),
		compactionTestUserEntry(t, 2, "request-2", "canceled question", "message-2"),
		newMetadataEntry(2, "request-2", "control", map[string]any{"status": "canceled", "replay_policy": "drop_turn"}),
	})

	compacted := compactedPromptProjectionEntries(conversation.Entries)
	if got := projectedPromptEntryKinds(compacted); !reflect.DeepEqual(got, []string{"compacted_summary", "user_message"}) {
		t.Fatalf("compacted projection kinds = %#v, want summary plus the tail user message", got)
	}
	replayable := replayablePromptProjectionEntries(conversation.Entries)
	if got := projectedPromptEntryKinds(replayable); !reflect.DeepEqual(got, []string{"compacted_summary"}) {
		t.Fatalf("cancel filtering kinds = %#v, want only the surviving summary", got)
	}
}

// TestCompactedPromptProjectionHonorsForkTruncation 固化：fork 截断在投影之前发生，
// 投影只基于被截断的历史，不引用被丢弃回合。
func TestCompactedPromptProjectionHonorsForkTruncation(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation-fork", Mode: "agent", NextTurnSeq: 4}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "first question", "message-1"),
		newAssistantTextEntry(1, "request-1", "first answer", "", ""),
		compactionTestUserEntry(t, 2, "request-2", "second question", "message-2"),
		tailReadToolCallEntry(t, 2, "request-2", "call-1", "model-call-1"),
		tailReadToolResultEntry(t, 2, "request-2", "call-1", "model-call-1", "/tmp/one.txt", "one contents"),
		newCompactionSummaryEntry(&PendingCompaction{Trigger: "auto", CurrentTurnSeq: 2, CurrentRequestID: "request-2", PreserveCurrentTurnInputs: true}, "second turn summary"),
		compactionTestUserEntry(t, 3, "request-3", "forked question", "message-3"),
		newAssistantTextEntry(3, "request-3", "forked answer", "", ""),
	})

	forked := &ConversationFile{ConversationID: conversation.ConversationID, Mode: conversation.Mode}
	forked.Entries = prefixEntriesBeforeTurn(conversation.Entries, 3)
	projected := compactedPromptProjectionEntries(forked.Entries)
	want := []string{"compacted_summary", "user_message", "tool_call", "tool_result"}
	if got := projectedPromptEntryKinds(projected); !reflect.DeepEqual(got, want) {
		t.Fatalf("forked projection kinds = %#v, want %#v", got, want)
	}
	for _, entry := range projected {
		if entry.TurnSeq >= 3 {
			t.Fatalf("forked projection retained dropped turn %d", entry.TurnSeq)
		}
	}
	if len(conversation.Entries) != 8 {
		t.Fatalf("projection mutated original entries: %d", len(conversation.Entries))
	}
}

// TestApplyCompactionPlanRejectsOverBudgetWithoutPersisting 固化：真实 applyCompactionPlan 在
// 编译后、持久化前的预算检查拒绝超预算候选，原历史与旧摘要保留。
func TestApplyCompactionPlanRejectsOverBudgetWithoutPersisting(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	conversation := compactionAppendOnlyConversation(t)
	if _, _, err := store.AppendEntries(conversation.ConversationID, resetEntrySequences(conversation.Entries)); err != nil {
		t.Fatalf("AppendEntries() error = %v", err)
	}
	persisted, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	originalEntries := append([]HistoryEntry(nil), persisted.Entries...)
	projector := NewHistoryProjector()
	service := &Service{
		store:     store,
		projector: projector,
		compiler:  compactionProjectionCompiler{projector: projector},
	}
	stream := &ActiveStream{
		RequestID:              "request-2",
		ConversationID:         conversation.ConversationID,
		TurnSeq:                2,
		Mode:                   agentv1.AgentMode_AGENT_MODE_AGENT,
		CheckpointConversation: persisted,
	}
	plan := &PendingCompaction{
		Trigger:           "manual",
		CurrentTurnSeq:    2,
		CurrentRequestID:  "request-2",
		ContextWindowSize: 20_000,
	}
	err = service.applyCompactionPlan(stream, conversation.ConversationID, plan, strings.Repeat("oversized summary body ", 20_000))
	if err == nil {
		t.Fatal("applyCompactionPlan() accepted an over-budget compaction")
	}
	var coded interface{ TerminalCode() string }
	if !errors.As(err, &coded) || coded.TerminalCode() != compactionOverflowTerminalCode {
		t.Fatalf("over-budget error = %v, want terminal code %q", err, compactionOverflowTerminalCode)
	}
	loaded, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(loaded.Entries) != len(originalEntries) {
		t.Fatalf("over-budget compaction persisted %d entries, want original %d", len(loaded.Entries), len(originalEntries))
	}
	for index := range originalEntries {
		if !reflect.DeepEqual(loaded.Entries[index], originalEntries[index]) {
			t.Fatalf("over-budget compaction changed persisted entry %d:\ngot  %#v\nwant %#v", index, loaded.Entries[index], originalEntries[index])
		}
	}
}

// projectedToolCallIDs 返回投影中 tool_call 条目的调用 ID 顺序。
func projectedToolCallIDs(projected []HistoryEntry) []string {
	ids := make([]string, 0)
	for _, entry := range projected {
		if strings.TrimSpace(entry.Kind) == "tool_call" {
			ids = append(ids, strings.TrimSpace(entry.ToolCallID))
		}
	}
	return ids
}

// TestCompactedPromptProjectionToolBatchToleratesInterleavedMetadata 固化：metadata/evidence 等
// 非 prompt 条目可以夹在同一次模型调用（同 turn/request/ModelCallID）的工具批次中，
// 它们不能截断同批次首项，也不能制造悬空结果。
func TestCompactedPromptProjectionToolBatchToleratesInterleavedMetadata(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation-batch-metadata", Mode: "agent", NextTurnSeq: 2}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-original", "resumed question", "message-1"),
		tailReadToolCallEntry(t, 1, "request-1", "call-1", "model-call-1"),
		newMetadataEntry(1, "request-1", "evidence", map[string]any{"kind": "tool"}),
		tailReadToolCallEntry(t, 1, "request-1", "call-2", "model-call-1"),
		tailReadToolResultEntry(t, 1, "request-1", "call-1", "model-call-1", "/tmp/one.txt", "one contents"),
		tailReadToolResultEntry(t, 1, "request-1", "call-2", "model-call-1", "/tmp/two.txt", "two contents"),
		newCompactionSummaryEntry(&PendingCompaction{Trigger: "auto", CurrentTurnSeq: 1, CurrentRequestID: "request-1", PreserveCurrentTurnInputs: true}, "resume summary"),
	})

	projected := compactedPromptProjectionEntries(conversation.Entries)
	want := []string{"compacted_summary", "tool_call", "tool_call", "tool_result", "tool_result"}
	if got := projectedPromptEntryKinds(projected); !reflect.DeepEqual(got, want) {
		t.Fatalf("batch with interleaved metadata kinds = %#v, want %#v", got, want)
	}
	if ids := projectedToolCallIDs(projected); !reflect.DeepEqual(ids, []string{"call-1", "call-2"}) {
		t.Fatalf("interleaved metadata batch tool calls = %#v, want both siblings before their results", ids)
	}
	// 夹在批次中的非 prompt 条目仍保留且只出现一次。
	var metadataCount int
	for _, entry := range projected {
		if strings.TrimSpace(entry.Kind) == "metadata" {
			metadataCount++
		}
	}
	if metadataCount != 1 {
		t.Fatalf("interleaved metadata count = %d, want exactly one preserved copy", metadataCount)
	}
	if len(conversation.Entries) != 7 {
		t.Fatalf("projection mutated original entries: %d", len(conversation.Entries))
	}
}

// TestCompactedPromptProjectionToolBatchStopsAtDifferentModelCall 对照：夹在中间的非 prompt
// 条目可被跳过，但不同真实调用身份的完整前一批不得被误扩展。
// 样本是合法工具序列：完整批次 call-1→result-1(mc1) 先结束，再 metadata，再批次
// call-2→result-2(mc2)；摘要保留 call-2，前一完整批整体留在归档前缀，不产生悬空结果。
func TestCompactedPromptProjectionToolBatchStopsAtDifferentModelCall(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation-batch-distinct", Mode: "agent", NextTurnSeq: 2}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-original", "resumed question", "message-1"),
		tailReadToolCallEntry(t, 1, "request-1", "call-1", "model-call-1"),
		tailReadToolResultEntry(t, 1, "request-1", "call-1", "model-call-1", "/tmp/one.txt", "one contents"),
		newMetadataEntry(1, "request-1", "evidence", map[string]any{"kind": "tool"}),
		tailReadToolCallEntry(t, 1, "request-1", "call-2", "model-call-2"),
		tailReadToolResultEntry(t, 1, "request-1", "call-2", "model-call-2", "/tmp/two.txt", "two contents"),
		newCompactionSummaryEntry(&PendingCompaction{Trigger: "auto", CurrentTurnSeq: 1, CurrentRequestID: "request-1", PreserveCurrentTurnInputs: true}, "resume summary"),
	})

	projected := compactedPromptProjectionEntries(conversation.Entries)
	want := []string{"compacted_summary", "tool_call", "tool_result"}
	if got := projectedPromptEntryKinds(projected); !reflect.DeepEqual(got, want) {
		t.Fatalf("distinct model-call batch kinds = %#v, want %#v", got, want)
	}
	if ids := projectedToolCallIDs(projected); !reflect.DeepEqual(ids, []string{"call-2"}) {
		t.Fatalf("distinct model-call batch tool calls = %#v, want only call-2 (no prefix guessing)", ids)
	}
	// 只保留与剩余调用匹配的 result，前一完整批不扩展、不产生悬空结果。
	resultIDs := make([]string, 0, 2)
	for _, entry := range projected {
		if strings.TrimSpace(entry.Kind) != "tool_result" {
			continue
		}
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatalf("decode projected tool_result: %v", err)
		}
		resultIDs = append(resultIDs, strings.TrimSpace(payload.ToolCallID))
	}
	if !reflect.DeepEqual(resultIDs, []string{"call-2"}) {
		t.Fatalf("distinct model-call batch results = %#v, want only the result matched to call-2", resultIDs)
	}
}

func TestNativeArchiveSourceRequiresAllFiveFields(t *testing.T) {
	if source, err := parseNativeArchiveSource(nil); err != nil || source != nil {
		t.Fatalf("缺对象应视为旧来源: source=%v err=%v", source, err)
	}
	if _, err := parseNativeArchiveSource(json.RawMessage(`{"prefix_start":0,"prefix_end":2,"candidate_count":2,"actual_count":2}`)); err == nil {
		t.Fatal("缺 window_tail 居然通过")
	}
	if _, err := parseNativeArchiveSource(json.RawMessage(`{"prefix_start":0,"prefix_end":0,"candidate_count":0,"actual_count":0,"window_tail":0,"extra":1}`)); err == nil {
		t.Fatal("未知字段居然通过")
	}
	source, err := parseNativeArchiveSource(json.RawMessage(`{"prefix_start":0,"prefix_end":0,"candidate_count":0,"actual_count":0,"window_tail":0}`))
	if err != nil || source == nil ||
		source.PrefixStart != 0 || source.PrefixEnd != 0 ||
		source.CandidateCount != 0 || source.ActualCount != 0 || source.WindowTail != 0 {
		t.Fatalf("合法 0 值来源失败: %#v err=%v", source, err)
	}
}

func TestParseNativeArchiveSourceRejectsNullAndNonIntegerFields(t *testing.T) {
	fields := []string{"prefix_start", "prefix_end", "candidate_count", "actual_count", "window_tail"}
	build := func(values map[string]string) string {
		parts := make([]string, 0, len(fields))
		for _, field := range fields {
			value, ok := values[field]
			if !ok {
				continue
			}
			parts = append(parts, `"`+field+`":`+value)
		}
		return `{` + strings.Join(parts, ",") + `}`
	}
	zeros := map[string]string{
		"prefix_start":    "0",
		"prefix_end":      "0",
		"candidate_count": "0",
		"actual_count":    "0",
		"window_tail":     "0",
	}
	source, err := parseNativeArchiveSource(json.RawMessage(build(zeros)))
	if err != nil || source == nil {
		t.Fatalf("合法 0 值来源失败: %#v err=%v", source, err)
	}
	if source.PrefixStart != 0 || source.PrefixEnd != 0 ||
		source.CandidateCount != 0 || source.ActualCount != 0 || source.WindowTail != 0 {
		t.Fatalf("合法 0 值应保留存在性: %#v", source)
	}

	invalidTokens := []string{"null", `"0"`, "1.5", "true", "[]", "{}"}
	for _, field := range fields {
		for _, token := range invalidTokens {
			values := map[string]string{
				"prefix_start":    "0",
				"prefix_end":      "0",
				"candidate_count": "0",
				"actual_count":    "0",
				"window_tail":     "0",
			}
			values[field] = token
			raw := build(values)
			parsed, parseErr := parseNativeArchiveSource(json.RawMessage(raw))
			if parseErr == nil || parsed != nil {
				t.Fatalf("非整数/null 字段居然通过: %s source=%#v", raw, parsed)
			}
		}
		missing := map[string]string{
			"prefix_start":    "0",
			"prefix_end":      "0",
			"candidate_count": "0",
			"actual_count":    "0",
			"window_tail":     "0",
		}
		delete(missing, field)
		raw := build(missing)
		parsed, parseErr := parseNativeArchiveSource(json.RawMessage(raw))
		if parseErr == nil || parsed != nil {
			t.Fatalf("缺 %s 居然通过: %s source=%#v", field, raw, parsed)
		}
	}
}

func TestProductionCompactionPlanFreezesHomologousPrefix(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil {
		t.Fatalf("buildAutoCompactionPlanFromHistory() error = %v", err)
	}
	if plan == nil || !plan.HomologousPrefixFrozen || plan.NativeArchiveSource == nil {
		t.Fatal("生产 plan 未冻结同源前缀")
	}
	src := plan.NativeArchiveSource
	if src.PrefixStart != 0 || src.PrefixEnd != 2 {
		t.Fatalf("冻结区间 = [%d,%d), want [0,2)", src.PrefixStart, src.PrefixEnd)
	}
	if src.CandidateCount != 2 || src.ActualCount != 2 || src.WindowTail != 0 {
		t.Fatalf("计数 = %#v, want A=h=2 window_tail=0", src)
	}
	if len(plan.CompactedTurns) != 1 || !strings.Contains(plan.CompactedTurns[0].UserText, "first question") {
		t.Fatalf("摘要历史未覆盖冻结前缀: %#v", plan.CompactedTurns)
	}
	cloned := clonePendingCompaction(newPendingCompaction(plan))
	if cloned == nil || cloned.NativeArchiveSource == nil || *cloned.NativeArchiveSource != *src || !cloned.HomologousPrefixFrozen {
		t.Fatal("clone/pending 丢失冻结来源")
	}
	cloned.NativeArchiveSource.PrefixEnd = 99
	if plan.NativeArchiveSource.PrefixEnd == 99 {
		t.Fatal("clone 未深拷贝来源")
	}
}

func TestPrepareCompactionSummaryFrozenPrefixRejectsOverBudgetWithoutTrim(t *testing.T) {
	service := &Service{}
	oldest := compactedTurnSummary{UserText: strings.Repeat("oldest-turn-content ", 800)}
	middle := compactedTurnSummary{UserText: strings.Repeat("middle-turn-content ", 800)}
	newest := compactedTurnSummary{UserText: "newest turn must remain"}
	plan := &PendingCompaction{
		ContextWindowSize:      20_000,
		ExistingSummary:        "keep existing summary",
		HookMessage:            "hook guidance",
		ManualInstruction:      "manual emphasis",
		CompactedTurns:         []compactedTurnSummary{oldest, middle, newest},
		HomologousPrefixFrozen: true,
	}
	originalTurns := append([]compactedTurnSummary(nil), plan.CompactedTurns...)
	messages, useFallback, err := service.prepareCompactionSummaryMessages(plan)
	if err == nil || useFallback || len(messages) != 0 {
		t.Fatalf("冻结前缀超预算应明确停止, messages=%#v fallback=%v err=%v", messages, useFallback, err)
	}
	var terminal compactionTerminalError
	if !errors.As(err, &terminal) || terminal.TerminalCode() != compactionOverflowTerminalCode {
		t.Fatalf("超预算错误 = %v, want compaction overflow terminal", err)
	}
	if !reflect.DeepEqual(plan.CompactedTurns, originalTurns) {
		t.Fatal("超预算准备改写了冻结回合")
	}
}

func TestPrepareCompactionSummaryFrozenPrefixAllowsOverflowFallbackAfterBudgetPass(t *testing.T) {
	plan := &PendingCompaction{
		ExistingSummary:        "keep this",
		CompactedTurns:         []compactedTurnSummary{{UserText: "turn one"}},
		HomologousPrefixFrozen: true,
		ContextWindowSize:      1_000_000,
	}
	messages, useFallback, err := (&Service{}).prepareCompactionSummaryMessages(plan)
	if err != nil || useFallback || len(messages) == 0 {
		t.Fatalf("预算内同源请求应发出: messages=%#v fallback=%v err=%v", messages, useFallback, err)
	}
	overflow := providerTerminalError{cause: &modeladapter.HTTPStatusError{
		StatusCode: 400,
		Code:       "context_length_exceeded",
		Body:       "prompt is too long",
	}}
	got, err := resolveCompactionSummaryResult(plan, "", overflow)
	if err != nil {
		t.Fatalf("预算通过后 overflow fallback error = %v", err)
	}
	if !strings.Contains(got, "keep this") || !strings.Contains(got, "turn one") {
		t.Fatalf("fallback summary = %q", got)
	}
}

func TestApplyCompactionPlanPersistsNativeArchiveSourceZeroWindowTail(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil || plan == nil {
		t.Fatalf("build plan error = %v plan=%v", err, plan)
	}
	pending := newPendingCompaction(plan)
	if err := applyCompactionToConversation(conversation, pending, "current progress summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}
	var summary HistoryEntry
	for _, entry := range conversation.Entries {
		if isCompactionSummaryKind(entry.Kind) {
			summary = entry
		}
	}
	if !strings.Contains(string(summary.Payload), `"window_tail":0`) {
		t.Fatalf("落盘未显式写出 window_tail=0: %s", summary.Payload)
	}
	var payload compactionSummaryEntryPayload
	if err := json.Unmarshal(summary.Payload, &payload); err != nil {
		t.Fatalf("decode summary payload: %v", err)
	}
	source, err := parseNativeArchiveSource(payload.NativeArchiveSource)
	if err != nil || source == nil || source.WindowTail != 0 || source.ActualCount != 2 {
		t.Fatalf("重读来源失败: %#v err=%v", source, err)
	}
}

func TestSuccessiveCompactionArchivesOnlyNewPrefix(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	first, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil || first == nil {
		t.Fatalf("first plan error = %v", err)
	}
	if err := applyCompactionToConversation(conversation, newPendingCompaction(first), "first summary"); err != nil {
		t.Fatalf("first apply error = %v", err)
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 3, "request-3", "third question", "message-3"),
		newAssistantTextEntry(3, "request-3", "third answer", "", ""),
		compactionTestUserEntry(t, 4, "request-4", "fourth question", "message-4"),
	})
	second, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            4,
		CurrentRequestID:          "request-4",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		ExistingSummary:           "first summary",
	}, conversation)
	if err != nil || second == nil || second.NativeArchiveSource == nil {
		t.Fatalf("second plan error = %v plan=%v", err, second)
	}
	if second.NativeArchiveSource.PrefixStart != first.NativeArchiveSource.PrefixEnd {
		t.Fatalf("第二次起点 %d 应等于第一次尾段起点/第一次 prefix_end %d", second.NativeArchiveSource.PrefixStart, first.NativeArchiveSource.PrefixEnd)
	}
	if second.NativeArchiveSource.WindowTail != 0 || second.NativeArchiveSource.CandidateCount != second.NativeArchiveSource.ActualCount {
		t.Fatalf("第二次计数非法: %#v", second.NativeArchiveSource)
	}
	if err := applyCompactionToConversation(conversation, newPendingCompaction(second), "second summary"); err != nil {
		t.Fatalf("second apply error = %v", err)
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("native writer error = %v", err)
	}
	if len(projection.State.GetSummaryArchive()) != 0 {
		t.Fatal("纯本地 singular 应为空")
	}
	if len(projection.State.GetSummaryArchives()) != 2 {
		t.Fatalf("archives=%d, want 2", len(projection.State.GetSummaryArchives()))
	}
}

func TestReasoningPrefixOmitsNativeSourceButKeepsUserCompaction(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation-reasoning", Mode: "agent", NextTurnSeq: 3}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "old question", "message-1"),
		newAssistantTextEntry(1, "request-1", "old answer", "hidden thought", ""),
		compactionTestUserEntry(t, 2, "request-2", "current question", "message-2"),
	})
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil || plan == nil || !plan.HomologousPrefixFrozen {
		t.Fatalf("域外前缀仍应冻结用户压缩: plan=%v err=%v", plan, err)
	}
	if plan.NativeArchiveSource != nil {
		t.Fatal("无法 native 编码时不得写出 native_archive_source")
	}
	if err := applyCompactionToConversation(conversation, newPendingCompaction(plan), "reasoning summary"); err != nil {
		t.Fatalf("用户压缩失败: %v", err)
	}
	found := false
	for _, entry := range conversation.Entries {
		if !isCompactionSummaryKind(entry.Kind) {
			continue
		}
		found = true
		var payload compactionSummaryEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.NativeArchiveSource) > 0 {
			t.Fatalf("省略来源后仍写出了 native_archive_source: %s", payload.NativeArchiveSource)
		}
	}
	if !found {
		t.Fatal("用户压缩未落盘摘要")
	}
	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err == nil || projection != nil {
		t.Fatal("省略 source 的压缩 native 写出居然成功")
	} else if !strings.Contains(err.Error(), "compaction not supported") {
		t.Fatalf("native 失败类别 = %v", err)
	}
	after, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("native 失败改动了会话")
	}
}

func TestEmptyModelCallIDMiddleToolResultDoesNotSplitHomologousPrefix(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation-empty-mc-batch", Mode: "agent", NextTurnSeq: 3}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "old question", "message-1"),
		newAssistantTextEntry(1, "request-1", "old answer", "", ""),
		tailReadToolCallEntry(t, 2, "request-2", "call-1", ""),
		tailReadToolCallEntry(t, 2, "request-2", "call-2", ""),
		tailReadToolResultEntry(t, 2, "request-2", "call-1", "", "/tmp/one.txt", "one contents"),
		tailReadToolResultEntry(t, 2, "request-2", "call-2", "", "/tmp/two.txt", "two contents"),
	})
	call2 := 3
	if aligned := extendContinuousTailToToolBatchStart(conversation.Entries, call2); aligned != 2 {
		t.Fatalf("空 ModelCallID 中间 tool 边界 helper = %d, want 2（向前保完整配对）", aligned)
	}
	if err := ensureCompactionPrefixClosed(conversation.Entries, 0, call2); err == nil {
		t.Fatal("ensureCompactionPrefixClosed 对空 ModelCallID 半组既不扩展也不停止")
	}
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil {
		if !strings.Contains(err.Error(), "group") && !strings.Contains(err.Error(), "prefix") {
			t.Fatalf("半组应明确停止，got %v", err)
		}
		return
	}
	if plan == nil || plan.NativeArchiveSource == nil {
		t.Fatal("生产 plan 为空且未明确停止")
	}
	prefix := conversation.Entries[plan.NativeArchiveSource.PrefixStart:plan.NativeArchiveSource.PrefixEnd]
	for _, entry := range prefix {
		switch strings.TrimSpace(entry.ToolCallID) {
		case "call-1", "call-2":
			t.Fatalf("同源前缀切了半组工具: kind=%s id=%s prefix_end=%d", entry.Kind, entry.ToolCallID, plan.NativeArchiveSource.PrefixEnd)
		}
	}
}

func TestApplyRejectsSameCountRewrittenFrozenPrefix(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil || plan == nil || !plan.HomologousPrefixFrozen {
		t.Fatalf("build plan error = %v %#v", err, plan)
	}
	pending := newPendingCompaction(plan)
	if len(pending.FrozenPrefixEntries) == 0 {
		t.Fatal("生产 plan 未深拷贝冻结前缀")
	}
	rewritten := compactionTestUserEntry(t, 1, "request-1", "rewritten first question", "message-1")
	rewritten.Seq = conversation.Entries[0].Seq
	rewritten.CreatedAt = conversation.Entries[0].CreatedAt
	conversation.Entries[0] = rewritten
	if err := applyCompactionToConversation(conversation, pending, "tampered summary"); err == nil {
		t.Fatal("同条数改写冻结前缀正文后 apply 居然通过")
	}
}

func TestApplyRejectsForkedReasoningPrefixWithoutNativeSource(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation-reasoning-fork", Mode: "agent", NextTurnSeq: 3}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "old question", "message-1"),
		newAssistantTextEntry(1, "request-1", "old answer", "hidden thought", ""),
		compactionTestUserEntry(t, 2, "request-2", "current question", "message-2"),
	})
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil || plan == nil || !plan.HomologousPrefixFrozen {
		t.Fatalf("域外前缀仍应冻结: plan=%v err=%v", plan, err)
	}
	if plan.NativeArchiveSource != nil {
		t.Fatal("reasoning 前缀不应写出 native_archive_source")
	}
	pending := newPendingCompaction(plan)
	if len(pending.FrozenPrefixEntries) == 0 {
		t.Fatal("域外前缀也必须深拷贝冻结身份/内容")
	}
	conversation.Entries = conversation.Entries[:1]
	if err := applyCompactionToConversation(conversation, pending, "forked reasoning summary"); err == nil {
		t.Fatal("NativeArchiveSource=nil 时 fork/截断冻结前缀 apply 居然通过")
	}
}

func TestApplyFrozenPrefixAllowsEscapedPayloadAndRequestHookMetadata(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil || plan == nil {
		t.Fatalf("build plan error = %v", err)
	}
	pending := newPendingCompaction(plan)
	pending.HookMessage = "pre-compact hook note"
	pending.SummaryModelCallID = "summary-call"
	cloned := clonePendingCompaction(pending)
	if cloned == nil || len(pending.FrozenPrefixEntries) == 0 || len(cloned.FrozenPrefixEntries) != len(pending.FrozenPrefixEntries) {
		t.Fatal("clone 丢失冻结前缀")
	}
	cloned.FrozenPrefixEntries[0].Payload = append(json.RawMessage(nil), []byte(`{"tampered":true}`)...)
	if bytes.Equal(pending.FrozenPrefixEntries[0].Payload, cloned.FrozenPrefixEntries[0].Payload) {
		t.Fatal("clone 未深拷贝冻结前缀正文")
	}
	encoded, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	reloaded := &ConversationFile{}
	if err := json.Unmarshal(encoded, reloaded); err != nil {
		t.Fatal(err)
	}
	appendEntriesInPlace(reloaded, []HistoryEntry{
		newCompactionRequestEntry(cloned),
		newMetadataEntry(2, "request-2", "hook", map[string]any{"note": "<pre-compact>"}),
	})
	if err := applyCompactionToConversation(reloaded, pending, "escaped summary"); err != nil {
		t.Fatalf("落盘转义或追加 request/hook 元数据不应导致冻结失配: %v", err)
	}
}
