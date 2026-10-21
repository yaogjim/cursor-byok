package updater

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"cursor/internal/buildinfo"
	"cursor/internal/logger"
	"cursor/internal/netproxy"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	manifestTimeout       = 90 * time.Second
	downloadTimeout       = 30 * time.Minute
	maxUpdateArchiveBytes = 512 << 20
	maxManifestBytes      = 1 << 20
	manifestMaxAttempts   = 3
	downloadMaxAttempts   = 4
)

// 更新请求的重试退避：用变量承载，便于测试压缩等待时间。
var (
	retryBaseDelay = 700 * time.Millisecond
	retryMaxDelay  = 5 * time.Second
)

var (
	errArchiveSizeMismatch = errors.New("archive size mismatch")
	errArchiveChecksum     = errors.New("archive checksum mismatch")
)

var allowedRedirectHosts = map[string]struct{}{
	"github.com":                            {},
	"release-assets.githubusercontent.com":  {},
	"objects.githubusercontent.com":         {},
	"github-releases.githubusercontent.com": {},
}

type State string

const (
	StateIdle        State = "idle"
	StateChecking    State = "checking"
	StateAvailable   State = "available"
	StateDownloading State = "downloading"
	StateReady       State = "ready"
	StateInstalling  State = "installing"
	StateError       State = "error"
)

var errNoSupportedAsset = errors.New("no supported update asset for current platform")

type manifest struct {
	Version      string                      `json:"version"`
	ReleaseDate  string                      `json:"release_date"`
	ReleaseNotes string                      `json:"release_notes"`
	Platforms    map[string]manifestPlatform `json:"platforms"`
	Mandatory    bool                        `json:"mandatory"`
}

type manifestPlatform struct {
	URL      string `json:"url"`
	Size     int64  `json:"size"`
	Checksum string `json:"checksum"`
}

type UpdateInfo struct {
	Version      string
	ReleaseDate  string
	ReleaseNotes string
	Mandatory    bool
	PlatformKey  string
	Asset        manifestPlatform
}

type Manager struct {
	app *application.App

	client *http.Client
	ctx    context.Context
	cancel context.CancelFunc

	mu             sync.Mutex
	state          State
	currentInfo    *UpdateInfo
	readyInfo      *UpdateInfo
	downloadedPath string
	closed         bool
}

func NewManager(app *application.App) *Manager {
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		app:    app,
		client: newUpdateHTTPClient(5 * time.Minute),
		ctx:    ctx,
		cancel: cancel,
		state:  StateIdle,
	}
}

func newUpdateHTTPClient(timeout time.Duration) *http.Client {
	client := netproxy.NewHTTPClient(timeout)
	client.CheckRedirect = func(request *http.Request, _ []*http.Request) error {
		if !isAllowedUpdateRedirect(request) {
			host := ""
			if request != nil && request.URL != nil {
				host = request.URL.Host
			}
			return fmt.Errorf("更新请求重定向到不受信任的地址: %s", host)
		}
		return nil
	}
	return client
}

func isAllowedUpdateHost(host string) bool {
	_, ok := allowedRedirectHosts[strings.ToLower(strings.TrimSpace(host))]
	return ok
}

func isAllowedUpdateRedirect(request *http.Request) bool {
	if request == nil || request.URL == nil || request.URL.Scheme != "https" || request.URL.Port() != "" {
		return false
	}
	return isAllowedUpdateHost(request.URL.Hostname())
}

func (m *Manager) Start(checkOnStartup bool) {
	m.emitState(StateIdle, nil, "", "", false, "")
	if checkOnStartup {
		m.CheckNow(false)
	}
}

func (m *Manager) Shutdown() {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	m.closed = true
	m.mu.Unlock()

	m.cancel()
	m.cleanupDownloadedFile()
}

func (m *Manager) CheckNow(manual bool) {
	go m.checkNow(manual)
}

func (m *Manager) DownloadAvailableUpdate() error {
	return m.startAvailableDownload()
}

func (m *Manager) InstallReadyUpdate() error {
	return m.installReadyUpdate()
}

