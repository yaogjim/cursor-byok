package subscriptionauth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	codexClientID           = "app_EMoamEEZ73f0CkXaXp7hrann"
	codexDeviceCodeURL      = "https://auth.openai.com/api/accounts/deviceauth/usercode"
	codexDeviceTokenURL     = "https://auth.openai.com/api/accounts/deviceauth/token"
	codexOAuthTokenURL      = "https://auth.openai.com/oauth/token"
	codexDeviceCallbackURL  = "https://auth.openai.com/deviceauth/callback"
	codexVerificationURI    = "https://auth.openai.com/codex/device"
	codexRefreshSkew        = time.Minute
	codexStaleRefreshWindow = 8 * 24 * time.Hour
)

func parseCodexAuthJSON(content []byte) (storedCodexAuth, error) {
	var raw map[string]any
	if err := json.Unmarshal(content, &raw); err != nil {
		return storedCodexAuth{}, errors.New("auth.json 不是合法 JSON")
	}
	if _, exists := raw["chatgptAuthTokens"]; exists {
		return storedCodexAuth{}, errors.New("不支持 chatgptAuthTokens 外部 token 模式")
	}
	authMode := jsonString(raw, "auth_mode", "authMode")
	if authMode != "" && trimLower(authMode) != codexAuthMode {
		return storedCodexAuth{}, errors.New("仅支持 auth_mode=chatgpt 的 Codex 订阅认证")
	}
	tokens := nestedObject(raw, "tokens")
	if len(tokens) == 0 {
		tokens = raw
	}
	access := firstNonEmpty(jsonString(tokens, "access_token"), jsonString(raw, "access_token"))
	refresh := firstNonEmpty(jsonString(tokens, "refresh_token"), jsonString(raw, "refresh_token"))
	idToken := firstNonEmpty(jsonString(tokens, "id_token"), jsonString(raw, "id_token"))
	apiKey := firstNonEmpty(jsonString(raw, "OPENAI_API_KEY", "openai_api_key", "api_key"))
	if access == "" && apiKey != "" && refresh == "" {
		return storedCodexAuth{}, errors.New("仅包含 API key 的 auth.json 不能作为 Codex 订阅认证")
	}
	if access == "" {
		return storedCodexAuth{}, errors.New("auth.json 缺少 access_token")
	}
	if refresh == "" {
		return storedCodexAuth{}, errors.New("auth.json 缺少 refresh_token")
	}
	lastRefresh, _ := parseTime(firstNonEmpty(jsonString(raw, "last_refresh", "lastRefresh")))
	accountID := firstNonEmpty(jsonString(raw, "chatgpt_account_id", "chatgptAccountId", "account_id"), chatgptAccountIDFromToken(access))
	email := firstNonEmpty(jsonString(raw, "email"), emailFromToken(idToken, access))
	return storedCodexAuth{
		SchemaVersion: codexSchemaVersion,
		AuthMode:      codexAuthMode,
		LastRefresh:   lastRefresh,
		Tokens: storedTokenBundle{
			AccessToken:  access,
			RefreshToken: refresh,
			IDToken:      idToken,
		},
		ChatGPTAccountID: accountID,
		Email:            email,
		UpdatedAt:        time.Now().UTC(),
	}, nil
}

func parseTime(value string) (time.Time, error) {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return time.Time{}, errors.New("empty time")
	}
	layouts := []string{time.RFC3339Nano, time.RFC3339}
	for _, layout := range layouts {
		parsed, err := time.Parse(layout, trimmed)
		if err == nil {
			return parsed.UTC(), nil
		}
	}
	return time.Time{}, errors.New("unparseable time")
}

