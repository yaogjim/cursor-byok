package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	modeladapter "cursor/internal/backend/agent/model"
	"cursor/internal/backend/forwarder"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/logger"
	"cursor/internal/observability"
)

const (
	modelsPath          = "/v1/models"
	chatCompletionsPath = "/v1/chat/completions"
	responsesPath       = "/v1/responses"
	readHeaderTimeout   = 10 * time.Second
	maxRequestBodyBytes = 2 << 20
	shutdownTimeout     = 5 * time.Second
)

type ConfigSource interface {
	Current() serverconfig.Config
}

type Server struct {
	streamer forwarder.ProviderGateway
	configs  ConfigSource

	mu         sync.Mutex
	httpServer *http.Server
	listener   net.Listener
	listenAddr string
	lastError  string
}

func New(streamer forwarder.ProviderGateway, configs ConfigSource) *Server {
	return &Server{streamer: streamer, configs: configs}
}

func (server *Server) ListenAddr() string {
	if server == nil {
		return ""
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.listenAddr
}

func (server *Server) Running() bool {
	if server == nil {
		return false
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.httpServer != nil
}

func (server *Server) LastError() string {
	if server == nil {
		return ""
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.lastError
}

func (server *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+modelsPath, server.handleModels)
	mux.HandleFunc("POST "+chatCompletionsPath, server.handleChatCompletions)
	mux.HandleFunc("POST "+responsesPath, server.handleResponses)
	return gatewayObservedHandler(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request != nil && !isLoopbackAddr(request.RemoteAddr) {
			writeAPIError(writer, http.StatusForbidden, "permission_error", "loopback_only", "Gateway 只接受 loopback 请求")
			return
		}
		if err := server.authorize(request); err != nil {
			writeAPIError(writer, http.StatusUnauthorized, "invalid_request_error", "invalid_api_key", err.Error())
			return
		}
		mux.ServeHTTP(writer, request)
	}))
}

func (server *Server) Start(cfg serverconfig.GatewayConfig) error {
	if server == nil {
		return errors.New("gateway server is nil")
	}
	if !cfg.Enabled {
		ctx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		return server.Stop(ctx)
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return errors.New("Gateway token 未生成")
	}
	listenAddr := strings.TrimSpace(cfg.ListenAddr)
	if listenAddr == "" {
		listenAddr = serverconfig.DefaultGatewayListenAddr
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if server.httpServer != nil && server.listenAddr == listenAddr {
		server.lastError = ""
		return nil
	}

	// Bind the replacement listener before stopping the current one. A failed
	// hot update must leave the already-running Gateway available.
	listener, err := net.Listen("tcp", listenAddr)
	if err != nil {
		server.lastError = err.Error()
		return fmt.Errorf("监听 Gateway %s 失败: %w", listenAddr, err)
	}
	if err := requireLoopbackListener(listener); err != nil {
		_ = listener.Close()
		server.lastError = err.Error()
		return err
	}
	httpServer := &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           server.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
	}
	if previous := server.httpServer; previous != nil {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), shutdownTimeout)
		err := previous.Shutdown(stopCtx)
		stopCancel()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			_ = listener.Close()
			server.lastError = err.Error()
			return fmt.Errorf("停止旧 Gateway listener 失败: %w", err)
		}
	}
	server.httpServer = httpServer
	server.listener = listener
	server.listenAddr = listener.Addr().String()
	server.lastError = ""
	logger.Infof("gateway listening listen_addr=%s", server.listenAddr)
	go func(instance *http.Server, ln net.Listener) {
		if err := instance.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			server.mu.Lock()
			if server.httpServer == instance {
				server.httpServer = nil
				server.listener = nil
				server.listenAddr = ""
				server.lastError = err.Error()
			}
			server.mu.Unlock()
			logger.Errorf("gateway server exited listen_addr=%s err=%v", ln.Addr(), err)
		}
	}(httpServer, listener)
	return nil
}

func (server *Server) Stop(ctx context.Context) error {
	if server == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	return server.stopLocked(ctx)
}

