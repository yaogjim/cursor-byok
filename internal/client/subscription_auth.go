package client

import (
	"context"
	"fmt"
	"time"

	"cursor/internal/subscriptionauth"
)

func (s *ProxyService) subscriptionAuthService() *subscriptionauth.Service {
	if s == nil {
		return nil
	}
	return s.subscriptionAuth
}

func (s *ProxyService) GetCodexAuthStatus() subscriptionauth.AccountStatus {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.AccountStatus{Provider: subscriptionauth.ProviderCodex, State: subscriptionauth.StateMissing}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return service.CodexStatus(ctx)
}

func (s *ProxyService) ImportCodexAuth(path string) (subscriptionauth.AccountStatus, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.AccountStatus{Provider: subscriptionauth.ProviderCodex, State: subscriptionauth.StateError}, fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return service.ImportCodexAuthFile(ctx, path)
}

func (s *ProxyService) ImportCodexAuthContent(content string) (subscriptionauth.AccountStatus, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.AccountStatus{Provider: subscriptionauth.ProviderCodex, State: subscriptionauth.StateError}, fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return service.ImportCodexAuth(ctx, []byte(content))
}

func (s *ProxyService) ClearCodexAuth() (subscriptionauth.AccountStatus, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.AccountStatus{Provider: subscriptionauth.ProviderCodex, State: subscriptionauth.StateMissing}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := service.ClearCodexAuth(ctx); err != nil {
		return service.CodexStatus(ctx), err
	}
	return service.CodexStatus(ctx), nil
}

func (s *ProxyService) StartCodexDeviceAuth() (subscriptionauth.CodexDeviceCode, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.CodexDeviceCode{}, fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return service.StartCodexDeviceAuth(ctx)
}

func (s *ProxyService) PollCodexDeviceAuth(input subscriptionauth.CodexPollInput) (subscriptionauth.PollResult, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.PollResult{Status: subscriptionauth.PollStatusError, Error: "订阅认证服务未初始化"}, fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return service.PollCodexDeviceAuth(ctx, input)
}

func (s *ProxyService) StartGrokDeviceAuth() (subscriptionauth.GrokDeviceCode, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.GrokDeviceCode{}, fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return service.StartGrokDeviceAuth(ctx)
}

func (s *ProxyService) PollGrokDeviceAuth(input subscriptionauth.GrokPollInput) (subscriptionauth.PollResult, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.PollResult{Status: subscriptionauth.PollStatusError, Error: "订阅认证服务未初始化"}, fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return service.PollGrokDeviceAuth(ctx, input)
}

func (s *ProxyService) ListSubscriptionAccounts(provider string) ([]subscriptionauth.AccountStatus, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return nil, fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return service.ListAccounts(ctx, subscriptionauth.ProviderKind(provider))
}

func (s *ProxyService) ActivateSubscriptionAccount(accountID string) (subscriptionauth.AccountStatus, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.AccountStatus{State: subscriptionauth.StateError}, fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return service.ActivateAccount(ctx, accountID)
}

func (s *ProxyService) DeleteSubscriptionAccount(accountID string) error {
	service := s.subscriptionAuthService()
	if service == nil {
		return fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return service.DeleteAccount(ctx, accountID)
}

func (s *ProxyService) RefreshSubscriptionUsage(provider string) (subscriptionauth.UsageSnapshot, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.UsageSnapshot{}, fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return service.RefreshUsage(ctx, subscriptionauth.ProviderKind(provider))
}

func (s *ProxyService) RefreshSubscriptionAccountUsage(provider string, accountID string) (subscriptionauth.UsageSnapshot, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.UsageSnapshot{}, fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return service.RefreshAccountUsage(ctx, subscriptionauth.ProviderKind(provider), accountID)
}

func (s *ProxyService) PreviewSub2APIImport(path string, provider string) (subscriptionauth.Sub2APIImportPreview, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.Sub2APIImportPreview{}, fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return service.PreviewSub2APIFile(ctx, path, subscriptionauth.ProviderKind(provider))
}

func (s *ProxyService) ImportSub2APIAccounts(request subscriptionauth.Sub2APIImportRequest) (subscriptionauth.Sub2APIImportResult, error) {
	service := s.subscriptionAuthService()
	if service == nil {
		return subscriptionauth.Sub2APIImportResult{}, fmt.Errorf("订阅认证服务未初始化")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return service.ImportSub2APIFile(ctx, request)
}
