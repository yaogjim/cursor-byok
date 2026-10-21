import assert from "node:assert/strict";
import test, { after, before } from "node:test";
import path from "node:path";
import { fileURLToPath } from "node:url";

import vue from "@vitejs/plugin-vue";
import { createServer } from "vite";

const frontendRoot = path.resolve(path.dirname(fileURLToPath(import.meta.url)), "../..");

function createNode(kind, tag = "") {
  return {
    kind,
    tag,
    parent: null,
    children: [],
    props: {},
    className: "",
    listeners: {},
    text: "",
  };
}

function createTestRenderer(createRenderer) {
  return createRenderer({
    patchProp(el, key, _prev, next) {
      if (key === "class") {
        if (typeof next === "string") {
          el.className = next;
        } else if (Array.isArray(next)) {
          el.className = next.flat(Infinity).filter(Boolean).join(" ");
        } else if (next && typeof next === "object") {
          el.className = Object.entries(next).filter(([, value]) => value).map(([name]) => name).join(" ");
        } else {
          el.className = "";
        }
        return;
      }
      if (/^on[A-Z]/.test(key)) {
        el.listeners[key.slice(2).toLowerCase()] = next;
        return;
      }
      if (next == null || next === false) {
        delete el.props[key];
        return;
      }
      el.props[key] = next;
    },
    insert(el, parent, anchor) {
      el.parent = parent;
      if (!anchor) {
        parent.children.push(el);
        return;
      }
      const index = parent.children.indexOf(anchor);
      parent.children.splice(index < 0 ? parent.children.length : index, 0, el);
    },
    remove(el) {
      const parent = el.parent;
      if (!parent) {
        return;
      }
      parent.children = parent.children.filter((child) => child !== el);
      el.parent = null;
    },
    createElement(tag) {
      return createNode("element", tag);
    },
    createText(text) {
      const node = createNode("text");
      node.text = String(text ?? "");
      return node;
    },
    createComment(text) {
      const node = createNode("comment");
      node.text = String(text ?? "");
      return node;
    },
    setText(node, text) {
      node.text = String(text ?? "");
    },
    setElementText(el, text) {
      el.children = [];
      if (!text) {
        return;
      }
      const node = createNode("text");
      node.text = String(text);
      node.parent = el;
      el.children.push(node);
    },
    parentNode(node) {
      return node.parent;
    },
    nextSibling(node) {
      const parent = node.parent;
      if (!parent) {
        return null;
      }
      const index = parent.children.indexOf(node);
      return parent.children[index + 1] || null;
    },
    querySelector() {
      return null;
    },
    setScopeId(el, id) {
      el.props[id] = "";
    },
  });
}

function textOf(node) {
  if (!node) {
    return "";
  }
  if (node.kind === "text") {
    return node.text;
  }
  return (node.children || []).map(textOf).join("");
}

function collect(node, predicate, out = []) {
  if (!node) {
    return out;
  }
  if (predicate(node)) {
    out.push(node);
  }
  for (const child of node.children || []) {
    collect(child, predicate, out);
  }
  return out;
}

function buttons(root) {
  return collect(root, (node) => node.tag === "button");
}

function listItems(root) {
  return collect(root, (node) => node.tag === "li")
    .filter((node) => node.parent && String(node.parent.className).includes("subscription-account-list"));
}

function labeledButtons(root, label) {
  return buttons(root).filter((node) => textOf(node).trim() === label);
}

function closestListItem(node) {
  let current = node;
  while (current) {
    if (current.tag === "li") {
      return current;
    }
    current = current.parent;
  }
  return null;
}

function click(node) {
  const handler = node?.listeners?.click;
  if (typeof handler !== "function") {
    throw new Error("no click handler");
  }
  return handler({ type: "click", preventDefault() {}, stopPropagation() {} });
}

function isDisabled(node) {
  return Boolean(node?.props?.disabled);
}

function deferred() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

async function flush() {
  const { nextTick } = vueRuntime;
  for (let index = 0; index < 12; index += 1) {
    await Promise.resolve();
    await nextTick();
  }
}

