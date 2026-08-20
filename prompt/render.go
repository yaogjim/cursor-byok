package prompt

import "strings"

const (
	fakeModelIDPlaceholder   = "{{FAKE_MODEL_ID}}"
	fakeModelNamePlaceholder = "{{FAKE_MODEL_NAME}}"
)

// RenderPromptTemplate 将 prompt 资产中的占位符替换为当前请求的真实模型名称。
func RenderPromptTemplate(text string, modelName string) string {
	replacement := strings.TrimSpace(modelName)
	if replacement == "" {
		replacement = "当前请求模型"
	}
	text = strings.ReplaceAll(text, fakeModelIDPlaceholder, replacement)
	return strings.ReplaceAll(text, fakeModelNamePlaceholder, replacement)
}