func (server *Server) stopLocked(ctx context.Context) error {
	instance := server.httpServer
	server.httpServer = nil
	server.listener = nil
	server.listenAddr = ""
	if instance == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if _, hasDeadline := ctx.Deadline(); !hasDeadline {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, shutdownTimeout)
		defer cancel()
	}
	err := instance.Shutdown(ctx)
	if err != nil && !errors.Is(err, http.ErrServerClosed) {
		server.lastError = err.Error()
		return err
	}
	return nil
}

func (server *Server) currentConfig() serverconfig.Config {
	if server == nil || server.configs == nil {
		return serverconfig.DefaultConfig()
	}
	return server.configs.Current()
}

func (server *Server) authorize(request *http.Request) error {
	cfg := server.currentConfig()
	expected := strings.TrimSpace(cfg.Gateway.Token)
	if expected == "" {
		return errors.New("Gateway token 未配置")
	}
	header := strings.TrimSpace(request.Header.Get("Authorization"))
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return errors.New("需要 Bearer token")
	}
	provided := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	providedSum := sha256.Sum256([]byte(provided))
	expectedSum := sha256.Sum256([]byte(expected))
	if subtle.ConstantTimeCompare(providedSum[:], expectedSum[:]) != 1 {
		return errors.New("invalid API key")
	}
	return nil
}

func requireLoopbackListener(listener net.Listener) error {
	if listener == nil {
		return errors.New("gateway listener is nil")
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok || addr == nil || addr.IP == nil || !addr.IP.IsLoopback() {
		return errors.New("Gateway 只允许绑定 loopback")
	}
	return nil
}

func isLoopbackAddr(remoteAddr string) bool {
	host := strings.TrimSpace(remoteAddr)
	if host == "" {
		return false
	}
	if parsedHost, _, err := net.SplitHostPort(host); err == nil {
		host = parsedHost
	}
	ip := net.ParseIP(host)
	if ip != nil {
		return ip.IsLoopback()
	}
	return strings.EqualFold(host, "localhost")
}

func randomID(size int) string {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return hex.EncodeToString([]byte(fmt.Sprintf("%d", time.Now().UnixNano())))
	}
	return hex.EncodeToString(buffer)
}

func limitBody(request *http.Request) io.ReadCloser {
	if request == nil || request.Body == nil {
		return http.NoBody
	}
	return http.MaxBytesReader(nil, request.Body, maxRequestBodyBytes)
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code,omitempty"`
}

func writeAPIError(writer http.ResponseWriter, status int, typ, code, message string) {
	writeJSON(writer, status, map[string]any{
		"error": apiError{
			Message: strings.TrimSpace(message),
			Type:    typ,
			Code:    code,
		},
	})
}

func (server *Server) handleModels(writer http.ResponseWriter, request *http.Request) {
	cfg := server.currentConfig()
	models := serverconfig.PublicGatewayModels(cfg)
	data := make([]map[string]any, 0, len(models))
	for _, item := range models {
		data = append(data, map[string]any{
			"id":       item.ID,
			"object":   "model",
			"owned_by": "cursor-byok",
			"created":  0,
		})
	}
	writeJSON(writer, http.StatusOK, map[string]any{
		"object": "list",
		"data":   data,
	})
}

func requireJSONContentType(request *http.Request) error {
	if request == nil {
		return errors.New("Content-Type 必须是 application/json")
	}
	mediaType, _, err := mime.ParseMediaType(request.Header.Get("Content-Type"))
	if err != nil || !strings.EqualFold(strings.TrimSpace(mediaType), "application/json") {
		return errors.New("Content-Type 必须是 application/json")
	}
	return nil
}

