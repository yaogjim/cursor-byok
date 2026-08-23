package forwarder

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
)

func TestProjectCursorTranscriptJSONLMatchesCursorContract(t *testing.T) {
	const reasoningCanary = "PRIVATE_REASONING_CANARY"
	toolCall := transcriptTestEditToolCall(t, "file.txt")
	conversation := transcriptTestConversation([]HistoryEntry{
		transcriptTestUserMessageEntry(t, 1, "request-1", "<user_info>hidden</user_info>\n\nchange the file"),
		newAssistantTextEntry(1, "request-1", "<thinking>hidden</thinking>\nDone", reasoningCanary, ""),
		newToolCallEntry(1, "request-1", "call-1", "Edit", reasoningCanary, "", toolCall),
		newToolResultEntry(1, "request-1", "call-1", "Edit", `{"path":"file.txt"}`, "edited", "", toolCall),
		newMetadataEntry(1, "request-1", "turn_completed", nil),
		transcriptTestUserMessageEntry(t, 2, "request-2", "next question"),
		newMetadataEntry(2, "request-2", "turn_completed", nil),
	})

	data, err := projectCursorTranscriptJSONL(conversation)
	if err != nil {
		t.Fatalf("projectCursorTranscriptJSONL() error = %v", err)
	}
	if strings.Contains(string(data), reasoningCanary) {
		t.Fatalf("public transcript leaked reasoning canary:\n%s", data)
	}
	lines := decodeCursorTranscriptLines(t, data)
	if len(lines) != 5 {
		t.Fatalf("transcript lines = %d, want 5\n%s", len(lines), data)
	}

	if lines[0].Role != "user" || transcriptLineText(lines[0]) != "change the file" {
		t.Fatalf("user line = %#v", lines[0])
	}
	if lines[1].Role != "assistant" || transcriptLineText(lines[1]) != "Done" {
		t.Fatalf("assistant line = %#v", lines[1])
	}
	if lines[2].Role != "assistant" || lines[2].Message == nil || len(lines[2].Message.Content) != 1 {
		t.Fatalf("tool line = %#v", lines[2])
	}
	toolUse := lines[2].Message.Content[0]
	if toolUse.Type != "tool_use" || toolUse.Name != "Edit" {
		t.Fatalf("tool use = %#v", toolUse)
	}
	input, ok := toolUse.Input.(map[string]any)
	if !ok || input["path"] != "file.txt" {
		t.Fatalf("tool input = %#v", toolUse.Input)
	}
	if lines[3].Type != "turn_ended" || lines[3].Status != "success" {
		t.Fatalf("turn status = %#v", lines[3])
	}
	if lines[4].Role != "user" || transcriptLineText(lines[4]) != "next question" {
		t.Fatalf("current user line = %#v", lines[4])
	}
}

func TestProjectCursorTranscriptMultipleToolCallsOmitShared15KReasoning(t *testing.T) {
	const (
		reasoningCanaryStart = "PRIVATE_SHARED_15K_REASONING_CANARY_START"
		reasoningCanaryEnd   = "PRIVATE_SHARED_15K_REASONING_CANARY_END"
	)
	reasoningCanary := reasoningCanaryStart + strings.Repeat("x", 15*1024) + reasoningCanaryEnd
	conversation := transcriptTestConversation([]HistoryEntry{
		newAssistantTextEntry(1, "request-1", "", reasoningCanary, ""),
		newToolCallEntry(1, "request-1", "call-1", "Edit", reasoningCanary, "", transcriptTestEditToolCall(t, "one.txt")),
		newToolCallEntry(1, "request-1", "call-2", "Edit", reasoningCanary, "", transcriptTestEditToolCall(t, "two.txt")),
		newToolCallEntry(1, "request-1", "call-3", "Edit", reasoningCanary, "", transcriptTestEditToolCall(t, "three.txt")),
	})

	data, err := projectCursorTranscriptJSONL(conversation)
	if err != nil {
		t.Fatalf("projectCursorTranscriptJSONL() error = %v", err)
	}
	for _, canary := range []string{reasoningCanaryStart, reasoningCanaryEnd} {
		if strings.Contains(string(data), canary) {
			t.Fatalf("public transcript leaked shared 15K reasoning canary %q:\n%s", canary, data)
		}
	}
	lines := decodeCursorTranscriptLines(t, data)
	if len(lines) != 3 {
		t.Fatalf("transcript lines = %d, want 3 tool calls\n%s", len(lines), data)
	}
	for index, line := range lines {
		if line.Message == nil || len(line.Message.Content) != 1 || line.Message.Content[0].Type != "tool_use" {
			t.Fatalf("tool line %d = %#v", index, line)
		}
	}
}

