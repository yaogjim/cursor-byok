package subscriptionauth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"time"
)

type sub2APIFile struct {
	Accounts []sub2APIAccount `json:"accounts"`
}

type sub2APIAccount struct {
	Name        string         `json:"name"`
	Platform    string         `json:"platform"`
	Type        string         `json:"type"`
	Credentials map[string]any `json:"credentials"`
}

type parsedSub2APIAccount struct {
	preview Sub2APIAccountPreview
	codex   storedCodexAuth
	access  string
	refresh string
}

func parseSub2APIAccounts(content []byte, provider ProviderKind) ([]parsedSub2APIAccount, int, error) {
	if provider != ProviderCodex && provider != ProviderGrok {
		return nil, 0, errors.New("不支持的订阅类型")
	}
	var file sub2APIFile
	if err := json.Unmarshal(content, &file); err != nil {
		return nil, 0, errors.New("sub2api 文件不是合法 JSON")
	}
	if file.Accounts == nil {
		return nil, 0, errors.New("sub2api 文件缺少 accounts")
	}
	parsed := make([]parsedSub2APIAccount, 0, len(file.Accounts))
	skipped := 0
	seen := map[string]struct{}{}
	for _, account := range file.Accounts {
		platform := trimLower(account.Platform)
		authType := trimLower(account.Type)
		credentials := account.Credentials
		access := jsonString(credentials, "access_token")
		refresh := jsonString(credentials, "refresh_token")
		if authType != "oauth" || access == "" || refresh == "" {
			skipped++
			continue
		}
		switch provider {
		case ProviderCodex:
			if platform != "openai" && platform != "codex" {
				skipped++
				continue
			}
			idToken := jsonString(credentials, "id_token")
			accountID, displayName, tokenChatGPTID := accountIdentity(ProviderCodex, access, idToken)
			if _, exists := seen[accountID]; exists {
				skipped++
				continue
			}
			seen[accountID] = struct{}{}
			email := firstNonEmpty(jsonString(credentials, "email"), emailFromToken(idToken, access), displayName)
			plan := codexPlanLabel(jsonString(credentials, "plan_type"))
			parsed = append(parsed, parsedSub2APIAccount{
				preview: Sub2APIAccountPreview{AccountID: accountID, Provider: provider, Name: trimSpace(account.Name), Email: email, PlanLabel: plan},
				codex: storedCodexAuth{
					SchemaVersion:    codexSchemaVersion,
					AuthMode:         codexAuthMode,
					Tokens:           storedTokenBundle{AccessToken: access, RefreshToken: refresh, IDToken: idToken},
					ChatGPTAccountID: firstNonEmpty(jsonString(credentials, "chatgpt_account_id"), tokenChatGPTID),
					Email:            email,
					PlanLabel:        plan,
					UpdatedAt:        nowUTC(),
				},
			})
		case ProviderGrok:
			if platform != "grok" && platform != "xai" && platform != "x.ai" {
				skipped++
				continue
			}
			accountID, displayName, _ := accountIdentity(ProviderGrok, access, "")
			if _, exists := seen[accountID]; exists {
				skipped++
				continue
			}
			seen[accountID] = struct{}{}
			email := firstNonEmpty(jsonString(credentials, "email"), displayName)
			parsed = append(parsed, parsedSub2APIAccount{
				preview: Sub2APIAccountPreview{AccountID: accountID, Provider: provider, Name: trimSpace(account.Name), Email: email, PlanLabel: trimSpace(jsonString(credentials, "plan_type"))},
				access:  access,
				refresh: refresh,
			})
		}
	}
	return parsed, skipped, nil
}

func nowUTC() time.Time {
	return time.Now().UTC()
}

