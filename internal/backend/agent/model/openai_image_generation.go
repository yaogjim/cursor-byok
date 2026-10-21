// openai_image_generation.go 负责 OpenAI Responses 原生图片生成工具的注入门禁。
package modeladapter

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode/utf8"
)

func shouldExposeOpenAIResponsesImageGeneration(req StreamRequest, tools []map[string]any) bool {
	if !req.OpenAIImageGenerationEnabled {
		return false
	}
	if !openAIResponsesToolNamePresent(tools, "GenerateImage") {
		return false
	}
	return openAITextLooksLikeImageGenerationRequest(openAILatestUserRequestText(req))
}

func ensureOpenAIResponsesImageGenerationTool(tools []map[string]any) []map[string]any {
	for _, tool := range tools {
		if strings.TrimSpace(fmt.Sprint(tool["type"])) == "image_generation" {
			return tools
		}
	}
	return append(tools, map[string]any{"type": "image_generation"})
}

func openAIAnyToolItems(value any) ([]any, bool) {
	switch items := value.(type) {
	case []any:
		return items, true
	case []map[string]any:
		converted := make([]any, len(items))
		for i := range items {
			converted[i] = items[i]
		}
		return converted, true
	case []json.RawMessage:
		converted := make([]any, len(items))
		for i := range items {
			var item any
			if err := json.Unmarshal(items[i], &item); err != nil {
				return nil, false
			}
			converted[i] = item
		}
		return converted, true
	default:
		return nil, false
	}
}

func enforceOpenAIChatCompletionsImageGenerationToolPolicy(body map[string]any) {
	items, ok := openAIAnyToolItems(body["tools"])
	if !ok {
		return
	}
	filtered := make([]any, 0, len(items))
	for _, item := range items {
		tool, ok := item.(map[string]any)
		if ok && strings.TrimSpace(fmt.Sprint(tool["type"])) == "image_generation" {
			continue
		}
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		delete(body, "tools")
		return
	}
	body["tools"] = filtered
}

func enforceOpenAIResponsesImageGenerationToolPolicy(body map[string]any, req StreamRequest) {
	items, ok := openAIAnyToolItems(body["tools"])
	if !ok {
		return
	}
	canonicalTools, err := normalizeOpenAIResponsesTools(req.Tools)
	allowNative := err == nil && shouldExposeOpenAIResponsesImageGeneration(req, canonicalTools)
	filtered := make([]any, 0, len(items))
	nativeSeen := false
	for _, item := range items {
		tool, ok := item.(map[string]any)
		if !ok || strings.TrimSpace(fmt.Sprint(tool["type"])) != "image_generation" {
			filtered = append(filtered, item)
			continue
		}
		if !allowNative || nativeSeen {
			continue
		}
		nativeSeen = true
		filtered = append(filtered, item)
	}
	if len(filtered) == 0 {
		delete(body, "tools")
		return
	}
	body["tools"] = filtered
}

func openAIResponsesToolNamePresent(tools []map[string]any, name string) bool {
	for _, tool := range tools {
		if strings.TrimSpace(fmt.Sprint(tool["name"])) == name {
			return true
		}
		if functionShape, ok := tool["function"].(map[string]any); ok {
			if strings.TrimSpace(fmt.Sprint(functionShape["name"])) == name {
				return true
			}
		}
	}
	return false
}

