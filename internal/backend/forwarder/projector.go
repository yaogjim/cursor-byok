// projector.go 负责把 JSON history 投影成 prompt replay 和 legacy checkpoint 视图。
package forwarder

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	modeladapter "cursor/internal/backend/agent/model"
	promptengine "cursor/internal/backend/agent/prompt"
)

const projectedConversationMaxTokens = 130000

type HistoryProjector struct {
}

type CheckpointBlob struct {
	ID   []byte
	Data []byte
}

type CheckpointProjection struct {
	State *agentv1.ConversationStateStructure
	Blobs []CheckpointBlob
}

type checkpointBlobGraph struct {
	blobs map[[sha256.Size]byte][]byte
	order [][sha256.Size]byte
}

func newCheckpointBlobGraph() *checkpointBlobGraph {
	return &checkpointBlobGraph{blobs: make(map[[sha256.Size]byte][]byte)}
}

func (graph *checkpointBlobGraph) add(data []byte) []byte {
	if graph == nil || len(data) == 0 {
		return nil
	}
	id := sha256.Sum256(data)
	if _, exists := graph.blobs[id]; !exists {
		graph.blobs[id] = append([]byte(nil), data...)
		graph.order = append(graph.order, id)
	}
	return append([]byte(nil), id[:]...)
}

func (graph *checkpointBlobGraph) list() []CheckpointBlob {
	if graph == nil || len(graph.order) == 0 {
		return nil
	}
	blobs := make([]CheckpointBlob, 0, len(graph.order))
	for _, id := range graph.order {
		blobs = append(blobs, CheckpointBlob{
			ID:   append([]byte(nil), id[:]...),
			Data: append([]byte(nil), graph.blobs[id]...),
		})
	}
	return blobs
}

// NewHistoryProjector 创建 history 投影器。
func NewHistoryProjector() *HistoryProjector {
	return &HistoryProjector{}
}

// ProjectPromptReplay 把 conversation history 还原为 provider 可消费的消息列表。
func (projector *HistoryProjector) ProjectPromptReplay(conversation *ConversationFile) ([]modeladapter.Message, error) {
	if conversation == nil {
		return nil, nil
	}
	entries := replayablePromptProjectionEntries(conversation.Entries)
	messages := make([]projectedReplayMessage, 0, len(entries)*2)
	seenToolCalls := make(map[string]struct{})
	openToolCalls := make(map[string]struct{})
	toolCallMessageIndexes := make(map[string]int)
	reasoningEmissions := newReplayReasoningEmissionTracker()
	for _, entry := range entries {
		switch strings.TrimSpace(entry.Kind) {
		case "model_message":
			var payload modelMessageEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode model_message entry: %w", err)
			}
			message := cloneReplayModelMessage(payload.Message)
			if strings.TrimSpace(message.Role) != "" {
				messages = append(messages, newProjectedReplayMessage(message, entry))
			}
		case "compaction_summary", "compacted_summary":
			summary, ok := decodeCompactionSummaryEntry(entry)
			if ok {
				messages = append(messages, newProjectedReplayMessage(modeladapter.Message{
					Role:    "user",
					Content: "<conversation_summary>\n" + summary + "\n</conversation_summary>",
				}, entry))
			}
		case "user_message":
			userMessage := &agentv1.UserMessage{}
			if err := protojson.Unmarshal(entry.Payload, userMessage); err != nil {
				return nil, fmt.Errorf("decode user_message entry: %w", err)
			}
			message, ok := promptengine.BuildUserMessageReplayMessage(userMessage)
			if ok {
				messages = append(messages, newProjectedReplayMessage(toModelMessage(message), entry))
			}
		case "request_context":
			requestContext := &agentv1.RequestContext{}
			if err := protojson.Unmarshal(entry.Payload, requestContext); err != nil {
				return nil, fmt.Errorf("decode request_context entry: %w", err)
			}
			for _, replay := range promptengine.BuildRequestContextReplayMessages(requestContext) {
				messages = append(messages, newProjectedReplayMessage(toModelMessage(replay), entry))
			}
		case "prompt_context":
			var payload promptContextEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode prompt_context entry: %w", err)
			}
			context := normalizePromptContextMessage(PromptContextMessage{
				Source:      payload.Source,
				ContentHash: payload.ContentHash,
				Message: modeladapter.Message{
					Role:    firstNonEmpty(strings.TrimSpace(payload.Role), "user"),
					Content: strings.TrimSpace(payload.Content),
				},
				Persist: true,
			})
			if isReplayablePromptContext(context) {
				messages = append(messages, newProjectedReplayMessage(context.Message, entry))
			}
		case "assistant_text":
			var payload assistantTextPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode assistant_text entry: %w", err)
			}
			if strings.TrimSpace(payload.Text) == "" && strings.TrimSpace(payload.ReasoningContent) != "" && len(openToolCalls) > 0 {
				continue
			}
			if strings.TrimSpace(payload.Text) == "" && !hasReplayableReasoningPayload(payload.ReasoningContent, payload.ReasoningSignature, payload.ReasoningSignatureSource) {
				continue
			}
			replayMessage := modeladapter.Message{
				Role:                            "assistant",
				Content:                         strings.TrimSpace(payload.Text),
				ReasoningContent:                payload.ReasoningContent,
				ReasoningSignature:              payload.ReasoningSignature,
				ReasoningSignatureSource:        payload.ReasoningSignatureSource,
				OpenAIResponsesReasoningID:      payload.ReasoningItemID,
				OpenAIResponsesReasoningStatus:  payload.ReasoningStatus,
				OpenAIResponsesReasoningSummary: append(json.RawMessage(nil), payload.ReasoningSummary...),
			}
			messages = append(messages, newProjectedReplayMessage(replayMessage, entry))
			applyProjectedReplayReasoning(reasoningEmissions, entry, messages)
		case "tool_call":
			var payload toolCallEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode tool_call entry: %w", err)
			}
			toolCall := &agentv1.ToolCall{}
			if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
				return nil, fmt.Errorf("decode tool_call payload: %w", err)
			}
			replayMessage, ok := promptengine.BuildAssistantToolCallReplayMessage(payload.ToolCallID, toolCall)
			if !ok {
				continue
			}
			replayMessage.ReasoningContent = payload.ReasoningContent
			replayMessage.ReasoningSignature = payload.ReasoningSignature
			replayMessage.ReasoningSignatureSource = payload.ReasoningSignatureSource
			replayMessage.OpenAIResponsesReasoningID = payload.ReasoningItemID
			replayMessage.OpenAIResponsesReasoningStatus = payload.ReasoningStatus
			replayMessage.OpenAIResponsesReasoningSummary = append(json.RawMessage(nil), payload.ReasoningSummary...)
			applyPromptProviderMetadataToFirstToolCall(&replayMessage, payload.ProviderItemID, payload.ProviderCallID, payload.ProviderStatus)
			modelMessage := toModelMessage(replayMessage)
			messages = append(messages, newProjectedReplayMessage(modelMessage, entry))
			applyProjectedReplayReasoning(reasoningEmissions, entry, messages)
			if toolCallID := strings.TrimSpace(payload.ToolCallID); toolCallID != "" {
				toolCallMessageIndexes[toolCallID] = len(messages) - 1
				seenToolCalls[toolCallID] = struct{}{}
				openToolCalls[toolCallID] = struct{}{}
			}
		case "tool_result":
			var payload toolResultEntryPayload
			if err := json.Unmarshal(entry.Payload, &payload); err != nil {
				return nil, fmt.Errorf("decode tool_result entry: %w", err)
			}
			historicalToolResult := isHistoricalReplayToolResult(conversation, entry)
			toolCallID := strings.TrimSpace(payload.ToolCallID)
			if _, ok := seenToolCalls[toolCallID]; ok {
				delete(openToolCalls, toolCallID)
				if index, found := toolCallMessageIndexes[toolCallID]; found && index >= 0 && index < len(messages) {
					overrideModelToolReplayFromEntry(&messages[index].Message, payload.ToolName, payload.Arguments)
					delete(toolCallMessageIndexes, toolCallID)
				}
				var toolCall *agentv1.ToolCall
				if len(payload.ToolCall) > 0 {
					decoded := &agentv1.ToolCall{}
					if err := protojson.Unmarshal(payload.ToolCall, decoded); err == nil {
						toolCall = decoded
					}
				}
				toolName := strings.TrimSpace(payload.ToolName)
				if toolName == "" && toolCall != nil {
					toolName = inferToolName(toolCall)
				}
				if toolCallID == "" || toolName == "" {
					continue
				}
				if isLegacyPlainWriteReplay(toolName, len(payload.ToolCall) > 0) {
					continue
				}
				if toolCall != nil {
					replayMessage, ok := promptengine.BuildToolResultReplayMessage(toolCallID, toolCall)
					if ok {
						replayMessage.Name = toolName
						replayMessage.Content = limitProjectedToolResultReplay(toolName, replayMessage.Content, payload.ResultText, true, historicalToolResult)
						messages = append(messages, newProjectedReplayMessage(toModelMessage(replayMessage), entry))
						continue
					}
				}
				messages = append(messages, newProjectedReplayMessage(modeladapter.Message{
					Role:       "tool",
					Name:       toolName,
					ToolCallID: toolCallID,
					Content:    limitProjectedToolResultReplay(toolName, payload.ResultText, "", false, historicalToolResult),
				}, entry))
				continue
			}
			if len(payload.ToolCall) > 0 {
				toolCall := &agentv1.ToolCall{}
				if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
					return nil, fmt.Errorf("decode tool_result tool_call entry: %w", err)
				}
				replayMessages, ok := promptengine.BuildToolCallReplayMessages(payload.ToolCallID, toolCall)
				if ok {
					overrideToolReplayFromEntry(replayMessages, payload.ToolName, payload.Arguments)
					for index := range replayMessages {
						if strings.TrimSpace(replayMessages[index].Role) != "assistant" || len(replayMessages[index].ToolCalls) == 0 {
							continue
						}
						replayMessages[index].ReasoningContent = payload.ReasoningContent
						replayMessages[index].ReasoningSignature = payload.ReasoningSignature
						replayMessages[index].ReasoningSignatureSource = payload.ReasoningSignatureSource
						replayMessages[index].OpenAIResponsesReasoningID = payload.ReasoningItemID
						replayMessages[index].OpenAIResponsesReasoningStatus = payload.ReasoningStatus
						replayMessages[index].OpenAIResponsesReasoningSummary = append(json.RawMessage(nil), payload.ReasoningSummary...)
						applyPromptProviderMetadataToFirstToolCall(&replayMessages[index], payload.ProviderItemID, payload.ProviderCallID, payload.ProviderStatus)
					}
					for _, replay := range replayMessages {
						if strings.TrimSpace(replay.Role) == "tool" {
							toolName := firstNonEmpty(strings.TrimSpace(replay.Name), strings.TrimSpace(payload.ToolName))
							replay.Content = limitProjectedToolResultReplay(toolName, replay.Content, payload.ResultText, true, historicalToolResult)
						}
						modelMessage := toModelMessage(replay)
						messages = append(messages, newProjectedReplayMessage(modelMessage, entry))
						applyProjectedReplayReasoning(reasoningEmissions, entry, messages)
					}
					continue
				}
			}
			if strings.TrimSpace(payload.ToolCallID) == "" || strings.TrimSpace(payload.ToolName) == "" {
				continue
			}
			if isLegacyPlainWriteReplay(strings.TrimSpace(payload.ToolName), len(payload.ToolCall) > 0) {
				continue
			}
			if !hasReplayableReasoningPayload(payload.ReasoningContent, payload.ReasoningSignature, payload.ReasoningSignatureSource) && strings.TrimSpace(payload.ToolName) != "ForceBackgroundShell" {
				continue
			}
			effectiveToolName := effectiveReplayToolName(strings.TrimSpace(payload.ToolName), strings.TrimSpace(payload.ToolName))
			effectiveArguments := firstNonEmpty(strings.TrimSpace(payload.Arguments), "{}")
			if isLegacyPatchEditToolName(payload.ToolName) {
				effectiveArguments = "{}"
			}
			fallbackAssistant := modeladapter.Message{
				Role:                            "assistant",
				ReasoningContent:                payload.ReasoningContent,
				ReasoningSignature:              payload.ReasoningSignature,
				ReasoningSignatureSource:        payload.ReasoningSignatureSource,
				OpenAIResponsesReasoningID:      payload.ReasoningItemID,
				OpenAIResponsesReasoningStatus:  payload.ReasoningStatus,
				OpenAIResponsesReasoningSummary: append(json.RawMessage(nil), payload.ReasoningSummary...),
				ToolCalls: []modeladapter.ToolCallDescriptor{{
					ID:                    strings.TrimSpace(payload.ToolCallID),
					Type:                  "function",
					OpenAIResponsesID:     strings.TrimSpace(payload.ProviderItemID),
					OpenAIResponsesCallID: strings.TrimSpace(payload.ProviderCallID),
					OpenAIResponsesStatus: strings.TrimSpace(payload.ProviderStatus),
					Function: modeladapter.ToolCallFunctionShape{
						Name:      effectiveToolName,
						Arguments: effectiveArguments,
					},
				}},
			}
			messages = append(messages, newProjectedReplayMessage(fallbackAssistant, entry))
			applyProjectedReplayReasoning(reasoningEmissions, entry, messages)
			messages = append(messages, newProjectedReplayMessage(modeladapter.Message{
				Role:       "tool",
				Name:       effectiveToolName,
				ToolCallID: strings.TrimSpace(payload.ToolCallID),
				Content:    limitProjectedToolResultReplay(payload.ToolName, payload.ResultText, "", false, historicalToolResult),
			}, entry))
		}
	}
	return projectedReplayMessagesToModel(normalizeProjectedReplayMessages(messages)), nil
}

