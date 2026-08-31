const SUPPORTED_THEMES = new Set(["light", "dark", "system"]);
const SUPPORTED_OBSERVABILITY_MODES = new Set(["off", "basic", "full"]);

export const DEFAULT_OBSERVABILITY_CONFIG = Object.freeze({
  mode: "basic",
  retentionDays: 7,
  maxDiskMB: 1024,
});

export const OBSERVABILITY_LIMITS = Object.freeze({
  retentionDays: Object.freeze({ min: 1, max: 90 }),
  maxDiskMB: Object.freeze({ min: 64, max: 10240 }),
});

export const DEFAULT_CLIENT_PREFERENCES = Object.freeze({
  appearance: { theme: "light" },
  advertising: { enabled: false },
  updates: { checkOnStartup: false },
});

export const DEFAULT_SUBAGENT_RESCHEDULE = Object.freeze({
  enabled: false,
});

export function normalizeSubagentRescheduleConfig(_source) {
  // Runtime relaunch 尚无稳定 typed failure 关联。前端投影固定关闭，
  // 避免加载或保存未来兼容字段时误启用当前未接线能力。
  return {
    enabled: DEFAULT_SUBAGENT_RESCHEDULE.enabled,
  };
}

export function buildSubagentRescheduleConfigFromState(_source = {}) {
  return {
    enabled: DEFAULT_SUBAGENT_RESCHEDULE.enabled,
  };
}

export const DEFAULT_PROVIDER_FALLBACK = Object.freeze({
  enabled: false,
  primaryChannelID: "",
  candidateChannelIDs: Object.freeze([]),
  maxHttpAttempts: 5,
  maxWaitSeconds: 8,
});

export const MAX_PROVIDER_FALLBACK_CANDIDATES = 4;

export const PROVIDER_FALLBACK_LIMITS = Object.freeze({
  maxHttpAttempts: Object.freeze({ min: 2, max: 9 }),
  maxWaitSeconds: Object.freeze({ min: 1, max: 30 }),
});

export const DEFAULT_MAX_CONCURRENT_REQUESTS = 0;

export const MAX_CONCURRENT_REQUESTS_LIMITS = Object.freeze({
  min: 1,
  max: 16,
});

export const LOGICAL_ROUTING_RUNTIME_VERIFY_HINT = "已保存。逻辑路由 alias 自身不会调用虚拟 endpoint，整条 fallback 链需通过实际运行验证。";

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
  return ["true", "1", "yes"].includes(asString(value).toLowerCase());
}

export function normalizeTheme(value) {
  const theme = asString(value).toLowerCase();
  return SUPPORTED_THEMES.has(theme) ? theme : DEFAULT_CLIENT_PREFERENCES.appearance.theme;
}

export function resolveEffectiveTheme(value, prefersDark = false) {
  const theme = normalizeTheme(value);
  if (theme === "dark") {
    return "dark";
  }
  if (theme === "system") {
    return prefersDark ? "dark" : "light";
  }
  return "light";
}

function boundedInteger(value, fallback, { min, max }) {
  const parsed = Number(asString(value));
  if (!Number.isInteger(parsed) || parsed <= 0) {
    return fallback;
  }
  return Math.min(max, Math.max(min, parsed));
}

function parseBudgetNumber(value) {
  if (value === null || value === undefined) {
    return 0;
  }
  if (typeof value === "number") {
    return Number.isFinite(value) ? value : Number.NaN;
  }
  const text = String(value).trim();
  if (!text) {
    return 0;
  }
  const parsed = Number(text);
  return Number.isFinite(parsed) ? parsed : Number.NaN;
}

function defaultBudget(value, fallback) {
  return value === 0 ? fallback : value;
}

