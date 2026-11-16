//go:build manualcacheab

package modeladapter

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"cursor/internal/appdata"
	"cursor/internal/modelchannel"
	"cursor/internal/netproxy"
	"cursor/internal/subscriptionauth"
)

// TestManualCodexCacheAB 是 V3 真实上游 A/B 探针，默认不编译也不在 CI 运行。
// 运行方式：
//
//	CACHE_AB_CONFIRM=1 go test -tags manualcacheab ./internal/backend/agent/model -run TestManualCodexCacheAB -v -count=1
//
// 只读使用本机已导入的 Codex 账号；串行、限速，不打印 token 或请求正文。
// 每档使用不同前缀，避免跨档共享上游自动缓存导致归因错误。
func TestManualCodexCacheAB(t *testing.T) {
	if os.Getenv("CACHE_AB_CONFIRM") != "1" {
		t.Skip("set CACHE_AB_CONFIRM=1 to run real Codex upstream A/B")
	}
	service := subscriptionauth.NewService(appdata.SubscriptionAuthRootPath(), netproxy.NewHTTPClient(30*time.Second))
	ctx := context.Background()
	cred, err := service.Resolve(ctx, subscriptionauth.CredentialSourceCodex)
	if err != nil {
		t.Fatalf("resolve codex credential: %v", err)
	}
	if !cred.StableAccountID || strings.TrimSpace(cred.AccountID) == "" {
		t.Fatal("stable codex account identity unavailable")
	}
	key, err := service.CodexAffinityKey()
	if err != nil {
		t.Fatalf("load affinity key: %v", err)
	}

	adapter := NewOpenAIAdapter()
	conversationID := "cache-ab-long-" + time.Now().UTC().Format("20060102T150405")
	prefixes := map[string]string{
		CodexAffinityProfileControl:   strings.Repeat("Cache probe delta line with deterministic content. ", 350),
		CodexAffinityProfilePromptKey: strings.Repeat("Cache probe echo line with deterministic content. ", 350),
		CodexAffinityProfileFull:      strings.Repeat("Cache probe foxtrot line with deterministic content. ", 350),
	}

	for _, profile := range []string{CodexAffinityProfileFull, CodexAffinityProfilePromptKey, CodexAffinityProfileControl} {
		for attempt := 1; attempt <= 2; attempt++ {
			modelCallID := fmt.Sprintf("%s-%s-%d", conversationID, profile, attempt)
			req := StreamRequest{
				RequestID:        "cache-ab-long",
				RunID:            "cache-ab-long",
				ModelCallID:      modelCallID,
				ConversationID:   conversationID,
				Messages:         []Message{{Role: "user", Content: prefixes[profile] + fmt.Sprintf("\n\nReply with exactly: ok-%d", attempt)}},
				BaseURL:          subscriptionauth.CodexResponsesURL,
				APIKey:           cred.AccessToken,
				CredentialSource: string(subscriptionauth.CredentialSourceCodex),
				CredentialID:     cred.AccountID,
				ChatGPTAccountID: cred.ChatGPTAccountID,
				StableAccountID:  cred.StableAccountID,
				ProviderModelID:  "gpt-6-sol",
				OpenAIEndpoint:   modelchannel.OpenAIEndpointResponses,
				MaxTokens:        32,
			}
			if profile != CodexAffinityProfileControl {
				affinity, err := DeriveCodexAffinity(key, cred.AccountID, conversationID, modelCallID)
				if err != nil {
					t.Fatalf("derive affinity: %v", err)
				}
				switch profile {
				case CodexAffinityProfilePromptKey:
					req.CodexAffinity.PromptCacheKey = affinity.PromptCacheKey
				case CodexAffinityProfileFull:
					req.CodexAffinity = affinity
				}
			}
			req.CodexAffinityProfile = profile

			var usage ModelEvent
			err := adapter.Stream(ctx, req, func(event ModelEvent) error {
				if event.UsagePresent {
					usage = event
				}
				return nil
			})
			if err != nil {
				t.Errorf("profile=%s attempt=%d ERROR=%s", profile, attempt, summarizeModelArtifactError(err))
				time.Sleep(15 * time.Second)
				continue
			}
			t.Logf("profile=%s attempt=%d usage_present=%v input=%d output=%d cache_read=%d cache_present=%v cache_write=%d",
				profile, attempt, usage.UsagePresent, usage.InputTokens, usage.OutputTokens,
				usage.CacheReadTokens, usage.CacheReadPresent, usage.CacheWriteTokens)
			time.Sleep(15 * time.Second)
		}
	}
}
