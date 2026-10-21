package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"strings"
	"time"

	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/logger"
	"cursor/internal/observability"
	legacyruntime "cursor/internal/runtime"
)

func Recover() Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) (err error) {
			defer func() {
				if recovered := recover(); recovered != nil {
					err = fmt.Errorf("panic: %v\n%s", recovered, string(debug.Stack()))
				}
			}()
			return next(ctx)
		}
	}
}

type captureRecorder interface {
	Record(context.Context, observability.Capture) bool
	RecordEvent(context.Context, observability.Event) bool
}

func Observe(recorder captureRecorder) Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) (err error) {
			if ctx == nil || ctx.Request == nil || recorder == nil {
				return next(ctx)
			}
			correlation := requestCorrelation(ctx.Request)
			ctx.Correlation = correlation
			requestContext := observability.WithCorrelation(ctx.Request.Context(), correlation)
			ctx.Request = ctx.Request.WithContext(requestContext)
			requestBytes := ctx.Request.ContentLength
			if requestBytes < 0 {
				requestBytes = 0
			}
			requestPath := ""
			if ctx.Request.URL != nil {
				requestPath = ctx.Request.URL.Path
			}
			capability := observability.ClassifyRequestCapability(requestPath, ctx.RouteName)
			operation := observability.ClassifyRequestOperation(capability, "transport.request")
			recorder.RecordEvent(requestContext, observability.Event{
				Layer:        "backend",
				Event:        "request_started",
				Capability:   capability,
				Operation:    operation,
				Direction:    observability.DirectionCursorToProxy,
				Route:        ctx.RouteName,
				Protocol:     string(ctx.Protocol),
				Status:       "started",
				RequestBytes: requestBytes,
				Fields: map[string]any{
					"method": ctx.Request.Method,
					"path":   requestPath,
				},
			})

			defer func() {
				recovered := recover()
				finalErr := err
				if ctx.LastError != nil {
					finalErr = ctx.LastError
				}
				statusCode, responseBytes := responseMetrics(ctx.Writer)
				if recovered != nil {
					finalErr = fmt.Errorf("panic: %v", recovered)
					if statusCode < http.StatusBadRequest {
						// Recover converts the panic to an error that the route writes as 502
						// after this middleware unwinds. Record that eventual HTTP terminal state.
						statusCode = http.StatusBadGateway
					}
				}
				status := "ok"
				if finalErr != nil || statusCode >= http.StatusBadRequest {
					status = "error"
				}
				severity := ""
				if statusCode == http.StatusNotFound && capability != "unknown" {
					severity = observability.SeverityWarning
				}
				recorder.RecordEvent(requestContext, observability.Event{
					Layer:           "backend",
					Event:           "request_finished",
					Capability:      capability,
					Operation:       operation,
					Direction:       observability.DirectionProxyToCursor,
					Route:           ctx.RouteName,
					ExecutionTarget: executionTarget(ctx),
					Protocol:        string(ctx.Protocol),
					Status:          status,
					Severity:        severity,
					ErrorCategory:   serverErrorCategory(finalErr, statusCode),
					DurationMS:      time.Since(ctx.StartedAt).Milliseconds(),
					RequestBytes:    requestBytes,
					ResponseBytes:   responseBytes,
					Fields: map[string]any{
						"method":      ctx.Request.Method,
						"path":        requestPath,
						"status_code": statusCode,
						"source":      string(ctx.Source),
					},
				})
				if recovered != nil {
					panic(recovered)
				}
			}()

			err = next(ctx)
			return err
		}
	}
}

func requestCorrelation(request *http.Request) observability.Correlation {
	trace := observability.NewTrace()
	if request == nil {
		return trace
	}
	incomingTraceID := strings.TrimSpace(request.Header.Get(HeaderTraceID))
	incomingSpanID := strings.TrimSpace(request.Header.Get(HeaderParentSpanID))
	if incomingTraceID != "" {
		trace = observability.ChildSpan(observability.Correlation{
			TraceID: incomingTraceID,
			SpanID:  incomingSpanID,
		})
	}
	trace.HTTPRequestID = strings.TrimSpace(request.Header.Get("x-request-id"))
	if trace.HTTPRequestID == "" {
		trace.HTTPRequestID = trace.TraceID
	}
	return trace
}

func responseMetrics(writer http.ResponseWriter) (int, int64) {
	for writer != nil {
		if tracked, ok := writer.(*trackedResponseWriter); ok {
			return tracked.StatusCode(), tracked.BytesWritten()
		}
		unwrapper, ok := writer.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			break
		}
		next := unwrapper.Unwrap()
		if next == nil || next == writer {
			break
		}
		writer = next
	}
	return http.StatusOK, 0
}

func executionTarget(ctx *Context) string {
	if ctx == nil {
		return ""
	}
	if target := strings.TrimSpace(ctx.ExecutionTarget); target != "" {
		return target
	}
	if ctx.Mode == ModeUpstream {
		return "official_upstream"
	}
	return "local_runtime"
}

func serverErrorCategory(err error, statusCode int) string {
	switch {
	case errors.Is(err, context.Canceled):
		return "canceled"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case err != nil:
		return "handler_error"
	case statusCode >= http.StatusInternalServerError:
		return "server_error"
	case statusCode >= http.StatusBadRequest:
		return "client_error"
	default:
		return ""
	}
}

func ServerContext() Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) error {
			if ctx == nil {
				return fmt.Errorf("server context is nil")
			}
			if err := ctx.ParseUpstreamURL(); err != nil {
				return err
			}
			return next(ctx)
		}
	}
}

func PolicyMiddleware(configs *serverconfig.Manager) Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) error {
			if ctx == nil {
				return fmt.Errorf("server context is nil")
			}
			if configs == nil {
				ctx.Mode = ModeLocal
			} else {
				ctx.Mode = parseExecutionMode(configs.RouteMode(ctx.UpstreamURL != nil))
			}
			logger.Infof("ctx.Mode=%s upstream=%t", ctx.Mode, ctx.UpstreamURL != nil)
			return next(ctx)
		}
	}
}

func ErrorEncoder() Middleware {
	return func(next HandlerFunc) HandlerFunc {
		return func(ctx *Context) error {
			if ctx != nil {
				ctx.LastError = nil
			}
			if err := next(ctx); err != nil {
				if ctx != nil {
					ctx.LastError = err
				}
				if ctx == nil || ctx.Writer == nil {
					return err
				}
				writeServerError(ctx.Writer, err)
				return nil
			}
			return nil
		}
	}
}

func writeServerError(writer http.ResponseWriter, err error) {
	if responseWriterHasWrittenHeader(writer) {
		return
	}
	status := http.StatusBadGateway
	message := "bad gateway"
	switch {
	case err == nil:
		status = http.StatusOK
		message = ""
	case strings.TrimSpace(err.Error()) == "empty raw url":
		status = http.StatusBadRequest
		message = "invalid raw url"
	case errors.Is(err, ErrInvalidBidiAppendPayload):
		status = http.StatusBadRequest
		message = "invalid bidi append payload"
	case errors.Is(err, legacyruntime.ErrInvalidSystemSetting):
		status = http.StatusInternalServerError
		message = "invalid system setting"
	case errors.Is(err, legacyruntime.ErrChannelNotAvailable):
		status = http.StatusServiceUnavailable
		message = "no available channel"
	}
	http.Error(writer, message, status)
}
