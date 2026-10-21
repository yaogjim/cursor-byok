const PROVIDERS = Object.freeze([
  { value: "openai", label: "OpenAI", color: "#10b981" },
  { value: "anthropic", label: "Anthropic", color: "#d97757" },
  { value: "google", label: "Google", color: "#4285f4" },
  { value: "deepseek", label: "DeepSeek", color: "#22c7df" },
  { value: "xai", label: "xAI", color: "#1f2937" },
  { value: "qwen", label: "通义千问", color: "#7c3aed" },
  { value: "moonshot", label: "Moonshot", color: "#14b8a6" },
  { value: "tencent", label: "腾讯接口", color: "#2563eb" },
]);

const PROVIDER_BY_VALUE = new Map(PROVIDERS.map((provider) => [provider.value, provider]));

function asString(value) {
  return String(value || "").trim();
}

export function modelProviderKey(adapter = {}) {
  const haystack = [
    adapter.displayName,
    adapter.modelID,
    adapter.baseURL,
    adapter.openAIEndpoint,
  ].map((part) => asString(part).toLowerCase()).join(" ");

  if (/\b(claude|anthropic)\b/.test(haystack) || adapter.type === "anthropic") return "anthropic";
  if (/\b(gemini|google)\b|generativelanguage/.test(haystack)) return "google";
  if (/deepseek/.test(haystack)) return "deepseek";
  if (/\b(grok|xai)\b|x\.ai/.test(haystack)) return "xai";
  if (/\b(qwen|tongyi)\b|dashscope|aliyuncs/.test(haystack)) return "qwen";
  if (/\b(kimi|moonshot)\b/.test(haystack)) return "moonshot";
  if (/\b(hunyuan|tencent)\b|腾讯/.test(haystack)) return "tencent";
  return "openai";
}

export function modelProviderMeta(adapter = {}) {
  const value = modelProviderKey(adapter);
  return PROVIDER_BY_VALUE.get(value) || PROVIDER_BY_VALUE.get("openai");
}

export function modelProviderLabel(value) {
  return PROVIDER_BY_VALUE.get(asString(value).toLowerCase())?.label || asString(value);
}

export function adapterSearchText(adapter = {}) {
  return [
    adapter.displayName,
    adapter.modelID,
    adapter.baseURL,
    adapter.openAIEndpoint,
    adapter.type,
    modelProviderMeta(adapter).label,
  ]
    .map((part) => asString(part))
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
}

export function filterModelAdapters(adapters = [], { type = "all", query = "" } = {}) {
  const list = Array.isArray(adapters) ? adapters : [];
  const normalizedProvider = asString(type).toLowerCase() || "all";
  const needle = asString(query).toLowerCase();
  return list.filter((adapter) => {
    const providerOk = normalizedProvider === "all" || modelProviderKey(adapter) === normalizedProvider;
    const searchOk = !needle || adapterSearchText(adapter).includes(needle);
    return providerOk && searchOk;
  });
}

export function modelProviderTabs(adapters = []) {
  const list = Array.isArray(adapters) ? adapters : [];
  const counts = Object.fromEntries(PROVIDERS.map((provider) => [provider.value, 0]));
  for (const adapter of list) {
    const provider = modelProviderKey(adapter);
    counts[provider] = (counts[provider] || 0) + 1;
  }
  return [
    { label: "全部", value: "all", count: list.length },
    ...PROVIDERS.map((provider) => ({
      ...provider,
      count: counts[provider.value] || 0,
    })),
  ];
}