export function normalizeObservabilityConfig(source, legacyLog = false) {
  const hasObservability = Boolean(source && typeof source === "object" && !Array.isArray(source));
  const raw = hasObservability ? source : {};
  const requestedMode = asString(raw.mode).toLowerCase();
  const mode = SUPPORTED_OBSERVABILITY_MODES.has(requestedMode)
    ? requestedMode
    : (!hasObservability && legacyLog !== undefined
      ? (asBoolean(legacyLog) ? "full" : "off")
      : DEFAULT_OBSERVABILITY_CONFIG.mode);
  return {
    mode,
    retentionDays: boundedInteger(
      raw.retentionDays,
      DEFAULT_OBSERVABILITY_CONFIG.retentionDays,
      OBSERVABILITY_LIMITS.retentionDays,
    ),
    maxDiskMB: boundedInteger(
      raw.maxDiskMB,
      DEFAULT_OBSERVABILITY_CONFIG.maxDiskMB,
      OBSERVABILITY_LIMITS.maxDiskMB,
    ),
  };
}

export function buildObservabilityConfigFromState(source = {}) {
  const raw = source && typeof source === "object" ? source : {};
  return normalizeObservabilityConfig({
    mode: raw.observabilityMode,
    retentionDays: raw.observabilityRetentionDays,
    maxDiskMB: raw.observabilityMaxDiskMB,
  });
}

export function normalizeClientPreferences(source = {}) {
  const raw = source && typeof source === "object" ? source : {};
  const appearance = raw.appearance && typeof raw.appearance === "object" ? raw.appearance : {};
  const advertising = raw.advertising && typeof raw.advertising === "object" ? raw.advertising : {};
  const updates = raw.updates && typeof raw.updates === "object" ? raw.updates : {};
  return {
    appearance: {
      theme: normalizeTheme(appearance.theme),
    },
    advertising: {
      enabled: asBoolean(advertising.enabled),
    },
    updates: {
      checkOnStartup: asBoolean(updates.checkOnStartup),
    },
  };
}

export function buildClientPreferencesFromState(source = {}) {
  const raw = source && typeof source === "object" ? source : {};
  return normalizeClientPreferences({
    appearance: { theme: raw.appearanceTheme },
    advertising: { enabled: raw.advertisingEnabled },
    updates: { checkOnStartup: raw.updateCheckOnStartup },
  });
}

// normalizeProviderFallback 归一化单条 providerFallback 配置（纯函数，无副作用）。
// 缺失/0 预算归一化为 5/8；非零越界原样保留供校验报错，禁止静默 clamp。
// 禁用时保留引用和预算字段，但不参与运行。
export function normalizeProviderFallback(source) {
  const raw = source && typeof source === "object" && !Array.isArray(source) ? source : {};
  const candidateSource = Array.isArray(raw.candidateChannelIDs)
    ? raw.candidateChannelIDs
    : (Array.isArray(raw.candidate_channel_ids) ? raw.candidate_channel_ids : []);
  return {
    enabled: asBoolean(raw.enabled),
    primaryChannelID: asString(raw.primaryChannelID ?? raw.primary_channel_id ?? ""),
    candidateChannelIDs: candidateSource.map((id) => asString(id)).filter(Boolean),
    maxHttpAttempts: defaultBudget(
      parseBudgetNumber(raw.maxHttpAttempts ?? raw.max_http_attempts),
      DEFAULT_PROVIDER_FALLBACK.maxHttpAttempts,
    ),
    maxWaitSeconds: defaultBudget(
      parseBudgetNumber(raw.maxWaitSeconds ?? raw.max_wait_seconds),
      DEFAULT_PROVIDER_FALLBACK.maxWaitSeconds,
    ),
  };
}

