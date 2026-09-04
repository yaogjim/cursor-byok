import assert from "node:assert/strict";
import test from "node:test";

import {
  applyModelAdapterTypeChange,
  applyOpenAIImageGenerationConstraint,
  isOpenAIImageGenerationCompatible,
  normalizeOpenAIImageGenerationEnabled,
} from "./modelAdapterTypeChange.js";

function modelIdentifierError(adapters) {
  for (const [index, adapter] of adapters.entries()) {
    if (!String(adapter?.modelID || "").trim()) {
      return `模型 ${index + 1} 的模型标识不能为空`;
    }
  }
  return "";
}

test("switching model type keeps the current modelID", () => {
  const draft = {
    type: "openai",
    modelID: "cc",
    openAIEndpoint: "/v1/chat/completions",
    anthropicThinkingEffort: "",
  };

  applyModelAdapterTypeChange(draft, "anthropic");

  assert.equal(draft.type, "anthropic");
  assert.equal(draft.modelID, "cc");
  assert.equal(draft.anthropicThinkingEffort, "xhigh");
  assert.equal(modelIdentifierError([draft]), "");
});

test("clearing modelID on type switch reproduces the user save error", () => {
  const draft = { type: "openai", modelID: "cc" };
  draft.modelID = "";
  assert.equal(modelIdentifierError([draft]), "模型 1 的模型标识不能为空");
});

test("switching to openai fills a missing endpoint without clearing modelID", () => {
  const draft = {
    type: "anthropic",
    modelID: "gpt-5.5",
    openAIEndpoint: "",
    anthropicThinkingEffort: "high",
  };

  applyModelAdapterTypeChange(draft, "openai");

  assert.equal(draft.type, "openai");
  assert.equal(draft.modelID, "gpt-5.5");
  assert.equal(draft.openAIEndpoint, "/v1/chat/completions");
});

test("unknown type is ignored", () => {
  const draft = { type: "openai", modelID: "cc" };
  applyModelAdapterTypeChange(draft, "unknown");
  assert.equal(draft.type, "openai");
  assert.equal(draft.modelID, "cc");
});

test("native media generation defaults off", () => {
  assert.equal(normalizeOpenAIImageGenerationEnabled({}), false);
  assert.equal(normalizeOpenAIImageGenerationEnabled({
    type: "openai",
    credentialSource: "static",
    openAIEndpoint: "/v1/chat/completions",
  }), false);
});

test("native media generation enabled round trip for compatible adapters", () => {
  const source = {
    type: "openai",
    credentialSource: "static",
    openAIEndpoint: "/v1/responses",
    openAIImageGenerationEnabled: true,
  };
  assert.equal(isOpenAIImageGenerationCompatible(source), true);
  assert.equal(normalizeOpenAIImageGenerationEnabled(source), true);
  assert.equal(
    applyOpenAIImageGenerationConstraint({ ...source }).openAIImageGenerationEnabled,
    true,
  );
});

test("native media generation clears invalid combinations", () => {
  const invalid = [
    {
      type: "anthropic",
      credentialSource: "static",
      openAIEndpoint: "/v1/responses",
      openAIImageGenerationEnabled: true,
    },
    {
      type: "openai",
      credentialSource: "static",
      openAIEndpoint: "/v1/chat/completions",
      openAIImageGenerationEnabled: true,
    },
    {
      type: "openai",
      credentialSource: "static",
      openAIEndpoint: "/custom",
      openAIImageGenerationEnabled: true,
    },
    {
      type: "openai",
      credentialSource: "codex",
      openAIEndpoint: "/v1/responses",
      openAIImageGenerationEnabled: true,
    },
    {
      type: "openai",
      credentialSource: "grok",
      openAIEndpoint: "/v1/responses",
      openAIImageGenerationEnabled: true,
    },
  ];
  for (const source of invalid) {
    assert.equal(isOpenAIImageGenerationCompatible(source), false);
    assert.equal(normalizeOpenAIImageGenerationEnabled(source), false);
    assert.equal(
      applyOpenAIImageGenerationConstraint({ ...source }).openAIImageGenerationEnabled,
      false,
    );
  }
});

test("switching type or endpoint clears native media generation", () => {
  const draft = {
    type: "openai",
    modelID: "gpt-5.5",
    credentialSource: "static",
    openAIEndpoint: "/v1/responses",
    openAIImageGenerationEnabled: true,
  };

  applyModelAdapterTypeChange(draft, "anthropic");
  assert.equal(draft.openAIImageGenerationEnabled, false);

  const endpointDraft = {
    type: "openai",
    credentialSource: "static",
    openAIEndpoint: "/v1/chat/completions",
    openAIImageGenerationEnabled: true,
  };
  applyOpenAIImageGenerationConstraint(endpointDraft);
  assert.equal(endpointDraft.openAIImageGenerationEnabled, false);

  const credentialDraft = {
    type: "openai",
    credentialSource: "codex",
    openAIEndpoint: "/v1/responses",
    openAIImageGenerationEnabled: true,
  };
  applyOpenAIImageGenerationConstraint(credentialDraft);
  assert.equal(credentialDraft.openAIImageGenerationEnabled, false);
});
