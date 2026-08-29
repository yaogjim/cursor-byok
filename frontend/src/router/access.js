export const ACCESS_CLIENTS = Object.freeze(["gateway", "cursor", "codex", "grok", "anthropic"]);
export const DEFAULT_ACCESS_CLIENT = "gateway";
export const ACCESS_PATH = "/access";

const ACCESS_CLIENT_SET = new Set(ACCESS_CLIENTS);
const ACCESS_CLIENT_ALIASES = Object.freeze({ claude: "anthropic" });

const ACCESS_CLIENT_CONFIG_SCOPE = Object.freeze({
  gateway: "gateway",
  cursor: "cursor",
});

export const ACCESS_CONFIG_SCOPES = Object.freeze(Object.values(ACCESS_CLIENT_CONFIG_SCOPE));

function firstQueryValue(value) {
  if (Array.isArray(value)) {
    return value.length > 0 ? value[0] : "";
  }
  return value;
}

export function parseAccessClient(value) {
  const client = String(firstQueryValue(value) ?? "").trim().toLowerCase();
  const normalized = ACCESS_CLIENT_ALIASES[client] || client;
  return ACCESS_CLIENT_SET.has(normalized) ? normalized : DEFAULT_ACCESS_CLIENT;
}

export function isCanonicalAccessClientQuery(query) {
  const raw = query?.client;
  return typeof raw === "string" && raw === parseAccessClient(raw);
}

export function canonicalizeAccessRoute(to) {
  if (!to || to.path !== ACCESS_PATH) {
    return null;
  }
  if (isCanonicalAccessClientQuery(to.query)) {
    return null;
  }
  return {
    path: ACCESS_PATH,
    query: { ...(to.query || {}), client: parseAccessClient(to.query?.client) },
    hash: to.hash || "",
    replace: true,
  };
}

export function isAccessClientSupported(client) {
  const normalized = parseAccessClient(client);
  return normalized === "gateway"
    || normalized === "cursor"
    || normalized === "codex"
    || normalized === "grok";
}

export function accessClientConfigScope(client) {
  return ACCESS_CLIENT_CONFIG_SCOPE[parseAccessClient(client)] || "";
}

export function accessRouteLocation(client = DEFAULT_ACCESS_CLIENT) {
  return {
    path: ACCESS_PATH,
    query: { client: parseAccessClient(client) },
  };
}

export function isAccessTabDirty(dirty = {}) {
  return Boolean(dirty.cursor || dirty.gateway);
}

export function accessLeaveConfigScopes(from, to) {
  if (!from || from.path !== ACCESS_PATH) {
    return [];
  }
  if (to && to.path === ACCESS_PATH) {
    const scope = accessClientConfigScope(from.query?.client);
    return scope ? [scope] : [];
  }
  return [...ACCESS_CONFIG_SCOPES];
}

export function sameAccessNavigation(to, from) {
  if (!to || !from) {
    return false;
  }
  if (to.path !== from.path) {
    return false;
  }
  if (to.path !== ACCESS_PATH) {
    return true;
  }
  return parseAccessClient(to.query?.client) === parseAccessClient(from.query?.client);
}