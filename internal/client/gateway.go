package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"cursor/internal/backend/forwarder"
	serverconfig "cursor/internal/backend/server/config"
	"cursor/internal/gateway"
	"cursor/internal/logger"
)

func (s *ProxyService) StartGateway() (ProxyState, error) {
	if s == nil {
		return ProxyState{}, errors.New("配置服务未初始化")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	cfg, err := s.LoadUserConfig()
	if err != nil {
		s.setGatewayError(err)
		s.emitState()
		return s.GetState(), err
	}
	if !cfg.Gateway.Enabled {
		err := errors.New("Gateway 未启用，请先保存启用配置")
		s.setGatewayError(err)
		s.emitState()
		return s.GetState(), err
	}
	if err := s.ensureBackendHost(); err != nil {
		s.setGatewayError(err)
		s.emitState()
		return s.GetState(), err
	}
	s.reconcileGateway(cfg)
	state := s.GetState()
	if state.GatewayLastError != "" {
		return state, errors.New(state.GatewayLastError)
	}
	if !state.GatewayRunning {
		err := errors.New(firstNonEmptyGatewayError(state.GatewayLastError, "Gateway 启动失败"))
		return state, err
	}
	s.emitState()
	return state, nil
}

func (s *ProxyService) StopGateway() (ProxyState, error) {
	if s == nil {
		return ProxyState{}, errors.New("配置服务未初始化")
	}
	s.lifecycleMu.Lock()
	defer s.lifecycleMu.Unlock()
	s.stopGatewayBestEffort()
	state := s.GetState()
	if state.GatewayLastError != "" {
		return state, errors.New(state.GatewayLastError)
	}
	s.emitState()
	return state, nil
}

func gatewayStateError(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func firstNonEmptyGatewayError(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return "Gateway 启动失败"
}

func (s *ProxyService) currentGatewayEnabled() bool {
	if s == nil {
		return false
	}
	if s.backendHost != nil && s.backendHost.ConfigManager() != nil {
		return s.backendHost.ConfigManager().Current().Gateway.Enabled
	}
	return false
}

func (s *ProxyService) ensureGateway() *gateway.Server {
	if s == nil {
		return nil
	}
	if s.gateway != nil {
		return s.gateway
	}
	if s.backendHost == nil || s.backendHost.ConfigManager() == nil {
		return nil
	}
	manager := s.backendHost.ConfigManager()
	s.gateway = gateway.New(forwarder.NewProviderGateway(manager, s.subscriptionAuth), manager)
	return s.gateway
}

func (s *ProxyService) reconcileGateway(cfg serverconfig.Config) {
	if s == nil {
		return
	}
	s.gatewayMu.Lock()
	defer s.gatewayMu.Unlock()
	instance := s.ensureGateway()
	if instance == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if !cfg.Gateway.Enabled {
		if err := instance.Stop(ctx); err != nil {
			logger.Errorf("gateway stop failed err=%v", err)
			s.setGatewayError(err)
			return
		}
		s.setGatewayError(nil)
		return
	}
	if err := instance.Start(cfg.Gateway); err != nil {
		logger.Errorf("gateway start failed listen_addr=%s err=%v", cfg.Gateway.ListenAddr, err)
		s.setGatewayError(err)
		return
	}
	s.setGatewayError(nil)
}

func (s *ProxyService) stopGatewayBestEffort() {
	if s == nil {
		return
	}
	s.gatewayMu.Lock()
	defer s.gatewayMu.Unlock()
	if s.gateway == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := s.gateway.Stop(ctx); err != nil {
		logger.Errorf("gateway stop failed err=%v", err)
		s.setGatewayError(err)
		return
	}
	s.setGatewayError(nil)
}

func (s *ProxyService) snapshotGateway() (listenAddr string, running bool, lastError string) {
	if s == nil {
		return "", false, ""
	}
	s.gatewayMu.Lock()
	instance := s.gateway
	s.gatewayMu.Unlock()
	if instance == nil {
		return "", false, ""
	}
	return instance.ListenAddr(), instance.Running(), instance.LastError()
}

func (s *ProxyService) setGatewayError(err error) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err == nil {
		s.gatewayLastError = ""
		return
	}
	s.gatewayLastError = strings.TrimSpace(err.Error())
}

func (s *ProxyService) CopyGatewayToken() (string, error) {
	if s == nil {
		return "", errors.New("配置服务未初始化")
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	cfg, err := s.loadUserConfig()
	if err != nil {
		return "", err
	}
	token := strings.TrimSpace(cfg.Gateway.Token)
	if token == "" {
		return "", errors.New("尚未生成 Gateway token")
	}
	return token, nil
}

type GatewayTestResult struct {
	ListenAddr string `json:"listenAddr"`
	ModelCount int    `json:"modelCount"`
	LatencyMS  int64  `json:"latencyMs"`
}

func (s *ProxyService) TestGateway() (GatewayTestResult, error) {
	if s == nil {
		return GatewayTestResult{}, errors.New("配置服务未初始化")
	}
	listenAddr, running, lastError := s.snapshotGateway()
	if !running || strings.TrimSpace(listenAddr) == "" {
		return GatewayTestResult{}, errors.New(firstNonEmptyGatewayError(lastError, "Gateway 未运行"))
	}
	cfg, err := s.LoadUserConfig()
	if err != nil {
		return GatewayTestResult{}, err
	}
	token := strings.TrimSpace(cfg.Gateway.Token)
	if token == "" {
		return GatewayTestResult{}, errors.New("尚未生成 Gateway token")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	requestURL := "http://" + listenAddr + "/v1/models"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return GatewayTestResult{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	startedAt := time.Now()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}, Timeout: 3 * time.Second}
	response, err := client.Do(request)
	if err != nil {
		return GatewayTestResult{}, fmt.Errorf("Gateway 连接失败: %w", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		return GatewayTestResult{}, fmt.Errorf("读取 Gateway 响应失败: %w", err)
	}
	if response.StatusCode != http.StatusOK {
		return GatewayTestResult{}, fmt.Errorf("Gateway 返回 HTTP %d", response.StatusCode)
	}
	var payload struct {
		Data []json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return GatewayTestResult{}, fmt.Errorf("Gateway 响应格式无效: %w", err)
	}
	return GatewayTestResult{
		ListenAddr: listenAddr,
		ModelCount: len(payload.Data),
		LatencyMS:  time.Since(startedAt).Milliseconds(),
	}, nil
}

func (s *ProxyService) RotateGatewayToken() (string, error) {
	if s == nil {
		return "", errors.New("配置服务未初始化")
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	token, err := serverconfig.GenerateGatewayToken()
	if err != nil {
		return "", err
	}
	ctx := context.Background()
	var saved serverconfig.Config
	if s.backendHost != nil && s.backendHost.ConfigManager() != nil {
		saved, err = s.backendHost.ConfigManager().SaveGatewayToken(ctx, token)
	} else if s.store != nil {
		saved, _, err = s.store.SaveGatewayToken(ctx, token)
	} else {
		return "", errors.New("配置存储未初始化")
	}
	if err != nil {
		return "", err
	}
	s.emitUserConfigChanged(saved)
	return token, nil
}
