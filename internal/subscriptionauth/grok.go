package subscriptionauth

import (
	"context"
	"errors"
	"net/url"
	"strings"
	"time"
)

const (
	grokClientID      = "b1a00492-073a-47ea-816f-4c329264a828"
	grokDeviceCodeURL = "https://auth.x.ai/oauth2/device/code"
	grokTokenURL      = "https://auth.x.ai/oauth2/token"
	grokDefaultScope  = "openid profile email offline_access grok-cli:access api:access"
)

func (service *Service) StartGrokDeviceAuth(ctx context.Context) (GrokDeviceCode, error) {
	form := url.Values{}
	form.Set("client_id", grokClientID)
	form.Set("scope", grokDefaultScope)
	status, body, err := doForm(ctx, service.http, grokDeviceCodeURL, nil, form)
	if err != nil {
		return GrokDeviceCode{}, err
	}
	if status >= 400 {
		return GrokDeviceCode{}, httpStatusError("Grok device code", status, body)
	}
	parsed := parseJSONObject(body)
	userCode := jsonString(parsed, "user_code")
	deviceCode := jsonString(parsed, "device_code")
	if userCode == "" || deviceCode == "" {
		return GrokDeviceCode{}, errors.New("Grok 设备码响应不完整")
	}
	challenge := GrokDeviceCode{
		PollToken:               newPollToken(),
		DeviceCode:              deviceCode,
		UserCode:                userCode,
		VerificationURI:         jsonString(parsed, "verification_uri"),
		VerificationURIComplete: jsonString(parsed, "verification_uri_complete"),
		ExpiresIn:               jsonInt(parsed, 900, "expires_in"),
		Interval:                jsonInt(parsed, 5, "interval"),
	}
	service.rememberPending(challenge.PollToken, pendingAuth{
		provider:   ProviderGrok,
		deviceCode: deviceCode,
		userCode:   userCode,
		expiresAt:  time.Now().Add(time.Duration(challenge.ExpiresIn) * time.Second),
	})
	return challenge, nil
}

func (service *Service) PollGrokDeviceAuth(ctx context.Context, input GrokPollInput) (PollResult, error) {
	pending, ok := service.pendingByInput(input.PollToken, input.DeviceCode)
	deviceCode := firstNonEmpty(pending.deviceCode, input.DeviceCode)
	if !ok && deviceCode == "" {
		return PollResult{Status: PollStatusError, Error: "设备授权会话不存在或已过期"}, nil
	}
	form := url.Values{}
	form.Set("grant_type", "urn:ietf:params:oauth:grant-type:device_code")
	form.Set("client_id", grokClientID)
	form.Set("device_code", deviceCode)
	status, body, err := doForm(ctx, service.http, grokTokenURL, nil, form)
	if err != nil {
		return PollResult{Status: PollStatusError, Error: "轮询 Grok 设备授权失败"}, err
	}
	parsed := parseJSONObject(body)
	if status >= 200 && status < 300 {
		access := jsonString(parsed, "access_token")
		refresh := jsonString(parsed, "refresh_token")
		if access == "" {
			return PollResult{Status: PollStatusError, Error: "Grok 授权响应缺少 access_token"}, nil
		}
		account, saveErr := service.upsertGrokAccount(access, refresh, true)
		if saveErr != nil {
			return PollResult{Status: PollStatusError, Error: "保存 Grok 账号失败"}, saveErr
		}
		service.forgetPending(input.PollToken)
		statusDTO := grokAccountStatus(account)
		return PollResult{Status: PollStatusSuccess, Account: &statusDTO}, nil
	}
	code := jsonString(parsed, "error")
	desc := RedactText(jsonString(parsed, "error_description"))
	switch code {
	case "authorization_pending":
		return PollResult{Status: PollStatusPending}, nil
	case "slow_down":
		return PollResult{Status: PollStatusSlowDown, RetryAfterSeconds: 5}, nil
	case "expired_token":
		return PollResult{Status: PollStatusExpired, Error: firstNonEmpty(desc, "Device authorization code expired")}, nil
	case "access_denied":
		return PollResult{Status: PollStatusAccessDenied, Error: firstNonEmpty(desc, "User denied authorization")}, nil
	default:
		return PollResult{Status: PollStatusError, Error: firstNonEmpty(desc, "OAuth error")}, nil
	}
}

