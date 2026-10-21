import assert from "node:assert/strict";
import test from "node:test";

import {
  activeSubscriptionFooter,
  canActivateSubscriptionAccount,
  normalizeSubscriptionAccount,
  normalizeSubscriptionAccountList,
  panelUsageRefreshDisabled,
  subscriptionAccountActions,
  subscriptionAccountHeadline,
  subscriptionStateLabel,
} from "./subscriptionAccounts.js";

test("normalize accepts PascalCase fields only as a compatibility fallback", () => {
  const account = normalizeSubscriptionAccount({
    AccountID: "codex:one",
    DisplayName: "one@example.test",
    State: "ready",
    Active: true,
    RemainingPercent: 80,
  });
  assert.equal(account.accountId, "codex:one");
  assert.equal(account.active, true);
  assert.equal(account.state, "ready");
  assert.equal(account.remainingPercent, 80);
});

test("canonical camelCase AccountStatus json tags remain the primary mapping", () => {
  const account = normalizeSubscriptionAccount({
    accountId: "codex:one",
    displayName: "one@example.test",
    state: "auth_required",
    active: true,
    remainingPercent: 12,
  });
  assert.equal(account.accountId, "codex:one");
  assert.equal(account.active, true);
  assert.equal(account.state, "auth_required");
  assert.equal(subscriptionAccountHeadline(account).selectionLabel, "当前激活");
  assert.equal(subscriptionAccountHeadline(account).stateLabel, "需要重新授权");
});

test("active selection is independent of ready, auth_required, and quota_exhausted", () => {
  const accounts = normalizeSubscriptionAccountList([
    { accountId: "codex:one", displayName: "one@example.test", state: "auth_required", active: true },
    { accountId: "codex:two", displayName: "two@example.test", state: "ready", active: false },
    { accountId: "codex:three", displayName: "three@example.test", state: "quota_exhausted", active: false },
  ]);
  assert.equal(subscriptionAccountHeadline(accounts[0]).selectionLabel, "当前激活");
  assert.equal(subscriptionAccountHeadline(accounts[1]).selectionLabel, "备用");
  assert.equal(subscriptionAccountHeadline(accounts[2]).selectionLabel, "备用");
  assert.equal(subscriptionAccountActions(accounts[0]).showActivate, false);
  assert.equal(subscriptionAccountActions(accounts[1]).showActivate, true);
  assert.equal(subscriptionAccountActions(accounts[2]).showActivate, false);
  assert.equal(canActivateSubscriptionAccount(accounts[1]), true);
  assert.equal(
    activeSubscriptionFooter(accounts),
    "当前激活：one@example.test · 需要重新授权",
  );
  assert.equal(subscriptionStateLabel("quota_exhausted"), "配额已用尽");
});

test("refresh stays enabled after auth_required so a later retry is possible", () => {
  const authRequired = normalizeSubscriptionAccount({
    accountId: "codex:one",
    state: "auth_required",
    active: true,
  });
  assert.equal(subscriptionAccountActions(authRequired, { busy: false }).refreshDisabled, false);
  assert.equal(subscriptionAccountActions(authRequired, { busy: true }).refreshDisabled, true);
  assert.equal(panelUsageRefreshDisabled({ busy: false, accounts: [authRequired] }), false);
  assert.equal(panelUsageRefreshDisabled({ busy: false, accounts: [] }), true);
});

test("normalized account DTOs drop credential fields", () => {
  const account = normalizeSubscriptionAccount({
    accountId: "codex:one",
    state: "ready",
    active: true,
    accessToken: "secret-access",
    refreshToken: "secret-refresh",
    tokens: { access_token: "secret-access", refresh_token: "secret-refresh" },
    error: "dial tcp: i/o timeout",
  });
  const serialized = JSON.stringify(account);
  assert.equal(serialized.includes("secret-access"), false);
  assert.equal(serialized.includes("secret-refresh"), false);
  assert.equal(serialized.includes("access_token"), false);
  assert.equal(account.error, "dial tcp: i/o timeout");
});