package forwarder

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptengine "cursor/internal/backend/agent/prompt"
)

func TestProjectCheckpointProjectionBuildsResolvableForkState(t *testing.T) {
	userPayload, err := protojson.Marshal(&agentv1.UserMessage{
		Text:      "parent question",
		MessageId: "message-1",
	})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	conversation := &ConversationFile{
		ConversationID:        "conversation-1",
		RootConversationID:    "conversation-1",
		Mode:                  "agent",
		NextTurnSeq:           2,
		NextEntrySeq:          3,
		TokenDetailsMaxTokens: projectedConversationMaxTokens,
		Entries: []HistoryEntry{
			{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: userPayload},
			newAssistantTextEntry(1, "request-1", "parent answer", "", ""),
		},
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	state := projection.State
	if len(state.GetTurns()) != 1 {
		t.Fatalf("ProjectCheckpointProjection() turns = %d, want 1 Blob-backed turn", len(state.GetTurns()))
	}
	blobs := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		digest := sha256.Sum256(blob.Data)
		if len(blob.ID) != sha256.Size || string(blob.ID) != string(digest[:]) {
			t.Fatalf("invalid content-addressed Blob id=%x", blob.ID)
		}
		blobs[string(blob.ID)] = blob.Data
	}
	turnPayload, ok := blobs[string(state.GetTurns()[0])]
	if !ok {
		t.Fatal("turn references a missing Blob")
	}
	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(turnPayload, turn); err != nil {
		t.Fatalf("decode turn Blob: %v", err)
	}
	agentTurn := turn.GetAgentConversationTurn()
	if agentTurn == nil {
		t.Fatal("turn Blob does not contain an agent turn")
	}
	if _, ok := blobs[string(agentTurn.GetUserMessage())]; !ok {
		t.Fatal("turn references a missing user message Blob")
	}
	for _, stepID := range agentTurn.GetSteps() {
		if _, ok := blobs[string(stepID)]; !ok {
			t.Fatal("turn references a missing step Blob")
		}
	}

	messages, err := importedConversationStateModelMessages(state)
	if err != nil {
		t.Fatalf("importedConversationStateModelMessages() error = %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("imported messages = %d, want parent user and assistant context", len(messages))
	}
	if messages[0].Role != "user" || !strings.Contains(messages[0].Content, "parent question") {
		t.Fatalf("first imported message = %#v", messages[0])
	}
	if messages[1].Role != "assistant" || messages[1].Content != "parent answer" {
		t.Fatalf("second imported message = %#v", messages[1])
	}
}

func TestProjectCheckpointProjectionMergesResumeActivityIntoPreviousUserTurn(t *testing.T) {
	userPayload, err := protojson.Marshal(&agentv1.UserMessage{
		Text:      "original question",
		MessageId: "message-1",
	})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	firstAnswer := newAssistantTextEntry(1, "request-1", "before resume", "", "")
	firstAnswer.Seq = 2
	resumedAnswer := newAssistantTextEntry(2, "request-resume", "after resume", "", "")
	resumedAnswer.Seq = 3
	conversation := &ConversationFile{
		ConversationID:     "conversation-1",
		RootConversationID: "conversation-1",
		Mode:               "agent",
		NextTurnSeq:        3,
		NextEntrySeq:       4,
		Entries: []HistoryEntry{
			{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: userPayload},
			firstAnswer,
			resumedAnswer,
		},
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if len(projection.State.GetTurns()) != 1 {
		t.Fatalf("checkpoint turns = %d, want one logical user turn", len(projection.State.GetTurns()))
	}

	blobs := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		blobs[string(blob.ID)] = blob.Data
	}
	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(blobs[string(projection.State.GetTurns()[0])], turn); err != nil {
		t.Fatalf("decode checkpoint turn: %v", err)
	}
	agentTurn := turn.GetAgentConversationTurn()
	if agentTurn == nil {
		t.Fatal("checkpoint turn does not contain an agent turn")
	}
	if len(agentTurn.GetUserMessage()) == 0 {
		t.Fatal("checkpoint turn lost the original user message Blob id")
	}
	if _, ok := blobs[string(agentTurn.GetUserMessage())]; !ok {
		t.Fatal("checkpoint turn references a missing user message Blob")
	}
	if agentTurn.GetRequestId() != "request-1" {
		t.Fatalf("checkpoint request id = %q, want original request", agentTurn.GetRequestId())
	}

	steps := checkpointProjectionSteps(t, projection)
	if len(steps) != 2 || steps[0].GetAssistantMessage().GetText() != "before resume" || steps[1].GetAssistantMessage().GetText() != "after resume" {
		t.Fatalf("checkpoint steps did not preserve resumed activity: %#v", steps)
	}

	replay, err := promptengine.DecodeReplayMessages(projection.State.GetRootPromptMessagesJson())
	if err != nil {
		t.Fatalf("decode root prompt replay: %v", err)
	}
	if len(replay) != 3 || replay[0].Role != "user" || replay[1].Content != "before resume" || replay[2].Content != "after resume" {
		t.Fatalf("checkpoint turn merge changed model replay: %#v", replay)
	}
}

func TestProjectCheckpointProjectionOmitsActivityWithoutAnyUserMessage(t *testing.T) {
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Mode:           "agent",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			newAssistantTextEntry(1, "request-resume", "orphaned resume output", "", ""),
		},
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if len(projection.State.GetTurns()) != 0 || len(projection.Blobs) != 0 {
		t.Fatalf("checkpoint emitted a turn without a user message: %#v", projection)
	}
}

func TestProjectCheckpointProjectionKeepsForkPointIsolatedFromLaterHistory(t *testing.T) {
	firstUser, err := protojson.Marshal(&agentv1.UserMessage{Text: "first question", MessageId: "message-1"})
	if err != nil {
		t.Fatalf("marshal first user message: %v", err)
	}
	conversation := &ConversationFile{
		ConversationID:        "conversation-1",
		RootConversationID:    "conversation-1",
		Mode:                  "agent",
		NextTurnSeq:           2,
		NextEntrySeq:          3,
		TokenDetailsMaxTokens: projectedConversationMaxTokens,
		Entries: []HistoryEntry{
			{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: firstUser},
			newAssistantTextEntry(1, "request-1", "first answer", "", ""),
		},
	}
	projector := NewHistoryProjector()
	midpoint, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("midpoint projection: %v", err)
	}

	secondUser, err := protojson.Marshal(&agentv1.UserMessage{Text: "second question", MessageId: "message-2"})
	if err != nil {
		t.Fatalf("marshal second user message: %v", err)
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		{TurnSeq: 2, RequestID: "request-2", Role: "user", Kind: "user_message", Payload: secondUser},
		newAssistantTextEntry(2, "request-2", "second answer", "", ""),
	})
	latest, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("latest projection: %v", err)
	}

	midpointMessages, err := importedConversationStateModelMessages(midpoint.State)
	if err != nil {
		t.Fatalf("import midpoint messages: %v", err)
	}
	latestMessages, err := importedConversationStateModelMessages(latest.State)
	if err != nil {
		t.Fatalf("import latest messages: %v", err)
	}
	if len(midpoint.State.GetTurns()) != 1 || len(midpointMessages) != 2 {
		t.Fatalf("midpoint turns=%d messages=%d, want 1 turn and 2 messages", len(midpoint.State.GetTurns()), len(midpointMessages))
	}
	if len(latest.State.GetTurns()) != 2 || len(latestMessages) != 4 {
		t.Fatalf("latest turns=%d messages=%d, want 2 turns and 4 messages", len(latest.State.GetTurns()), len(latestMessages))
	}
	if midpointMessages[1].Content != "first answer" || latestMessages[3].Content != "second answer" {
		t.Fatalf("fork snapshots are not isolated: midpoint=%#v latest=%#v", midpointMessages, latestMessages)
	}
}

func TestProjectCheckpointProjectionMergesToolCallWithCompletedResult(t *testing.T) {
	userPayload, err := protojson.Marshal(&agentv1.UserMessage{Text: "inspect file", MessageId: "message-1"})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	startedAt := uint64(100)
	toolCallID := "call-1"
	startedToolCall := checkpointTestToolCallPayload(t, &agentv1.ToolCall{
		ToolCallId:  &toolCallID,
		StartedAtMs: &startedAt,
		Tool: &agentv1.ToolCall_ReadToolCall{
			ReadToolCall: &agentv1.ReadToolCall{
				Args: &agentv1.ReadToolArgs{Path: "/tmp/example.txt"},
			},
		},
	})
	completedAt := uint64(200)
	completedToolCall := checkpointTestToolCallPayload(t, &agentv1.ToolCall{
		CompletedAtMs: &completedAt,
		Tool: &agentv1.ToolCall_ReadToolCall{
			ReadToolCall: &agentv1.ReadToolCall{
				Result: &agentv1.ReadToolResult{
					Result: &agentv1.ReadToolResult_Success{
						Success: &agentv1.ReadToolSuccess{
							Path:       "/tmp/example.txt",
							TotalLines: 1,
							Output:     &agentv1.ReadToolSuccess_Content{Content: "file contents"},
						},
					},
				},
			},
		},
	})
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Mode:           "agent",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: userPayload},
			newAssistantTextEntry(1, "request-1", "before", "", ""),
			newToolCallEntry(1, "request-1", "call-1", "Read", "", "", startedToolCall),
			newToolResultEntry(1, "request-1", "call-1", "Read", `{"path":"/tmp/example.txt"}`, "file contents", "", completedToolCall),
			newAssistantTextEntry(1, "request-1", "after", "", ""),
		},
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	if len(projection.Blobs) != 5 {
		t.Fatalf("checkpoint blobs = %d, want user, three final steps, and turn", len(projection.Blobs))
	}
	steps := checkpointProjectionSteps(t, projection)
	if len(steps) != 3 {
		t.Fatalf("checkpoint steps = %d, want assistant, completed Read, assistant", len(steps))
	}
	if steps[0].GetAssistantMessage().GetText() != "before" || steps[2].GetAssistantMessage().GetText() != "after" {
		t.Fatalf("checkpoint step ordering changed: %#v", steps)
	}
	mergedToolCall := steps[1].GetToolCall()
	readCall := mergedToolCall.GetReadToolCall()
	if readCall == nil || readCall.GetResult().GetSuccess().GetContent() != "file contents" {
		t.Fatalf("checkpoint Read step does not contain completed result: %#v", steps[1].GetToolCall())
	}
	if readCall.GetArgs().GetPath() != "/tmp/example.txt" || mergedToolCall.GetToolCallId() != toolCallID || mergedToolCall.GetStartedAtMs() != startedAt || mergedToolCall.GetCompletedAtMs() != completedAt {
		t.Fatalf("checkpoint Read step lost started-call fields: %#v", mergedToolCall)
	}

	replay, err := promptengine.DecodeReplayMessages(projection.State.GetRootPromptMessagesJson())
	if err != nil {
		t.Fatalf("decode root prompt replay: %v", err)
	}
	for _, message := range replay {
		if message.Name == "Read" || len(message.ToolCalls) > 0 {
			t.Fatalf("UI-only Read result leaked into root prompt replay: %#v", replay)
		}
	}
}

func TestProjectCheckpointProjectionIsIdempotentAndDoesNotMutateHistory(t *testing.T) {
	userPayload, err := protojson.Marshal(&agentv1.UserMessage{Text: "inspect file", MessageId: "message-1"})
	if err != nil {
		t.Fatalf("marshal user message: %v", err)
	}
	completedToolCall := checkpointTestReadToolCall(t, &agentv1.ReadToolResult{
		Result: &agentv1.ReadToolResult_Success{
			Success: &agentv1.ReadToolSuccess{
				Path:       "/tmp/example.txt",
				TotalLines: 1,
				Output:     &agentv1.ReadToolSuccess_Content{Content: "file contents"},
			},
		},
	})
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Mode:           "agent",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			{Seq: 1, TurnSeq: 1, RequestID: "request-1", Role: "user", Kind: "user_message", Payload: userPayload},
			newToolCallEntry(1, "request-1", "call-1", "Read", "", "", checkpointTestReadToolCall(t, nil)),
			newToolResultEntry(1, "request-1", "call-1", "Read", `{"path":"/tmp/example.txt"}`, "file contents", "", completedToolCall),
		},
	}
	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatalf("marshal conversation before projection: %v", err)
	}

	projector := NewHistoryProjector()
	first, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("first projection: %v", err)
	}
	second, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("second projection: %v", err)
	}

	if !proto.Equal(first.State, second.State) {
		t.Fatalf("repeated projection changed checkpoint state: first=%#v second=%#v", first.State, second.State)
	}
	if len(first.Blobs) != len(second.Blobs) {
		t.Fatalf("repeated projection changed blob count: first=%d second=%d", len(first.Blobs), len(second.Blobs))
	}
	for index := range first.Blobs {
		if !bytes.Equal(first.Blobs[index].ID, second.Blobs[index].ID) || !bytes.Equal(first.Blobs[index].Data, second.Blobs[index].Data) {
			t.Fatalf("repeated projection changed blob %d", index)
		}
	}
	after, err := json.Marshal(conversation)
	if err != nil {
		t.Fatalf("marshal conversation after projection: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatalf("checkpoint projection mutated semantic history:\nbefore=%s\nafter=%s", before, after)
	}
}

