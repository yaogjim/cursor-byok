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
  return withApiLogging("FetchModelAdapterModels", () =>
    Call.ByName(`${PROXY_SERVICE_NAME}.FetchModelAdapterModels`, payload),
  );
}