function notImplemented(name) {
  return async () => {
    throw new Error(`${name} is not stubbed`);
  };
}

function stubApi(overrides = {}) {
  const api = {
    listSubscriptionAccounts: notImplemented("listSubscriptionAccounts"),
    activateSubscriptionAccount: notImplemented("activateSubscriptionAccount"),
    refreshSubscriptionAccountUsage: notImplemented("refreshSubscriptionAccountUsage"),
    refreshSubscriptionUsage: notImplemented("refreshSubscriptionUsage"),
    deleteSubscriptionAccount: notImplemented("deleteSubscriptionAccount"),
    clearCodexAuth: notImplemented("clearCodexAuth"),
    importCodexAuth: notImplemented("importCodexAuth"),
    importSub2APIAccounts: notImplemented("importSub2APIAccounts"),
    previewSub2APIImport: notImplemented("previewSub2APIImport"),
    pollCodexDeviceAuth: notImplemented("pollCodexDeviceAuth"),
    pollGrokDeviceAuth: notImplemented("pollGrokDeviceAuth"),
    startCodexDeviceAuth: notImplemented("startCodexDeviceAuth"),
    startGrokDeviceAuth: notImplemented("startGrokDeviceAuth"),
    ...overrides,
  };
  globalThis.__subscriptionAuthTestApi = api;
  return api;
}

function threeCodexAccounts() {
  return [
    { accountId: "codex:one", displayName: "one@example.test", state: "auth_required", active: true },
    { accountId: "codex:two", displayName: "two@example.test", state: "ready", active: false },
    { accountId: "codex:three", displayName: "three@example.test", state: "quota_exhausted", active: false },
  ];
}

const STUB_CLIENT_API = `
const api = () => globalThis.__subscriptionAuthTestApi;
export function listSubscriptionAccounts(provider) { return api().listSubscriptionAccounts(provider); }
export function activateSubscriptionAccount(accountID) { return api().activateSubscriptionAccount(accountID); }
export function refreshSubscriptionAccountUsage(provider, accountID) { return api().refreshSubscriptionAccountUsage(provider, accountID); }
export function refreshSubscriptionUsage(provider) { return api().refreshSubscriptionUsage(provider); }
export function deleteSubscriptionAccount(accountID) { return api().deleteSubscriptionAccount(accountID); }
export function clearCodexAuth() { return api().clearCodexAuth(); }
export function importCodexAuth(path) { return api().importCodexAuth(path); }
export function importSub2APIAccounts(path, provider, accountIDs) { return api().importSub2APIAccounts(path, provider, accountIDs); }
export function previewSub2APIImport(path, provider) { return api().previewSub2APIImport(path, provider); }
export function pollCodexDeviceAuth(input) { return api().pollCodexDeviceAuth(input); }
export function pollGrokDeviceAuth(input) { return api().pollGrokDeviceAuth(input); }
export function startCodexDeviceAuth() { return api().startCodexDeviceAuth(); }
export function startGrokDeviceAuth() { return api().startGrokDeviceAuth(); }
`;

const STUB_WAILS = `
export const Browser = { OpenURL: async () => {} };
export const Dialogs = { OpenFile: async () => "" };
`;

const STUB_APP_STATE = `
export function toUserError(error) {
  if (error && typeof error === "object" && error.message) return String(error.message);
  return String(error || "");
}
`;

const STUB_CONTENT_MODAL = `
export default {
  name: "ContentModal",
  props: ["open", "title", "size", "closeDisabled"],
  emits: ["close"],
  setup(props, { slots }) {
    return () => props.open ? slots.default?.() : null;
  },
};
`;

let vite;
let Panel;
let vueRuntime;

