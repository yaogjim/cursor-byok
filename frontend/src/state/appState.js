import { computed, reactive, watchSyncEffect } from "vue";
import { Events } from "@wailsio/runtime";
import dayjs from "dayjs";
import {
  buildClientPreferencesFromState,
  buildObservabilityConfigFromState,
  buildSubagentRescheduleConfigFromState,
  DEFAULT_PROVIDER_FALLBACK,
  LOGICAL_ROUTING_RUNTIME_VERIFY_HINT,
  normalizeObservabilityConfig,
  normalizeSubagentRescheduleConfig,
  normalizeProviderFallback,
  normalizeMaxConcurrentRequests,
  prepareModelAdaptersForPersist,
  shouldTestModelAdapterEndpoint,
  validateProviderFallbackAdapters,
  validateUpstreamCapacityAdapters,
  normalizeGatewayConfig,
  gatewayPublicModelInvalid,
  normalizeTheme,
  resolveEffectiveTheme,
} from "@/state/configProjection";
import {
  ACCESS_PATH,
  accessClientConfigScope,
} from "@/router/access";
import {
  checkForUpdates,
  downloadAvailableUpdate,
  exportUserConfig as exportUserConfigFile,
  getAppVersion,
  getHomeMetricsSummary,
  getHomeMetricsReport,
  resetHomeMetricsSummary,
  getLogCaptureStatus,
  getModelAdapterTestResults,
  cleanupClosedLogSessions,
  installReadyUpdate,
  getProxyState,
  importUserConfig as importUserConfigFile,
  openConfigWindow as openConfig,
  loadUserConfig,
  openLogsDirectory,
  openModelConfig,
  saveUserConfig,
  saveGatewayConfig,
  saveModelAdapters,
  saveCursorConfig,
  saveSystemSettings,
  saveHomeMetrics,
  setAppearanceTheme,
  startProxyService,
  stopProxyService,
  startGatewayService,
  stopGatewayService,
  testGatewayService,
  testModelAdapter,
  fetchModelAdapterModels,
  copyGatewayToken as copyGatewayTokenApi,
  rotateGatewayToken as rotateGatewayTokenApi,
} from "@/services/clientApi";
import {
  normalizeReasoningEffort,
  SUPPORTED_REASONING_EFFORTS,
} from "@/state/modelAdapterReasoning";
import {
  createEmptyHomeMetrics,
  createEmptyHomeMetricsReport,
  DEFAULT_HOME_METRICS_RANGE,
  normalizeHomeMetrics,
  normalizeHomeMetricsReport,
} from "@/state/homeMetrics";
import { applyModelAdapterTypeChange } from "@/state/modelAdapterTypeChange";

export { applyModelAdapterTypeChange };

const APP_STATE_STORAGE_KEY = "cursor-client:runtime-state:v2";
const GENERIC_SERVICE_ERROR = "服务错误";
const SUPPORTED_MODEL_ADAPTER_TYPES = new Set(["openai", "anthropic"]);
const SUPPORTED_CREDENTIAL_SOURCES = new Set(["static", "codex", "grok"]);
const SUPPORTED_ANTHROPIC_THINKING_EFFORTS = new Set(["low", "medium", "high", "xhigh", "max"]);
export const ANTHROPIC_THINKING_EFFORT_DEFAULT = "xhigh";
export const OPENAI_ENDPOINT_RESPONSES = "/v1/responses";
export const OPENAI_ENDPOINT_CHAT_COMPLETIONS = "/v1/chat/completions";
export const OPENAI_ENDPOINT_CUSTOM = "/custom";
export const CODEX_SUBSCRIPTION_BASE_URL = "https://chatgpt.com/backend-api/codex/responses";
export const GROK_SUBSCRIPTION_BASE_URL = "https://api.x.ai/v1";
export const OPENAI_EXTRA_PARAMS_DEFAULT_JSON = `{
  "service_tier": "priority"
}`;
export const EXTRA_PARAMS_DEFAULT_JSON = `{
}`;
export const CUSTOM_HEADERS_DEFAULT_JSON = `{
}`;
const SUPPORTED_OPENAI_ENDPOINTS = new Set([OPENAI_ENDPOINT_RESPONSES, OPENAI_ENDPOINT_CHAT_COMPLETIONS, OPENAI_ENDPOINT_CUSTOM]);
const SUPPORTED_ROUTE_MODES = new Set(["local", "upstream"]);
const PROXY_STATE_EVENT = "proxy:state";
const USER_CONFIG_CHANGED_EVENT = "user-config:changed";
const UPDATE_STATE_EVENT = "update:state";
const UPDATE_PROGRESS_EVENT = "update:progress";
const UPDATE_READY_EVENT = "update:ready";
const UPDATE_ERROR_EVENT = "update:error";
const MODEL_ADAPTER_TEST_UPDATED_EVENT = "model-adapter-test:updated";
const SUPPORTED_MODEL_ADAPTER_TEST_STATUSES = new Set(["idle", "running", "success", "error"]);
const HOME_METRICS_MIN_LOADING_MS = 600;

export const ROUTE_MODE_OPTIONS = [
  { label: "本地服务模式", value: "local" },
  { label: "直连 Cursor 模式", value: "upstream" },
];

export const THEME_OPTIONS = [
  { label: "浅色", value: "light" },
  { label: "深色", value: "dark" },
  { label: "跟随系统", value: "system" },
];

function asString(value) {
  if (typeof value === "string") {
    return value.trim();
  }
  if (value instanceof String) {
    return value.toString().trim();
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return "";
}

function asBoolean(value, fallback = false) {
  if (typeof value === "boolean") {
    return value;
  }
  if (typeof value === "number") {
    return value !== 0;
  }
  const normalized = asString(value).toLowerCase();
  if (!normalized) {
    return fallback;
  }
  return normalized === "true" || normalized === "1" || normalized === "yes";
}

function asArray(value) {
  return Array.isArray(value) ? value : [];
}

function asPositiveIntegerString(value) {
  const text = asString(value);
  if (!text) {
    return "";
  }
  if (!/^\d+$/.test(text)) {
    return "";
  }
  return Number(text) > 0 ? text : "";
}

function asPositiveInteger(value) {
  const text = asPositiveIntegerString(value);
  if (!text) {
    return 0;
  }
  return Number(text);
}

function asNumber(value, fallback = 0) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  const text = asString(value);
  if (!text) {
    return fallback;
  }
  const parsed = Number(text);
  return Number.isFinite(parsed) ? parsed : fallback;
}

function formatReleaseDate(value) {
  const text = asString(value);
  if (!text) {
    return "未知";
  }
  const parsed = dayjs(text);
  if (!parsed.isValid()) {
    return text;
  }
  return parsed.format("YYYY-MM-DD HH:mm");
}

function normalizeRouteMode(value, fallback = "local") {
  const text = asString(value).toLowerCase();
  if (SUPPORTED_ROUTE_MODES.has(text)) {
    return text;
  }
  return fallback;
}

function normalizeBaseURL(value) {
  const text = asString(value);
  if (!text) {
    return "";
  }
  try {
    const parsed = new URL(text);
    if (parsed.protocol !== "http:" && parsed.protocol !== "https:") {
      return "";
    }
    parsed.protocol = parsed.protocol.toLowerCase();
    parsed.hostname = parsed.hostname.toLowerCase();
    const normalized = parsed.toString().replace(/\/+$/, "");
    return normalized || parsed.toString();
  } catch (_error) {
    return text;
  }
}

function buildModelAdapterIdentityKey(adapter) {
  return [
    normalizeBaseURL(adapter.baseURL),
    asString(adapter.modelID),
    isManagedCredentialSource(adapter.credentialSource)
      ? `credential:${normalizeCredentialSource(adapter.credentialSource)}`
      : asString(adapter.apiKey),
    asString(adapter.displayName),
    adapter.type === "openai" ? normalizeOpenAIEndpoint(adapter.openAIEndpoint) : "",
  ].join("\n");
}

function hashStringFNV32a(value) {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return hash.toString(16).padStart(8, "0");
}

export function buildModelAdapterTestRequestHash(source) {
  const adapter = normalizeModelAdapter(source);
  return hashStringFNV32a([
    asString(adapter.type),
    normalizeBaseURL(adapter.baseURL),
    asString(adapter.apiKey),
    asString(adapter.credentialSource),
    asString(adapter.modelID),
    adapter.type === "openai" ? asString(adapter.reasoningEffort) : "",
    adapter.type === "openai" ? normalizeOpenAIEndpoint(adapter.openAIEndpoint) : "",
    adapter.type === "openai" ? String(Boolean(adapter.openAIExtraParamsEnabled)) : "false",
    adapter.type === "openai" && adapter.openAIExtraParamsEnabled ? asString(adapter.openAIExtraParamsJSON) : "",
    String(Boolean(adapter.customHeadersEnabled)),
    adapter.customHeadersEnabled ? asString(adapter.customHeadersJSON) : "",
    adapter.type === "anthropic" ? String(Boolean(adapter.anthropicExtraParamsEnabled)) : "false",
    adapter.type === "anthropic" && adapter.anthropicExtraParamsEnabled ? asString(adapter.anthropicExtraParamsJSON) : "",
    String(asPositiveInteger(adapter.contextWindowTokens)),
    String(asPositiveInteger(adapter.maxCompletionTokens)),
    String(asPositiveInteger(adapter.anthropicMaxTokens)),
    adapter.type === "anthropic" ? asString(adapter.anthropicThinkingEffort || ANTHROPIC_THINKING_EFFORT_DEFAULT) : "",
  ].join("\n"));
}

