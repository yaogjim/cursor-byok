import { createRouter, createWebHashHistory } from "vue-router";
import Home from "@/views/Home.vue";
import AccessView from "@/views/AccessView.vue";
import ModelConfig from "@/views/ModelConfig.vue";
import SettingsView from "@/views/SettingsView.vue";
import { showModal } from "@/composables/useModal";
import {
  discardConfigSectionDraft,
  isConfigSectionDirty,
  routePathToConfigScope,
} from "@/state/appState";
import {
  ACCESS_PATH,
  accessLeaveConfigScopes,
  accessRouteLocation,
  canonicalizeAccessRoute,
  sameAccessNavigation,
} from "@/router/access";

export const MAIN_NAV_PATHS = ["/", ACCESS_PATH, "/models", "/settings"];

const MAIN_WINDOW_META = {
  showIcon: true,
  directlyClose: false,
  mainWindow: true,
};

export function isMainWindowPath(path) {
  if (MAIN_NAV_PATHS.includes(path)) {
    return true;
  }
  return path === "/cursor" || path === "/gateway";
}

export function leaveConfigScopes(from, to) {
  if (from?.path === ACCESS_PATH) {
    return accessLeaveConfigScopes(from, to);
  }
  const scope = routePathToConfigScope(from?.path, from?.query);
  return scope ? [scope] : [];
}

const router = createRouter({
  history: createWebHashHistory(),
  routes: [
    {
      path: "/",
      component: Home,
      meta: { ...MAIN_WINDOW_META, title: "数据概览" },
    },
    {
      path: ACCESS_PATH,
      component: AccessView,
      meta: { ...MAIN_WINDOW_META, title: "接入中心" },
    },
    {
      path: "/models",
      component: ModelConfig,
      meta: { ...MAIN_WINDOW_META, title: "上游模型" },
    },
    {
      path: "/settings",
      component: SettingsView,
      meta: { ...MAIN_WINDOW_META, title: "系统设置" },
    },
    {
      path: "/cursor",
      redirect: () => accessRouteLocation("cursor"),
    },
    {
      path: "/gateway",
      redirect: () => accessRouteLocation("gateway"),
    },
    {
      path: "/config",
      redirect: "/settings",
    },
    {
      path: "/model-config",
      redirect: "/models",
    },
  ],
});

router.beforeEach(async (to, from) => {
  const canonicalAccess = canonicalizeAccessRoute(to);
  if (!from.matched.length || sameAccessNavigation(to, from)) {
    return canonicalAccess || true;
  }
  const scopes = leaveConfigScopes(from, to);
  if (!scopes.some((scope) => isConfigSectionDirty(scope))) {
    return canonicalAccess || true;
  }
  const confirmed = await showModal({
    title: "有未保存更改",
    content: "离开后将丢弃本页未保存的修改。",
    confirmText: "离开",
    cancelText: "留下",
  });
  if (!confirmed) {
    return false;
  }
  for (const scope of scopes) {
    discardConfigSectionDraft(scope);
  }
  return canonicalAccess || true;
});

export default router;