func (auth storedCodexAuth) status() AccountStatus {
	expires := jwtExpiresAt(auth.Tokens.AccessToken)
	state := StateReady
	if auth.AuthRequired {
		state = StateAuthRequired
	} else if trimSpace(auth.Tokens.AccessToken) == "" {
		state = StateMissing
	} else if !expires.IsZero() && !expires.After(time.Now().UTC()) && trimSpace(auth.Tokens.RefreshToken) == "" {
		state = StateAuthRequired
	}
	if state == StateReady && auth.LimitReached {
		state = StateQuotaExhausted
	}
	accountID, displayName, chatgptAccountID := accountIdentity(ProviderCodex, auth.Tokens.AccessToken, auth.Tokens.IDToken)
	return AccountStatus{
		AccountID:               accountID,
		Provider:                ProviderCodex,
		State:                   state,
		Email:                   firstNonEmpty(auth.Email, displayName),
		DisplayName:             firstNonEmpty(auth.Email, displayName),
		PlanLabel:               auth.PlanLabel,
		ChatGPTAccountID:        firstNonEmpty(auth.ChatGPTAccountID, chatgptAccountID),
		LastRefresh:             auth.LastRefresh,
		ExpiresAt:               expires,
		HasRefreshToken:         trimSpace(auth.Tokens.RefreshToken) != "",
		RemainingPercent:        auth.RemainingPercent,
		UsedPercent:             auth.UsedPercent,
		ResetAt:                 timeFromMS(auth.ResetAtMS),
		SessionRemainingPercent: auth.SessionRemainingPercent,
		SessionResetAt:          timeFromMS(auth.SessionResetAtMS),
		LimitReached:            auth.LimitReached,
		Active:                  auth.Active,
	}
}

func (auth storedCodexAuth) credential() Credential {
	accountID, _, chatgptAccountID, stableAccountID := accountIdentityWithStability(ProviderCodex, auth.Tokens.AccessToken, auth.Tokens.IDToken)
	return Credential{
		Provider:         ProviderCodex,
		AccountID:        accountID,
		AccessToken:      auth.Tokens.AccessToken,
		ChatGPTAccountID: firstNonEmpty(auth.ChatGPTAccountID, chatgptAccountID),
		ExpiresAt:        jwtExpiresAt(auth.Tokens.AccessToken),
		StableAccountID:  stableAccountID,
	}
}

func (auth storedCodexAuth) needsRefresh(now time.Time) bool {
	if trimSpace(auth.Tokens.RefreshToken) == "" {
		return false
	}
	expires := jwtExpiresAt(auth.Tokens.AccessToken)
	if !expires.IsZero() && !expires.After(now.Add(codexRefreshSkew)) {
		return true
	}
	if auth.LastRefresh.IsZero() {
		return false
	}
	return now.Sub(auth.LastRefresh) >= codexStaleRefreshWindow
}

func (service *Service) listCodexStatusesLocked() ([]AccountStatus, error) {
	file, err := service.store.LoadCodexFile()
	if err != nil {
		return nil, err
	}
	if restoreExpiredCodexQuota(file.Accounts, time.Now().UTC()) {
		if err := service.store.SaveCodexFile(file); err != nil {
			return nil, err
		}
	}
	out := make([]AccountStatus, 0, len(file.Accounts))
	for _, account := range file.Accounts {
		out = append(out, account.status())
	}
	return out, nil
}

func (service *Service) activateCodexLocked(accountID string) (AccountStatus, error) {
	file, err := service.store.LoadCodexFile()
	if err != nil {
		return AccountStatus{Provider: ProviderCodex, State: StateError}, err
	}
	now := time.Now().UTC()
	restored := restoreExpiredCodexQuota(file.Accounts, now)
	for i := range file.Accounts {
		if codexAccountID(file.Accounts[i]) != accountID {
			continue
		}
		if !codexAccountAvailable(file.Accounts[i], now) {
			if restored {
				if err := service.store.SaveCodexFile(file); err != nil {
					return AccountStatus{Provider: ProviderCodex, State: StateError}, err
				}
			}
			if file.Accounts[i].LimitReached {
				return file.Accounts[i].status(), ErrQuotaExhausted
			}
			return file.Accounts[i].status(), ErrAuthRequired
		}
		for j := range file.Accounts {
			file.Accounts[j].Active = j == i
		}
		if err := service.store.SaveCodexFile(file); err != nil {
			return AccountStatus{Provider: ProviderCodex, State: StateError}, err
		}
		return file.Accounts[i].status(), nil
	}
	if restored {
		if err := service.store.SaveCodexFile(file); err != nil {
			return AccountStatus{Provider: ProviderCodex, State: StateError}, err
		}
	}
	return AccountStatus{Provider: ProviderCodex, State: StateMissing}, errors.New("Codex 账号不存在")
}