func compactedPromptProjectionEntries(entries []HistoryEntry) []HistoryEntry {
	if len(entries) == 0 {
		return nil
	}
	compactionIndex := -1
	for index := len(entries) - 1; index >= 0; index-- {
		if isCompactionSummaryKind(entries[index].Kind) {
			compactionIndex = index
			break
		}
	}
	if compactionIndex < 0 {
		return entries
	}
	var compactionPayload compactionSummaryEntryPayload
	_ = json.Unmarshal(entries[compactionIndex].Payload, &compactionPayload)
	preservedIndexes := map[int]struct{}{}
	if compactionPayload.PreserveCurrentTurnInputs {
		latestToolCallID := latestCompletedToolCallIDForTurn(entries, compactionPayload.CurrentTurnSeq, compactionPayload.CurrentRequestID)
		preservedIndexes = autoCompactionPreservedEntryIndexes(entries, compactionPayload.CurrentTurnSeq, compactionPayload.CurrentRequestID, latestToolCallID)
	}
	filtered := make([]HistoryEntry, 0, len(entries)-compactionIndex+len(preservedIndexes))
	for index := 0; index < compactionIndex; index++ {
		if !isPromptReplayEntryKind(entries[index].Kind) {
			filtered = append(filtered, entries[index])
		}
	}
	filtered = append(filtered, entries[compactionIndex])
	for index := 0; index < compactionIndex; index++ {
		if _, ok := preservedIndexes[index]; !ok || isCompactionSummaryKind(entries[index].Kind) {
			continue
		}
		entry := entries[index]
		if rewritten, ok := compactedProjectionPreservedEntry(entry); ok {
			entry = rewritten
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered, entries[compactionIndex+1:]...)
	return filtered
}

func replayablePromptProjectionEntries(entries []HistoryEntry) []HistoryEntry {
	return sanitizeCanceledReplayEntries(compactedPromptProjectionEntries(entries))
}

func checkpointProjectionEntries(entries []HistoryEntry) []HistoryEntry {
	return sanitizeCanceledReplayEntries(entries)
}

const (
	cancelReplayPolicyDropTurn        = "drop_turn"
	cancelReplayPolicyDropUnstarted   = "drop_unstarted_turn"
	cancelReplayPolicyKeepStableInput = "keep_stable_input"
	cancelReplayPolicyKeepInterrupted = "keep_interrupted_output"
)

func sanitizeCanceledReplayEntries(entries []HistoryEntry) []HistoryEntry {
	if len(entries) == 0 {
		return nil
	}
	canceledTurns := canceledReplayPolicies(entries)
	if len(canceledTurns) == 0 {
		return entries
	}
	activeCanceledTurns := canceledReplayActivityTurns(entries)
	filtered := make([]HistoryEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.TurnSeq > 0 {
			if policy, canceled := canceledTurns[entry.TurnSeq]; canceled {
				if policy == cancelReplayPolicyKeepInterrupted {
					filtered = append(filtered, entry)
					continue
				}
				if policy != cancelReplayPolicyDropTurn {
					if _, active := activeCanceledTurns[entry.TurnSeq]; active {
						filtered = append(filtered, entry)
						continue
					}
				}
				if policy == cancelReplayPolicyDropUnstarted {
					policy = cancelReplayPolicyDropTurn
				}
				if policy == cancelReplayPolicyDropTurn || !isStableCanceledTurnInputEntry(entry) {
					continue
				}
			}
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func canceledReplayPolicies(entries []HistoryEntry) map[int64]string {
	canceledTurns := make(map[int64]string)
	for _, entry := range entries {
		if entry.TurnSeq <= 0 {
			continue
		}
		policy, ok := canceledReplayPolicyForEntry(entry)
		if !ok {
			continue
		}
		if policy == cancelReplayPolicyDropTurn {
			canceledTurns[entry.TurnSeq] = policy
			continue
		}
		if _, exists := canceledTurns[entry.TurnSeq]; !exists {
			canceledTurns[entry.TurnSeq] = policy
		}
	}
	return canceledTurns
}

func canceledReplayActivityTurns(entries []HistoryEntry) map[int64]struct{} {
	activeTurns := make(map[int64]struct{})
	for _, entry := range entries {
		if entry.TurnSeq <= 0 || !isCanceledTurnActivityEntry(entry) {
			continue
		}
		activeTurns[entry.TurnSeq] = struct{}{}
	}
	return activeTurns
}

func canceledReplayPolicyForEntry(entry HistoryEntry) (string, bool) {
	if strings.TrimSpace(entry.Kind) != "metadata" {
		return "", false
	}
	var payload metadataPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return "", false
	}
	if strings.TrimSpace(payload.Type) != "control" {
		return "", false
	}
	if strings.TrimSpace(readStringValue(payload.Value["status"])) != "canceled" {
		return "", false
	}
	return normalizeCancelReplayPolicy(
		readStringValue(payload.Value["replay_policy"]),
		readStringValue(payload.Value["reason"]),
	), true
}

func normalizeCancelReplayPolicy(policy string, reason string) string {
	switch strings.TrimSpace(policy) {
	case cancelReplayPolicyDropTurn:
		if strings.Contains(strings.ToLower(strings.TrimSpace(reason)), "superseded by newer request") {
			return cancelReplayPolicyDropUnstarted
		}
		return cancelReplayPolicyDropTurn
	case cancelReplayPolicyDropUnstarted:
		return cancelReplayPolicyDropUnstarted
	case cancelReplayPolicyKeepStableInput:
		return cancelReplayPolicyKeepStableInput
	case cancelReplayPolicyKeepInterrupted:
		return cancelReplayPolicyKeepInterrupted
	default:
		return cancelReplayPolicyForReason(reason)
	}
}

func cancelReplayPolicyForReason(reason string) string {
	normalized := strings.ToLower(strings.TrimSpace(reason))
	switch {
	case strings.Contains(normalized, "superseded by newer request"):
		return cancelReplayPolicyDropUnstarted
	default:
		return cancelReplayPolicyKeepStableInput
	}
}

func isStableCanceledTurnInputEntry(entry HistoryEntry) bool {
	switch strings.TrimSpace(entry.Kind) {
	case "request_context", "user_message", "prompt_context":
		return true
	default:
		return false
	}
}

func isCanceledTurnActivityEntry(entry HistoryEntry) bool {
	switch strings.TrimSpace(entry.Kind) {
	case "model_message", "assistant_text", "tool_call", "tool_result":
		return true
	default:
		return false
	}
}

func compactedProjectionPreservedEntry(entry HistoryEntry) (HistoryEntry, bool) {
	if strings.TrimSpace(entry.Kind) != "tool_result" {
		return entry, false
	}
	if rewritten, ok := rewriteAutoCompactionToolResultEntry(entry, autoCompactionPreservedToolResultLimitBytes, false); ok {
		return rewritten, true
	}
	return entry, true
}

func isPromptReplayEntryKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "model_message", "compaction_summary", "compacted_summary", "user_message", "request_context", "prompt_context", "assistant_text", "tool_call", "tool_result":
		return true
	default:
		return false
	}
}

func decodeCompactionSummaryEntry(entry HistoryEntry) (string, bool) {
	var payload compactionSummaryEntryPayload
	if err := json.Unmarshal(entry.Payload, &payload); err != nil {
		return "", false
	}
	text := strings.TrimSpace(payload.Summary)
	return text, text != ""
}

func isHistoricalReplayToolResult(conversation *ConversationFile, entry HistoryEntry) bool {
	if conversation == nil || entry.TurnSeq <= 0 {
		return false
	}
	currentTurnSeq := conversation.NextTurnSeq - 1
	return currentTurnSeq > 0 && entry.TurnSeq < currentTurnSeq
}

// ProjectLegacyCheckpoint 按需从 JSON history 投影出兼容旧客户端的 checkpoint 结构。
func (projector *HistoryProjector) ProjectLegacyCheckpoint(conversation *ConversationFile) (*agentv1.ConversationStateStructure, error) {
	projection, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil || projection == nil {
		return nil, err
	}
	return projection.State, nil
}

// ProjectCheckpointProjection 同时返回 checkpoint 状态及其引用的内容寻址 Blob。
func (projector *HistoryProjector) ProjectCheckpointProjection(conversation *ConversationFile) (*CheckpointProjection, error) {
	blobs := newCheckpointBlobGraph()
	state := &agentv1.ConversationStateStructure{
		TokenDetails: &agentv1.ConversationTokenDetails{
			UsedTokens: conversationTokenDetailsUsedTokens(conversation),
			MaxTokens:  conversationTokenDetailsMaxTokens(conversation),
		},
		Summary:          latestCompactionSummaryBytes(conversation),
		SummaryArchive:   previousCompactionSummaryBytes(conversation),
		SummaryArchives:  compactionSummaryArchives(conversation),
		SelfSummaryCount: uint32(len(compactionSummaryTexts(conversation))),
	}
	if conversation == nil {
		mode := agentv1.AgentMode_AGENT_MODE_AGENT
		state.Mode = &mode
		return &CheckpointProjection{State: state}, nil
	}
	mode, err := parseModeAlias(conversation.Mode)
	if err != nil {
		return nil, err
	}
	state.Mode = &mode
	structuredState, err := projectConversationStructuredState(conversation)
	if err != nil {
		return nil, err
	}
	if structuredState.HasPlan {
		state.Plan = encodeConversationPlanBytes(structuredState.PlanText)
	}
	state.Plans = clonePlanRegistryEntries(structuredState.Plans)
	if structuredState.HasTodos {
		state.Todos = encodeConversationTodoBytes(structuredState.Todos)
	}
	turnIDs, err := projectCheckpointTurnBlobs(conversation, blobs)
	if err != nil {
		return nil, err
	}
	state.Turns = append(cloneByteSlices(conversation.ImportedTurnIDs), turnIDs...)
	replayMessages, err := projector.ProjectPromptReplay(conversation)
	if err != nil {
		return nil, err
	}
	promptReplay := make([]promptengine.Message, 0, len(replayMessages))
	for _, message := range replayMessages {
		promptReplay = append(promptReplay, promptengine.Message{
			Role:                            message.Role,
			Content:                         message.Content,
			ContentParts:                    toPromptContentParts(message.ContentParts),
			ReasoningContent:                message.ReasoningContent,
			ReasoningSignature:              message.ReasoningSignature,
			ReasoningSignatureSource:        message.ReasoningSignatureSource,
			OpenAIResponsesReasoningID:      message.OpenAIResponsesReasoningID,
			OpenAIResponsesReasoningStatus:  message.OpenAIResponsesReasoningStatus,
			OpenAIResponsesReasoningSummary: append(json.RawMessage(nil), message.OpenAIResponsesReasoningSummary...),
			ToolCalls:                       toPromptToolCalls(message.ToolCalls),
			ToolCallID:                      message.ToolCallID,
			Name:                            message.Name,
		})
	}
	promptReplay = filterCheckpointPersistentToolReplay(promptReplay)
	rootPromptMessages, err := promptengine.EncodeReplayMessages(promptReplay)
	if err != nil {
		return nil, err
	}
	state.RootPromptMessagesJson = rootPromptMessages
	return &CheckpointProjection{State: state, Blobs: blobs.list()}, nil
}

func projectCheckpointTurnBlobs(conversation *ConversationFile, blobs *checkpointBlobGraph) ([][]byte, error) {
	if conversation == nil || blobs == nil {
		return nil, nil
	}
	grouped := make(map[int64][]HistoryEntry)
	order := make([]int64, 0, conversation.NextTurnSeq)
	for _, entry := range checkpointProjectionEntries(conversation.Entries) {
		if entry.TurnSeq <= 0 {
			continue
		}
		if _, ok := grouped[entry.TurnSeq]; !ok {
			order = append(order, entry.TurnSeq)
		}
		grouped[entry.TurnSeq] = append(grouped[entry.TurnSeq], entry)
	}
	logicalTurns := make([][]HistoryEntry, 0, len(order))
	for _, turnSeq := range order {
		entries := grouped[turnSeq]
		if checkpointTurnHasUserMessage(entries) {
			logicalTurns = append(logicalTurns, append([]HistoryEntry(nil), entries...))
			continue
		}
		if len(logicalTurns) == 0 {
			continue
		}
		last := len(logicalTurns) - 1
		logicalTurns[last] = append(logicalTurns[last], entries...)
	}

	turnIDs := make([][]byte, 0, len(logicalTurns))
	for _, entries := range logicalTurns {
		completedToolCalls, err := collectCheckpointCompletedToolCalls(entries)
		if err != nil {
			return nil, err
		}
		var userMessageID []byte
		var turnRequestID string
		steps := make([]*agentv1.ConversationStep, 0, len(entries))
		seenToolCalls := make(map[string]struct{})
		openToolCalls := make(map[string]struct{})
		for _, entry := range entries {
			if turnRequestID == "" {
				turnRequestID = strings.TrimSpace(entry.RequestID)
			}
			switch strings.TrimSpace(entry.Kind) {
			case "user_message":
				userMessage := &agentv1.UserMessage{}
				if err := protojson.Unmarshal(entry.Payload, userMessage); err != nil {
					return nil, fmt.Errorf("decode checkpoint user_message: %w", err)
				}
				payload, err := proto.Marshal(userMessage)
				if err != nil {
					return nil, err
				}
				userMessageID = blobs.add(payload)
			case "assistant_text":
				var payload assistantTextPayload
				if err := json.Unmarshal(entry.Payload, &payload); err != nil {
					return nil, err
				}
				if strings.TrimSpace(payload.Text) == "" && strings.TrimSpace(payload.ReasoningContent) != "" && len(openToolCalls) > 0 {
					continue
				}
				if strings.TrimSpace(payload.ReasoningContent) != "" {
					steps = append(steps, &agentv1.ConversationStep{
						Message: &agentv1.ConversationStep_ThinkingMessage{
							ThinkingMessage: &agentv1.ThinkingMessage{Text: payload.ReasoningContent},
						},
					})
				}
				if strings.TrimSpace(payload.Text) == "" {
					continue
				}
				steps = append(steps, &agentv1.ConversationStep{
					Message: &agentv1.ConversationStep_AssistantMessage{
						AssistantMessage: &agentv1.AssistantMessage{Text: strings.TrimSpace(payload.Text)},
					},
				})
			case "tool_call":
				var payload toolCallEntryPayload
				if err := json.Unmarshal(entry.Payload, &payload); err != nil {
					return nil, err
				}
				if strings.TrimSpace(payload.ReasoningContent) != "" {
					steps = append(steps, &agentv1.ConversationStep{
						Message: &agentv1.ConversationStep_ThinkingMessage{
							ThinkingMessage: &agentv1.ThinkingMessage{Text: payload.ReasoningContent},
						},
					})
				}
				toolCall := &agentv1.ToolCall{}
				toolCallID := strings.TrimSpace(payload.ToolCallID)
				if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
					return nil, err
				}
				if completedPayload := completedToolCalls[toolCallID]; len(completedPayload) > 0 {
					completedToolCall := &agentv1.ToolCall{}
					if err := protojson.Unmarshal(completedPayload, completedToolCall); err != nil {
						return nil, err
					}
					proto.Merge(toolCall, completedToolCall)
				}
				steps = append(steps, &agentv1.ConversationStep{
					Message: &agentv1.ConversationStep_ToolCall{ToolCall: toolCall},
				})
				if toolCallID != "" {
					seenToolCalls[toolCallID] = struct{}{}
					openToolCalls[toolCallID] = struct{}{}
				}
			case "tool_result":
				var payload toolResultEntryPayload
				if err := json.Unmarshal(entry.Payload, &payload); err != nil {
					return nil, err
				}
				toolCallID := strings.TrimSpace(payload.ToolCallID)
				if toolCallID != "" {
					delete(openToolCalls, toolCallID)
				}
				if _, ok := seenToolCalls[toolCallID]; ok {
					continue
				}
				if strings.TrimSpace(payload.ReasoningContent) != "" {
					steps = append(steps, &agentv1.ConversationStep{
						Message: &agentv1.ConversationStep_ThinkingMessage{
							ThinkingMessage: &agentv1.ThinkingMessage{Text: payload.ReasoningContent},
						},
					})
				}
				if len(payload.ToolCall) == 0 {
					continue
				}
				toolCall := &agentv1.ToolCall{}
				if err := protojson.Unmarshal(payload.ToolCall, toolCall); err != nil {
					return nil, err
				}
				steps = append(steps, &agentv1.ConversationStep{
					Message: &agentv1.ConversationStep_ToolCall{ToolCall: toolCall},
				})
			}
		}
		if len(userMessageID) == 0 {
			continue
		}
		stepIDs := make([][]byte, 0, len(steps))
		for _, step := range steps {
			stepID, err := addCheckpointStepBlob(blobs, step)
			if err != nil {
				return nil, err
			}
			stepIDs = append(stepIDs, stepID)
		}
		agentTurn := &agentv1.AgentConversationTurnStructure{
			UserMessage: userMessageID,
			Steps:       stepIDs,
		}
		if turnRequestID != "" {
			agentTurn.RequestId = &turnRequestID
		}
		turnPayload, err := proto.Marshal(&agentv1.ConversationTurnStructure{
			Turn: &agentv1.ConversationTurnStructure_AgentConversationTurn{
				AgentConversationTurn: agentTurn,
			},
		})
		if err != nil {
			return nil, err
		}
		turnIDs = append(turnIDs, blobs.add(turnPayload))
	}
	return turnIDs, nil
}

