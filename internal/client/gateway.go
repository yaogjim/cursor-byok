package client

import (
	"context"
	"errors"
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
	s.gateway = gateway.New(forwarder.NewProviderGateway(manager), manager)
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