func (service *Service) deleteCodexLocked(accountID string) error {
	file, err := service.store.LoadCodexFile()
	if err != nil {
		return err
	}
	kept := make([]storedCodexAuth, 0, len(file.Accounts))
	deleted := false
	deletedActive := false
	for _, account := range file.Accounts {
		if codexAccountID(account) == accountID {
			deleted = true
			deletedActive = account.Active
			continue
		}
		kept = append(kept, account)
	}
	if !deleted {
		return errors.New("Codex 账号不存在")
	}
	if deletedActive {
		for i := range kept {
			if codexAccountAvailable(kept[i], time.Now().UTC()) {
				kept[i].Active = true
				break
			}
		}
	}
	file.Accounts = kept
	return service.store.SaveCodexFile(file)
}

func (service *Service) ImportCodexAuth(ctx context.Context, content []byte) (AccountStatus, error) {
	_ = ctx
	parsed, err := parseCodexAuthJSON(content)
	if err != nil {
		return AccountStatus{Provider: ProviderCodex, State: StateError, Error: RedactText(err.Error())}, RedactError(err)
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	if err := service.store.SaveCodex(parsed); err != nil {
		return AccountStatus{Provider: ProviderCodex, State: StateError, Error: "保存 Codex 认证副本失败"}, err
	}
	file, err := service.store.LoadCodexFile()
	if err != nil {
		return AccountStatus{Provider: ProviderCodex, State: StateError, Error: "读取 Codex 认证副本失败"}, err
	}
	for _, account := range file.Accounts {
		if codexAccountID(account) == codexAccountID(parsed) {
			return account.status(), nil
		}
	}
	return parsed.status(), nil
}

func (service *Service) ImportCodexAuthFile(ctx context.Context, path string) (AccountStatus, error) {
	trimmed := trimSpace(path)
	if trimmed == "" {
		return AccountStatus{Provider: ProviderCodex, State: StateError, Error: "未选择 auth.json"}, errors.New("未选择 auth.json")
	}
	before, err := os.ReadFile(trimmed)
	if err != nil {
		return AccountStatus{Provider: ProviderCodex, State: StateError, Error: "读取 auth.json 失败"}, err
	}
	status, importErr := service.ImportCodexAuth(ctx, before)
	after, _ := os.ReadFile(trimmed)
	if !bytesEqual(before, after) {
		return AccountStatus{Provider: ProviderCodex, State: StateError, Error: "禁止修改原始 auth.json"}, errors.New("import mutated source auth.json")
	}
	return status, importErr
}

func bytesEqual(left []byte, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}

func (service *Service) ClearCodexAuth(ctx context.Context) error {
	_ = ctx
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.store.ClearCodex()
}

func (service *Service) CodexStatus(ctx context.Context) AccountStatus {
	_ = ctx
	service.mu.Lock()
	defer service.mu.Unlock()
	auth, err := service.store.LoadCodex()
	if err != nil {
		return AccountStatus{Provider: ProviderCodex, State: StateError, Error: "读取 Codex 认证副本失败"}
	}
	if auth == nil {
		return AccountStatus{Provider: ProviderCodex, State: StateMissing}
	}
	return auth.status()
}

func (service *Service) loadCodexLocked() (*storedCodexAuth, error) {
	return service.store.LoadCodex()
}

func (service *Service) selectCodexLocked(credentialID string, requireAvailable bool) (*storedCodexAuth, error) {
	file, err := service.store.LoadCodexFile()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	changed := restoreExpiredCodexQuota(file.Accounts, now)
	requested := trimSpace(credentialID)
	selected := -1
	for i := range file.Accounts {
		accountID, _, _ := accountIdentity(ProviderCodex, file.Accounts[i].Tokens.AccessToken, file.Accounts[i].Tokens.IDToken)
		if requested != "" && accountID == requested {
			selected = i
			break
		}
	}
	if selected < 0 && requested == "" {
		for i := range file.Accounts {
			if file.Accounts[i].Active && codexAccountAvailable(file.Accounts[i], now) {
				selected = i
				break
			}
		}
		if selected < 0 {
			for i := range file.Accounts {
				if codexAccountAvailable(file.Accounts[i], now) {
					selected = i
					break
				}
			}
		}
	}
	if selected < 0 {
		if changed {
			if err := service.store.SaveCodexFile(file); err != nil {
				return nil, err
			}
		}
		for _, account := range file.Accounts {
			if account.LimitReached {
				return nil, ErrQuotaExhausted
			}
		}
		return nil, ErrAuthRequired
	}
	if requireAvailable && !codexAccountAvailable(file.Accounts[selected], now) {
		return nil, ErrAuthRequired
	}
	if requested == "" {
		for i := range file.Accounts {
			nextActive := i == selected
			if file.Accounts[i].Active != nextActive {
				file.Accounts[i].Active = nextActive
				changed = true
			}
		}
	}
	if changed {
		if err := service.store.SaveCodexFile(file); err != nil {
			return nil, err
		}
	}
	account := file.Accounts[selected]
	return &account, nil
}

func codexQuotaResetAt(account storedCodexAuth) time.Time {
	reset := timeFromMS(account.ResetAtMS)
	sessionReset := timeFromMS(account.SessionResetAtMS)
	latest := time.Time{}
	if account.RemainingPercent <= 0 && !reset.IsZero() {
		latest = reset
	}
	if account.SessionRemainingPercent <= 0 && !sessionReset.IsZero() && (latest.IsZero() || sessionReset.After(latest)) {
		latest = sessionReset
	}
	if !latest.IsZero() {
		return latest
	}
	if reset.IsZero() || (!sessionReset.IsZero() && sessionReset.After(reset)) {
		return sessionReset
	}
	return reset
}

func restoreExpiredCodexQuota(accounts []storedCodexAuth, now time.Time) bool {
	changed := false
	for i := range accounts {
		reset := codexQuotaResetAt(accounts[i])
		if accounts[i].LimitReached && !reset.IsZero() && !reset.After(now) {
			accounts[i].LimitReached = false
			changed = true
		}
	}
	return changed
}

func codexAccountAvailable(account storedCodexAuth, now time.Time) bool {
	if account.AuthRequired || trimSpace(account.Tokens.AccessToken) == "" {
		return false
	}
	if account.LimitReached {
		return false
	}
	return true
}

func codexAccountID(account storedCodexAuth) string {
	accountID, _, _ := accountIdentity(ProviderCodex, account.Tokens.AccessToken, account.Tokens.IDToken)
	return accountID
}

func (service *Service) markCodexQuotaExhaustedLocked(accountID string) error {
	file, err := service.store.LoadCodexFile()
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	current := -1
	alreadyExhausted := false
	for i := range file.Accounts {
		if codexAccountID(file.Accounts[i]) == accountID {
			current = i
			alreadyExhausted = file.Accounts[i].LimitReached
			file.Accounts[i].LimitReached = true
			break
		}
	}
	if current < 0 {
		return nil
	}
	if !file.Accounts[current].Active {
		if alreadyExhausted {
			return nil
		}
		return service.store.SaveCodexFile(file)
	}
	next := -1
	for offset := 1; offset <= len(file.Accounts); offset++ {
		idx := (current + offset) % len(file.Accounts)
		if idx != current && codexAccountAvailable(file.Accounts[idx], now) {
			next = idx
			break
		}
	}
	if next < 0 {
		if err := service.store.SaveCodexFile(file); err != nil {
			return err
		}
		return ErrQuotaExhausted
	}
	for i := range file.Accounts {
		file.Accounts[i].Active = i == next
	}
	return service.store.SaveCodexFile(file)
}

func (service *Service) markCodexAuthRequired(accountID string) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	file, err := service.store.LoadCodexFile()
	if err != nil {
		return err
	}
	for i := range file.Accounts {
		if codexAccountID(file.Accounts[i]) == accountID {
			file.Accounts[i].AuthRequired = true
			break
		}
	}
	return service.store.SaveCodexFile(file)
}

