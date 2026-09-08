package execbridge

import (
	"testing"

	runtimecore "cursor/internal/backend/agent/core"
)

func TestOpenExecAcceptsCanonicalShellAliases(t *testing.T) {
	argsJSON := []byte(`{"command":"echo alias-fixture"}`)
	for _, name := range []string{"Shell", "shell", "Bash", "bash"} {
		name := name
		t.Run(name, func(t *testing.T) {
			message, pending, err := NewBridge().OpenExec(OpenExecContext{}, runtimecore.ToolInvocation{
				CallID:   "call-alias-1",
				ToolName: name,
				ArgsJSON: argsJSON,
			})
			if err != nil {
				t.Fatalf("OpenExec(%q) error = %v", name, err)
			}
			if pending.ExecKind != "shell" {
				t.Fatalf("OpenExec(%q) exec kind = %q, want shell", name, pending.ExecKind)
			}
			if pending.ToolCallID != "call-alias-1" {
				t.Fatalf("OpenExec(%q) tool call id = %q, want call-alias-1", name, pending.ToolCallID)
			}
			if string(pending.ArgsJSON) != string(argsJSON) {
				t.Fatalf("OpenExec(%q) args changed: %s", name, pending.ArgsJSON)
			}
			if message.GetExecServerMessage().GetShellStreamArgs() == nil {
				t.Fatalf("OpenExec(%q) did not open a Shell stream", name)
			}
			if got := message.GetExecServerMessage().GetShellStreamArgs().GetToolCallId(); got != "call-alias-1" {
				t.Fatalf("OpenExec(%q) exec call id = %q", name, got)
			}
			if got := message.GetExecServerMessage().GetShellStreamArgs().GetCommand(); got != "echo alias-fixture" {
				t.Fatalf("OpenExec(%q) command = %q", name, got)
			}
		})
	}
}

func TestOpenExecLeavesNonShellNamesUnchanged(t *testing.T) {
	_, _, err := NewBridge().OpenExec(OpenExecContext{}, runtimecore.ToolInvocation{
		CallID:   "call-unknown",
		ToolName: "MysteryRemovedTool",
		ArgsJSON: []byte(`{}`),
	})
	if err == nil {
		t.Fatal("OpenExec(MysteryRemovedTool) succeeded, want unsupported exec tool")
	}
}
