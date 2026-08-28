package bridge

import (
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/certs"
	"cursor/internal/client"
	"cursor/internal/mitm"
	"cursor/internal/subscriptionauth"
	"runtime"
)

// Public DTOs remain in package main for Wails service compatibility.
// ProxyState 定义了当前模块中的 ProxyState 类型。
type ProxyState = client.ProxyState

// UserConfig 定义了当前模块中的 UserConfig 类型。
type UserConfig = client.UserConfig

// ModelAdapterConfig 定义模型测速使用的模型配置结构。
type ModelAdapterConfig = serverconfig.ModelAdapterConfig

// ModelAdapterTestResult 定义一次模型测速结果。
type ModelAdapterTestResult = client.ModelAdapterTestResult

// ModelAdapterTestResultsPayload 定义测速结果事件载荷。
type ModelAdapterTestResultsPayload = client.ModelAdapterTestResultsPayload

// ModelAdapterModelsRequest 定义模型列表查询请求。
type ModelAdapterModelsRequest = client.ModelAdapterModelsRequest

// ModelAdapterModelsResult 定义模型列表查询结果。
type ModelAdapterModelsResult = client.ModelAdapterModelsResult

// CursorAccountStatus 是可安全展示给桌面前端的独立 Cursor 账号状态。
type CursorAccountStatus = client.CursorAccountStatus

type SubscriptionAccountStatus = subscriptionauth.AccountStatus
type SubscriptionUsageSnapshot = subscriptionauth.UsageSnapshot
type GrokDeviceCode = subscriptionauth.GrokDeviceCode
type GrokPollInput = subscriptionauth.GrokPollInput
type CodexDeviceCode = subscriptionauth.CodexDeviceCode
type CodexPollInput = subscriptionauth.CodexPollInput
type SubscriptionPollResult = subscriptionauth.PollResult

// LicenseActionRequest 定义了当前模块中的 LicenseActionRequest 类型。
type LicenseActionRequest = client.LicenseActionRequest

// LicenseSwitchDeviceRequest 定义了当前模块中的 LicenseSwitchDeviceRequest 类型。
type LicenseSwitchDeviceRequest = client.LicenseSwitchDeviceRequest

// LicenseAPIResult 定义了当前模块中的 LicenseAPIResult 类型。
type LicenseAPIResult = client.LicenseAPIResult

// UsageRecordsRequest 定义了当前模块中的 UsageRecordsRequest 类型。
type UsageRecordsRequest = client.UsageRecordsRequest

// UsageRecord 定义了当前模块中的 UsageRecord 类型。
type UsageRecord = client.UsageRecord

// UsageRecordsData 定义了当前模块中的 UsageRecordsData 类型。
type UsageRecordsData = client.UsageRecordsData

// UsageRecordsResult 定义了当前模块中的 UsageRecordsResult 类型。
type UsageRecordsResult = client.UsageRecordsResult

// LogCaptureStatus 定义当前客户端采集状态。
type LogCaptureStatus = client.LogCaptureStatus

// LogCleanupResult 定义已关闭采集 session 的清理结果。
type LogCleanupResult = client.LogCleanupResult

// ProxyService 定义了当前模块中的 ProxyService 类型。
type ProxyService struct {
	// core 表示当前声明中的 core。
	core *client.ProxyService
}

// NewProxyService 用于处理与 NewProxyService 相关的逻辑。
func NewProxyService(proxy *mitm.ProxyServer, certManager *certs.Manager, caCertPEM []byte) *ProxyService {
	return &ProxyService{core: client.NewProxyService(proxy, certManager, caCertPEM)}
}

// StartProxy 用于处理与 StartProxy 相关的逻辑。
func (s *ProxyService) StartProxy() (ProxyState, error) {
	return s.core.StartProxy()
}

// StopProxy 用于处理与 StopProxy 相关的逻辑。
func (s *ProxyService) StopProxy() (ProxyState, error) {
	return s.core.StopProxy()
}

// StartGateway 只启动独立模型 Gateway，不启动 Cursor backend 或 MITM。
func (s *ProxyService) StartGateway() (ProxyState, error) {
	return s.core.StartGateway()
}

// StopGateway 只停止独立模型 Gateway。
func (s *ProxyService) StopGateway() (ProxyState, error) {
	return s.core.StopGateway()
}