export function formatDuration(value) {
  const durationMS = Math.max(0, Math.round(asNumber(value)));
  if (durationMS < 1000) {
    return `${durationMS} ms`;
  }
  return `${(durationMS / 1000).toFixed(1)} s`;
}

function normalizeModelAdapterTestStatus(value) {
  const text = asString(value).toLowerCase();
  return SUPPORTED_MODEL_ADAPTER_TEST_STATUSES.has(text) ? text : "idle";
}

export function formatModelAdapterTestSummary(source) {
  const result = source && typeof source === "object" ? source : {};
  const status = normalizeModelAdapterTestStatus(result.status);
  if (status === "running") {
    return "测试中...";
  }
  if (status === "error") {
    return asString(result.error) || "模型测试失败";
  }
  if (status !== "success") {
    return "";
  }
  const roundedTPS = Math.max(0, Math.round(asNumber(result.tokensPerSecond)));
  return `${roundedTPS} t/s | 首字 ${formatDuration(result.firstTextTokenMS)}`;
}

function normalizeModelAdapterTestResult(source) {
  const raw = source && typeof source === "object" ? source : {};
  const status = normalizeModelAdapterTestStatus(raw.status);
  const normalized = {
    adapterID: asString(raw.adapterID),
    requestHash: asString(raw.requestHash),
    status,
    tokensPerSecond: Math.max(0, asNumber(raw.tokensPerSecond)),
    firstTextTokenMS: Math.max(0, Math.round(asNumber(raw.firstTextTokenMS))),
    totalDurationMS: Math.max(0, Math.round(asNumber(raw.totalDurationMS))),
    outputTokens: Math.max(0, Math.round(asNumber(raw.outputTokens))),
    tokensEstimated: asBoolean(raw.tokensEstimated),
    summaryText: asString(raw.summaryText),
    error: asString(raw.error),
    rawResponse: asString(raw.rawResponse),
    testedAt: asString(raw.testedAt),
  };
  if (status === "running" || status === "success") {
    normalized.summaryText = formatModelAdapterTestSummary(normalized);
  }
  if (status === "error" && !normalized.summaryText) {
    normalized.summaryText = normalized.error || "模型测试失败";
  }
  return normalized;
}

function normalizeModelAdapterTestResults(source) {
  const raw = source && typeof source === "object" && !Array.isArray(source)
    ? source.results
    : source;
  return asArray(raw)
    .map((item) => normalizeModelAdapterTestResult(item))
    .filter((item) => item.adapterID);
}

export function createEmptyModelAdapter() {
  return {
    id: "",
    sort: 0,
    displayName: "",
    type: "openai",
    baseURL: "",
    apiKey: "",
    credentialSource: "static",
    tooltipData: "备注",
    modelID: "",
    reasoningEffort: "",
    openAIEndpoint: OPENAI_ENDPOINT_CHAT_COMPLETIONS,
    openAIExtraParamsEnabled: false,
    openAIExtraParamsJSON: OPENAI_EXTRA_PARAMS_DEFAULT_JSON,
    customHeadersEnabled: false,
    customHeadersJSON: CUSTOM_HEADERS_DEFAULT_JSON,
    anthropicExtraParamsEnabled: false,
    anthropicExtraParamsJSON: EXTRA_PARAMS_DEFAULT_JSON,
    contextWindowTokens: 0,
    maxCompletionTokens: 0,
    anthropicMaxTokens: 0,
    anthropicThinkingEffort: ANTHROPIC_THINKING_EFFORT_DEFAULT,
    thinkingBudgetTokens: 0,
    maxConcurrentRequests: 0,
    providerFallback: {
      enabled: false,
      primaryChannelID: "",
      candidateChannelIDs: [],
      maxHttpAttempts: DEFAULT_PROVIDER_FALLBACK.maxHttpAttempts,
      maxWaitSeconds: DEFAULT_PROVIDER_FALLBACK.maxWaitSeconds,
    },
  };
}

// normalizeOpenAIEndpoint 归一化 endpoint 路径。
// 支持三个预设值：/v1/responses、/v1/chat/completions、/custom（自定义路径）。
// 选 /custom 时，用户需在接口地址栏填写完整请求 URL。
function normalizeOpenAIEndpoint(value) {
  const text = asString(value).toLowerCase();
  if (!text) {
    return OPENAI_ENDPOINT_CHAT_COMPLETIONS;
  }
  return SUPPORTED_OPENAI_ENDPOINTS.has(text) ? text : "";
}

function isValidOpenAIEndpoint(value) {
  return normalizeOpenAIEndpoint(value) !== "";
}

function normalizeCredentialSource(value) {
  const source = asString(value).toLowerCase();
  return SUPPORTED_CREDENTIAL_SOURCES.has(source) ? source : "static";
}

function isManagedCredentialSource(value) {
  const source = normalizeCredentialSource(value);
  return source === "codex" || source === "grok";
}

export { isManagedCredentialSource };

function validateJSONObject(value, label) {
  const text = asString(value);
  if (!text) {
    return `${label}不能为空`;
  }
  try {
    const parsed = JSON.parse(text);
    if (!parsed || typeof parsed !== "object" || Array.isArray(parsed)) {
      return `${label}必须是 JSON 对象`;
    }
  } catch (_error) {
    return `${label}必须是合法 JSON 对象`;
  }
  return "";
}

function validateHeadersJSON(value) {
  const objectError = validateJSONObject(value, "自定义请求头 JSON");
  if (objectError) {
    return objectError;
  }
  const parsed = JSON.parse(asString(value));
  for (const [key, item] of Object.entries(parsed)) {
    if (!asString(key)) {
      return "自定义请求头名称不能为空";
    }
    if (typeof item !== "string") {
      return `自定义请求头 ${key} 的值必须是字符串`;
    }
  }
  return "";
}

function validateOpenAIExtraParamsJSON(value) {
  return validateJSONObject(value, "额外参数 JSON");
}

function validateAnthropicExtraParamsJSON(value) {
  return validateJSONObject(value, "Anthropic 额外参数 JSON");
}

export function normalizeModelAdapter(source) {
  const raw = source && typeof source === "object" ? source : {};
  const normalizedType = asString(raw.type).toLowerCase();
  const normalizedReasoningEffort = normalizeReasoningEffort(raw.reasoningEffort ?? raw.reasoning_effort);
  const normalizedAnthropicThinkingEffort = asString(
    raw.anthropicThinkingEffort
      ?? raw.anthropic_thinking_effort
      ?? raw.outputConfigEffort
      ?? raw.output_config_effort,
  ).toLowerCase();
  const normalizedOpenAIEndpoint = normalizeOpenAIEndpoint(
    raw.openAIEndpoint ?? raw.openaiEndpoint ?? raw.open_ai_endpoint ?? raw.endpoint,
  );
  const normalizedCredentialSource = normalizeCredentialSource(raw.credentialSource ?? raw.credential_source);
  let normalizedBaseURL = normalizeBaseURL(raw.baseURL || raw.url);
  let effectiveOpenAIEndpoint = normalizedOpenAIEndpoint;
  if (normalizedType === "openai" && normalizedCredentialSource === "codex") {
    normalizedBaseURL = CODEX_SUBSCRIPTION_BASE_URL;
    effectiveOpenAIEndpoint = OPENAI_ENDPOINT_RESPONSES;
  } else if (normalizedType === "openai" && normalizedCredentialSource === "grok") {
    normalizedBaseURL = GROK_SUBSCRIPTION_BASE_URL;
    effectiveOpenAIEndpoint = OPENAI_ENDPOINT_CHAT_COMPLETIONS;
  }
  const openAIExtraParamsEnabled = normalizedType === "openai"
    ? asBoolean(raw.openAIExtraParamsEnabled ?? raw.openaiExtraParamsEnabled ?? raw.open_ai_extra_params_enabled)
    : false;
  const openAIExtraParamsJSON = normalizedType === "openai"
    ? asString(raw.openAIExtraParamsJSON ?? raw.openaiExtraParamsJSON ?? raw.open_ai_extra_params_json) || OPENAI_EXTRA_PARAMS_DEFAULT_JSON
    : "";
  const customHeadersEnabled = asBoolean(raw.customHeadersEnabled ?? raw.custom_headers_enabled);
  const customHeadersJSON = asString(raw.customHeadersJSON ?? raw.custom_headers_json) || CUSTOM_HEADERS_DEFAULT_JSON;
  const anthropicExtraParamsEnabled = normalizedType === "anthropic"
    ? asBoolean(raw.anthropicExtraParamsEnabled ?? raw.anthropic_extra_params_enabled)
    : false;
  const anthropicExtraParamsJSON = normalizedType === "anthropic"
    ? asString(raw.anthropicExtraParamsJSON ?? raw.anthropic_extra_params_json) || EXTRA_PARAMS_DEFAULT_JSON
    : "";
  return {
    id: asString(raw.id),
    sort: asPositiveInteger(raw.sort),
    displayName: asString(raw.displayName || raw.name),
    type: SUPPORTED_MODEL_ADAPTER_TYPES.has(normalizedType) ? normalizedType : "",
    baseURL: normalizedBaseURL,
    apiKey: isManagedCredentialSource(normalizedCredentialSource) ? "" : asString(raw.apiKey || raw.key),
    credentialSource: normalizedCredentialSource,
    tooltipData: asString(raw.tooltipData),
    modelID: asString(raw.modelID),
    reasoningEffort: normalizedReasoningEffort,
    openAIEndpoint: normalizedType === "openai" ? effectiveOpenAIEndpoint : "",
    openAIExtraParamsEnabled,
    openAIExtraParamsJSON,
    customHeadersEnabled,
    customHeadersJSON,
    anthropicExtraParamsEnabled,
    anthropicExtraParamsJSON,
    contextWindowTokens: asPositiveInteger(
      raw.contextWindowTokens ?? raw.context_window_tokens ?? raw.maxInputTokens ?? raw.max_input_tokens,
    ),
    maxCompletionTokens: asPositiveInteger(
      raw.maxCompletionTokens ?? raw.max_completion_tokens ?? raw.max_tokens ?? raw.max_token,
    ),
    anthropicMaxTokens: asPositiveInteger(
      raw.anthropicMaxTokens ?? raw.anthropic_max_tokens ?? raw.max_tokens,
    ),
    anthropicThinkingEffort: normalizedType === "anthropic"
      ? (SUPPORTED_ANTHROPIC_THINKING_EFFORTS.has(normalizedAnthropicThinkingEffort)
        ? normalizedAnthropicThinkingEffort
        : ANTHROPIC_THINKING_EFFORT_DEFAULT)
      : "",
    thinkingBudgetTokens: asPositiveInteger(
      raw.thinkingBudgetTokens ?? raw.thinking_budget_tokens,
    ),
    maxConcurrentRequests: normalizeMaxConcurrentRequests(
      raw.maxConcurrentRequests ?? raw.max_concurrent_requests,
    ),
    providerFallback: normalizeProviderFallback(raw.providerFallback ?? raw.provider_fallback),
  };
}

