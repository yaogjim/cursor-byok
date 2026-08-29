package subscriptionauth

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strings"
	"time"
)

const (
	grokCreditsURL = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	codexUsageURL  = "https://chatgpt.com/backend-api/wham/usage"
)

func clampPercent(value float64) float64 {
	return math.Max(0, math.Min(100, value))
}

func parseResetTime(value any) time.Time {
	if value == nil {
		return time.Time{}
	}
	if n, ok := anyFloat(value); ok {
		if n > 10_000_000_000 {
			return time.UnixMilli(int64(n)).UTC()
		}
		return time.Unix(int64(n), 0).UTC()
	}
	if text := jsonText(value); text != "" {
		parsed, err := parseTime(text)
		if err == nil {
			return parsed
		}
	}
	return time.Time{}
}

func parseGrokUsage(body map[string]any) UsageSnapshot {
	config := body
	if nested, ok := body["config"].(map[string]any); ok {
		config = nested
	}
	used, ok := anyFloat(config["creditUsagePercent"])
	if !ok {
		used, ok = anyFloat(config["credit_usage_percent"])
	}
	if !ok {
		usedAmount, hasUsed := anyFloat(firstValue(config, "onDemandUsed", "on_demand_used"))
		capAmount, hasCap := anyFloat(firstValue(config, "onDemandCap", "on_demand_cap"))
		if hasUsed && hasCap && capAmount > 0 {
			used = (usedAmount / capAmount) * 100
			ok = true
		}
	}
	if !ok {
		if _, hasPeriod := config["currentPeriod"]; hasPeriod {
			used = 0
			ok = true
		} else if _, hasPeriod := config["current_period"]; hasPeriod {
			used = 0
			ok = true
		}
	}
	snapshot := UsageSnapshot{
		Provider:  ProviderGrok,
		UpdatedAt: time.Now().UTC(),
	}
	if ok {
		snapshot.UsedPercent = clampPercent(used)
		snapshot.RemainingPercent = clampPercent(100 - used)
		snapshot.LimitReached = snapshot.RemainingPercent <= 0
	}
	snapshot.ResetAt = parseResetTime(firstValue(
		config,
		"billingPeriodEnd",
		"billing_period_end",
	))
	if snapshot.ResetAt.IsZero() {
		period, _ := config["currentPeriod"].(map[string]any)
		if period == nil {
			period, _ = config["current_period"].(map[string]any)
		}
		if period != nil {
			snapshot.ResetAt = parseResetTime(period["end"])
		}
	}
	snapshot.PlanLabel = firstNonEmpty(
		jsonString(config, "subscriptionTierDisplay", "subscription_tier_display", "subscriptionTier", "product"),
	)
	return snapshot
}

