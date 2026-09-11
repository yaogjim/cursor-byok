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

var (
	initOnce sync.Once
	appLogMu sync.RWMutex
	// appLogWriter is set once the observability controller has read the real
	// config. Until then application file writes are held back and stdout is the
	// only sink.
	appLogWriter AppLogWriter
)

// AppLogWriter is the narrow seam the observability controller satisfies so
// application log file writes share the trace/payload budget.
type AppLogWriter interface {
	WriteAppLog(payload []byte) (int, error)
}

// ConfigureAppLogWriter enables application log file output. logger.Init runs
// before the config is loaded, so file output stays deferred until the host has
// built a controller that knows the real observability budget.
func ConfigureAppLogWriter(writer AppLogWriter) {
	appLogMu.Lock()
	previous := appLogWriter
	appLogWriter = writer
	appLogMu.Unlock()
	if writer != nil && previous == nil {
		slog.Log(context.Background(), slog.LevelInfo, "应用日志文件输出已启用", "pid", os.Getpid())
	}
}

type appLogGateWriter struct{}

func (appLogGateWriter) Write(payload []byte) (int, error) {
	appLogMu.RLock()
	writer := appLogWriter
	appLogMu.RUnlock()
	if writer == nil {
		return len(payload), nil
	}
	return writer.WriteAppLog(payload)
}

// Init 配置默认 slog logger，并把标准库 log 接到同一输出。
func Init() {
	initOnce.Do(func() {
		stdoutHandler := tint.NewHandler(colorable.NewColorableStdout(), &tint.Options{
			Level:      slog.LevelInfo,
			TimeFormat: "15:04:05.000",
			NoColor:    disableColor(),
		})
		fileHandler := tint.NewHandler(appLogGateWriter{}, &tint.Options{
			Level:      slog.LevelInfo,
			TimeFormat: time.RFC3339,
			NoColor:    true,
		})
		slog.SetDefault(slog.New(&multiHandler{handlers: []slog.Handler{stdoutHandler, fileHandler}}))
		stdlog.SetFlags(0)
		stdlog.SetOutput(standardLogWriter{})
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

type standardLogWriter struct{}

func (standardLogWriter) Write(payload []byte) (int, error) {
	message := observability.SanitizeText(strings.TrimSpace(string(payload)))
	if message != "" {
		slog.Log(context.Background(), stdlibLogLevel(message), message, "source", "stdlib")
	}
	return len(payload), nil
}

func stdlibLogLevel(message string) slog.Level {
	lower := strings.ToLower(message)
	switch {
	case strings.Contains(lower, "panic"), strings.Contains(lower, "fatal"):
		return slog.LevelError
	case strings.Contains(lower, "error"), strings.Contains(lower, "fail"):
		return slog.LevelWarn
	default:
		return slog.LevelInfo
	}
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
