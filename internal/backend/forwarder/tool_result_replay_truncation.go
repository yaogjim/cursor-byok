package forwarder

import (
	"encoding/json"
	"fmt"
	"strings"

	runtimecore "cursor/internal/backend/agent/core"
	"cursor/internal/backend/agent/toolresult"
)

const (
	projectedReadReplayLimit       = toolresult.ReadContentBytes
	projectedShellReplayLimit      = toolresult.ShellReplayPayloadBytes
	projectedShellStreamLimit      = toolresult.ShellStreamBytes
	projectedShellInterleavedLimit = toolresult.ShellInterleavedBytes
	projectedGrepReplayLimit       = toolresult.GrepContentBytes
	projectedEditReplayLimit       = toolresult.EditReplayBytes
	projectedPatchEditReplayLimit  = toolresult.PatchEditReplayBytes
	projectedWebFetchReplayLimit   = toolresult.WebFetchMarkdownBytes
	projectedWebSearchReplayLimit  = toolresult.WebSearchPayloadBytes
	projectedMcpReplayLimit        = toolresult.MCPResourcesBytes
)

func limitProjectedToolResultReplay(toolName string, content string, resultText string, fromStoredToolCall bool, historical bool) string {
	toolName = runtimecore.CanonicalToolName(strings.TrimSpace(toolName))
	if compacted, ok := compactProjectedGenerateImageResultReplay(toolName, content, resultText); ok {
		return compacted
	}
	if historical {
		if compacted, ok := compactHistoricalEditErrorReplay(toolName, content); ok {
			content = compacted
			fromStoredToolCall = false
		}
	}
	if compacted, ok := compactProjectedEditToolResultReplay(toolName, content); ok {
		content = compacted
		fromStoredToolCall = false
	}
	if compacted, ok := compactProjectedShellToolResultReplay(toolName, content); ok {
		content = compacted
		fromStoredToolCall = false
	}
	limit, ok := projectedToolReplayLimit(toolName)
	if !ok {
		return strings.TrimSpace(content)
	}
	content = strings.TrimSpace(content)
	if len(content) <= limit {
		return content
	}
	if fromStoredToolCall {
		fallback := strings.TrimSpace(resultText)
		notice := fmt.Sprintf("[tool result replay truncated: stored ToolCall result exceeded %d bytes]", limit)
		if fallback == "" {
			fallback = notice
		} else {
			fallback += "\n\n" + notice
		}
		return truncateProjectedReplayText(toolName, fallback, limit)
	}
	return truncateProjectedReplayText(toolName, content, limit)
}

func projectedToolReplayLimit(toolName string) (int, bool) {
	return toolresult.PayloadBytes(toolName, toolresult.PurposeReplay)
}

func compactProjectedGenerateImageResultReplay(toolName string, content string, resultText string) (string, bool) {
	if strings.TrimSpace(toolName) != "GenerateImage" {
		return "", false
	}
	fallback := strings.TrimSpace(resultText)
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		if fallback == "" {
			return "", false
		}
		return fallback, true
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		if fallback == "" {
			limit, _ := toolresult.PayloadBytes("GenerateImage", toolresult.PurposeReplay)
			return truncateProjectedReplayText("GenerateImage", trimmed, limit), true
		}
		return fallback, true
	}
	if compactGenerateImagePayload(payload) {
		encoded, err := json.Marshal(payload)
		if err == nil {
			return string(encoded), true
		}
	}
	if fallback != "" {
		return fallback, true
	}
	return trimmed, true
}

func compactGenerateImagePayload(value any) bool {
	changed := false
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if text, ok := child.(string); ok {
				switch key {
				case "image_data", "imageData":
					if strings.Contains(text, "base64 image data omitted from replay") {
						continue
					}
					item[key] = fmt.Sprintf("[base64 image data omitted from replay; bytes=%d]", len(strings.TrimSpace(text)))
					changed = true
					continue
				}
			}
			if compactGenerateImagePayload(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range item {
			if compactGenerateImagePayload(child) {
				changed = true
			}
		}
	}
	return changed
}

func patchEditReplayLimit(toolName string) int {
	limit, ok := toolresult.PayloadBytes(toolName, toolresult.PurposeReplay)
	if ok {
		return limit
	}
	return projectedEditReplayLimit
}

