import {
  buildClientPreferencesFromState,
  buildObservabilityConfigFromState,
  normalizeClientPreferences,
  normalizeObservabilityConfig,
  normalizeProviderFallback,
} from "../src/state/configProjection.js";

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
if (JSON.stringify(projected) !== JSON.stringify(expected)) {
  throw new Error(`state projection mismatch: ${JSON.stringify(projected)}`);
}

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
if (JSON.stringify(defaults) !== JSON.stringify(expectedDefaults)) {
  throw new Error(`preference defaults mismatch: ${JSON.stringify(defaults)}`);
}

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
if (JSON.stringify(boundedObservability) !== JSON.stringify(expectedBoundedObservability)) {
  throw new Error(`observability bounds mismatch: ${JSON.stringify(boundedObservability)}`);
}

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
if (JSON.stringify(projectedObservability) !== JSON.stringify(expectedProjectedObservability)) {
  throw new Error(`observability projection mismatch: ${JSON.stringify(projectedObservability)}`);
}


// ── providerFallback 归一化测试 ──

// 默认关闭（null/undefined/disabled）
const fbOff = normalizeProviderFallback(null);
if (fbOff.enabled !== false || fbOff.primaryChannelID !== "" || fbOff.candidateChannelIDs.length !== 0) {
  throw new Error(`fallback null should be disabled: ${JSON.stringify(fbOff)}`);
}

const fbDisabled = normalizeProviderFallback({ enabled: false, primaryChannelID: "ch1", candidateChannelIDs: ["ch2"] });
if (fbDisabled.enabled !== false || fbDisabled.primaryChannelID !== "" || fbDisabled.candidateChannelIDs.length !== 0) {
  throw new Error(`fallback disabled should clear fields: ${JSON.stringify(fbDisabled)}`);
}

// 启用时保留字段
const fbEnabled = normalizeProviderFallback({
  enabled: true,
  primaryChannelID: "  ch-primary  ",
  candidateChannelIDs: ["ch-cand1", "", "ch-cand2"],
});
if (fbEnabled.enabled !== true) {
  throw new Error(`fallback enabled mismatch: ${JSON.stringify(fbEnabled)}`);
}
if (fbEnabled.primaryChannelID !== "ch-primary") {
  throw new Error(`fallback primaryChannelID trim mismatch: ${JSON.stringify(fbEnabled)}`);
}
// 空字符串候选被过滤
if (JSON.stringify(fbEnabled.candidateChannelIDs) !== JSON.stringify(["ch-cand1", "ch-cand2"])) {
  throw new Error(`fallback candidateChannelIDs filter mismatch: ${JSON.stringify(fbEnabled)}`);
}

// camelCase 和 snake_case 兼容
const fbSnake = normalizeProviderFallback({
  enabled: 1,
  primary_channel_id: "ch-snake",
  candidateChannelIDs: ["ch-a"],
});
if (fbSnake.primaryChannelID !== "ch-snake") {
  throw new Error(`fallback snake_case primaryChannelID mismatch: ${JSON.stringify(fbSnake)}`);
}

// 非对象输入 → 默认关闭
const fbBad = normalizeProviderFallback("not-an-object");
if (fbBad.enabled !== false) {
  throw new Error(`fallback non-object should be disabled: ${JSON.stringify(fbBad)}`);
}

console.log("config projection tests passed");