func (service *Service) replaceCodexAccount(accountID string, next storedCodexAuth) error {
	service.mu.Lock()
	defer service.mu.Unlock()
	file, err := service.store.LoadCodexFile()
	if err != nil {
		return err
	}
	for i := range file.Accounts {
		if codexAccountID(file.Accounts[i]) != accountID {
			continue
		}
		next.Active = file.Accounts[i].Active
		next.AuthRequired = false
		file.Accounts[i] = next
		return service.store.SaveCodexFile(file)
	}
	return ErrAuthRequired
}

func (service *Service) refreshCodex(ctx context.Context, force bool, credentialID string) (*storedCodexAuth, error) {
	service.codexRefreshMu.Lock()
	defer service.codexRefreshMu.Unlock()
	service.mu.Lock()
	auth, err := service.selectCodexLocked(credentialID, credentialID == "")
	service.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if auth == nil {
		return nil, ErrAuthRequired
	}
	if !force && !auth.needsRefresh(time.Now().UTC()) {
		return auth, nil
	}
	if trimSpace(auth.Tokens.RefreshToken) == "" {
		return nil, ErrAuthRequired
	}
	form := url.Values{}
	form.Set("grant_type", "refresh_token")
	form.Set("client_id", codexClientID)
	form.Set("refresh_token", auth.Tokens.RefreshToken)
	status, body, err := doForm(ctx, service.http, codexOAuthTokenURL, nil, form)
	if err != nil {
		return nil, err
	}
	parsed := parseJSONObject(body)
	access := jsonString(parsed, "access_token")
	if status >= 400 || access == "" {
		if status == http.StatusUnauthorized || status == http.StatusForbidden {
			if markErr := service.markCodexAuthRequired(codexAccountID(*auth)); markErr != nil {
				return nil, markErr
			}
			return nil, ErrAuthRequired
		}
		return nil, httpStatusError("Codex token refresh", status, body)
	}
	next := *auth
	next.Tokens.AccessToken = access
	if refresh := jsonString(parsed, "refresh_token"); refresh != "" {
		next.Tokens.RefreshToken = refresh
	}
	if idToken := jsonString(parsed, "id_token"); idToken != "" {
		next.Tokens.IDToken = idToken
	}
	next.LastRefresh = time.Now().UTC()
	next.ChatGPTAccountID = firstNonEmpty(next.ChatGPTAccountID, chatgptAccountIDFromToken(access))
	next.Email = firstNonEmpty(next.Email, emailFromToken(next.Tokens.IDToken, access))
	if err := service.replaceCodexAccount(codexAccountID(*auth), next); err != nil {
		return auth, err
	}
	return &next, nil
}

