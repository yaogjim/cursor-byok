package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

const (
	defaultConfigPath = "./config.yaml"
	defaultListenAddr = ":8041"
	defaultLogRoot    = "./logs"
	headerTraceID     = "X-Cursor-BYOK-Trace-ID"
	headerParentSpan  = "X-Cursor-BYOK-Parent-Span-ID"
)

var hopByHopHeaders = map[string]struct{}{
	"connection":          {},
	"proxy-connection":    {},
	"keep-alive":          {},
	"proxy-authenticate":  {},
	"proxy-authorization": {},
	"te":                  {},
	"trailer":             {},
	"transfer-encoding":   {},
	"upgrade":             {},
}

var defaultUpstreamTargets = map[string]string{
	"/aiserver.v1.AiService/StreamCpp":                      "https://api4.cursor.sh:443/aiserver.v1.AiService/StreamCpp",
	"/aiserver.v1.AiService/StreamNextCursorPrediction":     "https://api4.cursor.sh:443/aiserver.v1.AiService/StreamNextCursorPrediction",
	"/aiserver.v1.AiService/GetCppEditClassification":       "https://api4.cursor.sh:443/aiserver.v1.AiService/GetCppEditClassification",
	"/aiserver.v1.AiService/RefreshTabContext":              "https://api2.cursor.sh:443/aiserver.v1.AiService/RefreshTabContext",
	"/aiserver.v1.AiService/CppConfig":                      "https://api4.cursor.sh:443/aiserver.v1.AiService/CppConfig",
	"/aiserver.v1.AiService/CppEditHistoryStatus":           "https://api2.cursor.sh:443/aiserver.v1.AiService/CppEditHistoryStatus",
	"/aiserver.v1.AiService/CppAppend":                      "https://api3.cursor.sh:443/aiserver.v1.AiService/CppAppend",
	"/aiserver.v1.AiService/CppEditHistoryAppend":           "https://api3.cursor.sh:443/aiserver.v1.AiService/CppEditHistoryAppend",
	"/aiserver.v1.CppService/AvailableModels":               "https://api3.cursor.sh:443/aiserver.v1.CppService/AvailableModels",
	"/aiserver.v1.CppService/RecordCppFate":                 "https://api2.cursor.sh:443/aiserver.v1.CppService/RecordCppFate",
	"/aiserver.v1.AiService/ReportAiCodeChangeMetrics":      "https://api2.cursor.sh:443/aiserver.v1.AiService/ReportAiCodeChangeMetrics",
	"/aiserver.v1.AiService/WriteGitCommitMessage":          "https://api2.cursor.sh:443/aiserver.v1.AiService/WriteGitCommitMessage",
	"/aiserver.v1.AiService/WriteGitBranchName":             "https://api2.cursor.sh:443/aiserver.v1.AiService/WriteGitBranchName",
	"/aiserver.v1.FileSyncService/FSSyncFile":               "https://api4.cursor.sh:443/aiserver.v1.FileSyncService/FSSyncFile",
	"/aiserver.v1.FileSyncService/FSIsEnabledForUser":       "https://api4.cursor.sh:443/aiserver.v1.FileSyncService/FSIsEnabledForUser",
	"/aiserver.v1.FileSyncService/FSConfig":                 "https://api4.cursor.sh:443/aiserver.v1.FileSyncService/FSConfig",
	"/aiserver.v1.FileSyncService/FSUploadFile":             "https://api4.cursor.sh:443/aiserver.v1.FileSyncService/FSUploadFile",
	"/aiserver.v1.DashboardService/GetEffectiveUserPlugins": "https://api2.cursor.sh:443/aiserver.v1.DashboardService/GetEffectiveUserPlugins",
}

type appConfig struct {
	Token string
}

type serverApp struct {
	config          appConfig
	client          *http.Client
	upstreamTargets map[string]string
	recorder        *relayRecorder
}

func main() {
	cfg, err := loadConfig(defaultConfigPath)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "加载配置失败: %v\n", err)
		os.Exit(1)
	}
	recorder, recorderErr := openRelayRecorder(relayLogRoot())
	if recorderErr != nil {
		log.Printf("链路日志初始化失败 error_category=recorder_init_failed")
	} else {
		defer func() { _ = recorder.Close() }()
	}
	log.Printf("cursor-tab-server 启动 listen_addr=%s config_path=%s", defaultListenAddr, defaultConfigPath)
	server := &http.Server{
		Addr:              defaultListenAddr,
		Handler:           newServerAppWithRecorder(cfg, newHTTPClient(), defaultUpstreamTargets, recorder),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		_, _ = fmt.Fprintf(os.Stderr, "监听失败: %v\n", err)
		os.Exit(1)
	}
}