export function normalizeModelAdapters(source) {
  return asArray(source)
    .map((item, sourceIndex) => ({
      adapter: normalizeModelAdapter(item),
      sourceIndex,
    }))
    .sort((left, right) => {
      const leftSort = left.adapter.sort;
      const rightSort = right.adapter.sort;
      if (leftSort <= 0 && rightSort <= 0) {
        return left.sourceIndex - right.sourceIndex;
      }
      if (leftSort <= 0) {
        return 1;
      }
      if (rightSort <= 0) {
        return -1;
      }
      return leftSort - rightSort || left.sourceIndex - right.sourceIndex;
    })
    .map(({ adapter }, index) => ({
      ...adapter,
      sort: index + 1,
    }));
}

export function validateModelAdapters(source, { allAdapters } = {}) {
  const adapters = normalizeModelAdapters(source);
  const seenIdentityKeys = new Set();
  for (const [index, adapter] of adapters.entries()) {
    const prefix = `模型 ${index + 1}`;
    if (!SUPPORTED_MODEL_ADAPTER_TYPES.has(adapter.type)) {
      return `${prefix} 的类型仅支持 OpenAI 或 Anthropic`;
    }
    if (!adapter.baseURL) {
      return `${prefix} 的接口地址不能为空`;
    }
    if (!isManagedCredentialSource(adapter.credentialSource) && !adapter.apiKey) {
      return `${prefix} 的访问密钥不能为空`;
    }
    if (!adapter.displayName) {
      return `${prefix} 的显示名称不能为空`;
    }
    if (!adapter.modelID) {
      return `${prefix} 的模型标识不能为空`;
    }
    if (adapter.contextWindowTokens && (!Number.isInteger(adapter.contextWindowTokens) || adapter.contextWindowTokens <= 0)) {
      return `${prefix} 的上下文窗口必须为正整数`;
    }
    if (adapter.type === "openai" && !SUPPORTED_REASONING_EFFORTS.has(adapter.reasoningEffort)) {
      return `${prefix} 的推理强度仅支持不设置、low、medium、high、xhigh、max`;
    }
    if (adapter.type === "anthropic" && adapter.anthropicMaxTokens && (!Number.isInteger(adapter.anthropicMaxTokens) || adapter.anthropicMaxTokens <= 0)) {
      return `${prefix} 的最大输出 Token 必须为正整数`;
    }
    if (adapter.type === "anthropic" && !SUPPORTED_ANTHROPIC_THINKING_EFFORTS.has(adapter.anthropicThinkingEffort)) {
      return `${prefix} 的 Anthropic 思考强度仅支持 low、medium、high、xhigh、max`;
    }
    if (adapter.type === "openai" && adapter.maxCompletionTokens && (!Number.isInteger(adapter.maxCompletionTokens) || adapter.maxCompletionTokens <= 0)) {
      return `${prefix} 的最大输出 Token 必须为正整数`;
    }
    if (adapter.type === "openai" && !isValidOpenAIEndpoint(adapter.openAIEndpoint)) {
      return `${prefix} 的 OpenAI 端点仅支持 /v1/responses、/v1/chat/completions 或以 / 开头的自定义路径`;
    }
    if (adapter.type === "openai" && adapter.openAIExtraParamsEnabled) {
      const extraParamsError = validateOpenAIExtraParamsJSON(adapter.openAIExtraParamsJSON);
      if (extraParamsError) {
        return `${prefix} 的 ${extraParamsError}`;
      }
    }
    if (adapter.type === "anthropic" && adapter.anthropicExtraParamsEnabled) {
      const extraParamsError = validateAnthropicExtraParamsJSON(adapter.anthropicExtraParamsJSON);
      if (extraParamsError) {
        return `${prefix} 的 ${extraParamsError}`;
      }
    }
    if (adapter.customHeadersEnabled) {
      const customHeadersError = validateHeadersJSON(adapter.customHeadersJSON);
      if (customHeadersError) {
        return `${prefix} 的 ${customHeadersError}`;
      }
    }
    if (!adapter.tooltipData) {
      return `${prefix} 的悬停提示不能为空`;
    }
    if (adapter.thinkingBudgetTokens && (!Number.isInteger(adapter.thinkingBudgetTokens) || adapter.thinkingBudgetTokens <= 0)) {
      return `${prefix} 的思考预算 Token 必须为正整数`;
    }
    const dedupeKey = buildModelAdapterIdentityKey(adapter);
    if (seenIdentityKeys.has(dedupeKey)) {
      return `模型渠道重复，请检查 url、modelID、apiKey、displayName、endpoint 组合`;
    }
    seenIdentityKeys.add(dedupeKey);
  }
  // ── providerFallback 引用 / 预算校验（使用 backend 已返回的完整 adapter id）──
  // allAdapters 可由调用方传入完整列表（如单条编辑时），用于跨渠道引用完整性检查。
  const refAdapters = allAdapters ? normalizeModelAdapters(allAdapters) : adapters;
  const fallbackError = validateProviderFallbackAdapters(adapters, { allAdapters: refAdapters });
  if (fallbackError) {
    return fallbackError;
  }
  return validateUpstreamCapacityAdapters(adapters, { allAdapters: refAdapters });
}

function canUseLocalStorage() {
  return typeof window !== "undefined" && typeof window.localStorage !== "undefined";
}

function delay(ms) {
  return new Promise((resolve) => {
    window.setTimeout(resolve, Math.max(0, ms));
  });
}

function loadCachedState() {
  if (!canUseLocalStorage()) {
    return {};
  }

  try {
    const raw = window.localStorage.getItem(APP_STATE_STORAGE_KEY);
    if (!raw) {
      return {};
    }
    const parsed = JSON.parse(raw);
    if (!parsed || typeof parsed !== "object") {
      return {};
    }
    return parsed;
  } catch (_error) {
    return {};
  }
}

function normalizeConfig(source) {
  const raw = source && typeof source === "object" ? source : {};
  const homeMetrics = raw.homeMetrics && typeof raw.homeMetrics === "object" ? raw.homeMetrics : {};
  const appearance = raw.appearance && typeof raw.appearance === "object" ? raw.appearance : {};
  const advertising = raw.advertising && typeof raw.advertising === "object" ? raw.advertising : {};
  const updates = raw.updates && typeof raw.updates === "object" ? raw.updates : {};
  return {
    observability: normalizeObservabilityConfig(raw.observability, raw.log),
    providerStreamIdleTimeout: asPositiveInteger(raw.providerStreamIdleTimeout),
    backendListenAddr: asString(raw.configBackendListenAddr) || asString(raw.backendListenAddr),
    proxyListenAddr: asString(raw.configProxyListenAddr) || asString(raw.proxyListenAddr),
    modelAdapters: normalizeModelAdapters(raw.modelAdapters),
    routing: {
      mode: normalizeRouteMode(raw.routing?.mode),
    },
    homeMetrics: {
      includeCacheWriteInHitRate: asBoolean(homeMetrics.includeCacheWriteInHitRate),
    },
    appearance: {
      theme: normalizeTheme(appearance.theme),
    },
    advertising: {
      enabled: asBoolean(advertising.enabled),
    },
    updates: {
      checkOnStartup: asBoolean(updates.checkOnStartup),
    },
    subagentReschedule: normalizeSubagentRescheduleConfig(raw.subagentReschedule),
    lastAgentModelHash: asString(raw.lastAgentModelHash),
    gateway: normalizeGatewayConfig(raw.gateway),
  };
}

function applyHomeMetrics(raw) {
  appState.homeMetrics = normalizeHomeMetrics(raw);
  appState.homeMetricsError = "";
}

function serializeConfigPayload(normalized) {
  return {
    observability: normalized.observability,
    providerStreamIdleTimeout: normalized.providerStreamIdleTimeout,
    backendListenAddr: normalized.backendListenAddr,
    proxyListenAddr: normalized.proxyListenAddr,
    modelAdapters: normalized.modelAdapters.map((adapter) => ({ ...adapter })),
    routing: normalized.routing,
    homeMetrics: normalized.homeMetrics,
    appearance: normalized.appearance,
    advertising: normalized.advertising,
    updates: normalized.updates,
    subagentReschedule: normalized.subagentReschedule,
    lastAgentModelHash: normalized.lastAgentModelHash,
    gateway: {
      enabled: normalized.gateway.enabled,
      listenAddr: normalized.gateway.listenAddr,
      tokenConfigured: normalized.gateway.tokenConfigured,
      publicModels: normalized.gateway.publicModels,
    },
  };
}

