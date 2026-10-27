package forwarder

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"cursor/gen/agentv1"
	legacyruntime "cursor/internal/runtime"
)

// 小型集成回归使用真实导入器、回合/溢出压缩规划、Responses 适配器和追加式提交。
// 仅外部模型由脚本化 HTTP 响应替代。
func TestImportedHistoryCompactionThroughHTTP(t *testing.T) {
	for _, tc := range []struct {
		name       string
		oldSummary bool
		fallback   bool
	}{
		{name: "leading-assistant"},
		{name: "existing-native-summary", oldSummary: true},
		{name: "short-source-fallback", oldSummary: true, fallback: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("HOME", t.TempDir())
			service, seed := testOverflowRecoveryStream(t)
			// 复用依赖，不复用夹具预填的会话或流。
			service.store = NewConversationFileStore(t.TempDir())
			service.broker = NewStreamBroker()
			service.compiler = NewPromptCompiler(service.projector, NewToolCatalog(), NewReminderInjector(), nil)
			first := `{"role":"assistant","content":"PRECEDING_ANSWER"}`
			firstMarker := "PRECEDING_ANSWER"
			if tc.oldSummary {
				first = `{"role":"user","content":"PRIOR_SUMMARY","providerOptions":{"cursor":{"isSummary":true}}}`
				firstMarker = "PRIOR_SUMMARY"
			}
			state, blobs := prefetchedNativeRootTestState(first,
				`{"role":"user","content":"FIRST_REQUIREMENT"}`,
				`{"role":"assistant","content":"FIRST_ANSWER"}`,
				`{"role":"user","content":"DO_NOT_DELETE_B"}`,
				`{"role":"assistant","content":"SECOND_ANSWER"}`,
				`{"role":"user","content":"FIRST_REQUIREMENT"}`,
			)
			conversation, err := newRuntimeConversation(seed.ConversationID, agentv1.AgentMode_AGENT_MODE_AGENT)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := service.importConversationState(conversation, state, blobs)
			if err != nil {
				t.Fatal(err)
			}
			// 在同一个 TurnSeq=0 内交错两种合法用户消息格式，验证统一保序。
			mixed := append([]HistoryEntry(nil), entries[:3]...)
			mixed = append(mixed, compactionTestUserEntry(t, 0, "imported-local", "MIXED_USER_REQUIREMENT", "mixed-user"))
			mixed = append(mixed, entries[3:]...)
			mixed = append(mixed,
				compactionTestUserEntry(t, 1, "local-prior", "LOCAL_REQUIREMENT", "local-user"),
				newAssistantTextEntry(1, "local-prior", "LOCAL_ANSWER", "", ""))
			conversation.NextTurnSeq = 2
			conversation.TokenDetailsMaxTokens = 128000
			persisted, err := service.store.SaveConversationWithEntries(conversation.ConversationID, conversation, resetEntrySequences(mixed))
			if err != nil {
				t.Fatal(err)
			}
			persisted, err = service.store.LoadConversation(conversation.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			original := cloneHistoryEntries(persisted.Entries)
			if err := service.replaceCheckpointConversation(seed, persisted); err != nil {
				t.Fatal(err)
			}
			wantSequence := []string{firstMarker, "FIRST_REQUIREMENT", "FIRST_ANSWER", "MIXED_USER_REQUIREMENT", "DO_NOT_DELETE_B", "SECOND_ANSWER", "FIRST_REQUIREMENT", "LOCAL_REQUIREMENT", "LOCAL_ANSWER"}
			const remoteSummary = "HTTP_SUMMARY: DO_NOT_DELETE_B; MIXED_USER_REQUIREMENT; LOCAL_REQUIREMENT"
			var mu sync.Mutex
			var summaryInputs, mainInputs []string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Error(err)
					w.WriteHeader(400)
					return
				}
				var text strings.Builder
				input, _ := body["input"].([]any)
				for _, item := range input {
					if m, ok := item.(map[string]any); ok {
						for _, part := range sameTurnHTTPInputTexts(m) {
							text.WriteString(part)
							text.WriteByte('\n')
						}
					}
				}
				isSummary := body["tools"] == nil
				mu.Lock()
				if isSummary {
					summaryInputs = append(summaryInputs, text.String())
				} else {
					mainInputs = append(mainInputs, text.String())
				}
				mainCount := len(mainInputs)
				mu.Unlock()
				if isSummary {
					assertImportedCompactionSequence(t, text.String(), wantSequence)
					if body["max_output_tokens"] != float64(compactionSummaryOutputMaxTokens) {
						t.Error("summary output budget changed")
					}
				}
				// 经真实 HTTP 返回模拟溢出，进入生产强制恢复压缩规划；
				// 不伪造 usage，也不注入压缩计划或摘要。
				if (!isSummary && mainCount == 1) || (isSummary && tc.fallback) {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					fmt.Fprint(w, `{"error":{"code":"context_length_exceeded","message":"synthetic context overflow"}}`)
					return
				}
				w.Header().Set("Content-Type", "text/event-stream")
				answer := "CONTINUED_OK"
				if isSummary {
					answer = remoteSummary
				}
				writeSameTurnHTTPEvent(w, map[string]any{"type": "response.output_text.delta", "delta": answer})
				writeSameTurnHTTPEvent(w, map[string]any{"type": "response.completed", "response": map[string]any{"id": "imported-response", "model": "gpt-6.1-sol", "status": "completed"}})
			}))
			defer server.Close()
			service.provider = NewProviderGateway(compactionHTTPChannelResolver{channel: &legacyruntime.ResolvedChannel{
				ID: "imported-test", Provider: "openai", Model: "gpt-6.1-sol", BaseURL: server.URL,
				APIKey: "test-only", OpenAIEndpoint: "/v1/responses", ReasoningEffort: "high",
			}})
			for turn := 0; turn < 2; turn++ {
				requestID := fmt.Sprintf("imported-http-%d", turn)
				if err := service.dispatchInboundIntent(InboundIntent{Kind: "run", RequestID: requestID, ConversationID: seed.ConversationID,
					ModelID: seed.ModelID, ModelName: "gpt-6.1-sol", Mode: agentv1.AgentMode_AGENT_MODE_AGENT,
					UserMessage: &agentv1.UserMessage{Text: "CURRENT_CONTINUE"}}); err != nil {
					t.Fatal(err)
				}
				stream, ok := service.broker.Get(requestID)
				if !ok {
					t.Fatal("missing run stream")
				}
				t.Cleanup(func() {
					// 直接确认 checkpoint 可能在 actor 最后一次检查后令流终态；
					// 再唤醒一次，确保 actor 退出。
					_ = service.postStreamCommandWait(stream, streamCommand{Kind: streamCommandCancel, Intent: InboundIntent{Kind: "cancel", RequestID: requestID, CancelReason: "test cleanup"}})
					waitForBidiAppendActor(t, stream, "actor cleanup")
				})
				deadline := time.Now().Add(10 * time.Second)
				for {
					tryCompleteOverflowPreCompactHook(t, service, stream)
					acknowledgeCheckpointBlobs(t, service, stream)
					stream.mu.Lock()
					status, pending := stream.Status, len(stream.PendingCheckpointBlobWrites)
					stream.mu.Unlock()
					if isTerminalStreamStatus(status) && pending == 0 {
						if status != StreamStatusCompleted {
							t.Fatalf("turn %d ended %s", turn, status)
						}
						break
					}
					if time.Now().After(deadline) {
						t.Fatalf("turn %d did not finish: %s", turn, status)
					}
					time.Sleep(5 * time.Millisecond)
				}
			}
			loaded, err := service.store.LoadConversation(seed.ConversationID)
			if err != nil {
				t.Fatal(err)
			}
			if len(loaded.Entries) < len(original) || !reflect.DeepEqual(loaded.Entries[:len(original)], original) {
				t.Fatal("original imported/native history mutated")
			}
			if countHistoryKind(loaded.Entries, "compacted_summary") != 1 {
				t.Fatal("expected exactly one committed summary")
			}
			if err := validateCompactionLayouts(loaded.Entries); err != nil {
				t.Fatal(err)
			}
			payload, err := decodeCompactionSummaryPayload(loaded.Entries[latestCompactionSummaryIndex(loaded.Entries)].Payload)
			if err != nil {
				t.Fatal(err)
			}
			if tc.fallback {
				assertImportedCompactionSequence(t, payload.Summary, wantSequence)
				if !strings.HasPrefix(payload.Summary, "Conversation summary") {
					t.Fatalf("not local fallback: %s", payload.Summary)
				}
			} else if payload.Summary != remoteSummary {
				t.Fatalf("committed summary is not HTTP response: %q", payload.Summary)
			}
			replay, err := service.projector.ProjectPromptReplay(loaded)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := service.projector.ProjectCheckpointProjection(loaded)
			if err != nil || projection == nil {
				t.Fatalf("checkpoint projection: %v", err)
			}
			projectedSummary := false
			for _, message := range replay {
				if strings.Contains(message.Content, payload.Summary) {
					projectedSummary = true
				}
			}
			if !projectedSummary {
				t.Fatal("committed summary missing from replay")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(summaryInputs) != 1 || len(mainInputs) != 3 {
				t.Fatalf("unexpected HTTP calls: summary=%d main=%d", len(summaryInputs), len(mainInputs))
			}
			for _, input := range mainInputs[1:] {
				if !strings.Contains(input, payload.Summary) || !strings.Contains(input, "CURRENT_CONTINUE") {
					t.Fatal("compiled continuation lost summary/current request")
				}
			}
		})
	}
}

