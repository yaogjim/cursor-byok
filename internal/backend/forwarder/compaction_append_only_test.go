package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/observability"
	legacyruntime "cursor/internal/runtime"
)

func TestCompactionSummaryRecoveryBoundaries(t *testing.T) {
	plan := &PendingCompaction{ExistingSummary: "保留已完成任务", CompactedTurns: []compactedTurnSummary{{UserText: "继续修复"}}}
	for _, tc := range []struct {
		name     string
		err      error
		fallback bool
	}{
		{"摘要参数不兼容", &modeladapter.HTTPStatusError{StatusCode: 400, Code: "unsupported_value", Param: "reasoning.effort"}, true},
		{"摘要输出额度不兼容", &modeladapter.HTTPStatusError{StatusCode: 400, Code: "unsupported_parameter", Param: "max_output_tokens"}, true},
		{"服务暂时不可用", &modeladapter.HTTPStatusError{StatusCode: 503}, true},
		{"摘要流截断", &modeladapter.StreamTruncatedError{}, true},
		{"鉴权失败", &modeladapter.HTTPStatusError{StatusCode: 401}, false},
		{"限流", &modeladapter.HTTPStatusError{StatusCode: 429}, false},
		{"未知400", &modeladapter.HTTPStatusError{StatusCode: 400}, false},
		{"模型不存在", &modeladapter.HTTPStatusError{StatusCode: 400, Body: `{"error":{"code":"unsupported_value","param":"model"}}`}, false},
		{"安全拒绝", &modeladapter.ProviderTerminalStatusError{Status: "failed", Code: "content_filter"}, false},
		{"本地存储失败", errors.New("write checkpoint failed"), false},
		{"取消", context.Canceled, false},
		{"父请求超时", context.DeadlineExceeded, false},
		{"旧调用", errProviderLoopInterrupted, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveCompactionSummaryResult(plan, "不完整摘要不可使用", providerTerminalError{cause: tc.err})
			if tc.fallback {
				if err != nil || !strings.Contains(got, "保留已完成任务") || strings.Contains(got, "不完整摘要不可使用") {
					t.Fatalf("安全回退失败: summary=%q err=%v", got, err)
				}
			} else if !errors.Is(err, tc.err) || got != "" {
				t.Fatalf("不得吞掉非恢复错误: summary=%q err=%v", got, err)
			}
		})
	}
}

func TestCompactionUnicodeBudget(t *testing.T) {
	for _, text := range []string{"中文会话压缩", "日本語の履歴", "한국어대화", "😀🚀🧪"} {
		if got := estimateTextTokens(text); got < int64(len([]rune(text))) {
			t.Errorf("非ASCII文本按四字符一token严重低估: %q = %d", text, got)
		}
	}
	if got := estimateTextTokens("abcdefgh"); got != 2 {
		t.Fatalf("ASCII估算不应改变: %d", got)
	}
	compiled := CompiledConversation{Messages: []modeladapter.Message{{Role: "user", Content: strings.Repeat("中", 12000)}}}
	if err := validateCompactionCandidateBudget(compiled, &PendingCompaction{ContextWindowSize: 20000}); err == nil {
		t.Fatal("压缩后保留的大段中文输入必须通过真实字节敏感的预算检查")
	}
}

