package forwarder

import (
	"context"
	"testing"
	"time"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	modeladapter "cursor/internal/backend/agent/model"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
)

// TestBidiAppendAcceptsBinaryPayload 覆盖 Cursor 3.29.15 起的二进制上行：
// 客户端把 AgentClientMessage 放进 data_binary，data（hex 字符串）留空。
func TestBidiAppendAcceptsBinaryPayload(t *testing.T) {
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
	const requestID = "binary-payload-request"
	observed := false
	t.Cleanup(func() {
		stream, ok := service.broker.Get(requestID)
		if !ok || stream == nil {
			return
		}
		cancelBidiAppendRequestForCleanup(t, service, stream, requestID, "清理二进制载荷请求失败")
		if observed {
			select {
			case <-providerDone:
			case <-time.After(3 * time.Second):
				t.Error("模拟 provider 未结束")
			}
		}
		waitForBidiAppendActor(t, stream, "二进制载荷消息处理器未结束")
	})

	payload, err := proto.Marshal(&agentv1.AgentClientMessage{Message: &agentv1.AgentClientMessage_RunRequest{RunRequest: &agentv1.AgentRunRequest{
		ConversationId: stringPtr("binary-payload-conversation"),
		RequestedModel: &agentv1.RequestedModel{ModelId: "byok-test-model"},
		Action: &agentv1.ConversationAction{Action: &agentv1.ConversationAction_UserMessageAction{UserMessageAction: &agentv1.UserMessageAction{
			UserMessage: &agentv1.UserMessage{Text: "binary payload question", MessageId: "binary-payload-message"},
		}}},
	}}})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err = service.BidiAppend(ctx, connect.NewRequest(&aiserverv1.BidiAppendRequest{
		RequestId:   &aiserverv1.BidiRequestId{RequestId: requestID},
		AppendSeqno: 0,
		DataBinary:  payload,
	}))
	if err != nil {
		t.Fatalf("二进制载荷被 BidiAppend 拒绝: %v", err)
	}

	select {
	case req := <-requests:
		observed = true
		if req.ModelID != "byok-test-model" || req.RequestID != requestID {
			t.Fatalf("模型请求身份改变: model=%q request=%q", req.ModelID, req.RequestID)
		}
	case <-ctx.Done():
		t.Fatal("二进制载荷没有到达模拟模型适配器")
	}
}
