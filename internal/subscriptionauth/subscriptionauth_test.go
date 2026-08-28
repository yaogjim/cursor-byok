package subscriptionauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func testJWT(t *testing.T, claims map[string]any) string {
	t.Helper()
	payload, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) Do(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func jsonResponse(status int, body any) *http.Response {
	payload, _ := json.Marshal(body)
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewReader(payload)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
}

func TestParseCodexAuthJSONNestedTokens(t *testing.T) {
	access := testJWT(t, map[string]any{
		"email":                       "user@example.com",
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acct-9"},
		"exp":                         time.Now().Add(time.Hour).Unix(),
	})
	raw := map[string]any{
		"auth_mode":    "chatgpt",
		"last_refresh": "2026-08-26T00:00:00Z",
		"tokens": map[string]any{
			"access_token":  access,
			"refresh_token": "refresh-1",
			"id_token":      access,
		},
	}
	payload, _ := json.Marshal(raw)
	parsed, err := parseCodexAuthJSON(payload)
	if err != nil {
		t.Fatalf("parseCodexAuthJSON: %v", err)
	}
	if parsed.Tokens.AccessToken != access || parsed.Tokens.RefreshToken != "refresh-1" {
		t.Fatal("nested tokens were not parsed")
	}
	if parsed.ChatGPTAccountID != "acct-9" {
		t.Fatalf("chatgpt account id = %q", parsed.ChatGPTAccountID)
	}
}

func TestParseCodexAuthJSONRejectsUnsupportedBundles(t *testing.T) {
	cases := []string{
		`{"auth_mode":"api_key","tokens":{"access_token":"a","refresh_token":"b"}}`,
		`{"chatgptAuthTokens":{"access_token":"a"}}`,
		`{"OPENAI_API_KEY":"sk-test"}`,
		`{"tokens":{"access_token":"only-access"}}`,
		`{"tokens":{"refresh_token":"only-refresh"}}`,
	}
	for _, raw := range cases {
		if _, err := parseCodexAuthJSON([]byte(raw)); err == nil {
			t.Fatalf("expected rejection for %s", raw)
		}
	}
}

func TestImportCodexAuthDoesNotModifySourceFile(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "auth.json")
	access := testJWT(t, map[string]any{"email": "a@b.c", "exp": time.Now().Add(time.Hour).Unix()})
	original := []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"` + access + `","refresh_token":"r1"}}`)
	if err := os.WriteFile(source, original, 0o644); err != nil {
		t.Fatal(err)
	}
	service := NewService(filepath.Join(dir, "sidecar"), nil)
	status, err := service.ImportCodexAuthFile(context.Background(), source)
	if err != nil {
		t.Fatalf("ImportCodexAuthFile: %v", err)
	}
	if status.Email != "a@b.c" || status.HasRefreshToken != true {
		t.Fatalf("status = %+v", status)
	}
	after, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, after) {
		t.Fatal("source auth.json was modified")
	}
	if strings.Contains(status.Error, access) || strings.Contains(status.AccountID, access) {
		t.Fatal("token leaked into DTO")
	}
}

func TestStorePermissionsAndAtomicWriteKeepsOldFile(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits")
	}
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "subscription-auth"))
	auth := storedCodexAuth{
		AuthMode: codexAuthMode,
		Tokens:   storedTokenBundle{AccessToken: "old-token", RefreshToken: "old-refresh"},
	}
	if err := store.SaveCodex(auth); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(store.Dir())
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("dir perm = %o", info.Mode().Perm())
	}
	fileInfo, err := os.Stat(store.CodexPath())
	if err != nil {
		t.Fatal(err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("file perm = %o", fileInfo.Mode().Perm())
	}

	blocker := store.CodexPath() + ".tmp"
	if err := os.Mkdir(blocker, 0o700); err != nil {
		t.Fatal(err)
	}
	err = store.SaveCodex(storedCodexAuth{
		AuthMode: codexAuthMode,
		Tokens:   storedTokenBundle{AccessToken: "new-token", RefreshToken: "new-refresh"},
	})
	if err == nil {
		t.Fatal("expected write failure when temp path is a directory")
	}
	loaded, err := store.LoadCodex()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tokens.AccessToken != "old-token" {
		t.Fatalf("old credentials were overwritten: %+v", loaded)
	}
}

func TestCodexRefreshSingleFlightAndWriteback(t *testing.T) {
	dir := t.TempDir()
	var calls atomic.Int32
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		time.Sleep(50 * time.Millisecond)
		if req.URL.String() != codexOAuthTokenURL {
			t.Fatalf("unexpected url %s", req.URL)
		}
		_ = req.ParseForm()
		if req.PostForm.Get("grant_type") != "refresh_token" {
			t.Fatalf("grant_type = %q", req.PostForm.Get("grant_type"))
		}
		if req.PostForm.Get("client_id") != codexClientID {
			t.Fatalf("client_id = %q", req.PostForm.Get("client_id"))
		}
		next := testJWT(t, map[string]any{"email": "a@b.c", "exp": time.Now().Add(2 * time.Hour).Unix()})
		return jsonResponse(200, map[string]any{
			"access_token":  next,
			"refresh_token": "rotated-refresh",
			"id_token":      next,
		}), nil
	})
	service := NewService(dir, client)
	expired := testJWT(t, map[string]any{"email": "a@b.c", "exp": time.Now().Add(-time.Minute).Unix()})
	_, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+expired+`","refresh_token":"old-refresh"}}`))
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := service.Resolve(context.Background(), CredentialSourceCodex); err != nil {
				t.Errorf("Resolve: %v", err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls = %d, want 1", calls.Load())
	}
	loaded, err := service.store.LoadCodex()
	if err != nil || loaded == nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Tokens.RefreshToken != "rotated-refresh" {
		t.Fatalf("refresh token was not written back")
	}
	if loaded.Tokens.AccessToken == expired {
		t.Fatal("access token was not rotated")
	}
}

