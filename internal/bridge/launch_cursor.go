package bridge

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var (
	errCursorNotFound   = errors.New("未发现 Cursor 应用")
	errCursorPermission = errors.New("没有权限启动 Cursor")
)

type cursorLaunchEnv struct {
	goos     string
	lookPath func(string) (string, error)
	stat     func(string) (os.FileInfo, error)
	getenv   func(string) string
	homeDir  func() (string, error)
	start    func(name string, args ...string) error
}

type cursorTarget struct {
	name string
	args []string
}

const (
	windowsDetachedProcessFlag       = 0x00000008
	windowsCreateNewProcessGroupFlag = 0x00000200
)

type cursorProcessAttr struct {
	Detach        bool
	CreationFlags uint32
}

func cursorProcessAttrForGOOS(goos string) cursorProcessAttr {
	if goos != "windows" {
		return cursorProcessAttr{}
	}
	return cursorProcessAttr{
		Detach:        true,
		CreationFlags: windowsDetachedProcessFlag | windowsCreateNewProcessGroupFlag,
	}
}

func startCursorProcess(goos, name string, args ...string) error {
	return cursorCommand(goos, name, args...).Start()
}

func cursorCommand(goos, name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	applyCursorProcessAttr(cmd, cursorProcessAttrForGOOS(goos))
	return cmd
}

func defaultCursorLaunchEnv() cursorLaunchEnv {
	goos := runtime.GOOS
	return cursorLaunchEnv{
		goos:     goos,
		lookPath: exec.LookPath,
		stat:     os.Stat,
		getenv:   os.Getenv,
		homeDir:  os.UserHomeDir,
		start: func(name string, args ...string) error {
			return startCursorProcess(goos, name, args...)
		},
	}
}

// LaunchCursor discovers the Cursor desktop app and starts it.
// It returns explicit not-found, permission, or launch-failure errors
// and never treats a UI navigation as success.
func (s *WindowService) LaunchCursor() error {
	return launchCursor(defaultCursorLaunchEnv())
}

func launchCursor(env cursorLaunchEnv) error {
	target, err := discoverCursor(env)
	if err != nil {
		return classifyCursorLaunchError(err)
	}
	if err := env.start(target.name, target.args...); err != nil {
		return classifyCursorLaunchError(err)
	}
	return nil
}

func discoverCursor(env cursorLaunchEnv) (cursorTarget, error) {
	switch env.goos {
	case "darwin":
		return discoverCursorDarwin(env)
	case "windows":
		return discoverCursorWindows(env)
	case "linux":
		return discoverCursorLinux(env)
	default:
		return cursorTarget{}, errCursorNotFound
	}
}

func discoverCursorDarwin(env cursorLaunchEnv) (cursorTarget, error) {
	candidates := []string{"/Applications/Cursor.app"}
	if home, err := env.homeDir(); err == nil && strings.TrimSpace(home) != "" {
		candidates = append(candidates, filepath.Join(home, "Applications", "Cursor.app"))
	}
	for _, path := range candidates {
		info, err := env.stat(path)
		if err != nil {
			if classified := classifyCursorStatError(err); classified != nil {
				return cursorTarget{}, classified
			}
			continue
		}
		if info != nil && info.IsDir() {
			return cursorTarget{name: "open", args: []string{path}}, nil
		}
	}
	return lookPathCursor(env, "cursor")
}

