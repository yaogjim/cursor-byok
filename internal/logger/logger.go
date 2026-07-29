package logger

import (
	"context"
	"errors"
	"fmt"
	stdlog "log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"cursor/internal/appdata"
	"cursor/internal/logsink"
	"cursor/internal/observability"

	"github.com/lmittmann/tint"
	"github.com/mattn/go-colorable"
	"github.com/mattn/go-isatty"
)

const (
	appLogSegmentMaxBytes = 10 << 20
	appLogMaxFiles        = 10
	appLogMaxTotalBytes   = 100 << 20
	appLogMaxAge          = 14 * 24 * time.Hour
)

var (
	initOnce    sync.Once
	logFilePath string
)

// Init 配置默认 slog logger，并把标准库 log 接到同一输出。
func Init() {
	initOnce.Do(func() {
		handlers := []slog.Handler{tint.NewHandler(colorable.NewColorableStdout(), &tint.Options{
			Level:      slog.LevelInfo,
			TimeFormat: "15:04:05.000",
			NoColor:    disableColor(),
		})}
		fileHandler, path, fileErr := buildFileHandler()
		if fileErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "[logger] 初始化日志文件失败: %v\n", fileErr)
		} else if fileHandler != nil {
			handlers = append(handlers, fileHandler)
			logFilePath = path
		}
		handler := handlers[0]
		if len(handlers) > 1 {
			handler = &multiHandler{handlers: handlers}
		}
		slog.SetDefault(slog.New(handler))
		stdlog.SetFlags(0)
		stdlog.SetOutput(standardLogWriter{})
		if logFilePath != "" {
			slog.Info("应用日志已写入分片目录", "path", logFilePath, "pid", os.Getpid())
		}
		go cleanupLegacyPayloadDirectory()
	})
}

// Info 输出 info 级日志。
func Info(msg string, args ...any) {
	Init()
	slog.Info(observability.SanitizeText(msg), args...)
}

// Warn 输出 warning 级日志。
func Warn(msg string, args ...any) {
	Init()
	slog.Warn(observability.SanitizeText(msg), args...)
}

// Error 输出 error 级日志。
func Error(msg string, args ...any) {
	Init()
	slog.Error(observability.SanitizeText(msg), args...)
}

// Infof 输出格式化的 info 级日志。
func Infof(format string, args ...any) {
	Init()
	slog.Info(formatMessage(format, args...))
}

// Warnf 输出格式化的 warning 级日志。
func Warnf(format string, args ...any) {
	Init()
	slog.Warn(formatMessage(format, args...))
}

// Errorf 输出格式化的 error 级日志。
func Errorf(format string, args ...any) {
	Init()
	slog.Error(formatMessage(format, args...))
}

func formatMessage(format string, args ...any) string {
	if len(args) == 0 {
		return observability.SanitizeText(strings.TrimSpace(format))
	}
	return observability.SanitizeText(strings.TrimSpace(fmt.Sprintf(format, args...)))
}

func disableColor() bool {
	if strings.TrimSpace(os.Getenv("NO_COLOR")) != "" {
		return true
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return true
	}
	fd := os.Stdout.Fd()
	return !isatty.IsTerminal(fd) && !isatty.IsCygwinTerminal(fd)
}

func cleanupLegacyPayloadDirectory() {
	path := filepath.Join(appdata.LogsRootPath(), "payloads")
	stats, err := logsink.CleanupPayloadDirectory(path, false)
	if err != nil {
		slog.Warn("旧版 payload 日志清理失败", "path", path, "error", err)
		return
	}
	if stats.Removed > 0 {
		slog.Info("旧版 payload 日志清理完成", "path", path, "removed", stats.Removed)
	}
}

func buildFileHandler() (slog.Handler, string, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return nil, "", err
	}
	dir := filepath.Join(appdata.LogsRootPath(), "app")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, "", fmt.Errorf("创建应用日志目录失败: %w", err)
	}
	writer := logsink.NewRotatingFile(dir, logsink.RotationConfig{
		Prefix:        "app",
		Extension:     ".log",
		MaxBytes:      appLogSegmentMaxBytes,
		MaxFiles:      appLogMaxFiles,
		MaxTotalBytes: appLogMaxTotalBytes,
		MaxAge:        appLogMaxAge,
	})
	return tint.NewHandler(writer, &tint.Options{
		Level:      slog.LevelInfo,
		TimeFormat: time.RFC3339,
		NoColor:    true,
	}), dir, nil
}

type standardLogWriter struct{}

func (standardLogWriter) Write(payload []byte) (int, error) {
	message := observability.SanitizeText(strings.TrimSpace(string(payload)))
	if message != "" {
		slog.Warn(message, "source", "stdlib")
	}
	return len(payload), nil
}

type multiHandler struct {
	handlers []slog.Handler
}

func (h *multiHandler) Enabled(ctx context.Context, level slog.Level) bool {
	for _, handler := range h.handlers {
		if handler.Enabled(ctx, level) {
			return true
		}
	}
	return false
}

func (h *multiHandler) Handle(ctx context.Context, record slog.Record) error {
	var handleErr error
	for _, handler := range h.handlers {
		if !handler.Enabled(ctx, record.Level) {
			continue
		}
		if err := handler.Handle(ctx, record.Clone()); err != nil {
			handleErr = errors.Join(handleErr, err)
		}
	}
	return handleErr
}

func (h *multiHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	next := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		next = append(next, handler.WithAttrs(attrs))
	}
	return &multiHandler{handlers: next}
}

func (h *multiHandler) WithGroup(name string) slog.Handler {
	next := make([]slog.Handler, 0, len(h.handlers))
	for _, handler := range h.handlers {
		next = append(next, handler.WithGroup(name))
	}
	return &multiHandler{handlers: next}
}