func TestCodexRefreshFailureIsAuthRequired(t *testing.T) {
	dir := t.TempDir()
	client := roundTripFunc(func(*http.Request) (*http.Response, error) {
		return jsonResponse(401, map[string]any{"error": "invalid_grant", "error_description": "revoked"}), nil
	})
	service := NewService(dir, client)
	expired := testJWT(t, map[string]any{"exp": time.Now().Add(-time.Minute).Unix()})
	if _, err := service.ImportCodexAuth(context.Background(), []byte(`{"tokens":{"access_token":"`+expired+`","refresh_token":"r"}}`)); err != nil {
		t.Fatal(err)
	}
	_, err := service.Resolve(context.Background(), CredentialSourceCodex)
	if err != ErrAuthRequired {
		t.Fatalf("err = %v, want ErrAuthRequired", err)
	}
}

func TestCodexDevicePollClassification(t *testing.T) {
	if kind := classifyCodexDevicePoll(http.StatusForbidden, map[string]any{
		"error": map[string]any{"message": "Device authorization is pending. Please try again."},
	}); kind != pollKindPending {
		t.Fatalf("forbidden pending = %v", kind)
	}
	if kind := classifyCodexDevicePoll(http.StatusNotFound, map[string]any{}); kind != pollKindPending {
		t.Fatalf("not found = %v", kind)
	}
	if kind := classifyCodexDevicePoll(http.StatusOK, map[string]any{
		"authorization_code": "auth-code",
		"code_verifier":      "pkce",
	}); kind != pollKindAuthCode {
		t.Fatalf("auth code = %v", kind)
	}
}

func TestGrokDeviceAuthPendingAndSuccess(t *testing.T) {
	dir := t.TempDir()
	var stage atomic.Int32
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch req.URL.String() {
		case grokDeviceCodeURL:
			return jsonResponse(200, map[string]any{
				"device_code":               "dev-1",
				"user_code":                 "ABCD-1234",
				"verification_uri":          "https://auth.x.ai/device",
				"verification_uri_complete": "https://auth.x.ai/device?user_code=ABCD-1234",
				"expires_in":                900,
				"interval":                  5,
			}), nil
		case grokTokenURL:
			if stage.Add(1) == 1 {
				return jsonResponse(400, map[string]any{"error": "authorization_pending"}), nil
			}
			access := testJWT(t, map[string]any{"sub": "user-1", "email": "a@x.ai"})
			return jsonResponse(200, map[string]any{
				"access_token":  access,
				"refresh_token": "g-refresh",
			}), nil
		default:
			t.Fatalf("unexpected url %s", req.URL)
			return nil, nil
		}
	})
	service := NewService(dir, client)
	challenge, err := service.StartGrokDeviceAuth(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if challenge.UserCode != "ABCD-1234" || challenge.DeviceCode != "" && challenge.PollToken == "" {
		t.Fatalf("challenge = %+v", challenge)
	}
	if challenge.PollToken == "" {
		t.Fatal("poll token missing")
	}
	pending, err := service.PollGrokDeviceAuth(context.Background(), GrokPollInput{PollToken: challenge.PollToken})
	if err != nil {
		t.Fatal(err)
	}
	if pending.Status != PollStatusPending {
		t.Fatalf("pending status = %s", pending.Status)
	}
	done, err := service.PollGrokDeviceAuth(context.Background(), GrokPollInput{PollToken: challenge.PollToken})
	if err != nil {
		t.Fatal(err)
	}
	if done.Status != PollStatusSuccess || done.Account == nil {
		t.Fatalf("success = %+v", done)
	}
	if done.Account.DisplayName != "a@x.ai" || !strings.HasPrefix(done.Account.AccountID, "grok:") {
		t.Fatalf("account = %+v", done.Account)
	}
	if strings.Contains(done.Account.AccountID, "eyJ") {
		t.Fatal("token leaked into account id")
	}
}

