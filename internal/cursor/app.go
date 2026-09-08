package cursor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	// CursorBundleID is the macOS CFBundleIdentifier verified from
	// /Applications/Cursor.app/Contents/Info.plist. Quit and running checks
	// use this identity, never a fuzzy process-name kill.
	CursorBundleID = "com.todesktop.230313mzl4w4u92"

	defaultQuitPollInterval = 200 * time.Millisecond
	defaultLaunchTimeout    = 15 * time.Second
)

var (
	ErrAppNotFound             = errors.New("未发现已识别的 Cursor 应用")
	ErrUnsupportedPlatform     = errors.New("当前系统不支持自动管理 Cursor 应用")
	ErrGracefulQuitUnsupported = errors.New("当前系统不支持正常退出 Cursor")
	ErrGracefulQuitTimeout     = errors.New("Cursor 未在时限内正常退出")
	ErrGracefulQuitRefused     = errors.New("Cursor 拒绝或未能正常退出")
	ErrLaunchFailed            = errors.New("启动 Cursor 失败")
)

// AppIdentity is a verified Cursor install. Empty BundleID means the platform
// has a path but no bundle identifier.
type AppIdentity struct {
	BundleID string
	AppPath  string
}

// AppController locates, observes, quits, and launches the Cursor app.
type AppController interface {
	Identify() (AppIdentity, error)
	Running(ctx context.Context, id AppIdentity) (bool, error)
	SupportsGracefulQuit() bool
	QuitGracefully(ctx context.Context, id AppIdentity) error
	Launch(ctx context.Context, id AppIdentity) error
}

type appEnv struct {
	goos            string
	stat            func(string) (os.FileInfo, error)
	homeDir         func() (string, error)
	readBundleID    func(appPath string) (string, error)
	osa             func(ctx context.Context, script string) (string, error)
	run             func(ctx context.Context, name string, args ...string) error
	processRunning  func(id AppIdentity) (bool, error)
	quitPoll        time.Duration
	allowedBundleID string
}

func defaultAppEnv() appEnv {
	goos := runtime.GOOS
	return appEnv{
		goos:            goos,
		stat:            os.Stat,
		homeDir:         os.UserHomeDir,
		readBundleID:    readCursorBundleID,
		osa:             runOSAScript,
		run:             runNamedCommand,
		processRunning:  nil,
		quitPoll:        defaultQuitPollInterval,
		allowedBundleID: CursorBundleID,
	}
}

type defaultAppController struct {
	env appEnv
}

// DefaultAppController returns the platform Cursor controller.
func DefaultAppController() AppController {
	return defaultAppController{env: defaultAppEnv()}
}

func (c defaultAppController) Identify() (AppIdentity, error) {
	return c.env.Identify()
}

func (c defaultAppController) Running(ctx context.Context, id AppIdentity) (bool, error) {
	return c.env.Running(ctx, id)
}

func (c defaultAppController) SupportsGracefulQuit() bool {
	return c.env.SupportsGracefulQuit()
}

func (c defaultAppController) QuitGracefully(ctx context.Context, id AppIdentity) error {
	return c.env.QuitGracefully(ctx, id)
}

func (c defaultAppController) Launch(ctx context.Context, id AppIdentity) error {
	return c.env.Launch(ctx, id)
}

func (e appEnv) allowedID() string {
	id := strings.TrimSpace(e.allowedBundleID)
	if id == "" {
		return CursorBundleID
	}
	return id
}

func (e appEnv) SupportsGracefulQuit() bool {
	return e.goos == "darwin"
}

func (e appEnv) Identify() (AppIdentity, error) {
	switch e.goos {
	case "darwin":
		return e.identifyDarwin()
	default:
		return AppIdentity{}, ErrUnsupportedPlatform
	}
}

func (e appEnv) identifyDarwin() (AppIdentity, error) {
	candidates := []string{"/Applications/Cursor.app"}
	if e.homeDir != nil {
		if home, err := e.homeDir(); err == nil && strings.TrimSpace(home) != "" {
			candidates = append(candidates, filepath.Join(home, "Applications", "Cursor.app"))
		}
	}
	stat := e.stat
	if stat == nil {
		stat = os.Stat
	}
	readID := e.readBundleID
	if readID == nil {
		readID = readCursorBundleID
	}
	allowed := e.allowedID()
	for _, path := range candidates {
		info, err := stat(path)
		if err != nil {
			continue
		}
		if info == nil || !info.IsDir() {
			continue
		}
		bundleID, err := readID(path)
		if err != nil {
			continue
		}
		bundleID = strings.TrimSpace(bundleID)
		if bundleID == "" || bundleID != allowed {
			continue
		}
		return AppIdentity{BundleID: bundleID, AppPath: path}, nil
	}
	return AppIdentity{}, ErrAppNotFound
}

func (e appEnv) Running(ctx context.Context, id AppIdentity) (bool, error) {
	if strings.TrimSpace(id.BundleID) == "" && strings.TrimSpace(id.AppPath) == "" {
		return false, nil
	}
	if e.goos == "darwin" {
		return e.runningDarwin(ctx, id)
	}
	if e.processRunning != nil {
		return e.processRunning(id)
	}
	return false, ErrUnsupportedPlatform
}

