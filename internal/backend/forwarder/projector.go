// projector.go 负责把 JSON history 投影成 prompt replay 和 legacy checkpoint 视图。
package forwarder

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"

	"cursor/gen/agentv1"
	runtimecore "cursor/internal/backend/agent/core"
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

func (graph *checkpointBlobGraph) addOriginal(id []byte, data []byte) error {
	if graph == nil {
		return errNativeCheckpointNilGraph
	}
	if len(data) == 0 {
		return fmt.Errorf("native checkpoint: empty imported blob")
	}
	added := graph.add(data)
	if !bytes.Equal(added, id) {
		return fmt.Errorf("native checkpoint: imported blob id mismatch")
	}
	return nil
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
				ReasoningOrigin:                 payload.ReasoningOrigin,
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
			replayMessage.ReasoningOrigin = payload.ReasoningOrigin
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
				toolName := runtimecore.CanonicalToolName(strings.TrimSpace(payload.ToolName))
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
						replayMessages[index].ReasoningOrigin = payload.ReasoningOrigin
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
				ReasoningOrigin:                 payload.ReasoningOrigin,
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
	return projectCompactedPromptEntries(entries, true)
}

func compactedNativeCheckpointEntries(entries []HistoryEntry) []HistoryEntry {
	return projectCompactedPromptEntries(entries, false)
}

func projectCompactedPromptEntries(entries []HistoryEntry, rewritePreservedToolResults bool) []HistoryEntry {
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
	// §19.3g：保留集合只由摘要前缀（entries[:summaryIndex]）决定；摘要之后新增的条目
	// 不重新计算旧边界，压缩时点因而是冻结的。
	preservedIndexes := map[int]struct{}{}
	if compactionPayload.PreserveCurrentTurnInputs {
		prefix := entries[:compactionIndex]
		latestToolCallID := latestCompletedToolCallIDForTurn(prefix, compactionPayload.CurrentTurnSeq, compactionPayload.CurrentRequestID)
		preservedIndexes = autoCompactionPreservedEntryIndexes(prefix, compactionPayload.CurrentTurnSeq, compactionPayload.CurrentRequestID, latestToolCallID)
	}
	tailStart := continuousTailStartIndex(entries, compactionIndex, preservedIndexes)
	filtered := make([]HistoryEntry, 0, len(entries))
	for index := 0; index < compactionIndex; index++ {
		if !isPromptReplayEntryKind(entries[index].Kind) {
			filtered = append(filtered, entries[index])
		}
	}
	filtered = append(filtered, entries[compactionIndex])
	for index := tailStart; index < compactionIndex; index++ {
		if !isOrdinaryPromptReplayEntryKind(entries[index].Kind) {
			continue
		}
		entry := entries[index]
		// 结果展示裁剪只作用于模型视图的原保留集合；独立 native ROOT 使用未改写原文。
		if rewritePreservedToolResults {
			if _, ok := preservedIndexes[index]; ok {
				if rewritten, ok := compactedProjectionPreservedEntry(entry); ok {
					entry = rewritten
				}
			}
		}
		filtered = append(filtered, entry)
	}
	filtered = append(filtered, entries[compactionIndex+1:]...)
	return filtered
}

// isOrdinaryPromptReplayEntryKind 判定模型可见的普通 prompt 条目（排除旧摘要标记）。
func isOrdinaryPromptReplayEntryKind(kind string) bool {
	if isCompactionSummaryKind(kind) {
		return false
	}
	return isPromptReplayEntryKind(kind)
}

// continuousTailStartIndex 取保留集合中最早的普通 prompt 条目为连续尾段起点；无保留条目
// 时起点为摘要位置；起点落在工具批次中时向前扩展到完整批次首项，避免丢兄弟调用或悬空结果。
func continuousTailStartIndex(entries []HistoryEntry, compactionIndex int, preservedIndexes map[int]struct{}) int {
	start := compactionIndex
	for index := 0; index < compactionIndex; index++ {
		if _, ok := preservedIndexes[index]; !ok {
			continue
		}
		if !isOrdinaryPromptReplayEntryKind(entries[index].Kind) {
			continue
		}
		start = index
		break
	}
	return extendContinuousTailToNativeGroupStart(entries, extendContinuousTailToToolBatchStart(entries, start))
}

func extendContinuousTailToToolBatchStart(entries []HistoryEntry, start int) int {
	aligned, err := compactionToolBoundaryStart(entries, start)
	if err != nil {
		return start
	}
	return aligned
}

func compactionToolBoundaryStart(entries []HistoryEntry, start int) (int, error) {
	if start <= 0 || start >= len(entries) {
		return start, nil
	}
	if !isContinuousTailToolEntryKind(entries[start].Kind) {
		return start, nil
	}
	targetTurn := entries[start].TurnSeq
	targetRequestID := strings.TrimSpace(entries[start].RequestID)
	targetModelCallID := strings.TrimSpace(entries[start].ModelCallID)
	sameBatch := func(entry HistoryEntry) bool {
		if !isContinuousTailToolEntryKind(entry.Kind) {
			return false
		}
		if entry.TurnSeq != targetTurn || strings.TrimSpace(entry.RequestID) != targetRequestID {
			return false
		}
		return strings.TrimSpace(entry.ModelCallID) == targetModelCallID
	}
	callIndex := map[string]int{}
	for index := start - 1; index >= 0; index-- {
		entry := entries[index]
		if !isPromptReplayEntryKind(entry.Kind) {
			continue
		}
		if !sameBatch(entry) {
			break
		}
		if strings.TrimSpace(entry.Kind) != "tool_call" {
			continue
		}
		id := historyEntryToolCallID(entry)
		if id == "" {
			return start, fmt.Errorf("compaction prefix splits a complete group")
		}
		callIndex[id] = index
	}
	resultAfter := map[string]struct{}{}
	for index := start; index < len(entries); index++ {
		entry := entries[index]
		if !isPromptReplayEntryKind(entry.Kind) {
			continue
		}
		if !sameBatch(entry) {
			break
		}
		if strings.TrimSpace(entry.Kind) != "tool_result" {
			continue
		}
		id := historyEntryToolCallID(entry)
		if id == "" {
			return start, fmt.Errorf("compaction prefix splits a complete group")
		}
		resultAfter[id] = struct{}{}
	}
	earliest := start
	for id := range resultAfter {
		if idx, ok := callIndex[id]; ok {
			if idx < earliest {
				earliest = idx
			}
			continue
		}
		foundCall := false
		for index := start; index < len(entries); index++ {
			entry := entries[index]
			if !isPromptReplayEntryKind(entry.Kind) {
				continue
			}
			if !sameBatch(entry) {
				break
			}
			if strings.TrimSpace(entry.Kind) == "tool_call" && historyEntryToolCallID(entry) == id {
				foundCall = true
				break
			}
		}
		if !foundCall {
			return start, fmt.Errorf("compaction prefix splits a complete group")
		}
	}
	for id, idx := range callIndex {
		if _, ok := resultAfter[id]; ok {
			if idx < earliest {
				earliest = idx
			}
			continue
		}
		hasResultBefore := false
		for index := idx + 1; index < start; index++ {
			entry := entries[index]
			if !isPromptReplayEntryKind(entry.Kind) {
				continue
			}
			if !sameBatch(entry) {
				break
			}
			if strings.TrimSpace(entry.Kind) == "tool_result" && historyEntryToolCallID(entry) == id {
				hasResultBefore = true
				break
			}
		}
		if !hasResultBefore {
			if idx < earliest {
				earliest = idx
			}
		}
	}
	return earliest, nil
}