func TestGrokActivateDeleteAndStaticResolve(t *testing.T) {
	dir := t.TempDir()
	service := NewService(dir, nil)
	first := testJWT(t, map[string]any{"sub": "one", "email": "one@x.ai"})
	second := testJWT(t, map[string]any{"sub": "two", "email": "two@x.ai"})
	if _, err := service.upsertGrokAccount(first, "r1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.upsertGrokAccount(second, "r2", false); err != nil {
		t.Fatal(err)
	}
	accounts, err := service.ListAccounts(context.Background(), ProviderGrok)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("list = %v %v", accounts, err)
	}
	var inactive string
	for _, account := range accounts {
		if !account.Active {
			inactive = account.AccountID
		}
	}
	activated, err := service.ActivateAccount(context.Background(), inactive)
	if err != nil || !activated.Active {
		t.Fatalf("activate = %+v %v", activated, err)
	}
	if err := service.DeleteAccount(context.Background(), activated.AccountID); err != nil {
		t.Fatal(err)
	}
	left, err := service.ListAccounts(context.Background(), ProviderGrok)
	if err != nil || len(left) != 1 || !left[0].Active {
		t.Fatalf("after delete = %+v %v", left, err)
	}
	_, err = service.Resolve(context.Background(), CredentialSourceStatic)
	if err != ErrStaticCredential {
		t.Fatalf("static resolve = %v", err)
	}
}

func TestParseGrokAndCodexUsage(t *testing.T) {
	grok := parseGrokUsage(map[string]any{
		"config": map[string]any{
			"creditUsagePercent":      34.0,
			"subscriptionTierDisplay": "SuperGrok",
			"currentPeriod":           map[string]any{"end": "2026-06-08T00:00:00Z"},
		},
	})
	if grok.RemainingPercent != 66 || grok.UsedPercent != 34 || grok.PlanLabel != "SuperGrok" {
		t.Fatalf("grok usage = %+v", grok)
	}
	if grok.ResetAt.UnixMilli() != 1_780_876_800_000 {
		t.Fatalf("reset = %d", grok.ResetAt.UnixMilli())
	}
	codex := parseCodexUsage(map[string]any{
		"plan_type": "plus",
		"rate_limit": map[string]any{
			"limit_reached":    false,
			"primary_window":   map[string]any{"used_percent": 80.0, "reset_at": 1_780_000_000},
			"secondary_window": map[string]any{"used_percent": 25.0, "reset_at": 1_780_500_000},
		},
	})
	if codex.PlanLabel != "ChatGPT Plus" || codex.RemainingPercent != 75 {
		t.Fatalf("codex usage = %+v", codex)
	}
}

func TestRedactKeepsTokensOutOfErrors(t *testing.T) {
	token := testJWT(t, map[string]any{"sub": "x"})
	got := RedactText("Bearer " + token + ` {"access_token":"` + token + `"}`)
	if strings.Contains(got, token) || strings.Contains(got, "eyJ") {
		t.Fatalf("token leaked: %s", got)
	}
}

func TestManagedChannelIDSecretIsStable(t *testing.T) {
	if ChannelIDSecret(CredentialSourceCodex, "tok-a") != ChannelIDSecret(CredentialSourceCodex, "tok-b") {
		t.Fatal("managed channel id must not depend on rotating token")
	}
	if ChannelIDSecret(CredentialSourceStatic, "sk-a") == ChannelIDSecret(CredentialSourceStatic, "sk-b") {
		t.Fatal("static channel id should still depend on api key")
	}
}