func newServerApp(cfg appConfig, client *http.Client, upstreamTargets map[string]string) http.Handler {
	return newServerAppWithRecorder(cfg, client, upstreamTargets, nil)
}

func newServerAppWithRecorder(cfg appConfig, client *http.Client, upstreamTargets map[string]string, recorder *relayRecorder) http.Handler {
	app := &serverApp{
		config:          cfg,
		client:          client,
		upstreamTargets: cloneUpstreamTargets(upstreamTargets),
		recorder:        recorder,
	}
	if app.client == nil {
		app.client = newHTTPClient()
	}
	return app
}

func (app *serverApp) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if err := app.handleProxy(writer, request); err != nil {
		http.Error(writer, err.Error(), http.StatusBadGateway)
	}
}

func (app *serverApp) handleProxy(writer http.ResponseWriter, request *http.Request) (resultErr error) {
	if app == nil {
		return fmt.Errorf("服务实例为空")
	}
	startedAt := time.Now()
	route := strings.TrimSpace(request.URL.Path)
	correlation := relayCorrelationFromHeaders(
		request.Header.Get(headerTraceID),
		request.Header.Get(headerParentSpan),
		request.Header.Get("x-request-id"),
	)
	requestBytes := int64(0)
	responseBytes := int64(0)
	statusCode := http.StatusBadGateway
	defer func() {
		status := "ok"
		errorCategory := ""
		if resultErr != nil {
			status = "error"
			errorCategory = "official_upstream_failed"
		} else if statusCode >= http.StatusBadRequest {
			status = "error"
			if statusCode >= http.StatusInternalServerError {
				errorCategory = "upstream_server_error"
			} else {
				errorCategory = "upstream_client_error"
			}
		}
		app.record(correlation, relayEvent{
			Layer:           "relay",
			Event:           "request_finished",
			Route:           route,
			ExecutionTarget: "official_upstream",
			Protocol:        "http",
			Status:          status,
			ErrorCategory:   errorCategory,
			DurationMS:      time.Since(startedAt).Milliseconds(),
			RequestBytes:    requestBytes,
			ResponseBytes:   responseBytes,
			Fields: map[string]any{
				"method":      request.Method,
				"status_code": statusCode,
			},
		})
	}()

	rawTarget, ok := app.upstreamTargets[route]
	if !ok {
		statusCode = http.StatusNotFound
		http.NotFound(writer, request)
		return nil
	}
	targetURL, err := url.Parse(rawTarget)
	if err != nil {
		return fmt.Errorf("解析上游地址失败: %w", err)
	}
	targetURL.RawQuery = request.URL.RawQuery

	requestBody := []byte{}
	if shouldRequestCarryBody(request.Method) {
		requestBody, err = io.ReadAll(request.Body)
		if err != nil {
			return fmt.Errorf("读取请求体失败: %w", err)
		}
	}
	requestBytes = int64(len(requestBody))
	app.record(correlation, relayEvent{
		Layer:           "relay",
		Event:           "request_started",
		Route:           route,
		ExecutionTarget: "official_upstream",
		Protocol:        "http",
		Status:          "started",
		RequestBytes:    requestBytes,
		Fields: map[string]any{
			"method":      request.Method,
			"target_host": targetURL.Host,
		},
	})

	upstreamRequest, err := http.NewRequestWithContext(request.Context(), request.Method, targetURL.String(), bytes.NewReader(requestBody))
	if err != nil {
		return fmt.Errorf("构建上游请求失败: %w", err)
	}
	copyRequestHeaders(upstreamRequest.Header, request.Header)
	upstreamRequest.Header.Del(headerTraceID)
	upstreamRequest.Header.Del(headerParentSpan)
	authorization := formatBearerAuthorization(app.config.Token)
	upstreamRequest.Header.Set("Authorization", authorization)
	upstreamRequest.Header.Set("x-cursor-checksum", buildCursorChecksum(authorization))
	if !shouldRequestCarryBody(request.Method) {
		upstreamRequest.Header.Del("content-length")
	} else {
		upstreamRequest.Header.Set("content-length", strconv.Itoa(len(requestBody)))
	}
	upstreamRequest.Host = targetURL.Host

	response, err := app.client.Do(upstreamRequest)
	if err != nil {
		log.Printf("上游转发失败 method=%s path=%s target_host=%s error_category=upstream_request_failed", request.Method, request.URL.Path, targetURL.Host)
		return fmt.Errorf("上游请求失败: %w", err)
	}
	defer response.Body.Close()
	statusCode = response.StatusCode
	log.Printf("上游响应 method=%s path=%s target_host=%s status=%d", request.Method, request.URL.Path, targetURL.Host, response.StatusCode)

	copyResponseHeaders(writer.Header(), response.Header)
	writer.WriteHeader(response.StatusCode)
	responseBytes, resultErr = copyStream(writer, response.Body)
	return resultErr
}

