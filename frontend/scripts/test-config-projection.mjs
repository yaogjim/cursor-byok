import { buildClientPreferencesFromState, normalizeClientPreferences } from "../src/state/configProjection.js";

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

console.log("config projection tests passed");