func TestProjectCursorTranscriptModelMessageOmitsReasoning(t *testing.T) {
	const reasoningCanary = "PRIVATE_IMPORTED_REASONING_CANARY"
	entry, ok, err := newModelMessageEntry(1, "request-1", modeladapter.Message{
		Role:             "assistant",
		Content:          "visible imported text",
		ReasoningContent: reasoningCanary,
	})
	if err != nil {
		t.Fatalf("newModelMessageEntry() error = %v", err)
	}
	if !ok {
		t.Fatal("newModelMessageEntry() omitted visible assistant message")
	}

	data, err := projectCursorTranscriptJSONL(transcriptTestConversation([]HistoryEntry{entry}))
	if err != nil {
		t.Fatalf("projectCursorTranscriptJSONL() error = %v", err)
	}
	if strings.Contains(string(data), reasoningCanary) {
		t.Fatalf("public transcript leaked imported reasoning canary:\n%s", data)
	}
	lines := decodeCursorTranscriptLines(t, data)
	if len(lines) != 1 || transcriptLineText(lines[0]) != "visible imported text" {
		t.Fatalf("imported assistant transcript = %s", data)
	}
}

func TestConversationFileStoreSyncsCursorTranscript(t *testing.T) {
	historyRoot := filepath.Join(t.TempDir(), "history")
	transcriptsFolder := transcriptTestFolder(t)
	store := NewConversationFileStore(historyRoot)
	conversation := transcriptTestConversation(nil)
	conversation.AgentTranscriptsFolder = transcriptsFolder

	persisted, err := store.SaveConversationWithEntries(conversation.ConversationID, conversation, []HistoryEntry{
		transcriptTestUserMessageEntry(t, 1, "request-1", "hello"),
		newAssistantTextEntry(1, "request-1", "hi", "", ""),
	})
	if err != nil {
		t.Fatalf("SaveConversationWithEntries() error = %v", err)
	}
	if persisted.AgentTranscriptsFolder != transcriptsFolder {
		t.Fatalf("persisted transcript folder = %q", persisted.AgentTranscriptsFolder)
	}

	path := filepath.Join(transcriptsFolder, conversation.ConversationID, conversation.ConversationID+".jsonl")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read synced transcript: %v", err)
	}
	lines := decodeCursorTranscriptLines(t, data)
	if len(lines) != 2 || lines[0].Role != "user" || lines[1].Role != "assistant" {
		t.Fatalf("synced transcript = %s", data)
	}

	reloaded, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatalf("LoadConversation() error = %v", err)
	}
	if reloaded.AgentTranscriptsFolder != transcriptsFolder {
		t.Fatalf("reloaded transcript folder = %q", reloaded.AgentTranscriptsFolder)
	}
}

func TestNormalizeAgentTranscriptsFolderRejectsUnexpectedPaths(t *testing.T) {
	root := t.TempDir()
	projectsRoot := filepath.Join(root, ".cursor", "projects")
	valid := filepath.Join(projectsRoot, "project-id", "agent-transcripts")
	if got := normalizeAgentTranscriptsFolderWithin(valid, projectsRoot); got != valid {
		t.Fatalf("valid transcript folder = %q, want %q", got, valid)
	}
	for _, unexpected := range []string{
		filepath.Join(projectsRoot, "project-id", "other"),
		filepath.Join(root, "outside", "agent-transcripts"),
		filepath.Join(projectsRoot, "agent-transcripts"),
		"agent-transcripts",
	} {
		if got := normalizeAgentTranscriptsFolderWithin(unexpected, projectsRoot); got != "" {
			t.Fatalf("unexpected folder accepted: %q", got)
		}
	}
}