// 使用正式新回合入口、默认编译器与真实 HTTP adapter；仅模拟远端上游和客户端 ACK。
// 覆盖摘要阶段故障、摘要成功后主请求故障，以及同会话下一回合无需改写内部状态即可恢复。
func TestCompactionRecoveryThroughRunIntentHTTP(t *testing.T) {
	for _, scenario := range []string{"summary_parameter", "summary_truncated", "summary_auth", "summary_unknown400", "post_summary_failure", "repeat_overflow"} {
		t.Run(scenario, func(t *testing.T) {
			service, seed := testOverflowRecoveryStream(t)
			appendCompletedToolHistory(t, service, seed)
			service.compiler = NewPromptCompiler(service.projector, NewToolCatalog(), NewReminderInjector(), nil)
			conversationID := seed.ConversationID
			_, err := service.updateConversationMetaAndCheckpoint(seed, conversationID, func(c *ConversationFile) error {
				c.TokenDetailsMaxTokens = 128000
				c.TokenDetailsUsedTokens = 127000
				c.AutoCompactionPending = true
				c.AutoCompactionPromptTokens = 127000
				if scenario == "repeat_overflow" {
					c.TokenDetailsUsedTokens = 1000
					clearConversationAutoCompactionState(c)
				}
				return nil
			})
			if err != nil {
				t.Fatal(err)
			}
			var mu sync.Mutex
			summaryCalls, mainCalls := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				reasoning, _ := body["reasoning"].(map[string]any)
				isSummary := reasoning["effort"] == "low"
				if !isSummary {
					input, err := json.Marshal(body["input"])
					if err != nil || !bytes.Contains(input, []byte("继续")) {
						t.Error("续接主请求丢失了当前用户输入")
					}
				}
				mu.Lock()
				if isSummary {
					summaryCalls++
				} else {
					mainCalls++
				}
				s, m := summaryCalls, mainCalls
				mu.Unlock()
				if isSummary && (body["tools"] != nil || body["max_output_tokens"] != float64(compactionSummaryOutputMaxTokens)) {
					t.Errorf("摘要请求合同错误: tools=%v max_output_tokens=%v", body["tools"], body["max_output_tokens"])
				}
				if isSummary && s == 1 && scenario == "summary_parameter" {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":{"code":"unsupported_value","param":"reasoning.effort","message":"private-provider-body"}}`)
					return
				}
				if isSummary && s == 1 && scenario == "summary_auth" {
					w.WriteHeader(401)
					return
				}
				if isSummary && s == 1 && scenario == "summary_unknown400" {
					w.WriteHeader(400)
					return
				}
				if !isSummary && m <= 2 && scenario == "repeat_overflow" {
					w.WriteHeader(400)
					fmt.Fprint(w, `{"error":{"code":"context_length_exceeded","message":"context window exceeded"}}`)
					return
				}
				if !isSummary && m == 1 && scenario == "post_summary_failure" {
					w.WriteHeader(400)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				if isSummary && s == 1 && scenario == "summary_truncated" {
					fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"incomplete-secret-summary\"}\n\n")
					return
				}
				text := "主回合已完成"
				if isSummary {
					text = "保留已完成的工具和任务"
				}
				fmt.Fprintf(w, "data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", text)
				fmt.Fprint(w, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-review\",\"model\":\"gpt-6.1-sol\",\"status\":\"completed\",\"usage\":{\"input_tokens\":1000,\"output_tokens\":20}}}\n\n")
			}))
			defer server.Close()
			service.provider = NewProviderGateway(compactionHTTPChannelResolver{channel: &legacyruntime.ResolvedChannel{
				ID: "review-channel", Provider: "openai", Model: "gpt-6.1-sol", BaseURL: server.URL,
				APIKey: "test-only", OpenAIEndpoint: "/v1/responses", ReasoningEffort: "high",
			}})
			for turn := 0; turn < 2; turn++ {
				intent := InboundIntent{RequestID: fmt.Sprintf("review-%s-%d", scenario, turn), ConversationID: conversationID,
					ModelID: seed.ModelID, ModelName: "gpt-6.1-sol", Mode: agentv1.AgentMode_AGENT_MODE_AGENT,
					UserMessage: &agentv1.UserMessage{Text: "继续"}}
				if err := service.handleRunIntent(intent); err != nil {
					t.Fatal(err)
				}
				stream, ok := service.broker.Get(intent.RequestID)
				if !ok {
					t.Fatal("缺少新回合")
				}
				deadline := time.Now().Add(10 * time.Second)
				for {
					tryCompleteOverflowPreCompactHook(t, service, stream)
					acknowledgeCheckpointBlobs(t, service, stream)
					stream.mu.Lock()
					status, pending := stream.Status, len(stream.PendingCheckpointBlobWrites)
					stream.mu.Unlock()
					if isTerminalStreamStatus(status) && pending == 0 {
						want := StreamStatusCompleted
						if turn == 0 && (scenario == "summary_auth" || scenario == "summary_unknown400" || scenario == "post_summary_failure" || scenario == "repeat_overflow") {
							want = StreamStatusFailed
						}
						if status != want {
							t.Fatalf("回合%d状态=%s，期望%s", turn, status, want)
						}
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("新回合未收口: status=%s pending=%d", status, pending)
					}
					time.Sleep(10 * time.Millisecond)
				}
				stream.mu.Lock()
				entries := cloneHistoryEntries(stream.CheckpointConversation.Entries)
				stream.mu.Unlock()
				if countHistoryKind(entries, "tool_call") != 1 || countHistoryKind(entries, "tool_result") != 1 {
					t.Fatal("已执行工具丢失或被重复写入")
				}
				if turn == 1 && countHistoryKind(entries, "compacted_summary") != 1 {
					t.Fatal("摘要应只提交一次，后续继续不能重复压缩旧历史")
				}
				for _, event := range readCheckpointTestEvents(t, service, stream) {
					if summary := event.Message.GetInteractionUpdate().GetSummary(); summary != nil && strings.Contains(summary.GetSummary(), "incomplete-secret-summary") {
						t.Fatal("截断摘要被发布")
					}
				}
			}
			mu.Lock()
			defer mu.Unlock()
			wantSummaries, wantMain := 1, 2
			if scenario == "summary_auth" || scenario == "summary_unknown400" {
				wantSummaries, wantMain = 2, 1
			}
			if scenario == "repeat_overflow" {
				wantMain = 3
			}
			if summaryCalls != wantSummaries || mainCalls != wantMain {
				t.Fatalf("调用次数失控: summary=%d main=%d", summaryCalls, mainCalls)
			}
		})
	}
}