func checkpointTurnHasUserMessage(entries []HistoryEntry) bool {
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) == "user_message" {
			return true
		}
	}
	return false
}

func collectCheckpointCompletedToolCalls(entries []HistoryEntry) (map[string]json.RawMessage, error) {
	completed := make(map[string]json.RawMessage)
	for _, entry := range entries {
		if strings.TrimSpace(entry.Kind) != "tool_result" {
			continue
		}
		var payload toolResultEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return nil, err
		}
		if toolCallID := strings.TrimSpace(payload.ToolCallID); toolCallID != "" && len(payload.ToolCall) > 0 {
			completed[toolCallID] = payload.ToolCall
		}
	}
	return completed, nil
}

func addCheckpointStepBlob(blobs *checkpointBlobGraph, step *agentv1.ConversationStep) ([]byte, error) {
	payload, err := proto.Marshal(step)
	if err != nil {
		return nil, err
	}
	return blobs.add(payload), nil
}

func conversationTokenDetailsUsedTokens(conversation *ConversationFile) uint32 {
	if conversation == nil {
		return 0
	}
	return conversation.TokenDetailsUsedTokens
}

func conversationTokenDetailsMaxTokens(conversation *ConversationFile) uint32 {
	if conversation == nil || conversation.TokenDetailsMaxTokens == 0 {
		return projectedConversationMaxTokens
	}
	return conversation.TokenDetailsMaxTokens
}