func (m *Manager) checkNow(manual bool) {
	if err := m.ctx.Err(); err != nil {
		return
	}

	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return
	}
	switch m.state {
	case StateReady:
		info := m.readyInfo
		m.mu.Unlock()
		m.emitReady(info, true)
		return
	case StateAvailable:
		info := m.currentInfo
		m.mu.Unlock()
		m.emitAvailable(info, true)
		return
	case StateChecking, StateDownloading, StateInstalling:
		state := m.state
		info := m.currentInfo
		m.mu.Unlock()
		if manual {
			m.emitState(state, info, "", fmt.Sprintf("当前正在%s，请稍后再试。", stateLabel(state)), true, "idle")
		}
		return
	default:
		m.state = StateChecking
		m.currentInfo = nil
		m.readyInfo = nil
		m.downloadedPath = ""
		m.mu.Unlock()
	}
	m.emitState(StateChecking, nil, "", "", false, "")

	ctx, cancel := context.WithTimeout(m.ctx, manifestTimeout)
	defer cancel()

	info, err := m.fetchUpdateInfo(ctx)
	if err != nil {
		logger.Errorf("检查更新失败: %v", err)
		m.setState(StateError, nil, "")
		m.emitError(nil, "check", err.Error(), manual)
		return
	}
	if info == nil {
		m.setState(StateIdle, nil, "")
		m.emitState(StateIdle, nil, "", fmt.Sprintf("当前已是最新版本（v%s）。", buildinfo.CurrentVersion()), manual, "idle")
		return
	}

	logger.Infof("发现新版本：current=%s latest=%s platform=%s", buildinfo.CurrentVersion(), info.Version, info.PlatformKey)
	m.setState(StateAvailable, info, "")
	m.emitAvailable(info, true)
}

func (m *Manager) startAvailableDownload() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("更新管理器已关闭")
	}
	// 下载失败（StateError）后仍保留 currentInfo，允许用户直接重试下载
	if (m.state != StateAvailable && m.state != StateError) || m.currentInfo == nil {
		m.mu.Unlock()
		return errors.New("当前没有等待下载的更新")
	}
	info := m.currentInfo
	m.state = StateDownloading
	m.readyInfo = nil
	m.downloadedPath = ""
	m.mu.Unlock()

	m.emitState(StateDownloading, info, "", "", false, "")
	go m.downloadAvailable(info)
	return nil
}

func (m *Manager) downloadAvailable(info *UpdateInfo) {
	ctx, cancel := context.WithTimeout(m.ctx, downloadTimeout)
	defer cancel()

	archivePath, err := m.downloadUpdate(ctx, info)
	if err != nil {
		logger.Errorf("下载更新失败: %v", err)
		m.mu.Lock()
		closed := m.closed
		m.mu.Unlock()
		if !closed {
			m.setState(StateError, info, "")
			m.emitError(info, "download", err.Error(), true)
		}
		return
	}

	m.mu.Lock()
	if m.closed || m.ctx.Err() != nil {
		m.mu.Unlock()
		_ = os.Remove(archivePath)
		return
	}
	m.state = StateReady
	m.currentInfo = info
	m.readyInfo = info
	m.downloadedPath = archivePath
	m.mu.Unlock()
	m.emitState(StateReady, info, "", "", false, "")
	m.emitReady(info, true)
}

func (m *Manager) fetchUpdateInfo(ctx context.Context) (*UpdateInfo, error) {
	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= manifestMaxAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepWithContext(ctx, retryDelay(attempt)); err != nil {
				return nil, manifestFetchError(lastErr, attempts)
			}
			logger.Infof("重试获取更新清单：attempt=%d/%d", attempt, manifestMaxAttempts)
		}
		attempts = attempt
		info, retryable, err := m.fetchUpdateInfoOnce(ctx)
		if err == nil {
			return info, nil
		}
		lastErr = err
		// 语义类错误（版本未更新、缺少平台资产、清单字段非法）重试无意义
		if !retryable || ctx.Err() != nil {
			return nil, err
		}
		logger.Errorf("获取更新清单失败，准备重试：attempt=%d/%d err=%v", attempt, manifestMaxAttempts, err)
	}
	return nil, manifestFetchError(lastErr, attempts)
}

func (m *Manager) fetchUpdateInfoOnce(ctx context.Context) (*UpdateInfo, bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildinfo.UpdateBaseURL+"update.json", nil)
	if err != nil {
		return nil, false, err
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return nil, true, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, isRetryableStatus(resp.StatusCode), fmt.Errorf("update manifest request failed: %s", resp.Status)
	}

	// 整体读出再解析：被截断的响应体会表现为 unexpected EOF，可直接判定为可重试。
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxManifestBytes))
	if err != nil {
		return nil, true, err
	}

	var data manifest
	if err := json.Unmarshal(body, &data); err != nil {
		return nil, true, err
	}

	if compareVersions(data.Version, buildinfo.CurrentVersion()) <= 0 {
		return nil, false, nil
	}

	platformKey, err := currentPlatformKey()
	if err != nil {
		return nil, false, err
	}

	asset, ok := data.Platforms[platformKey]
	if !ok {
		return nil, false, errNoSupportedAsset
	}
	if err := validateUpdateAsset(asset); err != nil {
		return nil, false, err
	}

	return &UpdateInfo{
		Version:      strings.TrimSpace(data.Version),
		ReleaseDate:  strings.TrimSpace(data.ReleaseDate),
		ReleaseNotes: strings.TrimSpace(data.ReleaseNotes),
		Mandatory:    data.Mandatory,
		PlatformKey:  platformKey,
		Asset:        asset,
	}, true, nil
}