function buildConfigPayload(source) {
  return serializeConfigPayload(normalizeConfig(source));
}

function buildConfigPayloadFromState(source = appState) {
  const preferences = buildClientPreferencesFromState(source);
  return serializeConfigPayload(normalizeConfig({
    observability: buildObservabilityConfigFromState(source),
    providerStreamIdleTimeout: source.providerStreamIdleTimeout,
    backendListenAddr: source.configBackendListenAddr || source.backendListenAddr,
    proxyListenAddr: source.configProxyListenAddr || source.proxyListenAddr,
    modelAdapters: source.modelAdapters,
    routing: {
      mode: source.routingMode,
    },
    homeMetrics: {
      includeCacheWriteInHitRate: source.includeCacheWriteInHitRate,
    },
    appearance: preferences.appearance,
    advertising: preferences.advertising,
    updates: preferences.updates,
    subagentReschedule: buildSubagentRescheduleConfigFromState(source),
    lastAgentModelHash: source.lastAgentModelHash,
    gateway: {
      enabled: source.gatewayEnabled,
      listenAddr: source.gatewayListenAddr,
      tokenConfigured: source.gatewayTokenConfigured,
      publicModels: source.gatewayPublicModels,
    },
  }));
}

const CONFIG_SECTION_SCOPES = ["cursor", "gateway", "models", "settings"];
const ROUTE_PATH_TO_CONFIG_SCOPE = {
  "/cursor": "cursor",
  "/gateway": "gateway",
  "/models": "models",
  "/settings": "settings",
};

const savedConfigSectionSnapshots = reactive({
  cursor: "",
  gateway: "",
  models: "",
  settings: "",
});

const GATEWAY_PUBLIC_MODEL_ADAPTER_ERROR = "Gateway 公开模型仍指向当前适配器；删除或更改适配器标识前，请先在网关集成页更新映射";

function unresolvedGatewayPublicModelsError(adapters) {
  if (!asArray(appState.gatewayPublicModels).some((item) => gatewayPublicModelInvalid(item, adapters))) {
    return "";
  }
  return GATEWAY_PUBLIC_MODEL_ADAPTER_ERROR;
}

function snapshotCursorSection(source = appState) {
  return JSON.stringify({
    routingMode: normalizeRouteMode(source.routingMode),
  });
}

function snapshotGatewaySection(source = appState) {
  return JSON.stringify({
    enabled: Boolean(source.gatewayEnabled),
    listenAddr: asString(source.gatewayListenAddr),
    publicModels: asArray(source.gatewayPublicModels).map((item) => ({
      id: asString(item?.id),
      targetAdapterID: asString(item?.targetAdapterID),
    })),
  });
}

function snapshotSettingsSection(source = appState) {
  return JSON.stringify({
    observabilityMode: asString(source.observabilityMode),
    observabilityRetentionDays: Number(source.observabilityRetentionDays) || 0,
    observabilityMaxDiskMB: Number(source.observabilityMaxDiskMB) || 0,
    appearanceTheme: normalizeTheme(source.appearanceTheme),
    advertisingEnabled: Boolean(source.advertisingEnabled),
    updateCheckOnStartup: Boolean(source.updateCheckOnStartup),
    subagentRescheduleEnabled: Boolean(source.subagentRescheduleEnabled),
  });
}

function snapshotModelsSection(source = appState) {
  return JSON.stringify(normalizeModelAdapters(source.modelAdapters));
}

function snapshotConfigSection(scope, source = appState) {
  if (scope === "cursor") {
    return snapshotCursorSection(source);
  }
  if (scope === "gateway") {
    return snapshotGatewaySection(source);
  }
  if (scope === "models") {
    return snapshotModelsSection(source);
  }
  if (scope === "settings") {
    return snapshotSettingsSection(source);
  }
  return "";
}

function applyConfigSectionSnapshot(scope, raw) {
  const parsed = typeof raw === "string" ? JSON.parse(raw) : raw;
  if (scope === "cursor") {
    appState.routingMode = normalizeRouteMode(parsed.routingMode);
    return;
  }
  if (scope === "gateway") {
    appState.gatewayEnabled = Boolean(parsed.enabled);
    appState.gatewayListenAddr = asString(parsed.listenAddr);
    appState.gatewayPublicModels = asArray(parsed.publicModels).map((item) => ({
      id: asString(item?.id),
      targetAdapterID: asString(item?.targetAdapterID),
    }));
    return;
  }
  if (scope === "models") {
    appState.modelAdapters = normalizeModelAdapters(parsed);
    return;
  }
  if (scope === "settings") {
    appState.observabilityMode = asString(parsed.observabilityMode);
    appState.observabilityRetentionDays = Number(parsed.observabilityRetentionDays) || 0;
    appState.observabilityMaxDiskMB = Number(parsed.observabilityMaxDiskMB) || 0;
    appState.appearanceTheme = normalizeTheme(parsed.appearanceTheme);
    appState.advertisingEnabled = Boolean(parsed.advertisingEnabled);
    appState.updateCheckOnStartup = Boolean(parsed.updateCheckOnStartup);
    appState.subagentRescheduleEnabled = Boolean(parsed.subagentRescheduleEnabled);
  }
}

function captureConfigSectionSnapshot(scope) {
  if (!CONFIG_SECTION_SCOPES.includes(scope)) {
    return;
  }
  savedConfigSectionSnapshots[scope] = snapshotConfigSection(scope);
}

function captureAllConfigSectionSnapshots() {
  for (const scope of CONFIG_SECTION_SCOPES) {
    captureConfigSectionSnapshot(scope);
  }
}

export function isConfigSectionDirty(scope) {
  if (!CONFIG_SECTION_SCOPES.includes(scope)) {
    return false;
  }
  if (scope === "models" && appState.modelsEditorDraftDirty) {
    return true;
  }
  return snapshotConfigSection(scope) !== savedConfigSectionSnapshots[scope];
}

export function setModelsEditorDraftDirty(value) {
  appState.modelsEditorDraftDirty = Boolean(value);
}

export function configSectionStatusText(scope) {
  if (appState.configSaving) {
    return "保存中";
  }
  if (isConfigSectionDirty(scope)) {
    return "有未保存更改";
  }
  return "已保存";
}

export function routePathToConfigScope(path, query) {
  const normalized = asString(path);
  if (normalized === ACCESS_PATH) {
    return accessClientConfigScope(query?.client);
  }
  return ROUTE_PATH_TO_CONFIG_SCOPE[normalized] || "";
}

export function isRouteConfigDirty(pathOrRoute, query) {
  const path = pathOrRoute && typeof pathOrRoute === "object"
    ? pathOrRoute.path
    : pathOrRoute;
  const routeQuery = pathOrRoute && typeof pathOrRoute === "object"
    ? pathOrRoute.query
    : query;
  const scope = routePathToConfigScope(path, routeQuery);
  return Boolean(scope && isConfigSectionDirty(scope));
}

export function isAccessTabDirty() {
  return isConfigSectionDirty("cursor") || isConfigSectionDirty("gateway");
}

export function discardConfigSectionDraft(scope) {
  if (!CONFIG_SECTION_SCOPES.includes(scope)) {
    return;
  }
  if (scope === "models") {
    appState.modelsEditorDraftDirty = false;
  }
  const saved = savedConfigSectionSnapshots[scope];
  if (!saved) {
    return;
  }
  applyConfigSectionSnapshot(scope, saved);
}

function applyConfigToState(config, { modelAdaptersOnly = false, savedScope = "" } = {}) {
  const normalized = normalizeConfig(config);
  if (modelAdaptersOnly) {
    const preserveModelsDraft = savedScope !== "all" && savedScope !== "models" && isConfigSectionDirty("models");
    const preservedModels = preserveModelsDraft ? snapshotConfigSection("models") : "";
    appState.modelAdapters = normalized.modelAdapters;
    if (preservedModels) {
      applyConfigSectionSnapshot("models", preservedModels);
    } else {
      captureConfigSectionSnapshot("models");
    }
    return normalized;
  }
  const preserved = {};
  if (savedScope !== "all") {
    for (const scope of CONFIG_SECTION_SCOPES) {
      if (scope !== savedScope && isConfigSectionDirty(scope)) {
        preserved[scope] = snapshotConfigSection(scope);
      }
    }
  }
  appState.modelAdapters = normalized.modelAdapters;
  appState.observabilityMode = normalized.observability.mode;
  appState.observabilityRetentionDays = normalized.observability.retentionDays;
  appState.observabilityMaxDiskMB = normalized.observability.maxDiskMB;
  appState.providerStreamIdleTimeout = normalized.providerStreamIdleTimeout;
  appState.lastAgentModelHash = normalized.lastAgentModelHash;
  appState.configBackendListenAddr = normalized.backendListenAddr;
  appState.configProxyListenAddr = normalized.proxyListenAddr;
  appState.routingMode = normalized.routing.mode;
  appState.includeCacheWriteInHitRate = normalized.homeMetrics.includeCacheWriteInHitRate;
  appState.appearanceTheme = normalized.appearance.theme;
  appState.advertisingEnabled = normalized.advertising.enabled;
  appState.updateCheckOnStartup = normalized.updates.checkOnStartup;
  appState.subagentRescheduleEnabled = normalized.subagentReschedule.enabled;
  appState.gatewayEnabled = normalized.gateway.enabled;
  appState.gatewayListenAddr = normalized.gateway.listenAddr;
  appState.gatewayTokenConfigured = normalized.gateway.tokenConfigured;
  appState.gatewayPublicModels = normalized.gateway.publicModels.map((item) => ({ ...item }));
  for (const scope of CONFIG_SECTION_SCOPES) {
    if (preserved[scope]) {
      applyConfigSectionSnapshot(scope, preserved[scope]);
      continue;
    }
    captureConfigSectionSnapshot(scope);
  }
  return normalized;
}

