import { createRouter, createWebHashHistory } from "vue-router";
import Home from "@/views/Home.vue";
import CursorView from "@/views/CursorView.vue";
import GatewayView from "@/views/GatewayView.vue";
import ModelConfig from "@/views/ModelConfig.vue";
import SettingsView from "@/views/SettingsView.vue";
import { showModal } from "@/composables/useModal";
import {
  discardConfigSectionDraft,
  isRouteConfigDirty,
  routePathToConfigScope,
} from "@/state/appState";

export const MAIN_NAV_PATHS = ["/", "/cursor", "/gateway", "/models", "/settings"];

const MAIN_WINDOW_META = {
  showIcon: true,
  directlyClose: false,
  mainWindow: true,
};

export function isMainWindowPath(path) {
  return MAIN_NAV_PATHS.includes(path);
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
      path: "/cursor",
      component: CursorView,
      meta: { ...MAIN_WINDOW_META, title: "Cursor 集成" },
    },
    {
      path: "/gateway",
      component: GatewayView,
      meta: { ...MAIN_WINDOW_META, title: "网关集成" },
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
  if (!from.matched.length || to.path === from.path) {
    return true;
  }
  if (!isRouteConfigDirty(from.path)) {
    return true;
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
  discardConfigSectionDraft(routePathToConfigScope(from.path));
  return true;
});

export default router;