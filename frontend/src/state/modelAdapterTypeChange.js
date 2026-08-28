const SUPPORTED_MODEL_ADAPTER_TYPES = new Set(["openai", "anthropic"]);
const OPENAI_ENDPOINT_CHAT_COMPLETIONS = "/v1/chat/completions";
const ANTHROPIC_THINKING_EFFORT_DEFAULT = "xhigh";

function asString(value) {
  if (typeof value === "string") {
    return value.trim();
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return "";
}

// applyModelAdapterTypeChange 切换 OpenAI/Anthropic 类别时保留当前模型标识。
// 模型目录可能不同，但用户已填写的标识不应被清空；保存前仍走既有非空校验。
export function applyModelAdapterTypeChange(adapter, type) {
  const draft = adapter && typeof adapter === "object" ? adapter : {};
  const nextType = asString(type).toLowerCase();
  if (!SUPPORTED_MODEL_ADAPTER_TYPES.has(nextType)) {
    return draft;
  }
  draft.type = nextType;
  if (nextType === "openai" && !asString(draft.openAIEndpoint)) {
    draft.openAIEndpoint = OPENAI_ENDPOINT_CHAT_COMPLETIONS;
  }
  if (nextType === "anthropic" && !asString(draft.anthropicThinkingEffort)) {
    draft.anthropicThinkingEffort = ANTHROPIC_THINKING_EFFORT_DEFAULT;
  }
  return draft;
}