func assertImportedCompactionSequence(t *testing.T, text string, sequence []string) {
	t.Helper()
	remaining := text
	for _, want := range sequence {
		index := strings.Index(remaining, want)
		if index < 0 {
			t.Errorf("missing or reordered %q in %s", want, text)
			return
		}
		remaining = remaining[index+len(want):]
	}
	counts := make(map[string]int)
	for _, want := range sequence {
		counts[want]++
	}
	for want, count := range counts {
		if got := strings.Count(text, want); got != count {
			t.Errorf("%q instances=%d, want %d", want, got, count)
		}
	}
}

func TestImportedHistoryCompactionPreservesUserSequence(t *testing.T) {
	state, blobs := prefetchedNativeRootTestState(
		`{"role":"assistant","content":"PRECEDING_ANSWER"}`,
		`{"role":"user","content":"FIRST_REQUIREMENT"}`,
		`{"role":"assistant","content":"FIRST_ANSWER"}`,
		`{"role":"user","content":"DO_NOT_DELETE_B"}`,
		`{"role":"assistant","content":"SECOND_ANSWER"}`,
		`{"role":"user","content":"FIRST_REQUIREMENT"}`,
	)
	conversation, err := newRuntimeConversation("imported-compaction-users", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	service := &Service{}
	entries, err := service.importConversationState(conversation, state, blobs)
	if err != nil {
		t.Fatal(err)
	}
	appendEntriesInPlace(conversation, entries)
	before, _ := json.Marshal(conversation)
	plan := &PendingCompaction{ContextWindowSize: 1_000_000, HomologousPrefixFrozen: true, CompactedTurns: compactedTurnsFromCanonicalPrefix(conversation.Entries, 0, len(conversation.Entries))}
	messages, fallback, err := service.prepareCompactionSummaryMessages(plan)
	if err != nil || fallback || len(messages) == 0 {
		t.Fatalf("摘要准备失败: fallback=%v err=%v", fallback, err)
	}
	outputs := map[string]string{"request": messages[len(messages)-1].Content, "fallback": buildFallbackCompactionSummary(plan), "local": buildCompactedConversationSummary("", plan.CompactedTurns)}
	for name, text := range outputs {
		t.Run(name, func(t *testing.T) {
			remaining := text
			for _, want := range []string{"PRECEDING_ANSWER", "FIRST_REQUIREMENT", "FIRST_ANSWER", "DO_NOT_DELETE_B", "SECOND_ANSWER", "FIRST_REQUIREMENT"} {
				index := strings.Index(remaining, want)
				if index < 0 {
					t.Fatalf("缺少或乱序 %s: %s", want, text)
				}
				remaining = remaining[index+len(want):]
			}
			if strings.Count(text, "FIRST_REQUIREMENT") != 2 || strings.Count(text, "DO_NOT_DELETE_B") != 1 {
				t.Fatalf("用户消息实例改变: %s", text)
			}
		})
	}
	after, _ := json.Marshal(conversation)
	if string(before) != string(after) {
		t.Fatal("生成摘要候选修改了原历史")
	}
}
