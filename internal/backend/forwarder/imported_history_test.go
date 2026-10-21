package forwarder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	modeladapter "cursor/internal/backend/agent/model"
)

func TestImportedConversationStateReadsPrefetchedNativeRootWithoutTurns(t *testing.T) {
	// Cursor 3.15.19 GH/LH：单条 CoreMessage UTF-8 JSON，以 SHA-256 原始字节引用。
	rawMessages := []string{
		`{"role":"system","content":"old system prompt"}`,
		`{"role":"user","content":"official question"}`,
		`{"role":"assistant","content":"official answer"}`,
		`{"role":"user","content":"official question"}`,
	}
	state := &agentv1.ConversationStateStructure{}
	prefetched := make([]*agentv1.PreFetchedBlob, 0, len(rawMessages))
	for _, raw := range rawMessages {
		digest := sha256.Sum256([]byte(raw))
		state.RootPromptMessagesJson = append(state.RootPromptMessagesJson, digest[:])
		prefetched = append(prefetched, &agentv1.PreFetchedBlob{Id: digest[:], Value: []byte(raw)})
	}
	conversation, err := newRuntimeConversation("native-root-conversation", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{}).importConversationState(conversation, state, prefetched)
	if err != nil {
		t.Fatalf("已齐全的原生 root 导入失败: %v", err)
	}
	appendEntriesInPlace(conversation, entries)
	messages, err := NewHistoryProjector().ProjectPromptReplay(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 3 || messages[0].Role != "user" || messages[0].Content != "official question" ||
		messages[1].Role != "assistant" || messages[1].Content != "official answer" ||
		messages[2].Role != "user" || messages[2].Content != "official question" {
		t.Fatalf("原生历史顺序、重复实例或当前系统提示边界改变: %#v", messages)
	}
}

func TestImportedConversationStateRestoresNativeTextBlocksAndToolResults(t *testing.T) {
	state, prefetched := prefetchedNativeRootTestState(
		`{"role":"user","content":[{"type":"text","text":"第一段 "},{"type":"text","text":"第二段"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"准备读取"},{"type":"tool-call","toolCallId":"read-1","toolName":"Read","args":{"path":"/synthetic/a","offset":9007199254740993}},{"type":"tool-call","toolCallId":"read-2","toolName":"Read","args":{"path":"/synthetic/b"}}]}`,
		`{"role":"tool","content":[{"type":"tool-result","toolCallId":"read-1","toolName":"Read","result":{"lines":["a","b"],"value":9007199254740993}},{"type":"tool-result","toolCallId":"read-2","toolName":"Read","result":"原始字符串结果","isError":false}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"已经读取"}]}`,
	)
	conversation, err := newRuntimeConversation("native-content-blocks", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{}).importConversationState(conversation, state, prefetched)
	if err != nil {
		t.Fatalf("原生文本块和工具链恢复失败: %v", err)
	}
	appendEntriesInPlace(conversation, entries)
	messages, err := NewHistoryProjector().ProjectPromptReplay(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != 5 {
		t.Fatalf("恢复消息数=%d，预期 user/assistant/两个 tool/assistant", len(messages))
	}
	if messages[0].Role != "user" || messages[0].Content != "第一段 第二段" || messages[1].Content != "准备读取" || len(messages[1].ToolCalls) != 2 {
		t.Fatalf("原生文本或工具批次丢失: %#v", messages)
	}
	for index, wantID := range []string{"read-1", "read-2"} {
		call := messages[1].ToolCalls[index]
		result := messages[index+2]
		if call.ID != wantID || call.Function.Name != "Read" || result.Role != "tool" || result.ToolCallID != wantID || result.Name != "Read" {
			t.Fatal("工具调用和结果的关联、顺序或名称改变")
		}
	}
	if messages[1].ToolCalls[0].Function.Arguments != `{"path":"/synthetic/a","offset":9007199254740993}` ||
		messages[2].Content != `{"lines":["a","b"],"value":9007199254740993}` || messages[3].Content != "原始字符串结果" || messages[4].Content != "已经读取" {
		t.Fatal("工具参数精度、结构结果或字符串结果改变")
	}
}

func TestImportedConversationStateNativeToolFailuresPreserveConversation(t *testing.T) {
	call := `{"role":"assistant","content":[{"type":"tool-call","toolCallId":"read-1","toolName":"Read","args":{}}]}`
	result := `{"role":"tool","content":[{"type":"tool-result","toolCallId":"read-1","toolName":"Read","result":"ok"}]}`
	for _, test := range []struct {
		name      string
		raw       []string
		wantError string
	}{
		{"missing-result", []string{call}, "native tool calls missing results"},
		{"orphan-result", []string{result}, "native tool result has no matching call"},
		{"wrong-result-id", []string{call, strings.ReplaceAll(result, "read-1", "read-2")}, "native tool result has no matching call"},
		{"wrong-result-name", []string{call, strings.ReplaceAll(result, "Read", "Shell")}, "native tool result has no matching call"},
		{"duplicate-result", []string{call, result, result}, "native tool result has no matching call"},
		{"duplicate-call", []string{`{"role":"assistant","content":[{"type":"tool-call","toolCallId":"read-1","toolName":"Read","args":{}},{"type":"tool-call","toolCallId":"read-1","toolName":"Read","args":{}}]}`, result}, "duplicate native tool call id in batch"},
		{"interrupted-batch", []string{call, `{"role":"user","content":"interrupted"}`, result}, "native tool batch interrupted before results"},
		{"text-after-call", []string{`{"role":"assistant","content":[{"type":"tool-call","toolCallId":"read-1","toolName":"Read","args":{}},{"type":"text","text":"after call"}]}`, result}, "unsupported native text after tool call"},
		{"error-flag", []string{call, strings.ReplaceAll(result, `"result":"ok"`, `"result":"ok","isError":true`)}, "unsupported native tool-result error flag"},
		{"multimodal-result", []string{call, strings.ReplaceAll(result, `"result":"ok"`, `"result":"ok","experimental_content":[{"type":"image","data":"00ff"}]`)}, "unsupported native tool-result content"},
		{"unknown-text-field", []string{`{"role":"user","content":[{"type":"text","text":"part","futureContent":"must preserve"}]}`}, "invalid or unsupported native content parts"},
		{"unknown-message-field", []string{`{"role":"assistant","content":"part","providerOptions":{"opaque":"must preserve"}}`}, "invalid or unsupported native root message"},
		{"misplaced-text-payload", []string{`{"role":"user","content":[{"type":"text","text":"part","result":{"must":"preserve"}}]}`}, "invalid native text part"},
		{"misplaced-call-payload", []string{strings.ReplaceAll(call, `"args":{}`, `"args":{},"text":"must preserve"`), result}, "invalid native tool-call part"},
		{"unknown-block", []string{`{"role":"assistant","content":[{"type":"text","text":"before"},{"type":"reasoning","text":"reasoning"}]}`}, "unsupported native content part"},
		{"missing-args", []string{strings.ReplaceAll(call, `,"args":{}`, ""), result}, "invalid native tool-call part"},
		{"wrong-call-role", []string{strings.ReplaceAll(call, `"role":"assistant"`, `"role":"user"`), result}, "invalid native tool-call part"},
		{"suppressed-tool", []string{strings.ReplaceAll(call, "Read", "GenerateImage"), strings.ReplaceAll(result, "Read", "GenerateImage")}, "unsupported native tool replay"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, blobs := prefetchedNativeRootTestState(test.raw...)
			turnID := sha256.Sum256([]byte("new turn"))
			state.Turns = [][]byte{turnID[:]}
			state.TokenDetails = &agentv1.ConversationTokenDetails{UsedTokens: 999}
			conversation := compactionAppendOnlyConversation(t)
			before, err := json.Marshal(conversation)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := (&Service{}).importConversationState(conversation, state, blobs)
			if err == nil || entries != nil {
				t.Fatal("无法完整转换的原生工具历史被静默接受")
			}
			if err.Error() != "decode imported replay messages: "+test.wantError {
				t.Fatalf("失败原因不符合预期: got=%v want=%s", err, test.wantError)
			}
			after, marshalErr := json.Marshal(conversation)
			if marshalErr != nil {
				t.Fatal(marshalErr)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("失败导入修改了原会话或元数据")
			}
		})
	}
}

func prefetchedNativeRootTestState(rawMessages ...string) (*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob) {
	state := &agentv1.ConversationStateStructure{}
	blobs := make([]*agentv1.PreFetchedBlob, 0, len(rawMessages))
	for _, raw := range rawMessages {
		digest := sha256.Sum256([]byte(raw))
		state.RootPromptMessagesJson = append(state.RootPromptMessagesJson, digest[:])
		blobs = append(blobs, &agentv1.PreFetchedBlob{Id: digest[:], Value: []byte(raw)})
	}
	return state, blobs
}

func TestImportedConversationStateNativeRootTakesPrecedenceOverTurns(t *testing.T) {
	state, blobs := prefetchedNativeRootTestState(`{"role":"user","content":"native root"}`)
	parent := compactionAppendOnlyConversation(t)
	parent.Entries = parent.Entries[:2]
	projection, err := NewHistoryProjector().ProjectCheckpointProjection(parent)
	if err != nil {
		t.Fatal(err)
	}
	state.Turns = projection.State.GetTurns()
	for _, blob := range projection.Blobs {
		blobs = append(blobs, &agentv1.PreFetchedBlob{Id: blob.ID, Value: blob.Data})
	}
	conversation, err := newRuntimeConversation("root-priority", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := newImportServiceWithBlobStore(t).importConversationState(conversation, state, blobs)
	if err != nil {
		t.Fatal(err)
	}
	appendEntriesInPlace(conversation, entries)
	messages, err := NewHistoryProjector().ProjectPromptReplay(conversation)
	if err != nil || len(messages) != 1 || messages[0].Content != "native root" {
		t.Fatalf("root 被 turns 替代或重复拼接: messages=%#v err=%v", messages, err)
	}
	if len(conversation.ImportedTurnIDs) != len(state.Turns) || conversation.NextTurnSeq != int64(len(state.Turns))+1 {
		t.Fatal("原生 root 读取丢失既有回合引用元数据")
	}
}

func TestImportedConversationStateFailurePreservesEntireConversation(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob)
		wantError string
	}{
		{"missing-root", func(state *agentv1.ConversationStateStructure, blobs []*agentv1.PreFetchedBlob) { blobs[0].Id = nil }, "missing prefetched turn blob"},
		{"corrupt-hash", func(state *agentv1.ConversationStateStructure, blobs []*agentv1.PreFetchedBlob) {
			blobs[0].Value = []byte("corrupt")
		}, "failed SHA-256 validation"},
		{"partial-root", func(state *agentv1.ConversationStateStructure, blobs []*agentv1.PreFetchedBlob) {
			missing := sha256.Sum256([]byte("missing root"))
			state.RootPromptMessagesJson = append(state.RootPromptMessagesJson, missing[:])
		}, "incomplete prefetched root message blobs"},
		{"mixed-root", func(state *agentv1.ConversationStateStructure, blobs []*agentv1.PreFetchedBlob) {
			state.RootPromptMessagesJson = append(state.RootPromptMessagesJson, []byte(`{"role":"user","content":"inline"}`))
		}, "mixed inline and referenced root messages"},
		{"late-plan-error", func(state *agentv1.ConversationStateStructure, blobs []*agentv1.PreFetchedBlob) {
			state.Plan = []byte{0xff}
		}, "decode imported plan"},
		{"late-todo-error", func(state *agentv1.ConversationStateStructure, blobs []*agentv1.PreFetchedBlob) {
			state.Todos = [][]byte{{0xff}}
		}, "decode imported todo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, blobs := prefetchedNativeRootTestState(`{"role":"user","content":"native root"}`)
			turnID := sha256.Sum256([]byte("new turn"))
			state.Turns = [][]byte{turnID[:], turnID[:]}
			state.TokenDetails = &agentv1.ConversationTokenDetails{UsedTokens: 999}
			test.change(state, blobs)
			conversation, err := newRuntimeConversation("preserve-import-error", agentv1.AgentMode_AGENT_MODE_AGENT)
			if err != nil {
				t.Fatal(err)
			}
			conversation.TokenDetailsUsedTokens = 17
			originalTurnID := sha256.Sum256([]byte("original turn"))
			conversation.ImportedTurnIDs = [][]byte{originalTurnID[:]}
			conversation.Entries = []HistoryEntry{compactionTestUserEntry(t, 1, "original-request", "original history", "original-message")}
			before, err := json.Marshal(conversation)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := (&Service{}).importConversationState(conversation, state, blobs)
			if err == nil || entries != nil {
				t.Fatal("导入失败没有返回明确错误和 nil entries")
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("失败阶段不符合预期: got=%v want=%s", err, test.wantError)
			}
			after, err := json.Marshal(conversation)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("失败导入修改了调用者完整会话状态")
			}
		})
	}
}

func TestImportedConversationStateRejectsInvalidNativeRootWithoutTurnFallback(t *testing.T) {
	for _, raw := range []string{
		`{"role":"user","content":[{"type":"image","image":{"__type":"Uint8Array","hex":"00ff"}}]}`,
		`{"role":"assistant","content":[{"type":"tool-call","toolCallId":"call-1","toolName":"Read","args":{}}]}`,
		`{"role":"future","content":"opaque"}`,
		`{"role":"user"}`, `null`, `[]`, `{broken`,
	} {
		t.Run(raw, func(t *testing.T) {
			state, prefetched := prefetchedNativeRootTestState(raw)
			// 即使有可用 turns，也不能掩盖已提供但无法完整读取的 root。
			state.Turns = [][]byte{{}}
			blobs, err := newImportedBlobStore(prefetched)
			if err != nil {
				t.Fatal(err)
			}
			messages, err := importedConversationStateModelMessagesWithBlobs(state, blobs)
			if err == nil || messages != nil {
				t.Fatal("损坏或不支持的原生 root 被忽略并改用 turns")
			}
		})
	}
}