func (service *Service) StartCodexDeviceAuth(ctx context.Context) (CodexDeviceCode, error) {
	payload := map[string]string{"client_id": codexClientID}
	status, body, err := doJSON(ctx, service.http, http.MethodPost, codexDeviceCodeURL, nil, payload)
	if err != nil {
		return CodexDeviceCode{}, err
	}
	if status >= 400 {
		return CodexDeviceCode{}, httpStatusError("Codex device code", status, body)
	}
	parsed := parseJSONObject(body)
	userCode := jsonString(parsed, "user_code", "usercode")
	deviceCode := jsonString(parsed, "device_auth_id", "device_code")
	if userCode == "" || deviceCode == "" {
		return CodexDeviceCode{}, errors.New("Codex 设备码响应缺少 user_code")
	}
	challenge := CodexDeviceCode{
		PollToken:               newPollToken(),
		DeviceCode:              deviceCode,
		UserCode:                userCode,
		VerificationURI:         codexVerificationURI,
		VerificationURIComplete: codexVerificationURI,
		ExpiresIn:               jsonInt(parsed, 900, "expires_in"),
		Interval:                jsonInt(parsed, 5, "interval"),
	}
	service.rememberPending(challenge.PollToken, pendingAuth{
		provider:   ProviderCodex,
		deviceCode: deviceCode,
		userCode:   userCode,
		expiresAt:  time.Now().Add(time.Duration(challenge.ExpiresIn) * time.Second),
	})
	return challenge, nil
}

