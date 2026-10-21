package modeladapter

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"cursor/internal/subscriptionauth"
)

const (
	CodexAffinityProfileEnv       = "CURSOR_BYOK_CODEX_AFFINITY_PROFILE"
	CodexAffinityProfileControl   = "control"
	CodexAffinityProfilePromptKey = "prompt_key"
	CodexAffinityProfileFull      = "full"

	codexAffinityDomainPromptCacheKey  = "prompt_cache_key"
	codexAffinityDomainSessionID       = "session-id"
	codexAffinityDomainThreadID        = "thread-id"
	codexAffinityDomainClientRequestID = "x-client-request-id"
)

type codexAffinityKeySource interface {
	CodexAffinityKey() ([]byte, error)
}

// NormalizeCodexAffinityProfile returns a stable allowlisted experiment profile.
// Unknown or empty values fail closed to control.
func NormalizeCodexAffinityProfile(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case CodexAffinityProfilePromptKey:
		return CodexAffinityProfilePromptKey
	case CodexAffinityProfileFull:
		return CodexAffinityProfileFull
	default:
		return CodexAffinityProfileControl
	}
}

func DeriveCodexAffinity(key []byte, accountID string, conversationID string, modelCallID string) (CodexAffinity, error) {
	if len(key) != subscriptionauth.AffinityKeySize {
		return CodexAffinity{}, subscriptionauth.ErrAffinityKeyLength
	}
	accountID = strings.TrimSpace(accountID)
	conversationID = strings.TrimSpace(conversationID)
	modelCallID = strings.TrimSpace(modelCallID)
	if accountID == "" || conversationID == "" || modelCallID == "" {
		return CodexAffinity{}, errCodexAffinityInputsUnavailable
	}
	return CodexAffinity{
		PromptCacheKey:  hmacCodexAffinity(key, codexAffinityDomainPromptCacheKey, accountID, conversationID),
		SessionID:       hmacCodexAffinity(key, codexAffinityDomainSessionID, accountID, conversationID),
		ThreadID:        hmacCodexAffinity(key, codexAffinityDomainThreadID, accountID, conversationID),
		ClientRequestID: hmacCodexAffinity(key, codexAffinityDomainClientRequestID, accountID, conversationID, modelCallID),
	}, nil
}

func hmacCodexAffinity(key []byte, parts ...string) string {
	mac := hmac.New(sha256.New, key)
	for i, part := range parts {
		if i > 0 {
			_, _ = mac.Write([]byte{0})
		}
		_, _ = mac.Write([]byte(part))
	}
	return hex.EncodeToString(mac.Sum(nil))
}

func (router *Router) attachCodexAffinity(req StreamRequest) StreamRequest {
	key, err := router.codexAffinityKey()
	if err != nil {
		key = nil
	}
	return attachCodexAffinity(req, key)
}

func (router *Router) codexAffinityKey() ([]byte, error) {
	if router == nil || router.credentials == nil {
		return nil, errCodexAffinityKeyUnavailable
	}
	source, ok := router.credentials.(codexAffinityKeySource)
	if !ok || source == nil {
		return nil, errCodexAffinityKeyUnavailable
	}
	return source.CodexAffinityKey()
}

func attachCodexAffinity(req StreamRequest, key []byte) StreamRequest {
	req.CodexAffinity = CodexAffinity{}
	if subscriptionauth.NormalizeCredentialSource(req.CredentialSource) != subscriptionauth.CredentialSourceCodex {
		return req
	}
	if !req.StableAccountID || strings.TrimSpace(req.CredentialID) == "" {
		return req
	}
	profile := NormalizeCodexAffinityProfile(os.Getenv(CodexAffinityProfileEnv))
	if profile == CodexAffinityProfileControl {
		return req
	}
	derived, err := DeriveCodexAffinity(key, req.CredentialID, req.ConversationID, req.ModelCallID)
	if err != nil {
		return req
	}
	switch profile {
	case CodexAffinityProfilePromptKey:
		req.CodexAffinity.PromptCacheKey = derived.PromptCacheKey
	case CodexAffinityProfileFull:
		req.CodexAffinity = derived
	}
	return req
}

var (
	errCodexAffinityKeyUnavailable    = errors.New("codex affinity key unavailable")
	errCodexAffinityInputsUnavailable = errors.New("codex affinity inputs unavailable")
)
