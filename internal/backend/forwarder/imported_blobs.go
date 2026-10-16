package forwarder

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

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

type importedBlobResolver interface {
	resolve(id []byte) ([]byte, bool)
}

type importedContentNode struct {
	ID   []byte
	Data []byte
}

type contentBlobResolver struct {
	store *ContentBlobStore
}

func (resolver contentBlobResolver) resolve(id []byte) ([]byte, bool) {
	if resolver.store == nil {
		return nil, false
	}
	data, err := resolver.store.Get(id)
	if err != nil {
		return nil, false
	}
	return data, true
}

var importedTurnForbiddenBlobFields = map[string]struct{}{
	"conversation_state_blob_id": {},
	"text_blob_id":               {},
	"rich_text_blob_id":          {},
	"blob_id":                    {},
	"blob_id_with_data":          {},
	"data_blob_id":               {},
	"content_blob_id":            {},
	"output_blob_id":             {},
	"original_step_blob_id":      {},
}

func collectImportedTurnGraph(turns [][]byte, blobs importedBlobResolver) ([][]byte, []importedContentNode, error) {
	ids := make([][]byte, 0, len(turns))
	nodes := make([]importedContentNode, 0)
	seen := make(map[string]struct{}, len(turns))
	add := func(id, data []byte) error {
		digest := sha256.Sum256(data)
		if !bytes.Equal(digest[:], id) {
			return fmt.Errorf("imported blob id does not match payload sha256")
		}
		key := string(id)
		if _, ok := seen[key]; ok {
			return nil
		}
		seen[key] = struct{}{}
		nodes = append(nodes, importedContentNode{
			ID:   append([]byte(nil), id...),
			Data: append([]byte(nil), data...),
		})
		return nil
	}
	for _, raw := range turns {
		if len(raw) == 0 {
			continue
		}
		if len(raw) == sha256.Size {
			data, ok := blobs.resolve(raw)
			if !ok {
				return nil, nil, fmt.Errorf("missing prefetched turn blob %x", raw)
			}
			turn := &agentv1.ConversationTurnStructure{}
			if err := proto.Unmarshal(data, turn); err != nil || turn.GetTurn() == nil {
				return nil, nil, fmt.Errorf("decode imported turn blob %x: %w", raw, firstNonNilError(err, fmt.Errorf("turn payload is empty")))
			}
			if err := validateImportedReferencedTurn(turn); err != nil {
				return nil, nil, err
			}
			if err := add(raw, data); err != nil {
				return nil, nil, err
			}
			if err := collectImportedTurnChildren(turn.GetAgentConversationTurn(), blobs, add); err != nil {
				return nil, nil, err
			}
			ids = append(ids, append([]byte(nil), raw...))
			continue
		}
		turn := &agentv1.ConversationTurnStructure{}
		if err := proto.Unmarshal(raw, turn); err != nil || turn.GetTurn() == nil {
			return nil, nil, fmt.Errorf("decode imported inline turn")
		}
	}
	return ids, nodes, nil
}

func collectImportedTurnChildren(agent *agentv1.AgentConversationTurnStructure, blobs importedBlobResolver, add func(id, data []byte) error) error {
	if agent == nil {
		return nil
	}
	if err := collectNamedTurnField(agent.GetUserMessage(), blobs, "user message", func(data []byte) error {
		message := &agentv1.UserMessage{}
		if err := proto.Unmarshal(data, message); err != nil {
			return fmt.Errorf("decode imported turn user_message: %w", err)
		}
		return walkImportedTurnMessage(message.ProtoReflect(), "user message")
	}, add); err != nil {
		return err
	}
	for _, raw := range agent.GetSteps() {
		if err := collectNamedTurnField(raw, blobs, "conversation step", func(data []byte) error {
			step := &agentv1.ConversationStep{}
			if err := proto.Unmarshal(data, step); err != nil {
				return fmt.Errorf("decode imported turn step: %w", err)
			}
			if step.GetMessage() == nil {
				return fmt.Errorf("decode imported turn step: payload is empty")
			}
			return walkImportedTurnMessage(step.ProtoReflect(), "conversation step")
		}, add); err != nil {
			return err
		}
	}
	return nil
}

