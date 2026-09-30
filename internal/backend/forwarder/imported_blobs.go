package forwarder

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptengine "cursor/internal/backend/agent/prompt"
)

type importedBlobStore map[string][]byte

func newImportedBlobStore(items []*agentv1.PreFetchedBlob) (importedBlobStore, error) {
	if len(items) == 0 {
		return nil, nil
	}
	store := make(importedBlobStore, len(items))
	for _, item := range items {
		if item == nil || len(item.GetId()) == 0 {
			continue
		}
		if len(item.GetId()) != sha256.Size {
			return nil, fmt.Errorf("prefetched blob id length %d, want %d", len(item.GetId()), sha256.Size)
		}
		digest := sha256.Sum256(item.GetValue())
		if string(digest[:]) != string(item.GetId()) {
			return nil, fmt.Errorf("prefetched blob %x failed SHA-256 validation", item.GetId())
		}
		store[string(item.GetId())] = append([]byte(nil), item.GetValue()...)
	}
	return store, nil
}

func (store importedBlobStore) resolve(id []byte) ([]byte, bool) {
	if len(id) == 0 || len(store) == 0 {
		return nil, false
	}
	value, ok := store[string(id)]
	return append([]byte(nil), value...), ok
}

// resolveImportedRootMessages 只解析内容引用，保留消息顺序及重复引用。
// native=true 且 resolved=nil 表示所有引用均缺失，由调用者决定既有 turns 回退。
func resolveImportedRootMessages(rawItems [][]byte, blobs importedBlobStore) (resolved [][]byte, native bool, err error) {
	hasInline := false
	for _, raw := range rawItems {
		if len(raw) == 0 {
			continue
		}
		if json.Valid(raw) || len(raw) != sha256.Size {
			hasInline = true
		} else {
			native = true
		}
	}
	if !native {
		return rawItems, false, nil
	}
	if hasInline {
		return nil, true, fmt.Errorf("mixed inline and referenced root messages")
	}
	missing := false
	for _, raw := range rawItems {
		if len(raw) == 0 {
			continue
		}
		data, ok := blobs.resolve(raw)
		if !ok {
			missing = true
			continue
		}
		resolved = append(resolved, data)
	}
	if missing && len(resolved) > 0 {
		return nil, true, fmt.Errorf("incomplete prefetched root message blobs")
	}
	return resolved, true, nil
}

// decodeImportedNativeMessages 在导入边界将 CoreMessage 转成现有 replay，
// 不改变本地编码；无法表达的内容块明确失败，不能静默忽略。
func decodeImportedNativeMessages(rawItems [][]byte) ([]promptengine.Message, error) {
	messages := make([]promptengine.Message, 0, len(rawItems))
	for _, raw := range rawItems {
		var item struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if !json.Valid(raw) || decoder.Decode(&item) != nil || len(item.Content) == 0 {
			return nil, fmt.Errorf("invalid or unsupported native root message")
		}
		switch item.Role {
		case "system", "user", "assistant", "tool":
		default:
			return nil, fmt.Errorf("invalid native root message role")
		}
		content := strings.TrimSpace(string(item.Content))
		if strings.HasPrefix(content, `"`) {
			var text string
			if err := json.Unmarshal(item.Content, &text); err != nil || item.Role == "tool" {
				return nil, fmt.Errorf("invalid native string message content")
			}
			if item.Role != "system" {
				messages = append(messages, promptengine.Message{Role: item.Role, Content: text})
			}
			continue
		}
		if item.Role == "system" || !strings.HasPrefix(content, "[") {
			return nil, fmt.Errorf("invalid native message content")
		}
		decoded, err := decodeImportedNativeContentParts(item.Role, item.Content)
		if err != nil {
			return nil, err
		}
		messages = append(messages, decoded...)
	}
	if err := validateImportedNativeToolSequence(messages); err != nil {
		return nil, err
	}
	return messages, nil
}