async function loadPersistedUserConfig() {
  return normalizeConfig(await loadUserConfig());
}

async function persistConfigPayload(config, { modelAdaptersOnly = false, savedScope = "all" } = {}) {
  if (appState.configSaving) {
    return { ok: false, error: "已有配置操作正在进行，请稍后再试" };
  }
  const normalized = normalizeConfig(config);
  const prepared = prepareModelAdaptersForPersist(
    normalized.modelAdapters,
    (adaptersWithIds) => validateModelAdapters(adaptersWithIds),
  );
  if (!prepared.ok) {
    return {
      ok: false,
      error: prepared.error,
    };
  }
  if (modelAdaptersOnly) {
    const mappingError = unresolvedGatewayPublicModelsError(prepared.adaptersWithIds);
    if (mappingError) {
      return {
        ok: false,
        error: mappingError,
      };
    }
  }
  const payload = serializeConfigPayload({
    ...normalized,
    modelAdapters: prepared.payloadAdapters,
  });

  appState.configSaving = true;
  try {
    if (modelAdaptersOnly) {
      await saveModelAdapters(payload);
    } else {
      await saveUserConfig(payload);
    }
    const persisted = await loadPersistedUserConfig();
    applyConfigToState(persisted, {
      modelAdaptersOnly,
      savedScope: modelAdaptersOnly ? "models" : savedScope,
    });
    return {
      ok: true,
      error: "",
    };
  } catch (error) {
    return {
      ok: false,
      error: toUserError(error),
    };
  } finally {
    appState.configSaving = false;
  }
}

async function persistScopedConfig(scope) {
  if (appState.configSaving) {
    return { ok: false, error: "已有配置操作正在进行，请稍后再试" };
  }
  const payload = buildConfigPayloadFromState();
  if (scope === "gateway") {
    const invalid = asArray(appState.gatewayPublicModels).some((item) => (
      gatewayPublicModelInvalid(item, appState.modelAdapters)
    ));
    if (invalid) {
      return { ok: false, error: "公开模型映射已失效或未选择目标适配器" };
    }
  }
  if (scope === "models") {
    const prepared = prepareModelAdaptersForPersist(
      normalizeModelAdapters(appState.modelAdapters),
      (adaptersWithIds) => validateModelAdapters(adaptersWithIds),
    );
    if (!prepared.ok) {
      return { ok: false, error: prepared.error };
    }
    const mappingError = unresolvedGatewayPublicModelsError(prepared.adaptersWithIds);
    if (mappingError) {
      return { ok: false, error: mappingError };
    }
    payload.modelAdapters = prepared.payloadAdapters;
  }
  const runners = {
    cursor: saveCursorConfig,
    gateway: saveGatewayConfig,
    models: saveModelAdapters,
    settings: saveSystemSettings,
  };
  const runner = runners[scope];
  if (!runner) {
    return { ok: false, error: "未知配置范围" };
  }
  appState.configSaving = true;
  try {
    await runner(payload);
    const persisted = await loadPersistedUserConfig();
    applyConfigToState(persisted, { savedScope: scope });
    return { ok: true, error: "" };
  } catch (error) {
    return { ok: false, error: toUserError(error) };
  } finally {
    appState.configSaving = false;
  }
}

function applyProxyState(raw) {
  const state = raw && typeof raw === "object" ? raw : {};
  appState.backendRunning = asBoolean(state.backendRunning);
  appState.proxyRunning = asBoolean(state.proxyRunning ?? state.running);
  appState.serviceRunning = appState.proxyRunning;
  appState.serviceLastError = asString(state.lastError);
  appState.backendListenAddr = asString(state.backendListenAddr);
  appState.proxyListenAddr = asString(state.proxyListenAddr || state.listenAddr);
  appState.serviceListenAddr = appState.proxyListenAddr;
  appState.cursorSettingsApplied = asBoolean(state.cursorSettingsApplied);
  appState.netProxySource = asString(state.netProxySource);
  appState.netProxyActive = asBoolean(state.netProxyActive);
  appState.netProxyUsingSystem = asBoolean(state.netProxyUsingSystem);
  appState.netProxyUsingEnv = asBoolean(state.netProxyUsingEnv);
  appState.netProxyHttp = asString(state.netProxyHttp);
  appState.netProxyHttps = asString(state.netProxyHttps);
  appState.netProxyPacIgnored = asBoolean(state.netProxyPacIgnored);
  appState.netProxyDescription = asString(state.netProxyDescription);
  appState.gatewayRunning = asBoolean(state.gatewayRunning);
  appState.gatewayRuntimeListenAddr = asString(state.gatewayListenAddr);
  appState.gatewayLastError = asString(state.gatewayLastError);
}

function handleProxyStateEvent(event) {
  if (event?.data && typeof event.data === "object") {
    applyProxyState(event.data);
    return;
  }
  void syncServiceState().catch(() => {});
}

function handleUserConfigChangedEvent(event) {
  if (event?.data && typeof event.data === "object") {
    applyConfigToState(event.data);
    return;
  }
  void reloadUserConfig().catch(() => {});
}

function applyModelAdapterTestResults(source) {
  const next = {};
  for (const result of normalizeModelAdapterTestResults(source)) {
    next[result.adapterID] = result;
  }
  appState.modelAdapterTestResults = next;
  return next;
}

function handleModelAdapterTestUpdatedEvent(event) {
  if (event?.data) {
    applyModelAdapterTestResults(event.data);
    return;
  }
  void refreshModelAdapterTestResults().catch(() => {});
}

function normalizeUpdateState(value) {
  const text = asString(value).toLowerCase();
  if (["idle", "checking", "available", "downloading", "ready", "installing", "error"].includes(text)) {
    return text;
  }
  return "idle";
}

function applyUpdateSnapshot(raw) {
  const data = raw && typeof raw === "object" ? raw : {};
  const nextState = normalizeUpdateState(data.state ?? appState.updateState);
  appState.updateState = nextState;

  const version = asString(data.version);
  if (version) {
    appState.updateVersion = version;
  } else if (nextState === "idle") {
    appState.updateVersion = "";
  }

  const releaseDate = asString(data.releaseDate);
  if (releaseDate) {
    appState.updateReleaseDate = releaseDate;
  } else if (nextState === "idle") {
    appState.updateReleaseDate = "";
  }

  if (typeof data.releaseNotes === "string") {
    appState.updateReleaseNotes = data.releaseNotes.replace(/\r\n/g, "\n");
  } else if (nextState === "idle") {
    appState.updateReleaseNotes = "";
  }

  if (typeof data.mandatory === "boolean") {
    appState.updateMandatory = data.mandatory;
  } else if (nextState === "idle") {
    appState.updateMandatory = false;
  }

  if (typeof data.error === "string") {
    appState.updateError = data.error.trim();
  } else if (nextState !== "error") {
    appState.updateError = "";
  }

  if (typeof data.message === "string") {
    appState.updateMessage = data.message.trim();
  } else if (!data.prompt) {
    appState.updateMessage = "";
  }

  if (typeof data.downloaded === "number") {
    appState.updateProgressDownloaded = data.downloaded;
  } else if (nextState !== "downloading") {
    appState.updateProgressDownloaded = 0;
  }

  if (typeof data.total === "number") {
    appState.updateProgressTotal = data.total;
  } else if (nextState !== "downloading") {
    appState.updateProgressTotal = 0;
  }

  if (typeof data.percentage === "number") {
    appState.updateProgressPercent = Math.max(0, Math.min(100, data.percentage));
  } else if (nextState !== "downloading") {
    appState.updateProgressPercent = 0;
  }
}

function openUpdatePrompt(kind, payload = {}) {
  appState.updatePromptKind = asString(kind) || "idle";
  appState.updatePromptVisible = true;
  appState.updatePromptBusy = false;
  if (typeof payload.message === "string") {
    appState.updateMessage = payload.message.trim();
  }
  if (typeof payload.error === "string") {
    appState.updateError = payload.error.trim();
  }
}

function handleUpdateStateEvent(event) {
  const data = event?.data && typeof event.data === "object" ? event.data : {};
  applyUpdateSnapshot(data);
  if (asBoolean(data.prompt)) {
    openUpdatePrompt(asString(data.promptKind) || "idle", data);
  }
}

function handleUpdateProgressEvent(event) {
  const data = event?.data && typeof event.data === "object" ? event.data : {};
  applyUpdateSnapshot({
    ...data,
    state: "downloading",
  });
}

function handleUpdateReadyEvent(event) {
  const data = event?.data && typeof event.data === "object" ? event.data : {};
  applyUpdateSnapshot({
    ...data,
    state: "ready",
  });
  if (data.prompt !== false) {
    openUpdatePrompt("ready", data);
  }
}

function handleUpdateErrorEvent(event) {
  const data = event?.data && typeof event.data === "object" ? event.data : {};
  applyUpdateSnapshot({
    ...data,
    state: "error",
  });
  if (asBoolean(data.prompt)) {
    openUpdatePrompt("error", data);
  }
}

function extractErrorMessage(error) {
  if (typeof error === "string") {
    return error.trim();
  }
  if (error && typeof error === "object") {
    return asString(error.message) || asString(error.error);
  }
  return "";
}

const cachedState = loadCachedState();
const cachedConfig = normalizeConfig(cachedState);

