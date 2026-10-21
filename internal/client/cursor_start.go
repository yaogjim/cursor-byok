package client

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cursor/internal/backend"
	"cursor/internal/cursor"
	"cursor/internal/logger"
	localruntime "cursor/internal/runtime"
)

const (
	cursorRestartConfirmationCode = "cursor_restart_confirmation_required"
	cursorQuitUnsupportedCode     = "cursor_quit_unsupported"
	cursorQuitTimeoutCode         = "cursor_quit_timeout"
	cursorLaunchPartialCode       = "cursor_launch_partial"
	cursorStartBusyCode           = "cursor_start_busy"

	defaultCursorQuitTimeout   = 30 * time.Second
	defaultCursorLaunchTimeout = 15 * time.Second
)

var (
	ErrCursorRestartConfirmationRequired = errors.New(cursorRestartConfirmationCode)
	ErrCursorQuitUnsupported             = errors.New(cursorQuitUnsupportedCode)
	ErrCursorQuitTimeout                 = errors.New(cursorQuitTimeoutCode)
	ErrCursorLaunchPartial               = errors.New(cursorLaunchPartialCode)
	ErrCursorStartBusy                   = errors.New(cursorStartBusyCode)
)

// CursorProxyStartInspection is a read-only preview of StartProxy restart needs.
// It does not include settings values.
type CursorProxyStartInspection struct {
	SettingsNeedChange   bool   `json:"settingsNeedChange"`
	CursorRunning        bool   `json:"cursorRunning"`
	NeedsRestart         bool   `json:"needsRestart"`
	SupportsGracefulQuit bool   `json:"supportsGracefulQuit"`
	ManualAction         bool   `json:"manualAction"`
	Message              string `json:"message"`
}

type servicePreState struct {
	backendRunning bool
	proxyRunning   bool
}

type cursorStartDecision struct {
	desiredURL    string
	snapshot      cursor.SettingsSnapshot
	needsChange   bool
	identified    bool
	identity      cursor.AppIdentity
	cursorRunning bool
	supportsQuit  bool
	needsRestart  bool
	manualAction  bool
	manualMessage string
}

func (s *ProxyService) cursorApp() cursor.AppController {
	if s != nil && s.cursorAppController != nil {
		return s.cursorAppController
	}
	return cursor.DefaultAppController()
}

func (s *ProxyService) cursorQuitWait() time.Duration {
	if s != nil && s.cursorQuitTimeout > 0 {
		return s.cursorQuitTimeout
	}
	return defaultCursorQuitTimeout
}

func (s *ProxyService) launchCursor(id cursor.AppIdentity) error {
	ctx, cancel := context.WithTimeout(context.Background(), defaultCursorLaunchTimeout)
	defer cancel()
	return s.cursorApp().Launch(ctx, id)
}

func (s *ProxyService) applyCursorSettings(snapshot cursor.SettingsSnapshot) error {
	if s != nil && s.applyCursorSettingsFn != nil {
		return s.applyCursorSettingsFn()
	}
	return s.applyCursorSettingsWithPlan(&snapshot)
}

func (s *ProxyService) injectCursorAccount() error {
	inject := cursor.InjectCursorUserInfo
	if s != nil && s.injectCursorUserInfoFn != nil {
		inject = s.injectCursorUserInfoFn
	}
	if err := inject(localruntime.InjectAccountEmail, localruntime.InjectAuthToken); err != nil {
		logger.Errorf("injectCursorUserInfo failed: %v", err)
		return err
	}
	return nil
}

func (s *ProxyService) desiredCursorProxyURL(cfg UserConfig) string {
	if s != nil && s.proxy != nil {
		if addr := s.proxy.Snapshot().ListenAddr; addr != "" {
			return cursor.ProxyURLFromListenAddr(addr)
		}
	}
	return cursor.ProxyURLFromListenAddr(cfg.ProxyListenAddr)
}