func collectNamedTurnField(raw []byte, blobs importedBlobResolver, kind string, validate func([]byte) error, add func(id, data []byte) error) error {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) == sha256.Size {
		data, ok := blobs.resolve(raw)
		if !ok {
			return fmt.Errorf("missing prefetched %s blob %x", kind, raw)
		}
		if err := validate(data); err != nil {
			return err
		}
		return add(raw, data)
	}
	return validate(raw)
}

func validateImportedReferencedTurn(turn *agentv1.ConversationTurnStructure) error {
	if turn == nil {
		return fmt.Errorf("decode imported turn blob: turn payload is empty")
	}
	if err := rejectImportedTurnUnknownFields(turn.ProtoReflect(), "turn blob"); err != nil {
		return err
	}
	if turn.GetShellConversationTurn() != nil {
		return fmt.Errorf("decode imported turn blob: shell turn")
	}
	agent := turn.GetAgentConversationTurn()
	if agent == nil {
		return fmt.Errorf("decode imported turn blob: turn payload is empty")
	}
	if agent.GetEncryptedModel() != "" {
		return fmt.Errorf("decode imported turn blob: encrypted_model")
	}
	return rejectImportedTurnUnknownFields(agent.ProtoReflect(), "turn blob")
}

func rejectImportedTurnUnknownFields(message protoreflect.Message, source string) error {
	if message.IsValid() && len(message.GetUnknown()) > 0 {
		return fmt.Errorf("decode imported %s: unknown field", source)
	}
	return nil
}

func walkImportedTurnMessage(message protoreflect.Message, source string) error {
	if !message.IsValid() {
		return nil
	}
	if err := rejectImportedTurnUnknownFields(message, source); err != nil {
		return err
	}
	var walkErr error
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		name := string(field.Name())
		if _, forbidden := importedTurnForbiddenBlobFields[name]; forbidden && importedTurnForbiddenFieldPopulated(field, value) {
			walkErr = fmt.Errorf("decode imported %s: unsupported blob reference %s", source, name)
			return false
		}
		switch {
		case field.IsList() && field.Kind() == protoreflect.MessageKind:
			list := value.List()
			for index := 0; index < list.Len(); index++ {
				if err := walkImportedTurnMessage(list.Get(index).Message(), source); err != nil {
					walkErr = err
					return false
				}
			}
		case field.IsMap() && field.MapValue().Kind() == protoreflect.MessageKind:
			var inner error
			value.Map().Range(func(_ protoreflect.MapKey, mapped protoreflect.Value) bool {
				if err := walkImportedTurnMessage(mapped.Message(), source); err != nil {
					inner = err
					return false
				}
				return true
			})
			if inner != nil {
				walkErr = inner
				return false
			}
		case field.Kind() == protoreflect.MessageKind && !field.IsList() && !field.IsMap():
			if err := walkImportedTurnMessage(value.Message(), source); err != nil {
				walkErr = err
				return false
			}
		}
		return true
	})
	return walkErr
}

func importedTurnForbiddenFieldPopulated(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
	switch {
	case field.Kind() == protoreflect.BytesKind:
		return len(value.Bytes()) > 0
	case field.Kind() == protoreflect.MessageKind:
		return value.Message().IsValid()
	default:
		return true
	}
}

func persistImportedTurnGraph(store *ContentBlobStore, nodes []importedContentNode) error {
	if len(nodes) == 0 {
		return nil
	}
	if !contentBlobStoreReady(store) {
		return fmt.Errorf("content blob store is not initialized")
	}
	for _, node := range nodes {
		if err := store.Put(node.ID, node.Data); err != nil {
			return err
		}
	}
	return nil
}

func loadImportedTurnGraph(ids [][]byte, store *ContentBlobStore) ([]importedContentNode, error) {
	if !contentBlobStoreReady(store) {
		return nil, fmt.Errorf("native checkpoint: imported reference graph not supported")
	}
	_, nodes, err := collectImportedTurnGraph(ids, contentBlobResolver{store: store})
	return nodes, err
}