// isRetryableStatus 判断 HTTP 状态码是否值得重试（5xx 与 429 视为临时故障）。
func isRetryableStatus(statusCode int) bool {
	if statusCode == http.StatusTooManyRequests {
		return true
	}
	return statusCode >= 500
}

// retryDelay 返回第 attempt 次尝试前应等待的退避时长（指数退避，上限 retryMaxDelay）。
func retryDelay(attempt int) time.Duration {
	if attempt <= 1 {
		return 0
	}
	delay := retryBaseDelay
	for i := 2; i < attempt; i++ {
		delay *= 2
		if delay >= retryMaxDelay {
			return retryMaxDelay
		}
	}
	if delay > retryMaxDelay {
		return retryMaxDelay
	}
	return delay
}

func sleepWithContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return ctx.Err()
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func manifestFetchError(cause error, attempts int) error {
	if cause == nil {
		return errors.New("获取更新清单失败")
	}
	return fmt.Errorf("获取更新清单失败（已尝试 %d 次）：%s；底层错误：%w", attempts, transientNetworkHint(cause), cause)
}

// transientNetworkHint 把底层网络错误翻译成用户能理解的中文原因。
func transientNetworkHint(err error) string {
	switch {
	case err == nil:
		return "未知错误"
	case errors.Is(err, context.Canceled):
		return "操作已取消"
	case errors.Is(err, context.DeadlineExceeded):
		return "网络请求超时"
	case errors.Is(err, io.ErrUnexpectedEOF), errors.Is(err, io.EOF):
		return "网络连接在数据接收完成前被中断，常见于代理、VPN 或运营商对 GitHub 连接的干扰"
	}
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "connection reset"), strings.Contains(message, "broken pipe"),
		strings.Contains(message, "server closed idle connection"), strings.Contains(message, "unexpected eof"):
		return "网络连接被对端提前断开"
	case strings.Contains(message, "tls:"), strings.Contains(message, "handshake"):
		return "TLS 握手失败，可能是代理证书或网络中间设备干扰"
	case strings.Contains(message, "no such host"), strings.Contains(message, "i/o timeout"),
		strings.Contains(message, "timeout"):
		return "网络不可达或超时"
	}
	return "网络请求失败"
}

// downloadUpdate 下载更新包，失败时按指数退避重试。
// 传输中断（如 unexpected EOF）保留已写入的字节并用 Range 续传；
// 大小或校验值不一致说明数据不可信，下一次尝试从零重新下载。
func (m *Manager) downloadUpdate(ctx context.Context, info *UpdateInfo) (string, error) {
	tempFile, err := os.CreateTemp("", "cursor-byok-update-*"+archiveSuffix(info.Asset.URL))
	if err != nil {
		return "", err
	}
	archivePath := tempFile.Name()
	discard := func(failure error) (string, error) {
		_ = tempFile.Close()
		_ = os.Remove(archivePath)
		return "", failure
	}

	var lastErr error
	attempts := 0
	restartFromScratch := false
	for attempt := 1; attempt <= downloadMaxAttempts; attempt++ {
		if attempt > 1 {
			if err := sleepWithContext(ctx, retryDelay(attempt)); err != nil {
				return discard(downloadFailure(lastErr, attempts))
			}
			logger.Infof("重试下载更新：version=%s attempt=%d/%d reason=%v", info.Version, attempt, downloadMaxAttempts, lastErr)
			if restartFromScratch {
				if err := tempFile.Truncate(0); err != nil {
					return discard(err)
				}
				restartFromScratch = false
			}
		}

		attempts = attempt
		err := m.downloadArchiveOnce(ctx, info, tempFile)
		if err == nil {
			err = verifyArchiveFile(tempFile, info)
		}
		if err == nil {
			_ = tempFile.Close()
			m.emitProgress(info, info.Asset.Size, info.Asset.Size)
			logger.Infof("更新包下载完成：version=%s path=%s", info.Version, archivePath)
			return archivePath, nil
		}
		if ctx.Err() != nil {
			return discard(ctx.Err())
		}
		// 大小或校验值不一致说明本地数据不可信，下一次从零重下而不是续传
		if errors.Is(err, errArchiveSizeMismatch) || errors.Is(err, errArchiveChecksum) {
			restartFromScratch = true
		}
		lastErr = err
		logger.Errorf("下载更新包失败：version=%s attempt=%d/%d err=%v", info.Version, attempt, downloadMaxAttempts, err)
	}

	return discard(downloadFailure(lastErr, attempts))
}

