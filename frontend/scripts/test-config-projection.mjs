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
  validateProviderFallbackAdapters,
  validateProviderFallbackBudget,
  DEFAULT_MAX_CONCURRENT_REQUESTS,
  MAX_PROVIDER_FALLBACK_CANDIDATES,
  MAX_CONCURRENT_REQUESTS_LIMITS,
  maxConcurrentRequestsFieldError,
  normalizeMaxConcurrentRequests,
  validateMaxConcurrentRequests,
  validateUpstreamCapacityAdapters,
  normalizeGatewayConfig,
  DEFAULT_GATEWAY_CONFIG,
  resolveEffectiveTheme,
} from "../src/state/configProjection.js";
import { applyModelAdapterTypeChange } from "../src/state/modelAdapterTypeChange.js";

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

const systemProjected = buildClientPreferencesFromState({
  appearanceTheme: " SYSTEM ",
  advertisingEnabled: false,
  updateCheckOnStartup: false,
});
assertEqual(systemProjected.appearance.theme, "system", "state projection must persist system theme");

const systemNormalized = normalizeClientPreferences({
  appearance: { theme: "system" },
});
assertEqual(systemNormalized.appearance.theme, "system", "normalize must keep system theme");

assertEqual(resolveEffectiveTheme("light", true), "light", "explicit light ignores OS dark");
assertEqual(resolveEffectiveTheme("dark", false), "dark", "explicit dark ignores OS light");
assertEqual(resolveEffectiveTheme("system", true), "dark", "system follows OS dark");
assertEqual(resolveEffectiveTheme("system", false), "light", "system follows OS light");
assertEqual(resolveEffectiveTheme("unsupported", true), "light", "invalid theme resolves to default light");
assertEqual(resolveEffectiveTheme("", true), "light", "empty theme resolves to default light");

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
assertEqual(MAX_PROVIDER_FALLBACK_CANDIDATES, 4, "fallback chain allows 1 primary + 4 candidates");

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

