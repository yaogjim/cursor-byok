import vue from "@vitejs/plugin-vue";
import wails from "@wailsio/runtime/plugins/vite";
import path from "node:path";
import { fileURLToPath, URL } from "node:url";
import { defineConfig } from "vite";

export default defineConfig({
  resolve: {
    alias: {
      "@": fileURLToPath(new URL("./src", import.meta.url)),
      "@bindings": path.resolve("./bindings"),
    },
  },
  build: {
    target: ["es2019", "safari13"],
    cssTarget: "safari13",
  },
  plugins: [wails("./bindings"), vue()],
});