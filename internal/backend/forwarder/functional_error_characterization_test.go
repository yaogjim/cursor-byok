package forwarder

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"connectrpc.com/connect"

	"cursor/gen/agentv1"
	"cursor/gen/aiserverv1"
	execbridge "cursor/internal/backend/agent/bridge/exec"
	runtimecore "cursor/internal/backend/agent/core"
)

func TestRepositoryHandlersPersistSuccessfulSyncWithoutFileData(t *testing.T) {
	root := t.TempDir()
	store := NewCodebaseIndexStore(root)
	service := &Service{codebaseIndexStore: store}
	repository := &aiserverv1.RepositoryInfo{
		RelativeWorkspacePath: "synthetic-repository",
		RepoName:              "synthetic",
		RepoOwner:             "test",
	}

	handshake, err := service.FastRepoInitHandshakeV2(context.Background(), connect.NewRequest(&aiserverv1.FastRepoInitHandshakeV2Request{
		Repository: repository,
		RootHash:   "synthetic-root-hash",
	}))
	if err != nil {
		t.Fatalf("FastRepoInitHandshakeV2() error = %v", err)
	}
	if got := handshake.Msg.GetStatus(); got != aiserverv1.FastRepoInitHandshakeV2Response_STATUS_SUCCESS {
		t.Fatalf("FastRepoInitHandshakeV2() status = %v, want success", got)
	}
	codebaseID := handshake.Msg.GetCodebases()[0].GetCodebaseId()

	update, err := service.FastUpdateFileV2(context.Background(), connect.NewRequest(&aiserverv1.FastUpdateFileV2Request{
		CodebaseId: codebaseID,
		FileUpdates: []*aiserverv1.FastUpdateFileV2Request_FileUpdate{
			{
				PartialPath: &aiserverv1.FastUpdateFileV2Request_FileUpdate_LocalFile{
					LocalFile: &aiserverv1.FastUpdateFileV2Request_LocalFile{
						Hash:                             "synthetic-file-hash",
						UnencryptedRelativeWorkspacePath: "synthetic.txt",
					},
				},
			},
		},
	}))
	if err != nil {
		t.Fatalf("FastUpdateFileV2() error = %v", err)
	}
	if got := update.Msg.GetStatus(); got != aiserverv1.FastUpdateFileV2Response_STATUS_SUCCESS {
		t.Fatalf("FastUpdateFileV2() status = %v, want success", got)
	}

	reloaded := NewCodebaseIndexStore(root)
	files, err := reloaded.ListFiles(codebaseID)
	if err != nil {
		t.Fatalf("ListFiles() error = %v", err)
	}
	if len(files) != 0 {
		t.Fatalf("ListFiles() count = %d, want 0 because the successful update is not persisted", len(files))
	}
}

func TestRepositoryStatusReportsUnseenRepositoryAsSynced(t *testing.T) {
	service := &Service{codebaseIndexStore: NewCodebaseIndexStore(t.TempDir())}

	response, err := service.RepositoryStatus(context.Background(), connect.NewRequest(&aiserverv1.RepositoryStatusRequest{
		Repository: &aiserverv1.RepositoryInfo{
			RelativeWorkspacePath: "never-indexed",
			RepoName:              "never-indexed",
		},
	}))
	if err != nil {
		t.Fatalf("RepositoryStatus() error = %v", err)
	}
	if response.Msg.GetSynced() == nil {
		t.Fatalf("RepositoryStatus() status = %T, want synced for current local behavior", response.Msg.GetStatus())
	}
}

func TestUploadDocumentationPersistsSuccessWithoutQueryableContent(t *testing.T) {
	root := t.TempDir()
	service := &Service{docsIndexStore: NewDocsIndexStore(root)}
	identifier := "https://synthetic.invalid/docs"

	upload, err := service.UploadDocumentation(context.Background(), connect.NewRequest(&aiserverv1.UploadDocumentationRequest{
		DocIdentifier: identifier,
	}))
	if err != nil {
		t.Fatalf("UploadDocumentation() error = %v", err)
	}
	if got := upload.Msg.GetStatus(); got != aiserverv1.UploadResponse_STATUS_SUCCESS {
		t.Fatalf("UploadDocumentation() status = %v, want success", got)
	}
	if got := upload.Msg.GetProgress(); got != 1 {
		t.Fatalf("UploadDocumentation() progress = %v, want 1", got)
	}

	reloaded := NewDocsIndexStore(root)
	record, ok, err := reloaded.Get(identifier)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() did not find the successfully uploaded document")
	}
	if record.Content != "" {
		t.Fatalf("persisted content length = %d, want 0", len(record.Content))
	}

	queryService := &Service{docsIndexStore: reloaded}
	query, err := queryService.DocumentationQuery(context.Background(), connect.NewRequest(&aiserverv1.DocumentationQueryRequest{
		DocIdentifier: identifier,
		TopK:          5,
	}))
	if err != nil {
		t.Fatalf("DocumentationQuery() error = %v", err)
	}
	if got := query.Msg.GetStatus(); got != aiserverv1.DocumentationQueryResponse_STATUS_SUCCESS {
		t.Fatalf("DocumentationQuery() status = %v, want success", got)
	}
	if got := len(query.Msg.GetDocChunks()); got != 0 {
		t.Fatalf("DocumentationQuery() chunk count = %d, want 0", got)
	}

	status, err := queryService.UploadedStatus(context.Background(), connect.NewRequest(&aiserverv1.UploadedStatusRequest{
		DocIdentifier: identifier,
	}))
	if err != nil {
		t.Fatalf("UploadedStatus() error = %v", err)
	}
	if got := status.Msg.GetStatus(); got != aiserverv1.UploadedStatus_STATUS_SUCCEEDED {
		t.Fatalf("UploadedStatus() status = %v, want succeeded", got)
	}
}

