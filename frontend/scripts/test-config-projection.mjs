import {
  buildClientPreferencesFromState,
  buildObservabilityConfigFromState,
  normalizeClientPreferences,
  normalizeObservabilityConfig,
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

console.log("config projection tests passed");