func TestImportedConversationStateKeeps32ByteInlineJSON(t *testing.T) {
	raw := []byte(`{"role":"user","content":"four"}`)
	if len(raw) != sha256.Size {
		t.Fatal("夹具必须恰为32字节")
	}
	messages, err := importedConversationStateModelMessages(&agentv1.ConversationStateStructure{
		RootPromptMessagesJson: [][]byte{raw}, Turns: [][]byte{{}},
	})
	if err != nil || len(messages) != 1 || messages[0].Content != "four" {
		t.Fatalf("32字节旧内联 JSON 被误识别为引用: %#v err=%v", messages, err)
	}
}

func TestImportedConversationStateNativeRootReachesProviderThroughBidiAppend(t *testing.T) {
	state, blobs := prefetchedNativeRootTestState(
		`{"role":"system","content":"old system prompt"}`,
		`{"role":"user","content":"official question"}`,
		`{"role":"assistant","content":"official answer"}`,
		`{"role":"user","content":"official question"}`,
	)
	req := nativeRootProviderRequestThroughBidiAppend(t, state, blobs, 3)
	var history []modeladapter.Message
	for _, message := range req.Messages {
		switch message.Content {
		case "official question", "official answer":
			history = append(history, message)
		}
	}
	if len(history) != 3 || history[0].Role != "user" || history[1].Role != "assistant" || history[2].Role != "user" {
		t.Fatalf("模型输入缺失、重复或乱序: history=%#v", history)
	}
}

func TestImportedConversationStateNativeToolsReachProviderThroughBidiAppend(t *testing.T) {
	state, blobs := prefetchedNativeRootTestState(
		`{"role":"user","content":[{"type":"text","text":"official tool question"}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"before tools"},{"type":"tool-call","toolCallId":"tc_native_1","toolName":"Read","args":{"path":"/synthetic/a","offset":9007199254740993}},{"type":"tool-call","toolCallId":"tc_native_2","toolName":"Read","args":{"path":"/synthetic/b"}}]}`,
		`{"role":"tool","content":[{"type":"tool-result","toolCallId":"tc_native_2","toolName":"Read","result":"string result","experimental_content": [ ]},{"type":"tool-result","toolCallId":"tc_native_1","toolName":"Read","result":{"value":9007199254740993}}]}`,
		`{"role":"assistant","content":[{"type":"text","text":"after tools"}]}`,
	)
	req := nativeRootProviderRequestThroughBidiAppend(t, state, blobs, 5)
	start := -1
	for index, message := range req.Messages {
		if message.Role == "user" && message.Content == "official tool question" {
			if start >= 0 {
				t.Fatal("原生工具历史被重复追加")
			}
			start = index
		}
	}
	if start < 0 || start+5 > len(req.Messages) {
		t.Fatal("原生工具历史没有完整进入模型请求")
	}
	history := req.Messages[start : start+5]
	if history[1].Role != "assistant" || history[1].Content != "before tools" || len(history[1].ToolCalls) != 2 ||
		history[2].Role != "tool" || history[2].ToolCallID != "tc_native_2" || history[2].Name != "Read" || history[2].Content != "string result" ||
		history[3].Role != "tool" || history[3].ToolCallID != "tc_native_1" || history[3].Name != "Read" || history[3].Content != `{"value":9007199254740993}` ||
		history[4].Role != "assistant" || history[4].Content != "after tools" {
		t.Fatal("模型请求改变了工具关联、结果内容或历史顺序")
	}
	for index, id := range []string{"tc_native_1", "tc_native_2"} {
		call := history[1].ToolCalls[index]
		if call.ID != id || call.Index != index || call.Type != "function" || call.Function.Name != "Read" {
			t.Fatal("模型请求改变了工具调用身份和批次")
		}
	}
	if history[1].ToolCalls[0].Function.Arguments != `{"path":"/synthetic/a","offset":9007199254740993}` || history[1].ToolCalls[1].Function.Arguments != `{"path":"/synthetic/b"}` {
		t.Fatal("模型请求改变了工具参数或数值精度")
	}
}

func TestImportedConversationStateNativeRepeatedToolBatchesRemainDistinct(t *testing.T) {
	call := `{"role":"assistant","content":[{"type":"tool-call","toolCallId":"native::read","toolName":"Read","args":{}}]}`
	result := `{"role":"tool","content":[{"type":"tool-result","toolCallId":"native::read","toolName":"Read","result":"same result"}]}`
	state, blobs := prefetchedNativeRootTestState(call, result, call, result)
	req := nativeRootProviderRequestThroughBidiAppend(t, state, blobs, 4)
	var history []modeladapter.Message
	for _, message := range req.Messages {
		if message.ToolCallID == "native::read" || (len(message.ToolCalls) > 0 && message.ToolCalls[0].ID == "native::read") {
			history = append(history, message)
		}
	}
	if len(history) != 4 || history[0].Role != "assistant" || history[1].Role != "tool" || history[2].Role != "assistant" || history[3].Role != "tool" {
		t.Fatal("重复原生工具批次被合并或去重")
	}
	for _, index := range []int{0, 2} {
		if len(history[index].ToolCalls) != 1 || history[index].ToolCalls[0].Function.Arguments != "{}" || history[index+1].Content != "same result" {
			t.Fatal("重复工具批次的参数或结果丢失")
		}
	}
}

func TestImportedConversationStateNativeToolFailureStopsRequestWithoutWrites(t *testing.T) {
	state, blobs := prefetchedNativeRootTestState(
		`{"role":"user","content":[{"type":"text","text":"valid prefix"}]}`,
		`{"role":"assistant","content":[{"type":"tool-call","toolCallId":"missing-result","toolName":"Read","args":{}}]}`,
	)
	assertImportedHistoryFailureThroughBidiAppend(t, state, blobs, "native tool calls missing results")
}

func TestImportedStructuredFailuresStopRequestWithoutWrites(t *testing.T) {
	missing := sha256.Sum256([]byte("missing structured HTTP content"))
	legacy32, err := proto.Marshal(&agentv1.ConversationPlan{Plan: strings.Repeat("p", 30)})
	if err != nil || len(legacy32) != sha256.Size {
		t.Fatalf("歧义夹具必须是32字节旧内联计划: len=%d err=%v", len(legacy32), err)
	}
	defaultTodo := []byte{0x0a, 0x00, 0x18, 0x00}
	defaultTodoID := sha256.Sum256(defaultTodo)
	for _, test := range []struct {
		name      string
		plan      []byte
		todos     [][]byte
		blobs     []*agentv1.PreFetchedBlob
		wantError string
	}{
		{name: "missing-plan", plan: missing[:], wantError: "missing prefetched plan blob"},
		{name: "missing-todo", todos: [][]byte{missing[:]}, wantError: "missing prefetched todo blob"},
		{name: "ambiguous-legacy-plan", plan: legacy32, wantError: "missing prefetched plan blob"},
		{name: "default-encoded-todo", todos: [][]byte{defaultTodoID[:]},
			blobs:     []*agentv1.PreFetchedBlob{{Id: defaultTodoID[:], Value: defaultTodo}},
			wantError: "decode imported todo blob: empty todo"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, blobs := prefetchedNativeRootTestState(`{"role":"user","content":"valid history"}`, `{"role":"assistant","content":"valid answer"}`)
			state.Plan, state.Todos = test.plan, test.todos
			assertImportedHistoryFailureThroughBidiAppend(t, state, append(blobs, test.blobs...), test.wantError)
		})
	}
}