func latestCompactionSummaryBytes(conversation *ConversationFile) []byte {
	texts := compactionSummaryTexts(conversation)
	if len(texts) == 0 {
		return nil
	}
	return encodeConversationSummaryBytes(texts[len(texts)-1])
}

func previousCompactionSummaryBytes(conversation *ConversationFile) []byte {
	texts := compactionSummaryTexts(conversation)
	if len(texts) < 2 {
		return nil
	}
	return encodeConversationSummaryBytes(texts[len(texts)-2])
}

func compactionSummaryArchives(conversation *ConversationFile) [][]byte {
	texts := compactionSummaryTexts(conversation)
	if len(texts) == 0 {
		return nil
	}
	archives := make([][]byte, 0, len(texts))
	for _, text := range texts {
		if encoded := encodeConversationSummaryBytes(text); len(encoded) > 0 {
			archives = append(archives, encoded)
		}
	}
	return archives
}

func compactionSummaryTexts(conversation *ConversationFile) []string {
	if conversation == nil || len(conversation.Entries) == 0 {
		return nil
	}
	texts := make([]string, 0)
	for _, entry := range conversation.Entries {
		if !isCompactionSummaryKind(entry.Kind) {
			continue
		}
		if text, ok := decodeCompactionSummaryEntry(entry); ok {
			texts = append(texts, text)
		}
	}
	return texts
}

func isCompactionSummaryKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "compaction_summary", "compacted_summary":
		return true
	default:
		return false
	}
}

func applyPromptProviderMetadataToFirstToolCall(message *promptengine.Message, providerItemID string, providerCallID string, providerStatus string) {
	if message == nil || len(message.ToolCalls) == 0 {
		return
	}
	message.ToolCalls[0].OpenAIResponsesID = strings.TrimSpace(providerItemID)
	message.ToolCalls[0].OpenAIResponsesCallID = strings.TrimSpace(providerCallID)
	message.ToolCalls[0].OpenAIResponsesStatus = strings.TrimSpace(providerStatus)
}

func normalizeReplayMessageSequence(messages []modeladapter.Message) []modeladapter.Message {
	return projectedReplayMessagesToModel(normalizeProjectedReplayMessages(wrapProjectedReplayMessages(messages)))
}

func normalizeProjectedReplayMessages(messages []projectedReplayMessage) []projectedReplayMessage {
	if len(messages) == 0 {
		return nil
	}
	normalized := make([]projectedReplayMessage, 0, len(messages))
	for _, item := range messages {
		message := cloneProjectedReplayMessage(item)
		if mergeProjectedReplayAssistantToolCalls(&normalized, message) {
			continue
		}
		normalized = append(normalized, message)
	}
	normalized = filterProjectedProviderSuppressedToolReplayMessages(normalized)
	normalized = coalesceProjectedInterleavedReplayToolBatches(normalized)
	return rehomeOrphanedReplayReasoning(trimProjectedReplayDanglingAssistantToolCalls(normalized))
}