// ── 使用 backend 已返回的完整 adapter id 校验并作为保存身份提示 ──

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
assertEqual(legalPersist.payloadAdapters[0].id, idA, "persist payload keeps backend id A as identity hint");
assertEqual(legalPersist.payloadAdapters[1].id, idB, "persist payload keeps backend id B as identity hint");
assertEqual(legalPersist.payloadAdapters[2].id, idLogical, "persist payload keeps logical id as identity hint");
assertEqual(
  legalPersist.payloadAdapters[2].providerFallback,
  logicalAdapter.providerFallback,
  "legal fallback fields survive persist",
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

const adapterC = physicalAdapter("backend-c", "C", "https://c.example.com/v1");
const adapterD = physicalAdapter("backend-d", "D", "https://d.example.com/v1");
const adapterE = physicalAdapter("backend-e", "E", "https://e.example.com/v1");
const adapterF = physicalAdapter("backend-f", "F", "https://f.example.com/v1");
const fourCandidatePersist = prepareModelAdaptersForPersist(
  [adapterA, adapterB, adapterC, adapterD, adapterE, adapterF, {
    ...logicalAdapter,
    providerFallback: normalizeProviderFallback({
      enabled: true,
      primaryChannelID: idA,
      candidateChannelIDs: [idB, adapterC.id, adapterD.id, adapterE.id],
    }),
  }],
  validateProviderFallbackAdapters,
);
assert(fourCandidatePersist.ok, `4 candidates should persist: ${fourCandidatePersist.error}`);
assertEqual(
  fourCandidatePersist.payloadAdapters[6].providerFallback.candidateChannelIDs,
  [idB, adapterC.id, adapterD.id, adapterE.id],
  "4 candidates must keep order through persist projection",
);

const fiveCandidatePersist = prepareModelAdaptersForPersist(
  [adapterA, adapterB, adapterC, adapterD, adapterE, adapterF, {
    ...logicalAdapter,
    providerFallback: normalizeProviderFallback({
      enabled: true,
      primaryChannelID: idA,
      candidateChannelIDs: [idB, adapterC.id, adapterD.id, adapterE.id, adapterF.id],
    }),
  }],
  validateProviderFallbackAdapters,
);
assert(
  !fiveCandidatePersist.ok && /1–4/.test(fiveCandidatePersist.error),
  `5 candidates must be rejected: ${fiveCandidatePersist.error}`,
);

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
assertEqual(
  enabledRoundtrip.payload.modelAdapters.map((item) => item.id),
  [idA, idB, idLogical],
  "roundtrip persist payload keeps backend ids as identity hints",
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
endpointPlan.toTest.forEach(fakeTestModelAdapter);
assertEqual(testModelAdapterCalls, [idA, idB], "spy: logical alias must not call testModelAdapter");
assertEqual(testModelAdapterCalls.length, 2, "spy: physical channels still tested");
assertEqual(endpointPlan.skippedLogical.length, 2, "batch plan still reports skipped logical aliases");

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

function extractSourceFunction(source, name) {
  const marker = `function ${name}`;
  const start = source.indexOf(marker);
  if (start < 0) {
    throw new Error(`missing function ${name}`);
  }
  const brace = source.indexOf("{", start);
  if (brace < 0) {
    throw new Error(`missing body for ${name}`);
  }
  let depth = 0;
  for (let index = brace; index < source.length; index += 1) {
    const char = source[index];
    if (char === "{") {
      depth += 1;
    } else if (char === "}") {
      depth -= 1;
      if (depth === 0) {
        return source.slice(start, index + 1);
      }
    }
  }
  throw new Error(`unclosed function ${name}`);
}

const frontendSrc = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../src");
const projectionSource = readFileSync(path.join(frontendSrc, "state/configProjection.js"), "utf8");
const appStateSource = readFileSync(path.join(frontendSrc, "state/appState.js"), "utf8");
const typeChangeSource = readFileSync(path.join(frontendSrc, "state/modelAdapterTypeChange.js"), "utf8");
const editorSource = readFileSync(path.join(frontendSrc, "components/ModelEditor.vue"), "utf8");
const modelConfigSource = readFileSync(path.join(frontendSrc, "views/ModelConfig.vue"), "utf8");
const selectSource = readFileSync(path.join(frontendSrc, "components/ui/Select.vue"), "utf8");

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
assert(editorSource.includes("applyModelAdapterTypeChange"), "ModelEditor must use the shared type-change helper");
assert(
  !/draft\.modelID\s*=\s*["']["']/.test(extractSourceFunction(editorSource, "handleModelTypeChange")),
  "switching model type must not clear modelID",
);
assert(
  editorSource.includes("currentModelIDOptions"),
  "ModelEditor must keep the current model identifier in the combobox options",
);
const refreshModelListFn = extractSourceFunction(editorSource, "refreshModelList");
assert(refreshModelListFn.includes("credentialSource"), "ModelEditor discovery must send credentialSource");
assert(
  refreshModelListFn.includes("isManagedCredentialSource(credentialSource)"),
  "ModelEditor managed discovery must not require apiKey",
);
assert(
  editorSource.includes("draft.credentialSource")
    && editorSource.includes("[draft.type, draft.baseURL, draft.apiKey, draft.credentialSource"),
  "ModelEditor must refetch models when credentialSource changes",
);
assert(
  editorSource.includes("isManagedCredentialSource(draft.credentialSource) || hasKey")
    || editorSource.includes("isManagedCredentialSource(draft.credentialSource) || String(draft.apiKey || \"\").trim()"),
  "ModelEditor canFetchModels must allow managed sources without apiKey",
);
assert(
  editorSource.includes('v-if="!isManagedCredential"'),
  "ModelEditor must hide apiKey input for managed credential sources",
);
assert(
  refreshModelListFn.includes("=== \"codex\"")
    && refreshModelListFn.includes("isCodex"),
  "ModelEditor Codex discovery must not require baseURL",
);
const fetchAvailableFn = extractSourceFunction(appStateSource, "fetchAvailableModelIDs");
assert(fetchAvailableFn.includes("credentialSource"), "fetchAvailableModelIDs must pass credentialSource");
assert(
  fetchAvailableFn.includes("isManagedCredentialSource(credentialSource) ? \"\" : asString(source.apiKey)"),
  "fetchAvailableModelIDs must strip apiKey for managed sources",
);
assert(
  appStateSource.includes("isManagedCredentialSource(normalizedCredentialSource) ? \"\" : asString(raw.apiKey || raw.key)"),
  "normalizeModelAdapter must strip managed apiKey before TestModelAdapter",
);
assert(
  appStateSource.includes("normalizedCredentialSource === \"codex\"")
    && appStateSource.includes("normalizedBaseURL = CODEX_SUBSCRIPTION_BASE_URL")
    && appStateSource.includes("effectiveOpenAIEndpoint = OPENAI_ENDPOINT_RESPONSES"),
  "normalizeModelAdapter must pin Codex subscriptions to the official Responses endpoint",
);
assert(
  appStateSource.includes("normalizedCredentialSource === \"grok\"")
    && appStateSource.includes("normalizedBaseURL = GROK_SUBSCRIPTION_BASE_URL")
    && appStateSource.includes("effectiveOpenAIEndpoint = OPENAI_ENDPOINT_CHAT_COMPLETIONS"),
  "normalizeModelAdapter must pin Grok subscriptions to the official chat completions endpoint",
);
assert(
  editorSource.includes(':disabled="isManagedCredential"'),
  "ModelEditor must prevent managed subscription endpoints from being edited",
);
const startModelAdapterTestFn = extractSourceFunction(appStateSource, "startModelAdapterTest");
assert(
  startModelAdapterTestFn.includes("testModelAdapter(normalized)"),
  "TestModelAdapter must send normalized adapter including credentialSource",
);
const typeChangeHelper = extractSourceFunction(typeChangeSource, "applyModelAdapterTypeChange");
assert(
  typeChangeHelper.includes("draft.type = nextType")
    && !/modelID\s*=/.test(typeChangeHelper),
  "type change helper must keep the current model identifier",
);
assert(
  appStateSource.includes("export { applyModelAdapterTypeChange }"),
  "appState must re-export applyModelAdapterTypeChange",
);

function modelIdentifierError(adapters) {
  for (const [index, adapter] of adapters.entries()) {
    if (!String(adapter?.modelID || "").trim()) {
      return `模型 ${index + 1} 的模型标识不能为空`;
    }
  }
  return "";
}

{
  const draft = {
    type: "openai",
    modelID: "cc",
    openAIEndpoint: "/v1/chat/completions",
    anthropicThinkingEffort: "",
  };
  applyModelAdapterTypeChange(draft, "anthropic");
  assertEqual(draft.type, "anthropic", "type switch to anthropic");
  assertEqual(draft.modelID, "cc", "type switch keeps modelID");
  assertEqual(draft.anthropicThinkingEffort, "xhigh", "type switch fills anthropic thinking default");
  assertEqual(modelIdentifierError([draft]), "", "kept modelID must pass the user-facing identifier check");
}

{
  const cleared = { type: "openai", modelID: "" };
  assertEqual(
    modelIdentifierError([cleared]),
    "模型 1 的模型标识不能为空",
    "empty modelID reproduces the user save error after type switch",
  );
}

{
  const draft = {
    type: "anthropic",
    modelID: "gpt-5.5",
    openAIEndpoint: "",
    anthropicThinkingEffort: "high",
  };
  applyModelAdapterTypeChange(draft, "openai");
  assertEqual(draft.type, "openai", "type switch to openai");
  assertEqual(draft.modelID, "gpt-5.5", "openai switch keeps modelID");
  assertEqual(draft.openAIEndpoint, "/v1/chat/completions", "openai switch fills missing endpoint");
}

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
const singleTestHandler = extractSourceFunction(modelConfigSource, "handleTestModelAdapter");
assert(
  singleTestHandler.includes("skippedLogical") && singleTestHandler.includes("LOGICAL_ROUTING_RUNTIME_VERIFY_HINT"),
  "single logical alias test must keep the runtime verify hint",
);
const batchTestHandler = extractSourceFunction(modelConfigSource, "handleTestAllModelAdapters");
assert(batchTestHandler.includes("selectAdaptersForEndpointTest"), "batch test must use the shared endpoint-test plan");
assert(
  !batchTestHandler.includes("LOGICAL_ROUTING_RUNTIME_VERIFY_HINT") && !batchTestHandler.includes("skippedLogical"),
  "batch test must silently skip logical aliases without a hint popup",
);
assert(
  projectionSource.includes("MAX_PROVIDER_FALLBACK_CANDIDATES")
    && /candidateChannelIDs\.length\s*>\s*MAX_PROVIDER_FALLBACK_CANDIDATES/.test(projectionSource),
  "validate must use the centralized candidate cap",
);
assert(editorSource.includes("MAX_PROVIDER_FALLBACK_CANDIDATES"), "ModelEditor must use the centralized candidate cap");
assert(!/const candidate1 = computed/.test(editorSource), "candidate slots must not hardcode candidate1");
assert(!/const candidate2 = computed/.test(editorSource), "candidate slots must not hardcode candidate2");
assert(
  /v-for="slotIndex in fallbackCandidateSlotIndexes"/.test(editorSource),
  "candidate slots must be data-driven",
);
assert(!/Math\.max\(\s*availableHeight\s*,\s*160\s*\)/.test(selectSource), "Select must not force height past the viewport");
assert(selectSource.includes("data-select-list"), "Select must mark the real scroll container");
assert(
  /<ul\b[^>]*data-select-list[\s\S]*?overflow-y-auto[\s\S]*?:style="listStyle"/.test(selectSource)
    || /<ul\b[^>]*data-select-list[\s\S]*?:style="listStyle"[\s\S]*?overflow-y-auto/.test(selectSource),
  "Select maxHeight must land on the overflowing ul, not the clipped outer shell",
);

const gateway = normalizeGatewayConfig({
  enabled: true,
  listenAddr: "127.0.0.1:18091",
  token: "leaked-secret-token",
  tokenConfigured: true,
  publicModels: [{ id: "public-a", targetAdapterID: "adapter-a" }],
});
assertEqual(
  gateway,
  {
    enabled: true,
    listenAddr: "127.0.0.1:18091",
    tokenConfigured: true,
    publicModels: [{ id: "public-a", targetAdapterID: "adapter-a" }],
  },
  "gateway projection must drop token",
);
assert(!Object.prototype.hasOwnProperty.call(gateway, "token"), "normalized gateway must not have token");
assertEqual(normalizeGatewayConfig(undefined), { ...DEFAULT_GATEWAY_CONFIG, publicModels: [] }, "gateway defaults");

assert(!appStateSource.includes("appState.gatewayToken "), "appState must not keep gateway token");
assert(appStateSource.includes("tokenConfigured"), "appState must project tokenConfigured only");
assert(!appStateSource.includes("token: normalized.gateway.token"), "serializeConfigPayload must not write gateway token");
assert(!appStateSource.includes("gatewayToken:"), "localStorage cache must not include gatewayToken");
const gatewayCardSource = readFileSync(path.join(frontendSrc, "components/GatewayCard.vue"), "utf8");
assert(gatewayCardSource.includes("copyGatewayToken"), "GatewayCard must copy via explicit API");
assert(gatewayCardSource.includes("rotateGatewayToken"), "GatewayCard must rotate via explicit API");
assert(gatewayCardSource.includes('import { Clipboard } from "@wailsio/runtime"'), "GatewayCard must use the native Wails clipboard");
assert(gatewayCardSource.includes("await Clipboard.SetText(text)"), "GatewayCard clipboard writes must await the native API");
assert(!gatewayCardSource.includes("copy-text-to-clipboard"), "GatewayCard must not use DOM clipboard fallbacks");
assert(gatewayCardSource.includes("handleGatewayTest"), "GatewayCard must expose a Gateway availability test");
assert(gatewayCardSource.includes("await handleGatewayTest()"), "Gateway start must automatically test availability");
assert(gatewayCardSource.includes("极简使用"), "GatewayCard must include minimal usage instructions");
assert(!gatewayCardSource.includes("appState.gatewayToken ="), "GatewayCard must not store token in appState");
const configViewSource = readFileSync(path.join(frontendSrc, "views/Config.vue"), "utf8");
assert(configViewSource.includes("GatewayCard"), "Config page must include Gateway card");

const clientApiSource = readFileSync(path.join(frontendSrc, "services/clientApi.js"), "utf8");
assert(clientApiSource.includes("TestGateway"), "client API must expose Gateway availability testing");
assert(appStateSource.includes("testGatewayService"), "appState must call the Gateway availability API");
const fetchModelsApiFn = extractSourceFunction(clientApiSource, "fetchModelAdapterModels");
assert(fetchModelsApiFn.includes("credentialSource"), "clientApi FetchModelAdapterModels must send credentialSource");
assert(
  fetchModelsApiFn.includes("managed ? \"\" : source.apiKey"),
  "clientApi must not send apiKey for managed credential sources",
);
const routerSource = readFileSync(path.join(frontendSrc, "router/index.js"), "utf8");
assert(appStateSource.includes('"/models": "models"'), "models route must map to models section");
assert(appStateSource.includes("snapshotModelsSection"), "models section must have a dirty snapshot");
assert(appStateSource.includes("modelsEditorDraftDirty"), "editor draft must participate in models dirty");
assert(appStateSource.includes("unresolvedGatewayPublicModelsError"), "model save must check gateway public models");
const includeCacheWriteSaver = extractSourceFunction(appStateSource, "saveIncludeCacheWriteInHitRate");
assert(includeCacheWriteSaver.includes("saveHomeMetrics"), "cache hit-rate toggle must use section save");
assert(!includeCacheWriteSaver.includes("persistConfigPayload"), "cache hit-rate toggle must not full-save user config");
assert(clientApiSource.includes("SaveHomeMetrics"), "client API must expose SaveHomeMetrics");
assert(modelConfigSource.includes("configSectionStatusText(\"models\")"), "models page must show section status");
assert(modelConfigSource.includes("persistScopedUserConfig(\"models\")"), "models page must save its own section");
assert(modelConfigSource.includes("handleSavePage"), "models page must offer save-this-page");
const sortHandler = extractSourceFunction(modelConfigSource, "handleModelSort");
assert(!sortHandler.includes("saveModelAdapterOrder"), "model sort must stay a page draft until save");
assert(routerSource.includes("isConfigSectionDirty"), "router must confirm leaving dirty config pages");
assert(editorSource.includes("setModelsEditorDraftDirty"), "ModelEditor must report unsaved draft dirty");

const accessSource = readFileSync(path.join(frontendSrc, "router/access.js"), "utf8");
const accessViewSource = readFileSync(path.join(frontendSrc, "views/AccessView.vue"), "utf8");
const unsupportedSource = readFileSync(path.join(frontendSrc, "views/UnsupportedClientPanel.vue"), "utf8");
const subscriptionAuthSource = readFileSync(path.join(frontendSrc, "components/SubscriptionAuthPanel.vue"), "utf8");
const settingsSource = readFileSync(path.join(frontendSrc, "views/SettingsView.vue"), "utf8");
const layoutSource = readFileSync(path.join(frontendSrc, "layouts/MainLayout.vue"), "utf8");
const catalogSource = readFileSync(path.join(frontendSrc, "state/modelCatalog.js"), "utf8");

const {
  parseAccessClient,
  accessClientConfigScope,
  accessLeaveConfigScopes,
  canonicalizeAccessRoute,
  isAccessTabDirty,
  sameAccessNavigation,
  ACCESS_CLIENTS,
  ACCESS_CONFIG_SCOPES,
  ACCESS_PATH,
  DEFAULT_ACCESS_CLIENT,
} = await import("../src/router/access.js");
const { filterModelAdapters, modelProviderTabs } = await import("../src/state/modelCatalog.js");

assertEqual(ACCESS_CLIENTS, ["gateway", "cursor", "codex", "grok", "anthropic"], "access client order");
assertEqual(parseAccessClient(""), DEFAULT_ACCESS_CLIENT, "empty access client defaults to gateway");
assertEqual(parseAccessClient("CURSOR"), "cursor", "access client is case-insensitive");
assertEqual(parseAccessClient("GROK"), "grok", "grok access client is case-insensitive");
assertEqual(parseAccessClient("claude"), "anthropic", "legacy claude route maps to anthropic");
assertEqual(parseAccessClient("nope"), "gateway", "unknown access client falls back to gateway");
assertEqual(parseAccessClient(["cursor", "gateway"]), "cursor", "array query uses the first client");
assertEqual(accessClientConfigScope("cursor"), "cursor", "cursor access maps to cursor section");
assertEqual(accessClientConfigScope("gateway"), "gateway", "gateway access maps to gateway section");
assertEqual(accessClientConfigScope("codex"), "", "codex has no config section");
assertEqual(accessClientConfigScope("grok"), "", "grok has no config section");
assertEqual(accessClientConfigScope("anthropic"), "", "anthropic has no config section");
assertEqual(accessClientConfigScope(["cursor"]), "cursor", "array cursor query maps to cursor section");
assertEqual(
  accessLeaveConfigScopes(
    { path: "/access", query: { client: "cursor" } },
    { path: "/access", query: { client: "gateway" } },
  ),
  ["cursor"],
  "switching access clients checks the current client scope",
);
assertEqual(
  accessLeaveConfigScopes(
    { path: "/access", query: { client: "codex" } },
    { path: "/access", query: { client: "cursor" } },
  ),
  [],
  "codex has no draft scope during in-access switches",
);
assertEqual(
  accessLeaveConfigScopes(
    { path: "/access", query: { client: "anthropic" } },
    { path: "/models" },
  ),
  [...ACCESS_CONFIG_SCOPES],
  "leaving access from anthropic checks cursor and gateway together",
);
assertEqual(
  accessLeaveConfigScopes(
    { path: "/access", query: { client: "cursor" } },
    { path: "/settings" },
  ),
  [...ACCESS_CONFIG_SCOPES],
  "leaving access to another top-level route checks both config scopes",
);
assert(isAccessTabDirty({ cursor: true, gateway: false }), "access tab dirty is OR of cursor");
assert(isAccessTabDirty({ cursor: false, gateway: true }), "access tab dirty is OR of gateway");
assert(!isAccessTabDirty({ cursor: false, gateway: false }), "access tab clean when both clean");
assert(
  sameAccessNavigation(
    { path: "/access", query: { client: "cursor" } },
    { path: "/access", query: { client: "cursor" } },
  ),
  "same access client is the same navigation",
);
assert(
  !sameAccessNavigation(
    { path: "/access", query: { client: "cursor" } },
    { path: "/access", query: { client: "gateway" } },
  ),
  "switching access client is a new navigation",
);
assert(
  sameAccessNavigation(
    { path: "/access", query: { client: "nope" } },
    { path: "/access", query: { client: "gateway" } },
  ),
  "unknown client is the same navigation as gateway",
);
assert(
  !sameAccessNavigation(
    { path: "/access", query: { client: "nope" } },
    { path: "/access", query: { client: "cursor" } },
  ),
  "unknown client leaving cursor is a new navigation",
);
assertEqual(
  canonicalizeAccessRoute({ path: ACCESS_PATH, query: { client: "cursor" } }),
  null,
  "canonical cursor query is left alone",
);
assertEqual(
  canonicalizeAccessRoute({ path: ACCESS_PATH, query: { client: "nope" } })?.query.client,
  DEFAULT_ACCESS_CLIENT,
  "unknown client canonicalizes to gateway",
);
assertEqual(
  canonicalizeAccessRoute({ path: ACCESS_PATH, query: { client: ["cursor", "gateway"] } })?.query.client,
  "cursor",
  "array query canonicalizes to the first known client",
);
assertEqual(
  canonicalizeAccessRoute({ path: "/cursor", query: { client: "nope" } }),
  null,
  "canonicalize ignores non-access paths",
);

assert(routerSource.includes('path: "/cursor"'), "old /cursor route remains as redirect");
assert(routerSource.includes('path: "/gateway"'), "old /gateway route remains as redirect");
assert(routerSource.includes("accessRouteLocation(\"cursor\")"), "old /cursor redirects into access cursor");
assert(routerSource.includes("accessRouteLocation(\"gateway\")"), "old /gateway redirects into access gateway");
assert(routerSource.includes("canonicalizeAccessRoute"), "router canonicalizes access query before dirty guard");
assert(routerSource.includes("accessLeaveConfigScopes"), "access dirty guard uses explicit leave scopes");
assert(routerSource.includes("leaveConfigScopes"), "router discards the scopes it actually checked");
assert(routerSource.includes("discardConfigSectionDraft(scope)"), "confirmed leave discards each checked scope");
assert(appStateSource.includes("accessClientConfigScope"), "appState maps /access query to section");
assert(appStateSource.includes("isAccessTabDirty"), "appState exposes access tab OR dirty");
assert(layoutSource.includes("configSectionDirty.access"), "shell access tab uses OR dirty");
assert(layoutSource.includes("app-tabbar"), "shell uses v5 top tab bar");
assert(layoutSource.includes("--wails-draggable: drag"), "shell keeps Wails drag region");
assert(layoutSource.includes("--wails-draggable: no-drag"), "tab controls remain no-drag");
assert(!layoutSource.includes("h-[40px] w-screen"), "tab bar must not be covered by a 40px drag overlay");
assert(!layoutSource.includes("z-[9999]"), "leftover full-width drag overlay z-index must be gone");
assert(layoutSource.includes("Window.Minimise"), "shell keeps Windows minimize");
assert(layoutSource.includes("checkForAppUpdates"), "shell keeps update check");
assert(layoutSource.includes("LocaleSelect"), "shell keeps locale select");
assert(layoutSource.includes("proxyBadgeText"), "shell keeps proxy badge");
assert(layoutSource.includes("lastAccessClient"), "access nav remembers last client");
assert(layoutSource.includes("accessRouteLocation"), "access nav always includes client query");
assert(layoutSource.includes("gatewayRunning"), "access badge counts running gateway");
assert(!layoutSource.includes("gatewayEnabled"), "access badge must not count merely enabled gateway");

assert(accessViewSource.includes("GatewayCard"), "AccessView reuses GatewayCard");
assert(accessViewSource.includes("CursorView"), "AccessView reuses Cursor features");
assert(accessViewSource.includes("UnsupportedClientPanel"), "AccessView keeps the Anthropic placeholder panel");
assert(accessViewSource.includes("SubscriptionAuthPanel"), "AccessView renders provider auth inside client panes");
assert(
  accessViewSource.includes("activeClient === 'codex' || activeClient === 'grok'")
    && accessViewSource.includes(':provider="activeClient"'),
  "Codex and Grok must each render their own subscription auth pane",
);
assert(!accessViewSource.includes("<SubscriptionAuthPanel />"), "subscription auth must not remain as a mixed global region");
assert(subscriptionAuthSource.includes("Codex 接入"), "Codex pane keeps the requested integration heading");
assert(subscriptionAuthSource.includes("Grok 接入"), "Grok pane has its own integration heading");
assert(subscriptionAuthSource.includes("导入 auth.json"), "Codex auth.json import stays in the Codex pane");
assert(subscriptionAuthSource.includes("设备码授权"), "Codex and Grok panes expose device authorization");
assert(subscriptionAuthSource.includes('listSubscriptionAccounts(props.provider)'), "Codex and Grok panes must both load account lists");
assert(subscriptionAuthSource.includes('v-for="account in accounts"'), "subscription panes must render managed account lists");
assert(subscriptionAuthSource.includes("refreshSubscriptionAccountUsage"), "Codex accounts must support account-specific usage refresh");
assert(subscriptionAuthSource.includes("导入 sub2api"), "Codex and Grok panes must expose sub2api import");
assert(subscriptionAuthSource.includes("sub2apiSelected"), "sub2api import must let users select accounts");
assert(subscriptionAuthSource.includes("resetSub2APIImport();"), "successful sub2api import must close and reset the selection modal while busy");
const sub2apiImportHandler = extractSourceFunction(subscriptionAuthSource, "handleImportSub2API");
assert(sub2apiImportHandler.includes("resetSub2APIImport();"), "sub2api success path must reset the modal directly");
assert(!sub2apiImportHandler.includes("closeSub2APIImport();"), "sub2api success path must not use the busy-guarded manual close handler");
assert(subscriptionAuthSource.includes('class="subscription-account-list"'), "managed account list must use its scroll container class");
assert(subscriptionAuthSource.includes("已按当前接入类型过滤"), "sub2api selection must explain provider filtering");
assert(subscriptionAuthSource.includes("当前使用"), "subscription account list must show the active account");
assert(clientApiSource.includes("RefreshSubscriptionAccountUsage"), "client API must expose account-specific usage refresh");
assert(clientApiSource.includes("PreviewSub2APIImport"), "client API must expose provider-filtered sub2api preview");
assert(clientApiSource.includes("ImportSub2APIAccounts"), "client API must expose selected sub2api import");
assert(accessViewSource.includes('v-if="activeClient === \'gateway\'"'), "AccessView remounts client panes with v-if");
assert(!accessViewSource.includes("keep-alive"), "AccessView must not keep-alive client panes");
assert(accessViewSource.includes("is-embedded-pane"), "cursor pane uses nested overflow layout");
assert(!accessViewSource.includes('aria-current="page"'), "client list buttons must not use page current");
assert(accessViewSource.includes("icon-[bxl--openai]"), "Codex uses the OpenAI brand icon");
assert(accessViewSource.includes("icon-[simple-icons--x]"), "Grok uses the xAI/X brand icon");
assert(accessViewSource.includes("icon-[logos--claude-icon]"), "Anthropic uses the Claude brand icon");
assert(accessViewSource.includes("is-codex"), "Codex icon keeps its brand class");
assert(accessViewSource.includes("is-grok"), "Grok icon keeps its brand class");
assert(accessViewSource.includes("is-anthropic"), "Anthropic icon keeps its brand class");
const accessLiteralSource = accessViewSource + unsupportedSource + subscriptionAuthSource;
assert(!/example\.(com|org)/.test(accessLiteralSource), "access UI must not ship sample accounts");
assert(!/developer@|team@|personal@|work@/.test(accessLiteralSource), "access UI must not ship sample emails");
assert(!/\beyJ[A-Za-z0-9_-]{8,}\./.test(accessLiteralSource), "access UI must not ship JWT literals");
assert(!/\bsk-(?:ant-|proj-)?[A-Za-z0-9]{12,}/.test(accessLiteralSource), "access UI must not ship sample API key literals");
assert(unsupportedSource.includes("添加授权"), "codex/claude keep the screenshot add-auth action");
assert(unsupportedSource.includes("暂无授权账号"), "codex/claude use empty account state");
assert(!unsupportedSource.includes("@click"), "codex/claude auth actions stay unwired");
const unsupportedButtons = [...unsupportedSource.matchAll(/<Button\b([^>]*)>/g)].map((match) => match[1]);
assert(unsupportedButtons.length > 0, "codex/claude keep screenshot action buttons");
assert(
  unsupportedButtons.every((attrs) => /\bdisabled\b/.test(attrs)),
  "codex/claude auth/sync/save buttons stay disabled",
);
assert(settingsSource.includes("基本设置") && settingsSource.includes("会话与日志") && settingsSource.includes("网络与请求") && settingsSource.includes("数据与恢复"), "settings has four segmented panels");
assert(settingsSource.includes("inert"), "planned settings controls must be inert");
assert(settingsSource.includes("开机自启动"), "settings shows planned autostart as inert");
assert(settingsSource.includes('role="group"'), "settings segmented control uses group semantics");
assert(!settingsSource.includes('role="tablist"'), "settings must not expose incomplete tabs");
assert(settingsSource.includes("LocaleSelect"), "language remains immediate LocaleSelect");
assert(appStateSource.includes('value: "system"'), "theme options include system");
assert(appStateSource.includes("prefers-color-scheme"), "system theme resolves through matchMedia");
assert(appStateSource.includes("effectiveAppearanceTheme"), "frontend tracks resolved theme separately from persisted enum");
const homeTrendChartSource = readFileSync(path.join(frontendSrc, "components/charts/HomeTrendChart.vue"), "utf8");
assert(homeTrendChartSource.includes("effectiveAppearanceTheme"), "trend chart redraws when resolved theme changes");
assert(!settingsSource.includes("不能端到端保存 system"), "settings no longer treats system as planned-only");
assert(settingsSource.includes("persistScopedUserConfig(\"settings\")"), "settings still saves its own section");
assert(modelConfigSource.includes("filterModelAdapters"), "models page uses shared search/provider filter");
assert(modelConfigSource.includes("layoutMode"), "models page has list/grid toggle");
assert(modelConfigSource.includes("handleDuplicateModelAdapter"), "list/grid keep duplicate");
assert(modelConfigSource.includes("handleDeleteModelAdapter"), "list/grid keep delete");
assert(modelConfigSource.includes("showModal"), "model delete asks for confirmation");
assert(modelConfigSource.includes("handleImportConfig"), "models import semantics remain");
assert(modelConfigSource.includes("handleExportConfig"), "models export semantics remain");
assert(modelConfigSource.includes("导入完整配置"), "models import button names full-config transfer");
assert(modelConfigSource.includes("导出完整配置"), "models export button names full-config transfer");
function extractNamedButtonInner(source, title) {
  const match = source.match(new RegExp(`<Button[\\s\\S]*?title="${title}"[\\s\\S]*?>([\\s\\S]*?)</Button>`));
  return match ? match[1] : "";
}
assert(!extractNamedButtonInner(modelConfigSource, "导入完整配置").includes("icon-["), "models import button has no icon");
assert(!extractNamedButtonInner(modelConfigSource, "导出完整配置").includes("icon-["), "models export button has no icon");
const deleteModelFn = extractSourceFunction(appStateSource, "deleteModelAdapterAt");
const duplicateModelFn = extractSourceFunction(appStateSource, "duplicateModelAdapterAt");
const deleteHandler = extractSourceFunction(modelConfigSource, "handleDeleteModelAdapter");
assert(!deleteModelFn.includes("persistConfigPayload"), "model delete stays a page draft until save");
assert(deleteModelFn.includes("appState.modelAdapters"), "model delete mutates the draft list");
assert(!duplicateModelFn.includes("persistConfigPayload"), "model duplicate stays a page draft until save");
assert(duplicateModelFn.includes("appState.modelAdapters"), "model duplicate mutates the draft list");
assert(deleteHandler.includes("showModal"), "delete handler confirms before mutating");
assert(catalogSource.includes("adapterSearchText"), "model catalog search is a pure helper");

const catalogAdapters = [
  { type: "openai", displayName: "gpt", modelID: "gpt-5", baseURL: "https://api.openai.com", openAIEndpoint: "/v1/responses" },
  { type: "anthropic", displayName: "claude", modelID: "claude-3", baseURL: "https://api.anthropic.com" },
];
assertEqual(filterModelAdapters(catalogAdapters, { type: "openai" }).map((item) => item.modelID), ["gpt-5"], "provider filter keeps openai");
assertEqual(filterModelAdapters(catalogAdapters, { query: "claude-3" }).map((item) => item.modelID), ["claude-3"], "search matches upstream id");
assertEqual(filterModelAdapters(catalogAdapters, { query: "/v1/responses" }).map((item) => item.modelID), ["gpt-5"], "search matches endpoint");
assertEqual(
  modelProviderTabs(catalogAdapters).map((tab) => [tab.value, tab.count]),
  [
    ["all", 2],
    ["openai", 1],
    ["anthropic", 1],
    ["google", 0],
    ["deepseek", 0],
    ["xai", 0],
    ["qwen", 0],
    ["moonshot", 0],
    ["tencent", 0],
  ],
  "provider tabs keep the full catalog and count real adapters",
);

const cursorViewSource = readFileSync(path.join(frontendSrc, "views/CursorView.vue"), "utf8");
assert(cursorViewSource.includes("embedded"), "CursorView supports nested access layout");
assert(cursorViewSource.includes("p-0"), "nested CursorView strips page padding");

const localeDir = path.join(frontendSrc, "i18n/locales");
const zhMessages = JSON.parse(readFileSync(path.join(localeDir, "zh-CN.json"), "utf8"));
const requiredLocaleSources = new Set([
  "主导航",
  "总览",
  "接入",
  "接入中心",
  "{0} 已接入",
  "Cursor 助手",
  "刷新运行状态",
  "共享入口",
  "接入方式",
  "基本设置",
  "会话与日志",
  "网络与请求",
  "数据与恢复",
  "设置分组",
  "开机自启动",
  "跟随系统",
  "删除模型配置",
  "确定删除「{0}」吗？删除后需要保存本页才会写入配置。",
  "Codex 接入",
  "Grok 接入",
  "Anthropic 接入",
  "静态 API key",
  "ChatGPT / Codex 订阅",
  "Grok 订阅",
  "导入 auth.json",
  "设备码授权",
  "凭据来源",
]);
const zhBySource = new Map(Object.entries(zhMessages).map(([id, source]) => [source, id]));
for (const source of requiredLocaleSources) {
  const id = zhBySource.get(source);
  assert(id, `zh-CN must contain source ${source}`);
  for (const locale of ["zh-CN", "en-US", "ja-JP", "ru-RU"]) {
    const messages = JSON.parse(readFileSync(path.join(localeDir, `${locale}.json`), "utf8"));
    assert(String(messages[id] || "").trim(), `${locale} must translate ${source}`);
  }
}

console.log("config projection tests passed");