func (service *Service) upsertGrokAccount(accessToken string, refreshToken string, makeActive bool) (storedGrokAccount, error) {
	accountID, displayName, _ := accountIdentity(ProviderGrok, accessToken, "")
	now := nowMS()
	service.mu.Lock()
	defer service.mu.Unlock()
	file, err := service.store.LoadGrok()
	if err != nil {
		return storedGrokAccount{}, err
	}
	activate := makeActive
	kept := make([]storedGrokAccount, 0, len(file.Accounts)+1)
	var existing *storedGrokAccount
	for _, account := range file.Accounts {
		sameToken := account.AccessToken == accessToken
		sameIdentity := account.AccountID == accountID
		if sameToken || sameIdentity {
			if account.Active {
				activate = true
			}
			copied := account
			existing = &copied
			continue
		}
		kept = append(kept, account)
	}
	next := storedGrokAccount{
		AccountID:    accountID,
		Provider:     string(ProviderGrok),
		AccessToken:  accessToken,
		RefreshToken: refreshToken,
		DisplayName:  displayName,
		UpdatedAtMS:  now,
	}
	if existing != nil {
		next.PlanLabel = existing.PlanLabel
		next.RemainingPercent = existing.RemainingPercent
		next.UsedPercent = existing.UsedPercent
		next.ResetAtMS = existing.ResetAtMS
		next.LimitReached = existing.LimitReached
		next.Active = existing.Active
		if trimSpace(refreshToken) == "" {
			next.RefreshToken = existing.RefreshToken
		}
	}
	if activate || (existing == nil && len(kept) == 0) {
		for i := range kept {
			kept[i].Active = false
			kept[i].UpdatedAtMS = now
		}
		next.Active = true
	}
	file.Accounts = append(kept, next)
	if err := service.store.SaveGrok(file); err != nil {
		return storedGrokAccount{}, err
	}
	return next, nil
}

func grokAccountStatus(account storedGrokAccount) AccountStatus {
	state := StateReady
	if account.LimitReached {
		state = StateQuotaExhausted
	}
	if trimSpace(account.AccessToken) == "" {
		state = StateMissing
	}
	return AccountStatus{
		AccountID:        account.AccountID,
		Provider:         ProviderGrok,
		State:            state,
		DisplayName:      account.DisplayName,
		Email:            account.DisplayName,
		PlanLabel:        account.PlanLabel,
		HasRefreshToken:  trimSpace(account.RefreshToken) != "",
		RemainingPercent: account.RemainingPercent,
		UsedPercent:      account.UsedPercent,
		ResetAt:          timeFromMS(account.ResetAtMS),
		LimitReached:     account.LimitReached,
		Active:           account.Active,
	}
}

func (service *Service) listGrokLocked() ([]storedGrokAccount, error) {
	file, err := service.store.LoadGrok()
	if err != nil {
		return nil, err
	}
	return file.Accounts, nil
}

func (service *Service) activeGrokLocked() (storedGrokAccount, bool, error) {
	accounts, err := service.listGrokLocked()
	if err != nil {
		return storedGrokAccount{}, false, err
	}
	for _, account := range accounts {
		if account.Active && trimSpace(account.AccessToken) != "" && !account.LimitReached {
			return account, true, nil
		}
	}
	for _, account := range accounts {
		if account.Active && trimSpace(account.AccessToken) != "" {
			return account, true, nil
		}
	}
	return storedGrokAccount{}, false, nil
}