func TestCompactionInFlightCancelRejectsLateFallback(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	started, finished := make(chan struct{}), make(chan struct{})
	provider := &overflowRecoveryTestProvider{handler: func(ctx context.Context, _ ProviderRequest, _ func(modeladapter.ModelEvent) error) error {
		close(started)
		<-ctx.Done()
		defer close(finished)
		// 模拟取消后才到达的可回退错误，不能复活会话或提交摘要。
		return &modeladapter.HTTPStatusError{StatusCode: 503}
	}}
	service.provider = provider
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: stream.CurrentProviderToken, Done: true, Err: testContextOverflowErr()}); err != nil {
		t.Fatal(err)
	}
	completeOverflowPreCompactHook(t, service, stream)
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("摘要未开始")
	}
	if err := service.handleCancelIntent(InboundIntent{RequestID: stream.RequestID, CancelReason: "user aborted"}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("取消未传到摘要请求")
	}
	_, done, err := service.ensureStreamActor(stream)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("取消后actor未退出")
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.Status != StreamStatusCanceled || countHistoryKind(stream.CheckpointConversation.Entries, "compacted_summary") != 0 || provider.requestCount() != 1 {
		t.Fatalf("迟到摘要恢复了已取消回合: status=%s", stream.Status)
	}
}

func TestCompactionLocalBudgetFallbackResumes(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	appendCompletedToolHistory(t, service, stream)
	provider := &overflowRecoveryTestProvider{handler: func(_ context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		if strings.Contains(req.CompileSummary, "compaction") {
			t.Error("超预算的摘要不应访问上游")
		}
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "恢复完成"}); err != nil {
			return err
		}
		return sink(overflowUsageFinished(1000, 10))
	}}
	service.provider = provider
	failedCall := stream.CurrentModelCallID
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: stream.CurrentProviderToken, Done: true, Err: testContextOverflowErr()}); err != nil {
		t.Fatal(err)
	}
	stream.mu.Lock()
	for _, pending := range stream.PendingExecs {
		if pending.ExecKind == "execute_hook_pre_compact" {
			stream.mu.Unlock()
			markExecCompleted(stream, pending)
			if err := service.handlePreCompactTerminal(stream, pending.ProviderPass, strings.Repeat("保留完整归档", 4000)); err != nil {
				t.Fatal(err)
			}
			stream.mu.Lock()
			break
		}
	}
	stream.mu.Unlock()
	waitForOverflowRecoveryResume(t, service, stream, failedCall, provider)
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.Status != StreamStatusCompleted || countHistoryKind(stream.CheckpointConversation.Entries, "compacted_summary") != 1 || provider.requestCount() != 1 {
		t.Fatalf("本地回退后未完成原回合: status=%s", stream.Status)
	}
}