func TestProjectCheckpointProjectionKeepsStartedToolCallWhenResultPayloadIsMissing(t *testing.T) {
	startedToolCall := checkpointTestReadToolCall(t, nil)
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Mode:           "agent",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			testCheckpointUserEntry(t),
			newToolCallEntry(1, "request-1", "call-1", "Read", "", "", startedToolCall),
			newToolResultEntry(1, "request-1", "call-1", "Read", `{"path":"/tmp/example.txt"}`, "read failed", "", nil),
		},
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	steps := checkpointProjectionSteps(t, projection)
	if len(steps) != 1 {
		t.Fatalf("checkpoint steps = %d, want the original Read step", len(steps))
	}
	readCall := steps[0].GetToolCall().GetReadToolCall()
	if readCall == nil || readCall.GetArgs().GetPath() != "/tmp/example.txt" || readCall.GetResult() != nil {
		t.Fatalf("checkpoint did not preserve the original Read call: %#v", steps[0].GetToolCall())
	}
}

func TestProjectCheckpointProjectionAppendsLegacyResultWithoutToolCallEntry(t *testing.T) {
	completedToolCall := checkpointTestReadToolCall(t, &agentv1.ReadToolResult{
		Result: &agentv1.ReadToolResult_Error{Error: &agentv1.ReadToolError{ErrorMessage: "not readable"}},
	})
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Mode:           "agent",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			testCheckpointUserEntry(t),
			newToolResultEntry(1, "request-1", "call-1", "Read", `{"path":"/tmp/example.txt"}`, "not readable", "", completedToolCall),
		},
	}

	projection, err := NewHistoryProjector().ProjectCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("ProjectCheckpointProjection() error = %v", err)
	}
	steps := checkpointProjectionSteps(t, projection)
	if len(steps) != 1 || steps[0].GetToolCall().GetReadToolCall().GetResult().GetError().GetErrorMessage() != "not readable" {
		t.Fatalf("legacy result-only Read step was not preserved: %#v", steps)
	}
}

func checkpointTestReadToolCall(t *testing.T, result *agentv1.ReadToolResult) []byte {
	t.Helper()
	return checkpointTestToolCallPayload(t, &agentv1.ToolCall{
		Tool: &agentv1.ToolCall_ReadToolCall{
			ReadToolCall: &agentv1.ReadToolCall{
				Args:   &agentv1.ReadToolArgs{Path: "/tmp/example.txt"},
				Result: result,
			},
		},
	})
}

func checkpointTestToolCallPayload(t *testing.T, toolCall *agentv1.ToolCall) []byte {
	t.Helper()
	payload, err := protojson.Marshal(toolCall)
	if err != nil {
		t.Fatalf("marshal Read tool call: %v", err)
	}
	return payload
}

func checkpointProjectionSteps(t *testing.T, projection *CheckpointProjection) []*agentv1.ConversationStep {
	t.Helper()
	if projection == nil || projection.State == nil || len(projection.State.GetTurns()) != 1 {
		t.Fatalf("checkpoint turns = %#v, want exactly one turn", projection)
	}
	blobs := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		blobs[string(blob.ID)] = blob.Data
	}
	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(blobs[string(projection.State.GetTurns()[0])], turn); err != nil {
		t.Fatalf("decode checkpoint turn: %v", err)
	}
	agentTurn := turn.GetAgentConversationTurn()
	if agentTurn == nil {
		t.Fatal("checkpoint turn does not contain an agent turn")
	}
	steps := make([]*agentv1.ConversationStep, 0, len(agentTurn.GetSteps()))
	for _, stepID := range agentTurn.GetSteps() {
		step := &agentv1.ConversationStep{}
		if err := proto.Unmarshal(blobs[string(stepID)], step); err != nil {
			t.Fatalf("decode checkpoint step: %v", err)
		}
		steps = append(steps, step)
	}
	return steps
}

// nativeRootMessageSpec 描述一条期望出现在原生 root 中的 user/assistant 文本消息。
type nativeRootMessageSpec struct {
	role string
	text string
}

// nativeRootCoreMessage 是本测试独立维护的最小原生 CoreMessage 视图；
// 只读取 role/content，不依赖或复用本地 replay 解码路径。
type nativeRootCoreMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// TestProjectCheckpointProjectionNativeRootReferences 固化检查点写出的原生 root 引用合同：
// State.RootPromptMessagesJson 每项必须是内容仓库中可解析的 raw32 SHA-256 引用，
// 对应 Blob 正文是仅有原生 role/content 的单条 CoreMessage JSON。
// 19 条合成消息只用于复现同类机制，不代表任何用户真实历史项数。
// 旧实现语义 RED：旧生产代码把本地 replay JSON 内联写入 root，应在引用查找处得到语义 RED。
func TestProjectCheckpointProjectionNativeRootReferences(t *testing.T) {
	cases := []struct {
		name  string
		specs []nativeRootMessageSpec
	}{
		{
			name:  "two synthetic model messages alternate user and assistant",
			specs: syntheticNativeRootSpecs(2),
		},
		{
			name:  "nineteen synthetic model messages alternate user and assistant",
			specs: syntheticNativeRootSpecs(19),
		},
		{
			name: "duplicate content retains repeated root instances",
			specs: []nativeRootMessageSpec{
				{role: "user", text: "repeated question"},
				{role: "assistant", text: "repeated answer"},
				{role: "user", text: "repeated question"},
			},
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			root, repo := assertCheckpointNativeRootReferences(t, testCase.name, testCase.specs)
			if len(root) == 3 && len(testCase.specs) == 3 {
				if !bytes.Equal(root[0], root[2]) {
					t.Fatalf("%s: duplicate native root instances are not retained as equal references: %x vs %x", testCase.name, root[0], root[2])
				}
				if _, exists := repo[string(root[0])]; !exists {
					t.Fatalf("%s: duplicate native root reference is missing from the content repository", testCase.name)
				}
			}
		})
	}
}

func syntheticNativeRootSpecs(count int) []nativeRootMessageSpec {
	specs := make([]nativeRootMessageSpec, 0, count)
	for index := 0; index < count; index++ {
		role := "user"
		if index%2 == 1 {
			role = "assistant"
		}
		specs = append(specs, nativeRootMessageSpec{
			role: role,
			text: fmt.Sprintf("synthetic native root message %02d", index+1),
		})
	}
	return specs
}

// assertCheckpointNativeRootReferences 真实投影给定消息，并逐条核对原生 root 引用。
// 对每个 root 项先在内容仓库中查找，再断言长度、哈希与正文；重复内容允许共享同一 Blob。
func assertCheckpointNativeRootReferences(t *testing.T, caseName string, specs []nativeRootMessageSpec) ([][]byte, map[string][]byte) {
	t.Helper()
	entries := make([]HistoryEntry, 0, len(specs))
	for index, spec := range specs {
		entry, ok, err := newModelMessageEntry(1, "request-1", modeladapter.Message{Role: spec.role, Content: spec.text})
		if err != nil {
			t.Fatalf("%s: newModelMessageEntry(%d) error = %v", caseName, index, err)
		}
		if !ok {
			t.Fatalf("%s: newModelMessageEntry(%d) unexpectedly dropped a text message", caseName, index)
		}
		entry.Seq = int64(index + 1)
		entries = append(entries, entry)
	}
	conversation := &ConversationFile{
		ConversationID: "conversation-1",
		Mode:           "agent",
		NextTurnSeq:    2,
		NextEntrySeq:   int64(len(entries)) + 1,
		Entries:        entries,
	}

	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("%s: projectNativeCheckpointProjection() error = %v", caseName, err)
	}
	if projection == nil || projection.State == nil {
		t.Fatalf("%s: ProjectCheckpointProjection() returned nil projection", caseName)
	}

	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		digest := sha256.Sum256(blob.Data)
		if len(blob.ID) != sha256.Size || !bytes.Equal(blob.ID, digest[:]) {
			t.Fatalf("%s: invalid content-addressed Blob id=%x", caseName, blob.ID)
		}
		repo[string(blob.ID)] = blob.Data
	}

	root := projection.State.GetRootPromptMessagesJson()
	if len(root) != len(specs) {
		t.Fatalf("%s: native root messages = %d, want %d", caseName, len(root), len(specs))
	}
	for index, reference := range root {
		data, ok := repo[string(reference)]
		if !ok {
			t.Fatalf("%s: missing native root blob for message %d: root item is not a content-addressed reference (len=%d, value=%x)", caseName, index, len(reference), reference)
		}
		if len(reference) != sha256.Size {
			t.Fatalf("%s: native root message %d reference length = %d, want %d", caseName, index, len(reference), sha256.Size)
		}
		digest := sha256.Sum256(data)
		if !bytes.Equal(digest[:], reference) {
			t.Fatalf("%s: native root message %d reference does not match its Blob SHA-256", caseName, index)
		}
		var topLevel map[string]json.RawMessage
		if err := json.Unmarshal(data, &topLevel); err != nil {
			t.Fatalf("%s: native root message %d is not a JSON object: %v", caseName, index, err)
		}
		for _, localField := range []string{"tool_calls", "content_parts", "name"} {
			if _, exists := topLevel[localField]; exists {
				t.Fatalf("%s: native root message %d leaked local replay field %q", caseName, index, localField)
			}
		}
		var core nativeRootCoreMessage
		if err := json.Unmarshal(data, &core); err != nil {
			t.Fatalf("%s: native root message %d CoreMessage decode error = %v", caseName, index, err)
		}
		if core.Role != specs[index].role {
			t.Fatalf("%s: native root message %d role = %q, want %q", caseName, index, core.Role, specs[index].role)
		}
		var content string
		if err := json.Unmarshal(core.Content, &content); err != nil {
			t.Fatalf("%s: native root message %d content is not a JSON string: %v", caseName, index, err)
		}
		if content != specs[index].text {
			t.Fatalf("%s: native root message %d content = %q, want %q", caseName, index, content, specs[index].text)
		}
	}
	return root, repo
}

// 以下为本轮新增的最小原生检查点回归（C0-W1 独立写出接缝）。