func assertImportedHistoryFailureThroughBidiAppend(t *testing.T, state *agentv1.ConversationStateStructure, blobs []*agentv1.PreFetchedBlob, wantError string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	store := NewConversationFileStore(t.TempDir())
	const conversationID = "native-tool-failure-conversation"
	if _, err := store.CreateConversation(conversationID, agentv1.AgentMode_AGENT_MODE_AGENT, "", "", ""); err != nil {
		t.Fatal(err)
	}
	original, err := store.UpdateConversationMeta(conversationID, func(item *ConversationFile) error {
		item.TokenDetailsUsedTokens = 17
		item.NextTurnSeq = 4
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	requests := make(chan modeladapter.StreamRequest, 1)
	gateway := &DefaultProviderGateway{router: providerGatewayTestRouter{
		stream: func(_ context.Context, req modeladapter.StreamRequest, _ func(modeladapter.ModelEvent) error) error {
			requests <- req
			return nil
		},
	}}
	projector := NewHistoryProjector()
	service := newServiceWithDependencies(store, projector,
		NewPromptCompiler(projector, NewToolCatalog(), NewReminderInjector(), nil), gateway, NewStreamBroker())
	const requestID = "native-tool-failure-request"
	t.Cleanup(func() {
		cleanupBidiAppendRequest(t, service, requestID, "清理失败请求", "失败请求的消息处理器未结束")
	})
	state.TokenDetails = &agentv1.ConversationTokenDetails{UsedTokens: 999}
	payload, err := proto.Marshal(&agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_RunRequest{RunRequest: &agentv1.AgentRunRequest{
		ConversationId: stringPtr(conversationID), RequestedModel: &agentv1.RequestedModel{ModelId: "byok-test-model"},
		ConversationState: state, PreFetchedBlobs: blobs,
		Action: &agentv1.ConversationAction{Action: &agentv1.ConversationAction_UserMessageAction{UserMessageAction: &agentv1.UserMessageAction{
			UserMessage: &agentv1.UserMessage{Text: "current question", MessageId: "current-message"},
		}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(connect.NewUnaryHandler("/aiserver.v1.BidiService/BidiAppend", service.BidiAppend))
	defer server.Close()
	client := connect.NewClient[aiserverv1.BidiAppendRequest, aiserverv1.BidiAppendResponse](server.Client(), server.URL+"/aiserver.v1.BidiService/BidiAppend")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.CallUnary(ctx, connect.NewRequest(&aiserverv1.BidiAppendRequest{
		RequestId: &aiserverv1.BidiRequestId{RequestId: requestID}, AppendSeqno: 1, Data: hex.EncodeToString(payload),
	}))
	if err == nil || connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), wantError) {
		t.Fatalf("导入失败未从请求入口返回预期错误: got=%v want-contains=%q", err, wantError)
	}
	select {
	case <-requests:
		t.Fatal("失败导入仍然启动了模型")
	default:
	}
	persisted, err := store.LoadConversation(conversationID)
	if err != nil {
		t.Fatal(err)
	}
	after, err := json.Marshal(persisted)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("失败请求写入了部分历史或修改原会话元数据")
	}
}

func TestWaitPostedStreamCommandKeepsResultWhenActorAlreadyStopped(t *testing.T) {
	for i := 0; i < 64; i++ {
		done := make(chan struct{})
		result := make(chan error, 1)
		result <- nil
		close(done)
		err := waitPostedStreamCommand(done, result)
		if err != nil {
			if errors.Is(err, errProviderLoopInterrupted) {
				t.Fatalf("iter %d: 命令已成功返回，actor 随后退出不得改报 %v", i, err)
			}
			t.Fatalf("iter %d: unexpected error %v", i, err)
		}
	}
}

func TestWaitPostedStreamCommandInterruptedWhenResultMissing(t *testing.T) {
	done := make(chan struct{})
	result := make(chan error)
	close(done)
	err := waitPostedStreamCommand(done, result)
	if !errors.Is(err, errProviderLoopInterrupted) {
		t.Fatalf("got %v, want provider loop interrupted", err)
	}
}

func cleanupBidiAppendRequest(t *testing.T, service *Service, requestID, cancelErrMsg, actorErrMsg string) {
	t.Helper()
	stream, ok := service.broker.Get(requestID)
	if !ok || stream == nil {
		return
	}
	cancelBidiAppendRequestForCleanup(t, service, stream, requestID, cancelErrMsg)
	waitForBidiAppendActor(t, stream, actorErrMsg)
}

func cancelBidiAppendRequestForCleanup(t *testing.T, service *Service, stream *ActiveStream, requestID, errMsg string) {
	t.Helper()
	if stream == nil {
		return
	}
	if isTerminalIntentStream(stream) {
		return
	}
	err := service.dispatchInboundIntent(InboundIntent{Kind: "cancel", RequestID: requestID, CancelReason: "test cleanup"})
	if err != nil && !errors.Is(err, errProviderLoopInterrupted) {
		t.Errorf("%s: %v", errMsg, err)
	}
}

func waitForBidiAppendActor(t *testing.T, stream *ActiveStream, errMsg string) {
	t.Helper()
	if stream == nil {
		return
	}
	stream.mu.Lock()
	done := stream.ActorDone
	stream.mu.Unlock()
	if done == nil {
		return
	}
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Error(errMsg)
	}
}

func nativeRootProviderRequestThroughBidiAppend(t *testing.T, state *agentv1.ConversationStateStructure, blobs []*agentv1.PreFetchedBlob, wantImportedCount int) modeladapter.StreamRequest {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	requests := make(chan modeladapter.StreamRequest, 1)
	providerDone := make(chan struct{})
	gateway := &DefaultProviderGateway{router: providerGatewayTestRouter{
		stream: func(ctx context.Context, req modeladapter.StreamRequest, _ func(modeladapter.ModelEvent) error) error {
			defer close(providerDone)
			requests <- req
			<-ctx.Done()
			return ctx.Err()
		},
	}}
	projector := NewHistoryProjector()
	service := newServiceWithDependencies(
		NewConversationFileStore(t.TempDir()), projector,
		NewPromptCompiler(projector, NewToolCatalog(), NewReminderInjector(), nil),
		gateway, NewStreamBroker(),
	)
	const requestID = "native-root-bidi-request"
	observed := false
	t.Cleanup(func() {
		stream, ok := service.broker.Get(requestID)
		if !ok || stream == nil {
			return
		}
		cancelBidiAppendRequestForCleanup(t, service, stream, requestID, "清理运行请求失败")
		if observed {
			select {
			case <-providerDone:
			case <-time.After(3 * time.Second):
				t.Error("模拟 provider 未结束")
			}
		}
		waitForBidiAppendActor(t, stream, "运行消息处理器未结束")
	})
	server := httptest.NewServer(connect.NewUnaryHandler("/aiserver.v1.BidiService/BidiAppend", service.BidiAppend))
	defer server.Close()
	client := connect.NewClient[aiserverv1.BidiAppendRequest, aiserverv1.BidiAppendResponse](server.Client(), server.URL+"/aiserver.v1.BidiService/BidiAppend")
	payload, err := proto.Marshal(&agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_RunRequest{RunRequest: &agentv1.AgentRunRequest{
		ConversationId:    stringPtr("native-root-bidi-conversation"),
		RequestedModel:    &agentv1.RequestedModel{ModelId: "byok-test-model"},
		ConversationState: state, PreFetchedBlobs: blobs,
		Action: &agentv1.ConversationAction{Action: &agentv1.ConversationAction_UserMessageAction{UserMessageAction: &agentv1.UserMessageAction{
			UserMessage: &agentv1.UserMessage{Text: "current question", MessageId: "current-message"},
		}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = client.CallUnary(ctx, connect.NewRequest(&aiserverv1.BidiAppendRequest{
		RequestId: &aiserverv1.BidiRequestId{RequestId: requestID}, AppendSeqno: 1, Data: hex.EncodeToString(payload),
	}))
	if err != nil {
		t.Fatalf("BidiAppend 原生历史入口失败: %v", err)
	}
	var req modeladapter.StreamRequest
	select {
	case req = <-requests:
		observed = true
	case <-ctx.Done():
		t.Fatal("原生历史没有到达模拟模型适配器")
	}
	if req.ModelID != "byok-test-model" || req.RequestID != requestID {
		t.Fatal("模型请求身份改变")
	}
	currentCount := 0
	for _, message := range req.Messages {
		if strings.Contains(message.Content, "old system prompt") {
			t.Fatal("旧系统提示污染本轮模型输入")
		}
		// 原有 current_user_request 提醒会再次引用正文，不能把它算成重复历史。
		if message.Role == "user" && strings.Contains(message.Content, "<user_query>\ncurrent question\n</user_query>") {
			currentCount++
		}
	}
	if currentCount != 1 {
		t.Fatalf("模型输入中的当前用户消息数=%d，预期1", currentCount)
	}
	persisted, err := service.store.LoadConversation("native-root-bidi-conversation")
	if err != nil || persisted == nil {
		t.Fatalf("导入历史未持久化: %v", err)
	}
	count := 0
	for _, entry := range persisted.Entries {
		if entry.Kind == "model_message" {
			count++
		}
	}
	if count != wantImportedCount {
		t.Fatalf("持久化原生消息数=%d，预期%d", count, wantImportedCount)
	}
	return req
}

func TestImportedConversationStateRestoresBlobOnlyForkAndCheckpointPrefix(t *testing.T) {
	parent := compactionAppendOnlyConversation(t)
	parent.Entries = parent.Entries[:2]
	parent.NextEntrySeq = 3
	parent.NextTurnSeq = 2
	projection, err := NewHistoryProjector().ProjectCheckpointProjection(parent)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	prefetched := make([]*agentv1.PreFetchedBlob, 0, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		prefetched = append(prefetched, &agentv1.PreFetchedBlob{Id: blob.ID, Value: blob.Data})
	}
	state := proto.Clone(projection.State).(*agentv1.ConversationStateStructure)
	state.RootPromptMessagesJson = nil
	conversation, err := newRuntimeConversation("fork-conversation", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatalf("newRuntimeConversation() error = %v", err)
	}
	entries, err := newImportServiceWithBlobStore(t).importConversationState(conversation, state, prefetched)
	if err != nil {
		t.Fatalf("importConversationState() error = %v", err)
	}
	if len(conversation.ImportedTurnIDs) != 1 || conversation.NextTurnSeq != 2 {
		t.Fatalf("imported prefix turns=%d next_turn_seq=%d, want 1 and 2", len(conversation.ImportedTurnIDs), conversation.NextTurnSeq)
	}
	if len(entries) != 2 {
		t.Fatalf("imported model entries = %d, want parent user and assistant", len(entries))
	}
	appendEntriesInPlace(conversation, append(entries,
		compactionTestUserEntry(t, 2, "request-2", "fork question", "message-2"),
	))
	forkProjection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("fork ProjectCheckpointProjection() error = %v", err)
	}
	if len(forkProjection.State.GetTurns()) != 2 {
		t.Fatalf("fork checkpoint turns = %d, want imported parent plus local fork turn", len(forkProjection.State.GetTurns()))
	}
	if string(forkProjection.State.GetTurns()[0]) != string(projection.State.GetTurns()[0]) {
		t.Fatal("fork checkpoint did not preserve the imported parent turn ID as its prefix")
	}
}

func TestImportedConversationStateFallsBackFromBlobRootPromptsToTurns(t *testing.T) {
	parent := compactionAppendOnlyConversation(t)
	parent.Entries = parent.Entries[:2]
	parent.NextEntrySeq = 3
	parent.NextTurnSeq = 2
	projection, err := NewHistoryProjector().ProjectCheckpointProjection(parent)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	prefetched := make([]*agentv1.PreFetchedBlob, 0, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		prefetched = append(prefetched, &agentv1.PreFetchedBlob{Id: blob.ID, Value: blob.Data})
	}
	state := proto.Clone(projection.State).(*agentv1.ConversationStateStructure)
	// root 内容缺失，turn 的 protobuf Blob 不能冒充 CoreMessage JSON。
	missingRoot := sha256.Sum256([]byte(`{"role":"user","content":"unavailable native root"}`))
	state.RootPromptMessagesJson = [][]byte{missingRoot[:]}
	conversation, err := newRuntimeConversation("fork-conversation", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatalf("newRuntimeConversation() error = %v", err)
	}
	entries, err := newImportServiceWithBlobStore(t).importConversationState(conversation, state, prefetched)
	if err != nil {
		t.Fatalf("importConversationState() error = %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("imported model entries = %d, want parent user and assistant", len(entries))
	}
}

func TestImportedConversationStateRejectsMalformedNonBlobRootPrompt(t *testing.T) {
	if _, err := importedConversationStateModelMessages(&agentv1.ConversationStateStructure{
		RootPromptMessagesJson: [][]byte{[]byte("hello")},
	}); err == nil {
		t.Fatal("importedConversationStateModelMessages() accepted malformed non-Blob replay data")
	}
}

func TestImportedConversationStateRejectsBlobRootPromptWithoutTurnFallback(t *testing.T) {
	blobID := sha256.Sum256([]byte("root prompt"))
	if _, err := importedConversationStateModelMessages(&agentv1.ConversationStateStructure{
		RootPromptMessagesJson: [][]byte{blobID[:]},
	}); err == nil {
		t.Fatal("importedConversationStateModelMessages() discarded a Blob replay reference without fallback turns")
	}
}

func TestImportedConversationStateRejectsUnresolvedBlobTurn(t *testing.T) {
	turnID := sha256.Sum256([]byte("missing imported turn"))
	conversation, err := newRuntimeConversation("fork-conversation", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatalf("newRuntimeConversation() error = %v", err)
	}
	if _, err := (&Service{}).importConversationState(conversation, &agentv1.ConversationStateStructure{
		Turns: [][]byte{turnID[:]},
	}, nil); err == nil {
		t.Fatal("importConversationState() accepted an unresolved Blob turn")
	}
}

func TestImportedTurnIDsPersistThroughConversationStore(t *testing.T) {
	store := NewConversationFileStore(t.TempDir())
	turnID := sha256.Sum256([]byte("parent turn"))
	conversation, err := newRuntimeConversation("fork-conversation", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatalf("newRuntimeConversation() error = %v", err)
	}
	conversation.ImportedTurnIDs = [][]byte{turnID[:]}
	persisted, err := store.SaveConversationWithEntries(conversation.ConversationID, conversation, []HistoryEntry{
		compactionTestUserEntry(t, 2, "request-2", "fork question", "message-2"),
	})
	if err != nil {
		t.Fatalf("SaveConversationWithEntries() error = %v", err)
	}
	if len(persisted.ImportedTurnIDs) != 1 || string(persisted.ImportedTurnIDs[0]) != string(turnID[:]) {
		t.Fatalf("persisted ImportedTurnIDs = %x, want %x", persisted.ImportedTurnIDs, turnID)
	}
	loaded, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if len(loaded.ImportedTurnIDs) != 1 || string(loaded.ImportedTurnIDs[0]) != string(turnID[:]) {
		t.Fatalf("loaded ImportedTurnIDs = %x, want %x", loaded.ImportedTurnIDs, turnID)
	}
}

func TestRewindImportedTurnPrefixUsesClientForkPoint(t *testing.T) {
	ids := make([][]byte, 3)
	for index := range ids {
		digest := sha256.Sum256([]byte{byte(index + 1)})
		ids[index] = digest[:]
	}
	trimmed := rewindImportedTurnPrefix(ids, runRewindDecision{
		TargetTurnSeq:      4,
		HasClientTurnCount: true,
		ClientTurnCount:    1,
	})
	if len(trimmed) != 1 || string(trimmed[0]) != string(ids[0]) {
		t.Fatalf("rewindImportedTurnPrefix() = %x, want first imported turn only", trimmed)
	}
}

func TestImportedStructuredStateReachesProviderThroughBidiAppend(t *testing.T) {
	state, blobs := prefetchedNativeRootTestState(
		`{"role":"user","content":"structured history question"}`,
		`{"role":"assistant","content":"structured history answer"}`,
	)
	planText := "结构化 HTTP 计划：验证真实导入入口。"
	planBody, err := proto.Marshal(&agentv1.ConversationPlan{Plan: planText})
	if err != nil {
		t.Fatal(err)
	}
	todoText := "结构化 HTTP 待办：内容完整后再调用模型。"
	todoBody, err := proto.Marshal(&agentv1.TodoItem{Id: "http-todo", Content: todoText,
		Status: agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS, CreatedAt: 9007199254740993, UpdatedAt: math.MaxInt64})
	if err != nil {
		t.Fatal(err)
	}
	planID, todoID := sha256.Sum256(planBody), sha256.Sum256(todoBody)
	state.Plan, state.Todos = planID[:], [][]byte{todoID[:]}
	blobs = append(blobs, &agentv1.PreFetchedBlob{Id: planID[:], Value: planBody}, &agentv1.PreFetchedBlob{Id: todoID[:], Value: todoBody})
	request := nativeRootProviderRequestThroughBidiAppend(t, state, blobs, 2)
	var hasPlan, hasTodo bool
	for _, message := range request.Messages {
		hasPlan = hasPlan || strings.Contains(message.Content, "<current_plan>\n"+planText+"\n</current_plan>")
		hasTodo = hasTodo || (strings.Contains(message.Content, "<todo_list>") && strings.Contains(message.Content, todoText))
	}
	if !hasPlan || !hasTodo {
		t.Fatalf("真实导入后的模型输入缺结构化计划/待办: plan=%t todo=%t", hasPlan, hasTodo)
	}
}

// TestImportedConversationStateReadsPrefetchedStructuredReferences 固定 §19.3e C0-W2 读侧合同：
// runtime_state 的 plan/todos 引用（恰32字节内容引用）必须经已校验预取仓库还原正文，
// 旧实现语义 RED：旧实现直接 proto.Unmarshal 32 字节引用，未做 Blob-aware 双读。
func TestImportedConversationStateReadsPrefetchedStructuredReferences(t *testing.T) {
	t.Run("prefetched-references", func(t *testing.T) {
		planText := "结构化计划：先核对引用，再保留重复的待办实例。"
		planBody, err := proto.Marshal(&agentv1.ConversationPlan{Plan: planText})
		if err != nil {
			t.Fatal(err)
		}
		planDigest := sha256.Sum256(planBody)

		todoA := &agentv1.TodoItem{
			Id:           "todo-a",
			Content:      "读取预取的结构化引用",
			Status:       agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS,
			CreatedAt:    9007199254740993,
			UpdatedAt:    math.MaxInt64,
			Dependencies: []string{"todo-root"},
		}
		todoB := &agentv1.TodoItem{
			Id:           "todo-b",
			Content:      "复核失败保全",
			Status:       agentv1.TodoStatus_TODO_STATUS_PENDING,
			CreatedAt:    1,
			UpdatedAt:    9007199254740993,
			Dependencies: []string{"todo-a", "todo-root"},
		}
		todoABody, err := proto.Marshal(todoA)
		if err != nil {
			t.Fatal(err)
		}
		todoBBody, err := proto.Marshal(todoB)
		if err != nil {
			t.Fatal(err)
		}
		todoADigest := sha256.Sum256(todoABody)
		todoBDigest := sha256.Sum256(todoBBody)

		state := &agentv1.ConversationStateStructure{
			Plan:  planDigest[:],
			Todos: [][]byte{todoADigest[:], todoBDigest[:], todoADigest[:]},
			Plans: map[string]*agentv1.PlanRegistryEntry{
				"current": {Id: "current", Path: "plan://synthetic/structured"},
			},
			TokenDetails: &agentv1.ConversationTokenDetails{UsedTokens: 4242},
		}
		prefetched := []*agentv1.PreFetchedBlob{
			{Id: planDigest[:], Value: planBody},
			{Id: todoADigest[:], Value: todoABody},
			{Id: todoBDigest[:], Value: todoBBody},
		}
		stateBefore, err := proto.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}

		conversation, err := newRuntimeConversation("structured-references", agentv1.AgentMode_AGENT_MODE_AGENT)
		if err != nil {
			t.Fatal(err)
		}
		conversation.TokenDetailsUsedTokens = 17
		conversation.NextTurnSeq = 4
		originalTurnID := sha256.Sum256([]byte("original structured turn"))
		conversation.ImportedTurnIDs = [][]byte{originalTurnID[:]}
		conversation.Entries = []HistoryEntry{compactionTestUserEntry(t, 1, "original-request", "original structured history", "original-message")}

		entries, err := (&Service{}).importConversationState(conversation, state, prefetched)
		if err != nil {
			t.Fatalf("读取预取结构化引用失败: %v", err)
		}
		stateAfter, err := proto.Marshal(state)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(stateBefore, stateAfter) {
			t.Fatal("成功导入修改了调用者传入的结构化状态输入")
		}

		runtimeEntry := HistoryEntry{}
		runtimeCount := 0
		for _, entry := range entries {
			if strings.TrimSpace(entry.Kind) == "runtime_state" {
				runtimeEntry = entry
				runtimeCount++
			}
		}
		if runtimeCount != 1 {
			t.Fatalf("runtime_state entry 数量=%d，预期 1", runtimeCount)
		}
		var runtimePayload runtimeStateEntryPayload
		if err := json.Unmarshal(runtimeEntry.Payload, &runtimePayload); err != nil {
			t.Fatalf("runtime_state payload 解码失败: %v", err)
		}
		if runtimePayload.PlanText != planText {
			t.Fatalf("plan 引用正文未按预取恢复: got=%q want=%q", runtimePayload.PlanText, planText)
		}

		projected, err := projectConversationStructuredState(&ConversationFile{
			ConversationID: "structured-references",
			Entries:        []HistoryEntry{runtimeEntry},
		})
		if err != nil {
			t.Fatalf("投影结构化状态失败: %v", err)
		}
		if !projected.HasPlan || projected.PlanText != planText {
			t.Fatalf("投影 plan 文本或存在标志错误: %#v", projected)
		}
		if len(projected.Todos) != 3 {
			t.Fatalf("todo 引用数量=%d，预期 3（含重复实例）", len(projected.Todos))
		}
		for index, want := range []*agentv1.TodoItem{todoA, todoB, todoA} {
			if !proto.Equal(projected.Todos[index], want) {
				t.Fatalf("todo[%d] 字段或顺序不符: got=%v want=%v", index, projected.Todos[index], want)
			}
		}
		if projected.Plans["current"] == nil || projected.Plans["current"].GetPath() != "plan://synthetic/structured" {
			t.Fatalf("Plans map 未保持内联: %#v", projected.Plans)
		}
	})

	t.Run("legacy-inline", func(t *testing.T) {
		// 非32字节旧内联正文（含未知字段）必须继续按原有内联解码/克隆规则读取，不新增拒绝。
		legacyPlanText := "旧非32字节内联计划正文，继续走原有内联解码与克隆规则。"
		planBody, err := proto.Marshal(&agentv1.ConversationPlan{Plan: legacyPlanText})
		if err != nil {
			t.Fatal(err)
		}
		planBody = append(planBody, 0x78, 0x01)
		if len(planBody) == sha256.Size {
			t.Fatalf("夹具必须是明确的非32字节内联正文，实际长度=%d", len(planBody))
		}
		todoBody, err := proto.Marshal(&agentv1.TodoItem{
			Id: "legacy-todo", Content: "旧内联待办", Status: agentv1.TodoStatus_TODO_STATUS_PENDING, CreatedAt: 5, UpdatedAt: 6,
		})
		if err != nil {
			t.Fatal(err)
		}
		todoBody = append(todoBody, 0x3a, 0x01, 0x78)
		if len(todoBody) == sha256.Size {
			t.Fatalf("夹具必须是明确的非32字节内联正文，实际长度=%d", len(todoBody))
		}
		state := &agentv1.ConversationStateStructure{
			Plan:         planBody,
			Todos:        [][]byte{todoBody},
			TokenDetails: &agentv1.ConversationTokenDetails{UsedTokens: 1},
		}
		conversation, err := newRuntimeConversation("legacy-inline-structured", agentv1.AgentMode_AGENT_MODE_AGENT)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := (&Service{}).importConversationState(conversation, state, nil)
		if err != nil {
			t.Fatalf("旧非32内联结构化状态读取失败: %v", err)
		}
		runtimeEntry := HistoryEntry{}
		found := false
		for _, entry := range entries {
			if strings.TrimSpace(entry.Kind) == "runtime_state" {
				runtimeEntry = entry
				found = true
			}
		}
		if !found {
			t.Fatal("旧内联结构化状态没有产生 runtime_state entry")
		}
		projected, err := projectConversationStructuredState(&ConversationFile{
			ConversationID: "legacy-inline-structured",
			Entries:        []HistoryEntry{runtimeEntry},
		})
		if err != nil {
			t.Fatalf("旧内联投影失败: %v", err)
		}
		if !projected.HasPlan || projected.PlanText != legacyPlanText {
			t.Fatalf("旧内联 plan 正文未按原有规则读取: %#v", projected)
		}
		if len(projected.Todos) != 1 || projected.Todos[0].GetId() != "legacy-todo" ||
			projected.Todos[0].GetContent() != "旧内联待办" || projected.Todos[0].GetStatus() != agentv1.TodoStatus_TODO_STATUS_PENDING ||
			projected.Todos[0].GetCreatedAt() != 5 || projected.Todos[0].GetUpdatedAt() != 6 {
			t.Fatalf("旧内联 todo 字段未按原有规则读取: %#v", projected.Todos)
		}
	})

	t.Run("mixed-structured-encodings", func(t *testing.T) {
		inlineTodo := &agentv1.TodoItem{Id: "inline-todo", Content: "旧内联待办", Status: agentv1.TodoStatus_TODO_STATUS_PENDING, CreatedAt: 7, UpdatedAt: 8, Dependencies: []string{"dep"}}
		refTodo := &agentv1.TodoItem{Id: "todo-a", Content: "引用待办", Status: agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS, CreatedAt: 9007199254740993, UpdatedAt: math.MaxInt64, Dependencies: []string{"todo-root"}}
		refPlanText := "混合读取计划：plan 为引用，todo 为旧非32内联。"
		inlinePlanText := "混合读取计划：plan 为旧非32内联，todo 为混合顺序。"
		inlineTodoBody, errInlineTodo := proto.Marshal(inlineTodo)
		refTodoBody, errRefTodo := proto.Marshal(refTodo)
		refPlanBody, errRefPlan := proto.Marshal(&agentv1.ConversationPlan{Plan: refPlanText})
		inlinePlanBody, errInlinePlan := proto.Marshal(&agentv1.ConversationPlan{Plan: inlinePlanText})
		if errInlineTodo != nil || errRefTodo != nil || errRefPlan != nil || errInlinePlan != nil {
			t.Fatalf("marshal 混合夹具失败: %v %v %v %v", errInlineTodo, errRefTodo, errRefPlan, errInlinePlan)
		}
		if len(inlineTodoBody) == sha256.Size || len(inlinePlanBody) == sha256.Size {
			t.Fatal("夹具必须是明确的非32字节内联正文")
		}
		refTodoDigest, refPlanDigest := sha256.Sum256(refTodoBody), sha256.Sum256(refPlanBody)

		for _, test := range []struct {
			name      string
			state     *agentv1.ConversationStateStructure
			blobs     []*agentv1.PreFetchedBlob
			wantPlan  string
			wantTodos []*agentv1.TodoItem
		}{
			{name: "plan-reference-todo-inline", wantPlan: refPlanText, wantTodos: []*agentv1.TodoItem{inlineTodo},
				state: &agentv1.ConversationStateStructure{Plan: refPlanDigest[:], Todos: [][]byte{inlineTodoBody}},
				blobs: []*agentv1.PreFetchedBlob{{Id: refPlanDigest[:], Value: refPlanBody}}},
			{name: "plan-inline-todos-mixed-order", wantPlan: inlinePlanText, wantTodos: []*agentv1.TodoItem{refTodo, inlineTodo, refTodo},
				state: &agentv1.ConversationStateStructure{Plan: inlinePlanBody, Todos: [][]byte{refTodoDigest[:], inlineTodoBody, refTodoDigest[:]}},
				blobs: []*agentv1.PreFetchedBlob{{Id: refTodoDigest[:], Value: refTodoBody}}},
		} {
			t.Run(test.name, func(t *testing.T) {
				conversation, err := newRuntimeConversation("mixed-structured-encodings", agentv1.AgentMode_AGENT_MODE_AGENT)
				if err != nil {
					t.Fatal(err)
				}
				entries, err := (&Service{}).importConversationState(conversation, test.state, test.blobs)
				if err != nil {
					t.Fatalf("混合结构化编码读取失败: %v", err)
				}
				runtimeEntry := HistoryEntry{}
				for _, entry := range entries {
					if strings.TrimSpace(entry.Kind) == "runtime_state" {
						runtimeEntry = entry
					}
				}
				projected, err := projectConversationStructuredState(&ConversationFile{
					ConversationID: "mixed-structured-encodings", Entries: []HistoryEntry{runtimeEntry}})
				if err != nil {
					t.Fatalf("混合结构化投影失败: %v", err)
				}
				if !projected.HasPlan || projected.PlanText != test.wantPlan || len(projected.Todos) != len(test.wantTodos) {
					t.Fatalf("混合读取 plan/todo 数量不一致: plan=%q todos=%d", projected.PlanText, len(projected.Todos))
				}
				for index, want := range test.wantTodos {
					if !proto.Equal(projected.Todos[index], want) {
						t.Fatalf("混合读取 todo[%d] 字段不符: got=%v want=%v", index, projected.Todos[index], want)
					}
				}
			})
		}
	})
}

// TestImportedConversationStateStructuredFailuresPreserveConversation 固定 §19.3e C0-W2 读侧负向合同：
// 每个恰32字节的结构化字段都必须按内容引用处理；缺失/损坏/哈希不匹配/引用正文未知字段都返回分类明确
// 的错误，nil entries 且原会话（非默认 TokenDetails/ImportedTurnIDs/NextTurnSeq/Entries）逐字段不变。
func TestImportedConversationStateStructuredFailuresPreserveConversation(t *testing.T) {
	validPlanBody, err := proto.Marshal(&agentv1.ConversationPlan{Plan: "结构化计划：先核对引用，再保留重复的待办实例。"})
	if err != nil {
		t.Fatal(err)
	}
	validPlanDigest := sha256.Sum256(validPlanBody)
	validTodoBody, err := proto.Marshal(&agentv1.TodoItem{
		Id: "todo-a", Content: "读取预取的结构化引用", Status: agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS,
		CreatedAt: 9007199254740993, UpdatedAt: math.MaxInt64, Dependencies: []string{"todo-root"},
	})
	if err != nil {
		t.Fatal(err)
	}
	validTodoDigest := sha256.Sum256(validTodoBody)
	missingDigest := sha256.Sum256([]byte("missing structured reference"))
	// 非法 wire-format 正文：解析原生引用正文时固定失败。
	badBody := []byte{0xff, 0xf0}
	badPlanDigest := sha256.Sum256(badBody)

	// 合法旧内联正文，但恰好32字节：即使 protobuf 能解析也必须按引用拒绝。
	inline32Plan, err := proto.Marshal(&agentv1.ConversationPlan{Plan: strings.Repeat("p", 30)})
	if err != nil {
		t.Fatal(err)
	}
	if len(inline32Plan) != sha256.Size {
		t.Fatalf("32字节内联 plan 夹具长度=%d", len(inline32Plan))
	}
	inline32Todo, err := proto.Marshal(&agentv1.TodoItem{Status: agentv1.TodoStatus_TODO_STATUS_PENDING, Dependencies: []string{strings.Repeat("d", 28)}})
	if err != nil {
		t.Fatal(err)
	}
	if len(inline32Todo) != sha256.Size {
		t.Fatalf("32字节内联 todo 夹具长度=%d", len(inline32Todo))
	}

	// 真实 SHA-256 可被 protobuf 接受为空 plan：夹具先证明该歧义，再要求仍报缺失。
	realHashBody, err := proto.Marshal(&agentv1.ConversationPlan{Plan: "synthetic-plan-167"})
	if err != nil {
		t.Fatal(err)
	}
	realHashPlan := sha256.Sum256(realHashBody)
	parsedRealHash := &agentv1.ConversationPlan{}
	if err := proto.Unmarshal(realHashPlan[:], parsedRealHash); err != nil {
		t.Fatalf("夹具前提不成立：真实引用应能被 protobuf 接受: %v", err)
	}
	if parsedRealHash.GetPlan() != "" {
		t.Fatal("夹具前提不成立：真实引用应被解析为空 plan")
	}

	// 引用正文带未知 protobuf 字段：会被现有 runtime schema 丢弃，必须明确拒绝。
	unknownPlanBody := append(append([]byte(nil), validPlanBody...), 0x78, 0x01)
	unknownPlanDigest := sha256.Sum256(unknownPlanBody)
	unknownTodoBody := append(append([]byte(nil), validTodoBody...), 0x3a, 0x01, 0x78)
	unknownTodoDigest := sha256.Sum256(unknownTodoBody)

	// 已校验引用指向全零 TodoItem 正文（proto.Marshal 为 0 字节）：读侧必须按空待办明确失败。
	emptyTodoBody := []byte{}
	emptyTodoDigest := sha256.Sum256(emptyTodoBody)

	for _, test := range []struct {
		name      string
		state     *agentv1.ConversationStateStructure
		blobs     []*agentv1.PreFetchedBlob
		wantError string
	}{
		{
			name:      "missing-plan-blob",
			state:     &agentv1.ConversationStateStructure{Plan: missingDigest[:]},
			wantError: "missing prefetched plan blob",
		},
		{
			name:      "missing-todo-blob",
			state:     &agentv1.ConversationStateStructure{Todos: [][]byte{missingDigest[:]}},
			wantError: "missing prefetched todo blob",
		},
		{
			name:      "legacy-inline-32-plan-rejected",
			state:     &agentv1.ConversationStateStructure{Plan: inline32Plan},
			wantError: "missing prefetched plan blob",
		},
		{
			name:      "legacy-inline-32-todo-rejected",
			state:     &agentv1.ConversationStateStructure{Todos: [][]byte{inline32Todo}},
			wantError: "missing prefetched todo blob",
		},
		{
			name:      "parseable-real-hash-still-missing",
			state:     &agentv1.ConversationStateStructure{Plan: realHashPlan[:]},
			wantError: "missing prefetched plan blob",
		},
		{
			name:      "corrupt-plan-body",
			state:     &agentv1.ConversationStateStructure{Plan: badPlanDigest[:]},
			blobs:     []*agentv1.PreFetchedBlob{{Id: badPlanDigest[:], Value: badBody}},
			wantError: "decode imported plan blob",
		},
		{
			name:  "corrupt-later-todo-body-after-readable",
			state: &agentv1.ConversationStateStructure{Todos: [][]byte{validTodoDigest[:], badPlanDigest[:]}},
			blobs: []*agentv1.PreFetchedBlob{
				{Id: validTodoDigest[:], Value: validTodoBody},
				{Id: badPlanDigest[:], Value: badBody},
			},
			wantError: "decode imported todo blob",
		},
		{
			name:      "bad-blob-hash",
			state:     &agentv1.ConversationStateStructure{Plan: validPlanDigest[:]},
			blobs:     []*agentv1.PreFetchedBlob{{Id: validPlanDigest[:], Value: []byte("tampered")}},
			wantError: "failed SHA-256 validation",
		},
		{
			name:      "unknown-field-plan-blob",
			state:     &agentv1.ConversationStateStructure{Plan: unknownPlanDigest[:]},
			blobs:     []*agentv1.PreFetchedBlob{{Id: unknownPlanDigest[:], Value: unknownPlanBody}},
			wantError: "unknown field",
		},
		{
			name:      "unknown-field-todo-blob",
			state:     &agentv1.ConversationStateStructure{Todos: [][]byte{unknownTodoDigest[:]}},
			blobs:     []*agentv1.PreFetchedBlob{{Id: unknownTodoDigest[:], Value: unknownTodoBody}},
			wantError: "unknown field",
		},
		{
			name:      "empty-todo-blob",
			state:     &agentv1.ConversationStateStructure{Todos: [][]byte{emptyTodoDigest[:]}},
			blobs:     []*agentv1.PreFetchedBlob{{Id: emptyTodoDigest[:], Value: emptyTodoBody}},
			wantError: "decode imported todo blob: empty todo",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			conversation, err := newRuntimeConversation("structured-failure", agentv1.AgentMode_AGENT_MODE_AGENT)
			if err != nil {
				t.Fatal(err)
			}
			conversation.TokenDetailsUsedTokens = 17
			conversation.TokenDetailsMaxTokens = 50_000
			conversation.NextTurnSeq = 6
			originalTurnID := sha256.Sum256([]byte("original structured failure turn"))
			conversation.ImportedTurnIDs = [][]byte{originalTurnID[:]}
			conversation.Entries = []HistoryEntry{compactionTestUserEntry(t, 3, "original-request", "original structured failure history", "original-message")}
			before, err := json.Marshal(conversation)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := (&Service{}).importConversationState(conversation, test.state, test.blobs)
			if err == nil {
				t.Fatal("结构化引用失败被静默接受")
			}
			if entries != nil {
				t.Fatalf("结构化引用失败仍返回 entries: %v", entries)
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("结构化失败分类不符: got=%v want-contains=%s", err, test.wantError)
			}
			after, err := json.Marshal(conversation)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("结构化失败修改了原会话元数据或历史")
			}
		})
	}
}

// 非空 protobuf wire 仍可只包含显式默认值；新引用必须按解码后的空对象判定，
// 不能只检查正文长度，否则导入成功的全零待办在原生再投影时无法写出。
func TestImportedConversationStateRejectsDefaultEncodedTodoReferences(t *testing.T) {
	for _, test := range []struct {
		name string
		body []byte
	}{
		{name: "empty-id", body: []byte{0x0a, 0x00}},
		{name: "zero-status", body: []byte{0x18, 0x00}},
		{name: "all-default-fields", body: []byte{0x0a, 0x00, 0x12, 0x00, 0x18, 0x00, 0x20, 0x00, 0x28, 0x00}},
	} {
		t.Run(test.name, func(t *testing.T) {
			item := &agentv1.TodoItem{}
			if err := proto.Unmarshal(test.body, item); err != nil || proto.Size(item) != 0 {
				t.Fatalf("夹具必须是合法但全零的非空 protobuf 正文: item=%v err=%v", item, err)
			}
			digest := sha256.Sum256(test.body)
			state := &agentv1.ConversationStateStructure{Todos: [][]byte{digest[:]}, TokenDetails: &agentv1.ConversationTokenDetails{UsedTokens: 999}}
			conversation, err := newRuntimeConversation("default-encoded-todo", agentv1.AgentMode_AGENT_MODE_AGENT)
			if err != nil {
				t.Fatal(err)
			}
			conversation.TokenDetailsUsedTokens = 17
			conversation.NextTurnSeq = 6
			conversation.Entries = []HistoryEntry{compactionTestUserEntry(t, 3, "original-request", "original history", "original-message")}
			before, err := json.Marshal(conversation)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := (&Service{}).importConversationState(conversation, state, []*agentv1.PreFetchedBlob{{Id: digest[:], Value: test.body}})
			if err == nil || entries != nil {
				t.Fatalf("全零待办引用被静默接受: entries=%v err=%v", entries, err)
			}
			if !strings.Contains(err.Error(), "decode imported todo blob: empty todo") {
				t.Fatalf("失败分类不符: %v", err)
			}
			after, err := json.Marshal(conversation)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("全零待办引用失败修改了原会话")
			}
		})
	}
	t.Run("legacy-inline-default-fields-remain-readable", func(t *testing.T) {
		body := []byte{0x0a, 0x00, 0x18, 0x00}
		conversation, err := newRuntimeConversation("legacy-default-todo", agentv1.AgentMode_AGENT_MODE_AGENT)
		if err != nil {
			t.Fatal(err)
		}
		entries, err := (&Service{}).importConversationState(conversation, &agentv1.ConversationStateStructure{Todos: [][]byte{body}}, nil)
		if err != nil {
			t.Fatalf("旧非32字节内联格式被新引用检查误伤: %v", err)
		}
		state, err := projectConversationStructuredState(&ConversationFile{Entries: entries})
		if err != nil || len(state.Todos) != 1 || !proto.Equal(state.Todos[0], &agentv1.TodoItem{}) {
			t.Fatalf("旧内联全零待办实例未保留: state=%+v err=%v", state, err)
		}
	})
}

// TestImportedNativeRootBindsRawAndSummary 覆盖普通文本、重复与空实例及原生摘要标记：
// 每条原文首条目保存未包装 raw 与 parts，空 user 不被省略，摘要 provider 视图被包装。
func TestImportedNativeRootBindsRawAndSummary(t *testing.T) {
	userRaw := `{"role":"user","content":"official question"}`
	assistantRaw := `{"role":"assistant","content":"official answer"}`
	emptyRaw := `{"role":"user","content":""}`
	summaryRaw := `{"role":"user","content":"earlier summary","providerOptions":{"cursor":{"isSummary":true}}}`
	state, prefetched := prefetchedNativeRootTestState(userRaw, assistantRaw, userRaw, emptyRaw, summaryRaw)
	conversation, err := newRuntimeConversation("native-raw-bind", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{}).importConversationState(conversation, state, prefetched)
	if err != nil {
		t.Fatalf("原生 root 原文绑定失败: %v", err)
	}
	wantRaws := []string{userRaw, assistantRaw, userRaw, emptyRaw, summaryRaw}
	if len(entries) != len(wantRaws) {
		t.Fatalf("条目数=%d，预期%d（含空与摘要实例）", len(entries), len(wantRaws))
	}
	for index, wantRaw := range wantRaws {
		var payload modelMessageEntryPayload
		if err := json.Unmarshal(entries[index].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if string(payload.NativeMessage) != wantRaw || payload.NativeMessageParts != 1 {
			t.Fatalf("第%d条未绑定原文或 parts: raw=%s parts=%d", index, payload.NativeMessage, payload.NativeMessageParts)
		}
	}
	var emptyPayload modelMessageEntryPayload
	if err := json.Unmarshal(entries[3].Payload, &emptyPayload); err != nil {
		t.Fatal(err)
	}
	if emptyPayload.Message.Role != "user" || emptyPayload.Message.Content != "" {
		t.Fatalf("空 user 原文实例被省略或改写: %+v", emptyPayload.Message)
	}
	var summaryPayload modelMessageEntryPayload
	if err := json.Unmarshal(entries[4].Payload, &summaryPayload); err != nil {
		t.Fatal(err)
	}
	if summaryPayload.Message.Content != "<conversation_summary>\nearlier summary\n</conversation_summary>" {
		t.Fatalf("摘要 provider 视图包装不符: %q", summaryPayload.Message.Content)
	}
	if strings.Contains(string(summaryPayload.NativeMessage), "conversation_summary") || !strings.Contains(string(summaryPayload.NativeMessage), "isSummary") {
		t.Fatalf("摘要原文被改写为包装视图: %s", summaryPayload.NativeMessage)
	}
	appendEntriesInPlace(conversation, entries)
	messages, err := NewHistoryProjector().ProjectPromptReplay(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if len(messages) != len(wantRaws) {
		t.Fatalf("模型回放消息数=%d，预期%d（空实例也保留）", len(messages), len(wantRaws))
	}
}

// TestImportedNativeRootToolGroupBindsRaw 覆盖单条 tool 原文解出多个结果时，
// 只有首条结果保存原文与 parts=2，后续条目不再携带 raw/parts。
func TestImportedNativeRootToolGroupBindsRaw(t *testing.T) {
	callRaw := `{"role":"assistant","content":[{"type":"tool-call","toolCallId":"read-1","toolName":"Read","args":{"path":"/a"}},{"type":"tool-call","toolCallId":"read-2","toolName":"Read","args":{"path":"/b"}}]}`
	resultRaw := `{"role":"tool","content":[{"type":"tool-result","toolCallId":"read-1","toolName":"Read","result":"one"},{"type":"tool-result","toolCallId":"read-2","toolName":"Read","result":"two"}]}`
	state, prefetched := prefetchedNativeRootTestState(callRaw, resultRaw)
	conversation, err := newRuntimeConversation("native-tool-group", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{}).importConversationState(conversation, state, prefetched)
	if err != nil {
		t.Fatalf("工具原文组导入失败: %v", err)
	}
	if len(entries) != 3 {
		t.Fatalf("条目数=%d，预期3（1调用+2结果）", len(entries))
	}
	var callPayload modelMessageEntryPayload
	if err := json.Unmarshal(entries[0].Payload, &callPayload); err != nil {
		t.Fatal(err)
	}
	if string(callPayload.NativeMessage) != callRaw || callPayload.NativeMessageParts != 1 {
		t.Fatalf("调用原文绑定不符: raw=%s parts=%d", callPayload.NativeMessage, callPayload.NativeMessageParts)
	}
	var resultFirst, resultSecond modelMessageEntryPayload
	if err := json.Unmarshal(entries[1].Payload, &resultFirst); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(entries[2].Payload, &resultSecond); err != nil {
		t.Fatal(err)
	}
	if string(resultFirst.NativeMessage) != resultRaw || resultFirst.NativeMessageParts != 2 {
		t.Fatalf("多结果原文组绑定不符: raw=%s parts=%d", resultFirst.NativeMessage, resultFirst.NativeMessageParts)
	}
	if len(resultSecond.NativeMessage) != 0 || resultSecond.NativeMessageParts != 0 {
		t.Fatal("原文组后续条目不应再携带 raw 或 parts")
	}
}

// TestImportedConversationStateRejectsUnknownNativeProviderOptions 固化摘要标记的严格拒绝边界。
func TestImportedConversationStateRejectsUnknownNativeProviderOptions(t *testing.T) {
	for _, test := range []struct {
		name      string
		raw       string
		wantError string
	}{
		{"nested-unknown", `{"role":"user","content":"x","providerOptions":{"cursor":{"isSummary":true,"extra":1}}}`, "invalid or unsupported native root message"},
		{"sibling-unknown", `{"role":"user","content":"x","providerOptions":{"cursor":{"isSummary":true},"other":1}}`, "invalid or unsupported native root message"},
		{"summary-false", `{"role":"user","content":"x","providerOptions":{"cursor":{"isSummary":false}}}`, "invalid or unsupported native root message"},
		{"summary-non-user", `{"role":"assistant","content":"x","providerOptions":{"cursor":{"isSummary":true}}}`, "invalid or unsupported native root message"},
		{"summary-array-content", `{"role":"user","content":[{"type":"text","text":"x"}],"providerOptions":{"cursor":{"isSummary":true}}}`, "invalid native string message content"},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, prefetched := prefetchedNativeRootTestState(test.raw)
			conversation, err := newRuntimeConversation("native-bad-summary", agentv1.AgentMode_AGENT_MODE_AGENT)
			if err != nil {
				t.Fatal(err)
			}
			entries, err := (&Service{}).importConversationState(conversation, state, prefetched)
			if err == nil || entries != nil {
				t.Fatalf("非法 providerOptions 被接受: entries=%v err=%v", entries, err)
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("错误分类不符: got=%v want-contains=%q", err, test.wantError)
			}
		})
	}
}

// TestImportedNativeSummaryReachesProviderWithoutLeakingRaw 走真实 BidiAppend 入口，
// 断言模型输入收到已包装摘要视图，且原生保全字段不泄露到 provider。
func TestImportedNativeSummaryReachesProviderWithoutLeakingRaw(t *testing.T) {
	summaryRaw := `{"role":"user","content":"prior summary text","providerOptions":{"cursor":{"isSummary":true}}}`
	userRaw := `{"role":"user","content":"official question"}`
	assistantRaw := `{"role":"assistant","content":"official answer"}`
	state, blobs := prefetchedNativeRootTestState(userRaw, assistantRaw, summaryRaw)
	req := nativeRootProviderRequestThroughBidiAppend(t, state, blobs, 3)
	wrapped := "<conversation_summary>\nprior summary text\n</conversation_summary>"
	found := false
	for _, message := range req.Messages {
		if strings.Contains(message.Content, "providerOptions") || strings.Contains(message.Content, "native_message") {
			t.Fatalf("原生保全字段泄露到 provider: %q", message.Content)
		}
		if message.Role == "user" && message.Content == wrapped {
			found = true
		}
	}
	if !found {
		t.Fatalf("provider 未收到已包装的原生摘要视图: %#v", req.Messages)
	}
}

func newImportServiceWithBlobStore(t *testing.T) *Service {
	t.Helper()
	return &Service{contentBlobs: NewContentBlobStore(t.TempDir())}
}

func marshalTestProto(t *testing.T, msg proto.Message) []byte {
	t.Helper()
	body, err := proto.Marshal(msg)
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func digestTestBytes(body []byte) []byte {
	sum := sha256.Sum256(body)
	return sum[:]
}

type testImportedTurnGraph struct {
	userBody, stepBody, turnBody []byte
	userID, stepID, turnID       []byte
	rootRaw                      string
	rootID                       []byte
	state                        *agentv1.ConversationStateStructure
	blobs                        []*agentv1.PreFetchedBlob
}

func newTestImportedTurnGraph(t *testing.T) testImportedTurnGraph {
	t.Helper()
	userBody := marshalTestProto(t, &agentv1.UserMessage{Text: "imported user", MessageId: "imported-user"})
	stepBody := marshalTestProto(t, &agentv1.ConversationStep{
		Message: &agentv1.ConversationStep_AssistantMessage{
			AssistantMessage: &agentv1.AssistantMessage{Text: "imported assistant"},
		},
	})
	userID := digestTestBytes(userBody)
	stepID := digestTestBytes(stepBody)
	count := uint32(4)
	turnBody := marshalTestProto(t, &agentv1.ConversationTurnStructure{
		Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
			AgentConversationTurn: &agentv1.AgentConversationTurnStructure{
				UserMessage:      userID,
				Steps:            [][]byte{{}, stepID},
				DynamicToolCount: &count,
			},
		},
	})
	turnID := digestTestBytes(turnBody)
	rootRaw := `{"role":"user","content":"native root"}`
	rootID := digestTestBytes([]byte(rootRaw))
	return testImportedTurnGraph{
		userBody: userBody, stepBody: stepBody, turnBody: turnBody,
		userID: userID, stepID: stepID, turnID: turnID,
		rootRaw: rootRaw, rootID: rootID,
		state: &agentv1.ConversationStateStructure{
			RootPromptMessagesJson: [][]byte{rootID},
			Turns:                  [][]byte{turnID, turnID},
			TokenDetails:           &agentv1.ConversationTokenDetails{UsedTokens: 99},
		},
		blobs: []*agentv1.PreFetchedBlob{
			{Id: rootID, Value: []byte(rootRaw)},
			{Id: userID, Value: userBody},
			{Id: stepID, Value: stepBody},
			{Id: turnID, Value: turnBody},
		},
	}
}

func (graph testImportedTurnGraph) blobsWithout(id []byte) []*agentv1.PreFetchedBlob {
	filtered := make([]*agentv1.PreFetchedBlob, 0, len(graph.blobs))
	for _, blob := range graph.blobs {
		if string(blob.Id) == string(id) {
			continue
		}
		filtered = append(filtered, blob)
	}
	return filtered
}

func referencedTurnState(t *testing.T, turn proto.Message, extra ...*agentv1.PreFetchedBlob) (*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob) {
	t.Helper()
	turnBody := marshalTestProto(t, turn)
	turnID := digestTestBytes(turnBody)
	rootRaw := `{"role":"user","content":"native root"}`
	rootID := digestTestBytes([]byte(rootRaw))
	state := &agentv1.ConversationStateStructure{
		RootPromptMessagesJson: [][]byte{rootID},
		Turns:                  [][]byte{turnID},
	}
	blobs := append([]*agentv1.PreFetchedBlob{
		{Id: rootID, Value: []byte(rootRaw)},
		{Id: turnID, Value: turnBody},
	}, extra...)
	return state, blobs
}

func referencedTurnWithUserAndStep(t *testing.T, user *agentv1.UserMessage, step *agentv1.ConversationStep) (*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob) {
	t.Helper()
	userBody := marshalTestProto(t, user)
	stepBody := marshalTestProto(t, step)
	userID := digestTestBytes(userBody)
	stepID := digestTestBytes(stepBody)
	turn := &agentv1.ConversationTurnStructure{
		Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
			AgentConversationTurn: &agentv1.AgentConversationTurnStructure{
				UserMessage: userID,
				Steps:       [][]byte{stepID},
			},
		},
	}
	return referencedTurnState(t, turn,
		&agentv1.PreFetchedBlob{Id: userID, Value: userBody},
		&agentv1.PreFetchedBlob{Id: stepID, Value: stepBody},
	)
}

func assertConversationUnchanged(t *testing.T, before []byte, conversation *ConversationFile) {
	t.Helper()
	after, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("失败导入修改了调用者完整会话状态")
	}
}

func snapshotConversation(t *testing.T, conversation *ConversationFile) []byte {
	t.Helper()
	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	return before
}

func preserveImportConversation(t *testing.T) *ConversationFile {
	t.Helper()
	conversation, err := newRuntimeConversation("imported-turn-graph", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	conversation.TokenDetailsUsedTokens = 17
	conversation.NextTurnSeq = 4
	conversation.ImportedTurnIDs = [][]byte{digestTestBytes([]byte("original imported turn"))}
	conversation.Entries = []HistoryEntry{compactionTestUserEntry(t, 1, "original-request", "original history", "original-message")}
	return conversation
}

// TestImportedConversationStatePersistsReferencedTurnGraph 固化 §19.3i：完整预取回合图
// 必须写入 ContentBlobStore；新 store 重建后 native 投影保留原 Turn/User/Step ID 与正文，
// root 仍来自已保存消息视图，不把回合图再拼进模型历史。
func TestImportedConversationStatePersistsReferencedTurnGraph(t *testing.T) {
	graph := newTestImportedTurnGraph(t)
	historyRoot := t.TempDir()
	blobStore := NewContentBlobStore(historyRoot)
	fileStore := NewConversationFileStore(historyRoot)
	conversation, err := newRuntimeConversation("persist-turn-graph", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{contentBlobs: blobStore}).importConversationState(conversation, graph.state, graph.blobs)
	if err != nil {
		t.Fatalf("完整回合图导入失败: %v", err)
	}
	if len(conversation.ImportedTurnIDs) != 2 || string(conversation.ImportedTurnIDs[0]) != string(graph.turnID) || string(conversation.ImportedTurnIDs[1]) != string(graph.turnID) {
		t.Fatalf("重复回合引用未保留: %x", conversation.ImportedTurnIDs)
	}
	appendEntriesInPlace(conversation, entries)
	if _, err := fileStore.SaveConversationWithEntries(conversation.ConversationID, conversation, conversation.Entries); err != nil {
		t.Fatalf("保存会话失败: %v", err)
	}

	reloadedBlobs := NewContentBlobStore(historyRoot)
	for _, want := range []struct {
		name string
		id   []byte
		body []byte
	}{
		{"turn", graph.turnID, graph.turnBody},
		{"user", graph.userID, graph.userBody},
		{"step", graph.stepID, graph.stepBody},
	} {
		got, err := reloadedBlobs.Get(want.id)
		if err != nil {
			t.Fatalf("重启后缺少%s正文: %v", want.name, err)
		}
		if !bytes.Equal(got, want.body) {
			t.Fatalf("重启后%s正文失真", want.name)
		}
	}

	loaded, err := fileStore.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(loaded, reloadedBlobs)
	if err != nil {
		t.Fatalf("重启后 native 投影失败（旧实现语义 RED）: %v", err)
	}
	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		repo[string(blob.ID)] = blob.Data
	}
	for _, want := range []struct {
		name string
		id   []byte
		body []byte
	}{
		{"turn", graph.turnID, graph.turnBody},
		{"user", graph.userID, graph.userBody},
		{"step", graph.stepID, graph.stepBody},
	} {
		if !bytes.Equal(repo[string(want.id)], want.body) {
			t.Fatalf("native Blobs 缺少原%s ID/正文", want.name)
		}
	}
	turns := projection.State.GetTurns()
	if len(turns) < 2 || string(turns[0]) != string(graph.turnID) || string(turns[1]) != string(graph.turnID) {
		t.Fatalf("native Turns 未保留原重复回合 ID: %x", turns)
	}
	decodedTurn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(repo[string(graph.turnID)], decodedTurn); err != nil {
		t.Fatal(err)
	}
	if decodedTurn.GetAgentConversationTurn().GetDynamicToolCount() != 4 {
		t.Fatalf("dynamic_tool_count 未按原文保留: %d", decodedTurn.GetAgentConversationTurn().GetDynamicToolCount())
	}
	if len(decodedTurn.GetAgentConversationTurn().GetSteps()) != 2 || len(decodedTurn.GetAgentConversationTurn().GetSteps()[0]) != 0 {
		t.Fatal("空 step 槽位被丢掉")
	}
	root := projection.State.GetRootPromptMessagesJson()
	if len(root) != 1 {
		t.Fatalf("root 条数=%d，回合图被拼进 root", len(root))
	}
	if !bytes.Equal(compactTestJSON(t, repo[string(root[0])]), compactTestJSON(t, []byte(graph.rootRaw))) {
		t.Fatalf("root 不是已保存的原生消息视图: %s", repo[string(root[0])])
	}
	replay, err := NewHistoryProjector().ProjectPromptReplay(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].Content != "native root" {
		t.Fatalf("回合图被拼进模型输入: %#v", replay)
	}
}

// TestImportedConversationStateRejectsMissingTurnChildWithCompleteRoot 即使 native root 齐全，
// 回合子节点缺失也必须停止且不改会话。
func TestImportedConversationStateRejectsMissingTurnChildWithCompleteRoot(t *testing.T) {
	graph := newTestImportedTurnGraph(t)
	conversation := preserveImportConversation(t)
	before := snapshotConversation(t, conversation)
	entries, err := newImportServiceWithBlobStore(t).importConversationState(conversation, graph.state, graph.blobsWithout(graph.stepID))
	if err == nil || entries != nil {
		t.Fatal("root 齐全时缺失 step 子节点仍被接受（旧实现语义 RED）")
	}
	if !strings.Contains(err.Error(), "missing prefetched conversation step blob") {
		t.Fatalf("错误分类不符: %v", err)
	}
	assertConversationUnchanged(t, before, conversation)
}

// TestImportedConversationStateStorageFailurePreservesMetadata 图校验成功但 Put 失败时不得改 metadata。
func TestImportedConversationStateStorageFailurePreservesMetadata(t *testing.T) {
	graph := newTestImportedTurnGraph(t)
	conversation := preserveImportConversation(t)
	before := snapshotConversation(t, conversation)
	entries, err := (&Service{contentBlobs: &ContentBlobStore{}}).importConversationState(conversation, graph.state, graph.blobs)
	if err == nil || entries != nil {
		t.Fatal("内容存储失败仍更新了会话（旧实现语义 RED）")
	}
	if !strings.Contains(err.Error(), "content blob store is not initialized") {
		t.Fatalf("存储失败分类不符: %v", err)
	}
	assertConversationUnchanged(t, before, conversation)
}

// TestImportedConversationStateRejectsAmbiguous32UserMessageChild 具名 user_message 恰32字节
// 必须当引用，不得按 UserMessage 内联猜中。
func TestImportedConversationStateRejectsAmbiguous32UserMessageChild(t *testing.T) {
	inlineUser := marshalTestProto(t, &agentv1.UserMessage{Text: strings.Repeat("a", 30)})
	if len(inlineUser) != sha256.Size {
		t.Fatalf("构造失败: UserMessage 内联长度=%d，要恰好32", len(inlineUser))
	}
	stepBody := marshalTestProto(t, &agentv1.ConversationStep{
		Message: &agentv1.ConversationStep_AssistantMessage{
			AssistantMessage: &agentv1.AssistantMessage{Text: "ok"},
		},
	})
	stepID := digestTestBytes(stepBody)
	state, blobs := referencedTurnState(t, &agentv1.ConversationTurnStructure{
		Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
			AgentConversationTurn: &agentv1.AgentConversationTurnStructure{
				UserMessage: inlineUser,
				Steps:       [][]byte{stepID},
			},
		},
	}, &agentv1.PreFetchedBlob{Id: stepID, Value: stepBody})
	conversation := preserveImportConversation(t)
	before := snapshotConversation(t, conversation)
	entries, err := newImportServiceWithBlobStore(t).importConversationState(conversation, state, blobs)
	if err == nil || entries != nil {
		t.Fatal("32字节 user_message 被当成内联正文（旧实现语义 RED）")
	}
	if !strings.Contains(err.Error(), "missing prefetched user message blob") {
		t.Fatalf("错误分类不符: %v", err)
	}
	assertConversationUnchanged(t, before, conversation)
}

// TestImportedConversationStateAcceptsInline32ToolDataBytes Read 结果内联 data 即使长32也不是引用。
func TestImportedConversationStateAcceptsInline32ToolDataBytes(t *testing.T) {
	step := &agentv1.ConversationStep{
		Message: &agentv1.ConversationStep_ToolCall{
			ToolCall: &agentv1.ToolCall{
				Tool: &agentv1.ToolCall_ReadToolCall{
					ReadToolCall: &agentv1.ReadToolCall{
						Result: &agentv1.ReadToolResult{
							Result: &agentv1.ReadToolResult_Success{
								Success: &agentv1.ReadToolSuccess{
									Path: "/synthetic/inline.bin",
									Output: &agentv1.ReadToolSuccess_Data{
										Data: bytes.Repeat([]byte{0xab}, 32),
									},
								},
							},
						},
					},
				},
			},
		},
	}
	state, blobs := referencedTurnWithUserAndStep(t, &agentv1.UserMessage{Text: "read inline"}, step)
	conversation, err := newRuntimeConversation("inline-32-data", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newImportServiceWithBlobStore(t).importConversationState(conversation, state, blobs); err != nil {
		t.Fatalf("内联32字节工具 data 被误判为引用: %v", err)
	}
}

func TestImportedConversationStateAcceptsEmptyEncryptedModel(t *testing.T) {
	empty := ""
	userBody := marshalTestProto(t, &agentv1.UserMessage{Text: "ok", MessageId: "u"})
	userID := digestTestBytes(userBody)
	state, blobs := referencedTurnState(t, &agentv1.ConversationTurnStructure{
		Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
			AgentConversationTurn: &agentv1.AgentConversationTurnStructure{
				UserMessage:    userID,
				EncryptedModel: &empty,
			},
		},
	}, &agentv1.PreFetchedBlob{Id: userID, Value: userBody})
	conversation, err := newRuntimeConversation("empty-encrypted-model", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newImportServiceWithBlobStore(t).importConversationState(conversation, state, blobs); err != nil {
		t.Fatalf("显式空 encrypted_model 被拒绝: %v", err)
	}
}

func TestImportedConversationStateRejectsNestedTurnGraphViolations(t *testing.T) {
	user := &agentv1.UserMessage{Text: "ok", MessageId: "u"}
	okStep := &agentv1.ConversationStep{
		Message: &agentv1.ConversationStep_AssistantMessage{
			AssistantMessage: &agentv1.AssistantMessage{Text: "ok"},
		},
	}
	for _, test := range []struct {
		name      string
		setup     func(t *testing.T) (*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob)
		wantError string
	}{
		{
			name: "nested-read-data-blob-id",
			setup: func(t *testing.T) (*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob) {
				return referencedTurnWithUserAndStep(t, user, &agentv1.ConversationStep{
					Message: &agentv1.ConversationStep_ToolCall{
						ToolCall: &agentv1.ToolCall{
							Tool: &agentv1.ToolCall_ReadToolCall{
								ReadToolCall: &agentv1.ReadToolCall{
									Result: &agentv1.ReadToolResult{
										Result: &agentv1.ReadToolResult_Success{
											Success: &agentv1.ReadToolSuccess{
												Output: &agentv1.ReadToolSuccess_DataBlobId{
													DataBlobId: bytes.Repeat([]byte{0x11}, 32),
												},
											},
										},
									},
								},
							},
						},
					},
				})
			},
			wantError: "unsupported blob reference",
		},
		{
			name: "nested-task-success-content-blob-id",
			setup: func(t *testing.T) (*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob) {
				return referencedTurnWithUserAndStep(t, user, &agentv1.ConversationStep{
					Message: &agentv1.ConversationStep_ToolCall{
						ToolCall: &agentv1.ToolCall{
							Tool: &agentv1.ToolCall_TaskToolCall{
								TaskToolCall: &agentv1.TaskToolCall{
									Result: &agentv1.TaskResult{
										Result: &agentv1.TaskResult_Success{
											Success: &agentv1.TaskSuccess{
												ConversationSteps: []*agentv1.ConversationStep{{
													Message: &agentv1.ConversationStep_ToolCall{
														ToolCall: &agentv1.ToolCall{
															Tool: &agentv1.ToolCall_ReadToolCall{
																ReadToolCall: &agentv1.ReadToolCall{
																	Result: &agentv1.ReadToolResult{
																		Result: &agentv1.ReadToolResult_Success{
																			Success: &agentv1.ReadToolSuccess{
																				Output: &agentv1.ReadToolSuccess_ContentBlobId{
																					ContentBlobId: bytes.Repeat([]byte{0x22}, 32),
																				},
																			},
																		},
																	},
																},
															},
														},
													},
												}},
											},
										},
									},
								},
							},
						},
					},
				})
			},
			wantError: "unsupported blob reference",
		},
		{
			name: "user-text-blob-id",
			setup: func(t *testing.T) (*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob) {
				blobID := bytes.Repeat([]byte{0x33}, 32)
				return referencedTurnWithUserAndStep(t, &agentv1.UserMessage{Text: "x", TextBlobId: blobID}, okStep)
			},
			wantError: "unsupported blob reference",
		},
		{
			name: "truncated-original-step-blob",
			setup: func(t *testing.T) (*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob) {
				return referencedTurnWithUserAndStep(t, user, &agentv1.ConversationStep{
					Message: &agentv1.ConversationStep_ToolCall{
						ToolCall: &agentv1.ToolCall{
							Tool: &agentv1.ToolCall_TruncatedToolCall{
								TruncatedToolCall: &agentv1.TruncatedToolCall{
									OriginalStepBlobId: bytes.Repeat([]byte{0x44}, 32),
								},
							},
						},
					},
				})
			},
			wantError: "unsupported blob reference",
		},
		{
			name: "shell-turn",
			setup: func(t *testing.T) (*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob) {
				return referencedTurnState(t, &agentv1.ConversationTurnStructure{
					Turn: &agentv1.ConversationTurnStructure_ShellConversationTurn{
						ShellConversationTurn: &agentv1.ShellConversationTurnStructure{ShellCommand: []byte("echo")},
					},
				})
			},
			wantError: "shell turn",
		},
		{
			name: "encrypted-model",
			setup: func(t *testing.T) (*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob) {
				secret := "encrypted-model-id"
				userBody := marshalTestProto(t, user)
				userID := digestTestBytes(userBody)
				return referencedTurnState(t, &agentv1.ConversationTurnStructure{
					Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
						AgentConversationTurn: &agentv1.AgentConversationTurnStructure{
							UserMessage:    userID,
							EncryptedModel: &secret,
						},
					},
				}, &agentv1.PreFetchedBlob{Id: userID, Value: userBody})
			},
			wantError: "encrypted_model",
		},
		{
			name: "encrypted-model-whitespace",
			setup: func(t *testing.T) (*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob) {
				secret := " \t"
				userBody := marshalTestProto(t, user)
				userID := digestTestBytes(userBody)
				return referencedTurnState(t, &agentv1.ConversationTurnStructure{
					Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
						AgentConversationTurn: &agentv1.AgentConversationTurnStructure{
							UserMessage:    userID,
							EncryptedModel: &secret,
						},
					},
				}, &agentv1.PreFetchedBlob{Id: userID, Value: userBody})
			},
			wantError: "encrypted_model",
		},
		{
			name: "unknown-field-turn-blob",
			setup: func(t *testing.T) (*agentv1.ConversationStateStructure, []*agentv1.PreFetchedBlob) {
				userBody := marshalTestProto(t, user)
				userID := digestTestBytes(userBody)
				turnBody := append(marshalTestProto(t, &agentv1.ConversationTurnStructure{
					Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
						AgentConversationTurn: &agentv1.AgentConversationTurnStructure{UserMessage: userID},
					},
				}), 0x78, 0x01)
				turnID := digestTestBytes(turnBody)
				rootRaw := `{"role":"user","content":"native root"}`
				rootID := digestTestBytes([]byte(rootRaw))
				return &agentv1.ConversationStateStructure{
						RootPromptMessagesJson: [][]byte{rootID},
						Turns:                  [][]byte{turnID},
					}, []*agentv1.PreFetchedBlob{
						{Id: rootID, Value: []byte(rootRaw)},
						{Id: userID, Value: userBody},
						{Id: turnID, Value: turnBody},
					}
			},
			wantError: "unknown field",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			state, blobs := test.setup(t)
			conversation := preserveImportConversation(t)
			before := snapshotConversation(t, conversation)
			entries, err := newImportServiceWithBlobStore(t).importConversationState(conversation, state, blobs)
			if err == nil || entries != nil {
				t.Fatalf("%s 被接受（旧实现语义 RED）", test.name)
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("错误分类不符: got=%v want-contains=%q", err, test.wantError)
			}
			assertConversationUnchanged(t, before, conversation)
		})
	}
}