func discoverCursorWindows(env cursorLaunchEnv) (cursorTarget, error) {
	if target, err := lookPathCursor(env, "Cursor.exe", "cursor.exe", "cursor"); err == nil {
		return target, nil
	} else if !errors.Is(err, errCursorNotFound) {
		return cursorTarget{}, err
	}

	localAppData := strings.TrimSpace(env.getenv("LOCALAPPDATA"))
	programFiles := strings.TrimSpace(env.getenv("PROGRAMFILES"))
	programFilesX86 := strings.TrimSpace(env.getenv("PROGRAMFILES(X86)"))
	candidates := make([]string, 0, 6)
	if localAppData != "" {
		candidates = append(candidates,
			filepath.Join(localAppData, "Programs", "cursor", "Cursor.exe"),
			filepath.Join(localAppData, "Programs", "Cursor", "Cursor.exe"),
		)
	}
	if programFiles != "" {
		candidates = append(candidates, filepath.Join(programFiles, "Cursor", "Cursor.exe"))
	}
	if programFilesX86 != "" {
		candidates = append(candidates, filepath.Join(programFilesX86, "Cursor", "Cursor.exe"))
	}
	return firstExistingExecutable(env, candidates)
}

func discoverCursorLinux(env cursorLaunchEnv) (cursorTarget, error) {
	if target, err := lookPathCursor(env, "cursor"); err == nil {
		return target, nil
	} else if !errors.Is(err, errCursorNotFound) {
		return cursorTarget{}, err
	}

	candidates := []string{
		"/usr/bin/cursor",
		"/usr/local/bin/cursor",
		"/opt/Cursor/cursor",
		"/opt/cursor/cursor",
		"/snap/bin/cursor",
	}
	if home, err := env.homeDir(); err == nil && strings.TrimSpace(home) != "" {
		candidates = append([]string{filepath.Join(home, ".local", "bin", "cursor")}, candidates...)
	}
	return firstExistingExecutable(env, candidates)
}

func lookPathCursor(env cursorLaunchEnv, names ...string) (cursorTarget, error) {
	if env.lookPath == nil {
		return cursorTarget{}, errCursorNotFound
	}
	var sawPermission bool
	for _, name := range names {
		path, err := env.lookPath(name)
		if err != nil {
			if classified := classifyCursorStatError(err); errors.Is(classified, errCursorPermission) {
				sawPermission = true
			}
			continue
		}
		if !looksLikeCursor(path) {
			continue
		}
		info, statErr := env.stat(path)
		if statErr != nil {
			if classified := classifyCursorStatError(statErr); classified != nil {
				return cursorTarget{}, classified
			}
			return cursorTarget{name: path}, nil
		}
		if info != nil && info.IsDir() {
			continue
		}
		return cursorTarget{name: path}, nil
	}
	if sawPermission {
		return cursorTarget{}, errCursorPermission
	}
	return cursorTarget{}, errCursorNotFound
}

func firstExistingExecutable(env cursorLaunchEnv, candidates []string) (cursorTarget, error) {
	for _, path := range candidates {
		if strings.TrimSpace(path) == "" {
			continue
		}
		info, err := env.stat(path)
		if err != nil {
			if classified := classifyCursorStatError(err); classified != nil {
				return cursorTarget{}, classified
			}
			continue
		}
		if info != nil && info.IsDir() {
			continue
		}
		return cursorTarget{name: path}, nil
	}
	return cursorTarget{}, errCursorNotFound
}

func looksLikeCursor(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	return base == "cursor" || base == "cursor.exe" || base == "cursor.app"
}

func classifyCursorStatError(err error) error {
	if err == nil {
		return nil
	}
	if os.IsPermission(err) {
		return errCursorPermission
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) && os.IsPermission(pathErr.Err) {
		return errCursorPermission
	}
	return nil
}

func classifyCursorLaunchError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errCursorNotFound) || errors.Is(err, exec.ErrNotFound) || os.IsNotExist(err) {
		return errCursorNotFound
	}
	if errors.Is(err, errCursorPermission) || os.IsPermission(err) {
		return errCursorPermission
	}
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		if os.IsNotExist(pathErr.Err) || errors.Is(pathErr.Err, exec.ErrNotFound) {
			return errCursorNotFound
		}
		if os.IsPermission(pathErr.Err) {
			return errCursorPermission
		}
	}
	return fmt.Errorf("启动 Cursor 失败: %w", err)
}