export function validateProviderFallbackBudget(source, prefix = "模型") {
  const fb = source && typeof source === "object" ? source : {};
  const attempts = fb.maxHttpAttempts;
  const wait = fb.maxWaitSeconds;
  if (
    !Number.isInteger(attempts)
    || attempts < PROVIDER_FALLBACK_LIMITS.maxHttpAttempts.min
    || attempts > PROVIDER_FALLBACK_LIMITS.maxHttpAttempts.max
  ) {
    return `${prefix} 的全链最大 HTTP 尝试次数必须为 2–9 的整数`;
  }
  if (
    !Number.isInteger(wait)
    || wait < PROVIDER_FALLBACK_LIMITS.maxWaitSeconds.min
    || wait > PROVIDER_FALLBACK_LIMITS.maxWaitSeconds.max
  ) {
    return `${prefix} 的全链最大等待秒数必须为 1–30 的整数`;
  }
  return "";
}

export function validateProviderFallbackAdapters(source, { allAdapters } = {}) {
  const adapters = Array.isArray(source) ? source : [];
  const refAdapters = Array.isArray(allAdapters) ? allAdapters : adapters;
  const adapterByID = new Map(
    refAdapters.map((item) => [asString(item?.id), item]).filter(([id]) => Boolean(id)),
  );
  const adapterIDSet = new Set(adapterByID.keys());

  for (const [index, adapter] of adapters.entries()) {
    const prefix = `模型 ${index + 1}`;
    const fb = normalizeProviderFallback(adapter?.providerFallback);
    const budgetError = validateProviderFallbackBudget(fb, prefix);
    if (budgetError) {
      return budgetError;
    }
    if (!fb.enabled) {
      continue;
    }
    if (!fb.primaryChannelID) {
      return `${prefix} 的 Fallback 主渠道 ID 不能为空`;
    }
    if (!adapterIDSet.has(fb.primaryChannelID)) {
      return `${prefix} 的 Fallback 主渠道 ID "${fb.primaryChannelID}" 引用了不存在的渠道`;
    }
    if (fb.primaryChannelID === asString(adapter?.id)) {
      return `${prefix} 的 Fallback 主渠道 ID 不能与当前渠道相同`;
    }
    if (isLogicalRoutingAdapter(adapterByID.get(fb.primaryChannelID))) {
      return `${prefix} 的 Fallback 主渠道必须是未启用 Fallback 的物理渠道`;
    }
    if (!fb.candidateChannelIDs.length || fb.candidateChannelIDs.length > MAX_PROVIDER_FALLBACK_CANDIDATES) {
      return `${prefix} 的 Fallback 候选渠道数量必须为 1–${MAX_PROVIDER_FALLBACK_CANDIDATES} 个`;
    }
    const seenInChain = new Set([fb.primaryChannelID, asString(adapter?.id)].filter(Boolean));
    for (const cid of fb.candidateChannelIDs) {
      if (!cid) {
        return `${prefix} 的 Fallback 候选渠道 ID 不能为空`;
      }
      if (!adapterIDSet.has(cid)) {
        return `${prefix} 的 Fallback 候选渠道 ID "${cid}" 引用了不存在的渠道`;
      }
      if (seenInChain.has(cid)) {
        return `${prefix} 的 Fallback 候选渠道包含重复或自引用 ID "${cid}"`;
      }
      if (isLogicalRoutingAdapter(adapterByID.get(cid))) {
        return `${prefix} 的 Fallback 候选渠道必须是未启用 Fallback 的物理渠道`;
      }
      seenInChain.add(cid);
    }
  }
  return "";
}

export function prepareModelAdaptersForPersist(source, validate = validateProviderFallbackAdapters) {
  const adaptersWithIds = (Array.isArray(source) ? source : []).map((adapter) => (
    adapter && typeof adapter === "object" ? { ...adapter } : {}
  ));
  const error = typeof validate === "function" ? (validate(adaptersWithIds) || "") : "";
  if (error) {
    return {
      ok: false,
      error,
      adaptersWithIds,
      payloadAdapters: null,
    };
  }
  return {
    ok: true,
    error: "",
    adaptersWithIds,
    // The backend uses the last persisted id as an identity hint while
    // recomputing derived channel IDs. New adapters keep an empty id.
    payloadAdapters: adaptersWithIds,
  };
}