// GetState 用于处理与 GetState 相关的逻辑。
func (s *ProxyService) GetState() ProxyState {
	return s.core.GetState()
}

// ClearLastError 用于处理与 ClearLastError 相关的逻辑。
func (s *ProxyService) ClearLastError() ProxyState {
	return s.core.ClearLastError()
}

// SetBaseURL 用于处理与 SetBaseURL 相关的逻辑。
func (s *ProxyService) SetBaseURL(baseURL string) (ProxyState, error) {
	return s.core.SetBaseURL(baseURL)
}

// LoadUserConfig 返回给前端的配置不含 Gateway token 明文。
func (s *ProxyService) LoadUserConfig() (UserConfig, error) {
	cfg, err := s.core.LoadUserConfig()
	if err != nil {
		return cfg, err
	}
	return serverconfig.RedactGatewayTokenForUI(cfg), nil
}

// SaveUserConfig 用于处理与 SaveUserConfig 相关的逻辑。
func (s *ProxyService) SaveUserConfig(cfg UserConfig) error {
	return s.core.SaveUserConfig(cfg)
}

// SaveGatewayConfig 只保存 Gateway 配置块，避免覆盖其他页面草稿。
func (s *ProxyService) SaveGatewayConfig(cfg UserConfig) error {
	return s.core.SaveGatewayConfig(cfg)
}

// SaveModelAdapters 只保存上游模型配置块。
func (s *ProxyService) SaveModelAdapters(cfg UserConfig) error {
	return s.core.SaveModelAdapters(cfg)
}

// SaveCursorConfig 只保存 Cursor 集成配置块。
func (s *ProxyService) SaveCursorConfig(cfg UserConfig) error {
	return s.core.SaveCursorConfig(cfg)
}

// SaveSystemSettings 只保存系统设置配置块。
func (s *ProxyService) SaveSystemSettings(cfg UserConfig) error {
	return s.core.SaveSystemSettings(cfg)
}

// SaveHomeMetrics 只保存首页统计口径。
func (s *ProxyService) SaveHomeMetrics(cfg UserConfig) error {
	return s.core.SaveHomeMetrics(cfg)
}

func (s *ProxyService) ExportUserConfig(path string) (string, error) {
	return s.core.ExportUserConfig(path)
}

// ImportUserConfig 从 YAML 文件校验并替换当前完整配置。
func (s *ProxyService) ImportUserConfig(path string) (UserConfig, error) {
	cfg, err := s.core.ImportUserConfig(path)
	if err != nil {
		return cfg, err
	}
	return serverconfig.RedactGatewayTokenForUI(cfg), nil
}

// GetCursorAccountStatus 返回 cursor-byok 独立 Cursor 账号的脱敏状态。
func (s *ProxyService) GetCursorAccountStatus() CursorAccountStatus {
	return s.core.GetCursorAccountStatus()
}

// StartCursorAccountLogin 打开官方浏览器登录并异步等待结果。
func (s *ProxyService) StartCursorAccountLogin() (CursorAccountStatus, error) {
	return s.core.StartCursorAccountLogin()
}

// DisconnectCursorAccount 只断开 cursor-byok 自己的账号。
func (s *ProxyService) DisconnectCursorAccount() (CursorAccountStatus, error) {
	return s.core.DisconnectCursorAccount()
}

func (s *ProxyService) GetCodexAuthStatus() SubscriptionAccountStatus {
	return s.core.GetCodexAuthStatus()
}

func (s *ProxyService) ImportCodexAuth(path string) (SubscriptionAccountStatus, error) {
	return s.core.ImportCodexAuth(path)
}

func (s *ProxyService) ClearCodexAuth() (SubscriptionAccountStatus, error) {
	return s.core.ClearCodexAuth()
}

func (s *ProxyService) StartCodexDeviceAuth() (CodexDeviceCode, error) {
	return s.core.StartCodexDeviceAuth()
}

func (s *ProxyService) PollCodexDeviceAuth(input CodexPollInput) (SubscriptionPollResult, error) {
	return s.core.PollCodexDeviceAuth(input)
}

func (s *ProxyService) StartGrokDeviceAuth() (GrokDeviceCode, error) {
	return s.core.StartGrokDeviceAuth()
}

func (s *ProxyService) PollGrokDeviceAuth(input GrokPollInput) (SubscriptionPollResult, error) {
	return s.core.PollGrokDeviceAuth(input)
}