func filterProjectedProviderSuppressedToolReplayMessages(messages []projectedReplayMessage) []projectedReplayMessage {
	if len(messages) == 0 {
		return nil
	}
	filtered := make([]projectedReplayMessage, 0, len(messages))
	skippedToolCallIDs := make(map[string]struct{})
	for _, item := range messages {
		message := cloneProjectedReplayMessage(item)
		if strings.TrimSpace(message.Role) == "assistant" && len(message.ToolCalls) > 0 {
			nextToolCalls := make([]modeladapter.ToolCallDescriptor, 0, len(message.ToolCalls))
			for _, toolCall := range message.ToolCalls {
				if isProviderPromptReplaySuppressedToolName(toolCall.Function.Name) {
					if toolCallID := strings.TrimSpace(toolCall.ID); toolCallID != "" {
						skippedToolCallIDs[toolCallID] = struct{}{}
					}
					continue
				}
				toolCall.Index = len(nextToolCalls)
				nextToolCalls = append(nextToolCalls, toolCall)
			}
			if len(nextToolCalls) == 0 && strings.TrimSpace(message.Content) == "" && len(message.ContentParts) == 0 && !hasReplayableReasoningPayload(message.ReasoningContent, message.ReasoningSignature, message.ReasoningSignatureSource) {
				continue
			}
			message.ToolCalls = nextToolCalls
			filtered = append(filtered, message)
			continue
		}
		if strings.TrimSpace(message.Role) == "tool" {
			if _, ok := skippedToolCallIDs[strings.TrimSpace(message.ToolCallID)]; ok {
				continue
			}
			if isProviderPromptReplaySuppressedToolName(message.Name) {
				continue
			}
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func isProviderPromptReplaySuppressedToolName(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "GenerateImage":
		return true
	default:
		return false
	}
}

func cloneReplayModelMessage(message modeladapter.Message) modeladapter.Message {
	cloned := message
	if len(message.ContentParts) > 0 {
		cloned.ContentParts = append([]modeladapter.ContentPart(nil), message.ContentParts...)
	}
	if len(message.ToolCalls) > 0 {
		cloned.ToolCalls = append([]modeladapter.ToolCallDescriptor(nil), message.ToolCalls...)
	}
	if len(message.OpenAIResponsesReasoningSummary) > 0 {
		cloned.OpenAIResponsesReasoningSummary = append(json.RawMessage(nil), message.OpenAIResponsesReasoningSummary...)
	}
	return cloned
}

func mergeProjectedReplayAssistantToolCalls(messages *[]projectedReplayMessage, message projectedReplayMessage) bool {
	if len(*messages) == 0 {
		return false
	}
	last := &(*messages)[len(*messages)-1]
	if !canMergeProjectedReplayAssistantToolCalls(*last, message) {
		return false
	}
	startIndex := len(last.ToolCalls)
	for index, toolCall := range message.ToolCalls {
		item := toolCall
		item.Index = startIndex + index
		last.ToolCalls = append(last.ToolCalls, item)
	}
	last.ReasoningContent = mergeReplayReasoning(last.ReasoningContent, message.ReasoningContent)
	mergeReplayReasoningMetadata(&last.Message, message.Message)
	if strings.TrimSpace(last.replayAggregationKey) == "" {
		last.replayAggregationKey = message.replayAggregationKey
	}
	return true
}

func canMergeProjectedReplayAssistantToolCalls(last projectedReplayMessage, current projectedReplayMessage) bool {
	if !canMergeReplayAssistantToolCalls(last.Message, current.Message) {
		return false
	}
	return strings.TrimSpace(last.replayAggregationKey) == strings.TrimSpace(current.replayAggregationKey)
}

func canMergeReplayAssistantToolCalls(last modeladapter.Message, current modeladapter.Message) bool {
	if strings.TrimSpace(last.Role) != "assistant" || strings.TrimSpace(current.Role) != "assistant" {
		return false
	}
	if len(last.ToolCalls) == 0 || len(current.ToolCalls) == 0 {
		return false
	}
	if strings.TrimSpace(last.ToolCallID) != "" || strings.TrimSpace(last.Name) != "" {
		return false
	}
	if strings.TrimSpace(current.ToolCallID) != "" || strings.TrimSpace(current.Name) != "" {
		return false
	}
	if strings.TrimSpace(current.Content) != "" || len(current.ContentParts) > 0 {
		return false
	}
	return true
}

func coalesceProjectedInterleavedReplayToolBatches(messages []projectedReplayMessage) []projectedReplayMessage {
	if len(messages) == 0 {
		return nil
	}
	normalized := make([]projectedReplayMessage, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		message := cloneProjectedReplayMessage(messages[index])
		groupID := replayAssistantToolGroupID(message.Message)
		if groupID == "" {
			normalized = append(normalized, message)
			continue
		}

		batch := message
		toolResults := make(map[string]projectedReplayMessage)
		toolResultOrder := make([]string, 0)
		changed := false
		nextIndex := index + 1
		for nextIndex < len(messages) {
			next := cloneProjectedReplayMessage(messages[nextIndex])
			if strings.TrimSpace(next.Role) == "tool" && replayToolCallGroupID(next.ToolCallID) == groupID {
				toolCallID := strings.TrimSpace(next.ToolCallID)
				if _, ok := toolResults[toolCallID]; !ok {
					toolResultOrder = append(toolResultOrder, toolCallID)
				}
				toolResults[toolCallID] = next
				changed = true
				nextIndex++
				continue
			}
			if replayAssistantToolGroupID(next.Message) == groupID && canMergeProjectedReplayAssistantToolCalls(batch, next) {
				startIndex := len(batch.ToolCalls)
				for toolIndex, toolCall := range next.ToolCalls {
					item := toolCall
					item.Index = startIndex + toolIndex
					batch.ToolCalls = append(batch.ToolCalls, item)
				}
				batch.ReasoningContent = mergeReplayReasoning(batch.ReasoningContent, next.ReasoningContent)
				mergeReplayReasoningMetadata(&batch.Message, next.Message)
				if strings.TrimSpace(batch.replayAggregationKey) == "" {
					batch.replayAggregationKey = next.replayAggregationKey
				}
				changed = true
				nextIndex++
				continue
			}
			break
		}

		if !changed {
			normalized = append(normalized, message)
			continue
		}
		normalized = append(normalized, batch)
		emittedResults := make(map[string]struct{}, len(toolResults))
		for _, toolCall := range batch.ToolCalls {
			toolCallID := strings.TrimSpace(toolCall.ID)
			result, ok := toolResults[toolCallID]
			if !ok {
				continue
			}
			normalized = append(normalized, result)
			emittedResults[toolCallID] = struct{}{}
		}
		for _, toolCallID := range toolResultOrder {
			if _, ok := emittedResults[toolCallID]; ok {
				continue
			}
			normalized = append(normalized, toolResults[toolCallID])
		}
		index = nextIndex - 1
	}
	return normalized
}

func replayAssistantToolGroupID(message modeladapter.Message) string {
	if strings.TrimSpace(message.Role) != "assistant" || len(message.ToolCalls) == 0 {
		return ""
	}
	groupID := ""
	for _, toolCall := range message.ToolCalls {
		nextGroupID := replayToolCallGroupID(toolCall.ID)
		if nextGroupID == "" {
			return ""
		}
		if groupID == "" {
			groupID = nextGroupID
			continue
		}
		if groupID != nextGroupID {
			return ""
		}
	}
	return groupID
}

func replayToolCallGroupID(toolCallID string) string {
	trimmed := strings.TrimSpace(toolCallID)
	if trimmed == "" {
		return ""
	}
	if namespace, _, ok := strings.Cut(trimmed, "::"); ok {
		return strings.TrimSpace(namespace)
	}
	if strings.HasPrefix(trimmed, "tc_") {
		parts := strings.SplitN(trimmed, "_", 3)
		if len(parts) >= 2 && strings.TrimSpace(parts[1]) != "" {
			return "tc_" + strings.TrimSpace(parts[1])
		}
	}
	return ""
}

type projectedReplayMessage struct {
	modeladapter.Message
	replayAggregationKey string
}

func newProjectedReplayMessage(message modeladapter.Message, entry HistoryEntry) projectedReplayMessage {
	return projectedReplayMessage{
		Message:              cloneReplayModelMessage(message),
		replayAggregationKey: replayModelCallAggregationKey(entry),
	}
}

func cloneProjectedReplayMessage(item projectedReplayMessage) projectedReplayMessage {
	return projectedReplayMessage{
		Message:              cloneReplayModelMessage(item.Message),
		replayAggregationKey: item.replayAggregationKey,
	}
}

func wrapProjectedReplayMessages(messages []modeladapter.Message) []projectedReplayMessage {
	if len(messages) == 0 {
		return nil
	}
	items := make([]projectedReplayMessage, 0, len(messages))
	for _, message := range messages {
		items = append(items, projectedReplayMessage{Message: cloneReplayModelMessage(message)})
	}
	return items
}

func projectedReplayMessagesToModel(items []projectedReplayMessage) []modeladapter.Message {
	if len(items) == 0 {
		return nil
	}
	messages := make([]modeladapter.Message, 0, len(items))
	for _, item := range items {
		messages = append(messages, cloneReplayModelMessage(item.Message))
	}
	return messages
}

type replayReasoningEmissionTracker struct {
	emittedCalls  map[string]int
	emittedLegacy map[string]int
}

func newReplayReasoningEmissionTracker() *replayReasoningEmissionTracker {
	return &replayReasoningEmissionTracker{
		emittedCalls:  make(map[string]int),
		emittedLegacy: make(map[string]int),
	}
}

func replayModelCallAggregationKey(entry HistoryEntry) string {
	requestID := strings.TrimSpace(entry.RequestID)
	modelCallID := strings.TrimSpace(entry.ModelCallID)
	if requestID == "" || modelCallID == "" {
		return ""
	}
	return fmt.Sprintf("%d\x1f%s\x1f%s", entry.TurnSeq, requestID, modelCallID)
}

func replayLegacyReasoningIdentityKey(entry HistoryEntry, message modeladapter.Message) string {
	requestID := strings.TrimSpace(entry.RequestID)
	itemID := strings.TrimSpace(message.OpenAIResponsesReasoningID)
	signature := strings.TrimSpace(message.ReasoningSignature)
	if requestID == "" || itemID == "" || signature == "" {
		return ""
	}
	return fmt.Sprintf("%d\x1f%s\x1f%s\x1f%s", entry.TurnSeq, requestID, itemID, signature)
}

func messageHasReplayReasoningTuple(message modeladapter.Message) bool {
	if hasReplayableReasoningPayload(message.ReasoningContent, message.ReasoningSignature, message.ReasoningSignatureSource) {
		return true
	}
	return strings.TrimSpace(message.OpenAIResponsesReasoningID) != "" ||
		strings.TrimSpace(message.OpenAIResponsesReasoningStatus) != "" ||
		len(message.OpenAIResponsesReasoningSummary) > 0
}

func clearReplayReasoning(message *modeladapter.Message) {
	if message == nil {
		return
	}
	message.ReasoningContent = ""
	message.ReasoningSignature = ""
	message.ReasoningSignatureSource = ""
	message.OpenAIResponsesReasoningID = ""
	message.OpenAIResponsesReasoningStatus = ""
	message.OpenAIResponsesReasoningSummary = nil
}

func copyReplayReasoning(dst *modeladapter.Message, src modeladapter.Message) {
	mergeReplayReasoningTuple(dst, src)
}

type replayReasoningTuple struct {
	content   string
	signature string
	source    string
	itemID    string
	status    string
	summary   json.RawMessage
}

func captureReplayReasoningTuple(message modeladapter.Message) replayReasoningTuple {
	tuple := replayReasoningTuple{
		content:   message.ReasoningContent,
		signature: message.ReasoningSignature,
		source:    message.ReasoningSignatureSource,
		itemID:    message.OpenAIResponsesReasoningID,
		status:    message.OpenAIResponsesReasoningStatus,
	}
	if len(message.OpenAIResponsesReasoningSummary) > 0 {
		tuple.summary = append(json.RawMessage(nil), message.OpenAIResponsesReasoningSummary...)
	}
	return tuple
}

func cloneReplayReasoningTuple(tuple replayReasoningTuple) replayReasoningTuple {
	cloned := tuple
	if len(tuple.summary) > 0 {
		cloned.summary = append(json.RawMessage(nil), tuple.summary...)
	} else {
		cloned.summary = nil
	}
	return cloned
}

func applyReplayReasoningTuple(dst *modeladapter.Message, tuple replayReasoningTuple) {
	if dst == nil {
		return
	}
	dst.ReasoningContent = tuple.content
	dst.ReasoningSignature = tuple.signature
	dst.ReasoningSignatureSource = tuple.source
	dst.OpenAIResponsesReasoningID = tuple.itemID
	dst.OpenAIResponsesReasoningStatus = tuple.status
	if len(tuple.summary) == 0 {
		dst.OpenAIResponsesReasoningSummary = nil
		return
	}
	dst.OpenAIResponsesReasoningSummary = append(json.RawMessage(nil), tuple.summary...)
}

func mergeReplayReasoningTuple(dst *modeladapter.Message, src modeladapter.Message) {
	if dst == nil {
		return
	}
	applyReplayReasoningTuple(dst, selectReplayReasoningTuple(captureReplayReasoningTuple(*dst), captureReplayReasoningTuple(src)))
}

func selectReplayReasoningTuple(current replayReasoningTuple, candidate replayReasoningTuple) replayReasoningTuple {
	currentContent := strings.TrimSpace(current.content)
	candidateContent := strings.TrimSpace(candidate.content)
	currentSig := strings.TrimSpace(current.signature)
	candidateSig := strings.TrimSpace(candidate.signature)
	exactContent := currentContent == candidateContent
	incompatible := replayReasoningContentsIncompatible(current.content, candidate.content)

	if currentSig != "" && candidateSig != "" && currentSig != candidateSig {
		if replayReasoningTupleMoreComplete(candidate, current) {
			return cloneReplayReasoningTuple(candidate)
		}
		return cloneReplayReasoningTuple(current)
	}

	if currentSig != "" && currentSig == candidateSig {
		if incompatible {
			if replayReasoningTupleMoreComplete(candidate, current) {
				return cloneReplayReasoningTuple(candidate)
			}
			return cloneReplayReasoningTuple(current)
		}
		if exactContent || candidateContent == "" {
			return fillReplayReasoningMatchingMetadata(cloneReplayReasoningTuple(current), candidate)
		}
		if currentContent == "" {
			return fillReplayReasoningMatchingMetadata(cloneReplayReasoningTuple(candidate), current)
		}
		return mergeReplayReasoningTerminalStatus(cloneReplayReasoningTuple(current), candidate)
	}

	if currentSig != "" && candidateSig == "" {
		if incompatible {
			return cloneReplayReasoningTuple(current)
		}
		if exactContent || candidateContent == "" {
			return fillReplayReasoningMatchingMetadata(cloneReplayReasoningTuple(current), candidate)
		}
		return mergeReplayReasoningTerminalStatus(cloneReplayReasoningTuple(current), candidate)
	}

	if currentSig == "" && candidateSig != "" {
		if exactContent {
			return fillReplayReasoningMatchingMetadata(cloneReplayReasoningTuple(current), candidate)
		}
		return cloneReplayReasoningTuple(candidate)
	}

	if incompatible {
		return cloneReplayReasoningTuple(current)
	}
	chosen := cloneReplayReasoningTuple(current)
	if replayReasoningContentMoreComplete(currentContent, candidateContent) || (currentContent == "" && candidateContent != "") {
		chosen.content = candidate.content
	}
	return fillReplayReasoningMatchingMetadata(chosen, candidate)
}

func fillReplayReasoningMatchingMetadata(dst replayReasoningTuple, src replayReasoningTuple) replayReasoningTuple {
	dstSig := strings.TrimSpace(dst.signature)
	srcSig := strings.TrimSpace(src.signature)
	srcSource := strings.TrimSpace(src.source)
	if srcSig != "" {
		switch {
		case dstSig == "":
			dst.signature = src.signature
			if srcSource != "" {
				dst.source = src.source
			}
		case dstSig == srcSig:
			if strings.TrimSpace(dst.source) == "" && srcSource != "" {
				dst.source = src.source
			}
		}
	}
	if itemID := strings.TrimSpace(src.itemID); itemID != "" && strings.TrimSpace(dst.itemID) == "" {
		dst.itemID = src.itemID
	}
	dst = mergeReplayReasoningTerminalStatus(dst, src)
	if strings.TrimSpace(string(src.summary)) != "" && strings.TrimSpace(string(dst.summary)) == "" {
		dst.summary = append(json.RawMessage(nil), src.summary...)
	}
	return dst
}

func mergeReplayReasoningTerminalStatus(dst replayReasoningTuple, src replayReasoningTuple) replayReasoningTuple {
	if status := strings.TrimSpace(src.status); status != "" {
		if strings.TrimSpace(dst.status) == "" || replayReasoningStatusMoreComplete(dst.status, status) {
			dst.status = src.status
		}
	}
	return dst
}

func replayReasoningTupleMoreComplete(candidate replayReasoningTuple, current replayReasoningTuple) bool {
	candidateReplayable := hasReplayableReasoningPayload(candidate.content, candidate.signature, candidate.source)
	currentReplayable := hasReplayableReasoningPayload(current.content, current.signature, current.source)
	if candidateReplayable != currentReplayable {
		return candidateReplayable
	}
	candidateLen := len(strings.TrimSpace(candidate.content))
	currentLen := len(strings.TrimSpace(current.content))
	if candidateLen != currentLen {
		return candidateLen > currentLen
	}
	if (strings.TrimSpace(candidate.signature) != "") != (strings.TrimSpace(current.signature) != "") {
		return strings.TrimSpace(candidate.signature) != ""
	}
	if (strings.TrimSpace(candidate.source) != "") != (strings.TrimSpace(current.source) != "") {
		return strings.TrimSpace(candidate.source) != ""
	}
	candidateSummary := strings.TrimSpace(string(candidate.summary)) != ""
	currentSummary := strings.TrimSpace(string(current.summary)) != ""
	if candidateSummary != currentSummary {
		return candidateSummary
	}
	if (strings.TrimSpace(candidate.itemID) != "") != (strings.TrimSpace(current.itemID) != "") {
		return strings.TrimSpace(candidate.itemID) != ""
	}
	candidateTerminal := replayReasoningStatusTerminal(candidate.status)
	currentTerminal := replayReasoningStatusTerminal(current.status)
	if candidateTerminal != currentTerminal {
		return candidateTerminal
	}
	return false
}

func replayReasoningContentsCompatible(left string, right string) bool {
	return !replayReasoningContentsIncompatible(left, right)
}

func replayReasoningContentsIncompatible(left string, right string) bool {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	if left == "" || right == "" || left == right {
		return false
	}
	return !strings.HasPrefix(right, left) && !strings.HasPrefix(left, right)
}

func replayReasoningContentMoreComplete(current string, candidate string) bool {
	current = strings.TrimSpace(current)
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || candidate == current {
		return false
	}
	if current == "" {
		return true
	}
	return strings.HasPrefix(candidate, current) && len(candidate) > len(current)
}

func replayReasoningStatusMoreComplete(current string, candidate string) bool {
	current = strings.TrimSpace(current)
	candidate = strings.TrimSpace(candidate)
	if candidate == "" || candidate == current {
		return false
	}
	if current == "" {
		return true
	}
	return replayReasoningStatusTerminal(candidate) && !replayReasoningStatusTerminal(current)
}

func replayReasoningStatusTerminal(status string) bool {
	switch strings.TrimSpace(status) {
	case "completed", "incomplete":
		return true
	default:
		return false
	}
}

func applyProjectedReplayReasoning(tracker *replayReasoningEmissionTracker, entry HistoryEntry, messages []projectedReplayMessage) {
	if tracker == nil || len(messages) == 0 {
		return
	}
	message := &messages[len(messages)-1].Message
	if strings.TrimSpace(message.Role) != "assistant" {
		return
	}
	if !messageHasReplayReasoningTuple(*message) {
		return
	}
	if callKey := replayModelCallAggregationKey(entry); callKey != "" {
		applyReplayReasoningEmission(tracker.emittedCalls, callKey, messages)
		return
	}
	if legacyKey := replayLegacyReasoningIdentityKey(entry, *message); legacyKey != "" {
		applyReplayReasoningEmission(tracker.emittedLegacy, legacyKey, messages)
	}
}

func applyReplayReasoningEmission(emitted map[string]int, key string, messages []projectedReplayMessage) {
	if emitted == nil || key == "" || len(messages) == 0 {
		return
	}
	currentIndex := len(messages) - 1
	occupiedIndex, occupied := emitted[key]
	if !occupied {
		emitted[key] = currentIndex
		return
	}
	if occupiedIndex >= 0 && occupiedIndex < len(messages) {
		mergeReplayReasoningTuple(&messages[occupiedIndex].Message, messages[currentIndex].Message)
	}
	clearReplayReasoning(&messages[currentIndex].Message)
}

func isOrphanedReplayReasoningCarrier(message modeladapter.Message) bool {
	if strings.TrimSpace(message.Role) != "assistant" {
		return false
	}
	if strings.TrimSpace(message.Content) != "" || len(message.ContentParts) > 0 || len(message.ToolCalls) > 0 {
		return false
	}
	return messageHasReplayReasoningTuple(message)
}

func nextReplayAssistantIndexBeforeUser(messages []projectedReplayMessage, from int) int {
	for index := from + 1; index < len(messages); index++ {
		switch strings.TrimSpace(messages[index].Role) {
		case "user":
			return -1
		case "assistant":
			return index
		}
	}
	return -1
}

func sameProjectedReplayAggregationKey(left projectedReplayMessage, right projectedReplayMessage) bool {
	leftKey := strings.TrimSpace(left.replayAggregationKey)
	rightKey := strings.TrimSpace(right.replayAggregationKey)
	return leftKey != "" && leftKey == rightKey
}

func rehomeOrphanedReplayReasoning(messages []projectedReplayMessage) []projectedReplayMessage {
	if len(messages) == 0 {
		return nil
	}
	rehomed := make([]projectedReplayMessage, 0, len(messages))
	for index := 0; index < len(messages); index++ {
		message := cloneProjectedReplayMessage(messages[index])
		if !isOrphanedReplayReasoningCarrier(message.Message) {
			rehomed = append(rehomed, message)
			continue
		}
		target := nextReplayAssistantIndexBeforeUser(messages, index)
		if target < 0 || messageHasReplayReasoningTuple(messages[target].Message) || !sameProjectedReplayAggregationKey(message, messages[target]) {
			rehomed = append(rehomed, message)
			continue
		}
		mergeReplayReasoningTuple(&messages[target].Message, message.Message)
	}
	return rehomed
}

func mergeReplayReasoning(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "", right == left:
		return left
	default:
		return left + "\n\n" + right
	}
}

func mergeReplayReasoningSignature(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "", right == left:
		return left
	default:
		return ""
	}
}

