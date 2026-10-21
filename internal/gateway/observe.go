package gateway

import (
	"context"
	"net/http"
	"strings"
	"time"

	"cursor/internal/observability"
)

type gatewayObservation struct {
	clientProtocol string
	publicModelID  string
}

type gatewayObservationKey struct{}

func gatewayClientProtocolForPath(path string) string {
	switch strings.TrimSpace(path) {
	case chatCompletionsPath:
		return "openai_chat"
	case responsesPath:
		return "openai_responses"
	default:
		return ""
	}
}

func observeGatewayRequest(request *http.Request, clientProtocol string, publicModelID string) {
	if request == nil {
		return
	}
	observation, _ := request.Context().Value(gatewayObservationKey{}).(*gatewayObservation)
	if observation == nil {
		return
	}
	if strings.TrimSpace(clientProtocol) != "" {
		observation.clientProtocol = strings.TrimSpace(clientProtocol)
	}
	if strings.TrimSpace(publicModelID) != "" {
		observation.publicModelID = strings.TrimSpace(publicModelID)
	}
}

func gatewayObservedHandler(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		sink := observability.ProcessSink()
		if sink == nil || request == nil {
			next.ServeHTTP(writer, request)
			return
		}
		correlation := observability.NewTrace()
		if requestID := strings.TrimSpace(request.Header.Get("x-request-id")); requestID != "" {
			correlation.HTTPRequestID = requestID
		} else {
			correlation.HTTPRequestID = correlation.TraceID
		}
		observation := &gatewayObservation{clientProtocol: gatewayClientProtocolForPath(request.URL.Path)}
		requestContext := observability.WithCorrelation(request.Context(), correlation)
		requestContext = context.WithValue(requestContext, gatewayObservationKey{}, observation)
		request = request.WithContext(requestContext)
		requestBytes := request.ContentLength
		if requestBytes < 0 {
			requestBytes = 0
		}
		startedAt := time.Now()
		baseFields := func() map[string]any {
			fields := map[string]any{"method": request.Method, "path": request.URL.Path}
			if observation.clientProtocol != "" {
				fields["client_protocol"] = observation.clientProtocol
			}
			if observation.publicModelID != "" {
				fields["public_model_id"] = observation.publicModelID
			}
			return fields
		}
		sink.Record(requestContext, observability.Capture{Event: observability.Event{
			Layer: "gateway", Event: "request_started", Capability: "unknown", Operation: "transport.request",
			Direction: observability.DirectionCursorToProxy, Route: request.URL.Path, Protocol: "http", Status: "started",
			RequestBytes: requestBytes, Fields: baseFields(),
		}})
		tracked := &gatewayResponseWriter{ResponseWriter: writer, statusCode: http.StatusOK}
		next.ServeHTTP(tracked, request)
		status := "ok"
		errorCategory := ""
		if tracked.statusCode >= http.StatusBadRequest {
			status = "error"
			if tracked.statusCode >= http.StatusInternalServerError {
				errorCategory = "server_error"
			} else {
				errorCategory = "client_error"
			}
		}
		fields := baseFields()
		fields["status_code"] = tracked.statusCode
		sink.Record(requestContext, observability.Capture{Event: observability.Event{
			Layer: "gateway", Event: "request_finished", Capability: "unknown", Operation: "transport.request",
			Direction: observability.DirectionProxyToCursor, Route: request.URL.Path, ExecutionTarget: "local_runtime",
			Protocol: "http", Status: status, ErrorCategory: errorCategory, DurationMS: time.Since(startedAt).Milliseconds(),
			RequestBytes: requestBytes, ResponseBytes: tracked.bytesWritten, Fields: fields,
		}})
	})
}

type gatewayResponseWriter struct {
	http.ResponseWriter
	statusCode   int
	bytesWritten int64
	wroteHeader  bool
}

func (writer *gatewayResponseWriter) WriteHeader(statusCode int) {
	if writer.wroteHeader {
		return
	}
	writer.statusCode = statusCode
	writer.wroteHeader = true
	writer.ResponseWriter.WriteHeader(statusCode)
}

func (writer *gatewayResponseWriter) Write(data []byte) (int, error) {
	if !writer.wroteHeader {
		writer.WriteHeader(http.StatusOK)
	}
	count, err := writer.ResponseWriter.Write(data)
	writer.bytesWritten += int64(count)
	return count, err
}

func (writer *gatewayResponseWriter) Flush() {
	if flusher, ok := writer.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}