// downloadArchiveOnce 执行一次下载尝试。文件非空时用 Range 续传，
// 服务器忽略 Range 时自动回退到从头覆盖。
func (m *Manager) downloadArchiveOnce(ctx context.Context, info *UpdateInfo, tempFile *os.File) error {
	resumeOffset, err := tempFile.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, info.Asset.URL, nil)
	if err != nil {
		return err
	}
	if resumeOffset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeOffset))
	}

	resp, err := m.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	offset := resumeOffset
	switch resp.StatusCode {
	case http.StatusPartialContent:
		if err := validateContentRange(resp, info, offset); err != nil {
			// 续传起点不可信：丢掉本地残片，下次从头下载
			if truncateErr := tempFile.Truncate(0); truncateErr != nil {
				return truncateErr
			}
			return err
		}
	case http.StatusOK:
		offset = 0
		if err := tempFile.Truncate(0); err != nil {
			return err
		}
	default:
		return fmt.Errorf("download request failed: %s", resp.Status)
	}

	if resp.ContentLength > maxUpdateArchiveBytes {
		return fmt.Errorf("更新包超过大小限制: %d", resp.ContentLength)
	}
	if offset+resp.ContentLength > maxUpdateArchiveBytes && resp.ContentLength > 0 {
		return fmt.Errorf("更新包超过大小限制: %d", offset+resp.ContentLength)
	}
	if offset == 0 && resp.ContentLength > 0 && resp.ContentLength != info.Asset.Size {
		return fmt.Errorf("更新包 Content-Length 与 manifest 不一致: expected %d got %d", info.Asset.Size, resp.ContentLength)
	}
	if resp.ContentLength > 0 && offset+resp.ContentLength > info.Asset.Size {
		return fmt.Errorf("更新包大小超出 manifest 声明: expected %d got %d", info.Asset.Size, offset+resp.ContentLength)
	}

	if _, err := tempFile.Seek(offset, io.SeekStart); err != nil {
		return err
	}

	remaining := maxUpdateArchiveBytes + 1 - offset
	if remaining < 1 {
		return fmt.Errorf("更新包超过大小限制: %d", offset)
	}
	progress := newProgressWriter(func(downloaded int64) {
		m.emitProgress(info, downloaded, info.Asset.Size)
	})
	progress.downloaded = offset
	m.emitProgress(info, offset, info.Asset.Size)

	written, err := io.Copy(io.MultiWriter(tempFile, progress), io.LimitReader(resp.Body, remaining))
	if err != nil {
		// 传输被截断：保留已写入内容，下一次尝试续传
		return err
	}

	total := offset + written
	if total > maxUpdateArchiveBytes {
		return fmt.Errorf("更新包超过大小限制: %d", total)
	}
	if total != info.Asset.Size {
		return fmt.Errorf("更新包大小与 manifest 不一致: expected %d got %d: %w", info.Asset.Size, total, errArchiveSizeMismatch)
	}
	return nil
}

// verifyArchiveFile 从磁盘重新计算大小与 SHA-256，避免续传时增量哈希失配。
func verifyArchiveFile(tempFile *os.File, info *UpdateInfo) error {
	expectedChecksum := strings.TrimSpace(strings.TrimPrefix(info.Asset.Checksum, "sha256:"))
	if expectedChecksum == "" {
		return errors.New("更新包缺少 SHA-256 校验值")
	}
	if err := tempFile.Sync(); err != nil {
		return err
	}
	if _, err := tempFile.Seek(0, io.SeekStart); err != nil {
		return err
	}

	hasher := sha256.New()
	size, err := io.Copy(hasher, tempFile)
	if err != nil {
		return err
	}
	if size != info.Asset.Size {
		return fmt.Errorf("更新包大小与 manifest 不一致: expected %d got %d: %w", info.Asset.Size, size, errArchiveSizeMismatch)
	}
	actualChecksum := hex.EncodeToString(hasher.Sum(nil))
	if !strings.EqualFold(expectedChecksum, actualChecksum) {
		return fmt.Errorf("checksum mismatch: expected %s got %s: %w", expectedChecksum, actualChecksum, errArchiveChecksum)
	}
	return nil
}

func downloadFailure(cause error, attempts int) error {
	if cause == nil {
		return errors.New("更新包下载失败")
	}
	return fmt.Errorf("更新包下载失败（已尝试 %d 次）：%s；底层错误：%w", attempts, downloadFailureHint(cause), cause)
}