// nativeRootToolBlock 是本测试独立维护的原生内容块视图，只读取断言所需字段。
type nativeRootToolBlock struct {
	Type       string          `json:"type"`
	Text       string          `json:"text"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
	Result     json.RawMessage `json:"result"`
	IsError    *bool           `json:"isError"`
}

// encodeNativeTestRoot 用独立 graph 调用 encodeNativeCheckpointRoot，
// 并逐项核对返回引用是内容仓库中长度 raw32、哈希匹配的正文。
func encodeNativeTestRoot(t *testing.T, caseName string, messages []modeladapter.Message) (root [][]byte, bodies [][]byte) {
	t.Helper()
	graph := newCheckpointBlobGraph()
	root, err := encodeNativeCheckpointRoot(messages, graph)
	if err != nil {
		t.Fatalf("%s: encodeNativeCheckpointRoot() error = %v", caseName, err)
	}
	repo := make(map[string][]byte, len(graph.list()))
	for _, blob := range graph.list() {
		digest := sha256.Sum256(blob.Data)
		if len(blob.ID) != sha256.Size || !bytes.Equal(blob.ID, digest[:]) {
			t.Fatalf("%s: invalid content-addressed Blob id=%x", caseName, blob.ID)
		}
		repo[string(blob.ID)] = blob.Data
	}
	if len(root) != len(messages) {
		t.Fatalf("%s: native root messages = %d, want %d", caseName, len(root), len(messages))
	}
	bodies = make([][]byte, 0, len(root))
	for index, reference := range root {
		if len(reference) != sha256.Size {
			t.Fatalf("%s: root %d reference length = %d, want %d", caseName, index, len(reference), sha256.Size)
		}
		blob, ok := repo[string(reference)]
		if !ok {
			t.Fatalf("%s: root %d is not a content-addressed reference present in the graph", caseName, index)
		}
		digest := sha256.Sum256(blob)
		if !bytes.Equal(digest[:], reference) {
			t.Fatalf("%s: root %d reference does not match its Blob SHA-256", caseName, index)
		}
		bodies = append(bodies, blob)
	}
	return root, bodies
}

// decodeNativeTestMessage 把正文解码为独立维护的 {role,content} 视图。
func decodeNativeTestMessage(t *testing.T, caseName string, raw []byte) nativeRootCoreMessage {
	t.Helper()
	var view nativeRootCoreMessage
	if err := json.Unmarshal(raw, &view); err != nil {
		t.Fatalf("%s: decode native CoreMessage: %v", caseName, err)
	}
	return view
}

func decodeNativeTestBlocks(t *testing.T, caseName string, content json.RawMessage) []nativeRootToolBlock {
	t.Helper()
	var blocks []nativeRootToolBlock
	if err := json.Unmarshal(content, &blocks); err != nil {
		t.Fatalf("%s: decode native content blocks: %v", caseName, err)
	}
	var rawBlocks []map[string]json.RawMessage
	if err := json.Unmarshal(content, &rawBlocks); err != nil {
		t.Fatalf("%s: decode native content block fields: %v", caseName, err)
	}
	allowedFields := map[string]struct{}{
		"type": {}, "text": {}, "toolCallId": {}, "toolName": {}, "args": {}, "result": {},
	}
	for index, block := range blocks {
		if block.IsError != nil {
			t.Fatalf("%s: content block %d unexpectedly carries isError", caseName, index)
		}
		for field := range rawBlocks[index] {
			if _, ok := allowedFields[field]; !ok {
				t.Fatalf("%s: content block %d carries unexpected field %q", caseName, index, field)
			}
		}
	}
	return blocks
}

func compactNativeTestJSON(t *testing.T, raw json.RawMessage) string {
	t.Helper()
	var out bytes.Buffer
	if err := json.Compact(&out, raw); err != nil {
		t.Fatalf("compact JSON %s: %v", raw, err)
	}
	return out.String()
}

func nativeTestObjectKeys(t *testing.T, caseName string, raw json.RawMessage) map[string]json.RawMessage {
	t.Helper()
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatalf("%s: decode object keys: %v", caseName, err)
	}
	return keys
}

func nativeTestToolCall(id, name, args string) modeladapter.ToolCallDescriptor {
	return modeladapter.ToolCallDescriptor{
		ID:       id,
		Type:     "function",
		Function: modeladapter.ToolCallFunctionShape{Name: name, Arguments: args},
	}
}

func assertNativeTestToolCall(t *testing.T, caseName string, block nativeRootToolBlock, toolCallID, toolName, argsJSON string) {
	t.Helper()
	if block.Type != "tool-call" || block.ToolCallID != toolCallID || block.ToolName != toolName {
		t.Fatalf("%s: tool-call block = %#v, want type/toolCallId/toolName = tool-call/%s/%s", caseName, block, toolCallID, toolName)
	}
	if got, want := compactNativeTestJSON(t, block.Args), compactNativeTestJSON(t, json.RawMessage(argsJSON)); got != want {
		t.Fatalf("%s: tool-call args = %s, want %s", caseName, got, want)
	}
}

func assertNativeTestToolResult(t *testing.T, caseName string, block nativeRootToolBlock, toolCallID, toolName, resultJSON string) {
	t.Helper()
	if block.Type != "tool-result" || block.ToolCallID != toolCallID || block.ToolName != toolName {
		t.Fatalf("%s: tool-result block = %#v, want type/toolCallId/toolName = tool-result/%s/%s", caseName, block, toolCallID, toolName)
	}
	if got, want := compactNativeTestJSON(t, block.Result), compactNativeTestJSON(t, json.RawMessage(resultJSON)); got != want {
		t.Fatalf("%s: tool-result result = %s, want %s", caseName, got, want)
	}
}

// TestNativeCheckpointRootTools 直接调用 encodeNativeCheckpointRoot，验证文本/工具调用/结果
// 的原生块结构、结果倒序、重复批次实例与安全数字域；不借助本地 replay 解码证明原生。
func TestNativeCheckpointRootTools(t *testing.T) {
	writeArgs := `{"path":"/tmp/native-write.txt","limit":9007199254740991,"flags":[true,false,null]}`
	patchArgs := `{"path":"/tmp/native-patch.txt","edits":[{"old_text":"a","new_text":"b"}]}`
	patchResult := `{"ok":true,"count":null,"lines":["a","b"]}`

	t.Run("assistant tool batch with reversed results and repeated batch", func(t *testing.T) {
		const caseName = "tool batch"
		userText := "用户 \"引号\" 与 emoji 😀 文本"
		messages := []modeladapter.Message{
			{Role: "user", Content: userText},
			{Role: "assistant", Content: "前缀文本", ToolCalls: []modeladapter.ToolCallDescriptor{
				nativeTestToolCall("call-write", "Write", writeArgs),
				nativeTestToolCall("call-patch", "PatchEdit", patchArgs),
			}},
			{Role: "tool", ToolCallID: "call-patch", Name: "PatchEdit", Content: patchResult},
			{Role: "tool", ToolCallID: "call-write", Name: "Write", Content: ""},
			{Role: "assistant", Content: "前缀文本", ToolCalls: []modeladapter.ToolCallDescriptor{
				nativeTestToolCall("call-write", "Write", writeArgs),
				nativeTestToolCall("call-patch", "PatchEdit", patchArgs),
			}},
			{Role: "tool", ToolCallID: "call-patch", Name: "PatchEdit", Content: patchResult},
			{Role: "tool", ToolCallID: "call-write", Name: "Write", Content: ""},
		}
		before, err := json.Marshal(messages)
		if err != nil {
			t.Fatalf("%s: marshal messages before: %v", caseName, err)
		}
		root, bodies := encodeNativeTestRoot(t, caseName, messages)
		after, err := json.Marshal(messages)
		if err != nil {
			t.Fatalf("%s: marshal messages after: %v", caseName, err)
		}
		if !bytes.Equal(before, after) {
			t.Fatalf("%s: encodeNativeCheckpointRoot mutated its input", caseName)
		}
		if len(root) != len(messages) {
			t.Fatalf("%s: root count = %d, want %d", caseName, len(root), len(messages))
		}

		view := decodeNativeTestMessage(t, caseName, bodies[0])
		if view.Role != "user" {
			t.Fatalf("%s: message 0 role = %q, want user", caseName, view.Role)
		}
		var userContent string
		if err := json.Unmarshal(view.Content, &userContent); err != nil {
			t.Fatalf("%s: message 0 content is not a JSON string: %v", caseName, err)
		}
		if userContent != userText {
			t.Fatalf("%s: message 0 content = %q, want %q", caseName, userContent, userText)
		}

		for _, index := range []int{1, 4} {
			view = decodeNativeTestMessage(t, caseName, bodies[index])
			if view.Role != "assistant" {
				t.Fatalf("%s: message %d role = %q, want assistant", caseName, index, view.Role)
			}
			blocks := decodeNativeTestBlocks(t, caseName, view.Content)
			if len(blocks) != 3 {
				t.Fatalf("%s: message %d blocks = %d, want text plus two tool calls", caseName, index, len(blocks))
			}
			if blocks[0].Type != "text" || blocks[0].Text != "前缀文本" {
				t.Fatalf("%s: message %d first block = %#v", caseName, index, blocks[0])
			}
			assertNativeTestToolCall(t, caseName, blocks[1], "call-write", "Write", writeArgs)
			assertNativeTestToolCall(t, caseName, blocks[2], "call-patch", "PatchEdit", patchArgs)
		}

		for _, index := range []int{2, 5} {
			view = decodeNativeTestMessage(t, caseName, bodies[index])
			if view.Role != "tool" {
				t.Fatalf("%s: message %d role = %q, want tool", caseName, index, view.Role)
			}
			blocks := decodeNativeTestBlocks(t, caseName, view.Content)
			if len(blocks) != 1 {
				t.Fatalf("%s: message %d blocks = %d, want one tool-result", caseName, index, len(blocks))
			}
			assertNativeTestToolResult(t, caseName, blocks[0], "call-patch", "PatchEdit", patchResult)
		}

		for _, index := range []int{3, 6} {
			view = decodeNativeTestMessage(t, caseName, bodies[index])
			if view.Role != "tool" {
				t.Fatalf("%s: message %d role = %q, want tool", caseName, index, view.Role)
			}
			blocks := decodeNativeTestBlocks(t, caseName, view.Content)
			if len(blocks) != 1 {
				t.Fatalf("%s: message %d blocks = %d, want one tool-result", caseName, index, len(blocks))
			}
			assertNativeTestToolResult(t, caseName, blocks[0], "call-write", "Write", `""`)
		}
	})

	t.Run("plain chinese string result stays a json string", func(t *testing.T) {
		const caseName = "plain chinese result"
		messages := []modeladapter.Message{
			{Role: "user", Content: "你好"},
			{Role: "assistant", ToolCalls: []modeladapter.ToolCallDescriptor{nativeTestToolCall("call-write", "Write", `{"path":"/tmp/x"}`)}},
			{Role: "tool", ToolCallID: "call-write", Name: "Write", Content: "普通中文结果"},
		}
		_, bodies := encodeNativeTestRoot(t, caseName, messages)
		blocks := decodeNativeTestBlocks(t, caseName, decodeNativeTestMessage(t, caseName, bodies[2]).Content)
		if len(blocks) != 1 {
			t.Fatalf("%s: blocks = %d, want one", caseName, len(blocks))
		}
		assertNativeTestToolResult(t, caseName, blocks[0], "call-write", "Write", `"普通中文结果"`)
	})

	t.Run("safe integer forms are accepted", func(t *testing.T) {
		const caseName = "safe integer forms"
		args := `{"sci":1e3,"frac":1.0,"lower":-9007199254740991}`
		messages := []modeladapter.Message{
			{Role: "user", Content: "数字"},
			{Role: "assistant", ToolCalls: []modeladapter.ToolCallDescriptor{nativeTestToolCall("call-a", "Write", args)}},
			{Role: "tool", ToolCallID: "call-a", Name: "Write", Content: ""},
		}
		_, bodies := encodeNativeTestRoot(t, caseName, messages)
		blocks := decodeNativeTestBlocks(t, caseName, decodeNativeTestMessage(t, caseName, bodies[1]).Content)
		if len(blocks) != 1 {
			t.Fatalf("%s: blocks = %d, want one tool call", caseName, len(blocks))
		}
		assertNativeTestToolCall(t, caseName, blocks[0], "call-a", "Write", args)
	})
}

// assertNativeCheckpointRootRejects 验证域外输入在写入 graph 前失败、root 为 nil、
// 图与输入前后完全不变，并返回固定类别错误。
func assertNativeCheckpointRootRejects(t *testing.T, caseName string, messages []modeladapter.Message, wantErr error) {
	t.Helper()
	graph := newCheckpointBlobGraph()
	if sentinel := graph.add([]byte("native-checkpoint-sentinel-blob")); len(sentinel) != sha256.Size {
		t.Fatalf("%s: sentinel blob id length = %d", caseName, len(sentinel))
	}
	beforeGraph, err := json.Marshal(graph.list())
	if err != nil {
		t.Fatalf("%s: marshal graph before: %v", caseName, err)
	}
	beforeMessages, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("%s: marshal messages before: %v", caseName, err)
	}
	root, err := encodeNativeCheckpointRoot(messages, graph)
	if err == nil {
		t.Fatalf("%s: encodeNativeCheckpointRoot() succeeded, want error %v", caseName, wantErr)
	}
	if root != nil {
		t.Fatalf("%s: encodeNativeCheckpointRoot() returned a non-nil root on error", caseName)
	}
	if !errors.Is(err, wantErr) && !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("%s: error = %v, want %v", caseName, err, wantErr)
	}
	afterGraph, err := json.Marshal(graph.list())
	if err != nil {
		t.Fatalf("%s: marshal graph after: %v", caseName, err)
	}
	if !bytes.Equal(beforeGraph, afterGraph) {
		t.Fatalf("%s: blob graph changed on failure: before=%s after=%s", caseName, beforeGraph, afterGraph)
	}
	afterMessages, err := json.Marshal(messages)
	if err != nil {
		t.Fatalf("%s: marshal messages after: %v", caseName, err)
	}
	if !bytes.Equal(beforeMessages, afterMessages) {
		t.Fatalf("%s: encodeNativeCheckpointRoot mutated its input", caseName)
	}
}

// TestNativeCheckpointRootRejectsLossyInput 固化为固定类别的域外拒绝，并覆盖单条正文大小边界。
func TestNativeCheckpointRootRejectsLossyInput(t *testing.T) {
	validUser := modeladapter.Message{Role: "user", Content: "合法用户问题"}
	prepend := func(messages ...modeladapter.Message) []modeladapter.Message {
		return append([]modeladapter.Message{validUser}, messages...)
	}
	assistantWithCall := func(id, name, args string) modeladapter.Message {
		return modeladapter.Message{Role: "assistant", ToolCalls: []modeladapter.ToolCallDescriptor{nativeTestToolCall(id, name, args)}}
	}
	toolResult := func(id, name, content string) modeladapter.Message {
		return modeladapter.Message{Role: "tool", ToolCallID: id, Name: name, Content: content}
	}

	cases := []struct {
		name     string
		messages []modeladapter.Message
		wantErr  error
	}{
		{
			name:     "invalid args",
			messages: prepend(assistantWithCall("c1", "Write", `{"path":`)),
			wantErr:  errNativeCheckpointJSONInvalid,
		},
		{
			name:     "unsafe integer",
			messages: prepend(assistantWithCall("c1", "Write", `{"n":9007199254740992}`)),
			wantErr:  errNativeCheckpointNumber,
		},
		{
			name:     "fraction",
			messages: prepend(assistantWithCall("c1", "Write", `{"n":1.000000000000000001}`)),
			wantErr:  errNativeCheckpointNumber,
		},
		{
			name:     "nested unsafe integer",
			messages: prepend(assistantWithCall("c1", "Write", `{"a":[{"b":9007199254740992}]}`)),
			wantErr:  errNativeCheckpointNumber,
		},
		{
			name: "json string result",
			messages: prepend(
				assistantWithCall("c1", "Write", `{}`),
				toolResult("c1", "Write", `"123"`),
			),
			wantErr: errNativeCheckpointResultType,
		},
		{
			name:     "unknown role",
			messages: prepend(modeladapter.Message{Role: "developer", Content: "x"}),
			wantErr:  errNativeCheckpointUnknownRole,
		},
		{
			name:     "reasoning metadata",
			messages: prepend(modeladapter.Message{Role: "user", Content: "x", ReasoningContent: "thought"}),
			wantErr:  errNativeCheckpointReasoningMetadata,
		},
		{
			name:     "content parts",
			messages: prepend(modeladapter.Message{Role: "user", Content: "x", ContentParts: []modeladapter.ContentPart{{Type: "image"}}}),
			wantErr:  errNativeCheckpointContentParts,
		},
		{
			name: "provider tool metadata",
			messages: prepend(modeladapter.Message{Role: "assistant", ToolCalls: []modeladapter.ToolCallDescriptor{{
				ID:                "c1",
				Type:              "function",
				Function:          modeladapter.ToolCallFunctionShape{Name: "Write", Arguments: `{}`},
				OpenAIResponsesID: "provider-item-1",
			}}}),
			wantErr: errNativeCheckpointProviderMetadata,
		},
		{
			name:     "orphan tool result",
			messages: prepend(toolResult("c-orphan", "Write", "")),
			wantErr:  errNativeCheckpointOrphanToolResult,
		},
		{
			name:     "missing tool result",
			messages: prepend(assistantWithCall("c1", "Write", `{}`)),
			wantErr:  errNativeCheckpointMissingToolResult,
		},
		{
			name: "duplicate tool result",
			messages: prepend(
				assistantWithCall("c1", "Write", `{}`),
				toolResult("c1", "Write", ""),
				toolResult("c1", "Write", ""),
			),
			wantErr: errNativeCheckpointDuplicateToolResult,
		},
		{
			name: "tool name mismatch",
			messages: prepend(
				assistantWithCall("c1", "Write", `{}`),
				toolResult("c1", "PatchEdit", ""),
			),
			wantErr: errNativeCheckpointToolNameMismatch,
		},
		{
			name: "duplicate tool call id in batch",
			messages: prepend(modeladapter.Message{Role: "assistant", ToolCalls: []modeladapter.ToolCallDescriptor{
				nativeTestToolCall("c1", "Write", `{}`),
				nativeTestToolCall("c1", "PatchEdit", `{}`),
			}}),
			wantErr: errNativeCheckpointDuplicateToolCall,
		},
		{
			name: "plain text inserted before tool results",
			messages: prepend(
				assistantWithCall("c1", "Write", `{}`),
				modeladapter.Message{Role: "assistant", Content: "提前结束"},
			),
			wantErr: errNativeCheckpointPendingToolResults,
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			assertNativeCheckpointRootRejects(t, testCase.name, testCase.messages, testCase.wantErr)
		})
	}

	t.Run("single message size boundary", func(t *testing.T) {
		base, err := nativeCheckpointPlainMessageJSON("assistant", "")
		if err != nil {
			t.Fatalf("encode base message: %v", err)
		}
		okText := strings.Repeat("a", nativeCheckpointMaxMessageBytes-len(base))
		okEncoded, err := nativeCheckpointPlainMessageJSON("assistant", okText)
		if err != nil {
			t.Fatalf("encode at-limit message: %v", err)
		}
		if len(okEncoded) != nativeCheckpointMaxMessageBytes {
			t.Fatalf("at-limit message length = %d, want %d", len(okEncoded), nativeCheckpointMaxMessageBytes)
		}
		root, err := encodeNativeCheckpointRoot([]modeladapter.Message{{Role: "assistant", Content: okText}}, newCheckpointBlobGraph())
		if err != nil {
			t.Fatalf("encodeNativeCheckpointRoot() at limit error = %v", err)
		}
		if len(root) != 1 {
			t.Fatalf("at-limit root count = %d, want 1", len(root))
		}

		overText := strings.Repeat("a", nativeCheckpointMaxMessageBytes+1-len(base))
		overEncoded, err := nativeCheckpointPlainMessageJSON("assistant", overText)
		if err != nil {
			t.Fatalf("encode over-limit message: %v", err)
		}
		if len(overEncoded) != nativeCheckpointMaxMessageBytes+1 {
			t.Fatalf("over-limit message length = %d, want %d", len(overEncoded), nativeCheckpointMaxMessageBytes+1)
		}
		overRoot, err := encodeNativeCheckpointRoot([]modeladapter.Message{{Role: "assistant", Content: overText}}, newCheckpointBlobGraph())
		if !errors.Is(err, errNativeCheckpointMessageTooLarge) {
			t.Fatalf("over-limit error = %v, want errNativeCheckpointMessageTooLarge", err)
		}
		if overRoot != nil {
			t.Fatalf("over-limit encode returned a non-nil root")
		}
	})
}

// TestNativeCheckpointProjectionDomainPreservesHistory 验证独立原生投影只接受已闭合的纯文本域，
// 域外输入返回固定错误且不修改会话，纯文本投影可重复且确定。
func TestNativeCheckpointProjectionDomainPreservesHistory(t *testing.T) {
	baseConversation := func() *ConversationFile {
		user := testCheckpointUserEntry(t)
		assistant := newAssistantTextEntry(1, "request-1", "基准回答", "", "")
		assistant.Seq = 2
		return &ConversationFile{
			ConversationID: "conversation-native",
			Mode:           "agent",
			NextTurnSeq:    2,
			NextEntrySeq:   3,
			Entries:        []HistoryEntry{user, assistant},
		}
	}

	t.Run("pure local text history succeeds and is deterministic", func(t *testing.T) {
		conversation := baseConversation()
		projector := NewHistoryProjector()
		first, err := projector.projectNativeCheckpointProjection(conversation)
		if err != nil {
			t.Fatalf("projectNativeCheckpointProjection() error = %v", err)
		}
		if first == nil || first.State == nil || len(first.State.GetRootPromptMessagesJson()) != 2 {
			t.Fatalf("native projection = %#v, want two root messages", first)
		}
		second, err := projector.projectNativeCheckpointProjection(conversation)
		if err != nil {
			t.Fatalf("second projectNativeCheckpointProjection() error = %v", err)
		}
		if !proto.Equal(first.State, second.State) {
			t.Fatal("repeated native projection changed state")
		}
		if len(first.Blobs) != len(second.Blobs) {
			t.Fatalf("repeated native projection changed blob count: %d vs %d", len(first.Blobs), len(second.Blobs))
		}
		for index := range first.Blobs {
			if !bytes.Equal(first.Blobs[index].ID, second.Blobs[index].ID) || !bytes.Equal(first.Blobs[index].Data, second.Blobs[index].Data) {
				t.Fatalf("repeated native projection changed blob %d", index)
			}
		}
	})

	modelMessageEntry := func(t *testing.T, message modeladapter.Message) HistoryEntry {
		t.Helper()
		payload, err := json.Marshal(modelMessageEntryPayload{Message: message})
		if err != nil {
			t.Fatalf("marshal model_message payload: %v", err)
		}
		return HistoryEntry{Kind: "model_message", Role: message.Role, Payload: payload, TurnSeq: 1, RequestID: "request-1", Seq: 3}
	}

	cases := []struct {
		name     string
		extra    []HistoryEntry
		imported bool
		wantErr  error
		contains string
	}{
		{
			name:     "compaction summary",
			extra:    []HistoryEntry{{Kind: "compaction_summary", Payload: json.RawMessage(`{"summary":"压缩摘要"}`)}},
			contains: "native checkpoint: compaction not supported",
		},
		{
			name:     "runtime state unknown field",
			extra:    []HistoryEntry{{Kind: "runtime_state", Payload: json.RawMessage(`{"plan_text":"计划","unknown_field":true}`)}},
			contains: "native checkpoint: invalid structured state",
		},
		{
			name:     "runtime state todo unknown field",
			extra:    []HistoryEntry{{Kind: "runtime_state", Payload: json.RawMessage(`{"todos":[{"id":"a","content":"待办","unknown_field":true}]}`)}},
			contains: "native checkpoint: invalid structured state",
		},
		{
			name:     "runtime state invalid plan type",
			extra:    []HistoryEntry{{Kind: "runtime_state", Payload: json.RawMessage(`{"plan_text":123}`)}},
			contains: "native checkpoint: invalid structured state",
		},
		{
			name:    "model message reasoning",
			extra:   []HistoryEntry{modelMessageEntry(t, modeladapter.Message{Role: "assistant", Content: "x", ReasoningContent: "thought"})},
			wantErr: errNativeCheckpointReasoningMetadata,
		},
		{
			name:    "model message image content part",
			extra:   []HistoryEntry{modelMessageEntry(t, modeladapter.Message{Role: "user", Content: "x", ContentParts: []modeladapter.ContentPart{{Type: "image"}}})},
			wantErr: errNativeCheckpointContentParts,
		},
		{
			name:     "model message unknown field",
			extra:    []HistoryEntry{{Kind: "model_message", Role: "assistant", Payload: json.RawMessage(`{"message":{"role":"assistant","content":"x"},"unknown_field":true}`)}},
			contains: "native checkpoint: invalid model message payload",
		},
		{
			name:    "unfinished write tool",
			extra:   []HistoryEntry{modelMessageEntry(t, modeladapter.Message{Role: "assistant", ToolCalls: []modeladapter.ToolCallDescriptor{nativeTestToolCall("call-write", "Write", `{"path":"/tmp/x"}`)}})},
			wantErr: errNativeCheckpointMissingToolResult,
		},
		{
			name:    "ui-only read tool with reasoning",
			extra:   []HistoryEntry{newToolCallEntry(1, "request-1", "call-read", "Read", "ui-only-reasoning", "", checkpointTestReadToolCall(t, nil))},
			wantErr: errNativeCheckpointReasoningMetadata,
		},
		{
			name:     "imported turn ids",
			imported: true,
			contains: "native checkpoint: imported reference graph not supported",
		},
	}
	for _, testCase := range cases {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			conversation := baseConversation()
			conversation.Entries = append(conversation.Entries, testCase.extra...)
			if testCase.imported {
				conversation.ImportedTurnIDs = [][]byte{{1, 2, 3}}
			}
			before, err := json.Marshal(conversation)
			if err != nil {
				t.Fatalf("marshal conversation before: %v", err)
			}
			projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
			if err == nil {
				t.Fatal("projectNativeCheckpointProjection() succeeded for out-of-domain history")
			}
			if projection != nil {
				t.Fatal("projectNativeCheckpointProjection() returned a non-nil projection on error")
			}
			if testCase.wantErr != nil && !errors.Is(err, testCase.wantErr) {
				t.Fatalf("error = %v, want errors.Is(%v)", err, testCase.wantErr)
			}
			if testCase.contains != "" && !strings.Contains(err.Error(), testCase.contains) {
				t.Fatalf("error = %v, want contains %q", err, testCase.contains)
			}
			after, err := json.Marshal(conversation)
			if err != nil {
				t.Fatalf("marshal conversation after: %v", err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("projectNativeCheckpointProjection mutated the conversation on failure")
			}
		})
	}
}

// TestNativeCheckpointRootIdentityAndMetadata 检查身份不能通过空白归一化合并，已知额外字段不能被静默丢弃。
func TestNativeCheckpointRootIdentityAndMetadata(t *testing.T) {
	call := modeladapter.Message{Role: "assistant", ToolCalls: []modeladapter.ToolCallDescriptor{nativeTestToolCall("call", "Write", `{}`)}}
	t.Run("result identity is exact", func(t *testing.T) {
		assertNativeCheckpointRootRejects(t, "exact identity", []modeladapter.Message{call,
			{Role: "tool", ToolCallID: " call ", Name: "Write", Content: ""}}, errNativeCheckpointOrphanToolResult)
	})
	t.Run("unexpected user tool identity is rejected", func(t *testing.T) {
		assertNativeCheckpointRootRejects(t, "unexpected user identity", []modeladapter.Message{
			{Role: "user", Content: "问题", ToolCallID: "call"}}, errNativeCheckpointUnexpectedToolCalls)
	})
}

// TestNativeCheckpointProjectionPreservesFullToolResults 用真实独立方法投影一个完整 Write 工具批次，
// 断言原生 tool-result 保留 result.success.path 与完整正文；现有代码的 replay 压缩会丢 path。
func TestNativeCheckpointProjectionPreservesFullToolResults(t *testing.T) {
	const caseName = "native write tool result"
	startedWrite := buildCompletedWriteToolCall("/tmp/native.txt", "完整正文", nil)
	completedWrite := buildCompletedWriteToolCall("/tmp/native.txt", "完整正文", &agentv1.EditResult{
		Result: &agentv1.EditResult_Success{Success: &agentv1.EditSuccess{
			Path:                 "/tmp/native.txt",
			AfterFullFileContent: "完整正文",
		}},
	})
	conversation := &ConversationFile{
		ConversationID: "conversation-native-write",
		Mode:           "agent",
		NextTurnSeq:    2,
		NextEntrySeq:   4,
		Entries: []HistoryEntry{
			testCheckpointUserEntry(t),
			newToolCallEntry(1, "request-1", "call-write", "Write", "", "", checkpointTestToolCallPayload(t, startedWrite)),
			newToolResultEntry(1, "request-1", "call-write", "Write", `{"path":"/tmp/native.txt","stream_content":"完整正文"}`, "完整正文", "", checkpointTestToolCallPayload(t, completedWrite)),
		},
	}
	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatalf("%s: marshal conversation before: %v", caseName, err)
	}

	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("%s: projectNativeCheckpointProjection() error = %v", caseName, err)
	}
	if projection == nil || projection.State == nil {
		t.Fatalf("%s: projectNativeCheckpointProjection() returned nil projection", caseName)
	}

	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		digest := sha256.Sum256(blob.Data)
		if len(blob.ID) != sha256.Size || !bytes.Equal(blob.ID, digest[:]) {
			t.Fatalf("%s: invalid content-addressed Blob id=%x", caseName, blob.ID)
		}
		repo[string(blob.ID)] = blob.Data
	}
	root := projection.State.GetRootPromptMessagesJson()
	if len(root) != 3 {
		t.Fatalf("%s: native root messages = %d, want user, assistant tool call, and tool result", caseName, len(root))
	}
	var toolContent json.RawMessage
	for index, reference := range root {
		if len(reference) != sha256.Size {
			t.Fatalf("%s: root %d reference length = %d, want %d", caseName, index, len(reference), sha256.Size)
		}
		data, ok := repo[string(reference)]
		if !ok {
			t.Fatalf("%s: root %d is not a content-addressed reference present in the graph", caseName, index)
		}
		view := decodeNativeTestMessage(t, caseName, data)
		if view.Role == "tool" {
			toolContent = view.Content
		}
	}
	if len(toolContent) == 0 {
		t.Fatalf("%s: native root has no tool message", caseName)
	}

	blocks := decodeNativeTestBlocks(t, caseName, toolContent)
	if len(blocks) != 1 || blocks[0].Type != "tool-result" || blocks[0].ToolName != "Write" || blocks[0].ToolCallID != "call-write" {
		t.Fatalf("%s: tool blocks = %#v, want one Write tool-result", caseName, blocks)
	}
	var result struct {
		Success struct {
			Path                 string `json:"path"`
			AfterFullFileContent string `json:"afterFullFileContent"`
			AfterSnake           string `json:"after_full_file_content"`
		} `json:"success"`
	}
	if err := json.Unmarshal(blocks[0].Result, &result); err != nil {
		t.Fatalf("%s: decode tool result: %v", caseName, err)
	}
	if result.Success.Path != "/tmp/native.txt" {
		t.Fatalf("%s: tool result lost path: %s", caseName, compactNativeTestJSON(t, blocks[0].Result))
	}
	afterContent := result.Success.AfterFullFileContent
	if afterContent == "" {
		afterContent = result.Success.AfterSnake
	}
	if afterContent != "完整正文" {
		t.Fatalf("%s: tool result after content = %q, want %q", caseName, afterContent, "完整正文")
	}

	conversationAfter, err := json.Marshal(conversation)
	if err != nil {
		t.Fatalf("%s: marshal conversation after: %v", caseName, err)
	}
	if !bytes.Equal(before, conversationAfter) {
		t.Fatalf("%s: projectNativeCheckpointProjection mutated the conversation", caseName)
	}
}

// TestNativeCheckpointProjectionReadCompatibility 用当前未改动的基线读取器验证新旧格式语义一致。
// 完整 root 是唯一模型历史来源，重复消息和工具实例均应保留。
func TestNativeCheckpointProjectionReadCompatibility(t *testing.T) {
	for _, messages := range [][]modeladapter.Message{
		{{Role: "user", Content: "重复问题"}, {Role: "assistant", Content: "回答"}, {Role: "user", Content: "重复问题"}},
		{{Role: "user", Content: "写文件"}, {Role: "assistant", Content: "准备写入", ToolCalls: []modeladapter.ToolCallDescriptor{
			nativeTestToolCall("write", "Write", `{"path":"/tmp/native.txt","flags":[true,null]}`)}},
			{Role: "tool", ToolCallID: "write", Name: "Write", Content: `{"success":{"path":"/tmp/native.txt","count":9007199254740991}}`}},
	} {
		conversation := &ConversationFile{Mode: "agent", NextTurnSeq: 1}
		for _, message := range messages {
			entry, ok, err := newModelMessageEntry(0, "compatibility", message)
			if err != nil || !ok {
				t.Fatalf("构造双读样本失败: %v", err)
			}
			conversation.Entries = append(conversation.Entries, entry)
		}
		projector := NewHistoryProjector()
		legacy, err := projector.ProjectCheckpointProjection(conversation)
		if err != nil {
			t.Fatal(err)
		}
		native, err := projector.projectNativeCheckpointProjection(conversation)
		if err != nil {
			t.Fatal(err)
		}
		prefetched := make([]*agentv1.PreFetchedBlob, 0, len(native.Blobs))
		for _, blob := range native.Blobs {
			prefetched = append(prefetched, &agentv1.PreFetchedBlob{Id: blob.ID, Value: blob.Data})
		}
		store, err := newImportedBlobStore(prefetched)
		if err != nil {
			t.Fatal(err)
		}
		oldMessages, err := importedConversationStateModelMessages(legacy.State)
		if err != nil {
			t.Fatal(err)
		}
		newMessages, err := importedConversationStateModelMessagesWithBlobs(native.State, store)
		if err != nil {
			t.Fatal(err)
		}
		oldJSON, _ := json.Marshal(oldMessages)
		newJSON, _ := json.Marshal(newMessages)
		if !bytes.Equal(oldJSON, newJSON) || len(newMessages) != len(messages) {
			t.Fatalf("新旧格式恢复语义不一致或重复实例丢失: old=%s new=%s", oldJSON, newJSON)
		}
	}
}

// TestNativeCheckpointProjectionStructuredReferences 固化 §19.3e C0-W2 写侧合同：
// runtime_state 的 plan/todos 必须编码为 raw32 内容引用写入 State.Plan/State.Todos，
// 正文经同一投影 Blob 图存放且逐字段可 protobuf 还原；Plans map 仍内联。
// 投影再经真实 importConversationState 读回，必须得到相同的结构化投影且不丢重复/完整 int64；输入会话不变。
// 旧实现语义 RED：旧实现独立 writer 拒 plan/todos。
func TestNativeCheckpointProjectionStructuredReferences(t *testing.T) {
	planText := "结构化计划：先核对引用，再保留重复的待办实例。"
	todoA := &agentv1.TodoItem{
		Id:           "todo-a",
		Content:      "读取预取的结构化引用",
		Status:       agentv1.TodoStatus_TODO_STATUS_IN_PROGRESS,
		CreatedAt:    9007199254740993,
		UpdatedAt:    math.MaxInt64,
		Dependencies: []string{"todo-root"},
	}
	todoB := &agentv1.TodoItem{
		Id:           "todo-b",
		Content:      "复核失败保全",
		Status:       agentv1.TodoStatus_TODO_STATUS_PENDING,
		CreatedAt:    1,
		UpdatedAt:    9007199254740993,
		Dependencies: []string{"todo-a", "todo-root"},
	}
	runtimePayload, err := json.Marshal(runtimeStateEntryPayload{
		PlanText: planText,
		Plans: map[string]*agentv1.PlanRegistryEntry{
			"current": {Id: "current", Path: "plan://synthetic/structured"},
		},
		Todos: []*agentv1.TodoItem{todoA, todoB, todoA},
	})
	if err != nil {
		t.Fatalf("marshal runtime_state payload: %v", err)
	}
	assistant := newAssistantTextEntry(1, "request-1", "基准回答", "", "")
	assistant.Seq = 2
	conversation := &ConversationFile{
		ConversationID: "conversation-native-structured",
		Mode:           "agent",
		NextTurnSeq:    2,
		NextEntrySeq:   4,
		Entries: []HistoryEntry{
			testCheckpointUserEntry(t),
			assistant,
			{Kind: "runtime_state", Role: "system", Payload: runtimePayload},
		},
	}
	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatalf("marshal conversation before: %v", err)
	}

	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("projectNativeCheckpointProjection() 仍拒绝结构化状态（旧实现语义 RED）: %v", err)
	}
	if projection == nil || projection.State == nil {
		t.Fatal("projectNativeCheckpointProjection() returned nil projection")
	}

	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		digest := sha256.Sum256(blob.Data)
		if len(blob.ID) != sha256.Size || !bytes.Equal(blob.ID, digest[:]) {
			t.Fatalf("invalid content-addressed Blob id=%x", blob.ID)
		}
		repo[string(blob.ID)] = blob.Data
	}

	// plan：raw32 引用 + Blob 正文 protobuf 逐字段还原。
	planBody, err := proto.Marshal(&agentv1.ConversationPlan{Plan: planText})
	if err != nil {
		t.Fatalf("marshal plan: %v", err)
	}
	planDigest := sha256.Sum256(planBody)
	planRef := projection.State.GetPlan()
	if len(planRef) != sha256.Size || !bytes.Equal(planRef, planDigest[:]) {
		t.Fatalf("State.Plan 不是预期 raw32 引用: %x", planRef)
	}
	planBlob, ok := repo[string(planRef)]
	if !ok {
		t.Fatal("State.Plan 引用不在投影 Blob 图中")
	}
	plan := &agentv1.ConversationPlan{}
	if err := proto.Unmarshal(planBlob, plan); err != nil || plan.GetPlan() != planText {
		t.Fatalf("plan 正文 protobuf 还原失败: plan=%q err=%v", plan.GetPlan(), err)
	}

	// todos：raw32 引用、重复实例保留、逐字段还原。
	todoRefs := projection.State.GetTodos()
	if len(todoRefs) != 3 {
		t.Fatalf("State.Todos 数量=%d，预期 3（含重复实例）", len(todoRefs))
	}
	if !bytes.Equal(todoRefs[0], todoRefs[2]) {
		t.Fatalf("重复 todo 实例引用不相等: %x vs %x", todoRefs[0], todoRefs[2])
	}
	for index, want := range []*agentv1.TodoItem{todoA, todoB, todoA} {
		reference := todoRefs[index]
		if len(reference) != sha256.Size {
			t.Fatalf("State.Todos[%d] 不是 raw32 引用: %x", index, reference)
		}
		data, ok := repo[string(reference)]
		if !ok {
			t.Fatalf("State.Todos[%d] 引用不在投影 Blob 图中", index)
		}
		item := &agentv1.TodoItem{}
		if err := proto.Unmarshal(data, item); err != nil {
			t.Fatalf("State.Todos[%d] protobuf 还原失败: %v", index, err)
		}
		if !proto.Equal(item, want) {
			t.Fatalf("State.Todos[%d] 字段不完整或改变: got=%v want=%v", index, item, want)
		}
	}

	// Plans map 仍内联，不是 raw32 引用。
	if plans := projection.State.GetPlans(); plans["current"] == nil || plans["current"].GetPath() != "plan://synthetic/structured" {
		t.Fatalf("Plans map 未保持内联: %#v", plans)
	}

	// 投影不改输入会话。
	after, err := json.Marshal(conversation)
	if err != nil {
		t.Fatalf("marshal conversation after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("projectNativeCheckpointProjection mutated the conversation")
	}

	// 经真实 importConversationState 读回并重新投影，重复实例和完整 int64 不得丢失。
	prefetched := make([]*agentv1.PreFetchedBlob, 0, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		prefetched = append(prefetched, &agentv1.PreFetchedBlob{Id: blob.ID, Value: blob.Data})
	}
	imported, err := newRuntimeConversation("structured-projection-read", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatalf("newRuntimeConversation() error = %v", err)
	}
	entries, err := newImportServiceWithBlobStore(t).importConversationState(imported, projection.State, prefetched)
	if err != nil {
		t.Fatalf("importConversationState() 读取投影结构化引用失败: %v", err)
	}
	runtimeEntry := HistoryEntry{}
	runtimeCount := 0
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) == "runtime_state" {
			runtimeEntry = entry
			runtimeCount++
		}
	}
	if runtimeCount != 1 {
		t.Fatalf("读回 runtime_state entry 数量=%d，预期 1", runtimeCount)
	}

	wantStructured, err := projectConversationStructuredState(conversation)
	if err != nil {
		t.Fatalf("projectConversationStructuredState(原会话) error = %v", err)
	}
	gotStructured, err := projectConversationStructuredState(&ConversationFile{
		ConversationID: "structured-projection-read",
		Entries:        []HistoryEntry{runtimeEntry},
	})
	if err != nil {
		t.Fatalf("projectConversationStructuredState(读回) error = %v", err)
	}
	if !gotStructured.HasPlan || gotStructured.PlanText != wantStructured.PlanText {
		t.Fatalf("读回 plan 不一致: got=%q want=%q", gotStructured.PlanText, wantStructured.PlanText)
	}
	if len(gotStructured.Todos) != len(wantStructured.Todos) {
		t.Fatalf("读回 todo 数量=%d，预期 %d", len(gotStructured.Todos), len(wantStructured.Todos))
	}
	for index := range wantStructured.Todos {
		if !proto.Equal(gotStructured.Todos[index], wantStructured.Todos[index]) {
			t.Fatalf("读回 todo[%d] 字段或整数精度丢失: got=%v want=%v", index, gotStructured.Todos[index], wantStructured.Todos[index])
		}
	}
	if gotStructured.Plans["current"] == nil || gotStructured.Plans["current"].GetPath() != "plan://synthetic/structured" {
		t.Fatalf("读回 Plans map 未保持内联: %#v", gotStructured.Plans)
	}
}

// TestNativeCheckpointProjectionRejectsEmptyTodo 固化 §19.3e C0-W2 写侧空待办边界：
// TodoItem 全零时 proto.Marshal 为 0 字节，若被 graph.add 忽略会写出空引用并在读侧跳过而静默丢实例。
// 必须固定失败、返回 nil projection 且不改输入。旧实现语义 RED。
func TestNativeCheckpointProjectionRejectsEmptyTodo(t *testing.T) {
	assistant := newAssistantTextEntry(1, "request-1", "基准回答", "", "")
	assistant.Seq = 2
	conversation := &ConversationFile{ConversationID: "conversation-native-empty-todo", Mode: "agent", NextTurnSeq: 2, NextEntrySeq: 4,
		Entries: []HistoryEntry{testCheckpointUserEntry(t), assistant,
			{Kind: "runtime_state", Role: "system", Payload: json.RawMessage(`{"todos":[{}]}`)}}}
	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatalf("marshal conversation before: %v", err)
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
	if err == nil || projection != nil {
		t.Fatalf("projectNativeCheckpointProjection() 静默接受了全零 TodoItem（旧实现语义 RED 未触发）: projection=%#v err=%v", projection, err)
	}
	if !strings.Contains(err.Error(), "native checkpoint: empty todo") {
		t.Fatalf("空待办错误分类不符: got=%v want-contains=%q", err, "native checkpoint: empty todo")
	}
	after, err := json.Marshal(conversation)
	if err != nil {
		t.Fatalf("marshal conversation after: %v", err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("空待办失败修改了输入会话")
	}
}

func TestNativeCheckpointMessagesPreservesGroupedToolIdentities(t *testing.T) {
	for _, test := range []struct {
		name    string
		calls   []modeladapter.ToolCallDescriptor
		results []HistoryEntry
		wantErr error
	}{
		{
			name: "result-only-updates-matching-call",
			calls: []modeladapter.ToolCallDescriptor{
				nativeTestToolCall("first", "FirstTool", `{"first":1}`),
				nativeTestToolCall("second", "SecondTool", `{"second":2}`),
			},
			results: []HistoryEntry{
				newToolResultEntry(1, "request", "first", "FirstTool", `{"first":1}`, "first result", "", nil),
				newToolResultEntry(1, "request", "second", "SecondTool", `{"second":2}`, "second result", "", nil),
			},
		},
		{
			name:    "mismatched-result-cannot-rename-call",
			calls:   []modeladapter.ToolCallDescriptor{nativeTestToolCall("first", "FirstTool", `{"first":1}`)},
			results: []HistoryEntry{newToolResultEntry(1, "request", "first", "SecondTool", `{"first":1}`, "result", "", nil)},
			wantErr: errNativeCheckpointToolNameMismatch,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			payload, err := json.Marshal(modelMessageEntryPayload{Message: modeladapter.Message{Role: "assistant", ToolCalls: test.calls}})
			if err != nil {
				t.Fatal(err)
			}
			conversation := &ConversationFile{ConversationID: "grouped-tools", Mode: "agent", NextTurnSeq: 2,
				Entries: append([]HistoryEntry{{Kind: "model_message", TurnSeq: 1, RequestID: "request", Payload: payload}}, test.results...)}
			before, err := json.Marshal(conversation)
			if err != nil {
				t.Fatal(err)
			}
			messages, err := nativeCheckpointMessages(conversation)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) || messages != nil {
					t.Fatalf("失配结果被用于改写调用身份: messages=%+v err=%v", messages, err)
				}
			} else {
				if err != nil || len(messages) != 3 || len(messages[0].ToolCalls) != 2 {
					t.Fatalf("完整工具批次被错误拒绝或改变: messages=%+v err=%v", messages, err)
				}
				for index, want := range test.calls {
					got := messages[0].ToolCalls[index]
					if got.ID != want.ID || got.Function != want.Function {
						t.Fatalf("结果串改同批次调用: call[%d]=%+v want=%+v", index, got, want)
					}
				}
				if projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err != nil || projection == nil {
					t.Fatalf("合法完整批次不能原生写出: projection=%v err=%v", projection, err)
				}
			}
			after, err := json.Marshal(conversation)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(before, after) {
				t.Fatal("工具批次核对修改了原会话")
			}
		})
	}
}

// TestNativeCheckpointProjectionPreservesImportedRawGroups 断言独立原生写出对导入原文组
// 逐字保真、按实际字节哈希、保留重复实例，且摘要原文保持未包装。
func TestNativeCheckpointProjectionPreservesImportedRawGroups(t *testing.T) {
	userRaw := `{"role":"user","content":"official question"}`
	assistantRaw := `{"role":"assistant","content":"official answer"}`
	summaryRaw := `{"role":"user","content":"sum","providerOptions":{"cursor":{"isSummary":true}}}`
	conversation, err := nativeCheckpointImportedConversation(t, userRaw, assistantRaw, userRaw, summaryRaw)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("原生投影失败: %v", err)
	}
	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		repo[string(blob.ID)] = blob.Data
	}
	root := projection.State.GetRootPromptMessagesJson()
	wantRaws := []string{userRaw, assistantRaw, userRaw, summaryRaw}
	if len(root) != len(wantRaws) {
		t.Fatalf("root 数=%d，预期%d", len(root), len(wantRaws))
	}
	for index, want := range wantRaws {
		data, ok := repo[string(root[index])]
		if !ok {
			t.Fatalf("root[%d] 不是内容仓库引用", index)
		}
		if string(data) != want {
			t.Fatalf("root[%d] 正文未被原文保真: got=%s want=%s", index, data, want)
		}
		digest := sha256.Sum256(data)
		if !bytes.Equal(digest[:], root[index]) {
			t.Fatalf("root[%d] 哈希与正文不符", index)
		}
	}
	if !bytes.Equal(root[0], root[2]) {
		t.Fatal("重复实例未被保留为相等引用")
	}
	if strings.Contains(string(repo[string(root[3])]), "conversation_summary") {
		t.Fatal("摘要原文被包装后写出")
	}
}

// TestNativeCheckpointProjectionRejectsTamperedNativeGroups 固化原生组失配/截断/非法原文
// 的固定错误，并确认失败不改动输入。
func TestNativeCheckpointProjectionRejectsTamperedNativeGroups(t *testing.T) {
	callRaw := `{"role":"assistant","content":[{"type":"tool-call","toolCallId":"read-1","toolName":"Read","args":{"path":"/a"}},{"type":"tool-call","toolCallId":"read-2","toolName":"Read","args":{"path":"/b"}}]}`
	resultRaw := `{"role":"tool","content":[{"type":"tool-result","toolCallId":"read-1","toolName":"Read","result":"one"},{"type":"tool-result","toolCallId":"read-2","toolName":"Read","result":"two"}]}`
	build := func(t *testing.T) *ConversationFile {
		t.Helper()
		conversation, err := nativeCheckpointImportedConversation(t, callRaw, resultRaw)
		if err != nil {
			t.Fatal(err)
		}
		return conversation
	}
	mutate := func(t *testing.T, conversation *ConversationFile, index int, edit func(*modelMessageEntryPayload)) {
		t.Helper()
		var payload modelMessageEntryPayload
		if err := json.Unmarshal(conversation.Entries[index].Payload, &payload); err != nil {
			t.Fatal(err)
		}
		edit(&payload)
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		conversation.Entries[index].Payload = encoded
	}
	t.Run("parts-negative", func(t *testing.T) {
		conversation := build(t)
		mutate(t, conversation, 0, func(p *modelMessageEntryPayload) { p.NativeMessageParts = -1 })
		if _, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err != errNativeCheckpointNativeParts {
			t.Fatalf("非法负数 parts 被当成默认值: %v", err)
		}
	})
	t.Run("parts-without-native-message", func(t *testing.T) {
		conversation := build(t)
		mutate(t, conversation, 0, func(p *modelMessageEntryPayload) { p.NativeMessage = nil })
		if _, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err != errNativeCheckpointNativeGroup {
			t.Fatalf("有 parts 无原文被静默降级到旧编码: %v", err)
		}
	})
	t.Run("parts-truncated", func(t *testing.T) {
		conversation := build(t)
		mutate(t, conversation, 1, func(p *modelMessageEntryPayload) { p.NativeMessageParts = 3 })
		if _, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err != errNativeCheckpointNativeParts {
			t.Fatalf("parts 截断未按固定错误失败: %v", err)
		}
	})
	t.Run("non-first-member-has-raw", func(t *testing.T) {
		conversation := build(t)
		mutate(t, conversation, 2, func(p *modelMessageEntryPayload) { p.NativeMessage = json.RawMessage(resultRaw) })
		if _, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err != errNativeCheckpointNativeGroup {
			t.Fatalf("成员重复携带原文未按固定错误失败: %v", err)
		}
	})
	t.Run("view-mismatch", func(t *testing.T) {
		conversation := build(t)
		mutate(t, conversation, 0, func(p *modelMessageEntryPayload) { p.Message.Content = "tampered" })
		if _, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err != errNativeCheckpointNativeViewMismatch {
			t.Fatalf("内容视图失配未按固定错误失败: %v", err)
		}
	})
	t.Run("unsupported-raw", func(t *testing.T) {
		conversation := build(t)
		// 合法 JSON 但 parse 无法表达的原文：走非法原文固定错误。
		mutate(t, conversation, 0, func(p *modelMessageEntryPayload) { p.NativeMessage = json.RawMessage(`{"role":"user"}`) })
		if _, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err != errNativeCheckpointNativePayload {
			t.Fatalf("非法原文未按固定错误失败: %v", err)
		}
	})
	t.Run("failure-preserves-input", func(t *testing.T) {
		conversation := build(t)
		mutate(t, conversation, 0, func(p *modelMessageEntryPayload) { p.Message.Content = "tampered" })
		before, err := json.Marshal(conversation)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err == nil {
			t.Fatal("失配原生组居然投影成功")
		}
		after, err := json.Marshal(conversation)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(before, after) {
			t.Fatal("原生组失配失败改动了输入")
		}
	})
}

// TestNativeRawGroupsSurviveStoreRoundtrip 断言磁盘保存重读后原文组仍按字节保真投影。
func TestNativeRawGroupsSurviveStoreRoundtrip(t *testing.T) {
	userRaw := `{"role":"user","content":"roundtrip question"}`
	assistantRaw := `{"role":"assistant","content":"roundtrip answer"}`
	state, prefetched := prefetchedNativeRootTestState(userRaw, assistantRaw)
	conversation, err := newRuntimeConversation("native-roundtrip", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{}).importConversationState(conversation, state, prefetched)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	store := NewConversationFileStore(t.TempDir())
	if _, err := store.SaveConversationWithEntries(conversation.ConversationID, conversation, entries); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	loaded, err := store.LoadConversation(conversation.ConversationID)
	if err != nil || loaded == nil {
		t.Fatalf("重读失败: %v", err)
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(loaded)
	if err != nil {
		t.Fatalf("重读后原生投影失败: %v", err)
	}
	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		repo[string(blob.ID)] = blob.Data
	}
	root := projection.State.GetRootPromptMessagesJson()
	if len(root) != 2 {
		t.Fatalf("root 数=%d，预期2", len(root))
	}
	// 磁盘落盘可规范化 JSON 空白，因此按语义（紧凑 JSON）比较原文保真。
	if !bytes.Equal(compactTestJSON(t, repo[string(root[0])]), compactTestJSON(t, []byte(userRaw))) ||
		!bytes.Equal(compactTestJSON(t, repo[string(root[1])]), compactTestJSON(t, []byte(assistantRaw))) {
		t.Fatalf("重读后原文未保真: %q %q", repo[string(root[0])], repo[string(root[1])])
	}
}

// TestNativeToolGroupsSurviveStoreRoundtrip 覆盖嵌套参数/结果经落盘缩进后仍匹配，
// 同时保留数字精度、字符串内空白、分组、重复工具批次与未包装摘要。
func TestNativeToolGroupsSurviveStoreRoundtrip(t *testing.T) {
	callRaw := ` { "role": "assistant", "content": [{"type":"tool-call","toolCallId":"read-1","toolName":"Read","args": { "nested": [9007199254740991, {"text":"  <中文>&  "}] }},{"type":"tool-call","toolCallId":"read-2","toolName":"Read","args": {"path":"/b"}}] } `
	resultRaw := `{"role":"tool","content":[{"type":"tool-result","toolCallId":"read-1","toolName":"Read","result": { "nested": [9007199254740991, {"text":"  <中文>&  "}] }},{"type":"tool-result","toolCallId":"read-2","toolName":"Read","result":"  { \"x\": 1 }  "}]}`
	summaryRaw := `{"role":"user","content":"summary","providerOptions":{"cursor":{"isSummary":true}}}`
	raws := []string{callRaw, resultRaw, callRaw, resultRaw, summaryRaw}
	conversation, err := nativeCheckpointImportedConversation(t, raws...)
	if err != nil {
		t.Fatal(err)
	}
	store := NewConversationFileStore(t.TempDir())
	if _, err := store.SaveConversationWithEntries(conversation.ConversationID, conversation, conversation.Entries); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(loaded)
	if err != nil {
		t.Fatalf("原生工具组落盘重读后不能再投影: %v", err)
	}
	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		repo[string(blob.ID)] = blob.Data
	}
	root := projection.State.GetRootPromptMessagesJson()
	if len(root) != len(raws) {
		t.Fatalf("root=%d want=%d", len(root), len(raws))
	}
	for index, raw := range raws {
		data := repo[string(root[index])]
		if !bytes.Equal(compactTestJSON(t, data), compactTestJSON(t, []byte(raw))) {
			t.Fatalf("root[%d] 改变原文内容或分组: %s", index, data)
		}
		digest := sha256.Sum256(data)
		if !bytes.Equal(root[index], digest[:]) {
			t.Fatalf("root[%d] 哈希与实际保存正文不符", index)
		}
	}
	var payload modelMessageEntryPayload
	if err := json.Unmarshal(loaded.Entries[2].Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Message.Content != `  { "x": 1 }  ` {
		t.Fatalf("字符串结果内空白被规范化: %q", payload.Message.Content)
	}
}

// TestNativeCheckpointRawToolResultTypes 保留原文已明确的字符串结果类型，
// 不套用本地重编码对 JSON 字符串/数值的歧义判断。
func TestNativeCheckpointRawToolResultTypes(t *testing.T) {
	callRaw := `{"role":"assistant","content":[{"type":"tool-call","toolCallId":"read","toolName":"Read","args":{}}]}`
	for _, result := range []string{`"\"42\""`, `"1.5"`, `"{\"count\":9007199254740993}"`} {
		t.Run(result, func(t *testing.T) {
			resultRaw := `{"role":"tool","content":[{"type":"tool-result","toolCallId":"read","toolName":"Read","result":` + result + `}]}`
			conversation, err := nativeCheckpointImportedConversation(t, callRaw, resultRaw)
			if err != nil {
				t.Fatal(err)
			}
			projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
			if err != nil {
				t.Fatalf("已有原文类型的合法字符串结果被拒绝: %v", err)
			}
			root := projection.State.GetRootPromptMessagesJson()
			if len(root) != 2 {
				t.Fatalf("root=%d want=2", len(root))
			}
			var saved []byte
			for _, blob := range projection.Blobs {
				if bytes.Equal(blob.ID, root[1]) {
					saved = blob.Data
				}
			}
			if !bytes.Equal(compactTestJSON(t, saved), compactTestJSON(t, []byte(resultRaw))) {
				t.Fatalf("原文字符串结果被改为其它类型: %s", saved)
			}
		})
	}
}

// compactTestJSON 按保存时的紧凑及 HTML 转义规则比较 JSON，保留字符串内容与数值原文。
func compactTestJSON(t *testing.T, raw []byte) []byte {
	t.Helper()
	encoded, err := json.Marshal(json.RawMessage(raw))
	if err != nil {
		t.Fatalf("规范化 JSON 失败: %v", err)
	}
	return encoded
}

// nativeCheckpointImportedConversation 通过真实导入入口构造带原生原文组的会话。
func nativeCheckpointImportedConversation(t *testing.T, raws ...string) (*ConversationFile, error) {
	t.Helper()
	state, prefetched := prefetchedNativeRootTestState(raws...)
	conversation, err := newRuntimeConversation("native-imported", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		return nil, err
	}
	entries, err := (&Service{}).importConversationState(conversation, state, prefetched)
	if err != nil {
		return nil, err
	}
	appendEntriesInPlace(conversation, entries)
	return conversation, nil
}

// TestNativeCheckpointProjectionMixedLocalAndNative 断言原生原文组与本地生成段按 entry 顺序
// 切段：原生段按原文输出，本地段仍走既有生成编码，整段不产生孤立工具结果。
func TestNativeCheckpointProjectionMixedLocalAndNative(t *testing.T) {
	userRaw := `{"role":"user","content":"native question"}`
	assistantRaw := `{"role":"assistant","content":"native answer"}`
	conversation, err := nativeCheckpointImportedConversation(t, userRaw, assistantRaw)
	if err != nil {
		t.Fatal(err)
	}
	localUser, ok, err := newModelMessageEntry(2, "local-request", modeladapter.Message{Role: "user", Content: "local question"})
	if err != nil || !ok {
		t.Fatalf("本地 user entry: ok=%v err=%v", ok, err)
	}
	localAssistant, ok, err := newModelMessageEntry(2, "local-request", modeladapter.Message{Role: "assistant", Content: "local answer"})
	if err != nil || !ok {
		t.Fatalf("本地 assistant entry: ok=%v err=%v", ok, err)
	}
	appendEntriesInPlace(conversation, []HistoryEntry{localUser, localAssistant})
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("混合投影失败: %v", err)
	}
	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		repo[string(blob.ID)] = blob.Data
	}
	root := projection.State.GetRootPromptMessagesJson()
	if len(root) != 4 {
		t.Fatalf("root 数=%d，预期4", len(root))
	}
	if string(repo[string(root[0])]) != userRaw || string(repo[string(root[1])]) != assistantRaw {
		t.Fatalf("混合中原生组未按原文输出: %q %q", repo[string(root[0])], repo[string(root[1])])
	}
	for index, wantRole := range []string{"user", "assistant"} {
		var core nativeRootCoreMessage
		if err := json.Unmarshal(repo[string(root[2+index])], &core); err != nil {
			t.Fatal(err)
		}
		if core.Role != wantRole {
			t.Fatalf("本地段 role=%q want=%q", core.Role, wantRole)
		}
		var content string
		if err := json.Unmarshal(core.Content, &content); err != nil {
			t.Fatal(err)
		}
		if content == "" {
			t.Fatalf("本地段正文为空")
		}
	}
}

// TestNativeCheckpointProjectionWritesImportedTurnGraphFromContentStore 有内容源时独立 native
// 写出必须纳入导入回合图原字节；无源时既有拒绝合同保持不变。
func TestNativeCheckpointProjectionWritesImportedTurnGraphFromContentStore(t *testing.T) {
	graph := newTestImportedTurnGraph(t)
	blobStore := NewContentBlobStore(t.TempDir())
	conversation, err := newRuntimeConversation("native-imported-graph", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{contentBlobs: blobStore}).importConversationState(conversation, graph.state, graph.blobs)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	appendEntriesInPlace(conversation, entries)
	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}

	if projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err == nil || projection != nil {
		t.Fatal("无内容源时 imported turn 图居然写出成功")
	} else if !strings.Contains(err.Error(), "imported reference graph not supported") {
		t.Fatalf("无源错误分类被改写: %v", err)
	}
	afterNoSource, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, afterNoSource) {
		t.Fatal("无源失败改动了会话")
	}

	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation, blobStore)
	if err != nil {
		t.Fatalf("有源 native 投影失败（旧实现语义 RED）: %v", err)
	}
	after, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("有源投影改动了输入会话")
	}
	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		repo[string(blob.ID)] = blob.Data
	}
	if !bytes.Equal(repo[string(graph.turnID)], graph.turnBody) || !bytes.Equal(repo[string(graph.userID)], graph.userBody) || !bytes.Equal(repo[string(graph.stepID)], graph.stepBody) {
		t.Fatal("有源投影未写入原 Turn/User/Step 正文")
	}
	root := projection.State.GetRootPromptMessagesJson()
	if len(root) != 1 || !bytes.Equal(compactTestJSON(t, repo[string(root[0])]), compactTestJSON(t, []byte(graph.rootRaw))) {
		t.Fatalf("有源投影把回合图拼进 root: %d", len(root))
	}
}

func TestNativeCheckpointProjectionWritesImportedSummaryGraphFromContentStore(t *testing.T) {
	graph := newTestImportedSummaryGraph(t)
	blobStore := NewContentBlobStore(t.TempDir())
	conversation, err := newRuntimeConversation("native-imported-summary", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{contentBlobs: blobStore}).importConversationState(conversation, graph.state, graph.blobs)
	if err != nil {
		t.Fatalf("导入失败: %v", err)
	}
	appendEntriesInPlace(conversation, entries)
	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err == nil || projection != nil {
		t.Fatal("无内容源时 native summary 图居然写出成功")
	} else if !strings.Contains(err.Error(), "imported native summary graph not supported") {
		t.Fatalf("无源错误分类被改写: %v", err)
	}
	afterNoSource, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, afterNoSource) {
		t.Fatal("无源失败改动了会话")
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation, blobStore)
	if err != nil {
		t.Fatalf("有源 native 摘要投影失败（旧实现语义 RED）: %v", err)
	}
	if !bytes.Equal(projection.State.GetSummary(), graph.summaryID) || !bytes.Equal(projection.State.GetSummaryArchive(), graph.archiveID) {
		t.Fatal("有源投影未整组覆盖 Summary/Archive 原 ID")
	}
}

func TestNativeCheckpointProjectionKeepsNativeSummaryAfterSourcelessRuntimeUpdate(t *testing.T) {
	graph := newTestImportedSummaryGraph(t)
	blobStore := NewContentBlobStore(t.TempDir())
	conversation, err := newRuntimeConversation("native-summary-keep", agentv1.AgentMode_AGENT_MODE_AGENT)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := (&Service{contentBlobs: blobStore}).importConversationState(conversation, graph.state, graph.blobs)
	if err != nil {
		t.Fatal(err)
	}
	appendEntriesInPlace(conversation, entries)
	later, err := json.Marshal(runtimeStateEntryPayload{PlanText: "later plan only"})
	if err != nil {
		t.Fatal(err)
	}
	conversation.Entries = append(conversation.Entries, HistoryEntry{Kind: "runtime_state", Role: "system", Payload: later})
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation, blobStore)
	if err != nil {
		t.Fatalf("后追加无源 runtime 后投影失败（旧实现语义 RED）: %v", err)
	}
	if !bytes.Equal(projection.State.GetSummary(), graph.summaryID) || !bytes.Equal(projection.State.GetSummaryArchive(), graph.archiveID) {
		t.Fatal("后追加无源 runtime_state 擦除了 native 摘要来源")
	}
	if string(projection.State.GetPlan()) == "" {
		// plan is raw32 in native writer; just ensure later plan was applied via structured overlay
	}
	plan := &agentv1.ConversationPlan{}
	found := false
	for _, blob := range projection.Blobs {
		if proto.Unmarshal(blob.Data, plan) == nil && plan.GetPlan() == "later plan only" {
			found = true
			break
		}
	}
	if !found {
		t.Fatal("后追加 plan 未进入投影")
	}
}

func TestNativeCheckpointProjectionRejectsMismatchedNativeSummarySource(t *testing.T) {
	payload, err := json.Marshal(runtimeStateEntryPayload{NativeSummary: []byte("abcd")})
	if err != nil {
		t.Fatal(err)
	}
	conversation := &ConversationFile{
		ConversationID: "native-summary-mismatch",
		Mode:           "agent",
		NextTurnSeq:    2,
		Entries: []HistoryEntry{
			testCheckpointUserEntry(t),
			newAssistantTextEntry(1, "request-1", "hi", "", ""),
			{Kind: "runtime_state", Role: "system", Payload: payload},
		},
	}
	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation, NewContentBlobStore(t.TempDir())); err == nil || projection != nil {
		t.Fatal("非法长度 native_summary 居然投影成功")
	}
	after, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("失配来源失败改动了会话")
	}
}

func TestNativeCheckpointProjectionWritesLocalCompactionArchives(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil || plan == nil {
		t.Fatalf("build plan error = %v", err)
	}
	if err := applyCompactionToConversation(conversation, newPendingCompaction(plan), "current progress summary"); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	store := NewConversationFileStore(t.TempDir())
	if _, err := store.SaveConversationWithEntries(conversation.ConversationID, conversation, conversation.Entries); err != nil {
		t.Fatalf("save error = %v", err)
	}
	loaded, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatalf("load error = %v", err)
	}
	before, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(loaded)
	if err != nil {
		t.Fatalf("native writer error = %v", err)
	}
	after, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("native writer 改动了会话")
	}
	if len(projection.State.GetSummaryArchive()) != 0 {
		t.Fatal("纯本地 singular 应为空")
	}
	archives := projection.State.GetSummaryArchives()
	if len(archives) != 1 {
		t.Fatalf("archives=%d, want 1", len(archives))
	}
	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		repo[string(blob.ID)] = blob.Data
	}
	archive := &agentv1.ConversationSummaryArchive{}
	if proto.Unmarshal(repo[string(archives[0])], archive) != nil {
		t.Fatal("archive 不是 ConversationSummaryArchive")
	}
	if archive.GetWindowTail() != 0 || archive.GetSummary() != "current progress summary" {
		t.Fatalf("archive summary/window_tail = %#v", archive)
	}
	if len(archive.GetSummarizedMessages()) != 2 || len(archive.GetSummaryMessage()) != sha256.Size {
		t.Fatalf("archive 子图不完整: messages=%d summary_message=%d", len(archive.GetSummarizedMessages()), len(archive.GetSummaryMessage()))
	}
	for _, id := range archive.GetSummarizedMessages() {
		if len(id) != sha256.Size || len(repo[string(id)]) == 0 {
			t.Fatal("summarized_messages 缺 raw32 正文")
		}
	}
	if len(repo[string(archive.GetSummaryMessage())]) == 0 {
		t.Fatal("summary_message 缺正文")
	}
	summary := &agentv1.ConversationSummary{}
	if proto.Unmarshal(repo[string(projection.State.GetSummary())], summary) != nil || summary.GetSummary() != "current progress summary" {
		t.Fatal("State.Summary 不是最新 ConversationSummary")
	}
	root := projection.State.GetRootPromptMessagesJson()
	if len(root) != 3 {
		t.Fatalf("root 数=%d, want isSummary+连续尾 2 条", len(root))
	}
	if !bytes.Contains(repo[string(root[0])], []byte(`"isSummary":true`)) {
		t.Fatalf("root[0] 不是 isSummary: %s", repo[string(root[0])])
	}
	if bytes.Contains(repo[string(root[0])], []byte("conversation_summary")) {
		t.Fatal("保存原文被包装")
	}
}

func TestNativeCheckpointLocalCompactionRootKeepsUnclippedPreservedToolResult(t *testing.T) {
	const caseName = "native local compaction unclipped tail"
	largeResult := strings.Repeat("N", autoCompactionPreservedToolResultLimitBytes+2048)
	conversation := &ConversationFile{
		ConversationID:         "conversation-native-unclipped-tail",
		RootConversationID:     "conversation-native-unclipped-tail",
		Mode:                   "agent",
		NextTurnSeq:            3,
		TokenDetailsUsedTokens: 42_000,
		TokenDetailsMaxTokens:  50_000,
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "first question", "message-1"),
		newAssistantTextEntry(1, "request-1", "first answer", "", ""),
		compactionTestUserEntry(t, 2, "request-2", "current question", "message-2"),
		tailReadToolCallEntry(t, 2, "request-2", "call-1", "model-call-1"),
	})
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil || plan == nil {
		t.Fatalf("buildAutoCompactionPlanFromHistory() error = %v plan=%#v", err, plan)
	}
	// 在工具完成前冻结旧回合边界，当前输入仍在连续尾中，不需独立重放。
	// 带非空固定引用的新域由专门的拒绝回归覆盖，不能借本例放宽原生门禁。
	if plan.Layout == nil || len(plan.Layout.PinnedInputs) != 0 {
		t.Fatal("未构造出原生可表示的空固定引用边界")
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		tailReadToolResultEntry(t, 2, "request-2", "call-1", "model-call-1", "/tmp/large.txt", largeResult),
	})
	if err := applyCompactionToConversation(conversation, newPendingCompaction(plan), "current progress summary"); err != nil {
		t.Fatalf("applyCompactionToConversation() error = %v", err)
	}
	if got := historyToolResultText(t, conversation.Entries, "call-1"); got != largeResult {
		t.Fatalf("apply 改写了 canonical 工具结果: len=%d want %d", len(got), len(largeResult))
	}

	projected := compactedPromptProjectionEntries(conversation.Entries)
	if got := historyToolResultText(t, projected, "call-1"); len(got) >= len(largeResult) {
		t.Fatalf("默认 model 投影未裁剪保留集合结果: len=%d", len(got))
	}

	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("%s: projectNativeCheckpointProjection() error = %v", caseName, err)
	}
	after, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("native writer 改动了会话")
	}
	if got := nativeRootToolResultString(t, caseName, projection, "call-1"); got != largeResult {
		t.Fatalf("%s: native root 活动尾丢完整工具结果: len=%d want %d", caseName, len(got), len(largeResult))
	}

	store := NewConversationFileStore(t.TempDir())
	if _, err := store.SaveConversationWithEntries(conversation.ConversationID, conversation, conversation.Entries); err != nil {
		t.Fatalf("save error = %v", err)
	}
	loaded, err := store.LoadConversation(conversation.ConversationID)
	if err != nil {
		t.Fatalf("load error = %v", err)
	}
	reloaded, err := NewHistoryProjector().projectNativeCheckpointProjection(loaded)
	if err != nil {
		t.Fatalf("磁盘重读 native writer error = %v", err)
	}
	if got := nativeRootToolResultString(t, caseName, reloaded, "call-1"); got != largeResult {
		t.Fatalf("磁盘重读后 native root 仍丢完整工具结果: len=%d want %d", len(got), len(largeResult))
	}
}

func historyToolResultText(t *testing.T, entries []HistoryEntry, toolCallID string) string {
	t.Helper()
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "tool_result" {
			continue
		}
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatalf("decode tool_result: %v", err)
		}
		if strings.TrimSpace(payload.ToolCallID) == toolCallID {
			return payload.ResultText
		}
	}
	t.Fatalf("missing tool_result %s", toolCallID)
	return ""
}

func nativeRootToolResultString(t *testing.T, caseName string, projection *CheckpointProjection, toolCallID string) string {
	t.Helper()
	if projection == nil || projection.State == nil {
		t.Fatalf("%s: nil native projection", caseName)
	}
	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		digest := sha256.Sum256(blob.Data)
		if len(blob.ID) != sha256.Size || !bytes.Equal(blob.ID, digest[:]) {
			t.Fatalf("%s: invalid content-addressed Blob id=%x", caseName, blob.ID)
		}
		repo[string(blob.ID)] = blob.Data
	}
	for index, reference := range projection.State.GetRootPromptMessagesJson() {
		if len(reference) != sha256.Size {
			t.Fatalf("%s: root %d reference length = %d, want %d", caseName, index, len(reference), sha256.Size)
		}
		data, ok := repo[string(reference)]
		if !ok {
			t.Fatalf("%s: root %d missing blob", caseName, index)
		}
		view := decodeNativeTestMessage(t, caseName, data)
		if view.Role != "tool" {
			continue
		}
		blocks := decodeNativeTestBlocks(t, caseName, view.Content)
		for _, block := range blocks {
			if block.Type != "tool-result" || block.ToolCallID != toolCallID {
				continue
			}
			var result string
			if err := json.Unmarshal(block.Result, &result); err != nil {
				t.Fatalf("%s: native tool-result 不是完整字符串正文: %v body=%s", caseName, err, compactNativeTestJSON(t, block.Result))
			}
			return result
		}
	}
	t.Fatalf("%s: native root 缺少 tool-result %s", caseName, toolCallID)
	return ""
}

func TestNativeCheckpointProjectionLocalCompactionHonorsCancelAndFork(t *testing.T) {
	conversation := &ConversationFile{ConversationID: "conversation-cancel-fork", Mode: "agent", NextTurnSeq: 4}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 1, "request-1", "dropped question", "message-1"),
		newAssistantTextEntry(1, "request-1", "dropped answer", "", ""),
		newMetadataEntry(1, "request-1", "control", map[string]any{"status": "canceled", "replay_policy": "drop_turn"}),
		compactionTestUserEntry(t, 2, "request-2", "kept question", "message-2"),
		newAssistantTextEntry(2, "request-2", "kept answer", "", ""),
		compactionTestUserEntry(t, 3, "request-3", "current question", "message-3"),
	})
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            3,
		CurrentRequestID:          "request-3",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil || plan == nil || plan.NativeArchiveSource == nil {
		t.Fatalf("plan error = %v %#v", err, plan)
	}
	if plan.NativeArchiveSource.ActualCount != 2 {
		t.Fatalf("取消后计数 = %d, want 2 (turn 2 only)", plan.NativeArchiveSource.ActualCount)
	}
	if err := applyCompactionToConversation(conversation, newPendingCompaction(plan), "cancel summary"); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("native writer error = %v", err)
	}
	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		repo[string(blob.ID)] = blob.Data
	}
	archive := &agentv1.ConversationSummaryArchive{}
	if proto.Unmarshal(repo[string(projection.State.GetSummaryArchives()[0])], archive) != nil {
		t.Fatal("decode archive")
	}
	for _, id := range archive.GetSummarizedMessages() {
		if bytes.Contains(repo[string(id)], []byte("dropped question")) {
			t.Fatal("取消回合进入了 archive")
		}
	}
	forked := cloneConversationFile(conversation)
	forked.Entries = prefixEntriesBeforeTurn(conversation.Entries, 3)
	forked.NextTurnSeq = 3
	if _, err := NewHistoryProjector().projectNativeCheckpointProjection(forked); err != nil {
		t.Fatalf("fork 前缀 native writer error = %v", err)
	}
}

func TestNativeCheckpointProjectionLocalCompactionRawGroupAndDuplicates(t *testing.T) {
	resultRaw := `{"role":"tool","content":[{"type":"tool-result","toolCallId":"read-1","toolName":"Read","result":"one"},{"type":"tool-result","toolCallId":"read-2","toolName":"Read","result":"two"}]}`
	callRaw := `{"role":"assistant","content":[{"type":"tool-call","toolCallId":"read-1","toolName":"Read","args":{"path":"/a"}},{"type":"tool-call","toolCallId":"read-2","toolName":"Read","args":{"path":"/b"}}]}`
	userRaw := `{"role":"user","content":"old question"}`
	conversation, err := nativeCheckpointImportedConversation(t, userRaw, callRaw, resultRaw, userRaw)
	if err != nil {
		t.Fatal(err)
	}
	appendEntriesInPlace(conversation, []HistoryEntry{
		compactionTestUserEntry(t, 2, "request-2", "current question", "message-2"),
	})
	plan, err := (&Service{}).buildAutoCompactionPlanFromHistory(&compactionPlan{
		Trigger:                   "auto",
		CurrentTurnSeq:            2,
		CurrentRequestID:          "request-2",
		PreserveCurrentTurnInputs: true,
		ContextWindowSize:         1_000_000,
		IsFirstCompaction:         true,
	}, conversation)
	if err != nil || plan == nil || plan.NativeArchiveSource == nil {
		t.Fatalf("plan error = %v %#v", err, plan)
	}
	if plan.NativeArchiveSource.ActualCount != 4 {
		t.Fatalf("raw 组计数 = %d, want 4 (user+call+result组+重复user)", plan.NativeArchiveSource.ActualCount)
	}
	if err := applyCompactionToConversation(conversation, newPendingCompaction(plan), "raw group summary"); err != nil {
		t.Fatalf("apply error = %v", err)
	}
	projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation)
	if err != nil {
		t.Fatalf("native writer error = %v", err)
	}
	repo := make(map[string][]byte, len(projection.Blobs))
	for _, blob := range projection.Blobs {
		repo[string(blob.ID)] = blob.Data
	}
	archive := &agentv1.ConversationSummaryArchive{}
	if proto.Unmarshal(repo[string(projection.State.GetSummaryArchives()[0])], archive) != nil {
		t.Fatal("decode archive")
	}
	if len(archive.GetSummarizedMessages()) != 4 {
		t.Fatalf("archive messages=%d, want 4", len(archive.GetSummarizedMessages()))
	}
}

func TestNativeCheckpointProjectionRejectsBadLocalArchiveSource(t *testing.T) {
	conversation := compactionAppendOnlyConversation(t)
	if err := applyCompactionToConversation(conversation, &PendingCompaction{
		Trigger:          "auto",
		CurrentTurnSeq:   2,
		CurrentRequestID: "request-2",
	}, "old summary"); err != nil {
		t.Fatal(err)
	}
	before, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err == nil || projection != nil {
		t.Fatal("缺来源压缩 native 写出居然成功")
	}
	after, err := json.Marshal(conversation)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(before, after) {
		t.Fatal("坏来源失败改动了会话")
	}
	var payload compactionSummaryEntryPayload
	for _, entry := range conversation.Entries {
		if !isCompactionSummaryKind(entry.Kind) {
			continue
		}
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		payload.NativeArchiveSource = json.RawMessage(`{"prefix_start":0,"prefix_end":2,"candidate_count":2,"actual_count":2}`)
		encoded, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		entry.Payload = encoded
		conversation.Entries[len(conversation.Entries)-1] = entry
	}
	if projection, err := NewHistoryProjector().projectNativeCheckpointProjection(conversation); err == nil || projection != nil {
		t.Fatal("缺 window_tail 的来源 native 写出居然成功")
	}
}