func firstValue(object map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := object[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func parseCodexUsage(body map[string]any) UsageSnapshot {
	rateLimit := body
	if nested, ok := body["rate_limit"].(map[string]any); ok {
		rateLimit = nested
	}
	weekly, _ := rateLimit["secondary_window"].(map[string]any)
	primary, _ := rateLimit["primary_window"].(map[string]any)
	if weekly == nil {
		weekly = primary
	}
	used := 0.0
	if weekly != nil {
		if value, ok := anyFloat(weekly["used_percent"]); ok {
			used = value
		}
	}
	snapshot := UsageSnapshot{
		Provider:         ProviderCodex,
		UsedPercent:      clampPercent(used),
		RemainingPercent: clampPercent(100 - used),
		ResetAt:          windowResetAt(weekly),
		UpdatedAt:        time.Now().UTC(),
		PlanLabel:        codexPlanLabel(jsonString(body, "plan_type")),
	}
	if _, hasSecondary := rateLimit["secondary_window"]; hasSecondary && primary != nil {
		if sessionUsed, ok := anyFloat(primary["used_percent"]); ok {
			snapshot.SessionRemainingPercent = clampPercent(100 - sessionUsed)
		}
		snapshot.SessionResetAt = windowResetAt(primary)
	}
	if reached, ok := rateLimit["limit_reached"].(bool); ok {
		snapshot.LimitReached = reached
	} else {
		_, hasSecondary := rateLimit["secondary_window"]
		snapshot.LimitReached = snapshot.RemainingPercent <= 0 ||
			(hasSecondary && primary != nil && snapshot.SessionRemainingPercent <= 0)
	}
	return snapshot
}

func windowResetAt(window map[string]any) time.Time {
	if window == nil {
		return time.Time{}
	}
	if reset := parseResetTime(window["reset_at"]); !reset.IsZero() {
		return reset
	}
	if seconds, ok := anyFloat(window["reset_after_seconds"]); ok {
		return time.Now().UTC().Add(time.Duration(seconds * float64(time.Second)))
	}
	return time.Time{}
}

func codexPlanLabel(plan string) string {
	switch strings.ToLower(strings.TrimSpace(plan)) {
	case "plus":
		return "ChatGPT Plus"
	case "pro":
		return "ChatGPT Pro"
	case "team":
		return "ChatGPT Team"
	case "business":
		return "ChatGPT Business"
	case "enterprise":
		return "ChatGPT Enterprise"
	case "free":
		return "ChatGPT Free"
	case "go":
		return "ChatGPT Go"
	default:
		return strings.TrimSpace(plan)
	}
}

func (service *Service) fetchGrokUsage(ctx context.Context, accessToken string) (UsageSnapshot, error) {
	headers := map[string]string{
		"Authorization":    "Bearer " + accessToken,
		"Accept":           "application/json",
		"x-xai-token-auth": "xai-grok-cli",
	}
	status, body, err := doJSON(ctx, service.http, http.MethodGet, grokCreditsURL, headers, nil)
	if err != nil {
		return UsageSnapshot{}, err
	}
	if status >= 400 {
		return UsageSnapshot{}, httpStatusError("Grok usage lookup", status, body)
	}
	return parseGrokUsage(parseJSONObject(body)), nil
}

func (service *Service) fetchCodexUsage(ctx context.Context, accessToken string) (UsageSnapshot, error) {
	headers := map[string]string{
		"Authorization": "Bearer " + accessToken,
		"Accept":        "application/json",
		"originator":    "codex_cli_rs",
	}
	if accountID := chatgptAccountIDFromToken(accessToken); accountID != "" {
		headers["ChatGPT-Account-Id"] = accountID
	}
	status, body, err := doJSON(ctx, service.http, http.MethodGet, codexUsageURL, headers, nil)
	if err != nil {
		return UsageSnapshot{}, err
	}
	if status >= 400 {
		return UsageSnapshot{}, httpStatusError("Codex usage lookup", status, body)
	}
	return parseCodexUsage(parseJSONObject(body)), nil
}

func (service *Service) RefreshUsage(ctx context.Context, provider ProviderKind) (UsageSnapshot, error) {
	switch provider {
	case ProviderGrok:
		service.mu.Lock()
		account, ok, err := service.activeGrokLocked()
		service.mu.Unlock()
		if err != nil {
			return UsageSnapshot{}, err
		}
		if !ok {
			return UsageSnapshot{Provider: ProviderGrok}, ErrAuthRequired
		}
		usage, err := service.fetchGrokUsage(ctx, account.AccessToken)
		if err != nil {
			return UsageSnapshot{}, err
		}
		usage.AccountID = account.AccountID
		service.mu.Lock()
		defer service.mu.Unlock()
		file, loadErr := service.store.LoadGrok()
		if loadErr != nil {
			return usage, loadErr
		}
		for i := range file.Accounts {
			if file.Accounts[i].AccountID == account.AccountID {
				file.Accounts[i].PlanLabel = usage.PlanLabel
				file.Accounts[i].RemainingPercent = usage.RemainingPercent
				file.Accounts[i].UsedPercent = usage.UsedPercent
				if !usage.ResetAt.IsZero() {
					file.Accounts[i].ResetAtMS = usage.ResetAt.UnixMilli()
				}
				file.Accounts[i].LimitReached = usage.LimitReached
				file.Accounts[i].UpdatedAtMS = nowMS()
			}
		}
		if err := service.store.SaveGrok(file); err != nil {
			return usage, err
		}
		return usage, nil
	case ProviderCodex:
		auth, err := service.refreshCodex(ctx, false)
		if err != nil {
			return UsageSnapshot{}, err
		}
		usage, err := service.fetchCodexUsage(ctx, auth.Tokens.AccessToken)
		if err != nil {
			return UsageSnapshot{}, err
		}
		status := auth.status()
		usage.AccountID = status.AccountID
		next := *auth
		next.PlanLabel = usage.PlanLabel
		next.RemainingPercent = usage.RemainingPercent
		next.UsedPercent = usage.UsedPercent
		if !usage.ResetAt.IsZero() {
			next.ResetAtMS = usage.ResetAt.UnixMilli()
		}
		next.SessionRemainingPercent = usage.SessionRemainingPercent
		if !usage.SessionResetAt.IsZero() {
			next.SessionResetAtMS = usage.SessionResetAt.UnixMilli()
		}
		next.LimitReached = usage.LimitReached
		service.mu.Lock()
		saveErr := service.store.SaveCodex(next)
		service.mu.Unlock()
		if saveErr != nil {
			return usage, saveErr
		}
		return usage, nil
	default:
		return UsageSnapshot{}, safeErrorf("unsupported subscription provider")
	}
}

func IsQuotaError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ErrQuotaExhausted) {
		return true
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "insufficient_quota") ||
		strings.Contains(message, "usage_limit_reached") ||
		strings.Contains(message, "exceeded your current quota") ||
		strings.Contains(message, "quota_exceeded") ||
		strings.Contains(message, "5-hour") ||
		strings.Contains(message, "5 hour") {
		return true
	}
	return strings.Contains(message, "429") &&
		(strings.Contains(message, "quota") ||
			strings.Contains(message, "usage_limit") ||
			strings.Contains(message, "insufficient"))
}

func (service *Service) MarkQuotaExhausted(ctx context.Context, credentialID string) error {
	_ = ctx
	trimmed := trimSpace(credentialID)
	if trimmed == "" {
		return errors.New("credential id is empty")
	}
	if strings.HasPrefix(trimmed, string(ProviderCodex)+":") {
		return nil
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	return service.markGrokQuotaExhaustedLocked(trimmed)
}