func TestImportedConversationStateTurnGraphSurvivesRewindPrefix(t *testing.T) {
	first := newTestImportedTurnGraph(t)
	secondUser := marshalTestProto(t, &agentv1.UserMessage{Text: "second user", MessageId: "second"})
	secondUserID := digestTestBytes(secondUser)
	secondTurn := marshalTestProto(t, &agentv1.ConversationTurnStructure{
		Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
			AgentConversationTurn: &agentv1.AgentConversationTurnStructure{UserMessage: secondUserID},
		},
	})
	secondTurnID := digestTestBytes(secondTurn)
	first.state.Turns = [][]byte{first.turnID, secondTurnID}
	first.blobs = append(first.blobs,
		&agentv1.PreFetchedBlob{Id: secondUserID, Value: secondUser},
		&agentv1.PreFetchedBlob{Id: secondTurnID, Value: secondTurn},
	)
	historyRoot := t.TempDir()
	blobStore := NewContentBlobStore(historyRoot)
	conversation, err := newRuntimeConversation("rewind-turn-graph", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{contentBlobs: blobStore}).importConversationState(conversation, first.state, first.blobs)
	if err != nil {
		t.Fatal(err)
	}
	appendEntriesInPlace(conversation, entries)
	conversation.ImportedTurnIDs = rewindImportedTurnPrefix(conversation.ImportedTurnIDs, runRewindDecision{
		TargetTurnSeq:      9,
		HasClientTurnCount: true,
		ClientTurnCount:    1,
	})
	if len(conversation.ImportedTurnIDs) != 1 || string(conversation.ImportedTurnIDs[0]) != string(first.turnID) {
		t.Fatalf("前缀截断后 IDs=%x", conversation.ImportedTurnIDs)
	}
	restart := NewContentBlobStore(historyRoot)
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation, restart)
	if err != nil {
		t.Fatalf("截断后共享内容无法再投影: %v", err)
	}
	if len(projection.State.GetTurns()) < 1 || string(projection.State.GetTurns()[0]) != string(first.turnID) {
		t.Fatal("截断后未写出保留的原回合 ID")
	}
	for _, turnID := range projection.State.GetTurns() {
		if string(turnID) == string(secondTurnID) {
			t.Fatal("截断后仍写出已丢弃的回合 ID")
		}
	}
}