func collectImportedSummaryGraph(state *agentv1.ConversationStateStructure, blobs importedBlobResolver) (runtimeStateEntryPayload, []importedContentNode, error) {
	if state == nil {
		return runtimeStateEntryPayload{}, nil, nil
	}
	nodes := make([]importedContentNode, 0)
	seen := make(map[string]struct{})
	add := func(id, data []byte) error {
		digest := sha256.Sum256(data)
		if !bytes.Equal(digest[:], id) {
			return fmt.Errorf("imported blob id does not match payload sha256")
		}
		key := string(id)
		if _, ok := seen[key]; ok {
			return nil
		}
		seen[key] = struct{}{}
		nodes = append(nodes, importedContentNode{
			ID:   append([]byte(nil), id...),
			Data: append([]byte(nil), data...),
		})
		return nil
	}
	payload := runtimeStateEntryPayload{}
	summaryID, err := collectImportedSummaryParent(state.GetSummary(), blobs, "summary", func(data []byte) error {
		return validateImportedConversationSummary(data)
	}, add)
	if err != nil {
		return runtimeStateEntryPayload{}, nil, err
	}
	payload.NativeSummary = summaryID
	archiveID, err := collectImportedSummaryParent(state.GetSummaryArchive(), blobs, "summary archive", func(data []byte) error {
		return validateImportedConversationSummaryArchive(data, blobs, add)
	}, add)
	if err != nil {
		return runtimeStateEntryPayload{}, nil, err
	}
	payload.NativeSummaryArchive = archiveID
	if len(state.GetSummaryArchives()) > 0 {
		payload.NativeSummaryArchives = make([][]byte, 0, len(state.GetSummaryArchives()))
		for _, raw := range state.GetSummaryArchives() {
			id, err := collectImportedSummaryParent(raw, blobs, "summary archive", func(data []byte) error {
				return validateImportedConversationSummaryArchive(data, blobs, add)
			}, add)
			if err != nil {
				return runtimeStateEntryPayload{}, nil, err
			}
			payload.NativeSummaryArchives = append(payload.NativeSummaryArchives, id)
		}
	}
	return payload, nodes, nil
}

func collectImportedSummaryParent(raw []byte, blobs importedBlobResolver, kind string, validate func([]byte) error, add func(id, data []byte) error) ([]byte, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	data := raw
	var id []byte
	if len(raw) == sha256.Size {
		resolved, ok := blobs.resolve(raw)
		if !ok {
			return nil, fmt.Errorf("missing prefetched %s blob", kind)
		}
		data = resolved
		id = append([]byte(nil), raw...)
	}
	if err := validate(data); err != nil {
		return nil, err
	}
	if len(id) == 0 {
		digest := sha256.Sum256(data)
		id = append([]byte(nil), digest[:]...)
	}
	if err := add(id, data); err != nil {
		return nil, err
	}
	return id, nil
}

func validateImportedConversationSummary(data []byte) error {
	item := &agentv1.ConversationSummary{}
	if err := proto.Unmarshal(data, item); err != nil {
		return fmt.Errorf("decode imported summary: %w", err)
	}
	return rejectImportedTurnUnknownFields(item.ProtoReflect(), "summary")
}

func validateImportedConversationSummaryArchive(data []byte, blobs importedBlobResolver, add func(id, data []byte) error) error {
	archive := &agentv1.ConversationSummaryArchive{}
	if err := proto.Unmarshal(data, archive); err != nil {
		return fmt.Errorf("decode imported summary archive: %w", err)
	}
	if err := rejectImportedTurnUnknownFields(archive.ProtoReflect(), "summary archive"); err != nil {
		return err
	}
	for _, raw := range archive.GetSummarizedMessages() {
		if err := collectImportedArchiveCoreMessage(raw, blobs, add); err != nil {
			return err
		}
	}
	return collectImportedArchiveCoreMessage(archive.GetSummaryMessage(), blobs, add)
}

func collectImportedArchiveCoreMessage(raw []byte, blobs importedBlobResolver, add func(id, data []byte) error) error {
	if len(raw) == 0 {
		return nil
	}
	if len(raw) != sha256.Size {
		return fmt.Errorf("decode imported summary archive: inline payload")
	}
	data, ok := blobs.resolve(raw)
	if !ok {
		return fmt.Errorf("missing prefetched archive core message blob %x", raw)
	}
	if _, err := parseImportedNativeMessage(data); err != nil {
		return fmt.Errorf("decode imported archive core message: %w", err)
	}
	if err := validateNativeCheckpointJSON(data, false); err != nil {
		return err
	}
	return add(raw, data)
}