func openAILatestUserRequestText(req StreamRequest) string {
	for i := len(req.Messages) - 1; i >= 0; i-- {
		message := req.Messages[i]
		if strings.TrimSpace(strings.ToLower(message.Role)) != "user" {
			continue
		}
		text := message.Content
		if strings.TrimSpace(text) == "" && len(message.ContentParts) > 0 {
			text = collapseTextContentParts(message.ContentParts)
		}
		if tagged := textBetweenOpenAITag(text, "current_user_request"); tagged != "" {
			return tagged
		}
		if tagged := textBetweenOpenAITag(text, "user_query"); tagged != "" {
			return tagged
		}
		if strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func textBetweenOpenAITag(text string, tag string) string {
	openTag := "<" + tag + ">"
	closeTag := "</" + tag + ">"
	start := strings.LastIndex(text, openTag)
	if start < 0 {
		return ""
	}
	start += len(openTag)
	end := strings.Index(text[start:], closeTag)
	if end < 0 {
		return ""
	}
	return strings.TrimSpace(text[start : start+end])
}

func openAITextLooksLikeImageGenerationRequest(text string) bool {
	normalized := strings.ToLower(strings.TrimSpace(text))
	normalized = strings.ReplaceAll(normalized, "generateimage", " ")
	normalized = strings.ReplaceAll(normalized, "image_generation", " ")
	normalized = strings.Join(strings.Fields(normalized), " ")
	if normalized == "" {
		return false
	}
	return openAIImageGenerationHasActionOnVisual(normalized)
}

var openAIImageGenActions = []string{
	"generating", "generate",
	"creating", "create",
	"drawing", "draw",
	"painting", "paint",
	"生成", "绘制", "创建", "画一张", "画一幅", "画一个", "画个", "画画",
}

var openAIImageGenArtifacts = []string{
	"photorealistic", "illustration", "photograph", "portrait",
	"pictures", "picture", "images", "image",
	"photos", "photo", "posters", "poster", "wallpaper",
	"真实摄影", "图片", "图像", "照片", "相片", "人像", "头像", "插画", "海报", "壁纸", "封面", "摄影",
}

func openAIImageGenerationHasActionOnVisual(text string) bool {
	const window = 40
	for _, action := range openAIImageGenActions {
		for _, actionIdx := range openAIPhraseIndexes(text, action) {
			if openAIActionLocallyNegated(text, actionIdx) || openAIActionIsOnlyDiscussed(text, actionIdx) {
				continue
			}
			for _, artifact := range openAIImageGenArtifacts {
				for _, artifactIdx := range openAIPhraseIndexes(text, artifact) {
					if openAITextDistance(text, actionIdx, artifactIdx) <= window && !openAIVisualIsTestArtifact(text, actionIdx, artifactIdx) && !openAIVisualIsCodeArtifact(text, artifactIdx, artifact) {
						return true
					}
				}
			}
		}
	}
	return false
}

func openAITextDistance(text string, left, right int) int {
	if left > right {
		left, right = right, left
	}
	return utf8.RuneCountInString(text[left:right])
}

func openAIActionIsOnlyDiscussed(text string, actionIdx int) bool {
	start := actionIdx - 24
	if start < 0 {
		start = 0
	}
	before := strings.TrimSpace(text[start:actionIdx])
	for _, phrase := range []string{"how to", "how do i", "explain", "implement", "write code", "api for", "protocol for", "分析如何", "解释如何", "讨论如何", "测试是否", "检查是否"} {
		if strings.HasSuffix(before, phrase) {
			return true
		}
	}
	return false
}

func openAIVisualIsCodeArtifact(text string, artifactIdx int, artifact string) bool {
	start := artifactIdx + len(artifact)
	if start >= len(text) {
		return false
	}
	end := start + 24
	if end > len(text) {
		end = len(text)
	}
	after := strings.TrimLeft(text[start:end], " \t\r\n-_:,.;()[]{}")
	words := strings.Fields(after)
	if len(words) == 0 {
		return false
	}
	first := strings.Trim(words[0], "-_:,.;()[]{}")
	for _, marker := range []string{"loader", "handler", "handling", "parser", "payload", "response", "endpoint", "api", "format", "schema", "object", "field", "data", "bytes", "base64", "mime", "test", "fixture"} {
		if first == marker {
			return true
		}
	}
	return false
}

func openAIActionLocallyNegated(text string, actionIdx int) bool {
	if actionIdx <= 0 {
		return false
	}
	start := actionIdx - 32
	if start < 0 {
		start = 0
	}
	before := strings.TrimSpace(text[start:actionIdx])
	if before == "" {
		return false
	}
	for _, phrase := range []string{"不要", "不用", "无需", "禁止", "别再", "别去"} {
		if strings.HasSuffix(before, phrase) {
			return true
		}
	}
	if strings.HasSuffix(before, "别") && !strings.HasSuffix(before, "特别") {
		return true
	}
	if openAIEnglishSuffix(before, []string{"not to"}) || openAIEnglishNegationWithFiller(before) {
		return true
	}
	before = openAITrimTrailingEnglish(before, []string{"to", "ever", "please", "actually"})
	if openAIEnglishSuffix(before, []string{"forget", "fail", "hesitate", "miss"}) {
		return false
	}
	return openAIEnglishSuffix(before, []string{"don't", "do not", "dont", "never"})
}

func openAIEnglishNegationWithFiller(text string) bool {
	words := strings.Fields(text)
	if len(words) < 2 {
		return false
	}
	for start := len(words) - 1; start >= 0 && len(words)-start <= 4; start-- {
		phrase := strings.Join(words[start:], " ")
		for _, prefix := range []string{"do not ", "don't ", "dont ", "never ", "not "} {
			if !strings.HasPrefix(phrase, prefix) {
				continue
			}
			filler := strings.TrimSpace(strings.TrimPrefix(phrase, prefix))
			if openAIEnglishWordsOnly(filler, []string{"want", "wish", "intend", "mean", "plan", "to", "ever", "please", "actually"}) {
				return true
			}
		}
	}
	return false
}

func openAIEnglishWordsOnly(text string, allowed []string) bool {
	words := strings.Fields(text)
	if len(words) == 0 {
		return false
	}
	for _, word := range words {
		found := false
		for _, candidate := range allowed {
			if word == candidate {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func openAIVisualIsTestArtifact(text string, actionIdx, artifactIdx int) bool {
	if artifactIdx <= actionIdx {
		return false
	}
	between := text[actionIdx:artifactIdx]
	for _, marker := range []string{"test", "tests", "testing", "testcase", "testcases", "testdata", "fixture", "fixtures"} {
		if len(openAIPhraseIndexes(between, marker)) > 0 {
			return true
		}
	}
	return false
}

func openAITrimTrailingEnglish(text string, words []string) string {
	for {
		next := strings.TrimSpace(text)
		trimmed := false
		for _, word := range words {
			if openAIEnglishSuffix(next, []string{word}) {
				next = strings.TrimSpace(next[:len(next)-len(word)])
				trimmed = true
				break
			}
		}
		if !trimmed {
			return next
		}
		text = next
	}
}

func openAIEnglishSuffix(text string, words []string) bool {
	for _, word := range words {
		if !strings.HasSuffix(text, word) {
			continue
		}
		prefix := text[:len(text)-len(word)]
		if prefix == "" {
			return true
		}
		c := prefix[len(prefix)-1]
		if c < 'a' || c > 'z' {
			return true
		}
	}
	return false
}

func openAIPhraseIndexes(text, phrase string) []int {
	ascii := phrase != "" && phrase[0] < 128
	indexes := make([]int, 0)
	start := 0
	for start < len(text) {
		idx := strings.Index(text[start:], phrase)
		if idx < 0 {
			break
		}
		idx += start
		if !ascii || openAIASCIIWordBoundary(text, idx, len(phrase)) {
			indexes = append(indexes, idx)
		}
		start = idx + len(phrase)
	}
	return indexes
}

func openAIASCIIWordBoundary(text string, idx, width int) bool {
	if idx > 0 {
		c := text[idx-1]
		if c >= 'a' && c <= 'z' {
			return false
		}
	}
	end := idx + width
	if end < len(text) {
		c := text[end]
		if c >= 'a' && c <= 'z' {
			return false
		}
	}
	return true
}