func TestImportedTurnGraphMissingChildStopsBidiAppendWithoutWrites(t *testing.T) {
	graph := newTestImportedTurnGraph(t)
	assertImportedHistoryFailureThroughBidiAppend(t, graph.state, graph.blobsWithout(graph.userID), "missing prefetched user message blob")
}

type testImportedSummaryGraph struct {
	rootRaw, msgUser, msgTool, msgSummary      []byte
	rootID, msgUserID, msgToolID, msgSummaryID []byte
	summaryBody, summaryID                     []byte
	archiveBody, archiveID                     []byte
	emptySummaryBody, emptySummaryID           []byte
	inlineSummary                              []byte
	state                                      *agentv1.ConversationStateStructure
	blobs                                      []*agentv1.PreFetchedBlob
}

func newTestImportedSummaryGraph(t *testing.T) testImportedSummaryGraph {
	t.Helper()
	rootRaw := []byte(`{"role":"user","content":"native root"}`)
	msgUser := []byte(`{"role":"user","content":"archived-only-question"}`)
	msgTool := []byte(`{"role":"assistant","content":[{"type":"tool-call","toolCallId":"arch-1","toolName":"Read","args":{"path":"/synthetic/archived.bin"}}]}`)
	msgSummary := []byte(`{"role":"user","content":"archived summary message"}`)
	rootID := digestTestBytes(rootRaw)
	msgUserID := digestTestBytes(msgUser)
	msgToolID := digestTestBytes(msgTool)
	msgSummaryID := digestTestBytes(msgSummary)
	summaryBody := marshalTestProto(t, &agentv1.ConversationSummary{Summary: "native summary text"})
	summaryID := digestTestBytes(summaryBody)
	emptySummaryBody := []byte{0x0a, 0x00}
	emptySummaryID := digestTestBytes(emptySummaryBody)
	inlineSummary := marshalTestProto(t, &agentv1.ConversationSummary{Summary: "inline native summary"})
	if len(inlineSummary) == sha256.Size {
		t.Fatal("inline summary unexpectedly 32 bytes")
	}
	archiveBody := marshalTestProto(t, &agentv1.ConversationSummaryArchive{
		SummarizedMessages: [][]byte{{}, msgUserID, msgUserID, msgToolID},
		Summary:            "archive summary text",
		WindowTail:         3,
		SummaryMessage:     msgSummaryID,
	})
	archiveID := digestTestBytes(archiveBody)
	return testImportedSummaryGraph{
		rootRaw: rootRaw, msgUser: msgUser, msgTool: msgTool, msgSummary: msgSummary,
		rootID: rootID, msgUserID: msgUserID, msgToolID: msgToolID, msgSummaryID: msgSummaryID,
		summaryBody: summaryBody, summaryID: summaryID,
		archiveBody: archiveBody, archiveID: archiveID,
		emptySummaryBody: emptySummaryBody, emptySummaryID: emptySummaryID,
		inlineSummary: inlineSummary,
		state: &agentv1.ConversationStateStructure{
			RootPromptMessagesJson: [][]byte{rootID},
			Summary:                summaryID,
			SummaryArchive:         archiveID,
			SummaryArchives:        [][]byte{{}, archiveID, archiveID},
		},
		blobs: []*agentv1.PreFetchedBlob{
			{Id: rootID, Value: rootRaw},
			{Id: msgUserID, Value: msgUser},
			{Id: msgToolID, Value: msgTool},
			{Id: msgSummaryID, Value: msgSummary},
			{Id: summaryID, Value: summaryBody},
			{Id: archiveID, Value: archiveBody},
			{Id: emptySummaryID, Value: emptySummaryBody},
		},
	}
}

