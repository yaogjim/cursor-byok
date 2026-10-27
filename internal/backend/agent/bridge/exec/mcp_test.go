package execbridge

import (
	"reflect"
	"strings"
	"testing"

	runtimecore "cursor/internal/backend/agent/core"
)

func TestOpenMCPRejectsInvalidArgumentsWithoutPending(t *testing.T) {
	message, pending, err := NewBridge().OpenExec(OpenExecContext{}, runtimecore.ToolInvocation{CallID: "invalid", ToolName: "CallMcpTool", ArgsJSON: []byte(`{"server":"s","toolName":"cleanup","arguments":["secret-marker"]}`)})
	if err == nil || message != nil || !reflect.DeepEqual(pending, runtimecore.PendingExec{}) {
		t.Fatalf("非法参数未在派发前拒绝: message=%v pending=%+v err=%v", message, pending, err)
	}
	if strings.Contains(err.Error(), "secret-marker") {
		t.Fatal("错误泄漏原始参数")
	}
}
