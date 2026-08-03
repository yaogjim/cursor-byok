package forwarder

import (
	"reflect"
	"testing"

	"cursor/gen/agentv1"
	"cursor/internal/observability"
)

func TestClassifyDebugSemantics(t *testing.T) {
	provider := classifyDebugSemantics("provider", "llm_request", "started", "", nil)
	if provider.Capability != "provider" || provider.Operation != "provider.llm_request" || provider.Direction != observability.DirectionProxyToProvider || provider.Outcome != observability.OutcomeStarted || provider.Implementation != observability.ImplementationImplemented {
		t.Fatalf("unexpected provider semantics: %+v", provider)
	}

	tool := classifyDebugSemantics("runtime", "tool_result", "partial", "", nil)
	if tool.Capability != "tool" || tool.Operation != "agent.tool_result" || tool.Direction != observability.DirectionProxyInternal || tool.Outcome != observability.OutcomePartial || tool.Implementation != observability.ImplementationPartial {
		t.Fatalf("unexpected tool semantics: %+v", tool)
	}

	failed := classifyDebugSemantics("runsse", "terminal", "completed", "terminal_error", nil)
	if failed.Outcome != observability.OutcomeFailed || failed.Direction != observability.DirectionProxyToCursor {
		t.Fatalf("unexpected failed terminal semantics: %+v", failed)
	}
}

func TestWorkspacePathsFromIntent(t *testing.T) {
	intent := InboundIntent{RequestContext: &agentv1.RequestContext{Env: &agentv1.RequestContextEnv{
		WorkspacePaths: []string{"/workspace/a", "/workspace/b"},
		ProjectFolder:  "/workspace/a",
	}}}
	paths := workspacePathsFromIntent(intent)
	if want := []string{"/workspace/a", "/workspace/b"}; !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}

func TestDebugProjectPathsUsesOnlyExplicitWorkspaceKeys(t *testing.T) {
	paths := debugProjectPaths(
		map[string]any{
			"workspace_paths": []any{"/workspace/a", "/workspace/b"},
			"path":            "/must/not/be/used",
		},
		map[string]any{"project_folder": "/workspace/c"},
	)
	want := []string{"/workspace/a", "/workspace/b", "/workspace/c"}
	if !reflect.DeepEqual(paths, want) {
		t.Fatalf("paths = %#v, want %#v", paths, want)
	}
}