func TestCompactionSummaryFallbackRecordsSafeWarning(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	capture := &debugRecorderTestCapture{}
	service.debug = newDebugRecorder(t.TempDir(), service.broker, debugRecorderTestConfig("basic"), capture)
	defer service.debug.Close()
	service.recordCompactionSummaryFallback(context.Background(), stream, "summary-call", "summary_input_budget")
	if len(capture.captures) != 1 {
		t.Fatalf("回退诊断条数=%d", len(capture.captures))
	}
	event := capture.captures[0].Event
	if event.Event != "compaction_summary_fallback" || event.SemanticOutcome != observability.OutcomeDegraded || event.Status != "degraded" || event.Fields["reason"] != "summary_input_budget" {
		t.Fatalf("回退必须留下基础模式警告: %+v", event)
	}
}

func TestCompactionSummaryRecordsSafeLifecycle(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			service, stream := testOverflowRecoveryStream(t)
			capture := &debugRecorderTestCapture{}
			service.debug = newDebugRecorder(t.TempDir(), service.broker, debugRecorderTestConfig("basic"), capture)
			t.Cleanup(service.debug.Close)
			service.provider = &overflowRecoveryTestProvider{handler: func(_ context.Context, _ ProviderRequest, onEvent func(modeladapter.ModelEvent) error) error {
				if fail {
					return &modeladapter.HTTPStatusError{StatusCode: 400, Body: "sensitive-summary-body"}
				}
				return onEvent(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "sensitive-summary-body"})
			}}
			_, err := service.generateCompactionSummaryAttempt(context.Background(), stream, &PendingCompaction{Trigger: "auto"}, stream.CurrentModelCallID,
				[]modeladapter.Message{{Role: "user", Content: "sensitive-input-body"}}, compactionSummaryOutputMaxTokens)
			if (err != nil) != fail {
				t.Fatalf("unexpected error: %v", err)
			}
			var events []observability.Event
			for _, c := range capture.captures {
				if strings.HasPrefix(c.Event.Event, "compaction_summary_") {
					events = append(events, c.Event)
				}
			}
			if len(events) != 2 {
				t.Fatalf("摘要开始和结果必须有独立结构化记录，got %d", len(events))
			}
			if events[0].Event != "compaction_summary_started" {
				t.Fatal(events[0].Event)
			}
			last := events[1]
			correlation := capture.correlations[len(capture.correlations)-1]
			if correlation.ModelCallID != stream.CurrentModelCallID || correlation.ConversationID != stream.ConversationID {
				t.Fatal("缺少摘要调用关联")
			}
			if fail && (last.Event != "compaction_summary_failed" || last.ErrorCategory == "" || fmt.Sprint(last.Fields["http_status"]) != "400") {
				t.Fatalf("缺少摘要错误分类: %+v", last)
			}
			if !fail && last.Event != "compaction_summary_completed" {
				t.Fatal(last.Event)
			}
			encoded, _ := json.Marshal(capture.captures)
			if bytes.Contains(encoded, []byte("sensitive-")) {
				t.Fatal("摘要观测泄露正文")
			}
		})
	}
}

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

