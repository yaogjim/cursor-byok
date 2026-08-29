import {
  DisconnectCursorAccount,
  GetCursorAccountStatus,
  GetState,
  LoadUserConfig,
  SaveUserConfig,
  StartCursorAccountLogin,
  StartProxy,
  StopProxy,
} from "@bindings/cursor/internal/bridge/proxyservice.js";
import {
  GetAdRuntime,
  OpenExternalURL as OpenAdExternalURL,
} from "@bindings/cursor/internal/bridge/adservice.js";
import { GetHomeMetricsSummary } from "@bindings/cursor/internal/bridge/metricsservice.js";
import {
  CheckForUpdates,
  DownloadAvailableUpdate,
  GetAppVersion,
  GetFooterAuthorInfo,
  InstallReadyUpdate,
  OpenConfigWindow,
  OpenFooterAuthorHome,
  OpenHistoryWindow,
  OpenModelConfigWindow,
} from "@bindings/cursor/internal/bridge/windowservice.js";
import { Call } from "@wailsio/runtime";

const API_LOG_PREFIX = "[clientApi]";
const PROXY_SERVICE_NAME = "cursor/internal/bridge.ProxyService";
const METRICS_SERVICE_NAME = "cursor/internal/bridge.MetricsService";
const WINDOW_SERVICE_NAME = "cursor/internal/bridge.WindowService";

function logSuccess(name) {
  console.debug(`${API_LOG_PREFIX} ${name} completed`);
}

function logError(name) {
  console.error(`${API_LOG_PREFIX} ${name} failed`);
}

function withApiLogging(name, runner) {
  return Promise.resolve()
    .then(() => runner())
    .then((result) => {
      logSuccess(name);
      return result;
    })
    .catch((error) => {
      logError(name);
      throw error;
    });
}

export function loadUserConfig() {
  return withApiLogging("LoadUserConfig", () => LoadUserConfig());
}

export function saveUserConfig(payload) {
  return withApiLogging("SaveUserConfig", () => SaveUserConfig(payload));
}

export function saveGatewayConfig(payload) {
  return withApiLogging("SaveGatewayConfig", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.SaveGatewayConfig`, payload),
  );
}

export function saveModelAdapters(payload) {
  return withApiLogging("SaveModelAdapters", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.SaveModelAdapters`, payload),
  );
}

export function saveCursorConfig(payload) {
  return withApiLogging("SaveCursorConfig", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.SaveCursorConfig`, payload),
  );
}

export function saveSystemSettings(payload) {
  return withApiLogging("SaveSystemSettings", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.SaveSystemSettings`, payload),
  );
}

export function saveHomeMetrics(payload) {
  return withApiLogging("SaveHomeMetrics", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.SaveHomeMetrics`, payload),
  );
}

export function copyGatewayToken() {
  return withApiLogging("CopyGatewayToken", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.CopyGatewayToken`),
  );
}


export function rotateGatewayToken() {
  return withApiLogging("RotateGatewayToken", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.RotateGatewayToken`),
  );
}

export function exportUserConfig(path) {
  return withApiLogging("ExportUserConfig", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ExportUserConfig`, path),
  );
}

export function importUserConfig(path) {
  return Call.ByName(`${PROXY_SERVICE_NAME}.ImportUserConfig`, path).then(
    (result) => {
      console.log(`${API_LOG_PREFIX} ImportUserConfig response`, {
        path,
        modelCount: Array.isArray(result?.modelAdapters) ? result.modelAdapters.length : 0,
      });
      return result;
    },
    (error) => {
      logError("ImportUserConfig", { path }, error);
      throw error;
    },
  );
}

export function getCursorAccountStatus() {
  return withApiLogging("GetCursorAccountStatus", () => GetCursorAccountStatus());
}

export function startCursorAccountLogin() {
  return withApiLogging("StartCursorAccountLogin", () => StartCursorAccountLogin());
}

export function disconnectCursorAccount() {
  return withApiLogging("DisconnectCursorAccount", () => DisconnectCursorAccount());
}

export function getProxyState() {
  return withApiLogging("GetState", () => GetState());
}

export function getHomeMetricsSummary() {
  return withApiLogging("GetHomeMetricsSummary", () => GetHomeMetricsSummary());
}

export function getHomeMetricsReport(range) {
  return withApiLogging("GetHomeMetricsReport", () =>
    Call.ByName(`${METRICS_SERVICE_NAME}.GetHomeMetricsReport`, range),
  );
}

export function resetHomeMetricsSummary() {
  return withApiLogging("ResetHomeMetricsSummary", () =>
    Call.ByName(`${METRICS_SERVICE_NAME}.ResetHomeMetricsSummary`),
  );
}

export function launchCursor() {
  return withApiLogging("LaunchCursor", () =>
    Call.ByName(`${WINDOW_SERVICE_NAME}.LaunchCursor`),
  );
}

export function getAdRuntime() {
  return GetAdRuntime();
}

export function openAdExternalURL(url) {
  return OpenAdExternalURL(url);
}

export function startProxyService() {
  return withApiLogging("StartProxy", () => StartProxy());
}

export function stopProxyService() {
  return withApiLogging("StopProxy", () => StopProxy());
}

export function startGatewayService() {
  return withApiLogging("StartGateway", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.StartGateway`),
  );
}

export function stopGatewayService() {
  return withApiLogging("StopGateway", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.StopGateway`),
  );
}

