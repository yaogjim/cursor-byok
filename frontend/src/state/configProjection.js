const SUPPORTED_THEMES = new Set(["light", "dark"]);
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

function normalizeTheme(value) {
  const theme = asString(value).toLowerCase();
  return SUPPORTED_THEMES.has(theme) ? theme : DEFAULT_CLIENT_PREFERENCES.appearance.theme;
}

function boundedInteger(value, fallback, { min, max }) {
  const parsed = Number(asString(value));
  if (!Number.isInteger(parsed) || parsed <= 0) {
    return fallback;
  }
  return Math.min(max, Math.max(min, parsed));
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