func TestAvailableDocsPersistsUnknownIdentifierAsIndexed(t *testing.T) {
	root := t.TempDir()
	service := &Service{docsIndexStore: NewDocsIndexStore(root)}
	identifier := "synthetic-missing-doc"

	available, err := service.AvailableDocs(context.Background(), connect.NewRequest(&aiserverv1.AvailableDocsRequest{
		AdditionalDocIdentifiers: []string{identifier},
	}))
	if err != nil {
		t.Fatalf("AvailableDocs() error = %v", err)
	}
	if got := len(available.Msg.GetDocs()); got != 1 {
		t.Fatalf("AvailableDocs() count = %d, want 1", got)
	}

	reloaded := NewDocsIndexStore(root)
	record, ok, err := reloaded.Get(identifier)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if !ok {
		t.Fatal("Get() did not find the identifier persisted by AvailableDocs")
	}
	if record.Status != docsIndexStatusIndexed {
		t.Fatalf("persisted status = %q, want %q", record.Status, docsIndexStatusIndexed)
	}
	if record.Content != "" {
		t.Fatalf("persisted content length = %d, want 0", len(record.Content))
	}

	query, err := (&Service{docsIndexStore: reloaded}).DocumentationQuery(context.Background(), connect.NewRequest(&aiserverv1.DocumentationQueryRequest{
		DocIdentifier: identifier,
	}))
	if err != nil {
		t.Fatalf("DocumentationQuery() error = %v", err)
	}
	if got := query.Msg.GetStatus(); got != aiserverv1.DocumentationQueryResponse_STATUS_SUCCESS {
		t.Fatalf("DocumentationQuery() status = %v, want success", got)
	}
	if got := len(query.Msg.GetDocChunks()); got != 0 {
		t.Fatalf("DocumentationQuery() chunk count = %d, want 0", got)
	}
}

func TestKnowledgeBaseAddAcknowledgesDocsIndexPersistenceFailure(t *testing.T) {
	root := t.TempDir()
	rulesRoot := filepath.Join(root, "rules")
	blockedDocsRoot := filepath.Join(root, "docs-index-file")
	if err := os.WriteFile(blockedDocsRoot, []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	service := &Service{
		rules:          NewUserRuleStore(rulesRoot),
		docsIndexStore: NewDocsIndexStore(blockedDocsRoot),
	}

	response, err := service.KnowledgeBaseAdd(context.Background(), connect.NewRequest(&aiserverv1.KnowledgeBaseAddRequest{
		Knowledge: "synthetic knowledge",
		Title:     "synthetic title",
	}))
	if err != nil {
		t.Fatalf("KnowledgeBaseAdd() error = %v", err)
	}
	if !response.Msg.GetSuccess() {
		t.Fatal("KnowledgeBaseAdd() success = false, want true for current local behavior")
	}

	rules, err := service.rules.List()
	if err != nil {
		t.Fatalf("rules.List() error = %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("rules.List() count = %d, want 1", len(rules))
	}
	if _, ok, err := service.docsIndexStore.Get(response.Msg.GetId()); err == nil || ok {
		t.Fatalf("docsIndexStore.Get() = (_, %v, %v), want persistence error", ok, err)
	}
}

func TestExposedToolCatalogHasDispatchPath(t *testing.T) {
	tests := []struct {
		name         string
		mode         agentv1.AgentMode
		subagentType string
	}{
		{name: "agent", mode: agentv1.AgentMode_AGENT_MODE_AGENT},
		{name: "ask", mode: agentv1.AgentMode_AGENT_MODE_ASK},
		{name: "plan", mode: agentv1.AgentMode_AGENT_MODE_PLAN},
		{name: "debug", mode: agentv1.AgentMode_AGENT_MODE_DEBUG},
		{name: "multitask", mode: agentv1.AgentMode_AGENT_MODE_MULTITASK},
		{name: "child", mode: agentv1.AgentMode_AGENT_MODE_AGENT, subagentType: "explore"},
	}

	catalog := NewToolCatalog()
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, names, err := catalog.Load(test.mode, test.subagentType)
			if err != nil {
				t.Fatalf("catalog.Load() error = %v", err)
			}
			if len(names) == 0 {
				t.Fatal("catalog.Load() exposed no tools")
			}
			for _, name := range names {
				if !hasToolDispatchPath(name) {
					t.Errorf("exposed tool %q has no dispatch path", name)
				}
			}
		})
	}
}