export function testGatewayService() {
  return withApiLogging("TestGateway", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.TestGateway`),
  );
}

export function getLogCaptureStatus() {
  return withApiLogging("GetLogCaptureStatus", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.GetLogCaptureStatus`),
  );
}

export function cleanupClosedLogSessions() {
  return withApiLogging("CleanupClosedLogSessions", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.CleanupClosedLogSessions`),
  );
}

export function openLogsDirectory() {
  return withApiLogging("OpenHistoryWindow", () => OpenHistoryWindow());
}

export function openConfigWindow() {
  return withApiLogging("OpenConfigWindow", () => OpenConfigWindow());
}

export function getAppVersion() {
  return withApiLogging("GetAppVersion", () => GetAppVersion());
}

export function getFooterAuthorInfo() {
  return withApiLogging("GetFooterAuthorInfo", () => GetFooterAuthorInfo());
}

export function checkForUpdates() {
  return withApiLogging("CheckForUpdates", () => CheckForUpdates());
}

export function downloadAvailableUpdate() {
  return withApiLogging("DownloadAvailableUpdate", () => DownloadAvailableUpdate());
}

export function installReadyUpdate() {
  return withApiLogging("InstallReadyUpdate", () => InstallReadyUpdate());
}

export function openFooterAuthorHome() {
  return withApiLogging("OpenFooterAuthorHome", () => OpenFooterAuthorHome());
}

export function openModelConfig() {
  return withApiLogging("OpenModelConfigWindow", () => OpenModelConfigWindow());
}

export function setAppearanceTheme(theme) {
  return withApiLogging("SetAppearanceTheme", () =>
    Call.ByName(`${WINDOW_SERVICE_NAME}.SetAppearanceTheme`, theme),
  );
}

export function testModelAdapter(adapter) {
  return Call.ByName(`${PROXY_SERVICE_NAME}.TestModelAdapter`, adapter).then(
    (result) => {
      logSuccess("TestModelAdapter");
      return result;
    },
    (error) => {
      logError("TestModelAdapter");
      throw error;
    },
  );
}

export function getModelAdapterTestResults() {
  return withApiLogging("GetModelAdapterTestResults", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.GetModelAdapterTestResults`),
  );
}

export function fetchModelAdapterModels(payload) {
  const source = payload && typeof payload === "object" ? payload : {};
  const credentialSource = String(source.credentialSource || "").trim().toLowerCase();
  const managed = credentialSource === "codex" || credentialSource === "grok";
  const request = {
    type: source.type,
    baseURL: source.baseURL,
    apiKey: managed ? "" : source.apiKey,
    credentialSource: credentialSource || "static",
    customHeadersEnabled: source.customHeadersEnabled,
    customHeadersJSON: source.customHeadersJSON,
  };
  return withApiLogging("FetchModelAdapterModels", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.FetchModelAdapterModels`, request),
  );
}

export function getCodexAuthStatus() {
  return withApiLogging("GetCodexAuthStatus", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.GetCodexAuthStatus`),
  );
}

export function importCodexAuth(path) {
  return withApiLogging("ImportCodexAuth", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ImportCodexAuth`, path),
  );
}

export function clearCodexAuth() {
  return withApiLogging("ClearCodexAuth", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ClearCodexAuth`),
  );
}

export function startCodexDeviceAuth() {
  return withApiLogging("StartCodexDeviceAuth", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.StartCodexDeviceAuth`),
  );
}

export function pollCodexDeviceAuth(input) {
  return withApiLogging("PollCodexDeviceAuth", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.PollCodexDeviceAuth`, input),
  );
}

export function startGrokDeviceAuth() {
  return withApiLogging("StartGrokDeviceAuth", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.StartGrokDeviceAuth`),
  );
}

export function pollGrokDeviceAuth(input) {
  return withApiLogging("PollGrokDeviceAuth", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.PollGrokDeviceAuth`, input),
  );
}

export function listSubscriptionAccounts(provider) {
  return withApiLogging("ListSubscriptionAccounts", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ListSubscriptionAccounts`, provider),
  );
}

export function activateSubscriptionAccount(accountID) {
  return withApiLogging("ActivateSubscriptionAccount", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ActivateSubscriptionAccount`, accountID),
  );
}

export function deleteSubscriptionAccount(accountID) {
  return withApiLogging("DeleteSubscriptionAccount", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.DeleteSubscriptionAccount`, accountID),
  );
}

export function refreshSubscriptionUsage(provider) {
  return withApiLogging("RefreshSubscriptionUsage", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.RefreshSubscriptionUsage`, provider),
  );
}

export function refreshSubscriptionAccountUsage(provider, accountID) {
  return withApiLogging("RefreshSubscriptionAccountUsage", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.RefreshSubscriptionAccountUsage`, provider, accountID),
  );
}

export function previewSub2APIImport(path, provider) {
  return withApiLogging("PreviewSub2APIImport", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.PreviewSub2APIImport`, path, provider),
  );
}

export function importSub2APIAccounts(path, provider, accountIDs) {
  return withApiLogging("ImportSub2APIAccounts", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.ImportSub2APIAccounts`, {
      path,
      provider,
      accountIds: Array.isArray(accountIDs) ? accountIDs : [],
    }),
  );
}