func extendContinuousTailToNativeGroupStart(entries []HistoryEntry, start int) int {
	if start <= 0 || start >= len(entries) {
		return start
	}
	if strings.TrimSpace(entries[start].Kind) != "model_message" {
		return start
	}
	var payload modelMessageEntryPayload
	if json.Unmarshal(entries[start].Payload, &payload) != nil {
		return start
	}
	if len(payload.NativeMessage) > 0 {
		return start
	}
	for index := start - 1; index >= 0; index-- {
		if strings.TrimSpace(entries[index].Kind) != "model_message" {
			break
		}
		var member modelMessageEntryPayload
		if json.Unmarshal(entries[index].Payload, &member) != nil {
			break
		}
		if len(member.NativeMessage) == 0 {
			continue
		}
		parts := member.NativeMessageParts
		if parts == 0 {
			parts = 1
		}
		if index+parts > start {
			return index
		}
		break
	}
	return start
}

func isContinuousTailToolEntryKind(kind string) bool {
	switch strings.TrimSpace(kind) {
	case "tool_call", "tool_result":
		return true
	default:
		return false
	}
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
	dropped := canceledReplayDroppedIndexes(entries)
	if len(dropped) == 0 {
		return entries
	}
	filtered := make([]HistoryEntry, 0, len(entries))
	for index, entry := range entries {
		if _, skip := dropped[index]; skip {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

func canceledReplayDroppedIndexes(entries []HistoryEntry) map[int]struct{} {
	dropped := make(map[int]struct{})
	if len(entries) == 0 {
		return dropped
	}
	canceledTurns := canceledReplayPolicies(entries)
	if len(canceledTurns) == 0 {
		return dropped
	}
	activeCanceledTurns := canceledReplayActivityTurns(entries)
	for index, entry := range entries {
		if entry.TurnSeq <= 0 {
			continue
		}
		policy, canceled := canceledTurns[entry.TurnSeq]
		if !canceled {
			continue
		}
		if policy == cancelReplayPolicyKeepInterrupted {
			continue
		}
		if policy != cancelReplayPolicyDropTurn {
			if _, active := activeCanceledTurns[entry.TurnSeq]; active {
				continue
			}
		}
		if policy == cancelReplayPolicyDropUnstarted {
			policy = cancelReplayPolicyDropTurn
		}
		if policy == cancelReplayPolicyDropTurn || !isStableCanceledTurnInputEntry(entry) {
			dropped[index] = struct{}{}
		}
	}
	return dropped
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
			ReasoningOrigin:                 message.ReasoningOrigin,
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

// projectNativeCheckpointProjection 实现 C0-W1/W2 的独立写出接缝。
// 默认发布入口仍使用 ProjectCheckpointProjection；未闭合数据域不得据此全局启用。
func (projector *HistoryProjector) projectNativeCheckpointProjection(conversation *ConversationFile, contentBlobs ...*ContentBlobStore) (*CheckpointProjection, error) {
	var store *ContentBlobStore
	if len(contentBlobs) > 0 {
		store = contentBlobs[0]
	}
	source, hasSource, err := lastNativeSummarySource(conversation)
	if err != nil {
		return nil, err
	}
	var summaryNodes []importedContentNode
	if hasSource {
		nodes, err := loadImportedSummaryGraph(source, store)
		if err != nil {
			return nil, err
		}
		summaryNodes = nodes
	}
	var importedNodes []importedContentNode
	if conversation != nil && len(conversation.ImportedTurnIDs) > 0 {
		nodes, err := loadImportedTurnGraph(conversation.ImportedTurnIDs, store)
		if err != nil {
			return nil, err
		}
		importedNodes = nodes
	}
	// 先在既有取消政策下做原生域校验，保持旧接缝的错误优先级：
	// 域外输入与未闭合工具批次必须先于共享投影失败，且过滤不得掩盖未完成调用。
	if _, err := nativeCheckpointMessages(conversation); err != nil {
		return nil, err
	}
	projection, err := projector.ProjectCheckpointProjection(conversation)
	if err != nil || projection == nil {
		return nil, err
	}
	// 仅复用回合结构和既有历史工具过滤，不使用面向模型的裁剪结果作保存正文。
	graph := newCheckpointBlobGraph()
	for _, blob := range projection.Blobs {
		graph.add(blob.Data)
	}
	root, err := encodeNativeCheckpointRootItems(conversation, graph)
	if err != nil {
		return nil, err
	}
	structured, err := projectConversationStructuredState(conversation)
	if err != nil {
		return nil, fmt.Errorf("native checkpoint: invalid structured state")
	}
	if structured.HasPlan && structured.PlanText != "" {
		data, err := proto.Marshal(&agentv1.ConversationPlan{Plan: structured.PlanText})
		if err != nil {
			return nil, fmt.Errorf("native checkpoint: encode plan")
		}
		projection.State.Plan = graph.add(data)
	}
	projection.State.Todos = nil
	for _, item := range structured.Todos {
		data, err := proto.Marshal(item)
		if err != nil {
			return nil, fmt.Errorf("native checkpoint: encode todo")
		}
		if len(data) == 0 {
			return nil, fmt.Errorf("native checkpoint: empty todo")
		}
		projection.State.Todos = append(projection.State.Todos, graph.add(data))
	}
	projection.State.RootPromptMessagesJson = root
	for _, node := range importedNodes {
		if err := graph.addOriginal(node.ID, node.Data); err != nil {
			return nil, err
		}
	}
	if hasSource {
		projection.State.Summary = append([]byte(nil), source.NativeSummary...)
		if len(projection.State.Summary) == 0 {
			projection.State.Summary = nil
		}
		projection.State.SummaryArchive = append([]byte(nil), source.NativeSummaryArchive...)
		if len(projection.State.SummaryArchive) == 0 {
			projection.State.SummaryArchive = nil
		}
		projection.State.SummaryArchives = cloneByteSlices(source.NativeSummaryArchives)
		for _, node := range summaryNodes {
			if err := graph.addOriginal(node.ID, node.Data); err != nil {
				return nil, err
			}
		}
	} else {
		projection.State.Summary = nil
		projection.State.SummaryArchive = nil
		projection.State.SummaryArchives = nil
	}
	if err := attachLocalCompactionNativeGraph(conversation, projection, graph); err != nil {
		return nil, err
	}
	projection.Blobs = graph.list()
	return projection, nil
}

func lastNativeSummarySource(conversation *ConversationFile) (runtimeStateEntryPayload, bool, error) {
	if conversation == nil {
		return runtimeStateEntryPayload{}, false, nil
	}
	var found runtimeStateEntryPayload
	ok := false
	for _, entry := range checkpointProjectionEntries(conversation.Entries) {
		if strings.TrimSpace(entry.Kind) != "runtime_state" {
			continue
		}
		var payload runtimeStateEntryPayload
		if err := json.Unmarshal(entry.Payload, &payload); err != nil {
			return runtimeStateEntryPayload{}, false, fmt.Errorf("native checkpoint: invalid structured state")
		}
		if !hasNativeSummarySource(payload) {
			continue
		}
		if err := validateNativeSummarySourceIDs(payload); err != nil {
			return runtimeStateEntryPayload{}, false, err
		}
		found = payload
		ok = true
	}
	return found, ok, nil
}

// nativeCheckpointMessages 在既有取消政策后从有效原始历史取保存正文。
// 域校验先于规范化/过滤；工具结果不经过 limitProjectedToolResultReplay 的裁剪和浮点重编码。
func nativeCheckpointMessages(conversation *ConversationFile) ([]modeladapter.Message, error) {
	if conversation == nil {
		return nil, nil
	}
	if conversationHasLocalCompaction(conversation) {
		if err := validateAllLocalCompactionSources(conversation); err != nil {
			return nil, err
		}
		return nativeCheckpointSegmentMessages(sanitizeCanceledReplayEntries(compactedNativeCheckpointEntries(conversation.Entries)), true, true)
	}
	return nativeCheckpointSegmentMessages(checkpointProjectionEntries(conversation.Entries), true, false)
}

// nativeCheckpointSegmentMessages 在既有取消政策后从一段有效原始历史取保存正文。
// requireComplete 为 true 时要求工具批次在段内闭合；分段写出时由整段配对校验兜底。
func nativeCheckpointSegmentMessages(entries []HistoryEntry, requireComplete bool, skipSummaries bool) ([]modeladapter.Message, error) {
	// 原生组先复核原文及全部视图；原文已明确 JSON 类型，不能再把视图中的
	// 字符串工具结果猜成 JSON 值。数值门禁直接校验原文，仍不放宽安全数值域。
	nativeMembers := make(map[int]struct{})
	for index := 0; index < len(entries); index++ {
		raw, parts, native := nativeCheckpointGroupStart(entries[index])
		if !native {
			continue
		}
		_, next, err := parseNativeCheckpointGroup(entries, index, raw, parts)
		if err != nil {
			return nil, err
		}
		if err := validateNativeCheckpointJSON(raw, false); err != nil {
			return nil, err
		}
		for member := index; member < next; member++ {
			nativeMembers[member] = struct{}{}
		}
		index = next - 1
	}
	messages := make([]projectedReplayMessage, 0, len(entries))
	pending := make(map[string]int)
	completed := make(map[string]struct{})
	for entryIndex, entry := range entries {
		var message modeladapter.Message
		switch strings.TrimSpace(entry.Kind) {
		case "runtime_state":
			// 保存侧必须检查原始载荷，避免共享投影的克隆过程剥掉未知字段后成功。
			var payload runtimeStateEntryPayload
			decoder := json.NewDecoder(bytes.NewReader(entry.Payload))
			decoder.DisallowUnknownFields()
			if !json.Valid(entry.Payload) || decoder.Decode(&payload) != nil {
				return nil, fmt.Errorf("native checkpoint: invalid structured state")
			}
			continue
		case "compaction_summary", "compacted_summary":
			if skipSummaries {
				continue
			}
			return nil, fmt.Errorf("native checkpoint: compaction not supported")
		case "model_message":
			var payload modelMessageEntryPayload
			decoder := json.NewDecoder(bytes.NewReader(entry.Payload))
			decoder.DisallowUnknownFields()
			if !json.Valid(entry.Payload) || decoder.Decode(&payload) != nil {
				return nil, fmt.Errorf("native checkpoint: invalid model message payload")
			}
			if len(payload.NativeMessage) == 0 && payload.NativeMessageParts != 0 {
				return nil, errNativeCheckpointNativeGroup
			}
			message = payload.Message
		case "user_message":
			user := &agentv1.UserMessage{}
			if protojson.Unmarshal(entry.Payload, user) != nil {
				return nil, fmt.Errorf("native checkpoint: invalid user message payload")
			}
			replay, ok := promptengine.BuildUserMessageReplayMessage(user)
			if !ok {
				continue
			}
			message = toModelMessage(replay)
		case "request_context":
			context := &agentv1.RequestContext{}
			if protojson.Unmarshal(entry.Payload, context) != nil {
				return nil, fmt.Errorf("native checkpoint: invalid request context")
			}
			for _, replay := range promptengine.BuildRequestContextReplayMessages(context) {
				message := toModelMessage(replay)
				if err := validateNativeCheckpointMessage(message); err != nil {
					return nil, err
				}
				messages = append(messages, newProjectedReplayMessage(message, entry))
			}
			continue
		case "prompt_context":
			var payload promptContextEntryPayload
			if json.Unmarshal(entry.Payload, &payload) != nil {
				return nil, fmt.Errorf("native checkpoint: invalid prompt context")
			}
			context := normalizePromptContextMessage(PromptContextMessage{Source: payload.Source, ContentHash: payload.ContentHash,
				Message: modeladapter.Message{Role: firstNonEmpty(payload.Role, "user"), Content: payload.Content}, Persist: true})
			if !isReplayablePromptContext(context) {
				continue
			}
			message = context.Message
		case "assistant_text":
			var payload assistantTextPayload
			decoder := json.NewDecoder(bytes.NewReader(entry.Payload))
			decoder.DisallowUnknownFields()
			if !json.Valid(entry.Payload) || decoder.Decode(&payload) != nil {
				return nil, fmt.Errorf("native checkpoint: invalid assistant payload")
			}
			message = modeladapter.Message{Role: "assistant", Content: strings.TrimSpace(payload.Text),
				ReasoningContent: payload.ReasoningContent, ReasoningSignature: payload.ReasoningSignature,
				ReasoningSignatureSource: payload.ReasoningSignatureSource, OpenAIResponsesReasoningID: payload.ReasoningItemID,
				OpenAIResponsesReasoningStatus: payload.ReasoningStatus, OpenAIResponsesReasoningSummary: payload.ReasoningSummary,
				ReasoningOrigin: payload.ReasoningOrigin}
		case "tool_call", "tool_result":
			// 两种历史载荷的共有字段一致；先检查只用于界面的工具，不能过滤后才判定域。
			var payload toolResultEntryPayload
			decoder := json.NewDecoder(bytes.NewReader(entry.Payload))
			decoder.DisallowUnknownFields()
			if !json.Valid(entry.Payload) || decoder.Decode(&payload) != nil {
				return nil, fmt.Errorf("native checkpoint: invalid tool payload")
			}
			metadata := modeladapter.Message{ReasoningContent: payload.ReasoningContent, ReasoningSignature: payload.ReasoningSignature,
				ReasoningSignatureSource: payload.ReasoningSignatureSource, OpenAIResponsesReasoningID: payload.ReasoningItemID,
				OpenAIResponsesReasoningStatus: payload.ReasoningStatus, OpenAIResponsesReasoningSummary: payload.ReasoningSummary,
				ReasoningOrigin: payload.ReasoningOrigin}
			if hasNativeCheckpointReasoningMetadata(metadata) || payload.ProviderItemID != "" || payload.ProviderCallID != "" || payload.ProviderStatus != "" {
				return nil, errNativeCheckpointReasoningMetadata
			}
			tool := &agentv1.ToolCall{}
			if len(payload.ToolCall) > 0 && protojson.Unmarshal(payload.ToolCall, tool) != nil {
				return nil, fmt.Errorf("native checkpoint: invalid tool call payload")
			}
			if entry.Kind == "tool_call" {
				replay, ok := promptengine.BuildAssistantToolCallReplayMessage(payload.ToolCallID, tool)
				if !ok {
					return nil, fmt.Errorf("native checkpoint: unsupported tool call payload")
				}
				message = toModelMessage(replay)
				// 原始派发条目决定调用名称；结果不能事后重命名已派发的调用。
				overrideModelToolReplayFromEntry(&message, payload.ToolName, "")
			} else {
				if index, exists := pending[payload.ToolCallID]; exists {
					for callIndex, call := range messages[index].Message.ToolCalls {
						if call.ID != payload.ToolCallID {
							continue
						}
						if payload.ToolName != "" && payload.ToolName != call.Function.Name {
							return nil, errNativeCheckpointToolNameMismatch
						}
						// pending 按调用 ID 关联，但同一 assistant 可以包含多个调用。
						// 仅对匹配项恢复参数，不把某个结果的名称/参数覆盖整个批次。
						matched := modeladapter.Message{Role: "assistant", ToolCalls: []modeladapter.ToolCallDescriptor{call}}
						overrideModelToolReplayFromEntry(&matched, payload.ToolName, payload.Arguments)
						if err := validateNativeCheckpointMessage(matched); err != nil {
							return nil, err
						}
						messages[index].Message.ToolCalls[callIndex] = matched.ToolCalls[0]
						break
					}
				} else {
					if _, exists := completed[payload.ToolCallID]; exists {
						return nil, errNativeCheckpointDuplicateToolResult
					}
					replay, ok := promptengine.BuildAssistantToolCallReplayMessage(payload.ToolCallID, tool)
					if !ok {
						return nil, errNativeCheckpointOrphanToolResult
					}
					call := toModelMessage(replay)
					overrideModelToolReplayFromEntry(&call, payload.ToolName, payload.Arguments)
					if err := validateNativeCheckpointMessage(call); err != nil {
						return nil, err
					}
					pending[payload.ToolCallID] = len(messages)
					messages = append(messages, newProjectedReplayMessage(call, entry))
				}
				replay, ok := promptengine.BuildToolResultReplayMessage(payload.ToolCallID, tool)
				if ok {
					message = toModelMessage(replay)
					message.Name = firstNonEmpty(payload.ToolName, message.Name)
				} else {
					message = modeladapter.Message{Role: "tool", ToolCallID: payload.ToolCallID, Name: payload.ToolName, Content: payload.ResultText}
				}
			}
		default:
			continue
		}
		if _, native := nativeMembers[entryIndex]; !native {
			if err := validateNativeCheckpointMessage(message); err != nil {
				return nil, err
			}
		}
		if len(pending) > 0 && message.Role != "tool" && len(message.ToolCalls) == 0 {
			return nil, errNativeCheckpointPendingToolResults
		}
		for _, call := range message.ToolCalls {
			if _, exists := pending[call.ID]; exists {
				return nil, errNativeCheckpointDuplicateToolCall
			}
			pending[call.ID] = len(messages)
			delete(completed, call.ID)
		}
		if message.Role == "tool" {
			index, exists := pending[message.ToolCallID]
			if !exists {
				return nil, errNativeCheckpointOrphanToolResult
			}
			matched := false
			for _, call := range messages[index].Message.ToolCalls {
				if call.ID == message.ToolCallID && call.Function.Name == message.Name {
					matched = true
				}
			}
			if !matched {
				return nil, errNativeCheckpointToolNameMismatch
			}
			delete(pending, message.ToolCallID)
			completed[message.ToolCallID] = struct{}{}
		}
		messages = append(messages, newProjectedReplayMessage(message, entry))
	}
	if requireComplete && len(pending) > 0 {
		return nil, errNativeCheckpointMissingToolResult
	}
	return projectedReplayMessagesToModel(normalizeProjectedReplayMessages(messages)), nil
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
				nextIndex++
				continue
			}
			if replayAssistantToolGroupID(next.Message) == groupID && canMergeProjectedReplayAssistantToolCalls(batch, next) {
				// ID 前缀不能证明同一次模型调用；已有结果后，只有实际调用身份
				// 相同才合并交错片段，保留导入历史中的独立批次和重复实例。
				if len(toolResults) > 0 && !sameProjectedReplayAggregationKey(batch, next) {
					break
				}
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
	keepOrigin := replayMessageHasOpaqueToolMetadata(*message)
	message.ReasoningContent = ""
	message.ReasoningSignature = ""
	message.ReasoningSignatureSource = ""
	if !keepOrigin {
		message.ReasoningOrigin = modeladapter.ReasoningOrigin{}
	}
	message.OpenAIResponsesReasoningID = ""
	message.OpenAIResponsesReasoningStatus = ""
	message.OpenAIResponsesReasoningSummary = nil
}

func replayMessageHasOpaqueToolMetadata(message modeladapter.Message) bool {
	for _, toolCall := range message.ToolCalls {
		if strings.TrimSpace(toolCall.OpenAIResponsesID) != "" ||
			strings.TrimSpace(toolCall.OpenAIResponsesCallID) != "" ||
			strings.TrimSpace(toolCall.OpenAIResponsesStatus) != "" {
			return true
		}
	}
	return false
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
	origin    modeladapter.ReasoningOrigin
}

func captureReplayReasoningTuple(message modeladapter.Message) replayReasoningTuple {
	tuple := replayReasoningTuple{
		content:   message.ReasoningContent,
		signature: message.ReasoningSignature,
		source:    message.ReasoningSignatureSource,
		itemID:    message.OpenAIResponsesReasoningID,
		status:    message.OpenAIResponsesReasoningStatus,
		origin:    message.ReasoningOrigin,
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
	dst.ReasoningOrigin = tuple.origin
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
	dst.origin = mergeReplayReasoningOrigin(dst.origin, src.origin)
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

func mergeReplayReasoningOrigin(left modeladapter.ReasoningOrigin, right modeladapter.ReasoningOrigin) modeladapter.ReasoningOrigin {
	if left.IsZero() {
		return right
	}
	if right.IsZero() || left.Equal(right) {
		return left
	}
	return modeladapter.ReasoningOrigin{}
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
		last.ReasoningOrigin = modeladapter.ReasoningOrigin{}
		last.OpenAIResponsesReasoningID = ""
		last.OpenAIResponsesReasoningStatus = ""
		last.OpenAIResponsesReasoningSummary = nil
		return
	}
	if leftSignature == "" && rightSignature != "" {
		last.ReasoningSignatureSource = strings.TrimSpace(current.ReasoningSignatureSource)
		last.ReasoningOrigin = current.ReasoningOrigin
		last.OpenAIResponsesReasoningID = current.OpenAIResponsesReasoningID
		last.OpenAIResponsesReasoningStatus = current.OpenAIResponsesReasoningStatus
		last.OpenAIResponsesReasoningSummary = append(json.RawMessage(nil), current.OpenAIResponsesReasoningSummary...)
		return
	}
	if leftSignature == rightSignature {
		last.ReasoningSignatureSource = mergeReplayReasoningSignatureSource(last.ReasoningSignatureSource, current.ReasoningSignatureSource)
		last.ReasoningOrigin = mergeReplayReasoningOrigin(last.ReasoningOrigin, current.ReasoningOrigin)
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
	currentName = runtimecore.CanonicalToolName(strings.TrimSpace(currentName))
	overrideName = runtimecore.CanonicalToolName(strings.TrimSpace(overrideName))
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
		ReasoningOrigin:                 message.ReasoningOrigin,
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

// native checkpoint 写出（C0-W1）：把本地模型消息编码为原生 {role,content} root。
//
// 只覆盖 W1 可表达域：user/assistant 文本、assistant 完整工具调用与 tool 结果。
// 域外输入（system、reasoning/provider 元数据、ContentParts、未知角色、不完整批次、
// 安全域外数字、结果类型歧义、超限正文、raw32 与内联 JSON 歧义）一律返回固定类别错误，
// 不退回内联 JSON、不裁块、不产生半图。函数不修改入参 messages。

const (
	// nativeCheckpointBlockTypeText 是原生文本块类型。
	nativeCheckpointBlockTypeText = "text"
	// nativeCheckpointBlockTypeToolCall 是原生工具调用块类型。
	nativeCheckpointBlockTypeToolCall = "tool-call"
	// nativeCheckpointBlockTypeToolResult 是原生工具结果块类型。
	nativeCheckpointBlockTypeToolResult = "tool-result"
	// nativeCheckpointMaxMessageBytes 是客户端已核实的单条 root UTF-8 JSON 上限，不自造预算。
	nativeCheckpointMaxMessageBytes = 5_000_000
)

// nativeCheckpointMaxSafeInteger 是 2^53-1；原生 JS 消费者超过该值会舍入。
var nativeCheckpointMaxSafeInteger = big.NewInt(9007199254740991)

var (
	errNativeCheckpointNilGraph            = errors.New("native checkpoint: nil blob graph")
	errNativeCheckpointUnknownRole         = errors.New("native checkpoint: unknown message role")
	errNativeCheckpointSystemRole          = errors.New("native checkpoint: system role not supported")
	errNativeCheckpointReasoningMetadata   = errors.New("native checkpoint: reasoning metadata not supported")
	errNativeCheckpointContentParts        = errors.New("native checkpoint: content parts not supported")
	errNativeCheckpointProviderMetadata    = errors.New("native checkpoint: provider item metadata not supported")
	errNativeCheckpointToolCallIdentity    = errors.New("native checkpoint: tool call missing id or name")
	errNativeCheckpointToolCallType        = errors.New("native checkpoint: unsupported tool call type")
	errNativeCheckpointToolResultIdentity  = errors.New("native checkpoint: tool result missing id or name")
	errNativeCheckpointUnexpectedToolCalls = errors.New("native checkpoint: unexpected tool calls for role")
	errNativeCheckpointJSONInvalid         = errors.New("native checkpoint: invalid JSON value")
	errNativeCheckpointNumber              = errors.New("native checkpoint: number outside safe integer domain")
	errNativeCheckpointResultType          = errors.New("native checkpoint: tool result string is itself valid JSON (W-RESULT-TYPE)")
	errNativeCheckpointOrphanToolResult    = errors.New("native checkpoint: orphan tool result")
	errNativeCheckpointDuplicateToolResult = errors.New("native checkpoint: duplicate tool result")
	errNativeCheckpointMissingToolResult   = errors.New("native checkpoint: missing tool result")
	errNativeCheckpointDuplicateToolCall   = errors.New("native checkpoint: duplicate tool call id in batch")
	errNativeCheckpointToolNameMismatch    = errors.New("native checkpoint: tool result name mismatch")
	errNativeCheckpointPendingToolResults  = errors.New("native checkpoint: message inserted before tool results completed")
	errNativeCheckpointMessageTooLarge     = errors.New("native checkpoint: message exceeds client JSON size limit")
	errNativeCheckpointIDAmbiguity         = errors.New("native checkpoint: content id collides with inline JSON (W-ID-AMBIGUITY)")
	errNativeCheckpointNativePayload       = errors.New("native checkpoint: invalid native message payload")
	errNativeCheckpointNativeGroup         = errors.New("native checkpoint: invalid native message group")
	errNativeCheckpointNativeParts         = errors.New("native checkpoint: native message parts truncated or mismatched")
	errNativeCheckpointNativeViewMismatch  = errors.New("native checkpoint: native message view mismatch")
)

// nativeCheckpointRootMessage 是原生 CoreMessage 的最小顶层结构，仅含 role/content。
type nativeCheckpointRootMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// nativeCheckpointTextBlock 是原生文本块。
type nativeCheckpointTextBlock struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// nativeCheckpointToolCallBlock 是原生工具调用块，args 为原始 JSON。
type nativeCheckpointToolCallBlock struct {
	Type       string          `json:"type"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Args       json.RawMessage `json:"args"`
}

// nativeCheckpointToolResultBlock 是原生工具结果块，result 为原始 JSON。
type nativeCheckpointToolResultBlock struct {
	Type       string          `json:"type"`
	ToolCallID string          `json:"toolCallId"`
	ToolName   string          `json:"toolName"`
	Result     json.RawMessage `json:"result"`
}

// encodeNativeCheckpointRoot 把消息序列编码为原生 root 引用序列。
// 返回值是每条消息正文的 raw32 SHA-256，顺序与入参一致；相同正文共享一个 Blob，
// 但重复实例保留在返回序列中。只有整条序列全部校验/序列化成功后才写入 graph。
func encodeNativeCheckpointRoot(messages []modeladapter.Message, graph *checkpointBlobGraph) ([][]byte, error) {
	if graph == nil {
		return nil, errNativeCheckpointNilGraph
	}
	tracker := newNativeCheckpointPairingTracker()
	encoded := make([][]byte, 0, len(messages))
	for _, message := range messages {
		if err := tracker.observe(message); err != nil {
			return nil, err
		}
		data, err := encodeNativeCheckpointGeneratedMessage(message)
		if err != nil {
			return nil, err
		}
		if len(data) > nativeCheckpointMaxMessageBytes {
			return nil, errNativeCheckpointMessageTooLarge
		}
		digest := sha256.Sum256(data)
		if json.Valid(digest[:]) {
			return nil, errNativeCheckpointIDAmbiguity
		}
		encoded = append(encoded, data)
	}
	if err := tracker.finish(); err != nil {
		return nil, err
	}
	ids := make([][]byte, 0, len(encoded))
	for _, data := range encoded {
		ids = append(ids, graph.add(data))
	}
	return ids, nil
}

// nativeCheckpointPairingTracker 复用既有工具配对语义，供整段校验与编码共享。
type nativeCheckpointPairingTracker struct {
	pending   map[string]string
	completed map[string]struct{}
}

func newNativeCheckpointPairingTracker() *nativeCheckpointPairingTracker {
	return &nativeCheckpointPairingTracker{pending: make(map[string]string), completed: make(map[string]struct{})}
}

// observe 校验单条消息并推进配对状态；错误类别与旧编码顺序保持一致。
func (tracker *nativeCheckpointPairingTracker) observe(message modeladapter.Message) error {
	if err := validateNativeCheckpointMessage(message); err != nil {
		return err
	}
	return tracker.observePairing(message)
}

// observePairing 只推进配对状态；原生原文已由解析器和原文数值门禁校验，
// 不经过仅针对本地重编码的工具结果类型猜测。
func (tracker *nativeCheckpointPairingTracker) observePairing(message modeladapter.Message) error {
	switch strings.TrimSpace(message.Role) {
	case "user":
		if len(tracker.pending) > 0 {
			return errNativeCheckpointPendingToolResults
		}
		tracker.completed = make(map[string]struct{})
	case "assistant":
		if len(tracker.pending) > 0 {
			return errNativeCheckpointPendingToolResults
		}
		if len(message.ToolCalls) == 0 {
			tracker.completed = make(map[string]struct{})
			return nil
		}
		tracker.pending = make(map[string]string)
		tracker.completed = make(map[string]struct{})
		for _, toolCall := range message.ToolCalls {
			toolCallID := toolCall.ID
			if _, exists := tracker.pending[toolCallID]; exists {
				return errNativeCheckpointDuplicateToolCall
			}
			tracker.pending[toolCallID] = toolCall.Function.Name
		}
	case "tool":
		toolCallID := message.ToolCallID
		toolName, exists := tracker.pending[toolCallID]
		if !exists {
			if _, done := tracker.completed[toolCallID]; done {
				return errNativeCheckpointDuplicateToolResult
			}
			return errNativeCheckpointOrphanToolResult
		}
		if toolName != message.Name {
			return errNativeCheckpointToolNameMismatch
		}
		delete(tracker.pending, toolCallID)
		tracker.completed[toolCallID] = struct{}{}
	}
	return nil
}

func (tracker *nativeCheckpointPairingTracker) finish() error {
	if len(tracker.pending) > 0 {
		return errNativeCheckpointMissingToolResult
	}
	return nil
}

// encodeNativeCheckpointGeneratedMessage 编码单条生成本地消息，不修改入参、不做整段配对。
func encodeNativeCheckpointGeneratedMessage(message modeladapter.Message) ([]byte, error) {
	switch strings.TrimSpace(message.Role) {
	case "user":
		return nativeCheckpointPlainMessageJSON("user", message.Content)
	case "assistant":
		if len(message.ToolCalls) == 0 {
			return nativeCheckpointPlainMessageJSON("assistant", message.Content)
		}
		return nativeCheckpointAssistantToolCallMessageJSON(message)
	case "tool":
		return nativeCheckpointToolMessageJSON(message)
	default:
		return nil, errNativeCheckpointUnknownRole
	}
}

// nativeCheckpointRootItem 是原生 root 的一个输出项：原生原文直接输出，否则编码生成消息。
type nativeCheckpointRootItem struct {
	native  []byte
	message modeladapter.Message
}

// encodeNativeCheckpointRootItems 按 entry 顺序切段构造原生 root。
// 原生组按原文输出一次（按实际字节哈希、保留重复实例），generated 段复用本地过滤/编码，
// 整段再用同一配对语义校验，确保跨段不产生孤立工具结果。
func encodeNativeCheckpointRootItems(conversation *ConversationFile, graph *checkpointBlobGraph) ([][]byte, error) {
	if graph == nil {
		return nil, errNativeCheckpointNilGraph
	}
	if conversation == nil {
		return nil, nil
	}
	localCompaction := conversationHasLocalCompaction(conversation)
	var effective []HistoryEntry
	if localCompaction {
		if err := validateAllLocalCompactionSources(conversation); err != nil {
			return nil, err
		}
		effective = sanitizeCanceledReplayEntries(compactedNativeCheckpointEntries(conversation.Entries))
	} else {
		effective = checkpointProjectionEntries(conversation.Entries)
	}
	return encodeNativeCheckpointEntryRoot(effective, graph, localCompaction)
}

func encodeNativeCheckpointEntryRoot(effective []HistoryEntry, graph *checkpointBlobGraph, unclipped bool) ([][]byte, error) {
	if graph == nil {
		return nil, errNativeCheckpointNilGraph
	}
	items := make([]nativeCheckpointRootItem, 0, len(effective))
	tracker := newNativeCheckpointPairingTracker()
	for index := 0; index < len(effective); {
		if unclipped && isCompactionSummaryKind(effective[index].Kind) {
			text, ok := decodeCompactionSummaryEntry(effective[index])
			if !ok {
				return nil, fmt.Errorf("native checkpoint: compaction not supported")
			}
			raw, err := encodeNativeIsSummaryMessage(text)
			if err != nil {
				return nil, err
			}
			if err := validateNativeCheckpointJSON(raw, false); err != nil {
				return nil, err
			}
			parsed, err := parseImportedNativeMessage(raw)
			if err != nil {
				return nil, err
			}
			for _, view := range parsed {
				if err := tracker.observePairing(toModelMessage(view)); err != nil {
					return nil, err
				}
			}
			items = append(items, nativeCheckpointRootItem{native: raw})
			index++
			continue
		}
		raw, parts, isNative := nativeCheckpointGroupStart(effective[index])
		if isNative {
			views, next, err := parseNativeCheckpointGroup(effective, index, raw, parts)
			if err != nil {
				return nil, err
			}
			if err := validateNativeCheckpointJSON(raw, false); err != nil {
				return nil, err
			}
			for _, view := range views {
				if err := tracker.observePairing(view); err != nil {
					return nil, err
				}
			}
			items = append(items, nativeCheckpointRootItem{native: raw})
			index = next
			continue
		}
		end := index + 1
		for end < len(effective) {
			if unclipped && isCompactionSummaryKind(effective[end].Kind) {
				break
			}
			if _, _, next := nativeCheckpointGroupStart(effective[end]); next {
				break
			}
			end++
		}
		segMessages, err := nativeCheckpointSegmentMessages(effective[index:end], false, unclipped)
		if err != nil {
			return nil, err
		}
		encodedMessages := segMessages
		if !unclipped {
			encodedMessages = checkpointFilteredModelMessages(segMessages)
		}
		for _, message := range encodedMessages {
			if err := tracker.observe(message); err != nil {
				return nil, err
			}
			items = append(items, nativeCheckpointRootItem{message: message})
		}
		index = end
	}
	if err := tracker.finish(); err != nil {
		return nil, err
	}
	ids := make([][]byte, 0, len(items))
	for _, item := range items {
		data := item.native
		if len(data) == 0 {
			encoded, err := encodeNativeCheckpointGeneratedMessage(item.message)
			if err != nil {
				return nil, err
			}
			data = encoded
		}
		if len(data) > nativeCheckpointMaxMessageBytes {
			return nil, errNativeCheckpointMessageTooLarge
		}
		digest := sha256.Sum256(data)
		if json.Valid(digest[:]) {
			return nil, errNativeCheckpointIDAmbiguity
		}
		ids = append(ids, graph.add(data))
	}
	return ids, nil
}

// checkpointFilteredModelMessages 复用经典 checkpoint 的工具过滤与消息转换。
func checkpointFilteredModelMessages(messages []modeladapter.Message) []modeladapter.Message {
	if len(messages) == 0 {
		return nil
	}
	replay := make([]promptengine.Message, 0, len(messages))
	for _, message := range messages {
		replay = append(replay, promptengine.Message{Role: message.Role, Content: message.Content,
			ToolCalls: toPromptToolCalls(message.ToolCalls), ToolCallID: message.ToolCallID, Name: message.Name})
	}
	replay = filterCheckpointPersistentToolReplay(replay)
	output := make([]modeladapter.Message, 0, len(replay))
	for _, message := range replay {
		output = append(output, toModelMessage(message))
	}
	return output
}

// nativeCheckpointGroupStart 判断 entry 是否开启一个原生原文组，返回原文与 parts。
func nativeCheckpointGroupStart(entry HistoryEntry) ([]byte, int, bool) {
	if strings.TrimSpace(entry.Kind) != "model_message" {
		return nil, 0, false
	}
	var payload modelMessageEntryPayload
	decoder := json.NewDecoder(bytes.NewReader(entry.Payload))
	decoder.DisallowUnknownFields()
	if !json.Valid(entry.Payload) || decoder.Decode(&payload) != nil || len(payload.NativeMessage) == 0 {
		return nil, 0, false
	}
	parts := payload.NativeMessageParts
	if parts == 0 {
		parts = 1
	}
	return payload.NativeMessage, parts, true
}

// parseNativeCheckpointGroup 逐条解析原文并与成员条目保存的 Message 视图比对；
// parts 截断/非 model/Turn 或 Request 失配/成员带 raw 或 parts/内容不同/非法 raw 固定失败。
func parseNativeCheckpointGroup(entries []HistoryEntry, start int, raw []byte, parts int) ([]modeladapter.Message, int, error) {
	if parts == 0 {
		parts = 1
	}
	if parts < 1 || parts > len(entries)-start {
		return nil, 0, errNativeCheckpointNativeParts
	}
	first := entries[start]
	var firstPayload modelMessageEntryPayload
	if !decodeStrictModelMessagePayload(first.Payload, &firstPayload) || !bytes.Equal(firstPayload.NativeMessage, raw) {
		return nil, 0, errNativeCheckpointNativePayload
	}
	parsed, err := parseImportedNativeMessage(raw)
	if err != nil {
		return nil, 0, errNativeCheckpointNativePayload
	}
	filtered := filterInternalPromptContextReplay(parsed)
	views := make([]modeladapter.Message, 0, len(filtered))
	for _, message := range filtered {
		views = append(views, toModelMessage(message))
	}
	if len(views) != parts {
		return nil, 0, errNativeCheckpointNativeParts
	}
	for offset := 0; offset < parts; offset++ {
		entry := entries[start+offset]
		if strings.TrimSpace(entry.Kind) != "model_message" || entry.TurnSeq != first.TurnSeq ||
			strings.TrimSpace(entry.RequestID) != strings.TrimSpace(first.RequestID) {
			return nil, 0, errNativeCheckpointNativeGroup
		}
		var member modelMessageEntryPayload
		if !decodeStrictModelMessagePayload(entry.Payload, &member) {
			return nil, 0, errNativeCheckpointNativeGroup
		}
		if offset > 0 && (len(member.NativeMessage) > 0 || member.NativeMessageParts != 0) {
			return nil, 0, errNativeCheckpointNativeGroup
		}
		if offset == 0 && !bytes.Equal(member.NativeMessage, raw) {
			return nil, 0, errNativeCheckpointNativeGroup
		}
		if !modelMessageViewsEqual(member.Message, views[offset]) {
			return nil, 0, errNativeCheckpointNativeViewMismatch
		}
	}
	return views, start + parts, nil
}

// decodeStrictModelMessagePayload 只接受恰好已知字段的 model_message 载荷。
func decodeStrictModelMessagePayload(payload json.RawMessage, target *modelMessageEntryPayload) bool {
	if !json.Valid(payload) {
		return false
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target) == nil
}

// modelMessageViewsEqual 以确定性 JSON 比较两条消息视图，避免两阶段规范化静默丢字段后继续。
func modelMessageViewsEqual(left modeladapter.Message, right modeladapter.Message) bool {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return false
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return false
	}
	return bytes.Equal(leftJSON, rightJSON)
}

// validateNativeCheckpointMessage 校验单条消息的全部字段，不做批次配对检查。
// 供原始载荷门禁与 encodeNativeCheckpointRoot 共同调用。
func validateNativeCheckpointMessage(message modeladapter.Message) error {
	role := message.Role
	switch role {
	case "user", "assistant", "tool":
	case "system":
		return errNativeCheckpointSystemRole
	default:
		return errNativeCheckpointUnknownRole
	}
	if hasNativeCheckpointReasoningMetadata(message) {
		return errNativeCheckpointReasoningMetadata
	}
	if len(message.ContentParts) > 0 {
		return errNativeCheckpointContentParts
	}
	if (role != "tool" && (message.Name != "" || message.ToolCallID != "")) || (role != "assistant" && len(message.ToolCalls) > 0) {
		return errNativeCheckpointUnexpectedToolCalls
	}
	switch role {
	case "assistant":
		return validateNativeCheckpointAssistantMessage(message)
	case "tool":
		return validateNativeCheckpointToolMessage(message)
	default:
		return nil
	}
}

// validateNativeCheckpointAssistantMessage 校验 assistant 的工具调用身份、类型、provider 元数据与 args。
func validateNativeCheckpointAssistantMessage(message modeladapter.Message) error {
	for _, toolCall := range message.ToolCalls {
		if strings.TrimSpace(toolCall.ID) == "" || strings.TrimSpace(toolCall.Function.Name) == "" {
			return errNativeCheckpointToolCallIdentity
		}
		switch strings.TrimSpace(toolCall.Type) {
		case "", "function":
		default:
			return errNativeCheckpointToolCallType
		}
		if toolCall.OpenAIResponsesID != "" || toolCall.OpenAIResponsesCallID != "" || toolCall.OpenAIResponsesStatus != "" {
			return errNativeCheckpointProviderMetadata
		}
		if err := validateNativeCheckpointJSON([]byte(toolCall.Function.Arguments), false); err != nil {
			return err
		}
	}
	return nil
}

// validateNativeCheckpointToolMessage 校验 tool 结果身份与结果正文（含 W-RESULT-TYPE）。
func validateNativeCheckpointToolMessage(message modeladapter.Message) error {
	if strings.TrimSpace(message.ToolCallID) == "" || strings.TrimSpace(message.Name) == "" {
		return errNativeCheckpointToolResultIdentity
	}
	_, err := nativeCheckpointToolResultJSON(message.Content)
	return err
}

// hasNativeCheckpointReasoningMetadata 报告消息是否携带任何 reasoning/provider 推理元数据。
func hasNativeCheckpointReasoningMetadata(message modeladapter.Message) bool {
	return message.ReasoningContent != "" ||
		message.ReasoningSignature != "" ||
		message.ReasoningSignatureSource != "" ||
		message.OpenAIResponsesReasoningID != "" ||
		message.OpenAIResponsesReasoningStatus != "" ||
		len(message.OpenAIResponsesReasoningSummary) > 0 ||
		message.ReasoningOrigin != (modeladapter.ReasoningOrigin{})
}

// nativeCheckpointPlainMessageJSON 编码无工具的消息，content 为 JSON 字符串。
func nativeCheckpointPlainMessageJSON(role string, content string) ([]byte, error) {
	encodedContent, err := json.Marshal(content)
	if err != nil {
		return nil, errNativeCheckpointJSONInvalid
	}
	encoded, err := json.Marshal(nativeCheckpointRootMessage{Role: role, Content: encodedContent})
	if err != nil {
		return nil, errNativeCheckpointJSONInvalid
	}
	return encoded, nil
}

// nativeCheckpointAssistantToolCallMessageJSON 编码带工具调用的 assistant，content 为块数组。
func nativeCheckpointAssistantToolCallMessageJSON(message modeladapter.Message) ([]byte, error) {
	blocks := make([]any, 0, len(message.ToolCalls)+1)
	if message.Content != "" {
		blocks = append(blocks, nativeCheckpointTextBlock{Type: nativeCheckpointBlockTypeText, Text: message.Content})
	}
	for _, toolCall := range message.ToolCalls {
		blocks = append(blocks, nativeCheckpointToolCallBlock{
			Type:       nativeCheckpointBlockTypeToolCall,
			ToolCallID: toolCall.ID,
			ToolName:   toolCall.Function.Name,
			Args:       json.RawMessage(toolCall.Function.Arguments),
		})
	}
	encodedContent, err := json.Marshal(blocks)
	if err != nil {
		return nil, errNativeCheckpointJSONInvalid
	}
	encoded, err := json.Marshal(nativeCheckpointRootMessage{Role: "assistant", Content: encodedContent})
	if err != nil {
		return nil, errNativeCheckpointJSONInvalid
	}
	return encoded, nil
}

// nativeCheckpointToolMessageJSON 编码 tool 消息，content 为单条 tool-result 块数组。
func nativeCheckpointToolMessageJSON(message modeladapter.Message) ([]byte, error) {
	result, err := nativeCheckpointToolResultJSON(message.Content)
	if err != nil {
		return nil, err
	}
	blocks := []any{nativeCheckpointToolResultBlock{
		Type:       nativeCheckpointBlockTypeToolResult,
		ToolCallID: message.ToolCallID,
		ToolName:   message.Name,
		Result:     result,
	}}
	encodedContent, err := json.Marshal(blocks)
	if err != nil {
		return nil, errNativeCheckpointJSONInvalid
	}
	encoded, err := json.Marshal(nativeCheckpointRootMessage{Role: "tool", Content: encodedContent})
	if err != nil {
		return nil, errNativeCheckpointJSONInvalid
	}
	return encoded, nil
}

// nativeCheckpointToolResultJSON 编码工具结果正文：有效 JSON 直接嵌入原始值，
// 否则编码为 JSON 字符串；空正文保留为空字符串。字符串结果本身又是合法 JSON 时报 W-RESULT-TYPE。
func nativeCheckpointToolResultJSON(content string) (json.RawMessage, error) {
	if content == "" {
		return json.RawMessage(`""`), nil
	}
	raw := []byte(content)
	if json.Valid(raw) {
		if err := validateNativeCheckpointJSON(raw, true); err != nil {
			return nil, err
		}
		return json.RawMessage(raw), nil
	}
	encoded, err := json.Marshal(content)
	if err != nil {
		return nil, errNativeCheckpointJSONInvalid
	}
	return json.RawMessage(encoded), nil
}

// validateNativeCheckpointJSON 校验一段 JSON 是否合法、恰好一个值，且数字落在安全整数域。
// result 为 true 时额外校验结果类型歧义（W-RESULT-TYPE）。
func validateNativeCheckpointJSON(raw []byte, result bool) error {
	if len(raw) == 0 || !json.Valid(raw) {
		return errNativeCheckpointJSONInvalid
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return errNativeCheckpointJSONInvalid
	}
	if result {
		if text, ok := value.(string); ok && json.Valid([]byte(text)) {
			return errNativeCheckpointResultType
		}
	}
	return validateNativeCheckpointJSONValue(value)
}

// validateNativeCheckpointJSONValue 递归校验解码后的 JSON 值，数字必须是安全域内数学整数。
func validateNativeCheckpointJSONValue(value any) error {
	switch typed := value.(type) {
	case nil, bool, string:
		return nil
	case json.Number:
		return validateNativeCheckpointNumber(typed)
	case []any:
		for _, item := range typed {
			if err := validateNativeCheckpointJSONValue(item); err != nil {
				return err
			}
		}
		return nil
	case map[string]any:
		for _, item := range typed {
			if err := validateNativeCheckpointJSONValue(item); err != nil {
				return err
			}
		}
		return nil
	default:
		return errNativeCheckpointJSONInvalid
	}
}

// validateNativeCheckpointNumber 用 math/big.Rat 精确判定数学整数与安全上界，
// 不经过 float64，因此 1.000000000000000001 之类不会被舍入接受。
func validateNativeCheckpointNumber(number json.Number) error {
	rational, ok := new(big.Rat).SetString(number.String())
	if !ok || !rational.IsInt() {
		return errNativeCheckpointNumber
	}
	if new(big.Int).Abs(rational.Num()).Cmp(nativeCheckpointMaxSafeInteger) > 0 {
		return errNativeCheckpointNumber
	}
	return nil
}

func conversationHasLocalCompaction(conversation *ConversationFile) bool {
	if conversation == nil {
		return false
	}
	for _, entry := range conversation.Entries {
		if isCompactionSummaryKind(entry.Kind) {
			return true
		}
	}
	return false
}

func validateAllLocalCompactionSources(conversation *ConversationFile) error {
	if conversation == nil {
		return nil
	}
	found := false
	for _, entry := range conversation.Entries {
		if !isCompactionSummaryKind(entry.Kind) {
			continue
		}
		found = true
		if _, _, _, err := localCompactionArchiveFromEntry(conversation.Entries, entry); err != nil {
			return err
		}
	}
	if !found {
		return fmt.Errorf("native checkpoint: compaction not supported")
	}
	return nil
}

func nativeCoreMessagesFromCanonicalPrefix(entries []HistoryEntry, start, end int) ([][]byte, error) {
	if start < 0 || end > len(entries) || start > end {
		return nil, fmt.Errorf("native checkpoint: invalid native archive source")
	}
	dropped := canceledReplayDroppedIndexes(entries)
	selected := make([]HistoryEntry, 0, end-start)
	for index := start; index < end; index++ {
		if _, skip := dropped[index]; skip {
			continue
		}
		if !isOrdinaryPromptReplayEntryKind(entries[index].Kind) {
			continue
		}
		selected = append(selected, entries[index])
	}
	if len(selected) == 0 {
		return nil, nil
	}
	graph := newCheckpointBlobGraph()
	ids, err := encodeNativeCheckpointEntryRoot(selected, graph, true)
	if err != nil {
		return nil, err
	}
	repo := make(map[string][]byte, len(graph.blobs))
	for _, blob := range graph.list() {
		repo[string(blob.ID)] = blob.Data
	}
	messages := make([][]byte, 0, len(ids))
	for _, id := range ids {
		data, ok := repo[string(id)]
		if !ok {
			return nil, fmt.Errorf("native checkpoint: invalid native archive source")
		}
		messages = append(messages, append([]byte(nil), data...))
	}
	return messages, nil
}

func encodeNativeIsSummaryMessage(text string) ([]byte, error) {
	content, err := json.Marshal(text)
	if err != nil {
		return nil, errNativeCheckpointJSONInvalid
	}
	encoded, err := json.Marshal(struct {
		Role            string          `json:"role"`
		Content         json.RawMessage `json:"content"`
		ProviderOptions json.RawMessage `json:"providerOptions"`
	}{
		Role:            "user",
		Content:         content,
		ProviderOptions: json.RawMessage(`{"cursor":{"isSummary":true}}`),
	})
	if err != nil {
		return nil, errNativeCheckpointJSONInvalid
	}
	return encoded, nil
}

func attachLocalCompactionNativeGraph(conversation *ConversationFile, projection *CheckpointProjection, graph *checkpointBlobGraph) error {
	if conversation == nil || projection == nil || projection.State == nil || graph == nil {
		return nil
	}
	if !conversationHasLocalCompaction(conversation) {
		return nil
	}
	archives := make([][]byte, 0)
	var latestSummary []byte
	hadPrevious := false
	for _, entry := range conversation.Entries {
		if !isCompactionSummaryKind(entry.Kind) {
			continue
		}
		archiveID, summaryID, err := addLocalCompactionArchive(conversation.Entries, entry, graph, hadPrevious)
		if err != nil {
			return err
		}
		archives = append(archives, archiveID)
		latestSummary = summaryID
		hadPrevious = true
	}
	if len(archives) == 0 || len(latestSummary) == 0 {
		return fmt.Errorf("native checkpoint: compaction not supported")
	}
	projection.State.Summary = latestSummary
	projection.State.SummaryArchives = append(cloneByteSlices(projection.State.GetSummaryArchives()), archives...)
	return nil
}

func addLocalCompactionArchive(entries []HistoryEntry, entry HistoryEntry, graph *checkpointBlobGraph, hadPrevious bool) ([]byte, []byte, error) {
	payload, source, summarized, err := localCompactionArchiveFromEntry(entries, entry)
	if err != nil {
		return nil, nil, err
	}
	oldSummaryCount := uint32(0)
	if hadPrevious {
		oldSummaryCount = 1
	}
	if source.CandidateCount != source.ActualCount || source.WindowTail != 0 ||
		source.ActualCount != oldSummaryCount+uint32(len(summarized)) {
		return nil, nil, fmt.Errorf("native checkpoint: invalid native archive source")
	}
	summaryText := strings.TrimSpace(payload.Summary)
	summaryMessage, err := encodeNativeIsSummaryMessage(summaryText)
	if err != nil {
		return nil, nil, err
	}
	if err := validateNativeCheckpointJSON(summaryMessage, false); err != nil {
		return nil, nil, err
	}
	messageIDs := make([][]byte, 0, len(summarized))
	for _, raw := range summarized {
		if len(raw) == 0 {
			messageIDs = append(messageIDs, nil)
			continue
		}
		if len(raw) > nativeCheckpointMaxMessageBytes {
			return nil, nil, errNativeCheckpointMessageTooLarge
		}
		digest := sha256.Sum256(raw)
		if json.Valid(digest[:]) {
			return nil, nil, errNativeCheckpointIDAmbiguity
		}
		messageIDs = append(messageIDs, graph.add(raw))
	}
	summaryMessageID := graph.add(summaryMessage)
	archiveBody, err := proto.Marshal(&agentv1.ConversationSummaryArchive{
		SummarizedMessages: messageIDs,
		Summary:            summaryText,
		WindowTail:         source.WindowTail,
		SummaryMessage:     summaryMessageID,
	})
	if err != nil {
		return nil, nil, fmt.Errorf("native checkpoint: encode summary archive")
	}
	summaryBody, err := proto.Marshal(&agentv1.ConversationSummary{Summary: summaryText})
	if err != nil {
		return nil, nil, fmt.Errorf("native checkpoint: encode summary")
	}
	return graph.add(archiveBody), graph.add(summaryBody), nil
}

func localCompactionArchiveFromEntry(entries []HistoryEntry, entry HistoryEntry) (compactionSummaryEntryPayload, *compactionNativeArchiveSource, [][]byte, error) {
	payload, err := decodeCompactionSummaryPayload(entry.Payload)
	if err != nil {
		return compactionSummaryEntryPayload{}, nil, nil, fmt.Errorf("native checkpoint: compaction not supported")
	}
	source, err := parseNativeArchiveSource(payload.NativeArchiveSource)
	if err != nil {
		return compactionSummaryEntryPayload{}, nil, nil, err
	}
	if source == nil {
		return compactionSummaryEntryPayload{}, nil, nil, fmt.Errorf("native checkpoint: compaction not supported")
	}
	if source.PrefixStart < 0 || source.PrefixEnd > len(entries) || source.PrefixStart > source.PrefixEnd {
		return compactionSummaryEntryPayload{}, nil, nil, fmt.Errorf("native checkpoint: invalid native archive source")
	}
	summarized, err := nativeCoreMessagesFromCanonicalPrefix(entries, source.PrefixStart, source.PrefixEnd)
	if err != nil {
		return compactionSummaryEntryPayload{}, nil, nil, err
	}
	return payload, source, summarized, nil
}
