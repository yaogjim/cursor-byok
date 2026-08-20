package prompt

import "testing"

func TestRenderPromptTemplateReplacesModelPlaceholders(t *testing.T) {
	t.Parallel()

	got := RenderPromptTemplate(
		"id={{FAKE_MODEL_ID}} name={{FAKE_MODEL_NAME}}",
		"Test Model",
	)
	if want := "id=Test Model name=Test Model"; got != want {
		t.Fatalf("RenderPromptTemplate() = %q, want %q", got, want)
	}
}

func TestRenderPromptTemplateUsesFallbackModelName(t *testing.T) {
	t.Parallel()

	got := RenderPromptTemplate("{{FAKE_MODEL_NAME}}", "  ")
	if want := "当前请求模型"; got != want {
		t.Fatalf("RenderPromptTemplate() = %q, want %q", got, want)
	}
}
