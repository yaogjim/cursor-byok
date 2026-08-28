import assert from "node:assert/strict";
import test from "node:test";

import {
  ACCESS_PATH,
  DEFAULT_ACCESS_CLIENT,
  accessClientConfigScope,
  accessLeaveConfigScopes,
  accessRouteLocation,
  canonicalizeAccessRoute,
  parseAccessClient,
  sameAccessNavigation,
} from "./access.js";

test("parseAccessClient normalizes unknown, empty, and array query values", () => {
  assert.equal(parseAccessClient(""), DEFAULT_ACCESS_CLIENT);
  assert.equal(parseAccessClient("nope"), DEFAULT_ACCESS_CLIENT);
  assert.equal(parseAccessClient("CURSOR"), "cursor");
  assert.equal(parseAccessClient(["cursor", "gateway"]), "cursor");
  assert.equal(parseAccessClient([]), DEFAULT_ACCESS_CLIENT);
});

test("canonicalizeAccessRoute rewrites missing, unknown, and array clients", () => {
  assert.equal(canonicalizeAccessRoute({ path: "/models", query: { client: "nope" } }), null);
  assert.equal(
    canonicalizeAccessRoute({ path: ACCESS_PATH, query: { client: "cursor" } }),
    null,
  );

  assert.deepEqual(
    canonicalizeAccessRoute({ path: ACCESS_PATH, query: {}, hash: "" }),
    {
      path: ACCESS_PATH,
      query: { client: DEFAULT_ACCESS_CLIENT },
      hash: "",
      replace: true,
    },
  );

  assert.deepEqual(
    canonicalizeAccessRoute({ path: ACCESS_PATH, query: { client: "nope", tab: "1" }, hash: "#x" }),
    {
      path: ACCESS_PATH,
      query: { client: DEFAULT_ACCESS_CLIENT, tab: "1" },
      hash: "#x",
      replace: true,
    },
  );

  assert.deepEqual(
    canonicalizeAccessRoute({ path: ACCESS_PATH, query: { client: ["cursor", "gateway"] } }),
    {
      path: ACCESS_PATH,
      query: { client: "cursor" },
      hash: "",
      replace: true,
    },
  );
});

test("sameAccessNavigation treats unknown clients as the canonical client", () => {
  assert.equal(
    sameAccessNavigation(
      { path: ACCESS_PATH, query: { client: "nope" } },
      { path: ACCESS_PATH, query: { client: "gateway" } },
    ),
    true,
  );
  assert.equal(
    sameAccessNavigation(
      { path: ACCESS_PATH, query: { client: "nope" } },
      { path: ACCESS_PATH, query: { client: "cursor" } },
    ),
    false,
  );
  assert.equal(
    sameAccessNavigation(
      { path: ACCESS_PATH, query: { client: ["cursor"] } },
      { path: ACCESS_PATH, query: { client: "cursor" } },
    ),
    true,
  );
});

test("legacy /cursor location maps to access cursor and cursor config scope", () => {
  assert.deepEqual(accessRouteLocation("cursor"), {
    path: ACCESS_PATH,
    query: { client: "cursor" },
  });
  assert.equal(accessClientConfigScope("cursor"), "cursor");
  assert.equal(accessClientConfigScope("nope"), "gateway");
  assert.equal(accessClientConfigScope("codex"), "");
  assert.equal(accessClientConfigScope(["cursor"]), "cursor");
});

test("accessLeaveConfigScopes checks current client inside /access and both scopes when leaving", () => {
  assert.deepEqual(
    accessLeaveConfigScopes(
      { path: ACCESS_PATH, query: { client: "cursor" } },
      { path: ACCESS_PATH, query: { client: "gateway" } },
    ),
    ["cursor"],
  );
  assert.deepEqual(
    accessLeaveConfigScopes(
      { path: ACCESS_PATH, query: { client: "gateway" } },
      { path: ACCESS_PATH, query: { client: "cursor" } },
    ),
    ["gateway"],
  );
  assert.deepEqual(
    accessLeaveConfigScopes(
      { path: ACCESS_PATH, query: { client: "codex" } },
      { path: ACCESS_PATH, query: { client: "cursor" } },
    ),
    [],
  );
  assert.deepEqual(
    accessLeaveConfigScopes(
      { path: ACCESS_PATH, query: { client: "claude" } },
      { path: ACCESS_PATH, query: { client: "gateway" } },
    ),
    [],
  );
  assert.deepEqual(
    accessLeaveConfigScopes(
      { path: ACCESS_PATH, query: { client: "cursor" } },
      { path: "/models" },
    ),
    ["gateway", "cursor"],
  );
  assert.deepEqual(
    accessLeaveConfigScopes(
      { path: ACCESS_PATH, query: { client: "codex" } },
      { path: "/settings" },
    ),
    ["gateway", "cursor"],
  );
  assert.deepEqual(
    accessLeaveConfigScopes(
      { path: ACCESS_PATH, query: { client: "claude" } },
      { path: "/" },
    ),
    ["gateway", "cursor"],
  );
  assert.deepEqual(
    accessLeaveConfigScopes({ path: "/models" }, { path: "/" }),
    [],
  );
});