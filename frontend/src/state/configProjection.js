const SUPPORTED_THEMES = new Set(["light", "dark"]);

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