func (graph testImportedSummaryGraph) blobsWithout(id []byte) []*agentv1.PreFetchedBlob {
	filtered := make([]*agentv1.PreFetchedBlob, 0, len(graph.blobs))
	for _, blob := range graph.blobs {
		if string(blob.Id) == string(id) {
			continue
		}
		filtered = append(filtered, blob)
	}
	return filtered
}

func importedRuntimePayload(t *testing.T, entries []HistoryEntry) runtimeStateEntryPayload {
	t.Helper()
	var found runtimeStateEntryPayload
	count := 0
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "runtime_state" {
			continue
		}
		count++
		if err := json.Unmarshal(entry.Payload, &found); err != nil {
			t.Fatalf("runtime_state 解码失败: %v", err)
		}
	}
	if count != 1 {
		t.Fatalf("runtime_state 数量=%d，预期 1（旧实现语义 RED）", count)
	}
	return found
}

func TestImportedConversationStatePersistsNativeSummaryGraph(t *testing.T) {
	graph := newTestImportedSummaryGraph(t)
	historyRoot := t.TempDir()
	blobStore := NewContentBlobStore(historyRoot)
	fileStore := NewConversationFileStore(historyRoot)
	conversation, err := newRuntimeConversation("persist-native-summary", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{contentBlobs: blobStore}).importConversationState(conversation, graph.state, graph.blobs)
	if err != nil {
		t.Fatalf("完整摘要图导入失败（旧实现语义 RED）: %v", err)
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) == "compacted_summary" || strings.TrimSpace(entry.Kind) == "compaction_summary" {
			t.Fatal("原生摘要来源被写成 compacted_summary")
		}
	}
	payload := importedRuntimePayload(t, entries)
	if !bytes.Equal(payload.NativeSummary, graph.summaryID) || !bytes.Equal(payload.NativeSummaryArchive, graph.archiveID) {
		t.Fatalf("父图 ID 未按原文保全: summary=%x archive=%x", payload.NativeSummary, payload.NativeSummaryArchive)
	}
	if len(payload.NativeSummaryArchives) != 3 || len(payload.NativeSummaryArchives[0]) != 0 ||
		string(payload.NativeSummaryArchives[1]) != string(graph.archiveID) || string(payload.NativeSummaryArchives[2]) != string(graph.archiveID) {
		t.Fatalf("repeated archives 未保留空槽与重复: %x", payload.NativeSummaryArchives)
	}
	appendEntriesInPlace(conversation, entries)
	if _, err := fileStore.SaveConversationWithEntries(conversation.ConversationID, conversation, conversation.Entries); err != nil {
		t.Fatal(err)
	}
	reloaded := NewContentBlobStore(historyRoot)
	for _, want := range []struct {
		name string
		id   []byte
		body []byte
	}{
		{"summary", graph.summaryID, graph.summaryBody},
		{"archive", graph.archiveID, graph.archiveBody},
		{"archive-user", graph.msgUserID, graph.msgUser},
		{"archive-tool", graph.msgToolID, graph.msgTool},
		{"archive-summary-message", graph.msgSummaryID, graph.msgSummary},
	} {
		got, err := reloaded.Get(want.id)
		if err != nil {
			t.Fatalf("重启后缺少%s正文: %v", want.name, err)
		}
		if !bytes.Equal(got, want.body) {
			t.Fatalf("重启后%s正文失真", want.name)
		}
	}
	loaded, err := fileStore.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(loaded, reloaded)
	if err != nil {
		t.Fatalf("重启后 native 投影失败（旧实现语义 RED）: %v", err)
	}
	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		repo[string(blob.ID)] = blob.Data
	}
	if !bytes.Equal(projection.State.GetSummary(), graph.summaryID) || !bytes.Equal(repo[string(graph.summaryID)], graph.summaryBody) {
		t.Fatal("投影未覆盖 Summary 为原父图 ID/正文")
	}
	if !bytes.Equal(projection.State.GetSummaryArchive(), graph.archiveID) || !bytes.Equal(repo[string(graph.archiveID)], graph.archiveBody) {
		t.Fatal("投影未覆盖 SummaryArchive 为原父图 ID/正文")
	}
	archives := projection.State.GetSummaryArchives()
	if len(archives) != 3 || len(archives[0]) != 0 || string(archives[1]) != string(graph.archiveID) || string(archives[2]) != string(graph.archiveID) {
		t.Fatalf("投影 SummaryArchives 未整组覆盖: %x", archives)
	}
	decodedArchive := &agentv1.ConversationSummaryArchive{}
	if err := proto.Unmarshal(repo[string(graph.archiveID)], decodedArchive); err != nil {
		t.Fatal(err)
	}
	if decodedArchive.GetWindowTail() != 3 || decodedArchive.GetSummary() != "archive summary text" {
		t.Fatalf("archive summary/window_tail 被重算: %#v", decodedArchive)
	}
	if len(decodedArchive.GetSummarizedMessages()) != 4 || len(decodedArchive.GetSummarizedMessages()[0]) != 0 {
		t.Fatal("archive 空消息槽被丢掉")
	}
	replay, err := NewHistoryProjector().ProjectPromptReplay(loaded)
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range replay {
		if strings.Contains(message.Content, "archived-only-question") || strings.Contains(message.Content, "/synthetic/archived.bin") {
			t.Fatalf("归档正文泄入模型输入: %#v", replay)
		}
	}
	if len(replay) != 1 || replay[0].Content != "native root" {
		t.Fatalf("root 不是已保存原生消息: %#v", replay)
	}
}

