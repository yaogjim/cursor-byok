import { readFileSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import {
  buildClientPreferencesFromState,
  buildObservabilityConfigFromState,
  DEFAULT_PROVIDER_FALLBACK,
  formatFallbackBudgetInput,
  isLogicalRoutingAdapter,
  LOGICAL_ROUTING_RUNTIME_VERIFY_HINT,
  normalizeClientPreferences,
  normalizeObservabilityConfig,
  normalizeProviderFallback,
  parseFallbackBudgetInput,
  prepareModelAdaptersForPersist,
  providerFallbackBudgetFieldError,
  PROVIDER_FALLBACK_LIMITS,
  selectAdaptersForEndpointTest,
  shouldTestModelAdapterEndpoint,
  stripDerivedModelAdapterIDs,
  validateProviderFallbackAdapters,
  validateProviderFallbackBudget,
  DEFAULT_MAX_CONCURRENT_REQUESTS,
  MAX_CONCURRENT_REQUESTS_LIMITS,
  maxConcurrentRequestsFieldError,
  normalizeMaxConcurrentRequests,
  validateMaxConcurrentRequests,
  validateUpstreamCapacityAdapters,
} from "../src/state/configProjection.js";

function assertEqual(actual, expected, label) {
  const left = JSON.stringify(actual);
  const right = JSON.stringify(expected);
  if (left !== right) {
    throw new Error(`${label}: ${left} !== ${right}`);
  }
}

function assert(condition, label) {
  if (!condition) {
    throw new Error(label);
  }
}

const projected = buildClientPreferencesFromState({
  appearanceTheme: "dark",
  advertisingEnabled: true,
  updateCheckOnStartup: true,
});
const expected = {
  appearance: { theme: "dark" },
  advertising: { enabled: true },
  updates: { checkOnStartup: true },
};
assertEqual(projected, expected, "state projection mismatch");

const defaults = normalizeClientPreferences({
  appearance: { theme: "unsupported" },
  advertising: {},
  updates: {},
});
const expectedDefaults = {
  appearance: { theme: "light" },
  advertising: { enabled: false },
  updates: { checkOnStartup: false },
};
assertEqual(defaults, expectedDefaults, "preference defaults mismatch");

const legacyFull = normalizeObservabilityConfig(undefined, true);
if (legacyFull.mode !== "full") {
  throw new Error(`legacy log=true mode mismatch: ${JSON.stringify(legacyFull)}`);
}

const boundedObservability = normalizeObservabilityConfig({
  mode: " FULL ",
  retentionDays: 100,
  maxDiskMB: 1,
});
const expectedBoundedObservability = {
  mode: "full",
  retentionDays: 90,
  maxDiskMB: 64,
};
assertEqual(boundedObservability, expectedBoundedObservability, "observability bounds mismatch");

const explicitInvalidMode = normalizeObservabilityConfig({ mode: "unsupported" }, true);
if (explicitInvalidMode.mode !== "basic") {
  throw new Error(`explicit observability must override legacy log: ${JSON.stringify(explicitInvalidMode)}`);
}

const projectedObservability = buildObservabilityConfigFromState({
  observabilityMode: "full",
  observabilityRetentionDays: "30",
  observabilityMaxDiskMB: "2048",
});
const expectedProjectedObservability = {
  mode: "full",
  retentionDays: 30,
  maxDiskMB: 2048,
};
assertEqual(projectedObservability, expectedProjectedObservability, "observability projection mismatch");

// ── providerFallback 归一化 / 预算合同 ──

assertEqual(DEFAULT_PROVIDER_FALLBACK.maxHttpAttempts, 5, "default maxHttpAttempts");
assertEqual(DEFAULT_PROVIDER_FALLBACK.maxWaitSeconds, 8, "default maxWaitSeconds");
assertEqual(PROVIDER_FALLBACK_LIMITS.maxHttpAttempts, { min: 2, max: 9 }, "attempt limits");
assertEqual(PROVIDER_FALLBACK_LIMITS.maxWaitSeconds, { min: 1, max: 30 }, "wait limits");

const fbOff = normalizeProviderFallback(null);
assertEqual(fbOff.enabled, false, "null fallback enabled");
assertEqual(fbOff.primaryChannelID, "", "null fallback primary");
assertEqual(fbOff.candidateChannelIDs, [], "null fallback candidates");
assertEqual(fbOff.maxHttpAttempts, 5, "null fallback attempts default");
assertEqual(fbOff.maxWaitSeconds, 8, "null fallback wait default");

const fbDisabled = normalizeProviderFallback({
  enabled: false,
  primaryChannelID: "ch1",
  candidateChannelIDs: ["ch2"],
  maxHttpAttempts: 7,
  maxWaitSeconds: 12,
});
assertEqual(fbDisabled.enabled, false, "disabled keeps enabled=false");
assertEqual(fbDisabled.primaryChannelID, "ch1", "disabled keeps primaryChannelID");
assertEqual(fbDisabled.candidateChannelIDs, ["ch2"], "disabled keeps candidateChannelIDs");
assertEqual(fbDisabled.maxHttpAttempts, 7, "disabled keeps maxHttpAttempts");
assertEqual(fbDisabled.maxWaitSeconds, 12, "disabled keeps maxWaitSeconds");

const fbEnabled = normalizeProviderFallback({
  enabled: true,
  primaryChannelID: "  ch-primary  ",
  candidateChannelIDs: ["ch-cand1", "", "ch-cand2"],
});
assertEqual(fbEnabled.enabled, true, "enabled flag");
assertEqual(fbEnabled.primaryChannelID, "ch-primary", "primary trim");
assertEqual(fbEnabled.candidateChannelIDs, ["ch-cand1", "ch-cand2"], "empty candidates filtered");
assertEqual(fbEnabled.maxHttpAttempts, 5, "missing attempts default 5");
assertEqual(fbEnabled.maxWaitSeconds, 8, "missing wait default 8");

const fbZeroBudget = normalizeProviderFallback({
  enabled: true,
  primaryChannelID: "ch-a",
  candidateChannelIDs: ["ch-b"],
  maxHttpAttempts: 0,
  maxWaitSeconds: 0,
});
assertEqual(fbZeroBudget.maxHttpAttempts, 5, "zero attempts default 5");
assertEqual(fbZeroBudget.maxWaitSeconds, 8, "zero wait default 8");

const fbLegalBounds = normalizeProviderFallback({
  enabled: true,
  primaryChannelID: "ch-a",
  candidateChannelIDs: ["ch-b"],
  maxHttpAttempts: 2,
  maxWaitSeconds: 1,
});
assertEqual(fbLegalBounds.maxHttpAttempts, 2, "legal min attempts preserved");
assertEqual(fbLegalBounds.maxWaitSeconds, 1, "legal min wait preserved");

const fbLegalMax = normalizeProviderFallback({
  enabled: true,
  primaryChannelID: "ch-a",
  candidateChannelIDs: ["ch-b"],
  maxHttpAttempts: 9,
  maxWaitSeconds: 30,
});
assertEqual(fbLegalMax.maxHttpAttempts, 9, "legal max attempts preserved");
assertEqual(fbLegalMax.maxWaitSeconds, 30, "legal max wait preserved");

const fbOutOfRange = normalizeProviderFallback({
  enabled: true,
  primaryChannelID: "ch-a",
  candidateChannelIDs: ["ch-b"],
  maxHttpAttempts: 1,
  maxWaitSeconds: 31,
});
assertEqual(fbOutOfRange.maxHttpAttempts, 1, "out-of-range attempts must not clamp");
assertEqual(fbOutOfRange.maxWaitSeconds, 31, "out-of-range wait must not clamp");
assert(
  Boolean(validateProviderFallbackBudget(fbOutOfRange)),
  "out-of-range budget must error instead of clamp",
);
assert(
  /2–9/.test(validateProviderFallbackBudget({ ...fbLegalMax, maxHttpAttempts: 10 })),
  "attempts 10 must mention 2–9",
);
assert(
  /1–30/.test(validateProviderFallbackBudget({ ...fbLegalMax, maxWaitSeconds: 0 })),
  "wait 0 after normalize is defaulted; raw 0-as-stored invalid wait after explicit non-default check",
);

const waitTooLow = validateProviderFallbackBudget({
  enabled: true,
  maxHttpAttempts: 5,
  maxWaitSeconds: 0,
});
assert(Boolean(waitTooLow) && /1–30/.test(waitTooLow), `wait 0 without normalize must error: ${waitTooLow}`);

const attemptsTooHigh = validateProviderFallbackBudget({
  enabled: true,
  maxHttpAttempts: 10,
  maxWaitSeconds: 8,
});
assert(Boolean(attemptsTooHigh) && /2–9/.test(attemptsTooHigh), `attempts 10 must error: ${attemptsTooHigh}`);

assertEqual(validateProviderFallbackBudget(fbLegalBounds), "", "legal min budget");
assertEqual(validateProviderFallbackBudget(fbLegalMax), "", "legal max budget");
assertEqual(validateProviderFallbackBudget(fbZeroBudget), "", "normalized zero budget is valid");

const fbSnake = normalizeProviderFallback({
  enabled: 1,
  primary_channel_id: "ch-snake",
  candidateChannelIDs: ["ch-a"],
  max_http_attempts: 4,
  max_wait_seconds: 20,
});
assertEqual(fbSnake.primaryChannelID, "ch-snake", "snake_case primary");
assertEqual(fbSnake.maxHttpAttempts, 4, "snake_case attempts");
assertEqual(fbSnake.maxWaitSeconds, 20, "snake_case wait");

const fbBad = normalizeProviderFallback("not-an-object");
assertEqual(fbBad.enabled, false, "non-object disabled");
assertEqual(fbBad.maxHttpAttempts, 5, "non-object attempts default");
assertEqual(fbBad.maxWaitSeconds, 8, "non-object wait default");

const oldConfigEcho = normalizeProviderFallback({
  enabled: false,
  primaryChannelID: "legacy-primary",
  candidateChannelIDs: ["legacy-candidate"],
});
assertEqual(oldConfigEcho.maxHttpAttempts, 5, "old config attempts default");
assertEqual(oldConfigEcho.maxWaitSeconds, 8, "old config wait default");
assertEqual(oldConfigEcho.primaryChannelID, "legacy-primary", "old disabled config keeps primary");
assertEqual(
  normalizeProviderFallback(oldConfigEcho),
  oldConfigEcho,
  "import/export roundtrip must be idempotent",
);

assertEqual(
  isLogicalRoutingAdapter({ providerFallback: { enabled: true, primaryChannelID: "a", candidateChannelIDs: ["b"] } }),
  true,
  "enabled fallback is logical routing",
);
assertEqual(
  isLogicalRoutingAdapter({ providerFallback: { enabled: false, primaryChannelID: "a", candidateChannelIDs: ["b"] } }),
  false,
  "disabled fallback is not logical routing",
);
assertEqual(
  shouldTestModelAdapterEndpoint({ providerFallback: { enabled: true } }),
  false,
  "logical alias must not call virtual endpoint",
);
assertEqual(
  shouldTestModelAdapterEndpoint({ providerFallback: { enabled: false } }),
  true,
  "physical channel remains testable",
);
assert(
  LOGICAL_ROUTING_RUNTIME_VERIFY_HINT.includes("运行验证"),
  "save hint must tell operator to runtime-verify the chain",
);

// ── 预算输入 helper：非数字不得显示字符串 NaN ──

assertEqual(formatFallbackBudgetInput(0), "", "zero budget displays empty");
assertEqual(formatFallbackBudgetInput(""), "", "empty budget displays empty");
assertEqual(formatFallbackBudgetInput(undefined), "", "undefined budget displays empty");
assertEqual(formatFallbackBudgetInput(null), "", "null budget displays empty");
assertEqual(formatFallbackBudgetInput(7), "7", "legal budget displays decimal text");
assertEqual(formatFallbackBudgetInput(Number.NaN), "", "numeric NaN must not render as NaN");
assertEqual(formatFallbackBudgetInput("NaN"), "", "NaN string must not be shown");
assertEqual(formatFallbackBudgetInput("abc"), "abc", "other raw invalid text can remain visible");
assertEqual(parseFallbackBudgetInput(""), 0, "empty parse is default 0");
assertEqual(parseFallbackBudgetInput(" 5 "), 5, "numeric parse");
assertEqual(parseFallbackBudgetInput("abc"), "abc", "non-numeric parse keeps raw string");
assertEqual(
  formatFallbackBudgetInput(parseFallbackBudgetInput("xyz")),
  "xyz",
  "non-numeric roundtrip must not become NaN",
);
assertEqual(
  formatFallbackBudgetInput(parseFallbackBudgetInput("NaN")),
  "",
  "typed NaN must not stay visible as NaN",
);
assert(
  Boolean(providerFallbackBudgetFieldError("maxHttpAttempts", parseFallbackBudgetInput("abc"))),
  "raw non-numeric attempts still shows field error",
);
assert(
  Boolean(providerFallbackBudgetFieldError("maxHttpAttempts", Number.NaN)),
  "numeric NaN still shows field error",
);
assertEqual(providerFallbackBudgetFieldError("maxHttpAttempts", ""), "", "empty attempts has no inline error");
assertEqual(providerFallbackBudgetFieldError("maxHttpAttempts", 0), "", "defaulted attempts has no inline error");
assertEqual(
  providerFallbackBudgetFieldError("maxHttpAttempts", 1),
  "全链最大 HTTP 尝试次数必须为 2–9 的整数",
  "attempts 1 inline error",
);
assertEqual(
  providerFallbackBudgetFieldError("maxWaitSeconds", 31),
  "全链最大等待秒数必须为 1–30 的整数",
  "wait 31 inline error",
);

// ── 使用 backend 已返回的完整 adapter id 校验，然后序列化剥 id ──

function physicalAdapter(id, name, url) {
  return {
    id,
    displayName: name,
    type: "openai",
    baseURL: url,
    apiKey: "sk-test",
    tooltipData: "备注",
    modelID: "gpt-test",
    openAIEndpoint: "/v1/chat/completions",
    providerFallback: normalizeProviderFallback({ enabled: false }),
  };
}

function persistPayloadRoundtrip(adapters) {
  const prepared = prepareModelAdaptersForPersist(adapters, validateProviderFallbackAdapters);
  if (!prepared.ok) {
    return prepared;
  }
  const payload = JSON.parse(JSON.stringify({
    modelAdapters: prepared.payloadAdapters,
  }));
  const reloaded = payload.modelAdapters.map((item, index) => ({
    ...item,
    id: adapters[index].id,
    providerFallback: normalizeProviderFallback(item.providerFallback),
  }));
  const second = prepareModelAdaptersForPersist(reloaded, validateProviderFallbackAdapters);
  return {
    ok: second.ok,
    error: second.error,
    prepared,
    second,
    reloaded,
    payload,
  };
}

const idA = "backend-https://example.com:443/v1";
const idB = "backend-https://xn--fsq.com:443/v1";
const idLogical = "backend-logical-alias:443";
const adapterA = physicalAdapter(idA, "A", "https://example.com:443/v1");
const adapterB = physicalAdapter(idB, "B", "https://例子.com/v1");
const logicalAdapter = {
  ...physicalAdapter(idLogical, "Logical", "https://alias.example.com/v1"),
  providerFallback: normalizeProviderFallback({
    enabled: true,
    primaryChannelID: idA,
    candidateChannelIDs: [idB],
    maxHttpAttempts: 5,
    maxWaitSeconds: 8,
  }),
};

const missingIDs = validateProviderFallbackAdapters([
  { ...adapterA, id: "" },
  { ...adapterB, id: "" },
  { ...logicalAdapter, id: "" },
]);
assert(
  Boolean(missingIDs) && /不存在/.test(missingIDs),
  `enabled fallback without backend ids must look like dangling refs: ${missingIDs}`,
);

const legalPersist = prepareModelAdaptersForPersist(
  [adapterA, adapterB, logicalAdapter],
  validateProviderFallbackAdapters,
);
assert(legalPersist.ok, `legal fallback save should succeed: ${legalPersist.error}`);
assertEqual(legalPersist.adaptersWithIds[0].id, idA, "backend id A must be kept");
assertEqual(legalPersist.adaptersWithIds[1].id, idB, "backend id B must be kept");
assertEqual(legalPersist.adaptersWithIds[2].id, idLogical, "backend logical id must be kept");
assert(
  legalPersist.payloadAdapters.every((item) => !Object.prototype.hasOwnProperty.call(item, "id")),
  `persist payload must strip id: ${JSON.stringify(legalPersist.payloadAdapters)}`,
);
assertEqual(
  legalPersist.payloadAdapters[2].providerFallback,
  logicalAdapter.providerFallback,
  "legal fallback fields survive persist",
);
assertEqual(
  stripDerivedModelAdapterIDs(legalPersist.adaptersWithIds).every((item) => !("id" in item)),
  true,
  "stripDerivedModelAdapterIDs removes id",
);

const nestedPrimary = prepareModelAdaptersForPersist(
  [adapterA, adapterB, logicalAdapter, {
    ...physicalAdapter("backend-logical-nested-primary", "Nested", "https://nested.example.com/v1"),
    providerFallback: normalizeProviderFallback({
      enabled: true,
      primaryChannelID: idLogical,
      candidateChannelIDs: [idB],
    }),
  }],
  validateProviderFallbackAdapters,
);
assert(!nestedPrimary.ok && /物理渠道/.test(nestedPrimary.error), `logical primary ref: ${nestedPrimary.error}`);

const nestedCandidate = prepareModelAdaptersForPersist(
  [adapterA, adapterB, logicalAdapter, {
    ...physicalAdapter("backend-logical-nested-candidate", "Nested", "https://nested.example.com/v1"),
    providerFallback: normalizeProviderFallback({
      enabled: true,
      primaryChannelID: idA,
      candidateChannelIDs: [idLogical],
    }),
  }],
  validateProviderFallbackAdapters,
);
assert(!nestedCandidate.ok && /物理渠道/.test(nestedCandidate.error), `logical candidate ref: ${nestedCandidate.error}`);

const danglingPersist = prepareModelAdaptersForPersist(
  [{
    ...logicalAdapter,
    providerFallback: normalizeProviderFallback({
      enabled: true,
      primaryChannelID: "deadbeefdeadbeef",
      candidateChannelIDs: [idB],
    }),
  }, adapterA, adapterB],
  validateProviderFallbackAdapters,
);
assert(!danglingPersist.ok && /不存在/.test(danglingPersist.error), `dangling ref: ${danglingPersist.error}`);

const selfPersist = prepareModelAdaptersForPersist(
  [adapterA, adapterB, {
    ...logicalAdapter,
    providerFallback: normalizeProviderFallback({
      enabled: true,
      primaryChannelID: idLogical,
      candidateChannelIDs: [idB],
    }),
  }],
  validateProviderFallbackAdapters,
);
assert(!selfPersist.ok && /不能与当前渠道相同/.test(selfPersist.error), `self ref: ${selfPersist.error}`);

const dupPersist = prepareModelAdaptersForPersist(
  [adapterA, adapterB, {
    ...logicalAdapter,
    providerFallback: normalizeProviderFallback({
      enabled: true,
      primaryChannelID: idA,
      candidateChannelIDs: [idB, idB],
    }),
  }],
  validateProviderFallbackAdapters,
);
assert(!dupPersist.ok && /重复/.test(dupPersist.error), `duplicate candidates: ${dupPersist.error}`);

const disabledEcho = prepareModelAdaptersForPersist(
  [{
    ...logicalAdapter,
    providerFallback: normalizeProviderFallback({
      enabled: false,
      primaryChannelID: idA,
      candidateChannelIDs: [idB],
      maxHttpAttempts: 7,
      maxWaitSeconds: 12,
    }),
  }, adapterA, adapterB],
  validateProviderFallbackAdapters,
);
assert(disabledEcho.ok, `disabled fallback should persist: ${disabledEcho.error}`);
assertEqual(disabledEcho.payloadAdapters[0].providerFallback.enabled, false, "disabled echo enabled");
assertEqual(disabledEcho.payloadAdapters[0].providerFallback.primaryChannelID, idA, "disabled echo primary");
assertEqual(disabledEcho.payloadAdapters[0].providerFallback.candidateChannelIDs, [idB], "disabled echo candidates");
assertEqual(disabledEcho.payloadAdapters[0].providerFallback.maxHttpAttempts, 7, "disabled echo attempts");
assertEqual(disabledEcho.payloadAdapters[0].providerFallback.maxWaitSeconds, 12, "disabled echo wait");

const outOfRangePersist = prepareModelAdaptersForPersist(
  [{
    ...logicalAdapter,
    providerFallback: normalizeProviderFallback({
      enabled: true,
      primaryChannelID: idA,
      candidateChannelIDs: [idB],
      maxHttpAttempts: 10,
      maxWaitSeconds: 8,
    }),
  }, adapterA, adapterB],
  validateProviderFallbackAdapters,
);
assert(
  !outOfRangePersist.ok && /2–9/.test(outOfRangePersist.error),
  `out-of-range persist must error: ${outOfRangePersist.error}`,
);

const disabledOutOfRange = prepareModelAdaptersForPersist(
  [{
    ...logicalAdapter,
    providerFallback: normalizeProviderFallback({
      enabled: false,
      primaryChannelID: idA,
      candidateChannelIDs: [idB],
      maxHttpAttempts: 10,
      maxWaitSeconds: 40,
    }),
  }, adapterA, adapterB],
  validateProviderFallbackAdapters,
);
assert(
  !disabledOutOfRange.ok && /2–9/.test(disabledOutOfRange.error),
  `disabled out-of-range budget must still be rejected: ${disabledOutOfRange.error}`,
);

const disabledWaitOutOfRange = prepareModelAdaptersForPersist(
  [{
    ...logicalAdapter,
    providerFallback: normalizeProviderFallback({
      enabled: false,
      primaryChannelID: idA,
      candidateChannelIDs: [idB],
      maxHttpAttempts: 5,
      maxWaitSeconds: 31,
    }),
  }, adapterA, adapterB],
  validateProviderFallbackAdapters,
);
assert(
  !disabledWaitOutOfRange.ok && /1–30/.test(disabledWaitOutOfRange.error),
  `disabled out-of-range wait must still be rejected: ${disabledWaitOutOfRange.error}`,
);

const enabledRoundtrip = persistPayloadRoundtrip([adapterA, adapterB, logicalAdapter]);
assert(enabledRoundtrip.ok, `enabled payload roundtrip should succeed: ${enabledRoundtrip.error}`);
assertEqual(enabledRoundtrip.reloaded[0].id, idA, "enabled roundtrip keeps :443 backend id");
assertEqual(enabledRoundtrip.reloaded[1].id, idB, "enabled roundtrip keeps IDN backend id");
assertEqual(enabledRoundtrip.reloaded[2].id, idLogical, "enabled roundtrip keeps logical backend id");
assertEqual(enabledRoundtrip.second.adaptersWithIds[0].id, idA, "second persist must not overwrite :443 id");
assertEqual(enabledRoundtrip.second.adaptersWithIds[1].id, idB, "second persist must not overwrite IDN id");
assertEqual(
  enabledRoundtrip.reloaded[2].providerFallback,
  logicalAdapter.providerFallback,
  "enabled roundtrip keeps fallback chain and budget",
);
assert(
  enabledRoundtrip.payload.modelAdapters.every((item) => !Object.prototype.hasOwnProperty.call(item, "id")),
  "roundtrip persist payload must not include id",
);

const disabledRoundtripAdapters = [
  {
    ...logicalAdapter,
    providerFallback: normalizeProviderFallback({
      enabled: false,
      primaryChannelID: idA,
      candidateChannelIDs: [idB],
      maxHttpAttempts: 7,
      maxWaitSeconds: 12,
    }),
  },
  adapterA,
  adapterB,
];
const disabledRoundtrip = persistPayloadRoundtrip(disabledRoundtripAdapters);
assert(disabledRoundtrip.ok, `disabled payload roundtrip should succeed: ${disabledRoundtrip.error}`);
assertEqual(disabledRoundtrip.reloaded[0].id, idLogical, "disabled roundtrip keeps backend id");
assertEqual(disabledRoundtrip.reloaded[0].providerFallback.enabled, false, "disabled roundtrip keeps enabled=false");
assertEqual(disabledRoundtrip.reloaded[0].providerFallback.primaryChannelID, idA, "disabled roundtrip keeps primary");
assertEqual(
  disabledRoundtrip.reloaded[0].providerFallback.candidateChannelIDs,
  [idB],
  "disabled roundtrip keeps candidates",
);
assertEqual(disabledRoundtrip.reloaded[0].providerFallback.maxHttpAttempts, 7, "disabled roundtrip keeps attempts");
assertEqual(disabledRoundtrip.reloaded[0].providerFallback.maxWaitSeconds, 12, "disabled roundtrip keeps wait");
assertEqual(
  disabledRoundtrip.second.adaptersWithIds[0].providerFallback,
  disabledRoundtrip.reloaded[0].providerFallback,
  "disabled second persist keeps budget and refs",
);

// ── 逻辑 alias 测试策略：0 次逻辑请求，物理渠道仍测 ──

const endpointPlan = selectAdaptersForEndpointTest([logicalAdapter, adapterA, adapterB, logicalAdapter]);
assertEqual(
  endpointPlan.toTest.map((item) => item.id),
  [idA, idB],
  "endpoint test plan must keep physical channels",
);
assertEqual(
  endpointPlan.skippedLogical.map((item) => item.id),
  [idLogical, idLogical],
  "endpoint test plan must skip logical aliases",
);

const testModelAdapterCalls = [];
function fakeTestModelAdapter(adapter) {
  testModelAdapterCalls.push(adapter.id);
}
const skippedHints = [];
if (endpointPlan.skippedLogical.length) {
  skippedHints.push(LOGICAL_ROUTING_RUNTIME_VERIFY_HINT);
}
endpointPlan.toTest.forEach(fakeTestModelAdapter);
assertEqual(testModelAdapterCalls, [idA, idB], "spy: logical alias must not call testModelAdapter");
assertEqual(testModelAdapterCalls.length, 2, "spy: physical channels still tested");
assertEqual(skippedHints, [LOGICAL_ROUTING_RUNTIME_VERIFY_HINT], "skipped logical tests use the same hint");

const singleLogicalPlan = selectAdaptersForEndpointTest([logicalAdapter]);
assertEqual(singleLogicalPlan.toTest, [], "single logical test plan is empty");
assertEqual(singleLogicalPlan.skippedLogical.length, 1, "single logical test is skipped");

// ── 上游并发上限：默认 / 边界 / 越界 / alias / 同组 / roundtrip / 保存回显 ──

assertEqual(DEFAULT_MAX_CONCURRENT_REQUESTS, 0, "default maxConcurrentRequests");
assertEqual(MAX_CONCURRENT_REQUESTS_LIMITS, { min: 1, max: 16 }, "capacity limits");
assertEqual(normalizeMaxConcurrentRequests(undefined), 0, "missing capacity defaults to 0");
assertEqual(normalizeMaxConcurrentRequests(""), 0, "empty capacity defaults to 0");
assertEqual(normalizeMaxConcurrentRequests(0), 0, "zero capacity stays 0");
assertEqual(normalizeMaxConcurrentRequests(1), 1, "capacity 1 preserved");
assertEqual(normalizeMaxConcurrentRequests(16), 16, "capacity 16 preserved");
assertEqual(normalizeMaxConcurrentRequests(17), 17, "out-of-range capacity must not clamp");
assertEqual(normalizeMaxConcurrentRequests(-1), -1, "negative capacity must not clamp");
assertEqual(normalizeMaxConcurrentRequests("8"), 8, "numeric string capacity");
assertEqual(maxConcurrentRequestsFieldError(0), "", "zero has no inline error");
assertEqual(maxConcurrentRequestsFieldError(""), "", "empty has no inline error");
assertEqual(maxConcurrentRequestsFieldError(1), "", "1 has no inline error");
assertEqual(maxConcurrentRequestsFieldError(16), "", "16 has no inline error");
assert(
  Boolean(maxConcurrentRequestsFieldError(17)) && /1–16/.test(maxConcurrentRequestsFieldError(17)),
  "17 inline error",
);
assertEqual(validateMaxConcurrentRequests(0), "", "zero is valid unlimited");
assertEqual(validateMaxConcurrentRequests(2), "", "2 is valid");
assert(
  Boolean(validateMaxConcurrentRequests(17, "模型 1")) && /模型 1/.test(validateMaxConcurrentRequests(17, "模型 1")),
  "prefixed out-of-range error",
);

const copiedCapacity = {
  ...physicalAdapter(idA, "CopySrc", "https://example.com:443/v1"),
  maxConcurrentRequests: normalizeMaxConcurrentRequests(4),
};
assertEqual(copiedCapacity.maxConcurrentRequests, 4, "copy preserves capacity");

const capacityPhysicalA = {
  ...physicalAdapter(idA, "A", "https://example.com:443/v1"),
  maxConcurrentRequests: normalizeMaxConcurrentRequests(2),
};
const capacityPhysicalB = {
  ...physicalAdapter(idB, "B", "https://example.com:443/v1"),
  maxConcurrentRequests: normalizeMaxConcurrentRequests(2),
};
const capacityPhysicalMismatch = {
  ...physicalAdapter(idB, "B", "https://example.com:443/v1"),
  maxConcurrentRequests: normalizeMaxConcurrentRequests(3),
};
const capacityNormalizedURLMismatch = {
  ...physicalAdapter(idB, "B", "HTTPS://EXAMPLE.COM:443/v1/"),
  type: "OPENAI",
  maxConcurrentRequests: normalizeMaxConcurrentRequests(3),
};
const capacityOtherGroup = {
  ...physicalAdapter("backend-other-group", "C", "https://other.example.com/v1"),
  maxConcurrentRequests: normalizeMaxConcurrentRequests(8),
};
const capacityLogical = {
  ...logicalAdapter,
  maxConcurrentRequests: normalizeMaxConcurrentRequests(0),
};
const capacityLogicalNonZero = {
  ...logicalAdapter,
  maxConcurrentRequests: normalizeMaxConcurrentRequests(2),
};

assertEqual(
  validateUpstreamCapacityAdapters([capacityPhysicalA, capacityPhysicalB]),
  "",
  "same-group matching limits",
);
assert(
  /必须使用相同/.test(validateUpstreamCapacityAdapters([capacityPhysicalA, capacityPhysicalMismatch])),
  "same-group mismatch rejected",
);
assert(
  /必须使用相同/.test(validateUpstreamCapacityAdapters([capacityPhysicalA, capacityNormalizedURLMismatch])),
  "normalized provider and base URL share one group",
);
assertEqual(
  validateUpstreamCapacityAdapters([capacityPhysicalA, capacityOtherGroup]),
  "",
  "different groups may differ",
);
assertEqual(
  validateUpstreamCapacityAdapters([capacityLogical, capacityPhysicalA, adapterB]),
  "",
  "logical alias zero is allowed",
);
assert(
  /逻辑路由 alias/.test(validateUpstreamCapacityAdapters([capacityLogicalNonZero, adapterA, adapterB])),
  "logical alias non-zero rejected",
);
assert(
  !/sk-test/.test(validateUpstreamCapacityAdapters([capacityPhysicalA, capacityPhysicalMismatch])),
  "capacity error must not leak API key",
);

function persistCapacityRoundtrip(adapters) {
  const prepared = prepareModelAdaptersForPersist(adapters, (items, options) => (
    validateProviderFallbackAdapters(items, options) || validateUpstreamCapacityAdapters(items, options)
  ));
  if (!prepared.ok) {
    return prepared;
  }
  const payload = JSON.parse(JSON.stringify({
    modelAdapters: prepared.payloadAdapters,
  }));
  const reloaded = payload.modelAdapters.map((item, index) => ({
    ...item,
    id: adapters[index].id,
    providerFallback: normalizeProviderFallback(item.providerFallback),
    maxConcurrentRequests: normalizeMaxConcurrentRequests(item.maxConcurrentRequests),
  }));
  const second = prepareModelAdaptersForPersist(reloaded, (items, options) => (
    validateProviderFallbackAdapters(items, options) || validateUpstreamCapacityAdapters(items, options)
  ));
  return {
    ok: second.ok,
    error: second.error,
    prepared,
    second,
    reloaded,
    payload,
  };
}

const capacityRoundtrip = persistCapacityRoundtrip([
  capacityPhysicalA,
  capacityPhysicalB,
  capacityLogical,
]);
assert(capacityRoundtrip.ok, `capacity persist should succeed: ${capacityRoundtrip.error}`);
assertEqual(capacityRoundtrip.reloaded[0].maxConcurrentRequests, 2, "save echo physical A");
assertEqual(capacityRoundtrip.reloaded[1].maxConcurrentRequests, 2, "save echo physical B");
assertEqual(capacityRoundtrip.reloaded[2].maxConcurrentRequests, 0, "save echo logical alias");
assertEqual(
  capacityRoundtrip.second.adaptersWithIds[0].maxConcurrentRequests,
  2,
  "second persist keeps capacity",
);
assert(
  capacityRoundtrip.payload.modelAdapters.every((item) => !Object.prototype.hasOwnProperty.call(item, "upstreamCapacityGroupKey")),
  "persist payload must not include derived group key",
);

const defaultCapacityRoundtrip = persistCapacityRoundtrip([
  physicalAdapter(idA, "A", "https://example.com:443/v1"),
  physicalAdapter(idB, "B", "https://例子.com/v1"),
]);
assert(defaultCapacityRoundtrip.ok, `default capacity persist should succeed: ${defaultCapacityRoundtrip.error}`);
assertEqual(defaultCapacityRoundtrip.reloaded[0].maxConcurrentRequests, 0, "missing capacity save echo is 0");

const outOfRangeCapacityPersist = persistCapacityRoundtrip([
  {
    ...physicalAdapter(idA, "A", "https://example.com:443/v1"),
    maxConcurrentRequests: normalizeMaxConcurrentRequests(17),
  },
]);
assert(!outOfRangeCapacityPersist.ok, "out-of-range capacity must fail persist");

const frontendSrc = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../src");
const projectionSource = readFileSync(path.join(frontendSrc, "state/configProjection.js"), "utf8");
const appStateSource = readFileSync(path.join(frontendSrc, "state/appState.js"), "utf8");
const editorSource = readFileSync(path.join(frontendSrc, "components/ModelEditor.vue"), "utf8");
const modelConfigSource = readFileSync(path.join(frontendSrc, "views/ModelConfig.vue"), "utf8");

assert(projectionSource.endsWith("\n"), "configProjection.js must end with a trailing newline");
assert(
  !/sha256Hex|SHA256_K|buildModelAdapterChannelID|withDerivedModelAdapterIDs/.test(projectionSource),
  "configProjection must not recompute channel IDs",
);
assert(
  !/const payload = buildConfigPayload\(config\);\s*const validationError = validateModelAdapters\(payload\.modelAdapters\)/.test(appStateSource),
  "persistConfigPayload must not validate after stripping ids",
);
assert(
  appStateSource.includes("prepareModelAdaptersForPersist")
    && appStateSource.includes("prepared.payloadAdapters")
    && appStateSource.includes("serializeConfigPayload"),
  "persistConfigPayload must keep existing ids, validate, then serialize stripped adapters",
);
assert(
  !appStateSource.includes("buildModelAdapterChannelID")
    && !appStateSource.includes("withDerivedModelAdapterIDs"),
  "appState must not recompute channel IDs",
);
assert(
  appStateSource.includes("shouldTestModelAdapterEndpoint")
    && appStateSource.includes("LOGICAL_ROUTING_RUNTIME_VERIFY_HINT"),
  "physical test path must refuse logical alias endpoints",
);
assert(
  appStateSource.includes("maxConcurrentRequests")
    && appStateSource.includes("validateUpstreamCapacityAdapters"),
  "appState must normalize and validate upstream capacity",
);
assert(
  !appStateSource.includes("upstreamCapacityGroupKey")
    && !projectionSource.includes("upstreamCapacityGroupKey"),
  "frontend must not persist derived capacity group keys",
);
assert(editorSource.includes("isLogicalRoutingAdapter(a)"), "fallback channel selectors must exclude logical aliases");
assert(editorSource.includes("formatFallbackBudgetInput"), "ModelEditor getter must hide NaN via helper");
assert(editorSource.includes("parseFallbackBudgetInput"), "ModelEditor setter must parse via helper");
assert(editorSource.includes("逻辑路由（建议仅子代理）"), "ModelEditor must mark logical routing");
assert(editorSource.includes("全链最大 HTTP 尝试次数（默认 5）"), "ModelEditor attempts field");
assert(editorSource.includes("全链最大等待秒数（默认 8）"), "ModelEditor wait field");
assert(editorSource.includes("min(剩余预算, 3)"), "help text must mention min(remaining, 3)");
assert(editorSource.includes("自身不会向虚拟 endpoint 发请求"), "help text must say alias sends no request");
assert(editorSource.includes("已有输出后不切换") || editorSource.includes("一旦已有输出则禁止切换"), "help text must forbid switch after output");
assert(editorSource.includes("费用") && editorSource.includes("隐私") && editorSource.includes("工具兼容"), "help text must mention cost/privacy/compat risk");
assert(editorSource.includes("LOGICAL_ROUTING_RUNTIME_VERIFY_HINT"), "save/test must hint runtime verification");
assert(editorSource.includes("shouldTestModelAdapterEndpoint"), "save-and-test must skip logical alias HTTP");
assert(editorSource.includes("上游并发上限"), "ModelEditor must show upstream concurrency limit");
assert(editorSource.includes("!isLogicalRoutingDraft"), "capacity field must hide on logical alias");
assert(editorSource.includes("等待固定 2 秒") || editorSource.includes("固定等待 2 秒"), "capacity help must mention fixed 2s wait");
assert(editorSource.includes("同一接口") && editorSource.includes("密钥"), "capacity help must mention shared interface and key");
assert(modelConfigSource.includes("LOGICAL_ROUTING_RUNTIME_VERIFY_HINT"), "ModelConfig must show the same runtime hint");
assert(modelConfigSource.includes("selectAdaptersForEndpointTest"), "ModelConfig must use the shared endpoint-test plan");
assert(
  /handleTestModelAdapter[\s\S]*selectAdaptersForEndpointTest/.test(modelConfigSource),
  "single test must explicitly skip logical aliases",
);
assert(
  /handleTestAllModelAdapters[\s\S]*selectAdaptersForEndpointTest/.test(modelConfigSource),
  "batch test must explicitly skip logical aliases",
);

console.log("config projection tests passed");