func (app *serverApp) record(correlation relayCorrelation, event relayEvent) {
	if app == nil || app.recorder == nil {
		return
	}
	app.recorder.Record(correlation, event)
}

func relayLogRoot() string {
	if value := strings.TrimSpace(os.Getenv("CURSOR_TAB_LOG_DIR")); value != "" {
		return value
	}
	return defaultLogRoot
}

func loadConfig(path string) (appConfig, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return appConfig{}, err
	}
	token, err := parseTokenYAML(contents)
	if err != nil {
		return appConfig{}, err
	}
	return appConfig{Token: token}, nil
}

func parseTokenYAML(contents []byte) (string, error) {
	var cfg appConfig
	if err := yaml.Unmarshal(contents, &cfg); err != nil {
		return "", fmt.Errorf("解析配置失败: %w", err)
	}
	token := strings.TrimSpace(cfg.Token)
	if token == "" {
		return "", fmt.Errorf("token 不能为空")
	}
	return token, nil
}

func copyRequestHeaders(target http.Header, source http.Header) {
	for key, values := range source {
		lowerKey := strings.ToLower(key)
		if _, exists := hopByHopHeaders[lowerKey]; exists {
			continue
		}
		for _, value := range values {
			target.Add(key, value)
		}
	}
}

func copyResponseHeaders(target http.Header, source http.Header) {
	for key, values := range source {
		lowerKey := strings.ToLower(key)
		if _, exists := hopByHopHeaders[lowerKey]; exists {
			continue
		}
		for _, value := range values {
			target.Add(key, value)
		}
	}
}

func copyStream(writer io.Writer, reader io.Reader) (int64, error) {
	buffer := make([]byte, 32*1024)
	var total int64
	for {
		readCount, readErr := reader.Read(buffer)
		if readCount > 0 {
			chunk := buffer[:readCount]
			written, writeErr := writer.Write(chunk)
			total += int64(written)
			if writeErr != nil {
				return total, writeErr
			}
			if written < len(chunk) {
				return total, io.ErrShortWrite
			}
			if flusher, ok := writer.(http.Flusher); ok {
				flusher.Flush()
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				return total, nil
			}
			return total, readErr
		}
	}
}

func shouldRequestCarryBody(method string) bool {
	switch strings.ToUpper(strings.TrimSpace(method)) {
	case http.MethodGet, http.MethodHead, http.MethodDelete:
		return false
	default:
		return true
	}
}

func formatBearerAuthorization(raw string) string {
	value := strings.TrimSpace(raw)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(value), "bearer ") {
		return value
	}
	return "Bearer " + value
}

func buildCursorChecksum(authorization string) string {
	const (
		checksumTimestampDivisor = 1_000_000
		checksumInitialSeed      = 165
	)
	timestamp := time.Now().UnixMilli() / checksumTimestampDivisor
	timestampBytes := make([]byte, 6)
	timestampBigInt := big.NewInt(timestamp)
	for index := 0; index < len(timestampBytes); index++ {
		shift := uint((len(timestampBytes) - 1 - index) * 8)
		timestampBytes[index] = byte(new(big.Int).Rsh(timestampBigInt, shift).Uint64() & 0xff)
	}
	seed := checksumInitialSeed
	for index := 0; index < len(timestampBytes); index++ {
		current := int(timestampBytes[index]^byte(seed)) + (index % 256)
		current &= 0xff
		timestampBytes[index] = byte(current)
		seed = current
	}
	prefix := strings.TrimRight(base64.StdEncoding.EncodeToString(timestampBytes), "=")
	hashBytes := sha256.Sum256([]byte(strings.TrimSpace(authorization)))
	hash := fmt.Sprintf("%x", hashBytes)
	return prefix + hash[:32]
}

func newHTTPClient() *http.Client {
	return &http.Client{}
}

func cloneUpstreamTargets(input map[string]string) map[string]string {
	output := make(map[string]string, len(input))
	for key, value := range input {
		output[key] = value
	}
	return output
}