func downloadFailureHint(err error) string {
	switch {
	case errors.Is(err, errArchiveChecksum):
		return "下载内容与更新清单的校验值不一致，文件在传输中被破坏"
	case errors.Is(err, errArchiveSizeMismatch):
		return "下载内容大小与更新清单不一致"
	}
	return transientNetworkHint(err)
}

type contentRange struct {
	start int64
	end   int64
	total int64
}

func parseContentRange(raw string) (contentRange, bool) {
	value := strings.TrimSpace(raw)
	if !strings.HasPrefix(strings.ToLower(value), "bytes") {
		return contentRange{}, false
	}
	value = strings.TrimSpace(value[len("bytes"):])
	slash := strings.Index(value, "/")
	if slash < 0 {
		return contentRange{}, false
	}
	rangePart := strings.TrimSpace(value[:slash])
	totalPart := strings.TrimSpace(value[slash+1:])
	dash := strings.Index(rangePart, "-")
	if dash < 0 {
		return contentRange{}, false
	}
	start, err := strconv.ParseInt(strings.TrimSpace(rangePart[:dash]), 10, 64)
	if err != nil || start < 0 {
		return contentRange{}, false
	}
	result := contentRange{start: start}
	if end, err := strconv.ParseInt(strings.TrimSpace(rangePart[dash+1:]), 10, 64); err == nil && end >= start {
		result.end = end
	}
	if totalPart != "*" {
		if total, err := strconv.ParseInt(totalPart, 10, 64); err == nil && total > 0 {
			result.total = total
		}
	}
	return result, true
}

// validateContentRange 校验续传响应的起点与总大小，防止拼接出错误的文件。
func validateContentRange(resp *http.Response, info *UpdateInfo, offset int64) error {
	raw := strings.TrimSpace(resp.Header.Get("Content-Range"))
	if raw == "" {
		// 个别代理会移除该响应头，退化为按最终大小与校验值校验
		return nil
	}
	parsed, ok := parseContentRange(raw)
	if !ok {
		return fmt.Errorf("服务器返回了无法解析的 Content-Range: %s: %w", raw, errArchiveSizeMismatch)
	}
	if parsed.start != offset {
		return fmt.Errorf("续传起点与本地残片不一致: expected %d got %d: %w", offset, parsed.start, errArchiveSizeMismatch)
	}
	if parsed.total > 0 && parsed.total != info.Asset.Size {
		return fmt.Errorf("更新包总大小与 manifest 不一致: expected %d got %d: %w", info.Asset.Size, parsed.total, errArchiveSizeMismatch)
	}
	return nil
}

func (m *Manager) installReadyUpdate() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return errors.New("更新管理器已关闭")
	}
	if m.state != StateReady || m.readyInfo == nil || m.downloadedPath == "" {
		m.mu.Unlock()
		return errors.New("当前没有可安装的更新")
	}
	info := m.readyInfo
	archivePath := m.downloadedPath
	m.state = StateInstalling
	m.mu.Unlock()
	m.emitState(StateInstalling, info, "", "", false, "")

	logger.Infof("开始安装更新：version=%s path=%s", info.Version, archivePath)

	if err := m.spawnInstaller(archivePath); err != nil {
		m.setState(StateReady, info, archivePath)
		m.emitState(StateReady, info, "", "", false, "")
		m.emitError(info, "install", err.Error(), true)
		return err
	}

	m.app.Quit()
	return nil
}

func (m *Manager) spawnInstaller(archivePath string) error {
	switch runtime.GOOS {
	case "darwin":
		return m.spawnDarwinInstaller(archivePath)
	case "linux":
		return m.spawnLinuxInstaller(archivePath)
	case "windows":
		return m.spawnWindowsInstaller(archivePath)
	default:
		return fmt.Errorf("unsupported updater platform: %s", runtime.GOOS)
	}
}

func (m *Manager) spawnDarwinInstaller(archivePath string) error {
	executablePath, err := os.Executable()
	if err != nil {
		return err
	}
	executablePath, err = filepath.Abs(executablePath)
	if err != nil {
		return err
	}

	appBundlePath, err := resolveMacBundlePath(executablePath)
	if err != nil {
		return err
	}

	scriptPath, err := writeHelperScript("cursor-byok-update-*.sh", darwinInstallerScript)
	if err != nil {
		return err
	}

	cmd := exec.Command(
		"/bin/sh",
		scriptPath,
		fmt.Sprintf("%d", os.Getpid()),
		archivePath,
		appBundlePath,
	)
	return cmd.Start()
}