func mergeReplayReasoningSignatureSource(left string, right string) string {
	left = strings.TrimSpace(left)
	right = strings.TrimSpace(right)
	switch {
	case left == "":
		return right
	case right == "", right == left:
		return left
	default:
		return ""
	}
}

func mergeReplayReasoningMetadata(last *modeladapter.Message, current modeladapter.Message) {
	if last == nil {
		return
	}
	leftSignature := strings.TrimSpace(last.ReasoningSignature)
	rightSignature := strings.TrimSpace(current.ReasoningSignature)
	mergedSignature := mergeReplayReasoningSignature(leftSignature, rightSignature)
	last.ReasoningSignature = mergedSignature
	if mergedSignature == "" {
		last.ReasoningSignatureSource = ""
		last.OpenAIResponsesReasoningID = ""
		last.OpenAIResponsesReasoningStatus = ""
		last.OpenAIResponsesReasoningSummary = nil
		return
	}
	if leftSignature == "" && rightSignature != "" {
		last.ReasoningSignatureSource = strings.TrimSpace(current.ReasoningSignatureSource)
		last.OpenAIResponsesReasoningID = current.OpenAIResponsesReasoningID
		last.OpenAIResponsesReasoningStatus = current.OpenAIResponsesReasoningStatus
		last.OpenAIResponsesReasoningSummary = append(json.RawMessage(nil), current.OpenAIResponsesReasoningSummary...)
		return
	}
	if leftSignature == rightSignature {
		last.ReasoningSignatureSource = mergeReplayReasoningSignatureSource(last.ReasoningSignatureSource, current.ReasoningSignatureSource)
		if strings.TrimSpace(last.OpenAIResponsesReasoningID) == "" {
			last.OpenAIResponsesReasoningID = current.OpenAIResponsesReasoningID
		}
		if strings.TrimSpace(last.OpenAIResponsesReasoningStatus) == "" {
			last.OpenAIResponsesReasoningStatus = current.OpenAIResponsesReasoningStatus
		}
		if len(last.OpenAIResponsesReasoningSummary) == 0 {
			last.OpenAIResponsesReasoningSummary = append(json.RawMessage(nil), current.OpenAIResponsesReasoningSummary...)
		}
	}
}