// validateImportedNativeToolSequence 在既有回放归一化之前拒绝不完整工具批次，
// 防止悬空调用被裁掉、孤立结果被接受，或错误关联被固化为本地历史。
func validateImportedNativeToolSequence(messages []promptengine.Message) error {
	pending := make(map[string]string)
	for _, message := range messages {
		if message.Role == "tool" {
			name, ok := pending[message.ToolCallID]
			if !ok || name != message.Name {
				return fmt.Errorf("native tool result has no matching call")
			}
			delete(pending, message.ToolCallID)
			continue
		}
		if len(pending) > 0 {
			return fmt.Errorf("native tool batch interrupted before results")
		}
		for _, call := range message.ToolCalls {
			if _, exists := pending[call.ID]; exists {
				return fmt.Errorf("duplicate native tool call id in batch")
			}
			pending[call.ID] = call.Function.Name
		}
	}
	if len(pending) > 0 {
		return fmt.Errorf("native tool calls missing results")
	}
	return nil
}

func decodeImportedNativeContentParts(role string, rawContent json.RawMessage) ([]promptengine.Message, error) {
	var parts []struct {
		Type                string          `json:"type"`
		Text                *string         `json:"text"`
		ToolCallID          string          `json:"toolCallId"`
		ToolName            string          `json:"toolName"`
		Args                json.RawMessage `json:"args"`
		Result              json.RawMessage `json:"result"`
		IsError             bool            `json:"isError"`
		ExperimentalContent json.RawMessage `json:"experimental_content"`
	}
	decoder := json.NewDecoder(bytes.NewReader(rawContent))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&parts); err != nil || len(parts) == 0 {
		return nil, fmt.Errorf("invalid or unsupported native content parts")
	}
	messages := make([]promptengine.Message, 0, len(parts))
	current := promptengine.Message{Role: role}
	for _, part := range parts {
		if part.Type != "tool-result" && (part.IsError || len(part.ExperimentalContent) > 0) {
			return nil, fmt.Errorf("invalid native content part metadata")
		}
		switch part.Type {
		case "text":
			if role == "tool" || part.Text == nil || part.ToolCallID != "" || part.ToolName != "" || len(part.Args) > 0 || len(part.Result) > 0 {
				return nil, fmt.Errorf("invalid native text part")
			}
			if len(current.ToolCalls) > 0 {
				// 现有消息形状不能保留调用与后续文本的块级顺序。
				return nil, fmt.Errorf("unsupported native text after tool call")
			}
			current.Content += *part.Text
		case "tool-call":
			if role != "assistant" || strings.TrimSpace(part.ToolCallID) == "" || strings.TrimSpace(part.ToolName) == "" || len(part.Args) == 0 || part.Text != nil || len(part.Result) > 0 {
				return nil, fmt.Errorf("invalid native tool-call part")
			}
			if isProviderPromptReplaySuppressedToolName(part.ToolName) {
				return nil, fmt.Errorf("unsupported native tool replay")
			}
			current.ToolCalls = append(current.ToolCalls, promptengine.ToolCallDescriptor{
				ID: part.ToolCallID, Index: len(current.ToolCalls), Type: "function",
				Function: promptengine.ToolCallFunctionShape{Name: part.ToolName, Arguments: string(part.Args)},
			})
		case "tool-result":
			if role != "tool" || strings.TrimSpace(part.ToolCallID) == "" || strings.TrimSpace(part.ToolName) == "" || len(part.Result) == 0 || part.Text != nil || len(part.Args) > 0 {
				return nil, fmt.Errorf("invalid native tool-result part")
			}
			if part.IsError {
				return nil, fmt.Errorf("unsupported native tool-result error flag")
			}
			if len(part.ExperimentalContent) > 0 {
				var extra []json.RawMessage
				if err := json.Unmarshal(part.ExperimentalContent, &extra); err != nil || len(extra) > 0 {
					return nil, fmt.Errorf("unsupported native tool-result content")
				}
			}
			result := string(part.Result)
			if strings.HasPrefix(strings.TrimSpace(result), `"`) {
				if err := json.Unmarshal(part.Result, &result); err != nil {
					return nil, fmt.Errorf("invalid native tool-result content")
				}
			}
			messages = append(messages, promptengine.Message{Role: "tool", ToolCallID: part.ToolCallID, Name: part.ToolName, Content: result})
		default:
			return nil, fmt.Errorf("unsupported native content part")
		}
	}
	if role != "tool" {
		messages = append(messages, current)
	}
	return messages, nil
}