func (m *Manager) spawnWindowsInstaller(archivePath string) error {
	executablePath, err := os.Executable()
	if err != nil {
		return err
	}
	executablePath, err = filepath.Abs(executablePath)
	if err != nil {
		return err
	}

	scriptPath, err := writeHelperScript("cursor-byok-update-*.ps1", windowsInstallerScript)
	if err != nil {
		return err
	}

	cmd := exec.Command(
		"powershell",
		"-NoProfile",
		"-ExecutionPolicy",
		"Bypass",
		"-WindowStyle",
		"Hidden",
		"-File",
		scriptPath,
		"-PidToWait",
		fmt.Sprintf("%d", os.Getpid()),
		"-ArchivePath",
		archivePath,
		"-TargetExecutable",
		executablePath,
	)
	return cmd.Start()
}

func (m *Manager) spawnLinuxInstaller(archivePath string) error {
	executablePath, err := os.Executable()
	if err != nil {
		return err
	}
	executablePath, err = filepath.Abs(executablePath)
	if err != nil {
		return err
	}

	if err := ensureWritableDirectory(filepath.Dir(executablePath)); err != nil {
		return fmt.Errorf("当前 Linux 安装目录不可写，无法执行原地更新: %w", err)
	}

	scriptPath, err := writeHelperScript("cursor-byok-update-*.sh", linuxInstallerScript)
	if err != nil {
		return err
	}

	cmd := exec.Command(
		"/bin/sh",
		scriptPath,
		fmt.Sprintf("%d", os.Getpid()),
		archivePath,
		executablePath,
	)
	return cmd.Start()
}

func (m *Manager) emitState(state State, info *UpdateInfo, errMsg, message string, prompt bool, promptKind string) {
	if m.app == nil {
		return
	}

	payload := StatePayload{
		State:      string(state),
		Error:      strings.TrimSpace(errMsg),
		Message:    strings.TrimSpace(message),
		Prompt:     prompt,
		PromptKind: strings.TrimSpace(promptKind),
	}
	if info != nil {
		payload.Version = info.Version
		payload.ReleaseDate = info.ReleaseDate
		payload.ReleaseNotes = info.ReleaseNotes
		payload.Mandatory = info.Mandatory
	}
	m.app.Event.Emit(EventState, payload)
}

func (m *Manager) emitProgress(info *UpdateInfo, downloaded, total int64) {
	if m.app == nil {
		return
	}

	if total < 0 {
		total = 0
	}
	percentage := 0.0
	if total > 0 {
		percentage = (float64(downloaded) / float64(total)) * 100
		if percentage > 100 {
			percentage = 100
		}
	}

	payload := ProgressPayload{
		State:      string(StateDownloading),
		Downloaded: downloaded,
		Total:      total,
		Percentage: percentage,
	}
	if info != nil {
		payload.Version = info.Version
	}
	m.app.Event.Emit(EventProgress, payload)
}

func (m *Manager) emitAvailable(info *UpdateInfo, prompt bool) {
	m.emitState(StateAvailable, info, "", "", prompt, "available")
}

func (m *Manager) emitReady(info *UpdateInfo, prompt bool) {
	if m.app == nil || info == nil {
		return
	}

	payload := ReadyPayload{
		State:        string(StateReady),
		Version:      info.Version,
		ReleaseDate:  info.ReleaseDate,
		ReleaseNotes: info.ReleaseNotes,
		Mandatory:    info.Mandatory,
		Prompt:       prompt,
		PromptKind:   "ready",
	}
	m.app.Event.Emit(EventReady, payload)
}

func (m *Manager) emitError(info *UpdateInfo, phase, errMsg string, prompt bool) {
	if m.app == nil {
		return
	}

	payload := ErrorPayload{
		State:      string(StateError),
		Phase:      strings.TrimSpace(phase),
		Error:      strings.TrimSpace(errMsg),
		Prompt:     prompt,
		PromptKind: "error",
	}
	if info != nil {
		payload.Version = info.Version
	}
	m.app.Event.Emit(EventError, payload)
}

func (m *Manager) cleanupDownloadedFile() {
	m.mu.Lock()
	if m.state == StateInstalling {
		m.mu.Unlock()
		return
	}
	archivePath := m.downloadedPath
	m.downloadedPath = ""
	m.readyInfo = nil
	m.mu.Unlock()
	if strings.TrimSpace(archivePath) != "" {
		_ = os.Remove(archivePath)
	}
}

func (m *Manager) setState(state State, info *UpdateInfo, archivePath string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.state = state
	m.currentInfo = info
	// readyInfo 只在“已下载可安装”路径持有，避免 available/error 误入安装语义
	if state == StateReady || state == StateInstalling {
		m.readyInfo = info
	} else {
		m.readyInfo = nil
	}
	m.downloadedPath = archivePath
}