export const appState = reactive({
  appVersion: "",
  modelAdapters: cachedConfig.modelAdapters,
  modelAdapterTestResults: {},
  configBackendListenAddr: cachedConfig.backendListenAddr,
  configProxyListenAddr: cachedConfig.proxyListenAddr,
  routingMode: cachedConfig.routing.mode,
  includeCacheWriteInHitRate: cachedConfig.homeMetrics.includeCacheWriteInHitRate,
  appearanceTheme: cachedConfig.appearance.theme,
  effectiveAppearanceTheme: resolveEffectiveTheme(cachedConfig.appearance.theme, false),
  advertisingEnabled: cachedConfig.advertising.enabled,
  updateCheckOnStartup: cachedConfig.updates.checkOnStartup,
  subagentRescheduleEnabled: cachedConfig.subagentReschedule.enabled,

  serviceRunning: asBoolean(cachedState.serviceRunning),
  backendRunning: asBoolean(cachedState.backendRunning),
  proxyRunning: asBoolean(cachedState.proxyRunning),
  serviceBusy: false,
  gatewayBusy: false,
  serviceLastError: asString(cachedState.serviceLastError),
  serviceListenAddr: asString(cachedState.serviceListenAddr),
  backendListenAddr: asString(cachedState.backendListenAddr),
  proxyListenAddr: asString(cachedState.proxyListenAddr),
  cursorSettingsApplied: asBoolean(cachedState.cursorSettingsApplied),
  netProxySource: asString(cachedState.netProxySource),
  netProxyActive: asBoolean(cachedState.netProxyActive),
  netProxyUsingSystem: asBoolean(cachedState.netProxyUsingSystem),
  netProxyUsingEnv: asBoolean(cachedState.netProxyUsingEnv),
  netProxyHttp: asString(cachedState.netProxyHttp),
  netProxyHttps: asString(cachedState.netProxyHttps),
  netProxyPacIgnored: asBoolean(cachedState.netProxyPacIgnored),
  netProxyDescription: asString(cachedState.netProxyDescription),

  configSaving: false,
  modelsEditorDraftDirty: false,
  observabilityMode: cachedConfig.observability.mode,
  observabilityRetentionDays: cachedConfig.observability.retentionDays,
  observabilityMaxDiskMB: cachedConfig.observability.maxDiskMB,
  logCaptureStatus: {
    enabled: false,
    mode: cachedConfig.observability.mode,
    sessionId: "",
    sessionPath: "",
    logsRoot: "",
    payloadDegraded: false,
    droppedEvents: 0,
    lastError: "",
  },
  logCaptureLoading: false,
  logCleanupRunning: false,
  providerStreamIdleTimeout: cachedConfig.providerStreamIdleTimeout,
  lastAgentModelHash: cachedConfig.lastAgentModelHash,
  gatewayEnabled: cachedConfig.gateway.enabled,
  gatewayListenAddr: cachedConfig.gateway.listenAddr,
  gatewayTokenConfigured: cachedConfig.gateway.tokenConfigured,
  gatewayPublicModels: cachedConfig.gateway.publicModels.map((item) => ({ ...item })),
  gatewayRunning: asBoolean(cachedState.gatewayRunning),
  gatewayRuntimeListenAddr: asString(cachedState.gatewayRuntimeListenAddr),
  gatewayLastError: asString(cachedState.gatewayLastError),
  homeMetrics: createEmptyHomeMetrics(),
  homeMetricsLoading: false,
  homeMetricsResetting: false,
  homeMetricsError: "",
  homeMetricsRange: DEFAULT_HOME_METRICS_RANGE,
  homeMetricsReport: createEmptyHomeMetricsReport(DEFAULT_HOME_METRICS_RANGE),
  homeMetricsReportLoading: false,
  homeMetricsReportError: "",

  updateState: "idle",
  updateMandatory: false,
  updateVersion: "",
  updateReleaseDate: "",
  updateReleaseNotes: "",
  updateProgressDownloaded: 0,
  updateProgressTotal: 0,
  updateProgressPercent: 0,
  updateError: "",
  updateMessage: "",
  updatePromptVisible: false,
  updatePromptKind: "idle",
  updatePromptBusy: false,
});

captureAllConfigSectionSnapshots();

export const configSectionDirty = reactive({
  cursor: computed(() => isConfigSectionDirty("cursor")),
  gateway: computed(() => isConfigSectionDirty("gateway")),
  models: computed(() => isConfigSectionDirty("models")),
  settings: computed(() => isConfigSectionDirty("settings")),
  access: computed(() => isAccessTabDirty()),
});

export function applyAppearanceTheme(value) {
  const effective = resolveEffectiveTheme(value, readSystemPrefersDark());
  if (appState.effectiveAppearanceTheme !== effective) {
    appState.effectiveAppearanceTheme = effective;
  }
  if (typeof document !== "undefined") {
    document.documentElement.dataset.theme = effective;
  }
  syncNativeWindowBackground(effective);
}

function readSystemPrefersDark() {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return false;
  }
  try {
    return Boolean(window.matchMedia("(prefers-color-scheme: dark)").matches);
  } catch (_error) {
    return false;
  }
}

let lastSyncedNativeTheme = "";

function syncNativeWindowBackground(effective) {
  if (effective === lastSyncedNativeTheme) {
    return;
  }
  lastSyncedNativeTheme = effective;
  setAppearanceTheme(effective).catch(() => {});
}

function bindSystemThemeMedia() {
  if (typeof window === "undefined" || typeof window.matchMedia !== "function") {
    return;
  }
  let media;
  try {
    media = window.matchMedia("(prefers-color-scheme: dark)");
  } catch (_error) {
    return;
  }
  const onChange = () => {
    if (normalizeTheme(appState.appearanceTheme) === "system") {
      applyAppearanceTheme(appState.appearanceTheme);
    }
  };
  if (typeof media.addEventListener === "function") {
    media.addEventListener("change", onChange);
    return;
  }
  if (typeof media.addListener === "function") {
    media.addListener(onChange);
  }
}

watchSyncEffect(() => {
  applyAppearanceTheme(appState.appearanceTheme);
});
bindSystemThemeMedia();