func (s *ProxyService) ListSubscriptionAccounts(provider string) ([]SubscriptionAccountStatus, error) {
	return s.core.ListSubscriptionAccounts(provider)
}

func (s *ProxyService) ActivateSubscriptionAccount(accountID string) (SubscriptionAccountStatus, error) {
	return s.core.ActivateSubscriptionAccount(accountID)
}

func (s *ProxyService) DeleteSubscriptionAccount(accountID string) error {
	return s.core.DeleteSubscriptionAccount(accountID)
}

func (s *ProxyService) RefreshSubscriptionUsage(provider string) (SubscriptionUsageSnapshot, error) {
	return s.core.RefreshSubscriptionUsage(provider)
}

// TestModelAdapter 用于处理与 TestModelAdapter 相关的逻辑。
func (s *ProxyService) TestModelAdapter(adapter ModelAdapterConfig) (ModelAdapterTestResult, error) {
	return s.core.TestModelAdapter(adapter)
}

// GetModelAdapterTestResults 用于处理与 GetModelAdapterTestResults 相关的逻辑。
func (s *ProxyService) GetModelAdapterTestResults() []ModelAdapterTestResult {
	return s.core.GetModelAdapterTestResults()
}

func (s *ProxyService) GetLogCaptureStatus() LogCaptureStatus {
	return s.core.GetLogCaptureStatus()
}

func (s *ProxyService) CleanupClosedLogSessions() (LogCleanupResult, error) {
	return s.core.CleanupClosedLogSessions()
}

// FetchModelAdapterModels 用于从模型服务读取可用模型列表。
func (s *ProxyService) FetchModelAdapterModels(input ModelAdapterModelsRequest) (ModelAdapterModelsResult, error) {
	return s.core.FetchModelAdapterModels(input)
}

// GetDeviceID 用于处理与 GetDeviceID 相关的逻辑。
func (s *ProxyService) GetDeviceID() (string, error) {
	return s.core.GetDeviceID()
}

// ActivateLicense 用于处理与 ActivateLicense 相关的逻辑。
func (s *ProxyService) ActivateLicense(req LicenseActionRequest) (LicenseAPIResult, error) {
	return s.core.ActivateLicense(req)
}

// BindLicenseDevice 用于处理与 BindLicenseDevice 相关的逻辑。
func (s *ProxyService) BindLicenseDevice(req LicenseActionRequest) (LicenseAPIResult, error) {
	return s.core.BindLicenseDevice(req)
}

// SwitchLicenseDevice 用于处理与 SwitchLicenseDevice 相关的逻辑。
func (s *ProxyService) SwitchLicenseDevice(req LicenseSwitchDeviceRequest) (LicenseAPIResult, error) {
	return s.core.SwitchLicenseDevice(req)
}

// QueryUsageRecords 用于处理与 QueryUsageRecords 相关的逻辑。
func (s *ProxyService) QueryUsageRecords(req UsageRecordsRequest) (UsageRecordsResult, error) {
	return s.core.QueryUsageRecords(req)
}

// ApplyCursorSettings 用于处理与 ApplyCursorSettings 相关的逻辑。
func (s *ProxyService) ApplyCursorSettings() error {
	return s.core.ApplyCursorSettings()
}

// ClearCursorSettings 用于处理与 ClearCursorSettings 相关的逻辑。
func (s *ProxyService) ClearCursorSettings() error {
	return s.core.ClearCursorSettings()
}

// ShutdownForQuit 用于处理与 ShutdownForQuit 相关的逻辑。
func (s *ProxyService) ShutdownForQuit() {
	s.core.ShutdownForQuit()
}

// ShutdownForQuitFrom 以指定 initiator 执行进程退出清理。
func (s *ProxyService) ShutdownForQuitFrom(initiator string) {
	s.core.ShutdownForQuitFrom(initiator)
}

// CopyGatewayToken 仅通过显式复制接口返回 Gateway token。
func (s *ProxyService) CopyGatewayToken() (string, error) {
	return s.core.CopyGatewayToken()
}

// RotateGatewayToken 轮换 Gateway token，并只把新 token 返回给这次调用。
func (s *ProxyService) RotateGatewayToken() (string, error) {
	return s.core.RotateGatewayToken()
}

// IsWindows 用于处理与 IsWindows 相关的逻辑。
func (s *ProxyService) IsWindows() bool {
	return runtime.GOOS == "windows"
}
