import { IsWindows } from "@bindings/cursor/internal/bridge/proxyservice.js";
import { ref } from "vue";

export const isWindows = ref(false);

try {
  isWindows.value = Boolean(await IsWindows());
} catch {
  // 独立 Vite 预览没有 Wails 后端，按非 Windows 布局继续渲染。
}