async function createHarness() {
  if (!globalThis.window) {
    globalThis.window = globalThis;
  }
  vite = await createServer({
    configFile: false,
    root: frontendRoot,
    logLevel: "error",
    appType: "custom",
    define: {
      __VUE_OPTIONS_API__: true,
      __VUE_PROD_DEVTOOLS__: false,
      __VUE_PROD_HYDRATION_MISMATCH_DETAILS__: false,
    },
    server: {
      middlewareMode: true,
      hmr: false,
      ws: false,
      watch: null,
    },
    optimizeDeps: { noDiscovery: true, include: [] },
    // Client SFC compile (render, not ssrRender) plus Node module-runner transform.
    environments: {
      ssr: {
        consumer: "client",
        optimizeDeps: { noDiscovery: true, include: [] },
        dev: { moduleRunnerTransform: true },
      },
    },
    resolve: {
      alias: [
        { find: /^@\/state\/appState(?:\.js)?$/, replacement: "virtual:sub-test/appState" },
        { find: /^@\/services\/clientApi(?:\.js)?$/, replacement: "virtual:sub-test/clientApi" },
        { find: "@wailsio/runtime", replacement: "virtual:sub-test/wails" },
        { find: "@", replacement: path.join(frontendRoot, "src") },
      ],
    },
    plugins: [
      {
        name: "subscription-auth-panel-test-stubs",
        enforce: "pre",
        resolveId(id) {
          if (id.startsWith("virtual:sub-test/")) {
            return `\0${id}`;
          }
          if (id.includes("ContentModal.vue")) {
            return "\0virtual:sub-test/ContentModal";
          }
          const normalized = String(id).replaceAll("\\", "/");
          if (normalized.endsWith("/src/state/appState.js")) {
            return "\0virtual:sub-test/appState";
          }
          if (normalized.endsWith("/src/services/clientApi.js")) {
            return "\0virtual:sub-test/clientApi";
          }
          if (normalized.includes("@wailsio/runtime")) {
            return "\0virtual:sub-test/wails";
          }
          return null;
        },
        load(id) {
          if (id === "\0virtual:sub-test/clientApi") return STUB_CLIENT_API;
          if (id === "\0virtual:sub-test/wails") return STUB_WAILS;
          if (id === "\0virtual:sub-test/appState") return STUB_APP_STATE;
          if (id === "\0virtual:sub-test/ContentModal") return STUB_CONTENT_MODAL;
          return null;
        },
      },
      vue(),
    ],
  });
  vueRuntime = await vite.ssrLoadModule("vue");
  const loaded = await vite.ssrLoadModule("/src/components/SubscriptionAuthPanel.vue");
  Panel = loaded.default;
  if (typeof Panel?.render !== "function") {
    throw new Error("SubscriptionAuthPanel is missing a client render function");
  }
  return { vite, Panel };
}

before(async () => {
  await createHarness();
});

after(async () => {
  await vite?.close();
});

function mountPanel(component, providerValue = "codex") {
  const { createRenderer, h, ref } = vueRuntime;
  const provider = ref(providerValue);
  const renderer = createTestRenderer(createRenderer);
  const root = createNode("element", "div");
  const app = renderer.createApp({
    setup() {
      return () => h(component, { provider: provider.value });
    },
  });
  app.mount(root);
  return {
    root,
    provider,
    async setProvider(next) {
      provider.value = next;
      await flush();
    },
    unmount() {
      app.unmount();
    },
  };
}

test("SubscriptionAuthPanel shows current active independently of ready/auth_required/quota", async (t) => {
  stubApi({
    listSubscriptionAccounts: async () => threeCodexAccounts(),
  });
  const harness = mountPanel(Panel);
  t.after(() => harness.unmount());
  await flush();

  const items = listItems(harness.root);
  assert.equal(items.length, 3);
  assert.match(textOf(items[0]), /one@example\.test/);
  assert.match(textOf(items[0]), /当前激活/);
  assert.match(textOf(items[0]), /需要重新授权/);
  assert.equal(items[0].props["aria-current"], "true");
  assert.equal(labeledButtons(items[0], "激活").length, 0);
  assert.match(textOf(items[1]), /two@example\.test/);
  assert.match(textOf(items[1]), /备用/);
  assert.match(textOf(items[1]), /已就绪/);
  assert.match(textOf(items[2]), /配额已用尽/);
  assert.equal(labeledButtons(items[2], "激活").length, 0);
  assert.match(textOf(harness.root), /当前激活：one@example\.test · 需要重新授权/);

  const activateButtons = labeledButtons(harness.root, "激活");
  assert.equal(activateButtons.length, 1);
  assert.equal(isDisabled(activateButtons[0]), false);
  assert.match(textOf(closestListItem(activateButtons[0])), /two@example\.test/);
});