func hasNativeSummarySource(payload runtimeStateEntryPayload) bool {
	return len(payload.NativeSummary) > 0 || len(payload.NativeSummaryArchive) > 0 || len(payload.NativeSummaryArchives) > 0
}

func validateNativeSummarySourceIDs(payload runtimeStateEntryPayload) error {
	if err := validateNativeSummarySourceID(payload.NativeSummary); err != nil {
		return err
	}
	if err := validateNativeSummarySourceID(payload.NativeSummaryArchive); err != nil {
		return err
	}
	for _, id := range payload.NativeSummaryArchives {
		if len(id) == 0 {
			continue
		}
		if err := validateNativeSummarySourceID(id); err != nil {
			return err
		}
	}
	return nil
}

func validateNativeSummarySourceID(id []byte) error {
	if len(id) == 0 {
		return nil
	}
	if len(id) != sha256.Size {
		return fmt.Errorf("native checkpoint: invalid native summary source")
	}
	return nil
}

func loadImportedSummaryGraph(source runtimeStateEntryPayload, store *ContentBlobStore) ([]importedContentNode, error) {
	if !contentBlobStoreReady(store) {
		return nil, fmt.Errorf("native checkpoint: imported native summary graph not supported")
	}
	state := &agentv1.ConversationStateStructure{
		Summary:         source.NativeSummary,
		SummaryArchive:  source.NativeSummaryArchive,
		SummaryArchives: source.NativeSummaryArchives,
	}
	_, nodes, err := collectImportedSummaryGraph(state, contentBlobResolver{store: store})
	return nodes, err
}

