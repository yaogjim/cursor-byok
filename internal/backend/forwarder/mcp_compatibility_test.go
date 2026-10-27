package forwarder

import (
	"testing"

	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestDirectMCPPreservesEmptyAndObjectArguments(t *testing.T) {
	for _, raw := range []string{"", "null", "{}", `{"scope":"only"}`} {
		t.Run(raw, func(t *testing.T) {
			service, stream, _ := testCheckpointBlobProjection(t)
			service.execBridge = execbridge.NewBridge()
			stream.MCPToolServers = map[string]string{"cleanup": "server"}
			if err := service.handleToolInvocation(stream, runtimecore.ToolInvocation{CallID: "valid", ToolName: "cleanup", ArgsJSON: []byte(raw)}); err != nil {
				t.Fatal(err)
			}
			dispatched := 0
			for _, event := range readCheckpointTestEvents(t, service, stream) {
				if exec := event.Message.GetExecServerMessage(); exec != nil && exec.GetMcpArgs() != nil {
					dispatched++
					args := exec.GetMcpArgs().GetArgs()
					if raw == `{"scope":"only"}` {
						if args["scope"].GetStringValue() != "only" {
							t.Fatal("对象参数没有透传")
						}
					} else if len(args) != 0 {
						t.Fatal("空参数合同改变")
					}
				}
			}
			if dispatched != 1 || len(stream.PendingExecs) != 1 {
				t.Fatalf("合法调用未派发: %d", dispatched)
			}
		})
	}
}
