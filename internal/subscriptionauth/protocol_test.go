package subscriptionauth

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type protocolCase struct {
	Status int             `json:"status"`
	Body   json.RawMessage `json:"body"`
}

func protocolFixtures(t *testing.T) map[string]protocolCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "protocol.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixtures map[string]protocolCase
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	return fixtures
}

func (c protocolCase) object(t *testing.T) map[string]any {
	t.Helper()
	if len(c.Body) == 0 || string(c.Body) == "null" {
		return map[string]any{}
	}
	var body map[string]any
	if err := json.Unmarshal(c.Body, &body); err != nil {
		t.Fatal(err)
	}
	if body == nil {
		return map[string]any{}
	}
	return body
}

func (c protocolCase) response() *http.Response {
	return jsonResponse(c.Status, json.RawMessage(c.Body))
}

func assertNoSecrets(t *testing.T, value any) {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	text := string(payload)
	for _, needle := range []string{"eyJ", "access_token", "refresh_token", "id_token", "Bearer "} {
		if strings.Contains(text, needle) {
			t.Fatalf("secret material leaked into DTO %T: %s", value, text)
		}
	}
}

func TestProtocolCodexDevicePollFixtures(t *testing.T) {
	fixtures := protocolFixtures(t)
	cases := []struct {
		name string
		want pollKind
	}{
		{"codex_device_pending_403", pollKindPending},
		{"codex_device_pending_404", pollKindPending},
		{"codex_device_pending_400", pollKindPending},
		{"codex_device_authorization_code", pollKindAuthCode},
		{"codex_device_token_success", pollKindTokens},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fx := fixtures[tc.name]
			if fx.Status == 0 && len(fx.Body) == 0 {
				t.Fatalf("missing fixture %s", tc.name)
			}
			got := classifyCodexDevicePoll(fx.Status, fx.object(t))
			if got != tc.want {
				t.Fatalf("classify = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestProtocolCodexAuthorizationCodePKCE(t *testing.T) {
	fixtures := protocolFixtures(t)
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case codexDeviceCodeURL:
			return fixtures["codex_device_code"].response(), nil
		case codexDeviceTokenURL:
			return fixtures["codex_device_authorization_code"].response(), nil
		case codexOAuthTokenURL:
			if err := req.ParseForm(); err != nil {
				t.Fatal(err)
			}
			if req.PostForm.Get("grant_type") != "authorization_code" {
				t.Fatalf("grant_type = %q", req.PostForm.Get("grant_type"))
			}
			if req.PostForm.Get("client_id") != codexClientID {
				t.Fatalf("client_id = %q", req.PostForm.Get("client_id"))
			}
			if req.PostForm.Get("code") != "auth-code" {
				t.Fatalf("code = %q", req.PostForm.Get("code"))
			}
			if req.PostForm.Get("code_verifier") != "pkce-verifier" {
				t.Fatalf("code_verifier = %q", req.PostForm.Get("code_verifier"))
			}
			if req.PostForm.Get("redirect_uri") != codexDeviceCallbackURL {
				t.Fatalf("redirect_uri = %q", req.PostForm.Get("redirect_uri"))
			}
			return fixtures["codex_oauth_token_success"].response(), nil
		default:
			t.Fatalf("unexpected url %s", req.URL)
			return nil, nil
		}
	})
	service := NewService(t.TempDir(), client)
	challenge, err := service.StartCodexDeviceAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, challenge)
	if challenge.UserCode != "ABCD-1234" || challenge.PollToken == "" {
		t.Fatalf("challenge = %+v", challenge)
	}
	result, err := service.PollCodexDeviceAuth(context.Background(), CodexPollInput{PollToken: challenge.PollToken})
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != PollStatusSuccess || result.Account == nil {
		t.Fatalf("poll = %+v", result)
	}
	if result.Account.Email != "user@example.com" || result.Account.ChatGPTAccountID != "acct-fixture" {
		t.Fatalf("account = %+v", result.Account)
	}
	assertNoSecrets(t, result)
}

func TestProtocolCodexDevicePendingAndTokenSuccess(t *testing.T) {
	fixtures := protocolFixtures(t)
	for _, name := range []string{"codex_device_pending_403", "codex_device_pending_404", "codex_device_pending_400"} {
		t.Run(name, func(t *testing.T) {
			client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case codexDeviceCodeURL:
					return fixtures["codex_device_code"].response(), nil
				case codexDeviceTokenURL:
					return fixtures[name].response(), nil
				default:
					t.Fatalf("unexpected url %s", req.URL)
					return nil, nil
				}
			})
			service := NewService(t.TempDir(), client)
			challenge, err := service.StartCodexDeviceAuth(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			result, err := service.PollCodexDeviceAuth(context.Background(), CodexPollInput{PollToken: challenge.PollToken})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != PollStatusPending {
				t.Fatalf("status = %s", result.Status)
			}
			assertNoSecrets(t, result)
		})
	}

	t.Run("codex_device_token_success", func(t *testing.T) {
		client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
			switch req.URL.String() {
			case codexDeviceCodeURL:
				return fixtures["codex_device_code"].response(), nil
			case codexDeviceTokenURL:
				return fixtures["codex_oauth_token_success"].response(), nil
			default:
				t.Fatalf("unexpected url %s", req.URL)
				return nil, nil
			}
		})
		service := NewService(t.TempDir(), client)
		challenge, err := service.StartCodexDeviceAuth(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		result, err := service.PollCodexDeviceAuth(context.Background(), CodexPollInput{PollToken: challenge.PollToken})
		if err != nil {
			t.Fatal(err)
		}
		if result.Status != PollStatusSuccess || result.Account == nil {
			t.Fatalf("poll = %+v", result)
		}
		assertNoSecrets(t, result)
	})
}

