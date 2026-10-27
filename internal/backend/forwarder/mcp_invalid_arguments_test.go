package forwarder

import (
	"encoding/json"
	"strings"
	"testing"

	"cursor/gen/agentv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestMCPInvalidArgumentsFailBeforeDispatch(t *testing.T) {
	for _, tool := range []string{"cleanup", "CallMcpTool"} {
		for _, raw := range []string{`{"secret-marker":`, `["secret-marker"]`, `12`, `true`, `"secret-marker"`} {
			t.Run(tool+raw, func(t *testing.T) {
				service, stream, _ := testCheckpointBlobProjection(t)
				service.execBridge = execbridge.NewBridge()
				stream.CurrentProviderToken = 1
				stream.CurrentModelCallID = "model-call-1"
				stream.ProviderActive = true
				stream.Status = StreamStatusStreaming
				stream.Phase = TurnPhaseProviderRunning
				stream.Mode = agentv1.AgentMode_AGENT_MODE_AGENT
				stream.MCPToolServers = map[string]string{"cleanup": "server"}
				args := raw
				if tool == "CallMcpTool" {
					// 损坏对象使用字符串形式，确保测试的是嵌套参数，而非外层 JSON。
					if strings.HasSuffix(raw, ":") {
						args = `"{\"secret-marker\":"`
					}
					args = `{"server":"server","toolName":"cleanup","arguments":` + args + `}`
				}
				err := service.handleToolInvocation(stream, runtimecore.ToolInvocation{CallID: "call-invalid", ToolName: tool, ArgsJSON: []byte(args), ModelCallID: "model-call-1"})
				if err != nil {
					t.Fatal(err)
				}
				conversation, _, _, err := service.snapshotCheckpointConversation(stream)
				if err != nil {
					t.Fatal(err)
				}
				results := 0
				for _, entry := range conversation.Entries {
					if entry.Kind == "tool_result" && entry.ToolCallID == "call-invalid" {
						results++
						var result struct {
							Text string `json:"result_text"`
						}
						if err := json.Unmarshal(entry.Payload, &result); err != nil {
							t.Fatal(err)
						}
						if !strings.Contains(result.Text, "invalid MCP") || strings.Contains(result.Text, "secret-marker") {
							t.Fatalf("错误结果不正确或泄漏参数: %s", entry.Payload)
						}
					}
				}
				completed := 0
				for _, event := range readCheckpointTestEvents(t, service, stream) {
					if event.Message.GetExecServerMessage() != nil {
						t.Fatal("非法参数产生了执行消息")
					}
					if update := event.Message.GetInteractionUpdate(); update != nil {
						if done := update.GetToolCallCompleted(); done != nil && done.GetCallId() == "call-invalid" {
							completed++
						}
					}
				}
				if results != 1 || completed != 1 {
					t.Fatalf("失败收口次数: results=%d completed=%d", results, completed)
				}
				if len(stream.PendingExecs) != 0 || stream.Status == StreamStatusFailed {
					t.Fatal("非法参数留下 pending 或终止会话")
				}
				corrected := `{"scope":"only"}`
				if tool == "CallMcpTool" {
					corrected = `{"server":"server","toolName":"cleanup","arguments":{"scope":"only"}}`
				}
				if err := service.handleToolInvocation(stream, runtimecore.ToolInvocation{CallID: "call-corrected", ToolName: tool, ArgsJSON: []byte(corrected), ModelCallID: "model-call-1"}); err != nil {
					t.Fatal(err)
				}
				dispatched := 0
				for _, event := range readCheckpointTestEvents(t, service, stream) {
					if exec := event.Message.GetExecServerMessage(); exec != nil && exec.GetMcpArgs() != nil && exec.GetMcpArgs().GetToolCallId() == "call-corrected" {
						dispatched++
					}
				}
				if dispatched != 1 || len(stream.PendingExecs) != 1 {
					t.Fatalf("纠正参数后未正常派发: exec=%d pending=%d", dispatched, len(stream.PendingExecs))
				}
			})
		}
	}
}