func TestCompactionSummaryOutputLimitRecoversWithoutFailingTurn(t *testing.T) {
	for _, exhausted := range []bool{false, true} {
		name := "retry_success"
		if exhausted {
			name = "retry_exhausted_local_fallback"
		}
		t.Run(name, func(t *testing.T) {
			service, stream := testOverflowRecoveryStream(t)
			plan := &PendingCompaction{
				Trigger: "auto", ContextWindowSize: 130_000,
				ExistingSummary: "必须保留的约束",
				CompactedTurns:  []compactedTurnSummary{{UserText: "继续原子任务"}},
			}
			stream.PendingCompaction = clonePendingCompaction(plan)
			provider := &overflowRecoveryTestProvider{}
			attempt := 0
			provider.handler = func(_ context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
				attempt++
				if attempt == 1 || exhausted {
					if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "不完整的摘要"}); err != nil {
						return err
					}
					return providerTerminalError{cause: &modeladapter.ProviderTerminalStatusError{
						Provider: "openai responses", Status: "incomplete", Message: "max_output_tokens",
					}}
				}
				return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "完整的摘要"})
			}
			service.provider = provider
			got, err := service.generateCompactionSummary(context.Background(), stream, plan, stream.CurrentModelCallID)
			got, err = resolveCompactionSummaryResult(plan, got, err)
			if err != nil {
				t.Fatalf("摘要输出不足不应结束子会话: %v", err)
			}
			if provider.requestCount() != 2 {
				t.Fatalf("摘要调用次数=%d，期望有界的两次调用", provider.requestCount())
			}
			requests := provider.requests
			if requests[1].MaxTokens <= requests[0].MaxTokens {
				t.Fatalf("重试未提高输出预算: %d -> %d", requests[0].MaxTokens, requests[1].MaxTokens)
			}
			if requests[0].ModelCallID == requests[1].ModelCallID {
				t.Fatal("摘要重试必须使用独立的模型调用标识")
			}
			for _, req := range requests {
				if req.ThinkingEffort != "disabled" || len(req.Tools) != 0 {
					t.Fatal("摘要应关闭思考且不派发工具")
				}
				if !req.EnforceOutputLimit {
					t.Fatal("摘要请求必须强制发送输出预算")
				}
				if estimateModelMessagesTokens(req.Messages)+int64(req.MaxTokens) > compactionBudgetTokens(plan.ContextWindowSize) {
					t.Fatal("摘要重试超出上下文预算")
				}
			}
			if !exhausted && got != "完整的摘要" {
				t.Fatalf("成功重试应仅使用完整摘要: %q", got)
			}
			if stream.CurrentModelCallID != requests[1].ModelCallID || stream.PendingCompaction.SummaryModelCallID != requests[1].ModelCallID {
				t.Fatal("摘要重试调用标识未同步")
			}
			for _, event := range readCheckpointTestEvents(t, service, stream) {
				if event.Message.GetInteractionUpdate().GetSummary() != nil {
					t.Fatal("生成阶段不得发布未经确认的摘要")
				}
			}
			if exhausted && (!strings.Contains(got, "必须保留的约束") || strings.Contains(got, "不完整的摘要")) {
				t.Fatalf("备用摘要丢失约束或包含被截断的内容: %q", got)
			}
		})
	}
}

func TestCompactionSummaryOutputLimitResumesOriginalTurn(t *testing.T) {
	service, stream := testOverflowRecoveryStream(t)
	appendCompletedToolHistory(t, service, stream)
	provider := &overflowRecoveryTestProvider{}
	attempts := 0
	provider.handler = func(_ context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		if strings.Contains(req.CompileSummary, "compaction") {
			attempts++
			if attempts == 1 {
				return &modeladapter.ProviderTerminalStatusError{Status: "incomplete", Message: "max_output_tokens"}
			}
			return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "已保留任务进展"})
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, FinishReason: "stop"})
	}
	service.provider = provider
	failedCall := stream.CurrentModelCallID
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: stream.CurrentProviderToken, Done: true, Err: testContextOverflowErr()}); err != nil {
		t.Fatal(err)
	}
	completeOverflowPreCompactHook(t, service, stream)
	waitForOverflowRecoveryResume(t, service, stream, failedCall, provider)
	summaryCount := 0
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if summary := event.Message.GetInteractionUpdate().GetSummary(); summary != nil {
			summaryCount++
			if summary.GetSummary() != "已保留任务进展" {
				t.Fatalf("发布了错误的摘要: %q", summary.GetSummary())
			}
		}
	}
	if summaryCount != 1 {
		t.Fatalf("完整摘要应恰好发布一次: %d", summaryCount)
	}
	stream.mu.Lock()
	defer stream.mu.Unlock()
	if stream.Status != StreamStatusCompleted {
		t.Fatalf("压缩恢复后原回合状态=%s", stream.Status)
	}
	if countHistoryKind(stream.CheckpointConversation.Entries, "tool_call") != 1 || countHistoryKind(stream.CheckpointConversation.Entries, "tool_result") != 1 {
		t.Fatal("摘要恢复重复执行或丢失了已完成工具")
	}
}