// replayToolResponseWindowEnd 返回 assistant tool-call 消息的响应收集窗口右边界（不含）。
// 窗口内除了 tool 结果消息，还允许出现同轮穿插的纯文本 assistant 消息：
// 部分模型（如 gpt-5.3-codex-spark）会在同一条响应里先输出 function_call 再输出说明文本，
// 落盘顺序为 tool_call → assistant_text → tool_result，若只收集紧邻的 tool 消息，
// 会把有结果回放的调用误判为悬空。
func replayToolResponseWindowEnd(messages []projectedReplayMessage, index int) int {
	end := index + 1
	for end < len(messages) {
		candidate := messages[end]
		switch {
		case strings.TrimSpace(candidate.Role) == "tool":
			end++
		case strings.TrimSpace(candidate.Role) == "assistant" && len(candidate.ToolCalls) == 0:
			end++
		default:
			return end
		}
	}
	return end
}

func trimReplayDanglingAssistantToolCalls(messages []modeladapter.Message) []modeladapter.Message {
	return projectedReplayMessagesToModel(trimProjectedReplayDanglingAssistantToolCalls(wrapProjectedReplayMessages(messages)))
}

func trimProjectedReplayDanglingAssistantToolCalls(messages []projectedReplayMessage) []projectedReplayMessage {
	if len(messages) == 0 {
		return nil
	}
	survivingToolCallIDs := make(map[string]struct{})
	for index, message := range messages {
		if strings.TrimSpace(message.Role) != "assistant" || len(message.ToolCalls) == 0 {
			continue
		}
		responded := make(map[string]struct{}, len(message.ToolCalls))
		for scan := index + 1; scan < replayToolResponseWindowEnd(messages, index); scan++ {
			if strings.TrimSpace(messages[scan].Role) != "tool" {
				continue
			}
			if toolCallID := strings.TrimSpace(messages[scan].ToolCallID); toolCallID != "" {
				responded[toolCallID] = struct{}{}
			}
		}
		for _, toolCall := range message.ToolCalls {
			if toolCallID := strings.TrimSpace(toolCall.ID); toolCallID != "" {
				if _, ok := responded[toolCallID]; ok {
					survivingToolCallIDs[toolCallID] = struct{}{}
				}
			}
		}
	}
	trimmed := make([]projectedReplayMessage, 0, len(messages))
	for _, item := range messages {
		message := cloneProjectedReplayMessage(item)
		if strings.TrimSpace(message.Role) == "assistant" && len(message.ToolCalls) > 0 {
			nextToolCalls := make([]modeladapter.ToolCallDescriptor, 0, len(message.ToolCalls))
			for _, toolCall := range message.ToolCalls {
				if _, ok := survivingToolCallIDs[strings.TrimSpace(toolCall.ID)]; !ok {
					continue
				}
				toolCall.Index = len(nextToolCalls)
				nextToolCalls = append(nextToolCalls, toolCall)
			}
			if len(nextToolCalls) == 0 {
				if strings.TrimSpace(message.Content) == "" && len(message.ContentParts) == 0 && !hasReplayableReasoningPayload(message.ReasoningContent, message.ReasoningSignature, message.ReasoningSignatureSource) {
					continue
				}
				message.ToolCalls = nil
			} else {
				message.ToolCalls = nextToolCalls
			}
			trimmed = append(trimmed, message)
			continue
		}
		if strings.TrimSpace(message.Role) == "tool" && strings.TrimSpace(message.ToolCallID) != "" {
			if _, ok := survivingToolCallIDs[strings.TrimSpace(message.ToolCallID)]; !ok {
				continue
			}
		}
		trimmed = append(trimmed, message)
	}
	return trimmed
}

func shouldPersistCheckpointReplayToolResultName(toolName string) bool {
	switch strings.TrimSpace(toolName) {
	case "PatchEdit", "PatchEditLines", "PatchEditSpan", "Edit", "Write", "GenerateImage":
		return true
	default:
		return false
	}
}