func (service *Service) PollCodexDeviceAuth(ctx context.Context, input CodexPollInput) (PollResult, error) {
	pending, ok := service.pendingByInput(input.PollToken, input.DeviceCode)
	deviceCode := firstNonEmpty(pending.deviceCode, input.DeviceCode)
	userCode := firstNonEmpty(pending.userCode, input.UserCode)
	if !ok && deviceCode == "" {
		return PollResult{Status: PollStatusError, Error: "设备授权会话不存在或已过期"}, nil
	}
	payload := map[string]string{
		"device_auth_id": deviceCode,
		"user_code":      userCode,
	}
	status, body, err := doJSON(ctx, service.http, http.MethodPost, codexDeviceTokenURL, nil, payload)
	if err != nil {
		return PollResult{Status: PollStatusError, Error: "轮询 Codex 设备授权失败"}, err
	}
	parsed := parseJSONObject(body)
	kind := classifyCodexDevicePoll(status, parsed)
	switch kind {
	case pollKindPending:
		return PollResult{Status: PollStatusPending}, nil
	case pollKindSlowDown:
		return PollResult{Status: PollStatusSlowDown, RetryAfterSeconds: 5}, nil
	case pollKindExpired:
		service.forgetPendingByInput(input.PollToken, deviceCode)
		return PollResult{Status: PollStatusExpired, Error: nestedErrorMessage(parsed, "Device authorization code expired")}, nil
	case pollKindDenied:
		service.forgetPendingByInput(input.PollToken, deviceCode)
		return PollResult{Status: PollStatusAccessDenied, Error: nestedErrorMessage(parsed, "User denied authorization")}, nil
	case pollKindAuthCode:
		return service.exchangeCodexAuthCode(ctx, jsonString(parsed, "authorization_code"), jsonString(parsed, "code_verifier"), input.PollToken)
	case pollKindTokens:
		return service.commitCodexTokens(ctx, jsonString(parsed, "access_token"), jsonString(parsed, "refresh_token"), jsonString(parsed, "id_token"), input.PollToken)
	default:
		service.forgetPendingByInput(input.PollToken, deviceCode)
		return PollResult{Status: PollStatusError, Error: nestedErrorMessage(parsed, "Codex 设备授权失败")}, nil
	}
}

func (service *Service) exchangeCodexAuthCode(ctx context.Context, code string, verifier string, pollToken string) (PollResult, error) {
	if trimSpace(code) == "" || trimSpace(verifier) == "" {
		return PollResult{Status: PollStatusError, Error: "Codex 授权码不完整"}, nil
	}
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", codexDeviceCallbackURL)
	form.Set("client_id", codexClientID)
	form.Set("code_verifier", verifier)
	status, body, err := doForm(ctx, service.http, codexOAuthTokenURL, nil, form)
	if err != nil {
		return PollResult{Status: PollStatusError, Error: "交换 Codex 授权码失败"}, err
	}
	parsed := parseJSONObject(body)
	access := jsonString(parsed, "access_token")
	if access == "" {
		return PollResult{Status: PollStatusError, Error: nestedErrorMessage(parsed, "Failed to exchange Codex authorization code")}, httpStatusError("Codex authorization code exchange", status, body)
	}
	return service.commitCodexTokens(ctx, access, jsonString(parsed, "refresh_token"), jsonString(parsed, "id_token"), pollToken)
}

