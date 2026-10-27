package forwarder

import (
	"context"
	"strings"
	"testing"
	"time"

	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
	modeladapter "cursor/internal/backend/agent/model"
)

// 验证 provider 结束及自动续接定时器；不手工注入第二次调用，stop 也必须回放错误。
func TestDirectMCPInvalidArgumentsAutomaticallyResumeProvider(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	service, stream, _ := testCheckpointBlobProjection(t)
	service.execBridge = execbridge.NewBridge()
	service.compiler = NewPromptCompiler(service.projector, NewToolCatalog(), NewReminderInjector(), nil)
	service.usageStore = NewUsageFileStore(t.TempDir())
	secondRequest := make(chan ProviderRequest, 1)
	provider := &overflowRecoveryTestProvider{}
	provider.handler = func(_ context.Context, req ProviderRequest, sink func(modeladapter.ModelEvent) error) error {
		callID, args := "invalid-direct", `[]`
		if provider.requestCount() == 2 {
			secondRequest <- req
			callID, args = "corrected-direct", `{"scope":"only"}`
		}
		if err := sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindToolLikeCompleted, ToolInvocation: &runtimecore.ToolInvocation{CallID: callID, ToolName: "cleanup", ArgsJSON: []byte(args)}}); err != nil {
			return err
		}
		return sink(modeladapter.ModelEvent{Kind: modeladapter.ModelEventKindTurnFinished, FinishReason: "stop"})
	}
	service.provider = provider
	stream.ProviderActive = false
	stream.MCPToolServers = map[string]string{"cleanup": "server"}
	t.Cleanup(func() {
		_ = service.postStreamCommandWait(stream, streamCommand{Kind: streamCommandCancel, Intent: InboundIntent{Kind: "cancel", RequestID: stream.RequestID, CancelReason: "test cleanup"}})
		waitForBidiAppendActor(t, stream, "MCP resume cleanup")
	})
	if err := service.requestProviderAction(stream, providerActionStart); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		acknowledgeCheckpointBlobs(t, service, stream)
		stream.mu.Lock()
		status := stream.Status
		var corrected runtimecore.PendingExec
		for _, pending := range stream.PendingExecs {
			if pending.ToolCallID == "corrected-direct" {
				corrected = pending
			}
		}
		stream.mu.Unlock()
		if corrected.ExecID != "" {
			// pending 先于事件发布写入，必须等待可观察的派发事件，而非把中间态当失败。
			published := false
			for _, event := range readCheckpointTestEvents(t, service, stream) {
				if exec := event.Message.GetExecServerMessage(); exec != nil && exec.GetMcpArgs() != nil && exec.GetMcpArgs().GetToolCallId() == "corrected-direct" {
					published = true
				}
			}
			if !published {
				time.Sleep(5 * time.Millisecond)
				continue
			}
			if provider.requestCount() != 2 {
				t.Fatalf("provider calls=%d, want 2", provider.requestCount())
			}
			req := <-secondRequest
			found := false
			for _, message := range req.Messages {
				if message.Role == "tool" && message.ToolCallID == "invalid-direct" && strings.Contains(message.Content, "invalid MCP arguments") {
					found = true
				}
			}
			if !found {
				t.Fatalf("second provider request missing failed tool result: %#v", req.Messages)
			}
			dispatched := 0
			for _, event := range readCheckpointTestEvents(t, service, stream) {
				if exec := event.Message.GetExecServerMessage(); exec != nil && exec.GetMcpArgs() != nil {
					if exec.GetMcpArgs().GetToolCallId() == "invalid-direct" {
						t.Fatal("invalid direct invocation dispatched")
					}
					if exec.GetMcpArgs().GetToolCallId() == "corrected-direct" {
						dispatched++
						if exec.GetMcpArgs().GetArgs()["scope"].GetStringValue() != "only" {
							t.Fatal("corrected arguments lost")
						}
					}
				}
			}
			if dispatched != 1 {
				t.Fatalf("corrected dispatches=%d, want 1", dispatched)
			}
			return
		}
		if isTerminalStreamStatus(status) {
			t.Fatalf("provider did not automatically resume after invalid direct MCP: status=%s calls=%d", status, provider.requestCount())
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("provider did not automatically resume and dispatch corrected MCP; calls=%d", provider.requestCount())
}
