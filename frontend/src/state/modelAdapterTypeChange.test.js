import assert from "node:assert/strict";
import test from "node:test";

import { applyModelAdapterTypeChange } from "./modelAdapterTypeChange.js";

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