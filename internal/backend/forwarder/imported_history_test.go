package forwarder

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	entries, err := (&Service{}).importConversationState(conversation, state, blobs)
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
		stream, ok := service.broker.Get(requestID)
		if !ok {
			return
		}
		if err := service.dispatchInboundIntent(InboundIntent{Kind: "cancel", RequestID: requestID, CancelReason: "test cleanup"}); err != nil {
			t.Errorf("清理失败请求: %v", err)
		}
		stream.mu.Lock()
		done := stream.ActorDone
		stream.mu.Unlock()
		if done != nil {
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Error("失败请求的消息处理器未结束")
			}
		}
	})
	state, blobs := prefetchedNativeRootTestState(
		`{"role":"user","content":[{"type":"text","text":"valid prefix"}]}`,
		`{"role":"assistant","content":[{"type":"tool-call","toolCallId":"missing-result","toolName":"Read","args":{}}]}`,
	)
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
	if err == nil || connect.CodeOf(err) != connect.CodeInternal || !strings.Contains(err.Error(), "native tool calls missing results") {
		t.Fatalf("工具历史缺失没有从请求入口明确失败: %v", err)
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
		if !ok {
			return
		}
		if err := service.dispatchInboundIntent(InboundIntent{Kind: "cancel", RequestID: requestID, CancelReason: "test cleanup"}); err != nil {
			t.Errorf("清理运行请求失败: %v", err)
		}
		if observed {
			select {
			case <-providerDone:
			case <-time.After(3 * time.Second):
				t.Error("模拟 provider 未结束")
			}
		}
		stream.mu.Lock()
		actorDone := stream.ActorDone
		stream.mu.Unlock()
		if actorDone != nil {
			select {
			case <-actorDone:
			case <-time.After(3 * time.Second):
				t.Error("运行消息处理器未结束")
			}
		}
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
	entries, err := (&Service{}).importConversationState(conversation, state, prefetched)
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
	entries, err := (&Service{}).importConversationState(conversation, state, prefetched)
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
