import { ref } from "vue";
import { Dialogs } from "@wailsio/runtime";
import { showModal } from "@/composables/useModal";
import {
  appState,
  exportUserConfigToFile,
  importUserConfigFromFile,
  toUserError,
} from "@/state/appState";

const YAML_FILE_FILTER = [{ DisplayName: "YAML 配置文件", Pattern: "*.yaml;*.yml" }];

export function useConfigTransfer({ message, showActionError }) {
  const busy = ref(false);

  async function exportConfig() {
    const confirmed = await showModal({
      title: "导出完整配置",
      content: "导出文件会包含全部模型配置、API Key 和自定义请求头。请将文件保存在安全位置，避免泄露；Windows 上的访问权限取决于目标文件夹的安全设置。",
      confirmText: "继续导出",
      cancelText: "取消",
      showCancel: true,
    });
    if (!confirmed) {
      return;
    }

    try {
      const path = await Dialogs.SaveFile({
        Title: "导出 cursor-byok 配置",
        Filename: "cursor-byok-config.yaml",
        Filters: YAML_FILE_FILTER,
        CanCreateDirectories: true,
        AllowsOtherFiletypes: false,
      });
      if (!path) {
        return;
      }
      busy.value = true;
      const exportedPath = await exportUserConfigToFile(path);
      message(`配置已导出到 ${exportedPath}`);
    } catch (error) {
      showActionError("导出失败", toUserError(error));
    } finally {
      busy.value = false;
    }
  }

  async function importConfig() {
    if (appState.serviceRunning || appState.backendRunning || appState.proxyRunning) {
      showActionError("导入失败", "服务运行中不能导入完整配置，请先停止服务");
      return;
    }
    try {
      const path = await Dialogs.OpenFile({
        Title: "导入 cursor-byok 配置",
        Filters: YAML_FILE_FILTER,
        CanChooseFiles: true,
        CanChooseDirectories: false,
        AllowsMultipleSelection: false,
        AllowsOtherFiletypes: false,
      });
      if (!path) {
        return;
      }
      const confirmed = await showModal({
        title: "覆盖当前配置",
        content: "导入会替换当前完整配置，包括模型、API Key、服务地址和其他设置。建议先导出备份，是否继续？",
        confirmText: "确认导入",
        cancelText: "取消",
        showCancel: true,
      });
      if (!confirmed) {
        return;
      }

      busy.value = true;
      const imported = await importUserConfigFromFile(path);
      message(`配置导入成功，共导入 ${imported.modelAdapters.length} 个模型`);
    } catch (error) {
      showActionError("导入失败", toUserError(error));
    } finally {
      busy.value = false;
    }
  }

  return {
    configTransferBusy: busy,
    handleExportConfig: exportConfig,
    handleImportConfig: importConfig,
  };
}
