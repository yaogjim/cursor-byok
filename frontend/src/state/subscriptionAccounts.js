const ACCOUNT_STATES = new Set([
  "ready",
  "auth_required",
  "quota_exhausted",
  "pending",
  "error",
  "missing",
]);

function asString(value) {
  if (typeof value === "string") {
    return value.trim();
  }
  if (typeof value === "number" || typeof value === "boolean") {
    return String(value);
  }
  return "";
}

function pick(raw, ...keys) {
  if (!raw || typeof raw !== "object") {
    return undefined;
  }
  for (const key of keys) {
    if (!Object.prototype.hasOwnProperty.call(raw, key)) {
      continue;
    }
    const value = raw[key];
    if (value === undefined || value === null) {
      continue;
    }
    return value;
  }
  return undefined;
}

function asBoolean(value) {
  if (typeof value === "boolean") {
    return value;
  }
  if (typeof value === "number") {
    return value !== 0;
  }
  const normalized = asString(value).toLowerCase();
  return normalized === "true" || normalized === "1" || normalized === "yes";
}

function asNumber(value) {
  if (typeof value === "number" && Number.isFinite(value)) {
    return value;
  }
  const parsed = Number(asString(value));
  return Number.isFinite(parsed) ? parsed : 0;
}

function asTime(value) {
  if (value === undefined || value === null || value === "") {
    return "";
  }
  return value;
}

export function normalizeSubscriptionAccount(value) {
  const raw = value && typeof value === "object" && !Array.isArray(value) ? value : {};
  const state = asString(pick(raw, "state", "State")).toLowerCase();
  return {
    accountId: asString(pick(raw, "accountId", "AccountID", "AccountId")),
    provider: asString(pick(raw, "provider", "Provider")).toLowerCase(),
    state: ACCOUNT_STATES.has(state) ? state : "missing",
    email: asString(pick(raw, "email", "Email")),
    displayName: asString(pick(raw, "displayName", "DisplayName")),
    planLabel: asString(pick(raw, "planLabel", "PlanLabel")),
    chatgptAccountId: asString(pick(raw, "chatgptAccountId", "ChatGPTAccountID", "ChatGPTAccountId")),
    lastRefresh: asTime(pick(raw, "lastRefresh", "LastRefresh")),
    expiresAt: asTime(pick(raw, "expiresAt", "ExpiresAt")),
    hasRefreshToken: asBoolean(pick(raw, "hasRefreshToken", "HasRefreshToken")),
    remainingPercent: asNumber(pick(raw, "remainingPercent", "RemainingPercent")),
    usedPercent: asNumber(pick(raw, "usedPercent", "UsedPercent")),
    resetAt: asTime(pick(raw, "resetAt", "ResetAt")),
    sessionRemainingPercent: asNumber(pick(raw, "sessionRemainingPercent", "SessionRemainingPercent")),
    sessionResetAt: asTime(pick(raw, "sessionResetAt", "SessionResetAt")),
    limitReached: asBoolean(pick(raw, "limitReached", "LimitReached")),
    active: asBoolean(pick(raw, "active", "Active")),
    error: asString(pick(raw, "error", "Error")),
  };
}

export function normalizeSubscriptionAccountList(value) {
  const raw = Array.isArray(value)
    ? value
    : Array.isArray(value?.accounts)
      ? value.accounts
      : Array.isArray(value?.Accounts)
        ? value.Accounts
        : [];
  return raw.map((item) => normalizeSubscriptionAccount(item));
}

export function subscriptionStateLabel(state) {
  switch (asString(state).toLowerCase()) {
    case "ready":
      return "已就绪";
    case "auth_required":
      return "需要重新授权";
    case "quota_exhausted":
      return "配额已用尽";
    case "pending":
      return "等待授权";
    case "error":
      return "异常";
    default:
      return "未配置";
  }
}

export function subscriptionAccountHeadline(account) {
  const item = normalizeSubscriptionAccount(account);
  return {
    selectionLabel: item.active ? "当前激活" : "备用",
    stateLabel: subscriptionStateLabel(item.state),
  };
}

export function canActivateSubscriptionAccount(account) {
  const item = normalizeSubscriptionAccount(account);
  return !item.active && item.state === "ready";
}

export function subscriptionAccountActions(account, { busy = false } = {}) {
  const item = normalizeSubscriptionAccount(account);
  const showActivate = canActivateSubscriptionAccount(item);
  return {
    refreshDisabled: Boolean(busy),
    showActivate,
    activateDisabled: Boolean(busy) || !showActivate,
  };
}

export function panelUsageRefreshDisabled({ busy = false, accounts } = {}) {
  return Boolean(busy) || normalizeSubscriptionAccountList(accounts).length === 0;
}

export function activeSubscriptionFooter(accounts) {
  const activeAccount = normalizeSubscriptionAccountList(accounts).find((item) => item.active) || null;
  if (!activeAccount) {
    return "未配置";
  }
  const name = activeAccount.displayName || activeAccount.email || activeAccount.accountId;
  return `当前激活：${name} · ${subscriptionStateLabel(activeAccount.state)}`;
}