watchSyncEffect(() => {
  if (!canUseLocalStorage()) {
    return;
  }
  try {
    window.localStorage.setItem(
      APP_STATE_STORAGE_KEY,
      JSON.stringify({
        ...buildConfigPayloadFromState(),
        serviceRunning: appState.serviceRunning,
        backendRunning: appState.backendRunning,
        proxyRunning: appState.proxyRunning,
        serviceLastError: appState.serviceLastError,
        serviceListenAddr: appState.serviceListenAddr,
        configBackendListenAddr: appState.configBackendListenAddr,
        configProxyListenAddr: appState.configProxyListenAddr,
        backendListenAddr: appState.backendListenAddr,
        proxyListenAddr: appState.proxyListenAddr,
        cursorSettingsApplied: appState.cursorSettingsApplied,
        netProxySource: appState.netProxySource,
        netProxyActive: appState.netProxyActive,
        netProxyUsingSystem: appState.netProxyUsingSystem,
        netProxyUsingEnv: appState.netProxyUsingEnv,
        netProxyHttp: appState.netProxyHttp,
        netProxyHttps: appState.netProxyHttps,
        netProxyPacIgnored: appState.netProxyPacIgnored,
        netProxyDescription: appState.netProxyDescription,
        gatewayRunning: appState.gatewayRunning,
        gatewayRuntimeListenAddr: appState.gatewayRuntimeListenAddr,
        gatewayLastError: appState.gatewayLastError,
      }),
    );
  } catch (_error) {
    // ignore local persistence failures
  }
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(PROXY_STATE_EVENT, handleProxyStateEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(USER_CONFIG_CHANGED_EVENT, handleUserConfigChangedEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(MODEL_ADAPTER_TEST_UPDATED_EVENT, handleModelAdapterTestUpdatedEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(UPDATE_STATE_EVENT, handleUpdateStateEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(UPDATE_PROGRESS_EVENT, handleUpdateProgressEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(UPDATE_READY_EVENT, handleUpdateReadyEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

watchSyncEffect((onCleanup) => {
  if (typeof window === "undefined") {
    return;
  }
  const unsubscribe = Events.On(UPDATE_ERROR_EVENT, handleUpdateErrorEvent);
  onCleanup(() => {
    unsubscribe();
  });
});

export const appViewState = reactive({
  serviceStatusText: computed(() => {
    if (appState.proxyRunning && appState.backendRunning) {
      return "服务运行中";
    }
    if (appState.backendRunning) {
      return "后端已启动，代理未启动";
    }
    return "服务未启动";
  }),
  serviceStatusClass: computed(() =>
    appState.serviceRunning ? "text-[var(--color-success-text)]" : "text-[var(--color-warning-text)]",
  ),
  serviceButtonText: computed(() => {
    if (appState.serviceBusy) {
      return appState.serviceRunning ? "关闭中..." : "启动中...";
    }
    return appState.serviceRunning ? "关闭服务" : "启动服务";
  }),
});

function localizeUpdateMessage(msg) {
  if (!msg) return "";
  if (/当前已是最新版本/.test(msg)) {
    const match = msg.match(/v?([0-9]+\.[0-9]+\.[0-9]+)/);
    const version = match ? match[1] : appState.appVersion || "...";
    return `当前已是最新版本（v${version}）。`;
  }
  return msg;
}

function localizeReadyContent() {
  const version = appState.updateVersion || appState.appVersion || "...";
  const date = formatReleaseDate(appState.updateReleaseDate);
  const notes = appState.updateReleaseNotes || "";
  const kind = appState.updatePromptKind;

  const actionHint = (() => {
    if (kind === "available") return appState.updateMandatory
      ? "该版本为必要更新，但下载仍需确认。"
      : "确认后才会开始下载；安装仍需再次确认。";
    if (kind === "ready") return "确认后将重启并安装此更新。";
    return "";
  })();

  return [
    `版本：v${version}`,
    `发布时间：${date}`,
    actionHint,
    "",
    notes || "无更新说明",
  ].join("\n");
}

export const updateViewState = reactive({
  footerDownloading: computed(() => appState.updateState === "downloading"),
  footerBusy: computed(() => ["checking", "installing"].includes(appState.updateState)),
  footerVersionLabel: computed(() => `v${appState.appVersion || "..."}`),
  footerProgressText: computed(() => `${Math.round(appState.updateProgressPercent || 0)}%`),
  footerProgressStyle: computed(() => ({
    width: `${Math.max(0, Math.min(100, appState.updateProgressPercent || 0))}%`,
  })),
  promptTitle: computed(() => {
    switch (appState.updatePromptKind) {
      case "available":
        return appState.updateMandatory ? "发现必要更新" : "发现新版本";
      case "ready":
        return "更新已下载";
      case "error":
        return "更新失败";
      default:
        return "检查更新";
    }
  }),
  promptContent: computed(() => {
    switch (appState.updatePromptKind) {
      case "available":
      case "ready":
        return localizeReadyContent();
      case "error":
        return appState.updateError || localizeUpdateMessage(appState.updateMessage) || GENERIC_SERVICE_ERROR;
      default:
        return localizeUpdateMessage(appState.updateMessage) || localizeUpdateMessage(`当前已是最新版本（v${appState.appVersion || "..."}）。`);
    }
  }),
  promptConfirmText: computed(() => {
    if (appState.updatePromptKind === "available") {
      return "下载更新";
    }
    if (appState.updatePromptKind === "ready") {
      return "立即重启更新";
    }
    return "确定";
  }),
  promptCancelText: computed(() => {
    if (appState.updatePromptKind === "available") {
      return "暂不下载";
    }
    if (appState.updatePromptKind === "ready") {
      return "稍后";
    }
    return "取消";
  }),
  promptShowCancel: computed(() => ["available", "ready"].includes(appState.updatePromptKind)),
});

export function getModelAdapterTestResultByID(adapterID) {
  const id = asString(adapterID);
  if (!id) {
    return null;
  }
  return appState.modelAdapterTestResults[id] ?? null;
}

export function getModelAdapterTestResult(adapter) {
  const normalized = normalizeModelAdapter(adapter);
  if (normalized.id && appState.modelAdapterTestResults[normalized.id]) {
    return appState.modelAdapterTestResults[normalized.id];
  }
  const requestHash = buildModelAdapterTestRequestHash(normalized);
  return Object.values(appState.modelAdapterTestResults).find((result) => result.requestHash === requestHash) ?? null;
}

export function isModelAdapterTestResultRunning(adapter) {
  return getModelAdapterTestResult(adapter)?.status === "running";
}

export function isModelAdapterTestResultStale(adapter, result) {
  if (!result || !result.requestHash) {
    return false;
  }
  return result.requestHash !== buildModelAdapterTestRequestHash(adapter);
}

export async function refreshModelAdapterTestResults() {
  const results = await getModelAdapterTestResults();
  applyModelAdapterTestResults(results);
  return Object.values(appState.modelAdapterTestResults);
}

export function startModelAdapterTest(adapter) {
  const normalized = normalizeModelAdapter(adapter);
  if (!shouldTestModelAdapterEndpoint(normalized)) {
    return Promise.reject(new Error(LOGICAL_ROUTING_RUNTIME_VERIFY_HINT));
  }
  const validationError = validateModelAdapters([normalized]);
  if (validationError) {
    return Promise.reject(new Error(validationError));
  }
  return testModelAdapter(normalized).then((rawResult) => {
    const result = normalizeModelAdapterTestResult(rawResult);
    if (result.adapterID) {
      appState.modelAdapterTestResults = {
        ...appState.modelAdapterTestResults,
        [result.adapterID]: result,
      };
    }
    return result;
  });
}

export async function runModelAdapterTest(adapter) {
  return startModelAdapterTest(adapter);
}

export async function persistUserConfig() {
  const currentConfig = await loadPersistedUserConfig();
  return persistConfigPayload({
    ...currentConfig,
    observability: buildObservabilityConfigFromState(appState),
    modelAdapters: normalizeModelAdapters(appState.modelAdapters),
    routing: {
      mode: appState.routingMode,
    },
    homeMetrics: {
      ...currentConfig.homeMetrics,
      includeCacheWriteInHitRate: appState.includeCacheWriteInHitRate,
    },
    appearance: {
      theme: normalizeTheme(appState.appearanceTheme),
    },
    advertising: {
      enabled: asBoolean(appState.advertisingEnabled),
    },
    updates: {
      checkOnStartup: asBoolean(appState.updateCheckOnStartup),
    },
    gateway: {
      enabled: appState.gatewayEnabled,
      listenAddr: appState.gatewayListenAddr,
      tokenConfigured: appState.gatewayTokenConfigured,
      publicModels: appState.gatewayPublicModels,
    },
  });
}

export async function persistScopedUserConfig(scope) {
  return persistScopedConfig(scope);
}

export async function exportUserConfigToFile(path) {
  return exportUserConfigFile(path);
}

export async function importUserConfigFromFile(path) {
  if (appState.serviceRunning || appState.backendRunning || appState.proxyRunning) {
    throw new Error("服务运行中不能导入完整配置，请先停止服务");
  }
  if (appState.configSaving) {
    throw new Error("已有配置操作正在进行，请稍后再试");
  }
  appState.configSaving = true;
  try {
    const imported = normalizeConfig(await importUserConfigFile(path));
    applyConfigToState(imported);
    return imported;
  } finally {
    appState.configSaving = false;
  }
}

export async function saveIncludeCacheWriteInHitRate(value) {
  if (appState.configSaving) {
    return { ok: false, error: "已有配置操作正在进行，请稍后再试" };
  }
  const previousValue = appState.includeCacheWriteInHitRate;
  const nextValue = asBoolean(value);
  appState.includeCacheWriteInHitRate = nextValue;
  appState.configSaving = true;
  try {
    await saveHomeMetrics(buildConfigPayloadFromState());
    const persisted = await loadPersistedUserConfig();
    applyConfigToState(persisted, { savedScope: "" });
    return { ok: true, error: "" };
  } catch (error) {
    appState.includeCacheWriteInHitRate = previousValue;
    return { ok: false, error: toUserError(error) };
  } finally {
    appState.configSaving = false;
  }
}

export async function saveRoutingMode(mode) {
  const currentConfig = await loadPersistedUserConfig();
  return persistConfigPayload({
    ...currentConfig,
    routing: {
      mode: normalizeRouteMode(mode),
    },
  });
}

export async function reloadUserConfig(options = {}) {
  const config = await loadPersistedUserConfig();
  applyConfigToState(config, options);
  return config;
}

export async function saveModelAdapterAt(index, adapter) {
  const nextAdapters = normalizeModelAdapters(appState.modelAdapters);
  const nextAdapter = normalizeModelAdapter(adapter);
  const targetIndex = index >= 0 && index < nextAdapters.length ? index : nextAdapters.length;

  if (index >= 0 && index < nextAdapters.length) {
    nextAdapters.splice(index, 1, nextAdapter);
  } else {
    nextAdapters.push(nextAdapter);
  }

  const currentConfig = await loadPersistedUserConfig();
  const result = await persistConfigPayload(
    {
      ...currentConfig,
      modelAdapters: nextAdapters,
    },
    { modelAdaptersOnly: true },
  );
  if (!result.ok) {
    return result;
  }
  return {
    ...result,
    index: targetIndex,
    adapter: appState.modelAdapters[targetIndex] ?? null,
  };
}

export async function fetchAvailableModelIDs(payload) {
  const source = payload && typeof payload === "object" ? payload : {};
  const credentialSource = normalizeCredentialSource(source.credentialSource);
  const result = await fetchModelAdapterModels({
    type: asString(source.type),
    baseURL: asString(source.baseURL),
    apiKey: isManagedCredentialSource(credentialSource) ? "" : asString(source.apiKey),
    credentialSource,
    customHeadersEnabled: asBoolean(source.customHeadersEnabled),
    customHeadersJSON: asString(source.customHeadersJSON),
  });
  return asArray(result?.models)
    .map((item) => asString(item))
    .filter(Boolean);
}

export function deleteModelAdapterAt(index) {
  const nextAdapters = normalizeModelAdapters(appState.modelAdapters);

  if (index < 0 || index >= nextAdapters.length) {
    return {
      ok: false,
      error: "模型配置不存在，无法删除",
    };
  }

  nextAdapters.splice(index, 1);
  appState.modelAdapters = normalizeModelAdapters(nextAdapters);
  return { ok: true, error: "" };
}

export async function saveModelAdapterOrder(adapterIDs) {
  const orderedIDs = asArray(adapterIDs)
    .map((item) => asString(item))
    .filter(Boolean);
  const currentConfig = await loadPersistedUserConfig();
  const currentAdapters = normalizeModelAdapters(currentConfig.modelAdapters);
  const adaptersByID = new Map(currentAdapters.map((adapter) => [adapter.id, adapter]));
  const uniqueIDs = new Set(orderedIDs);

  if (
    orderedIDs.length !== currentAdapters.length
    || uniqueIDs.size !== currentAdapters.length
    || orderedIDs.some((id) => !adaptersByID.has(id))
  ) {
    return {
      ok: false,
      error: "模型配置已发生变化，请刷新后重试",
    };
  }

  const nextAdapters = orderedIDs.map((id, index) => ({
    ...adaptersByID.get(id),
    sort: index + 1,
  }));
  return persistConfigPayload(
    {
      ...currentConfig,
      modelAdapters: nextAdapters,
    },
    { modelAdaptersOnly: true },
  );
}

function splitDisplayNameSeed(value) {
  const text = asString(value);
  const match = text.match(/^(.*?)(?:\s*[-+](\d+))?$/);
  if (!match) {
    return { base: text || "模型", number: 0 };
  }
  const base = asString(match[1]) || "模型";
  const number = match[2] ? Number(match[2]) : 0;
  return { base, number: Number.isFinite(number) ? number : 0 };
}

function buildNextDisplayName(existingAdapters, sourceName) {
  const { base } = splitDisplayNameSeed(sourceName);
  let next = 1;
  const taken = new Set(
    normalizeModelAdapters(existingAdapters)
      .map((adapter) => adapter.displayName)
      .filter(Boolean),
  );

  while (taken.has(`${base}-${next}`)) {
    next += 1;
  }
  return `${base}-${next}`;
}

export function duplicateModelAdapterAt(index) {
  const nextAdapters = normalizeModelAdapters(appState.modelAdapters);

  if (index < 0 || index >= nextAdapters.length) {
    return {
      ok: false,
      error: "模型配置不存在，无法复制",
    };
  }

  const source = normalizeModelAdapter(nextAdapters[index]);
  const duplicate = {
    ...source,
    id: "",
    displayName: buildNextDisplayName(nextAdapters, source.displayName || source.modelID || "模型"),
  };

  nextAdapters.splice(index + 1, 0, duplicate);
  appState.modelAdapters = normalizeModelAdapters(nextAdapters);
  return { ok: true, error: "" };
}

function normalizeLogCaptureStatus(source) {
  const raw = source && typeof source === "object" ? source : {};
  return {
    enabled: asBoolean(raw.enabled),
    mode: asString(raw.mode) || "basic",
    sessionId: asString(raw.sessionId ?? raw.session_id),
    sessionPath: asString(raw.sessionPath ?? raw.session_path),
    logsRoot: asString(raw.logsRoot ?? raw.logs_root),
    payloadDegraded: asBoolean(raw.payloadDegraded ?? raw.payload_degraded),
    droppedEvents: Math.max(0, Math.round(asNumber(raw.droppedEvents ?? raw.dropped_events))),
    lastError: asString(raw.lastError ?? raw.last_error),
  };
}

export async function syncLogCaptureStatus() {
  appState.logCaptureLoading = true;
  try {
    const status = normalizeLogCaptureStatus(await getLogCaptureStatus());
    appState.logCaptureStatus = status;
    return status;
  } finally {
    appState.logCaptureLoading = false;
  }
}

export async function clearClosedLogSessions() {
  if (appState.logCleanupRunning) {
    return { ok: false, error: "日志清理正在进行" };
  }
  appState.logCleanupRunning = true;
  try {
    const raw = await cleanupClosedLogSessions();
    await syncLogCaptureStatus();
    return {
      ok: true,
      error: "",
      removedSessions: Math.max(0, Math.round(asNumber(raw?.removedSessions ?? raw?.removed_sessions))),
      freedBytes: Math.max(0, Math.round(asNumber(raw?.freedBytes ?? raw?.freed_bytes))),
    };
  } catch (error) {
    return { ok: false, error: toUserError(error), removedSessions: 0, freedBytes: 0 };
  } finally {
    appState.logCleanupRunning = false;
  }
}

export async function syncServiceState() {
  const state = await getProxyState();
  applyProxyState(state);
  return state;
}

export async function syncHomeMetrics() {
  const startedAt = Date.now();
  appState.homeMetricsLoading = true;
  try {
    const summary = await getHomeMetricsSummary();
    applyHomeMetrics(summary);
    return {
      ok: true,
      error: "",
    };
  } catch (error) {
    appState.homeMetricsError = toUserError(error);
    return {
      ok: false,
      error: appState.homeMetricsError,
    };
  } finally {
    const elapsed = Date.now() - startedAt;
    if (elapsed < HOME_METRICS_MIN_LOADING_MS) {
      await delay(HOME_METRICS_MIN_LOADING_MS - elapsed);
    }
    appState.homeMetricsLoading = false;
  }
}

export async function syncHomeMetricsReport(range = appState.homeMetricsRange) {
  appState.homeMetricsReportLoading = true;
  appState.homeMetricsReportError = "";
  try {
    const report = normalizeHomeMetricsReport(await getHomeMetricsReport(range));
    appState.homeMetricsRange = report.range;
    appState.homeMetricsReport = report;
    applyHomeMetrics(report.summary);
    return { ok: true, error: "", report };
  } catch (error) {
    appState.homeMetricsReportError = toUserError(error);
    appState.homeMetricsReport = createEmptyHomeMetricsReport(
      asString(range) || appState.homeMetricsRange || "all",
    );
    return { ok: false, error: appState.homeMetricsReportError, report: appState.homeMetricsReport };
  } finally {
    appState.homeMetricsReportLoading = false;
  }
}

export async function resetHomeMetrics() {
  if (appState.homeMetricsResetting) {
    return { ok: false, error: "统计重置正在进行" };
  }
  appState.homeMetricsResetting = true;
  try {
    await resetHomeMetricsSummary();
    await syncHomeMetrics();
    return await syncHomeMetricsReport();
  } catch (error) {
    appState.homeMetricsError = toUserError(error);
    return {
      ok: false,
      error: appState.homeMetricsError,
    };
  } finally {
    appState.homeMetricsResetting = false;
  }
}

export async function startService() {
  if (appState.serviceBusy) {
    return { ok: false, error: "服务状态更新中，请稍后再试" };
  }
  if (isConfigSectionDirty("cursor")) {
    return { ok: false, error: "请先保存本页" };
  }
  appState.serviceBusy = true;
  try {
    const state = await startProxyService();
    applyProxyState(state);
    return { ok: true, error: "" };
  } catch (error) {
    await syncServiceState().catch(() => {});
    return { ok: false, error: toUserError(error) };
  } finally {
    appState.serviceBusy = false;
  }
}

export async function stopService() {
  if (appState.serviceBusy) {
    return { ok: false, error: "服务状态更新中，请稍后再试" };
  }
  appState.serviceBusy = true;
  try {
    const state = await stopProxyService();
    applyProxyState(state);
    return { ok: true, error: "" };
  } catch (error) {
    await syncServiceState().catch(() => {});
    return { ok: false, error: toUserError(error) };
  } finally {
    appState.serviceBusy = false;
  }
}

export async function startGateway() {
  if (appState.gatewayBusy) {
    return { ok: false, error: "Gateway 状态更新中，请稍后再试" };
  }
  if (isConfigSectionDirty("gateway")) {
    return { ok: false, error: "请先保存本页" };
  }
  appState.gatewayBusy = true;
  try {
    const state = await startGatewayService();
    applyProxyState(state);
    return { ok: true, error: "" };
  } catch (error) {
    await syncServiceState().catch(() => {});
    return { ok: false, error: toUserError(error) };
  } finally {
    appState.gatewayBusy = false;
  }
}

export async function stopGateway() {
  if (appState.gatewayBusy) {
    return { ok: false, error: "Gateway 状态更新中，请稍后再试" };
  }
  appState.gatewayBusy = true;
  try {
    const state = await stopGatewayService();
    applyProxyState(state);
    return { ok: true, error: "" };
  } catch (error) {
    await syncServiceState().catch(() => {});
    return { ok: false, error: toUserError(error) };
  } finally {
    appState.gatewayBusy = false;
  }
}

export async function testGateway() {
  if (!appState.gatewayRunning) {
    return { ok: false, error: "Gateway 未运行" };
  }
  try {
    const result = await testGatewayService();
    return { ok: true, result, error: "" };
  } catch (error) {
    await syncServiceState().catch(() => {});
    return { ok: false, error: toUserError(error) };
  }
}

export async function toggleService() {
  if (appState.serviceRunning) {
    return stopService();
  }
  return startService();
}

export async function openLocalLogsDirectory() {
  await openLogsDirectory();
}

export async function openConfigWindow() {
  await openConfig();
}

export async function openModelConfigWindow() {
  await openModelConfig();
}

export async function checkForAppUpdates() {
  await checkForUpdates();
}

export function dismissUpdatePrompt() {
  appState.updatePromptVisible = false;
  appState.updatePromptBusy = false;
}

export async function confirmUpdatePrompt() {
  if (appState.updatePromptBusy) {
    return;
  }

  // idle / error：仅关闭提示
  if (!["available", "ready"].includes(appState.updatePromptKind)) {
    dismissUpdatePrompt();
    return;
  }

  appState.updatePromptBusy = true;
  try {
    if (appState.updatePromptKind === "available") {
      // 第一次确认：只下载，不安装
      await downloadAvailableUpdate();
      dismissUpdatePrompt();
      return;
    }
    // 第二次确认：重启安装
    await installReadyUpdate();
  } catch (error) {
    appState.updatePromptBusy = false;
    const message = toUserError(error);
    appState.updateError = message;
    openUpdatePrompt("error", { error: message });
  }
}

export function toUserError(error) {
  const message = extractErrorMessage(error);
  return message || GENERIC_SERVICE_ERROR;
}

export async function copyGatewayToken() {
  return copyGatewayTokenApi();
}

export async function rotateGatewayToken() {
  const token = await rotateGatewayTokenApi();
  appState.gatewayTokenConfigured = true;
  return token;
}

export async function bootstrapAppState() {
  try {
    await reloadUserConfig();
  } catch (_error) {
    // keep cached config if loading fails
  }
  await refreshModelAdapterTestResults().catch(() => {});
  try {
    appState.appVersion = await getAppVersion();
  } catch (_error) {
    appState.appVersion = "";
  }
  await syncServiceState().catch(() => {});
  await syncHomeMetricsReport().catch(() => {});
}