func contentBlobStoreReady(store *ContentBlobStore) bool {
	return store != nil && strings.TrimSpace(store.root) != ""
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

// decodeImportedNativeMessages 在导入边界将完整原生 root 转成现有 replay。
// 先逐条解析（不做整段配对），再对整段做工具配对校验，保持旧错误类别。
func decodeImportedNativeMessages(rawItems [][]byte) ([]promptengine.Message, error) {
	messages := make([]promptengine.Message, 0, len(rawItems))
	for _, raw := range rawItems {
		decoded, err := parseImportedNativeMessage(raw)
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

// wrapProviderConversationSummary 用现有 provider 摘要包装包裹原生摘要文本。
func wrapProviderConversationSummary(text string) string {
	return "<conversation_summary>\n" + text + "\n</conversation_summary>"
}

// parseImportedNativeMessage 解析单条原生 CoreMessage，不做整段工具配对校验。
// system 角色按既有规则丢弃；严格 providerOptions:{cursor:{isSummary:true}} 的 user
// 字符串产生已包装的 provider 视图；未知顶层/嵌套字段、错误角色或内容明确失败。
func parseImportedNativeMessage(raw []byte) ([]promptengine.Message, error) {
	// RawMessage 随外层 JSON 落盘会紧凑/缩进并转义 HTML 字符，而 Message 中的
	// 参数及非字符串结果是 JSON 字符串。统一解码输入的序列化形式，避免只有
	// 原文空白/转义变化就使保存视图失配；不转成浮点数，也不改字符串内空白。
	canonical, err := json.Marshal(json.RawMessage(raw))
	if err != nil {
		return nil, fmt.Errorf("invalid or unsupported native root message")
	}
	var item struct {
		Role            string          `json:"role"`
		Content         json.RawMessage `json:"content"`
		ProviderOptions json.RawMessage `json:"providerOptions"`
	}
	decoder := json.NewDecoder(bytes.NewReader(canonical))
	decoder.DisallowUnknownFields()
	if !json.Valid(raw) || decoder.Decode(&item) != nil || len(item.Content) == 0 {
		return nil, fmt.Errorf("invalid or unsupported native root message")
	}
	switch item.Role {
	case "system", "user", "assistant", "tool":
	default:
		return nil, fmt.Errorf("invalid native root message role")
	}
	summary, err := parseImportedNativeSummaryFlag(item.Role, item.Content, item.ProviderOptions)
	if err != nil {
		return nil, err
	}
	content := strings.TrimSpace(string(item.Content))
	if strings.HasPrefix(content, `"`) {
		var text string
		if err := json.Unmarshal(item.Content, &text); err != nil || item.Role == "tool" {
			return nil, fmt.Errorf("invalid native string message content")
		}
		if item.Role == "system" {
			return nil, nil
		}
		if summary {
			text = wrapProviderConversationSummary(text)
		}
		return []promptengine.Message{{Role: item.Role, Content: text}}, nil
	}
	if item.Role == "system" || !strings.HasPrefix(content, "[") {
		return nil, fmt.Errorf("invalid native message content")
	}
	return decodeImportedNativeContentParts(item.Role, item.Content)
}

// parseImportedNativeSummaryFlag 只接受 user 字符串上的严格 providerOptions:{cursor:{isSummary:true}}；
// 任何未知嵌套字段、非 true 标记、错误角色或非字符串内容都明确失败。
func parseImportedNativeSummaryFlag(role string, content json.RawMessage, providerOptions json.RawMessage) (bool, error) {
	if len(providerOptions) == 0 {
		return false, nil
	}
	var options struct {
		Cursor *struct {
			IsSummary *bool `json:"isSummary"`
		} `json:"cursor"`
	}
	decoder := json.NewDecoder(bytes.NewReader(providerOptions))
	decoder.DisallowUnknownFields()
	if !json.Valid(providerOptions) || decoder.Decode(&options) != nil || options.Cursor == nil ||
		options.Cursor.IsSummary == nil || !*options.Cursor.IsSummary {
		return false, fmt.Errorf("invalid or unsupported native root message")
	}
	if role != "user" {
		return false, fmt.Errorf("invalid or unsupported native root message")
	}
	if !strings.HasPrefix(strings.TrimSpace(string(content)), `"`) {
		return false, fmt.Errorf("invalid native string message content")
	}
	return true, nil
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

// decodeImportedPlan 对32字节字段只按内容引用读取，不用 protobuf 解析成功掩盖缺失。
// 非32字节旧内联沿用既有解码；未知字段拒绝只适用于新的引用正文。
func decodeImportedPlan(raw []byte, blobs importedBlobStore) (*agentv1.ConversationPlan, error) {
	data := raw
	source := "plan"
	referenced := len(raw) == sha256.Size
	if referenced {
		var ok bool
		data, ok = blobs.resolve(raw)
		if !ok {
			return nil, fmt.Errorf("missing prefetched plan blob")
		}
		source = "plan blob"
	}
	plan := &agentv1.ConversationPlan{}
	if err := proto.Unmarshal(data, plan); err != nil {
		return nil, fmt.Errorf("decode imported %s: %w", source, err)
	}
	if referenced && len(plan.ProtoReflect().GetUnknown()) > 0 {
		return nil, fmt.Errorf("decode imported plan blob: unknown field")
	}
	return plan, nil
}

func decodeImportedTodo(raw []byte, blobs importedBlobStore) (*agentv1.TodoItem, error) {
	data := raw
	source := "todo"
	referenced := len(raw) == sha256.Size
	if referenced {
		var ok bool
		data, ok = blobs.resolve(raw)
		if !ok {
			return nil, fmt.Errorf("missing prefetched todo blob")
		}
		source = "todo blob"
		if len(data) == 0 {
			return nil, fmt.Errorf("decode imported todo blob: empty todo")
		}
	}
	item := &agentv1.TodoItem{}
	if err := proto.Unmarshal(data, item); err != nil {
		return nil, fmt.Errorf("decode imported %s: %w", source, err)
	}
	if referenced && len(item.ProtoReflect().GetUnknown()) > 0 {
		return nil, fmt.Errorf("decode imported todo blob: unknown field")
	}
	// 显式编码的默认字段也能解码为全零对象，检查语义而非仅看 wire 长度。
	if referenced && proto.Size(item) == 0 {
		return nil, fmt.Errorf("decode imported todo blob: empty todo")
	}
	return item, nil
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