func validateUpdateAsset(asset manifestPlatform) error {
	parsed, err := url.Parse(strings.TrimSpace(asset.URL))
	if err != nil {
		return fmt.Errorf("invalid update asset URL: %w", err)
	}
	if parsed.Scheme != "https" || !strings.EqualFold(parsed.Hostname(), "github.com") || parsed.Port() != "" {
		return errors.New("更新包地址必须使用 github.com HTTPS 发布地址")
	}
	expectedPathPrefix := "/" + buildinfo.ReleaseRepo + "/releases/download/"
	if !strings.HasPrefix(parsed.EscapedPath(), expectedPathPrefix) {
		return errors.New("更新包地址不属于当前项目的 GitHub Release")
	}
	if parsed.User != nil || strings.TrimSpace(parsed.RawQuery) != "" || strings.TrimSpace(parsed.Fragment) != "" {
		return errors.New("更新包地址包含不允许的凭据、查询参数或片段")
	}
	if asset.Size <= 0 || asset.Size > maxUpdateArchiveBytes {
		return fmt.Errorf("更新包大小必须在 1-%d 字节之间", maxUpdateArchiveBytes)
	}
	checksum := strings.TrimSpace(strings.TrimPrefix(asset.Checksum, "sha256:"))
	decoded, err := hex.DecodeString(checksum)
	if err != nil || len(decoded) != sha256.Size {
		return errors.New("更新包必须提供合法的 SHA-256 校验值")
	}
	return nil
}

func currentPlatformKey() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		switch runtime.GOARCH {
		case "arm64":
			return "macos-arm64", nil
		case "amd64":
			return "macos-amd64", nil
		}
	case "windows":
		if runtime.GOARCH == "amd64" {
			return "windows-amd64", nil
		}
	case "linux":
		if runtime.GOARCH == "amd64" {
			return "linux-amd64", nil
		}
	}
	return "", fmt.Errorf("unsupported platform for updater: %s/%s", runtime.GOOS, runtime.GOARCH)
}

func archiveSuffix(downloadURL string) string {
	if strings.HasSuffix(downloadURL, ".tar.gz") {
		return ".tar.gz"
	}
	return filepath.Ext(downloadURL)
}

func stateLabel(state State) string {
	switch state {
	case StateChecking:
		return "检查更新"
	case StateAvailable:
		return "等待下载确认"
	case StateDownloading:
		return "下载更新"
	case StateInstalling:
		return "安装更新"
	case StateError:
		return "更新失败"
	default:
		return string(state)
	}
}

func resolveMacBundlePath(executablePath string) (string, error) {
	contentsDir := filepath.Dir(filepath.Dir(executablePath))
	if filepath.Base(contentsDir) != "Contents" {
		return "", errors.New("当前 macOS 运行环境不是 .app 包，无法执行原地更新")
	}
	appBundlePath := filepath.Dir(contentsDir)
	if filepath.Ext(appBundlePath) != ".app" {
		return "", errors.New("无法定位 macOS 应用包路径")
	}
	return appBundlePath, nil
}

func writeHelperScript(pattern, content string) (string, error) {
	file, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	if _, err := file.WriteString(content); err != nil {
		_ = file.Close()
		_ = os.Remove(file.Name())
		return "", err
	}
	if err := file.Close(); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	if err := os.Chmod(file.Name(), 0o700); err != nil {
		_ = os.Remove(file.Name())
		return "", err
	}
	return file.Name(), nil
}

func ensureWritableDirectory(dir string) error {
	dir = strings.TrimSpace(dir)
	if dir == "" {
		return errors.New("target directory is empty")
	}

	probe, err := os.CreateTemp(dir, ".cursor-byok-writecheck-*")
	if err != nil {
		return err
	}
	name := probe.Name()
	if err := probe.Close(); err != nil {
		_ = os.Remove(name)
		return err
	}
	return os.Remove(name)
}

type progressWriter struct {
	onProgress func(downloaded int64)
	downloaded int64
	lastEmit   time.Time
}

func newProgressWriter(onProgress func(downloaded int64)) *progressWriter {
	return &progressWriter{
		onProgress: onProgress,
		lastEmit:   time.Now(),
	}
}

func (w *progressWriter) Write(p []byte) (int, error) {
	n := len(p)
	w.downloaded += int64(n)
	if w.onProgress != nil && time.Since(w.lastEmit) >= 120*time.Millisecond {
		w.lastEmit = time.Now()
		w.onProgress(w.downloaded)
	}
	return n, nil
}