func TestPreserveCursorAppendedTurnEnded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "conversation.jsonl")
	existing := []byte("{\"role\":\"user\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hello\"}]}}\n{\"type\":\"turn_ended\",\"status\":\"success\"}\n")
	if err := os.WriteFile(path, existing, 0o644); err != nil {
		t.Fatalf("write existing transcript: %v", err)
	}
	projected := []byte("{\"role\":\"user\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hello\"}]}}\n{\"role\":\"assistant\",\"message\":{\"content\":[{\"type\":\"text\",\"text\":\"hi\"}]}}\n")
	preserved := preserveCursorAppendedTurnEnded(path, projected)
	if countTranscriptTurnEnded(preserved) != 1 {
		t.Fatalf("preserved transcript = %s", preserved)
	}
	if !strings.HasSuffix(string(preserved), "{\"type\":\"turn_ended\",\"status\":\"success\"}\n") {
		t.Fatalf("terminal line not preserved: %s", preserved)
	}
}

func TestAgentTranscriptsFolderRecoveredFromLegacyRequestContext(t *testing.T) {
	folder := transcriptTestFolder(t)
	payload, err := protojson.Marshal(&agentv1.RequestContext{
		Env: &agentv1.RequestContextEnv{AgentTranscriptsFolder: folder},
	})
	if err != nil {
		t.Fatalf("marshal request context: %v", err)
	}
	conversation := transcriptTestConversation([]HistoryEntry{{
		TurnSeq: 1,
		Role:    "user",
		Kind:    "request_context",
		Payload: payload,
	}})
	conversation.AgentTranscriptsFolder = ""
	normalizeLoadedConversation(conversation.ConversationID, conversation)
	if conversation.AgentTranscriptsFolder != folder {
		t.Fatalf("recovered transcript folder = %q", conversation.AgentTranscriptsFolder)
	}
}

func transcriptTestFolder(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	return filepath.Join(home, ".cursor", "projects", "project-id", "agent-transcripts")
}

func transcriptTestConversation(entries []HistoryEntry) *ConversationFile {
	conversation := &ConversationFile{
		ConversationID:     "conversation-1",
		RootConversationID: "conversation-1",
		Mode:               "agent",
		NextTurnSeq:        1,
		NextEntrySeq:       1,
		Entries:            make([]HistoryEntry, 0, len(entries)),
	}
	appendEntriesInPlace(conversation, entries)
	return conversation
}

func transcriptTestUserMessageEntry(t *testing.T, turnSeq int64, requestID string, text string) HistoryEntry {
	t.Helper()
	payload, err := protojson.Marshal(&agentv1.UserMessage{Text: text, MessageId: fmt.Sprintf("message-%d", turnSeq)})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	return HistoryEntry{
		TurnSeq:   turnSeq,
		RequestID: requestID,
		Role:      "user",
		Kind:      "user_message",
		Payload:   payload,
	}
}

func transcriptTestEditToolCall(t *testing.T, path string) []byte {
	t.Helper()
	payload, err := protojson.Marshal(&agentv1.ToolCall{
		Tool: &agentv1.ToolCall_EditToolCall{
			EditToolCall: &agentv1.EditToolCall{
				Args: &agentv1.EditArgs{Path: path},
			},
		},
	})
	if err != nil {
		t.Fatalf("marshal edit tool call: %v", err)
	}
	return payload
}

func decodeCursorTranscriptLines(t *testing.T, data []byte) []cursorTranscriptLine {
	t.Helper()
	lines := make([]cursorTranscriptLine, 0)
	scanner := bufio.NewScanner(strings.NewReader(string(data)))
	for scanner.Scan() {
		var line cursorTranscriptLine
		if err := json.Unmarshal(scanner.Bytes(), &line); err != nil {
			t.Fatalf("decode transcript line %q: %v", scanner.Text(), err)
		}
		lines = append(lines, line)
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("scan transcript: %v", err)
	}
	return lines
}

func transcriptLineText(line cursorTranscriptLine) string {
	if line.Message == nil {
		return ""
	}
	texts := make([]string, 0, len(line.Message.Content))
	for _, content := range line.Message.Content {
		if content.Type == "text" {
			texts = append(texts, content.Text)
		}
	}
	return strings.Join(texts, "\n\n")
}
