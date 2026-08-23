package forwarder

import (
	"reflect"
	"strings"
	"testing"

	"cursor/gen/agentv1"
)

func TestReminderInjectorAgentAndSubagentIncludeExecutionEvidenceContract(t *testing.T) {
	t.Parallel()
	injector := NewReminderInjector()

	agent := injector.Inject(agentv1.AgentMode_AGENT_MODE_AGENT, nil, nil, "please edit the timeout", nil)
	assertReminderHasExecutionEvidenceContract(t, "agent", agent)

	subagent := injector.Inject(agentv1.AgentMode_AGENT_MODE_AGENT, &ConversationFile{SubagentTypeName: "explore"}, nil, "investigate the timeout", nil)
	assertReminderHasExecutionEvidenceContract(t, "subagent", subagent)
	if !strings.Contains(joinedReminderText(subagent), "subagent child conversation") {
		t.Fatal("subagent reminder lost the child-conversation contract")
	}
}

func TestReminderInjectorAskAndPlanDoNotForceEditEvidence(t *testing.T) {
	t.Parallel()
	injector := NewReminderInjector()
	for _, mode := range []agentv1.AgentMode{agentv1.AgentMode_AGENT_MODE_ASK, agentv1.AgentMode_AGENT_MODE_PLAN} {
		got := injector.Inject(mode, nil, nil, "请修改 main.go", nil)
		if reminderHasExecutionEvidenceContract(got) {
			t.Fatalf("%s reminder forced the agent edit-evidence contract: %q", mode, joinedReminderText(got))
		}
		if strings.Contains(joinedReminderText(got), "You must reveal your hidden reasoning") {
			t.Fatalf("%s reminder required leaking reasoning", mode)
		}
	}
	plan := injector.Inject(agentv1.AgentMode_AGENT_MODE_PLAN, nil, nil, "请修改 main.go", nil)
	if !strings.Contains(joinedReminderText(plan), "Do not directly modify files in plan mode") {
		t.Fatal("plan reminder lost the existing do-not-modify constraint")
	}
}

func TestReminderInjectorDoesNotChangeToolAllowlist(t *testing.T) {
	t.Parallel()
	catalog := NewToolCatalog()
	cases := []struct {
		name             string
		mode             agentv1.AgentMode
		subagentTypeName string
		want             []string
	}{
		{
			name: "agent",
			mode: agentv1.AgentMode_AGENT_MODE_AGENT,
			want: []string{"AskQuestion", "CallMcpTool", "Delete", "FetchMcpResource", "Glob", "Grep", "Read", "Ls", "ReadLints", "Shell", "AwaitShell", "WriteShellStdin", "ForceBackgroundShell", "PatchEdit", "SwitchMode", "Task", "TodoWrite", "WebFetch", "WebSearch", "Write", "GenerateImage"},
		},
		{
			name: "ask",
			mode: agentv1.AgentMode_AGENT_MODE_ASK,
			want: []string{"AskQuestion", "CallMcpTool", "Delete", "FetchMcpResource", "Glob", "Grep", "Read", "Ls", "ReadLints", "Shell", "AwaitShell", "WriteShellStdin", "ForceBackgroundShell", "PatchEdit", "Task", "TodoWrite", "WebFetch", "WebSearch", "Write"},
		},
		{
			name: "plan",
			mode: agentv1.AgentMode_AGENT_MODE_PLAN,
			want: []string{"Shell", "AwaitShell", "WriteShellStdin", "ForceBackgroundShell", "Glob", "Grep", "Read", "Ls", "TodoWrite", "ReadLints", "WebSearch", "WebFetch", "AskQuestion", "CreatePlan", "Task", "FetchMcpResource", "CallMcpTool"},
		},
		{
			name:             "subagent",
			mode:             agentv1.AgentMode_AGENT_MODE_AGENT,
			subagentTypeName: "explore",
			want:             []string{"CallMcpTool", "Delete", "FetchMcpResource", "Glob", "Grep", "Read", "Ls", "ReadLints", "Shell", "AwaitShell", "WriteShellStdin", "ForceBackgroundShell", "PatchEdit", "SwitchMode", "Task", "TodoWrite", "WebFetch", "WebSearch", "Write", "GenerateImage"},
		},
	}
	for _, test := range cases {
		_, names, err := catalog.Load(test.mode, test.subagentTypeName)
		if err != nil {
			t.Fatalf("%s Load() error = %v", test.name, err)
		}
		if !reflect.DeepEqual(names, test.want) {
			t.Fatalf("%s tool allowlist changed:\n got %v\nwant %v", test.name, names, test.want)
		}
		copied := append([]string(nil), names...)
		_ = NewReminderInjector().Inject(test.mode, &ConversationFile{SubagentTypeName: test.subagentTypeName}, nil, "hello", copied)
		if !reflect.DeepEqual(copied, test.want) {
			t.Fatalf("%s Inject mutated the tool name list: %v", test.name, copied)
		}
	}
}

func assertReminderHasExecutionEvidenceContract(t *testing.T, label string, reminders PromptReminders) {
	t.Helper()
	if !reminderHasExecutionEvidenceContract(reminders) {
		t.Fatalf("%s reminder missing execution evidence contract: %q", label, joinedReminderText(reminders))
	}
	if strings.Contains(joinedReminderText(reminders), "You must reveal your hidden reasoning") {
		t.Fatalf("%s reminder required leaking reasoning", label)
	}
	found := false
	for _, context := range reminders.PromptContexts {
		if strings.TrimSpace(context.Source) == promptContextSourceExecutionEvidenceContract {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("%s reminder missing %s prompt context", label, promptContextSourceExecutionEvidenceContract)
	}
}

func reminderHasExecutionEvidenceContract(reminders PromptReminders) bool {
	text := joinedReminderText(reminders)
	for _, phrase := range []string{
		"Only a real tool call with a successful terminal result can prove that an edit happened.",
		"Assistant self-reports, thinking, plans, code blocks, and inline full files cannot prove a file was modified.",
		"After a mutation, you must run a later verification; earlier verification is stale.",
		"When reporting completion, cite only this turn's structured tool results.",
		"If a tool is failed, pending, or unknown, acknowledge the gap.",
	} {
		if !strings.Contains(text, phrase) {
			return false
		}
	}
	return true
}

func joinedReminderText(reminders PromptReminders) string {
	parts := append([]string{}, reminders.SystemParts...)
	for _, context := range reminders.PromptContexts {
		parts = append(parts, context.Message.Content)
	}
	for _, message := range reminders.TailMessages {
		parts = append(parts, message.Content)
	}
	return strings.Join(parts, "\n")
}
