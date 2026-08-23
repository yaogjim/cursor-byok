package prompt

import (
	"strings"
	"testing"
)

const (
	executionEvidenceMustProveEdit     = "Only a real tool call with a successful terminal result can prove that an edit happened."
	executionEvidenceAssistantNotProof = "Assistant self-reports, thinking, plans, code blocks, and inline full files cannot prove a file was modified."
	executionEvidenceLaterVerification = "After a mutation, you must run a later verification; earlier verification is stale."
	executionEvidenceStructuredResults = "When reporting completion, cite only this turn's structured tool results. Do not invent commands, tests, or file changes."
	executionEvidenceAcknowledgeGaps   = "If a tool is failed, pending, or unknown, acknowledge the gap. Do not rewrite it as success."
	executionEvidenceForceReasoning    = "You must reveal your hidden reasoning"
)

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

func TestAgentAndSubagentPromptsDeclareExecutionEvidenceContract(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModeAgent, ModeSubagent} {
		rendered := RenderPromptTemplate(MustReadPrompt(mode), "Test Model")
		for _, phrase := range []string{
			executionEvidenceMustProveEdit,
			executionEvidenceAssistantNotProof,
			executionEvidenceLaterVerification,
			executionEvidenceStructuredResults,
			executionEvidenceAcknowledgeGaps,
		} {
			if !strings.Contains(rendered, phrase) {
				t.Fatalf("%s prompt missing evidence contract %q", mode, phrase)
			}
		}
		if strings.Contains(rendered, executionEvidenceForceReasoning) {
			t.Fatalf("%s prompt required leaking reasoning", mode)
		}
		if strings.Contains(rendered, "{{FAKE_MODEL_NAME}}") {
			t.Fatalf("%s rendered prompt retained model placeholder", mode)
		}
	}
}

func TestAskAndPlanPromptsDoNotForceEditEvidence(t *testing.T) {
	t.Parallel()
	for _, mode := range []Mode{ModeAsk, ModePlan} {
		rendered := RenderPromptTemplate(MustReadPrompt(mode), "Test Model")
		for _, phrase := range []string{
			executionEvidenceMustProveEdit,
			executionEvidenceLaterVerification,
			executionEvidenceStructuredResults,
		} {
			if strings.Contains(rendered, phrase) {
				t.Fatalf("%s prompt forced edit evidence contract %q", mode, phrase)
			}
		}
		if strings.Contains(rendered, executionEvidenceForceReasoning) {
			t.Fatalf("%s prompt required leaking reasoning", mode)
		}
	}
}