func (service *Service) PreviewSub2APIFile(ctx context.Context, path string, provider ProviderKind) (Sub2APIImportPreview, error) {
	_ = ctx
	trimmed := trimSpace(path)
	if trimmed == "" {
		return Sub2APIImportPreview{Provider: provider}, errors.New("未选择 sub2api 文件")
	}
	content, err := os.ReadFile(trimmed)
	if err != nil {
		return Sub2APIImportPreview{Provider: provider}, errors.New("读取 sub2api 文件失败")
	}
	parsed, skipped, err := parseSub2APIAccounts(content, provider)
	if err != nil {
		return Sub2APIImportPreview{Provider: provider}, err
	}
	existing, err := service.ListAccounts(context.Background(), provider)
	if err != nil {
		return Sub2APIImportPreview{Provider: provider}, err
	}
	existingIDs := make(map[string]struct{}, len(existing))
	for _, account := range existing {
		existingIDs[account.AccountID] = struct{}{}
	}
	preview := Sub2APIImportPreview{Provider: provider, Accounts: make([]Sub2APIAccountPreview, 0, len(parsed)), SkippedCount: skipped}
	for _, account := range parsed {
		item := account.preview
		_, item.AlreadyExists = existingIDs[item.AccountID]
		preview.Accounts = append(preview.Accounts, item)
	}
	return preview, nil
}

func (service *Service) ImportSub2APIFile(ctx context.Context, request Sub2APIImportRequest) (Sub2APIImportResult, error) {
	provider := request.Provider
	trimmed := trimSpace(request.Path)
	if trimmed == "" {
		return Sub2APIImportResult{Provider: provider}, errors.New("未选择 sub2api 文件")
	}
	content, err := os.ReadFile(trimmed)
	if err != nil {
		return Sub2APIImportResult{Provider: provider}, errors.New("读取 sub2api 文件失败")
	}
	parsed, _, err := parseSub2APIAccounts(content, provider)
	if err != nil {
		return Sub2APIImportResult{Provider: provider}, err
	}
	selected := make(map[string]struct{}, len(request.AccountIDs))
	for _, accountID := range request.AccountIDs {
		if trimmed := trimSpace(accountID); trimmed != "" {
			selected[trimmed] = struct{}{}
		}
	}
	if len(selected) == 0 {
		return Sub2APIImportResult{Provider: provider}, errors.New("请至少选择一个账号")
	}
	result := Sub2APIImportResult{Provider: provider, Accounts: make([]AccountStatus, 0, len(selected))}
	for _, account := range parsed {
		if _, ok := selected[account.preview.AccountID]; !ok {
			continue
		}
		var status AccountStatus
		switch provider {
		case ProviderCodex:
			payload, marshalErr := json.Marshal(map[string]any{
				"auth_mode": codexAuthMode,
				"tokens": map[string]string{
					"access_token":  account.codex.Tokens.AccessToken,
					"refresh_token": account.codex.Tokens.RefreshToken,
					"id_token":      account.codex.Tokens.IDToken,
				},
				"chatgpt_account_id": account.codex.ChatGPTAccountID,
				"email":              account.codex.Email,
			})
			if marshalErr != nil {
				return result, marshalErr
			}
			status, err = service.ImportCodexAuth(ctx, payload)
			if err == nil && account.codex.PlanLabel != "" {
				service.mu.Lock()
				file, loadErr := service.store.LoadCodexFile()
				if loadErr == nil {
					for i := range file.Accounts {
						if codexAccountID(file.Accounts[i]) == status.AccountID {
							file.Accounts[i].PlanLabel = account.codex.PlanLabel
						}
					}
					loadErr = service.store.SaveCodexFile(file)
				}
				service.mu.Unlock()
				if loadErr != nil {
					err = loadErr
				} else {
					status.PlanLabel = account.codex.PlanLabel
				}
			}
		case ProviderGrok:
			var stored storedGrokAccount
			stored, err = service.upsertGrokAccount(account.access, account.refresh, false)
			status = grokAccountStatus(stored)
		default:
			err = errors.New("不支持的订阅类型")
		}
		if err != nil {
			return result, RedactError(err)
		}
		result.Accounts = append(result.Accounts, status)
		delete(selected, account.preview.AccountID)
	}
	if len(selected) != 0 {
		return result, errors.New("选择的账号不属于当前接入类型或已不在文件中")
	}
	return result, nil
}