func (server *Server) handleChatCompletions(writer http.ResponseWriter, request *http.Request) {
	if err := requireJSONContentType(request); err != nil {
		writeAPIError(writer, http.StatusUnsupportedMediaType, "invalid_request_error", "invalid_content_type", err.Error())
		return
	}
	body, err := io.ReadAll(limitBody(request))
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request_error", "invalid_body", "请求体过大或无法读取")
		return
	}
	parsed, err := parseChatCompletionRequest(body)
	if err != nil {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request_error", "unsupported_parameter", err.Error())
		return
	}
	observeGatewayRequest(request, "openai_chat", parsed.Model)
	cfg := server.currentConfig()
	target, stale, ok := serverconfig.ResolveGatewayPublicModel(cfg, parsed.Model)
	if !ok {
		writeAPIError(writer, http.StatusNotFound, "invalid_request_error", "model_not_found", "模型不存在或未配置公开别名")
		return
	}
	if stale {
		writeAPIError(writer, http.StatusBadRequest, "invalid_request_error", "mapping_invalid", "公开模型映射已失效，请重新选择目标适配器")
		return
	}
	if server.streamer == nil {
		writeAPIError(writer, http.StatusServiceUnavailable, "api_error", "provider_unavailable", "Gateway provider 未初始化")
		return
	}

	requestID := "chatcmpl-" + randomID(12)
	conversationID := "gateway-" + randomID(8)
	correlation := observability.CorrelationFromContext(request.Context())
	correlation.HTTPRequestID = requestID
	correlation.ModelCallID = requestID
	correlation.ConversationID = conversationID
	providerContext := observability.WithCorrelation(request.Context(), correlation)
	providerReq := forwarder.ProviderRequest{
		RequestID:      requestID,
		ConversationID: conversationID,
		RunID:          requestID,
		ModelCallID:    requestID,
		ModelID:        target,
		Messages:       parsed.Messages,
		Tools:          parsed.Tools,
		RequestKnobs:   parsed.RequestKnobs,
		MaxTokens:      parsed.MaxTokens,
		CompileSummary: fmt.Sprintf("gateway chat completions public_model=%s", parsed.Model),
	}
	if parsed.Stream {
		server.streamChat(providerContext, writer, requestID, parsed.Model, providerReq)
		return
	}
	server.completeChat(providerContext, writer, requestID, parsed.Model, providerReq)
}

func (server *Server) completeChat(ctx context.Context, writer http.ResponseWriter, requestID, publicModel string, req forwarder.ProviderRequest) {
	agg := newChatAggregator()
	err := server.streamer.StartStream(ctx, req, agg.consume)
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		writeAPIError(writer, 499, "invalid_request_error", "cancelled", "请求已取消")
		return
	}
	if err != nil {
		writeAPIError(writer, http.StatusBadGateway, "api_error", "provider_error", "上游模型请求失败")
		logger.Errorf("gateway chat failed public_model=%s err=%v", publicModel, err)
		return
	}
	writeJSON(writer, http.StatusOK, agg.completion(requestID, publicModel))
}

func (server *Server) streamChat(ctx context.Context, writer http.ResponseWriter, requestID, publicModel string, req forwarder.ProviderRequest) {
	flusher, _ := writer.(http.Flusher)
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	writer.WriteHeader(http.StatusOK)
	if flusher != nil {
		flusher.Flush()
	}
	agg := newChatAggregator()
	firstChunk := true
	err := server.streamer.StartStream(ctx, req, func(event modeladapter.ModelEvent) error {
		previousToolCount := len(agg.toolCalls)
		if err := agg.consume(event); err != nil {
			return err
		}
		if event.Kind == modeladapter.ModelEventKindTextDelta && event.Text != "" {
			if err := writeSSE(writer, flusher, agg.chunk(requestID, publicModel, event.Text, "", firstChunk)); err != nil {
				return err
			}
			firstChunk = false
		}
		for index := previousToolCount; index < len(agg.toolCalls); index++ {
			if err := writeSSE(writer, flusher, agg.toolChunk(requestID, publicModel, agg.toolCalls[index], firstChunk)); err != nil {
				return err
			}
			firstChunk = false
		}
		return nil
	})
	if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
		_ = writeSSE(writer, flusher, map[string]any{
			"error": apiError{Message: "请求已取消", Type: "invalid_request_error", Code: "cancelled"},
		})
		return
	}
	if err != nil {
		logger.Errorf("gateway chat stream failed public_model=%s err=%v", publicModel, err)
		_ = writeSSE(writer, flusher, map[string]any{
			"error": apiError{Message: "上游模型请求失败", Type: "api_error", Code: "provider_error"},
		})
		return
	}
	_ = writeSSE(writer, flusher, agg.chunk(requestID, publicModel, "", agg.finishReason(), false))
	_, _ = io.WriteString(writer, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func writeSSE(writer http.ResponseWriter, flusher http.Flusher, payload any) error {
	data, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := fmt.Fprintf(writer, "data: %s\n\n", data); err != nil {
		return err
	}
	if flusher != nil {
		flusher.Flush()
	}
	return nil
}