func (service *Service) commitCodexTokens(ctx context.Context, access string, refresh string, idToken string, pollToken string) (PollResult, error) {
	_ = ctx
	if trimSpace(access) == "" || trimSpace(refresh) == "" {
		return PollResult{Status: PollStatusError, Error: "Codex 设备授权未返回 refresh_token"}, nil
	}
	auth := storedCodexAuth{
		SchemaVersion: codexSchemaVersion,
		AuthMode:      codexAuthMode,
		LastRefresh:   time.Now().UTC(),
		Tokens: storedTokenBundle{
			AccessToken:  access,
			RefreshToken: refresh,
			IDToken:      idToken,
		},
		ChatGPTAccountID: chatgptAccountIDFromToken(access),
		Email:            emailFromToken(idToken, access),
	}
	service.mu.Lock()
	err := service.store.SaveCodex(auth)
	status := auth.status()
	if err == nil {
		if file, loadErr := service.store.LoadCodexFile(); loadErr == nil {
			for _, account := range file.Accounts {
				if codexAccountID(account) == codexAccountID(auth) {
					status = account.status()
					break
				}
			}
		}
	}
	service.mu.Unlock()
	if err != nil {
		return PollResult{Status: PollStatusError, Error: "保存 Codex 认证副本失败"}, err
	}
	service.forgetPending(pollToken)
	return PollResult{Status: PollStatusSuccess, Account: &status}, nil
}

type pollKind int

const (
	pollKindError pollKind = iota
	pollKindPending
	pollKindSlowDown
	pollKindExpired
	pollKindDenied
	pollKindTokens
	pollKindAuthCode
)

func classifyCodexDevicePoll(status int, body map[string]any) pollKind {
	if status == http.StatusForbidden || status == http.StatusNotFound {
		return pollKindPending
	}
	code := nestedErrorCode(body)
	message := nestedErrorMessage(body, "")
	if isCodexPendingCode(code) || isCodexPendingMessage(message) {
		return pollKindPending
	}
	if status >= 200 && status < 300 {
		if jsonString(body, "access_token") != "" {
			return pollKindTokens
		}
		if jsonString(body, "authorization_code") != "" && jsonString(body, "code_verifier") != "" {
			return pollKindAuthCode
		}
	}
	switch code {
	case "slow_down":
		return pollKindSlowDown
	case "expired_token", "expired":
		return pollKindExpired
	case "access_denied", "denied":
		return pollKindDenied
	case "":
		if body["error"] == nil && status >= 400 {
			return pollKindPending
		}
	}
	return pollKindError
}

func isCodexPendingCode(code string) bool {
	switch code {
	case "authorization_pending", "pending", "waiting", "in_progress", "device_authorization_pending":
		return true
	default:
		return false
	}
}

func isCodexPendingMessage(message string) bool {
	lower := strings.ToLower(message)
	return strings.Contains(lower, "authorization is pending") ||
		strings.Contains(lower, "authorization_pending") ||
		strings.Contains(lower, "device authorization is pending")
}

func nestedErrorCode(body map[string]any) string {
	if code := jsonString(body, "error", "status", "state"); code != "" && !strings.Contains(code, " ") {
		return code
	}
	if nested, ok := body["error"].(map[string]any); ok {
		return jsonString(nested, "code", "type")
	}
	return ""
}

func nestedErrorMessage(body map[string]any, fallback string) string {
	if nested, ok := body["error"].(map[string]any); ok {
		if message := jsonString(nested, "message"); message != "" {
			return RedactText(message)
		}
	}
	if message := jsonString(body, "error_description", "message"); message != "" {
		return RedactText(message)
	}
	if fallback != "" {
		return fallback
	}
	return "OAuth error"
}

func newPollToken() string {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return hex.EncodeToString([]byte(time.Now().UTC().Format(time.RFC3339Nano)))
	}
	return hex.EncodeToString(buf)
}