func TestCompactionSummaryOutputLimitRespectsRetryBudget(t *testing.T) {
	for _, available := range []int64{compactionSummaryOutputMaxTokens, 6000} {
		t.Run(map[int64]string{compactionSummaryOutputMaxTokens: "no_retry_budget", 6000: "clipped_retry_budget"}[available], func(t *testing.T) {
			service, stream := testOverflowRecoveryStream(t)
			plan := &PendingCompaction{ContextWindowSize: 130_000, ExistingSummary: "保留约束"}
			messages, fallback, err := service.prepareCompactionSummaryMessages(plan)
			if err != nil || fallback {
				t.Fatalf("准备摘要输入失败: fallback=%v err=%v", fallback, err)
			}
			plan.ContextWindowSize = compactionAutoReserveTokens + estimateModelMessagesTokens(messages) + available
			provider := &overflowRecoveryTestProvider{}
			provider.handler = func(_ context.Context, _ ProviderRequest, _ func(modeladapter.ModelEvent) error) error {
				return &modeladapter.ProviderTerminalStatusError{Status: "incomplete", Code: "max_output_tokens"}
			}
			service.provider = provider
			got, err := service.generateCompactionSummary(context.Background(), stream, plan, stream.CurrentModelCallID)
			got, err = resolveCompactionSummaryResult(plan, got, err)
			wantCount := 2
			if available == compactionSummaryOutputMaxTokens {
				wantCount = 1
			}
			if err != nil || provider.requestCount() != wantCount || !strings.Contains(got, "保留约束") {
				t.Fatalf("预算受限的恢复不正确: got=%q err=%v calls=%d", got, err, provider.requestCount())
			}
			if wantCount == 2 && int64(provider.requests[1].MaxTokens) != available {
				t.Fatalf("重试预算应裁剪为%d: %d", available, provider.requests[1].MaxTokens)
			}
		})
	}
}

func TestCompactionSummaryOutputLimitFinishReasons(t *testing.T) {
	for _, reason := range []string{"length", "max_tokens", "max_output_tokens"} {
		t.Run(reason, func(t *testing.T) {
			service, stream := testOverflowRecoveryStream(t)
			plan := &PendingCompaction{ContextWindowSize: 130_000, ExistingSummary: "保留约束"}
			provider := &overflowRecoveryTestProvider{}
			provider.handler = func(_ context.Context, _ ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
				if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTextDelta, Text: "截断内容"}); err != nil {
					return err
				}
				return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, FinishReason: reason})
			}
			service.provider = provider
			got, err := service.generateCompactionSummary(context.Background(), stream, plan, stream.CurrentModelCallID)
			got, err = resolveCompactionSummaryResult(plan, got, err)
			if err != nil || provider.requestCount() != 2 || !strings.Contains(got, "保留约束") || strings.Contains(got, "截断内容") {
				t.Fatalf("截断恢复结果=%q err=%v 调用次数=%d", got, err, provider.requestCount())
			}
		})
	}
}

