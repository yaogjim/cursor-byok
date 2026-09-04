const SUPPORTED_MODEL_ADAPTER_TYPES = new Set(["openai", "anthropic"]);
const OPENAI_ENDPOINT_CHAT_COMPLETIONS = "/v1/chat/completions";
const OPENAI_ENDPOINT_RESPONSES = "/v1/responses";
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

function asBoolean(value) {
  if (typeof value === "boolean") {
    return value;
  }
  if (typeof value === "number") {
    return value !== 0;
  }
  const normalized = asString(value).toLowerCase();
  if (!normalized) {
    return false;
  }
  return normalized === "true" || normalized === "1" || normalized === "yes";
}

function normalizeCredentialSource(value) {
  const source = asString(value).toLowerCase();
  if (source === "codex" || source === "grok") {
    return source;
  }
  return "static";
}

// isOpenAIImageGenerationCompatible 仅 OpenAI + 静态密钥 + /v1/responses 可开启原生媒体生成。
export function isOpenAIImageGenerationCompatible(adapter) {
  const draft = adapter && typeof adapter === "object" ? adapter : {};
  return asString(draft.type).toLowerCase() === "openai"
    && normalizeCredentialSource(draft.credentialSource) === "static"
    && asString(draft.openAIEndpoint).toLowerCase() === OPENAI_ENDPOINT_RESPONSES;
}

export function normalizeOpenAIImageGenerationEnabled(source) {
  const raw = source && typeof source === "object" ? source : {};
  if (!isOpenAIImageGenerationCompatible(raw)) {
    return false;
  }
  return asBoolean(
    raw.openAIImageGenerationEnabled
      ?? raw.openaiImageGenerationEnabled
      ?? raw.open_ai_image_generation_enabled,
  );
}

export function applyOpenAIImageGenerationConstraint(adapter) {
  const draft = adapter && typeof adapter === "object" ? adapter : {};
  draft.openAIImageGenerationEnabled = normalizeOpenAIImageGenerationEnabled(draft);
  return draft;
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
  applyOpenAIImageGenerationConstraint(draft);
  return draft;
}