export function isLogicalRoutingAdapter(source) {
  return Boolean(normalizeProviderFallback(source?.providerFallback).enabled);
}

export function shouldTestModelAdapterEndpoint(source) {
  return !isLogicalRoutingAdapter(source);
}

export function selectAdaptersForEndpointTest(source) {
  const adapters = Array.isArray(source) ? source : [];
  const toTest = [];
  const skippedLogical = [];
  for (const adapter of adapters) {
    if (shouldTestModelAdapterEndpoint(adapter)) {
      toTest.push(adapter);
    } else {
      skippedLogical.push(adapter);
    }
  }
  return { toTest, skippedLogical };
}

export function formatFallbackBudgetInput(value) {
  if (value === 0 || value === undefined || value === null || value === "") {
    return "";
  }
  if (typeof value === "number" && !Number.isFinite(value)) {
    return "";
  }
  const text = String(value);
  return text === "NaN" ? "" : text;
}

export function parseFallbackBudgetInput(value) {
  const text = String(value ?? "").trim();
  if (!text) {
    return 0;
  }
  if (!/^-?\d+$/.test(text)) {
    return text;
  }
  return Number(text);
}

export function providerFallbackBudgetFieldError(field, value) {
  if (value === null || value === undefined || value === "" || value === 0) {
    return "";
  }
  const parsed = parseBudgetNumber(value);
  if (field === "maxHttpAttempts") {
    if (!Number.isInteger(parsed) || parsed < 2 || parsed > 9) {
      return "全链最大 HTTP 尝试次数必须为 2–9 的整数";
    }
    return "";
  }
  if (field === "maxWaitSeconds") {
    if (!Number.isInteger(parsed) || parsed < 1 || parsed > 30) {
      return "全链最大等待秒数必须为 1–30 的整数";
    }
    return "";
  }
  return "";
}

export function normalizeMaxConcurrentRequests(value) {
  return parseBudgetNumber(value);
}

export function validateMaxConcurrentRequests(value, prefix = "模型") {
  const parsed = parseBudgetNumber(value);
  if (parsed === 0) {
    return "";
  }
  if (
    !Number.isInteger(parsed)
    || parsed < MAX_CONCURRENT_REQUESTS_LIMITS.min
    || parsed > MAX_CONCURRENT_REQUESTS_LIMITS.max
  ) {
    return `${prefix} 的上游并发上限必须为 0（不限制）或 1–16 的整数`;
  }
  return "";
}

export function maxConcurrentRequestsFieldError(value) {
  if (value === null || value === undefined || value === "" || value === 0) {
    return "";
  }
  const parsed = parseBudgetNumber(value);
  if (
    !Number.isInteger(parsed)
    || parsed < MAX_CONCURRENT_REQUESTS_LIMITS.min
    || parsed > MAX_CONCURRENT_REQUESTS_LIMITS.max
  ) {
    return "上游并发上限必须为 0（不限制）或 1–16 的整数";
  }
  return "";
}