func filterCheckpointPersistentToolReplay(messages []promptengine.Message) []promptengine.Message {
	if len(messages) == 0 {
		return nil
	}
	filtered := make([]promptengine.Message, 0, len(messages))
	skippedToolCallIDs := make(map[string]struct{})
	for _, message := range messages {
		if strings.TrimSpace(message.Role) == "assistant" && len(message.ToolCalls) > 0 {
			nextToolCalls := make([]promptengine.ToolCallDescriptor, 0, len(message.ToolCalls))
			for _, toolCall := range message.ToolCalls {
				if !shouldPersistCheckpointReplayToolResultName(toolCall.Function.Name) {
					skippedToolCallIDs[strings.TrimSpace(toolCall.ID)] = struct{}{}
					continue
				}
				nextToolCalls = append(nextToolCalls, toolCall)
			}
			if len(nextToolCalls) == 0 && strings.TrimSpace(message.Content) == "" && !hasReplayableReasoningPayload(message.ReasoningContent, message.ReasoningSignature, message.ReasoningSignatureSource) {
				continue
			}
			message.ToolCalls = nextToolCalls
			filtered = append(filtered, message)
			continue
		}
		if strings.TrimSpace(message.Role) == "tool" {
			if _, ok := skippedToolCallIDs[strings.TrimSpace(message.ToolCallID)]; ok {
				continue
			}
			if !shouldPersistCheckpointReplayToolResultName(message.Name) {
				continue
			}
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func restoreImportedReplayUserMessages(messages []promptengine.Message, importedTurns [][]byte, blobs importedBlobStore) []promptengine.Message {
	if len(messages) == 0 || len(importedTurns) == 0 {
		return messages
	}
	cursor := 0
	for _, rawTurn := range importedTurns {
		if len(rawTurn) == 0 {
			continue
		}
		turn, _, err := decodeImportedTurn(rawTurn, blobs)
		if err != nil || turn == nil {
			continue
		}
		agentTurn := turn.GetAgentConversationTurn()
		if agentTurn == nil || len(agentTurn.GetUserMessage()) == 0 {
			continue
		}
		userMessage, err := decodeImportedUserMessage(agentTurn.GetUserMessage(), blobs)
		if err != nil {
			continue
		}
		replay, ok := promptengine.BuildUserMessageReplayMessage(userMessage)
		if !ok || len(replay.ContentParts) == 0 {
			continue
		}
		for cursor < len(messages) {
			if strings.TrimSpace(messages[cursor].Role) == "user" &&
				strings.TrimSpace(messages[cursor].Content) == strings.TrimSpace(replay.Content) {
				if len(messages[cursor].ContentParts) == 0 {
					messages[cursor].ContentParts = replay.ContentParts
				}
				if strings.TrimSpace(messages[cursor].Content) == "" {
					messages[cursor].Content = replay.Content
				}
				cursor++
				break
			}
			cursor++
		}
	}
	return messages
}

func isLegacyPlainWriteReplay(toolName string, hasStructuredToolCall bool) bool {
	return !hasStructuredToolCall && strings.TrimSpace(toolName) == "Write"
}

func overrideToolReplayFromEntry(messages []promptengine.Message, toolName string, arguments string) {
	overrideName := strings.TrimSpace(toolName)
	overrideArgs := strings.TrimSpace(arguments)
	if overrideName == "" || len(messages) == 0 {
		return
	}
	for index := range messages {
		switch strings.TrimSpace(messages[index].Role) {
		case "assistant":
			if len(messages[index].ToolCalls) == 0 {
				continue
			}
			for toolIndex := range messages[index].ToolCalls {
				currentName := strings.TrimSpace(messages[index].ToolCalls[toolIndex].Function.Name)
				effectiveName := effectiveReplayToolName(currentName, overrideName)
				effectiveArgs := firstNonEmpty(
					overrideArgs,
					strings.TrimSpace(messages[index].ToolCalls[toolIndex].Function.Arguments),
					"{}",
				)
				if isLegacyPatchEditToolName(overrideName) {
					effectiveArgs = firstNonEmpty(strings.TrimSpace(messages[index].ToolCalls[toolIndex].Function.Arguments), "{}")
				}
				messages[index].ToolCalls[toolIndex].Function.Name = effectiveName
				messages[index].ToolCalls[toolIndex].Function.Arguments = firstNonEmpty(effectiveArgs, "{}")
			}
		case "tool":
			messages[index].Name = effectiveReplayToolName(strings.TrimSpace(messages[index].Name), overrideName)
		}
	}
}

func overrideModelToolReplayFromEntry(message *modeladapter.Message, toolName string, arguments string) {
	if message == nil {
		return
	}
	overrideName := strings.TrimSpace(toolName)
	overrideArgs := strings.TrimSpace(arguments)
	if overrideName == "" {
		return
	}
	switch strings.TrimSpace(message.Role) {
	case "assistant":
		if len(message.ToolCalls) == 0 {
			return
		}
		for index := range message.ToolCalls {
			currentName := strings.TrimSpace(message.ToolCalls[index].Function.Name)
			effectiveName := effectiveReplayToolName(currentName, overrideName)
			effectiveArgs := firstNonEmpty(
				overrideArgs,
				strings.TrimSpace(message.ToolCalls[index].Function.Arguments),
				"{}",
			)
			if isLegacyPatchEditToolName(overrideName) {
				effectiveArgs = firstNonEmpty(strings.TrimSpace(message.ToolCalls[index].Function.Arguments), "{}")
			}
			message.ToolCalls[index].Function.Name = effectiveName
			message.ToolCalls[index].Function.Arguments = firstNonEmpty(effectiveArgs, "{}")
		}
	case "tool":
		message.Name = effectiveReplayToolName(strings.TrimSpace(message.Name), overrideName)
	}
}

func effectiveReplayToolName(currentName string, overrideName string) string {
	if isLegacyPatchEditToolName(currentName) || isLegacyPatchEditToolName(overrideName) {
		return "Edit"
	}
	switch strings.TrimSpace(currentName) {
	case "PatchEdit", "Edit", "Write":
		switch strings.TrimSpace(overrideName) {
		case "PatchEdit":
			return strings.TrimSpace(overrideName)
		case "Edit":
			return "Edit"
		case "Write":
			return "Write"
		}
		return strings.TrimSpace(currentName)
	default:
		return strings.TrimSpace(overrideName)
	}
}

func isLegacyPatchEditToolName(name string) bool {
	switch strings.TrimSpace(name) {
	case "PatchEditLines", "PatchEditSpan":
		return true
	default:
		return false
	}
}

func isLegacyPlainWriteToolCall(toolCall promptengine.ToolCallDescriptor) bool {
	if strings.TrimSpace(toolCall.Function.Name) != "Write" {
		return false
	}
	args := strings.TrimSpace(toolCall.Function.Arguments)
	return args == "" || args == "{}" || args == "null"
}

func filterLegacyPlainWriteReplay(messages []promptengine.Message) []promptengine.Message {
	if len(messages) == 0 {
		return nil
	}
	filtered := make([]promptengine.Message, 0, len(messages))
	skippedToolCallIDs := make(map[string]struct{})
	for _, message := range messages {
		if strings.TrimSpace(message.Role) == "assistant" && len(message.ToolCalls) > 0 {
			nextToolCalls := make([]promptengine.ToolCallDescriptor, 0, len(message.ToolCalls))
			for _, toolCall := range message.ToolCalls {
				if isLegacyPlainWriteToolCall(toolCall) {
					skippedToolCallIDs[strings.TrimSpace(toolCall.ID)] = struct{}{}
					continue
				}
				nextToolCalls = append(nextToolCalls, toolCall)
			}
			if len(nextToolCalls) == 0 && strings.TrimSpace(message.Content) == "" && !hasReplayableReasoningPayload(message.ReasoningContent, message.ReasoningSignature, message.ReasoningSignatureSource) {
				continue
			}
			message.ToolCalls = nextToolCalls
			filtered = append(filtered, message)
			continue
		}
		if strings.TrimSpace(message.Role) == "tool" {
			if _, ok := skippedToolCallIDs[strings.TrimSpace(message.ToolCallID)]; ok {
				continue
			}
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func filterInternalPromptContextReplay(messages []promptengine.Message) []promptengine.Message {
	if len(messages) == 0 {
		return nil
	}
	filtered := make([]promptengine.Message, 0, len(messages))
	for _, message := range messages {
		if isInternalPromptContextReplayMessage(message) {
			continue
		}
		filtered = append(filtered, message)
	}
	return filtered
}

func isInternalPromptContextReplayMessage(message promptengine.Message) bool {
	if strings.TrimSpace(message.Role) != "user" {
		return false
	}
	if strings.TrimSpace(message.Name) != "" || strings.TrimSpace(message.ToolCallID) != "" || len(message.ToolCalls) > 0 || len(message.ContentParts) > 0 {
		return false
	}
	if strings.TrimSpace(message.ReasoningContent) != "" || strings.TrimSpace(message.ReasoningSignature) != "" {
		return false
	}
	return isInternalPromptContextContent(message.Content)
}

func isInternalPromptContextContent(content string) bool {
	trimmed := strings.TrimSpace(content)
	switch {
	case trimmed == strings.TrimSpace(todoSectionReminderMessage):
		return true
	case strings.HasPrefix(trimmed, "<system_reminder>") &&
		strings.HasSuffix(trimmed, "</system_reminder>") &&
		strings.Contains(trimmed, "You recently successfully edited ") &&
		strings.Contains(trimmed, "latest source of truth is the most recent successful"):
		return true
	default:
		return false
	}
}

// toModelMessage 把 promptengine 的消息结构转换为 modeladapter 消息结构。
func toModelMessage(message promptengine.Message) modeladapter.Message {
	return modeladapter.Message{
		Role:                            message.Role,
		Content:                         message.Content,
		ContentParts:                    toModelContentParts(message.ContentParts),
		ReasoningContent:                message.ReasoningContent,
		ReasoningSignature:              message.ReasoningSignature,
		ReasoningSignatureSource:        message.ReasoningSignatureSource,
		OpenAIResponsesReasoningID:      message.OpenAIResponsesReasoningID,
		OpenAIResponsesReasoningStatus:  message.OpenAIResponsesReasoningStatus,
		OpenAIResponsesReasoningSummary: append(json.RawMessage(nil), message.OpenAIResponsesReasoningSummary...),
		ToolCalls:                       toModelToolCalls(message.ToolCalls),
		ToolCallID:                      message.ToolCallID,
		Name:                            message.Name,
	}
}

func toModelContentParts(items []promptengine.ContentPart) []modeladapter.ContentPart {
	if len(items) == 0 {
		return nil
	}
	output := make([]modeladapter.ContentPart, 0, len(items))
	for _, item := range items {
		part := modeladapter.ContentPart{
			Type: item.Type,
			Text: item.Text,
		}
		if item.Image != nil {
			part.Image = &modeladapter.ImageContent{
				MIMEType: item.Image.MIMEType,
				Path:     item.Image.Path,
				Data:     item.Image.Data,
			}
		}
		output = append(output, part)
	}
	return output
}

func toPromptContentParts(items []modeladapter.ContentPart) []promptengine.ContentPart {
	if len(items) == 0 {
		return nil
	}
	output := make([]promptengine.ContentPart, 0, len(items))
	for _, item := range items {
		part := promptengine.ContentPart{
			Type: item.Type,
			Text: item.Text,
		}
		if item.Image != nil {
			part.Image = &promptengine.ImageContent{
				MIMEType: item.Image.MIMEType,
				Path:     item.Image.Path,
				Data:     item.Image.Data,
			}
		}
		output = append(output, part)
	}
	return output
}

// toModelToolCalls 把 promptengine 的 tool call 描述转换为 modeladapter 版本。
func toModelToolCalls(items []promptengine.ToolCallDescriptor) []modeladapter.ToolCallDescriptor {
	output := make([]modeladapter.ToolCallDescriptor, 0, len(items))
	for _, item := range items {
		output = append(output, modeladapter.ToolCallDescriptor{
			ID:                    item.ID,
			Index:                 item.Index,
			Type:                  item.Type,
			OpenAIResponsesID:     item.OpenAIResponsesID,
			OpenAIResponsesCallID: item.OpenAIResponsesCallID,
			OpenAIResponsesStatus: item.OpenAIResponsesStatus,
			Function: modeladapter.ToolCallFunctionShape{
				Name:      item.Function.Name,
				Arguments: item.Function.Arguments,
			},
		})
	}
	return output
}

// toPromptToolCalls 把 modeladapter 的 tool call 描述转换回 promptengine 版本。
func toPromptToolCalls(items []modeladapter.ToolCallDescriptor) []promptengine.ToolCallDescriptor {
	output := make([]promptengine.ToolCallDescriptor, 0, len(items))
	for _, item := range items {
		output = append(output, promptengine.ToolCallDescriptor{
			ID:                    item.ID,
			Index:                 item.Index,
			Type:                  item.Type,
			OpenAIResponsesID:     item.OpenAIResponsesID,
			OpenAIResponsesCallID: item.OpenAIResponsesCallID,
			OpenAIResponsesStatus: item.OpenAIResponsesStatus,
			Function: promptengine.ToolCallFunctionShape{
				Name:      item.Function.Name,
				Arguments: item.Function.Arguments,
			},
		})
	}
	return output
}
