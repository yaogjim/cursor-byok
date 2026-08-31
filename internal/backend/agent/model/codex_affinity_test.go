package modeladapter

import (
	"bytes"
	"testing"

	"cursor/internal/subscriptionauth"
)

func TestDeriveCodexAffinityContracts(t *testing.T) {
	key := bytes.Repeat([]byte{0x2a}, subscriptionauth.AffinityKeySize)
	first, err := DeriveCodexAffinity(key, "codex:account-a", "conversation-a", "call-a")
	if err != nil {
		t.Fatal(err)
	}
	repeated, err := DeriveCodexAffinity(key, "codex:account-a", "conversation-a", "call-a")
	if err != nil {
		t.Fatal(err)
	}
	if first != repeated {
		t.Fatal("same account, conversation, and logical request did not remain stable")
	}
	if first.PromptCacheKey == first.SessionID || first.PromptCacheKey == first.ThreadID || first.SessionID == first.ThreadID {
		t.Fatal("stable affinity fields were not domain-separated")
	}

	nextCall, err := DeriveCodexAffinity(key, "codex:account-a", "conversation-a", "call-b")
	if err != nil {
		t.Fatal(err)
	}
	if nextCall.ClientRequestID == first.ClientRequestID {
		t.Fatal("different logical requests reused x-client-request-id")
	}
	if nextCall.PromptCacheKey != first.PromptCacheKey || nextCall.SessionID != first.SessionID || nextCall.ThreadID != first.ThreadID {
		t.Fatal("stable session fields drifted across logical requests")
	}

	otherAccount, err := DeriveCodexAffinity(key, "codex:account-b", "conversation-a", "call-a")
	if err != nil {
		t.Fatal(err)
	}
	if otherAccount == first {
		t.Fatal("different accounts shared affinity partition")
	}
}

func TestDeriveCodexAffinityRequiresStableInputs(t *testing.T) {
	key := bytes.Repeat([]byte{0x2a}, subscriptionauth.AffinityKeySize)
	for _, tc := range []struct {
		name           string
		accountID      string
		conversationID string
		modelCallID    string
	}{
		{name: "account", conversationID: "conversation-a", modelCallID: "call-a"},
		{name: "conversation", accountID: "codex:account-a", modelCallID: "call-a"},
		{name: "logical request", accountID: "codex:account-a", conversationID: "conversation-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DeriveCodexAffinity(key, tc.accountID, tc.conversationID, tc.modelCallID); err == nil {
				t.Fatal("missing affinity input was accepted")
			}
		})
	}
}

func TestRouterCodexAffinityRetryContracts(t *testing.T) {
	t.Setenv(CodexAffinityProfileEnv, CodexAffinityProfileFull)
	key := bytes.Repeat([]byte{0x33}, subscriptionauth.AffinityKeySize)
	base := StreamRequest{
		CredentialSource: "codex",
		CredentialID:     "codex:account-a",
		StableAccountID:  true,
		ConversationID:   "conversation-a",
		ModelCallID:      "call-a",
	}
	initial := attachCodexAffinity(base, key)

	sameAccount := base
	sameAccount.APIKey = "refreshed-token"
	refreshed := attachCodexAffinity(sameAccount, key)
	if refreshed.CodexAffinity != initial.CodexAffinity {
		t.Fatal("same-account credential refresh changed affinity")
	}

	rotatedAccount := base
	rotatedAccount.CredentialID = "codex:account-b"
	rotated := attachCodexAffinity(rotatedAccount, key)
	if rotated.CodexAffinity == initial.CodexAffinity {
		t.Fatal("quota account rotation did not change affinity partition")
	}
}

func TestAttachCodexAffinityProfiles(t *testing.T) {
	key := bytes.Repeat([]byte{0x2a}, subscriptionauth.AffinityKeySize)
	base := StreamRequest{
		CredentialSource: "codex",
		CredentialID:     "codex:account-a",
		StableAccountID:  true,
		ConversationID:   "conversation-a",
		ModelCallID:      "call-a",
	}

	t.Setenv(CodexAffinityProfileEnv, CodexAffinityProfilePromptKey)
	promptOnly := attachCodexAffinity(base, key)
	if promptOnly.CodexAffinity.PromptCacheKey == "" || promptOnly.CodexAffinity.SessionID != "" || promptOnly.CodexAffinity.ThreadID != "" || promptOnly.CodexAffinity.ClientRequestID != "" {
		t.Fatalf("prompt_key profile produced unexpected fields: %+v", promptOnly.CodexAffinity)
	}

	t.Setenv(CodexAffinityProfileEnv, CodexAffinityProfileFull)
	full := attachCodexAffinity(base, key)
	if full.CodexAffinity.PromptCacheKey == "" || full.CodexAffinity.SessionID == "" || full.CodexAffinity.ThreadID == "" || full.CodexAffinity.ClientRequestID == "" {
		t.Fatal("full profile did not derive all affinity fields")
	}

	unstable := base
	unstable.StableAccountID = false
	if got := attachCodexAffinity(unstable, key); got.CodexAffinity != (CodexAffinity{}) {
		t.Fatal("unstable account identity enabled affinity")
	}

	missingConversation := base
	missingConversation.ConversationID = ""
	if got := attachCodexAffinity(missingConversation, key); got.CodexAffinity != (CodexAffinity{}) {
		t.Fatal("missing conversation identity enabled affinity")
	}
}