const darwinInstallerScript = `#!/bin/sh
set -eu

PID_TO_WAIT="$1"
ARCHIVE_PATH="$2"
TARGET_APP="$3"
TMP_DIR="$(mktemp -d)"

cleanup() {
  rm -rf "$TMP_DIR"
  rm -f "$ARCHIVE_PATH"
  rm -f "$0"
}

trap cleanup EXIT

while kill -0 "$PID_TO_WAIT" 2>/dev/null; do
  sleep 1
done

tar -xzf "$ARCHIVE_PATH" -C "$TMP_DIR"

EXTRACTED_APP="$(find "$TMP_DIR" -maxdepth 1 -type d -name '*.app' | head -n 1)"
if [ -z "$EXTRACTED_APP" ]; then
  echo "No .app bundle found in archive" >&2
  exit 1
fi

rm -rf "$TARGET_APP"
mv "$EXTRACTED_APP" "$TARGET_APP"
open "$TARGET_APP"
`

const windowsInstallerScript = `param(
  [int]$PidToWait,
  [string]$ArchivePath,
  [string]$TargetExecutable
)

$ErrorActionPreference = "Stop"
$TargetDir = Split-Path -Parent $TargetExecutable
$TempDir = Join-Path ([System.IO.Path]::GetTempPath()) ("cursor-byok-update-" + [System.Guid]::NewGuid().ToString("N"))

New-Item -ItemType Directory -Path $TempDir | Out-Null

try {
  while (Get-Process -Id $PidToWait -ErrorAction SilentlyContinue) {
    Start-Sleep -Seconds 1
  }

  Expand-Archive -LiteralPath $ArchivePath -DestinationPath $TempDir -Force
  $ExtractedExecutables = @(Get-ChildItem -LiteralPath $TempDir -File | Where-Object { $_.Extension -ieq ".exe" })
  if ($ExtractedExecutables.Count -eq 0) {
    throw "No executable found in Windows update archive"
  }

  $TargetExecutableName = [System.IO.Path]::GetFileName($TargetExecutable)
  $PayloadExecutable = $ExtractedExecutables | Where-Object { $_.Name -ieq $TargetExecutableName } | Select-Object -First 1
  if ($null -eq $PayloadExecutable) {
    $PayloadExecutable = $ExtractedExecutables[0]
  }

  Get-ChildItem -LiteralPath $TempDir -Force | ForEach-Object {
    if ($_.PSIsContainer) {
      Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $TargetDir $_.Name) -Recurse -Force
    } elseif ($_.Extension -ine ".exe") {
      Copy-Item -LiteralPath $_.FullName -Destination (Join-Path $TargetDir $_.Name) -Force
    }
  }
  Copy-Item -LiteralPath $PayloadExecutable.FullName -Destination $TargetExecutable -Force
  Start-Process -FilePath $TargetExecutable
} finally {
  Remove-Item -LiteralPath $ArchivePath -Force -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath $TempDir -Recurse -Force -ErrorAction SilentlyContinue
  Remove-Item -LiteralPath $MyInvocation.MyCommand.Path -Force -ErrorAction SilentlyContinue
}
`

const linuxInstallerScript = `#!/bin/sh
set -eu

PID_TO_WAIT="$1"
ARCHIVE_PATH="$2"
TARGET_EXECUTABLE="$3"
TARGET_DIR="$(dirname "$TARGET_EXECUTABLE")"
TMP_DIR="$(mktemp -d)"
REPLACEMENT_PATH="${TARGET_EXECUTABLE}.new"

cleanup() {
  rm -rf "$TMP_DIR"
  rm -f "$ARCHIVE_PATH"
  rm -f "$REPLACEMENT_PATH"
  rm -f "$0"
}

trap cleanup EXIT

while kill -0 "$PID_TO_WAIT" 2>/dev/null; do
  sleep 1
done

tar -xzf "$ARCHIVE_PATH" -C "$TMP_DIR"

EXTRACTED_EXECUTABLE="$(find "$TMP_DIR" -maxdepth 1 -type f | head -n 1)"
if [ -z "$EXTRACTED_EXECUTABLE" ]; then
  echo "No executable found in Linux update archive" >&2
  exit 1
fi

cp "$EXTRACTED_EXECUTABLE" "$REPLACEMENT_PATH"
chmod +x "$REPLACEMENT_PATH"
mv -f "$REPLACEMENT_PATH" "$TARGET_EXECUTABLE"
chmod +x "$TARGET_EXECUTABLE"
cd "$TARGET_DIR"
nohup "$TARGET_EXECUTABLE" >/dev/null 2>&1 &
`