func TestImportedConversationStatePersistsInlineNativeSummaryParents(t *testing.T) {
	graph := newTestImportedSummaryGraph(t)
	graph.state.Summary = graph.inlineSummary
	graph.state.SummaryArchive = graph.archiveBody
	conversation, err := newRuntimeConversation("inline-native-summary", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	historyRoot := t.TempDir()
	blobStore := NewContentBlobStore(historyRoot)
	entries, err := (&Service{contentBlobs: blobStore}).importConversationState(conversation, graph.state, graph.blobs)
	if err != nil {
		t.Fatalf("内联父节点导入失败（旧实现语义 RED）: %v", err)
	}
	payload := importedRuntimePayload(t, entries)
	wantSummaryID := digestTestBytes(graph.inlineSummary)
	wantArchiveID := graph.archiveID
	if !bytes.Equal(payload.NativeSummary, wantSummaryID) || !bytes.Equal(payload.NativeSummaryArchive, wantArchiveID) {
		t.Fatalf("内联父未转成原字节内容 ID: summary=%x archive=%x", payload.NativeSummary, payload.NativeSummaryArchive)
	}
	got, err := blobStore.Get(wantSummaryID)
	if err != nil || !bytes.Equal(got, graph.inlineSummary) {
		t.Fatalf("内联 summary 原文未 Put: %v", err)
	}
}

func TestImportedConversationStatePreservesEmptySummaryStringNode(t *testing.T) {
	graph := newTestImportedSummaryGraph(t)
	graph.state.Summary = graph.emptySummaryID
	graph.state.SummaryArchive = nil
	graph.state.SummaryArchives = nil
	conversation, err := newRuntimeConversation("empty-summary-node", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	blobStore := NewContentBlobStore(t.TempDir())
	entries, err := (&Service{contentBlobs: blobStore}).importConversationState(conversation, graph.state, graph.blobs)
	if err != nil {
		t.Fatalf("空摘要字符串节点被拒绝或删除（旧实现语义 RED）: %v", err)
	}
	payload := importedRuntimePayload(t, entries)
	if !bytes.Equal(payload.NativeSummary, graph.emptySummaryID) {
		t.Fatal("空摘要字符串未保留父节点 ID")
	}
	got, err := blobStore.Get(graph.emptySummaryID)
	if err != nil || !bytes.Equal(got, graph.emptySummaryBody) {
		t.Fatal("空摘要字符串父节点正文被静默删除")
	}
}

func TestImportedConversationStateRejectsNativeSummaryViolations(t *testing.T) {
	for _, test := range []struct {
		name      string
		change    func(*testImportedSummaryGraph)
		wantError string
		store     func() *ContentBlobStore
	}{
		{
			name: "missing-archive-child",
			change: func(graph *testImportedSummaryGraph) {
				graph.blobs = graph.blobsWithout(graph.msgToolID)
			},
			wantError: "missing prefetched archive core message blob",
		},
		{
			name: "inline-archive-child",
			change: func(graph *testImportedSummaryGraph) {
				graph.state.SummaryArchive = marshalTestProto(t, &agentv1.ConversationSummaryArchive{
					SummarizedMessages: [][]byte{graph.msgUser},
				})
				graph.state.SummaryArchives = nil
			},
			wantError: "inline payload",
		},
		{
			name: "wrong-archive-schema",
			change: func(graph *testImportedSummaryGraph) {
				graph.state.SummaryArchive = graph.summaryBody
				graph.state.SummaryArchives = nil
			},
			wantError: "inline payload",
		},
		{
			name: "unknown-field-summary",
			change: func(graph *testImportedSummaryGraph) {
				body := append(append([]byte(nil), graph.summaryBody...), 0x78, 0x01)
				graph.state.Summary = digestTestBytes(body)
				graph.blobs = append(graph.blobs, &agentv1.PreFetchedBlob{Id: graph.state.Summary, Value: body})
				graph.state.SummaryArchive = nil
				graph.state.SummaryArchives = nil
			},
			wantError: "unknown field",
		},
		{
			name: "ambiguous-32-summary",
			change: func(graph *testImportedSummaryGraph) {
				inline := marshalTestProto(t, &agentv1.ConversationSummary{Summary: strings.Repeat("s", 30)})
				if len(inline) != sha256.Size {
					t.Fatalf("构造失败: summary 内联长度=%d，要恰好32", len(inline))
				}
				graph.state.Summary = inline
				graph.state.SummaryArchive = nil
				graph.state.SummaryArchives = nil
			},
			wantError: "missing prefetched summary blob",
		},
		{
			name:      "put-failure",
			change:    func(graph *testImportedSummaryGraph) {},
			wantError: "content blob store is not initialized",
			store:     func() *ContentBlobStore { return &ContentBlobStore{} },
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			graph := newTestImportedSummaryGraph(t)
			test.change(&graph)
			conversation := preserveImportConversation(t)
			before := snapshotConversation(t, conversation)
			service := newImportServiceWithBlobStore(t)
			if test.store != nil {
				service = &Service{contentBlobs: test.store()}
			}
			entries, err := service.importConversationState(conversation, graph.state, graph.blobs)
			if err == nil || entries != nil {
				t.Fatalf("%s 被接受（旧实现语义 RED）", test.name)
			}
			if !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("错误分类不符: got=%v want-contains=%q", err, test.wantError)
			}
			assertConversationUnchanged(t, before, conversation)
		})
	}
}