func TestCompactionSummaryOutputLimitHonorsInterruption(t *testing.T) {
	for _, mode := range []string{"cancel_context", "cancel_stream", "replace_call", "provider_error"} {
		t.Run(mode, func(t *testing.T) {
			service, stream := testOverflowRecoveryStream(t)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			plan := &PendingCompaction{ContextWindowSize: 130_000}
			provider := &overflowRecoveryTestProvider{}
			provider.handler = func(_ context.Context, _ ProviderRequest, _ func(modeladapter.ModelEvent) error) error {
				switch mode {
				case "cancel_context":
					cancel()
				case "cancel_stream":
					stream.mu.Lock()
					stream.Status = StreamStatusCanceled
					stream.mu.Unlock()
				case "replace_call":
					stream.mu.Lock()
					stream.CurrentModelCallID = "replacement"
					stream.mu.Unlock()
				case "provider_error":
					return errors.New("unrelated provider failure")
				}
				return &modeladapter.ProviderTerminalStatusError{Status: "incomplete", Code: "max_output_tokens"}
			}
			service.provider = provider
			got, err := service.generateCompactionSummary(ctx, stream, plan, stream.CurrentModelCallID)
			_, err = resolveCompactionSummaryResult(plan, got, err)
			if err == nil || provider.requestCount() != 1 {
				t.Fatalf("取消、替换和无关错误必须保留: err=%v 调用次数=%d", err, provider.requestCount())
			}
		})
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

func TestPrepareCompactionSummaryFrozenPrefixFallsBackWithoutTrim(t *testing.T) {
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
	if err != nil || !useFallback || len(messages) != 0 {
		t.Fatalf("冻结前缀超预算应改用本地摘要，不能裁掉源回合或发出超限请求: messages=%#v fallback=%v err=%v", messages, useFallback, err)
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

type compactionHTTPChannelResolver struct {
	channel *legacyruntime.ResolvedChannel
}

func (resolver compactionHTTPChannelResolver) SelectChannelForModel(context.Context, string) (*legacyruntime.ResolvedChannel, error) {
	return resolver.channel, nil
}

// TestCompactionSolSummaryResumesMainTurn 使用真实请求构建和 actor 续接链路，
// 将已捕获的 gpt-6.1-sol 参数拒绝条件固定为回归，避免只用假 provider 漏掉 400。
func TestCompactionSolSummaryResumesMainTurn(t *testing.T) {
	var mu sync.Mutex
	var efforts []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		reasoning, _ := body["reasoning"].(map[string]any)
		effort, _ := reasoning["effort"].(string)
		mu.Lock()
		efforts = append(efforts, effort)
		isSummary := len(efforts) == 1
		mu.Unlock()
		if effort == "none" {
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusBadRequest)
			_, _ = fmt.Fprint(writer, `{"error":{"code":"unsupported_value","message":"Unsupported value: 'none' is not supported with the 'gpt-6.1-sol' model. Supported values are: 'low', 'medium', 'high', 'xhigh', and 'max'.","param":"reasoning.effort","type":"invalid_request_error"}}`)
			return
		}
		if isSummary && body["max_output_tokens"] != float64(compactionSummaryOutputMaxTokens) {
			t.Errorf("摘要输出额度缺失或发生变化: %v", body["max_output_tokens"])
		}
		if isSummary && body["tools"] != nil {
			t.Error("摘要请求不应派发工具")
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"已保留任务进展\"}\n\n")
		_, _ = fmt.Fprint(writer, "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp-sol\",\"model\":\"gpt-6.1-sol\",\"status\":\"completed\"}}\n\n")
	}))
	defer server.Close()

	service, stream := testOverflowRecoveryStream(t)
	appendCompletedToolHistory(t, service, stream)
	gateway := NewProviderGateway(compactionHTTPChannelResolver{channel: &legacyruntime.ResolvedChannel{
		ID: "sol-channel", Provider: "openai", Model: "gpt-6.1-sol", BaseURL: server.URL,
		APIKey: "test-key", OpenAIEndpoint: "/v1/responses", ReasoningEffort: "high",
	}})
	provider := &overflowRecoveryTestProvider{}
	provider.handler = gateway.StartStream
	service.provider = provider
	failedCall := stream.CurrentModelCallID
	if err := service.handleProviderDoneEvent(stream, &streamProviderEvent{Token: stream.CurrentProviderToken, Done: true, Err: testContextOverflowErr()}); err != nil {
		t.Fatal(err)
	}
	completeOverflowPreCompactHook(t, service, stream)
	waitForOverflowRecoveryResume(t, service, stream, failedCall, provider)

	stream.mu.Lock()
	status := stream.Status
	entries := cloneHistoryEntries(stream.CheckpointConversation.Entries)
	stream.mu.Unlock()
	if status != StreamStatusCompleted {
		t.Fatalf("主对话摘要后未正常完成: %s", status)
	}
	if countHistoryKind(entries, "tool_call") != 1 || countHistoryKind(entries, "tool_result") != 1 {
		t.Fatal("压缩重放或丢失了已完成工具")
	}
	summaries := 0
	for _, event := range readCheckpointTestEvents(t, service, stream) {
		if summary := event.Message.GetInteractionUpdate().GetSummary(); summary != nil {
			summaries++
			if summary.GetSummary() != "已保留任务进展" {
				t.Fatalf("摘要内容错误: %q", summary.GetSummary())
			}
		}
	}
	if summaries != 1 {
		t.Fatalf("摘要应只发布一次: %d", summaries)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(efforts, []string{"low", "high"}) {
		t.Fatalf("摘要应使用最低合法等级，续接应恢复渠道等级: %v", efforts)
	}
}

// TestCompactionSummaryRealAdapterSendsBoundedOutputBudgets 通过真实 OpenAI 适配器
// 校验摘要两次 HTTP 请求确实携带 4096/8192 的 max_completion_tokens。
func TestCompactionSummaryRealAdapterSendsBoundedOutputBudgets(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []map[string]any
	)
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			http.Error(writer, err.Error(), http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies = append(bodies, body)
		attempt := len(bodies)
		mu.Unlock()

		writer.Header().Set("Content-Type", "text/event-stream")
		if attempt == 1 {
			_, _ = fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"},\"finish_reason\":\"length\"}]}\n\n")
		} else {
			_, _ = fmt.Fprint(writer, "data: {\"choices\":[{\"delta\":{\"content\":\"done\"},\"finish_reason\":\"stop\"}]}\n\n")
		}
		_, _ = fmt.Fprint(writer, "data: [DONE]\n\n")
	}))
	defer server.Close()

	service, stream := testOverflowRecoveryStream(t)
	service.provider = NewProviderGateway(compactionHTTPChannelResolver{channel: &legacyruntime.ResolvedChannel{
		ID:        "http-openai",
		Provider:  "openai",
		Model:     "gpt-test",
		BaseURL:   server.URL,
		APIKey:    "test-key",
		MaxTokens: 65_536,
	}})
	plan := &PendingCompaction{Trigger: "auto", ContextWindowSize: 130_000, ExistingSummary: "保留约束"}
	got, err := service.generateCompactionSummary(context.Background(), stream, plan, stream.CurrentModelCallID)
	if err != nil {
		t.Fatalf("generateCompactionSummary() error = %v", err)
	}
	if got != "done" {
		t.Fatalf("summary = %q, want done", got)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("HTTP 请求次数=%d，期望有界两次", len(bodies))
	}
	for index, want := range []int{compactionSummaryOutputMaxTokens, compactionSummaryRetryMaxTokens} {
		value, ok := bodies[index]["max_completion_tokens"]
		if !ok {
			t.Fatalf("第%d次请求缺少 max_completion_tokens: %#v", index+1, bodies[index])
		}
		if gotValue, ok := value.(float64); !ok || int(gotValue) != want {
			t.Fatalf("第%d次请求 max_completion_tokens = %#v, want %d", index+1, value, want)
		}
		if _, exists := bodies[index]["max_tokens"]; exists {
			t.Fatalf("第%d次请求不得同时发送 max_tokens: %#v", index+1, bodies[index])
		}
	}
}