func decodeImportedTurn(raw []byte, blobs importedBlobStore) (*agentv1.ConversationTurnStructure, []byte, error) {
	if data, ok := blobs.resolve(raw); ok {
		turn := &agentv1.ConversationTurnStructure{}
		if err := proto.Unmarshal(data, turn); err != nil || turn.GetTurn() == nil {
			return nil, nil, fmt.Errorf("decode imported turn blob %x: %w", raw, firstNonNilError(err, fmt.Errorf("turn payload is empty")))
		}
		return turn, append([]byte(nil), raw...), nil
	}
	turn := &agentv1.ConversationTurnStructure{}
	if err := proto.Unmarshal(raw, turn); err == nil && turn.GetTurn() != nil {
		return turn, nil, nil
	}
	if len(raw) == sha256.Size {
		return nil, append([]byte(nil), raw...), nil
	}
	return nil, nil, fmt.Errorf("decode imported inline turn")
}

func decodeImportedUserMessage(raw []byte, blobs importedBlobStore) (*agentv1.UserMessage, error) {
	data := raw
	if resolved, ok := blobs.resolve(raw); ok {
		data = resolved
	} else if len(raw) == sha256.Size {
		candidate := &agentv1.UserMessage{}
		if err := proto.Unmarshal(raw, candidate); err != nil || !hasKnownUserMessageContent(candidate) {
			return nil, fmt.Errorf("missing prefetched user message blob %x", raw)
		}
		return candidate, nil
	}
	message := &agentv1.UserMessage{}
	if err := proto.Unmarshal(data, message); err != nil {
		return nil, fmt.Errorf("decode imported turn user_message: %w", err)
	}
	return message, nil
}

func decodeImportedStep(raw []byte, blobs importedBlobStore) (*agentv1.ConversationStep, error) {
	data := raw
	if resolved, ok := blobs.resolve(raw); ok {
		data = resolved
	} else if len(raw) == sha256.Size {
		candidate := &agentv1.ConversationStep{}
		if err := proto.Unmarshal(raw, candidate); err != nil || candidate.GetMessage() == nil {
			return nil, fmt.Errorf("missing prefetched conversation step blob %x", raw)
		}
		return candidate, nil
	}
	step := &agentv1.ConversationStep{}
	if err := proto.Unmarshal(data, step); err != nil {
		return nil, fmt.Errorf("decode imported turn step: %w", err)
	}
	if step.GetMessage() == nil {
		return nil, fmt.Errorf("decode imported turn step: payload is empty")
	}
	return step, nil
}

func importedBlobTurnMessages(turn *agentv1.ConversationTurnStructure, blobs importedBlobStore) ([]modeladapter.Message, error) {
	if turn == nil || turn.GetAgentConversationTurn() == nil {
		return nil, nil
	}
	agentTurn := turn.GetAgentConversationTurn()
	messages := make([]modeladapter.Message, 0, 1+len(agentTurn.GetSteps()))
	if len(agentTurn.GetUserMessage()) > 0 {
		userMessage, err := decodeImportedUserMessage(agentTurn.GetUserMessage(), blobs)
		if err != nil {
			return nil, err
		}
		if replay, ok := promptengine.BuildUserMessageReplayMessage(userMessage); ok {
			messages = append(messages, toModelMessage(replay))
		}
	}
	for _, rawStep := range agentTurn.GetSteps() {
		if len(rawStep) == 0 {
			continue
		}
		step, err := decodeImportedStep(rawStep, blobs)
		if err != nil {
			return nil, err
		}
		for _, replay := range promptengine.BuildLegacyMessagesFromConversationStep(step) {
			messages = append(messages, toModelMessage(replay))
		}
	}
	return messages, nil
}

func importedTurnIDs(turns [][]byte, blobs importedBlobStore) ([][]byte, error) {
	ids := make([][]byte, 0, len(turns))
	for _, raw := range turns {
		if len(raw) == 0 {
			continue
		}
		_, id, err := decodeImportedTurn(raw, blobs)
		if err != nil {
			return nil, err
		}
		if len(id) > 0 {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func hasKnownUserMessageContent(message *agentv1.UserMessage) bool {
	if message == nil {
		return false
	}
	return message.GetText() != "" ||
		message.GetMessageId() != "" ||
		message.GetSelectedContext() != nil ||
		message.GetRichText() != "" ||
		len(message.GetConversationStateBlobId()) > 0 ||
		len(message.GetTextBlobId()) > 0 ||
		len(message.GetRichTextBlobId()) > 0
}

func firstNonNilError(err error, fallback error) error {
	if err != nil {
		return err
	}
	return fallback
}