func hasToolDispatchPath(name string) bool {
	return isPatchEditToolName(name) ||
		name == "Write" ||
		isExecTool(name) ||
		isInteractionTool(name) ||
		isLocalStateTool(name) ||
		isImmediateNativeTool(name)
}

func TestForceBackgroundShellResultReplaysWithoutReasoning(t *testing.T) {
	catalog := NewToolCatalog()
	_, names, err := catalog.Load(agentv1.AgentMode_AGENT_MODE_AGENT, "")
	if err != nil {
		t.Fatalf("catalog.Load() error = %v", err)
	}
	if !containsString(names, "ForceBackgroundShell") {
		t.Fatal("agent tool catalog does not expose ForceBackgroundShell")
	}

	invocation := runtimecore.ToolInvocation{
		CallID:   "force-background-call",
		ToolName: "ForceBackgroundShell",
		ArgsJSON: []byte(`{"tool_call_id":"running-shell-call"}`),
	}
	if started := buildStartedToolCall(invocation); started != nil {
		t.Fatalf("buildStartedToolCall() = %T, want nil for current protocol shape", started.GetTool())
	}

	bridge := execbridge.NewBridge()
	_, pending, err := bridge.OpenExec(execbridge.OpenExecContext{}, invocation)
	if err != nil {
		t.Fatalf("OpenExec() error = %v", err)
	}
	result, err := bridge.ApplyExecClientMessage(&agentv1.ExecClientMessage{
		Message: &agentv1.ExecClientMessage_ForceBackgroundShellResult{
			ForceBackgroundShellResult: &agentv1.ForceBackgroundShellResult{
				Status: agentv1.ForceBackgroundShellStatus_FORCE_BACKGROUND_SHELL_STATUS_ACCEPTED,
			},
		},
	}, pending)
	if err != nil {
		t.Fatalf("ApplyExecClientMessage() error = %v", err)
	}
	if !result.IsTerminal || result.ToolResultPayload == "" {
		t.Fatalf("ApplyExecClientMessage() terminal = %v payload = %q", result.IsTerminal, result.ToolResultPayload)
	}
	if result.ToolCall != nil {
		t.Fatalf("ApplyExecClientMessage() tool call = %T, want nil for current protocol shape", result.ToolCall.GetTool())
	}

	conversation := &ConversationFile{Entries: []HistoryEntry{
		newToolResultEntry(1, "request", invocation.CallID, invocation.ToolName, string(invocation.ArgsJSON), result.ToolResultPayload, "", nil),
	}}
	messages, err := NewHistoryProjector().ProjectPromptReplay(conversation)
	if err != nil {
		t.Fatalf("ProjectPromptReplay() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("ProjectPromptReplay() message count = %d, want assistant tool call and tool result", len(messages))
	}
	if got := messages[0].ToolCalls[0].Function.Name; got != invocation.ToolName {
		t.Fatalf("replayed tool name = %q, want %q", got, invocation.ToolName)
	}
	if got := messages[1].Content; got != result.ToolResultPayload {
		t.Fatalf("replayed result = %q, want %q", got, result.ToolResultPayload)
	}
}

func TestIsolatedToolResultWithoutReasoningOnlyReplaysForForceBackgroundShell(t *testing.T) {
	tests := []struct {
		toolName string
		want     int
	}{
		{toolName: "ForceBackgroundShell", want: 2},
		{toolName: "Shell", want: 0},
		{toolName: "PatchEdit", want: 0},
	}
	for _, test := range tests {
		t.Run(test.toolName, func(t *testing.T) {
			conversation := &ConversationFile{Entries: []HistoryEntry{
				newToolResultEntry(1, "request", "call-1", test.toolName, `{}`, `{"ok":true}`, "", nil),
			}}
			messages, err := NewHistoryProjector().ProjectPromptReplay(conversation)
			if err != nil {
				t.Fatalf("ProjectPromptReplay() error = %v", err)
			}
			if len(messages) != test.want {
				t.Fatalf("ProjectPromptReplay() message count = %d, want %d: %#v", len(messages), test.want, messages)
			}
		})
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