function normalizeCapacityBaseURL(value) {
  const raw = asString(value);
  try {
    const parsed = new URL(raw);
    if (!["http:", "https:"].includes(parsed.protocol.toLowerCase()) || !parsed.host) {
      return raw.replace(/\/+$/, "");
    }
    const schemeEnd = raw.indexOf("://");
    const rest = raw.slice(schemeEnd + 3);
    const authorityEnd = rest.search(/[/?#]/);
    const authority = authorityEnd < 0 ? rest : rest.slice(0, authorityEnd);
    const suffix = authorityEnd < 0 ? "" : rest.slice(authorityEnd);
    const at = authority.lastIndexOf("@");
    const userInfo = at < 0 ? "" : authority.slice(0, at + 1);
    const host = (at < 0 ? authority : authority.slice(at + 1)).toLowerCase();
    return `${raw.slice(0, schemeEnd).toLowerCase()}://${userInfo}${host}${suffix}`.replace(/\/+$/, "");
  } catch {
    return raw.replace(/\/+$/, "");
  }
}

function upstreamCapacityGroupIdentity(adapter) {
  return [
    asString(adapter?.type).toLowerCase(),
    normalizeCapacityBaseURL(adapter?.baseURL),
    asString(adapter?.apiKey),
  ].join("\n");
}

function mergeAdaptersForCapacityValidation(source, allAdapters) {
  const adapters = Array.isArray(source) ? source : [];
  const refs = Array.isArray(allAdapters) ? allAdapters : [];
  if (!refs.length) {
    return adapters;
  }
  const merged = new Map();
  refs.forEach((item, index) => {
    merged.set(asString(item?.id) || `#ref${index}`, item);
  });
  adapters.forEach((item, index) => {
    const id = asString(item?.id);
    merged.set(id || `#src${index}`, item);
  });
  return [...merged.values()];
}

export function validateUpstreamCapacityAdapters(source, { allAdapters } = {}) {
  const adapters = Array.isArray(source) ? source : [];
  for (const [index, adapter] of adapters.entries()) {
    const prefix = `模型 ${index + 1}`;
    const rangeError = validateMaxConcurrentRequests(adapter?.maxConcurrentRequests, prefix);
    if (rangeError) {
      return rangeError;
    }
    if (isLogicalRoutingAdapter(adapter) && parseBudgetNumber(adapter?.maxConcurrentRequests) !== 0) {
      return `${prefix} 是逻辑路由 alias，上游并发上限必须为 0`;
    }
  }
  const groups = new Map();
  for (const adapter of mergeAdaptersForCapacityValidation(adapters, allAdapters)) {
    if (isLogicalRoutingAdapter(adapter)) {
      continue;
    }
    const key = upstreamCapacityGroupIdentity(adapter);
    const limit = parseBudgetNumber(adapter?.maxConcurrentRequests);
    if (!groups.has(key)) {
      groups.set(key, limit);
      continue;
    }
    if (groups.get(key) !== limit) {
      return "同一接口地址和 API Key 的物理渠道必须使用相同的上游并发上限";
    }
  }
  return "";
}

export const DEFAULT_GATEWAY_LISTEN_ADDR = "127.0.0.1:18091";
export const MAX_GATEWAY_PUBLIC_MODELS = 32;

export const DEFAULT_GATEWAY_CONFIG = Object.freeze({
  enabled: false,
  listenAddr: DEFAULT_GATEWAY_LISTEN_ADDR,
  tokenConfigured: false,
  publicModels: Object.freeze([]),
});

export function normalizeGatewayConfig(source) {
  const raw = source && typeof source === "object" && !Array.isArray(source) ? source : {};
  const models = Array.isArray(raw.publicModels) ? raw.publicModels : [];
  const publicModels = [];
  const seen = new Set();
  for (const item of models) {
    const id = asString(item?.id);
    const targetAdapterID = asString(item?.targetAdapterID);
    if (!id || seen.has(id)) {
      continue;
    }
    seen.add(id);
    publicModels.push({ id, targetAdapterID });
    if (publicModels.length >= MAX_GATEWAY_PUBLIC_MODELS) {
      break;
    }
  }
  return {
    enabled: asBoolean(raw.enabled),
    listenAddr: asString(raw.listenAddr) || DEFAULT_GATEWAY_LISTEN_ADDR,
    tokenConfigured: asBoolean(raw.tokenConfigured),
    publicModels,
  };
}

export function gatewayPublicModelInvalid(model, adapters) {
  const target = asString(model?.targetAdapterID);
  if (!target) {
    return true;
  }
  const list = Array.isArray(adapters) ? adapters : [];
  return !list.some((adapter) => asString(adapter?.id) === target);
}