test("refresh failure restores busy and keeps the retry button enabled", async (t) => {
  const firstRefresh = deferred();
  const secondRefresh = deferred();
  let refreshCalls = 0;
  stubApi({
    listSubscriptionAccounts: async () => threeCodexAccounts(),
    refreshSubscriptionAccountUsage: async () => {
      refreshCalls += 1;
      return refreshCalls === 1 ? firstRefresh.promise : secondRefresh.promise;
    },
  });
  const harness = mountPanel(Panel);
  t.after(() => harness.unmount());
  await flush();

  const rowRefresh = labeledButtons(listItems(harness.root)[0], "刷新用量")[0];
  assert.ok(rowRefresh);
  assert.equal(isDisabled(rowRefresh), false);
  click(rowRefresh);
  await flush();
  assert.equal(isDisabled(rowRefresh), true);
  assert.equal(refreshCalls, 1);

  firstRefresh.reject(new Error("dial tcp: i/o timeout"));
  await flush();
  const retry = labeledButtons(listItems(harness.root)[0], "刷新用量")[0];
  assert.equal(isDisabled(retry), false);
  assert.match(textOf(harness.root), /dial tcp: i\/o timeout/);

  click(retry);
  await flush();
  assert.equal(refreshCalls, 2);
  assert.equal(isDisabled(retry), true);
  secondRefresh.resolve({ accounts: threeCodexAccounts() });
  await flush();
  assert.equal(isDisabled(labeledButtons(listItems(harness.root)[0], "刷新用量")[0]), false);
});

test("activate success switches the current account in the real list", async (t) => {
  let accounts = threeCodexAccounts();
  stubApi({
    listSubscriptionAccounts: async () => accounts,
    activateSubscriptionAccount: async (accountID) => {
      accounts = accounts.map((item) => ({
        ...item,
        active: item.accountId === accountID,
        state: item.accountId === accountID ? "ready" : item.state,
      }));
      return accounts.find((item) => item.accountId === accountID);
    },
  });
  const harness = mountPanel(Panel);
  t.after(() => harness.unmount());
  await flush();

  const activate = labeledButtons(harness.root, "激活")[0];
  assert.ok(activate);
  click(activate);
  await flush();

  const items = listItems(harness.root);
  assert.match(textOf(items[1]), /当前激活/);
  assert.match(textOf(items[1]), /two@example\.test/);
  assert.equal(items[1].props["aria-current"], "true");
  assert.doesNotMatch(textOf(items[0]), /当前激活/);
  assert.match(textOf(harness.root), /当前激活：two@example\.test · 已就绪/);
  assert.equal(labeledButtons(harness.root, "激活").length, 0);
});

test("stale list from a previous provider does not overwrite the new provider", async (t) => {
  const lists = [];
  stubApi({
    listSubscriptionAccounts: (provider) => {
      const pending = deferred();
      lists.push({ provider, pending });
      return pending.promise;
    },
  });
  const harness = mountPanel(Panel, "codex");
  t.after(() => harness.unmount());
  await flush();
  assert.equal(lists.length, 1);
  assert.equal(lists[0].provider, "codex");

  await harness.setProvider("grok");
  assert.equal(lists.length, 2);
  assert.equal(lists[1].provider, "grok");

  lists[0].pending.resolve([
    { accountId: "codex:stale", displayName: "stale-codex@example.test", state: "ready", active: true },
  ]);
  await flush();
  assert.doesNotMatch(textOf(harness.root), /stale-codex@example\.test/);
  assert.match(textOf(harness.root), /Grok 接入/);

  lists[1].pending.resolve([
    { accountId: "grok:one", displayName: "grok-one@example.test", state: "ready", active: true },
  ]);
  await flush();
  assert.match(textOf(harness.root), /grok-one@example\.test/);
  assert.match(textOf(harness.root), /当前激活/);
  assert.doesNotMatch(textOf(harness.root), /stale-codex@example\.test/);
});