func (s *ProxyService) snapshotServicePreState() servicePreState {
	pre := servicePreState{}
	if s == nil {
		return pre
	}
	if s.backendHost != nil {
		pre.backendRunning = s.backendHost.IsRunning()
	}
	if s.proxy != nil {
		pre.proxyRunning = s.proxy.IsRunning()
	}
	return pre
}

func (s *ProxyService) cleanupNewlyStarted(pre servicePreState) {
	if s == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !pre.proxyRunning && s.proxy != nil && s.proxy.IsRunning() {
		_ = s.proxy.Stop(ctx)
	}
	if !pre.backendRunning && s.backendHost != nil && s.backendHost.IsRunning() {
		_ = s.backendHost.StopWithCause(ctx, backend.ShutdownCause{
			Reason:    backend.ShutdownReasonServiceStop,
			Initiator: backend.ShutdownInitiatorStartFail,
		})
	}
}

func (s *ProxyService) decideCursorStart(cfg UserConfig) (cursorStartDecision, error) {
	decision := cursorStartDecision{
		desiredURL:   s.desiredCursorProxyURL(cfg),
		supportsQuit: s.cursorApp().SupportsGracefulQuit(),
	}
	if s.cursorSettingsStore != nil {
		snap, err := s.cursorSettingsStore.Plan(decision.desiredURL)
		if err != nil {
			return decision, err
		}
		decision.snapshot = snap
		decision.needsChange = snap.NeedsChange()
	} else {
		decision.needsChange = true
	}
	id, identErr := s.cursorApp().Identify()
	switch {
	case identErr == nil:
		decision.identified = true
		decision.identity = id
		if decision.supportsQuit {
			running, runErr := s.cursorApp().Running(context.Background(), id)
			if runErr != nil {
				return decision, runErr
			}
			decision.cursorRunning = running
		}
	case errors.Is(identErr, cursor.ErrUnsupportedPlatform):
		// Running state is unknown; do not treat this as "Cursor is not running".
	case errors.Is(identErr, cursor.ErrAppNotFound):
		// Verified platform with no Cursor install.
	default:
		return decision, identErr
	}
	if decision.needsChange && !decision.supportsQuit {
		decision.manualAction = true
		decision.manualMessage = "当前系统不支持正常退出 Cursor，请手动退出后重试以应用配置"
		return decision, nil
	}
	decision.needsRestart = decision.needsChange && decision.cursorRunning
	return decision, nil
}

func inspectionFromDecision(d cursorStartDecision) CursorProxyStartInspection {
	msg := ""
	if d.manualAction {
		msg = d.manualMessage
	} else if d.needsRestart {
		msg = "请确认保存工作后重启 Cursor 以应用配置"
	}
	return CursorProxyStartInspection{
		SettingsNeedChange:   d.needsChange,
		CursorRunning:        d.cursorRunning,
		NeedsRestart:         d.needsRestart,
		SupportsGracefulQuit: d.supportsQuit,
		ManualAction:         d.manualAction,
		Message:              msg,
	}
}

// InspectCursorProxyStart reports whether StartProxy needs a one-time restart
// confirmation. It does not start services, write settings, or quit Cursor.
func (s *ProxyService) InspectCursorProxyStart() (CursorProxyStartInspection, error) {
	if s == nil {
		return CursorProxyStartInspection{}, errors.New("配置服务未初始化")
	}
	if !s.lifecycleMu.TryLock() {
		return CursorProxyStartInspection{}, fmt.Errorf("%w: 服务状态更新中，请稍后再试", ErrCursorStartBusy)
	}
	defer s.lifecycleMu.Unlock()
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return CursorProxyStartInspection{}, err
	}
	decision, err := s.decideCursorStart(cfg)
	if err != nil {
		return CursorProxyStartInspection{}, err
	}
	return inspectionFromDecision(decision), nil
}

// StartProxyAfterRestartConfirm is the StartProxy entry after the existing UI
// confirmed a one-time graceful Cursor restart.
func (s *ProxyService) StartProxyAfterRestartConfirm() (ProxyState, error) {
	return s.startProxy(true)
}