func compactProjectedShellToolResultReplay(toolName string, content string) (string, bool) {
	if runtimecore.CanonicalToolName(strings.TrimSpace(toolName)) != "Shell" {
		return "", false
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return "", false
	}
	var payload any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}
	if !compactProjectedShellFields(payload) {
		return "", false
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func compactProjectedShellFields(value any) bool {
	changed := false
	switch item := value.(type) {
	case map[string]any:
		for key, child := range item {
			if text, ok := child.(string); ok {
				switch key {
				case "stdout", "stderr":
					next := truncateProjectedReplayTextMiddle("Shell "+key, text, projectedShellStreamLimit)
					if next != text {
						item[key] = next
						changed = true
					}
					continue
				case "interleaved_output", "interleavedOutput":
					next := truncateProjectedReplayTextMiddle("Shell interleaved output", text, projectedShellInterleavedLimit)
					if next != text {
						item[key] = next
						changed = true
					}
					continue
				}
			}
			if compactProjectedShellFields(child) {
				changed = true
			}
		}
	case []any:
		for _, child := range item {
			if compactProjectedShellFields(child) {
				changed = true
			}
		}
	}
	return changed
}

func compactProjectedEditToolResultReplay(toolName string, content string) (string, bool) {
	switch strings.TrimSpace(toolName) {
	case "PatchEdit", "PatchEditLines", "PatchEditSpan", "Edit", "Write":
	default:
		return "", false
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" || trimmed == "{}" || trimmed == "null" {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}
	success, ok := payload["success"].(map[string]any)
	if !ok {
		return "", false
	}
	diffString := firstJSONText(success, "diff_string", "diffString")
	beforeContent := ""
	afterContent := ""
	if diffString == "" {
		beforeContent = firstJSONText(success, "before_full_file_content", "beforeFullFileContent")
		afterContent = firstJSONText(success, "after_full_file_content", "afterFullFileContent")
		if beforeContent != "" {
			diffString, _, _ = computeEditDiff(beforeContent, afterContent)
		}
	}
	if diffString != "" {
		diffString = truncateProjectedReplayText(firstNonEmpty(strings.TrimSpace(toolName), "PatchEdit"), diffString, patchEditReplayLimit(toolName))
		encoded, err := json.Marshal(map[string]any{
			"success": map[string]any{
				"diff_string": diffString,
			},
		})
		if err != nil {
			return "", false
		}
		return string(encoded), true
	}
	if afterContent != "" {
		afterContent = truncateProjectedReplayText(firstNonEmpty(strings.TrimSpace(toolName), "Write"), afterContent, patchEditReplayLimit(toolName))
		encoded, err := json.Marshal(map[string]any{
			"success": map[string]any{
				"after_full_file_content": afterContent,
			},
		})
		if err != nil {
			return "", false
		}
		return string(encoded), true
	}
	return "", false
}

func compactHistoricalEditErrorReplay(toolName string, content string) (string, bool) {
	switch strings.TrimSpace(toolName) {
	case "PatchEdit", "PatchEditLines", "PatchEditSpan", "Edit", "Write":
	default:
		return "", false
	}
	trimmed := strings.TrimSpace(content)
	if trimmed == "" {
		return "", false
	}
	var payload map[string]any
	if err := json.Unmarshal([]byte(trimmed), &payload); err != nil {
		return "", false
	}
	errorPayload, ok := payload["error"].(map[string]any)
	if !ok {
		return "", false
	}
	if firstJSONText(errorPayload, "error", "modelVisibleError") == "" {
		return "", false
	}
	encoded, err := json.Marshal(map[string]any{
		"error": map[string]any{
			"error": "historical edit error omitted from replay; re-read the file before editing",
		},
	})
	if err != nil {
		return "", false
	}
	return string(encoded), true
}

func firstJSONText(payload map[string]any, keys ...string) string {
	for _, key := range keys {
		if value, ok := payload[key].(string); ok {
			return value
		}
	}
	return ""
}

func truncateProjectedReplayText(toolName string, text string, limit int) string {
	if limit <= 0 || len(text) <= limit {
		return strings.TrimSpace(text)
	}
	return toolresult.TruncateTail(toolName, text, limit)
}

func truncateProjectedReplayTextMiddle(toolName string, text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	return toolresult.TruncateMiddle(toolName, text, limit)
}