func TestImportedConversationStateDoesNotBindNativeSummaryOnInlineRoot(t *testing.T) {
	graph := newTestImportedSummaryGraph(t)
	graph.state.RootPromptMessagesJson = [][]byte{[]byte(`{"role":"user","content":"inline root"}`)}
	conversation, err := newRuntimeConversation("inline-root-summary", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{}).importConversationState(conversation, graph.state, graph.blobs)
	if err != nil {
		t.Fatalf("旧内联 root 导入失败: %v", err)
	}
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "runtime_state" {
			continue
		}
		var payload runtimeStateEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload.NativeSummary) > 0 || len(payload.NativeSummaryArchive) > 0 || len(payload.NativeSummaryArchives) > 0 {
			t.Fatal("旧内联 root 绑定了 native_* 来源")
		}
	}
}

func TestImportedNativeSummaryMissingArchiveChildStopsBidiAppendWithoutWrites(t *testing.T) {
	graph := newTestImportedSummaryGraph(t)
	assertImportedHistoryFailureThroughBidiAppend(t, graph.state, graph.blobsWithout(graph.msgUserID), "missing prefetched archive core message blob")
}

func TestNativeCheckpointProjectionAppendsLocalArchiveAfterImportedGraph(t *testing.T) {
	graph := newTestImportedSummaryGraph(t)
	blobStore := NewContentBlobStore(t.TempDir())
	conversation, err := newRuntimeConversation("mix-imported-local", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{contentBlobs: blobStore}).importConversationState(conversation, graph.state, graph.blobs)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	appendEntriesInPlace(conversation, entries)
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 2, "request-2", "local current question", "message-2"),
	})
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil || plan == nil || plan.NativeArchiveSource == nil {
		t.Fatalf("本地追加压缩 plan error = %v %#v", err, plan)
	}
	if err := applyCompactionToConversation(conversation, newPendingCompaction(plan), "local mix summary"); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation, blobStore)
	if err != nil {
		t.Fatalf("混合 native writer error = %v", err)
	}
	after, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("混合写出改动了会话")
	}
	if !bytes.Equal(projection.State.GetSummaryArchive(), graph.archiveID) {
		t.Fatal("导入 singular 原 bytes 被改写")
	}
	archives := projection.State.GetSummaryArchives()
	if len(archives) != 4 || len(archives[0]) != 0 ||
		string(archives[1]) != string(graph.archiveID) || string(archives[2]) != string(graph.archiveID) {
		t.Fatalf("导入 repeated 前缀丢失: %x", archives)
	}
	if bytes.Equal(archives[3], graph.archiveID) {
		t.Fatal("本地 archive 未追加")
	}
	if bytes.Equal(projection.State.GetSummary(), graph.summaryID) {
		t.Fatal("State.Summary 未换成最新本地摘要")
	}
	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		repo[string(blob.ID)] = blob.Data
	}
	if !bytes.Equal(repo[string(graph.archiveID)], graph.archiveBody) {
		t.Fatal("导入 archive 正文被重编码")
	}
	if !bytes.Equal(repo[string(graph.msgUserID)], graph.msgUser) {
		t.Fatal("导入 archive 子节点正文被重编码")
	}
	for _, id := range projection.State.GetRootPromptMessagesJson() {
		if bytes.Contains(repo[string(id)], []byte("archived-only-question")) {
			t.Fatal("root 展开了外来 archive")
		}
	}
}