func (s *ProxyService) startProxy(restartConfirmed bool) (ProxyState, error) {
	if !s.lifecycleMu.TryLock() {
		err := fmt.Errorf("%w: 服务状态更新中，请稍后再试", ErrCursorStartBusy)
		logger.Infof("start service rejected busy restart_confirmed=%t", restartConfirmed)
		return s.GetState(), err
	}
	defer s.lifecycleMu.Unlock()
	logger.Infof("start service requested config_path=%s logs_root=%s restart_confirmed=%t", s.configPath, s.logsRoot, restartConfirmed)
	fail := func(step string, err error) (ProxyState, error) {
		logger.Errorf("start service failed step=%s err=%v", step, err)
		s.setLastError(err)
		s.emitState()
		return s.GetState(), err
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return fail("load_user_config", err)
	}
	decision, err := s.decideCursorStart(cfg)
	if err != nil {
		return fail("inspect_cursor_settings", err)
	}
	if decision.manualAction {
		err := fmt.Errorf("%w: %s", ErrCursorQuitUnsupported, decision.manualMessage)
		return fail("cursor_quit_unsupported", err)
	}
	if decision.needsRestart && !restartConfirmed {
		err := fmt.Errorf("%w: 请确认保存工作后重启 Cursor 以应用配置", ErrCursorRestartConfirmationRequired)
		logger.Infof("start service waiting for restart confirmation")
		return fail("cursor_restart_confirmation", err)
	}

	pre := s.snapshotServicePreState()
	abortKeepOld := func(step string, err error) (ProxyState, error) {
		s.cleanupNewlyStarted(pre)
		return fail(step, err)
	}

	if err := s.ensureBackendHost(); err != nil {
		return fail("ensure_backend_host", err)
	}
	if !s.backendHost.IsRunning() {
		logger.Infof("starting embedded backend listen_addr=%s", s.backendHost.ListenAddr())
		if err := s.backendHost.Start(); err != nil {
			return fail("start_backend", err)
		}
	} else {
		logger.Infof("embedded backend already running listen_addr=%s", s.backendHost.ListenAddr())
	}
	healthCtx, healthCancel := context.WithTimeout(context.Background(), backendReadyTimeout)
	defer healthCancel()
	if err := s.waitForBackend(healthCtx); err != nil {
		return abortKeepOld("wait_backend_ready", err)
	}
	logger.Infof("embedded backend ready listen_addr=%s", s.backendHost.ListenAddr())
	if err := s.ensureProxy(cfg); err != nil {
		return abortKeepOld("ensure_proxy", err)
	}

	if !decision.needsRestart {
		_ = s.injectCursorAccount()
	}

	if s.proxy != nil && !s.proxy.IsRunning() {
		logger.Infof("starting mitm proxy listen_addr=%s", s.proxy.Snapshot().ListenAddr)
		if err := s.proxy.Start(); err != nil {
			return abortKeepOld("start_mitm_proxy", err)
		}
	}

	quitOccurred := false
	if decision.needsRestart {
		quitCtx, quitCancel := context.WithTimeout(context.Background(), s.cursorQuitWait())
		defer quitCancel()
		if err := s.cursorApp().QuitGracefully(quitCtx, decision.identity); err != nil {
			msg := "Cursor 未在 30 秒内退出，已保持原设置，请手动退出后重试"
			if errors.Is(err, cursor.ErrGracefulQuitRefused) {
				msg = "Cursor 未能正常退出，已保持原设置，请保存工作并手动退出后重试"
			} else if errors.Is(err, cursor.ErrGracefulQuitUnsupported) {
				msg = "当前系统不支持正常退出 Cursor，请手动退出后重试以应用配置"
			}
			return abortKeepOld("quit_cursor", fmt.Errorf("%w: %s", ErrCursorQuitTimeout, msg))
		}
		quitOccurred = true
		if err := s.injectCursorAccount(); err != nil {
			s.cleanupNewlyStarted(pre)
			var launchErr error
			if launchErr = s.launchCursor(decision.identity); launchErr != nil {
				logger.Errorf("relaunch cursor after inject failure err=%v", launchErr)
			}
			startErr := formatCursorInjectFailure(err, launchErr)
			logger.Errorf("start service failed step=inject_cursor_account err=%v", startErr)
			s.setLastError(startErr)
			s.emitState()
			return s.GetState(), startErr
		}
	}

	if s.cursorSettingsStore != nil {
		decision.desiredURL = s.desiredCursorProxyURL(cfg)
		snap, planErr := s.cursorSettingsStore.Plan(decision.desiredURL)
		if planErr != nil {
			s.cleanupNewlyStarted(pre)
			var launchErr error
			if quitOccurred {
				if launchErr = s.launchCursor(decision.identity); launchErr != nil {
					logger.Errorf("relaunch cursor after plan failure err=%v", launchErr)
				}
			}
			return fail("plan_cursor_settings", formatCursorSettingsFailure(planErr, nil, launchErr, false))
		}
		decision.snapshot = snap
	}

	if err := s.applyCursorSettings(decision.snapshot); err != nil {
		var restoreErr error
		restored := false
		if s.cursorSettingsStore != nil {
			restored = true
			if restoreErr = s.cursorSettingsStore.Restore(decision.snapshot, s.cursorSettingsOwnerID); restoreErr != nil {
				logger.Errorf("restore cursor settings failed err=%v", restoreErr)
			}
		}
		s.cleanupNewlyStarted(pre)
		var launchErr error
		if quitOccurred {
			if launchErr = s.launchCursor(decision.identity); launchErr != nil {
				logger.Errorf("relaunch cursor after settings failure err=%v", launchErr)
			}
		}
		startErr := formatCursorSettingsFailure(err, restoreErr, launchErr, restored)
		logger.Errorf("start service failed step=apply_cursor_settings err=%v", startErr)
		s.setLastError(startErr)
		s.emitState()
		return s.GetState(), startErr
	}

	s.reconcileGateway(cfg)

	if quitOccurred {
		if err := s.launchCursor(decision.identity); err != nil {
			partial := fmt.Errorf("%w: 服务已就绪，Cursor 启动失败，请手动启动: %v", ErrCursorLaunchPartial, err)
			logger.Errorf("start service partial success step=launch_cursor err=%v", err)
			s.setLastError(partial)
			s.emitState()
			state := s.GetState()
			logger.Infof(
				"start service partial backend_listen_addr=%s proxy_listen_addr=%s cursor_settings_applied=%t",
				state.BackendListenAddr,
				state.ProxyListenAddr,
				state.CursorSettingsApplied,
			)
			return state, partial
		}
	}

	s.setLastError(nil)
	s.emitState()
	state := s.GetState()
	logger.Infof(
		"start service completed backend_listen_addr=%s proxy_listen_addr=%s cursor_settings_applied=%t",
		state.BackendListenAddr,
		state.ProxyListenAddr,
		state.CursorSettingsApplied,
	)
	return state, nil
}

func formatCursorSettingsFailure(applyErr, restoreErr, launchErr error, restoreAttempted bool) error {
	msg := "注入 Cursor 配置失败"
	if restoreAttempted {
		if restoreErr != nil {
			msg += "，未能恢复原设置"
		} else {
			msg += "，已恢复原设置"
		}
	}
	err := fmt.Errorf("%s: %w", msg, applyErr)
	if restoreErr != nil {
		err = fmt.Errorf("%w: %v", err, restoreErr)
	}
	if launchErr != nil {
		err = fmt.Errorf("%w; Cursor 重新启动失败，请手动启动: %v", err, launchErr)
	}
	return err
}

func formatCursorInjectFailure(injectErr, launchErr error) error {
	err := fmt.Errorf("同步 Cursor 账号失败，已保持原设置: %w", injectErr)
	if launchErr != nil {
		err = fmt.Errorf("%w; Cursor 重新启动失败，请手动启动: %v", err, launchErr)
	}
	return err
}