func TestProtocolGrokDevicePollFixtures(t *testing.T) {
	fixtures := protocolFixtures(t)
	cases := []struct {
		name string
		want string
	}{
		{"grok_token_pending", PollStatusPending},
		{"grok_token_slow_down", PollStatusSlowDown},
		{"grok_token_expired", PollStatusExpired},
		{"grok_token_denied", PollStatusAccessDenied},
		{"grok_token_success", PollStatusSuccess},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				switch req.URL.String() {
				case grokDeviceCodeURL:
					return fixtures["grok_device_code"].response(), nil
				case grokTokenURL:
					return fixtures[tc.name].response(), nil
				default:
					t.Fatalf("unexpected url %s", req.URL)
					return nil, nil
				}
			})
			service := NewService(t.TempDir(), client)
			challenge, err := service.StartGrokDeviceAuth(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if challenge.UserCode != "ABCD-1234" || challenge.PollToken == "" {
				t.Fatalf("challenge = %+v", challenge)
			}
			assertNoSecrets(t, challenge)
			result, err := service.PollGrokDeviceAuth(context.Background(), GrokPollInput{PollToken: challenge.PollToken})
			if err != nil {
				t.Fatal(err)
			}
			if result.Status != tc.want {
				t.Fatalf("status = %s, want %s", result.Status, tc.want)
			}
			if tc.want == PollStatusSuccess {
				if result.Account == nil || result.Account.DisplayName != "a@x.ai" {
					t.Fatalf("account = %+v", result.Account)
				}
				if !strings.HasPrefix(result.Account.AccountID, "grok:") {
					t.Fatalf("account id = %q", result.Account.AccountID)
				}
			}
			assertNoSecrets(t, result)
		})
	}
}

func TestProtocolNestedAuthJSONAndUsage(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "codex_auth_nested.json"))
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := parseCodexAuthJSON(raw)
	if err != nil {
		t.Fatalf("parseCodexAuthJSON: %v", err)
	}
	if parsed.Tokens.RefreshToken != "test-refresh-codex" {
		t.Fatalf("refresh = %q", parsed.Tokens.RefreshToken)
	}
	if parsed.ChatGPTAccountID != "acct-fixture" {
		t.Fatalf("chatgpt account id = %q", parsed.ChatGPTAccountID)
	}
	if parsed.Email != "user@example.com" {
		t.Fatalf("email = %q", parsed.Email)
	}

	dir := t.TempDir()
	source := filepath.Join(dir, "auth.json")
	if err := os.WriteFile(source, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewService(filepath.Join(dir, "sidecar"), nil)
	status, err := service.ImportCodexAuthFile(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	if status.Email != "user@example.com" || !status.HasRefreshToken {
		t.Fatalf("status = %+v", status)
	}
	assertNoSecrets(t, status)
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(raw) {
		t.Fatal("source auth.json was modified")
	}

	fixtures := protocolFixtures(t)
	grok := parseGrokUsage(fixtures["grok_usage"].object(t))
	if grok.RemainingPercent != 66 || grok.UsedPercent != 34 || grok.PlanLabel != "SuperGrok" {
		t.Fatalf("grok usage = %+v", grok)
	}
	if grok.ResetAt.UnixMilli() != 1_780_876_800_000 {
		t.Fatalf("reset = %d", grok.ResetAt.UnixMilli())
	}
	assertNoSecrets(t, grok)

	codex := parseCodexUsage(fixtures["codex_usage"].object(t))
	if codex.PlanLabel != "ChatGPT Plus" || codex.RemainingPercent != 75 || codex.SessionRemainingPercent != 20 {
		t.Fatalf("codex usage = %+v", codex)
	}
	if codex.SessionResetAt.UnixMilli() != 1_780_000_000_000 {
		t.Fatalf("session reset = %d", codex.SessionResetAt.UnixMilli())
	}
	assertNoSecrets(t, codex)
}

func TestRefreshCodexUsagePersistsRedactedStatus(t *testing.T) {
	fixtures := protocolFixtures(t)
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != codexUsageURL {
			t.Fatalf("unexpected url %s", req.URL)
		}
		return fixtures["codex_usage"].response(), nil
	})
	service := NewService(t.TempDir(), client)
	token := testJWT(t, map[string]any{"email": "a@b.c", "exp": time.Now().Add(time.Hour).Unix()})
	if _, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+token+`","refresh_token":"refresh"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.RefreshUsage(context.Background(), ProviderCodex); err != nil {
		t.Fatal(err)
	}
	status := service.CodexStatus(context.Background())
	if status.PlanLabel != "ChatGPT Plus" || status.RemainingPercent != 75 || status.SessionRemainingPercent != 20 {
		t.Fatalf("status = %+v", status)
	}
	assertNoSecrets(t, status)
}

func TestHTTPStatusErrorOmitsTokens(t *testing.T) {
	token := testJWT(t, map[string]any{"sub": "leak"})
	err := httpStatusError("Codex token refresh", 500, []byte(`{"error_description":"`+token+`"}`))
	if err == nil {
		t.Fatal("expected error")
	}
	if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "eyJ") {
		t.Fatalf("token leaked into error: %v", err)
	}
}