func (e appEnv) runningDarwin(ctx context.Context, id AppIdentity) (bool, error) {
	bundleID := strings.TrimSpace(id.BundleID)
	if bundleID == "" {
		return false, nil
	}
	osa := e.osa
	if osa == nil {
		osa = runOSAScript
	}
	script := fmt.Sprintf(`tell application "System Events" to return (count of (every process whose bundle identifier is "%s")) as integer`, escapeOSAString(bundleID))
	out, err := osa(ctx, script)
	if err != nil {
		return false, fmt.Errorf("查询 Cursor 运行状态失败: %w", err)
	}
	return strings.TrimSpace(out) != "0", nil
}

func (e appEnv) QuitGracefully(ctx context.Context, id AppIdentity) error {
	if !e.SupportsGracefulQuit() {
		return ErrGracefulQuitUnsupported
	}
	bundleID := strings.TrimSpace(id.BundleID)
	if bundleID == "" || bundleID != e.allowedID() {
		return ErrAppNotFound
	}
	if ctx == nil {
		ctx = context.Background()
	}
	osa := e.osa
	if osa == nil {
		osa = runOSAScript
	}
	script := fmt.Sprintf(`tell application id "%s" to quit`, escapeOSAString(bundleID))
	out, err := osa(ctx, script)
	if ctx.Err() != nil {
		return ErrGracefulQuitTimeout
	}
	if err != nil {
		msg := strings.TrimSpace(string(out) + " " + err.Error())
		if isOSAUserCanceled(msg) {
			return ErrGracefulQuitRefused
		}
		// Still wait: a quit Apple Event may have been delivered.
	}
	poll := e.quitPoll
	if poll <= 0 {
		poll = defaultQuitPollInterval
	}
	ticker := time.NewTicker(poll)
	defer ticker.Stop()
	for {
		running, runErr := e.Running(ctx, id)
		if runErr == nil && !running {
			return nil
		}
		select {
		case <-ctx.Done():
			return ErrGracefulQuitTimeout
		case <-ticker.C:
		}
	}
}

func (e appEnv) Launch(ctx context.Context, id AppIdentity) error {
	if e.goos != "darwin" {
		return ErrUnsupportedPlatform
	}
	if strings.TrimSpace(id.BundleID) != e.allowedID() {
		return ErrAppNotFound
	}
	path := strings.TrimSpace(id.AppPath)
	if path == "" {
		return ErrAppNotFound
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, defaultLaunchTimeout)
		defer cancel()
	}
	run := e.run
	if run == nil {
		run = runNamedCommand
	}
	if err := run(ctx, "open", path); err != nil {
		return fmt.Errorf("%w: %v", ErrLaunchFailed, err)
	}
	return nil
}

func runNamedCommand(ctx context.Context, name string, args ...string) error {
	_, err := runHelper(ctx, name, args...)
	return err
}

func runOSAScript(ctx context.Context, script string) (string, error) {
	return runHelper(ctx, "osascript", "-e", script)
}

// runHelper runs a helper subprocess bound to ctx. Cancel kills only this
// helper (osascript/open/sleep), never the Cursor application.
func runHelper(ctx context.Context, name string, args ...string) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	out := strings.TrimSpace(stdout.String())
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = out
		}
		if msg == "" {
			return out, err
		}
		return out, fmt.Errorf("%w: %s", err, msg)
	}
	return out, nil
}

func readCursorBundleID(appPath string) (string, error) {
	info := filepath.Join(appPath, "Contents", "Info.plist")
	cmd := exec.Command("plutil", "-extract", "CFBundleIdentifier", "raw", info)
	out, err := cmd.CombinedOutput()
	if err == nil {
		id := strings.TrimSpace(string(out))
		if id != "" {
			return id, nil
		}
	}
	data, readErr := os.ReadFile(info)
	if readErr != nil {
		if err != nil {
			return "", fmt.Errorf("读取 Cursor bundle id 失败: %w", err)
		}
		return "", readErr
	}
	id, parseErr := parseBundleIDFromPlist(data)
	if parseErr != nil {
		return "", parseErr
	}
	return id, nil
}

func parseBundleIDFromPlist(data []byte) (string, error) {
	const key = "<key>CFBundleIdentifier</key>"
	idx := bytes.Index(data, []byte(key))
	if idx < 0 {
		return "", errors.New("Info.plist 缺少 CFBundleIdentifier")
	}
	rest := data[idx+len(key):]
	start := bytes.Index(rest, []byte("<string>"))
	end := bytes.Index(rest, []byte("</string>"))
	if start < 0 || end < 0 || end <= start {
		return "", errors.New("Info.plist CFBundleIdentifier 格式无效")
	}
	value := bytes.TrimSpace(rest[start+len("<string>") : end])
	if len(value) == 0 {
		return "", errors.New("Info.plist CFBundleIdentifier 为空")
	}
	return string(value), nil
}

func escapeOSAString(value string) string {
	return strings.ReplaceAll(value, `"`, `\"`)
}

func isOSAUserCanceled(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "user canceled") || strings.Contains(msg, "-128")
}