func (service *Service) ActivateAccount(ctx context.Context, accountID string) (AccountStatus, error) {
	_ = ctx
	trimmed := trimSpace(accountID)
	if trimmed == "" {
		return AccountStatus{State: StateError, Error: "账号 ID 不能为空"}, errors.New("账号 ID 不能为空")
	}
	if strings.HasPrefix(trimmed, string(ProviderCodex)+":") {
		status := service.CodexStatus(ctx)
		if status.AccountID != trimmed && status.State != StateMissing {
			return status, nil
		}
		if status.State == StateMissing {
			return status, errors.New("Codex 账号不存在")
		}
		return status, nil
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	file, err := service.store.LoadGrok()
	if err != nil {
		return AccountStatus{Provider: ProviderGrok, State: StateError}, err
	}
	found := false
	now := nowMS()
	for i := range file.Accounts {
		if file.Accounts[i].AccountID == trimmed {
			file.Accounts[i].Active = true
			file.Accounts[i].UpdatedAtMS = now
			found = true
			continue
		}
		if file.Accounts[i].Active {
			file.Accounts[i].Active = false
			file.Accounts[i].UpdatedAtMS = now
		}
	}
	if !found {
		return AccountStatus{Provider: ProviderGrok, State: StateMissing}, errors.New("Grok 账号不存在")
	}
	if err := service.store.SaveGrok(file); err != nil {
		return AccountStatus{Provider: ProviderGrok, State: StateError}, err
	}
	for _, account := range file.Accounts {
		if account.AccountID == trimmed {
			return grokAccountStatus(account), nil
		}
	}
	return AccountStatus{Provider: ProviderGrok, State: StateError}, errors.New("Grok 账号不存在")
}

func (service *Service) DeleteAccount(ctx context.Context, accountID string) error {
	trimmed := trimSpace(accountID)
	if trimmed == "" {
		return errors.New("账号 ID 不能为空")
	}
	if strings.HasPrefix(trimmed, string(ProviderCodex)+":") || trimmed == string(ProviderCodex) {
		return service.ClearCodexAuth(ctx)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	file, err := service.store.LoadGrok()
	if err != nil {
		return err
	}
	kept := make([]storedGrokAccount, 0, len(file.Accounts))
	deletedActive := false
	found := false
	for _, account := range file.Accounts {
		if account.AccountID == trimmed {
			found = true
			deletedActive = account.Active
			continue
		}
		kept = append(kept, account)
	}
	if !found {
		return errors.New("Grok 账号不存在")
	}
	if deletedActive && len(kept) > 0 {
		kept[0].Active = true
		kept[0].UpdatedAtMS = nowMS()
	}
	file.Accounts = kept
	return service.store.SaveGrok(file)
}

func (service *Service) ListAccounts(ctx context.Context, provider ProviderKind) ([]AccountStatus, error) {
	switch provider {
	case ProviderCodex, "":
		status := service.CodexStatus(ctx)
		if provider == ProviderCodex {
			if status.State == StateMissing {
				return []AccountStatus{}, nil
			}
			return []AccountStatus{status}, nil
		}
		accounts := []AccountStatus{}
		if status.State != StateMissing {
			accounts = append(accounts, status)
		}
		service.mu.Lock()
		grok, err := service.listGrokLocked()
		service.mu.Unlock()
		if err != nil {
			return nil, err
		}
		for _, account := range grok {
			accounts = append(accounts, grokAccountStatus(account))
		}
		return accounts, nil
	case ProviderGrok:
		service.mu.Lock()
		defer service.mu.Unlock()
		grok, err := service.listGrokLocked()
		if err != nil {
			return nil, err
		}
		out := make([]AccountStatus, 0, len(grok))
		for _, account := range grok {
			out = append(out, grokAccountStatus(account))
		}
		return out, nil
	default:
		return nil, errors.New("unsupported subscription provider")
	}
}
