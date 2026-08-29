package subscriptionauth

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
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
	token := testJWT(t, map[string]any{"sub": "x"})
	status, err := NewService(t.TempDir(), nil).ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"api_key","tokens":{"access_token":"`+token+`","refresh_token":"b"}}`))
	if err == nil {
		t.Fatal("expected import rejection")
	}
	if strings.Contains(status.Error, token) || strings.Contains(status.Error, "eyJ") {
		t.Fatalf("token leaked into DTO error: %+v", status)
	}
	assertNoSecrets(t, status)
}

func TestSub2APIImportFiltersProviderAndImportsSelectedCodexAccounts(t *testing.T) {
	codexAccessA := testJWT(t, map[string]any{
		"email":                       "a@example.com",
		"exp":                         time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "chatgpt-a", "chatgpt_plan_type": "plus"},
	})
	codexAccessB := testJWT(t, map[string]any{
		"email":                       "b@example.com",
		"exp":                         time.Now().Add(time.Hour).Unix(),
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "chatgpt-b", "chatgpt_plan_type": "pro"},
	})
	grokAccess := testJWT(t, map[string]any{"email": "grok@example.com", "sub": "grok-user"})
	payload, err := json.Marshal(map[string]any{
		"accounts": []map[string]any{
			{"name": "Codex A", "platform": "openai", "type": "oauth", "credentials": map[string]any{"access_token": codexAccessA, "refresh_token": "refresh-a", "email": "a@example.com", "plan_type": "plus"}},
			{"name": "Codex B", "platform": "codex", "type": "oauth", "credentials": map[string]any{"access_token": codexAccessB, "refresh_token": "refresh-b", "email": "b@example.com", "plan_type": "pro"}},
			{"name": "Grok", "platform": "grok", "type": "oauth", "credentials": map[string]any{"access_token": grokAccess, "refresh_token": "grok-refresh"}},
			{"name": "Static OpenAI", "platform": "openai", "type": "api_key", "credentials": map[string]any{"access_token": "sk-secret"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sub2api.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(t.TempDir(), nil)
	preview, err := service.PreviewSub2APIFile(context.Background(), path, ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Accounts) != 2 || preview.SkippedCount != 2 {
		t.Fatalf("preview = %+v", preview)
	}
	selected := preview.Accounts[1].AccountID
	result, err := service.ImportSub2APIFile(context.Background(), Sub2APIImportRequest{
		Path:       path,
		Provider:   ProviderCodex,
		AccountIDs: []string{selected},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].AccountID != selected || result.Accounts[0].Email != "b@example.com" {
		t.Fatalf("result = %+v", result)
	}
	statuses, err := service.ListAccounts(context.Background(), ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	if len(statuses) != 1 || statuses[0].Email != "b@example.com" || statuses[0].PlanLabel != "ChatGPT Pro" {
		t.Fatalf("statuses = %+v", statuses)
	}
	original, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(original, payload) {
		t.Fatal("source sub2api file was modified")
	}
	assertNoSecrets(t, preview)
	assertNoSecrets(t, result)
}

func TestSub2APIImportFiltersProviderAndImportsGrokAccount(t *testing.T) {
	codexAccess := testJWT(t, map[string]any{"email": "codex@example.com", "sub": "codex-user"})
	grokAccess := testJWT(t, map[string]any{"email": "grok@example.com", "sub": "grok-user"})
	payload, err := json.Marshal(map[string]any{
		"accounts": []map[string]any{
			{"name": "Codex", "platform": "openai", "type": "oauth", "credentials": map[string]any{"access_token": codexAccess, "refresh_token": "codex-refresh"}},
			{"name": "Grok", "platform": "xai", "type": "oauth", "credentials": map[string]any{"access_token": grokAccess, "refresh_token": "grok-refresh", "email": "grok@example.com"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "sub2api.json")
	if err := os.WriteFile(path, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	service := NewService(t.TempDir(), nil)
	preview, err := service.PreviewSub2APIFile(context.Background(), path, ProviderGrok)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.Accounts) != 1 || preview.SkippedCount != 1 {
		t.Fatalf("preview = %+v", preview)
	}
	result, err := service.ImportSub2APIFile(context.Background(), Sub2APIImportRequest{
		Path:       path,
		Provider:   ProviderGrok,
		AccountIDs: []string{preview.Accounts[0].AccountID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Accounts) != 1 || result.Accounts[0].Provider != ProviderGrok {
		t.Fatalf("result = %+v", result)
	}
	codexAccounts, err := service.ListAccounts(context.Background(), ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	if len(codexAccounts) != 0 {
		t.Fatalf("unexpected Codex accounts: %+v", codexAccounts)
	}
	assertNoSecrets(t, preview)
	assertNoSecrets(t, result)
}

func TestSub2APIProvidedFileFiltersCodexAccounts(t *testing.T) {
	path := "/Users/yaogj/Downloads/sub2api-account-20260829020741.json"
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		t.Skip("provided sub2api fixture is not available")
	} else if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	parsed, skipped, err := parseSub2APIAccounts(content, ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	if len(parsed) != 2 || skipped != 0 {
		t.Fatalf("parsed=%d skipped=%d", len(parsed), skipped)
	}
	for _, account := range parsed {
		if account.preview.Provider != ProviderCodex || account.preview.Email == "" || account.codex.Tokens.RefreshToken == "" {
			t.Fatalf("invalid parsed account: %+v", account.preview)
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
	assertNoSecrets(t, status)
}

func TestStorePermissionsAndAtomicWriteKeepsOldFile(t *testing.T) {
	dir := t.TempDir()
	store := NewFileStore(filepath.Join(dir, "subscription-auth"))
	auth := storedCodexAuth{
		AuthMode: codexAuthMode,
		Tokens:   storedTokenBundle{AccessToken: "old-token", RefreshToken: "old-refresh"},
	}
	if err := store.SaveCodex(auth); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
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
	}
	if leftover := leftoverTempFiles(t, store.Dir()); len(leftover) != 0 {
		t.Fatalf("temp files left after success: %v", leftover)
	}

	oldRename := renameFile
	renameFile = func(string, string) error {
		return errors.New("rename blocked")
	}
	t.Cleanup(func() { renameFile = oldRename })

	err := store.SaveCodex(storedCodexAuth{
		AuthMode: codexAuthMode,
		Tokens:   storedTokenBundle{AccessToken: "new-token", RefreshToken: "new-refresh"},
	})
	if err == nil {
		t.Fatal("expected write failure when rename is blocked")
	}
	loaded, err := store.LoadCodex()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Tokens.AccessToken != "old-token" || loaded.Tokens.RefreshToken != "old-refresh" {
		t.Fatalf("old credentials were overwritten: %+v", loaded)
	}
	if leftover := leftoverTempFiles(t, store.Dir()); len(leftover) != 0 {
		t.Fatalf("temp files left after failure: %v", leftover)
	}
}

func leftoverTempFiles(t *testing.T, dir string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	return matches
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
		if req.PostForm.Get("refresh_token") != "old-refresh" {
			t.Fatalf("refresh_token = %q", req.PostForm.Get("refresh_token"))
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

func TestCodexRefreshIndependentAdapterKeepsOldFileOnError(t *testing.T) {
	fixtures := protocolFixtures(t)
	expired := testJWT(t, map[string]any{"email": "a@b.c", "exp": time.Now().Add(-time.Minute).Unix()})
	cases := []struct {
		name     string
		fixture  string
		wantAuth bool
	}{
		{name: "invalid_grant", fixture: "codex_refresh_invalid_grant", wantAuth: true},
		{name: "empty_token", fixture: "codex_refresh_empty_token", wantAuth: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
				if req.URL.String() != codexOAuthTokenURL {
					t.Fatalf("unexpected url %s", req.URL)
				}
				if err := req.ParseForm(); err != nil {
					t.Fatal(err)
				}
				if req.PostForm.Get("grant_type") != "refresh_token" {
					t.Fatalf("grant_type = %q", req.PostForm.Get("grant_type"))
				}
				if req.PostForm.Get("client_id") != codexClientID {
					t.Fatalf("client_id = %q", req.PostForm.Get("client_id"))
				}
				if req.PostForm.Get("refresh_token") != "old-refresh" {
					t.Fatalf("refresh_token = %q", req.PostForm.Get("refresh_token"))
				}
				return fixtures[tc.fixture].response(), nil
			})
			service := NewService(t.TempDir(), client)
			if _, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+expired+`","refresh_token":"old-refresh"}}`)); err != nil {
				t.Fatal(err)
			}
			_, err := service.Resolve(context.Background(), CredentialSourceCodex)
			if tc.wantAuth {
				if err != ErrAuthRequired {
					t.Fatalf("err = %v, want ErrAuthRequired", err)
				}
			} else if err == nil {
				t.Fatal("expected refresh error")
			}
			if err != nil {
				if strings.Contains(err.Error(), expired) || strings.Contains(err.Error(), "eyJ") {
					t.Fatalf("token leaked into error: %v", err)
				}
			}
			loaded, loadErr := service.store.LoadCodex()
			if loadErr != nil || loaded == nil {
				t.Fatalf("load: %v", loadErr)
			}
			if loaded.Tokens.AccessToken != expired {
				t.Fatalf("old access token was changed")
			}
			if loaded.Tokens.RefreshToken != "old-refresh" {
				t.Fatalf("old refresh token was changed: %q", loaded.Tokens.RefreshToken)
			}
			if loaded.Tokens.AccessToken == "" || loaded.Tokens.RefreshToken == "" {
				t.Fatal("empty token written")
			}
			raw, readErr := os.ReadFile(service.store.CodexPath())
			if readErr != nil {
				t.Fatal(readErr)
			}
			if strings.Contains(string(raw), `"access_token": ""`) || strings.Contains(string(raw), `"refresh_token": ""`) {
				t.Fatalf("empty token serialized: %s", raw)
			}
		})
	}
}

func TestCodexRefreshIndependentAdapterRotatesRefreshToken(t *testing.T) {
	fixtures := protocolFixtures(t)
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != codexOAuthTokenURL {
			t.Fatalf("unexpected url %s", req.URL)
		}
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if req.PostForm.Get("grant_type") != "refresh_token" {
			t.Fatalf("grant_type = %q", req.PostForm.Get("grant_type"))
		}
		if req.PostForm.Get("client_id") != codexClientID {
			t.Fatalf("client_id = %q", req.PostForm.Get("client_id"))
		}
		if req.PostForm.Get("refresh_token") != "old-refresh" {
			t.Fatalf("refresh_token = %q", req.PostForm.Get("refresh_token"))
		}
		return fixtures["codex_refresh_success"].response(), nil
	})
	service := NewService(t.TempDir(), client)
	expired := testJWT(t, map[string]any{"email": "a@b.c", "exp": time.Now().Add(-time.Minute).Unix()})
	if _, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+expired+`","refresh_token":"old-refresh"}}`)); err != nil {
		t.Fatal(err)
	}
	cred, err := service.Resolve(context.Background(), CredentialSourceCodex)
	if err != nil {
		t.Fatal(err)
	}
	if cred.AccessToken == expired || cred.AccessToken == "" {
		t.Fatal("access token was not rotated")
	}
	loaded, err := service.store.LoadCodex()
	if err != nil || loaded == nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.Tokens.RefreshToken != "rotated-refresh" {
		t.Fatalf("refresh token was not rotated: %q", loaded.Tokens.RefreshToken)
	}
	status := service.CodexStatus(context.Background())
	assertNoSecrets(t, status)
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
	assertNoSecrets(t, activated)
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

func TestIsQuotaErrorRequiresExplicitQuotaText(t *testing.T) {
	if IsQuotaError(errors.New("openai adapter status=429")) {
		t.Fatal("bare 429 is not quota")
	}
	if IsQuotaError(errors.New("openai adapter status=401")) {
		t.Fatal("bare 401 is not quota")
	}
	if IsQuotaError(errors.New("openai adapter status=403")) {
		t.Fatal("bare 403 is not quota")
	}
	if IsQuotaError(errors.New("openai adapter status=429 body=rate_limit_reached")) {
		t.Fatal("rate_limit_reached alone is not quota")
	}
	if !IsQuotaError(errors.New("insufficient_quota")) {
		t.Fatal("insufficient_quota should match")
	}
	if !IsQuotaError(errors.New("usage_limit_reached: 5-hour limit")) {
		t.Fatal("usage_limit_reached should match")
	}
	if !IsQuotaError(ErrQuotaExhausted) {
		t.Fatal("typed ErrQuotaExhausted should match")
	}
}

func TestResolveAfterUnauthorizedForcesCodexRefresh(t *testing.T) {
	var calls atomic.Int32
	next := testJWT(t, map[string]any{"email": "a@b.c", "exp": time.Now().Add(2 * time.Hour).Unix()})
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		calls.Add(1)
		if req.URL.String() != codexOAuthTokenURL {
			t.Fatalf("unexpected url %s", req.URL)
		}
		return jsonResponse(200, map[string]any{
			"access_token":  next,
			"refresh_token": "rotated-refresh",
		}), nil
	})
	service := NewService(t.TempDir(), client)
	fresh := testJWT(t, map[string]any{"email": "a@b.c", "exp": time.Now().Add(2 * time.Hour).Unix()})
	if _, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+fresh+`","refresh_token":"old-refresh"}}`)); err != nil {
		t.Fatal(err)
	}
	cred, err := service.Resolve(context.Background(), CredentialSourceCodex)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 0 {
		t.Fatalf("Resolve refreshed unexpired token, calls=%d", calls.Load())
	}
	if cred.AccessToken != fresh {
		t.Fatal("Resolve should keep unexpired access token")
	}
	forced, err := service.ResolveAfterUnauthorized(context.Background(), CredentialSourceCodex, cred.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("ResolveAfterUnauthorized calls=%d, want 1", calls.Load())
	}
	if forced.AccessToken != next {
		t.Fatal("ResolveAfterUnauthorized should rotate access token")
	}
}

func TestMarkQuotaExhaustedActivatesNextGrokAccount(t *testing.T) {
	service := NewService(t.TempDir(), nil)
	first := testJWT(t, map[string]any{"sub": "one", "email": "one@x.ai"})
	second := testJWT(t, map[string]any{"sub": "two", "email": "two@x.ai"})
	if _, err := service.upsertGrokAccount(first, "r1", true); err != nil {
		t.Fatal(err)
	}
	if _, err := service.upsertGrokAccount(second, "r2", false); err != nil {
		t.Fatal(err)
	}
	active, err := service.Resolve(context.Background(), CredentialSourceGrok)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MarkQuotaExhausted(context.Background(), active.AccountID); err != nil {
		t.Fatalf("mark with next account: %v", err)
	}
	rotated, err := service.Resolve(context.Background(), CredentialSourceGrok)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.AccountID == "" || rotated.AccountID == active.AccountID {
		t.Fatalf("expected next account, got %+v after %+v", rotated, active)
	}
	if rotated.AccessToken != second {
		t.Fatalf("rotated token mismatch")
	}
	if err := service.MarkQuotaExhausted(context.Background(), rotated.AccountID); !errors.Is(err, ErrQuotaExhausted) {
		t.Fatalf("no next account err=%v, want ErrQuotaExhausted", err)
	}
	if _, err := service.Resolve(context.Background(), CredentialSourceGrok); !errors.Is(err, ErrQuotaExhausted) {
		t.Fatalf("resolve exhausted account err=%v, want ErrQuotaExhausted", err)
	}
}

func TestCodexMultiAccountMigrationManagementAndQuotaRotation(t *testing.T) {
	service := NewService(t.TempDir(), nil)
	first := testJWT(t, map[string]any{"sub": "one", "email": "one@example.com", "exp": time.Now().Add(time.Hour).Unix()})
	second := testJWT(t, map[string]any{"sub": "two", "email": "two@example.com", "exp": time.Now().Add(time.Hour).Unix()})
	legacy := storedCodexAuth{AuthMode: codexAuthMode, Tokens: storedTokenBundle{AccessToken: first, RefreshToken: "first-refresh"}}
	if err := service.store.writeJSON(service.store.CodexPath(), legacy); err != nil {
		t.Fatal(err)
	}
	secondStatus, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+second+`","refresh_token":"second-refresh"}}`))
	if err != nil {
		t.Fatal(err)
	}
	accounts, err := service.ListAccounts(context.Background(), ProviderCodex)
	if err != nil || len(accounts) != 2 {
		t.Fatalf("accounts = %#v, %v", accounts, err)
	}
	if !accounts[0].Active || secondStatus.Active {
		t.Fatalf("first account must remain active after import: %#v", accounts)
	}
	file, err := service.store.LoadCodexFile()
	if err != nil || file.SchemaVersion != codexSchemaVersion || len(file.Accounts) != 2 {
		t.Fatalf("migrated file = %#v, %v", file, err)
	}
	if _, err := service.ActivateAccount(context.Background(), secondStatus.AccountID); err != nil {
		t.Fatal(err)
	}
	if err := service.DeleteAccount(context.Background(), secondStatus.AccountID); err != nil {
		t.Fatal(err)
	}
	remaining, err := service.ListAccounts(context.Background(), ProviderCodex)
	if err != nil || len(remaining) != 1 || !remaining[0].Active {
		t.Fatalf("delete active = %#v, %v", remaining, err)
	}
	if _, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+second+`","refresh_token":"second-refresh"}}`)); err != nil {
		t.Fatal(err)
	}
	if err := service.MarkQuotaExhausted(context.Background(), remaining[0].AccountID); err != nil {
		t.Fatal(err)
	}
	cred, err := service.Resolve(context.Background(), CredentialSourceCodex)
	if err != nil || cred.AccountID == remaining[0].AccountID {
		t.Fatalf("quota rotation = %#v, %v", cred, err)
	}
	if err := service.MarkQuotaExhausted(context.Background(), cred.AccountID); !errors.Is(err, ErrQuotaExhausted) {
		t.Fatalf("all accounts exhausted = %v", err)
	}
	file, err = service.store.LoadCodexFile()
	if err != nil {
		t.Fatal(err)
	}
	for i := range file.Accounts {
		if codexAccountID(file.Accounts[i]) == remaining[0].AccountID {
			file.Accounts[i].ResetAtMS = time.Now().Add(-time.Minute).UnixMilli()
		}
	}
	if err := service.store.SaveCodexFile(file); err != nil {
		t.Fatal(err)
	}
	recovered, err := service.Resolve(context.Background(), CredentialSourceCodex)
	if err != nil || recovered.AccountID != remaining[0].AccountID {
		t.Fatalf("expired reset should recover account: %#v, %v", recovered, err)
	}
}

func TestCodexAccountUsageAndRepeatedImportPreserveAccountState(t *testing.T) {
	resetAt := time.Now().Add(time.Hour).Unix()
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.String() != codexUsageURL {
			t.Fatalf("unexpected url %s", req.URL)
		}
		return jsonResponse(http.StatusOK, map[string]any{
			"plan_type": "plus",
			"rate_limit": map[string]any{
				"secondary_window": map[string]any{"used_percent": 25, "reset_at": resetAt},
				"primary_window":   map[string]any{"used_percent": 10, "reset_at": resetAt},
			},
		}), nil
	})
	service := NewService(t.TempDir(), client)
	first := testJWT(t, map[string]any{"sub": "one", "exp": time.Now().Add(time.Hour).Unix()})
	second := testJWT(t, map[string]any{"sub": "two", "exp": time.Now().Add(time.Hour).Unix()})
	if _, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+first+`","refresh_token":"first-refresh"}}`)); err != nil {
		t.Fatal(err)
	}
	secondStatus, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+second+`","refresh_token":"second-refresh"}}`))
	if err != nil {
		t.Fatal(err)
	}
	usage, err := service.RefreshAccountUsage(context.Background(), ProviderCodex, secondStatus.AccountID)
	if err != nil {
		t.Fatal(err)
	}
	if usage.AccountID != secondStatus.AccountID || usage.PlanLabel != "ChatGPT Plus" || usage.RemainingPercent != 75 || usage.SessionRemainingPercent != 90 {
		t.Fatalf("usage = %#v", usage)
	}
	rotatedSecond := testJWT(t, map[string]any{"sub": "two", "exp": time.Now().Add(2 * time.Hour).Unix()})
	updated, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+rotatedSecond+`","refresh_token":"second-refresh-new"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if updated.Active || updated.PlanLabel != "ChatGPT Plus" || updated.RemainingPercent != 75 || updated.SessionRemainingPercent != 90 {
		t.Fatalf("updated account state = %#v", updated)
	}
	accounts, err := service.ListAccounts(context.Background(), ProviderCodex)
	if err != nil || len(accounts) != 2 || !accounts[0].Active || accounts[1].Active {
		t.Fatalf("accounts = %#v, %v", accounts, err)
	}
}

func TestCodexRepeatedQuotaSignalDoesNotAdvancePastCurrentSuccessor(t *testing.T) {
	service := NewService(t.TempDir(), nil)
	for _, subject := range []string{"one", "two", "three"} {
		token := testJWT(t, map[string]any{"sub": subject, "exp": time.Now().Add(time.Hour).Unix()})
		if _, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+token+`","refresh_token":"refresh-`+subject+`"}}`)); err != nil {
			t.Fatal(err)
		}
	}
	first, err := service.Resolve(context.Background(), CredentialSourceCodex)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MarkQuotaExhausted(context.Background(), first.AccountID); err != nil {
		t.Fatal(err)
	}
	second, err := service.Resolve(context.Background(), CredentialSourceCodex)
	if err != nil {
		t.Fatal(err)
	}
	if err := service.MarkQuotaExhausted(context.Background(), first.AccountID); err != nil {
		t.Fatal(err)
	}
	stillSecond, err := service.Resolve(context.Background(), CredentialSourceCodex)
	if err != nil {
		t.Fatal(err)
	}
	if second.AccountID == first.AccountID || stillSecond.AccountID != second.AccountID {
		t.Fatalf("rotation advanced unexpectedly: first=%s second=%s after-repeat=%s", first.AccountID, second.AccountID, stillSecond.AccountID)
	}
}

func TestCodexUnauthorizedRefreshMarksOnlyFailedAccountAndRotates(t *testing.T) {
	var refreshes []string
	client := roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if err := req.ParseForm(); err != nil {
			t.Fatal(err)
		}
		refreshes = append(refreshes, req.PostForm.Get("refresh_token"))
		return jsonResponse(http.StatusUnauthorized, map[string]any{"error": "invalid_grant"}), nil
	})
	service := NewService(t.TempDir(), client)
	first := testJWT(t, map[string]any{"sub": "one", "exp": time.Now().Add(time.Hour).Unix()})
	second := testJWT(t, map[string]any{"sub": "two", "exp": time.Now().Add(time.Hour).Unix()})
	if _, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+first+`","refresh_token":"first-refresh"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := service.ImportCodexAuth(context.Background(), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"`+second+`","refresh_token":"second-refresh"}}`)); err != nil {
		t.Fatal(err)
	}
	failed, err := service.Resolve(context.Background(), CredentialSourceCodex)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := service.ResolveAfterUnauthorized(context.Background(), CredentialSourceCodex, failed.AccountID)
	if err != nil || rotated.AccountID == failed.AccountID {
		t.Fatalf("unauthorized rotation = %#v, %v", rotated, err)
	}
	if len(refreshes) != 1 || refreshes[0] != "first-refresh" {
		t.Fatalf("targeted refreshes = %#v", refreshes)
	}
	accounts, err := service.ListAccounts(context.Background(), ProviderCodex)
	if err != nil {
		t.Fatal(err)
	}
	for _, account := range accounts {
		if account.AccountID == failed.AccountID && account.State != StateAuthRequired {
			t.Fatalf("failed account state = %#v", account)
		}
	}
}

func TestExpiredPendingAuthIsRemoved(t *testing.T) {
	service := NewService(t.TempDir(), nil)
	service.rememberPending("poll-token", pendingAuth{
		provider:   ProviderCodex,
		deviceCode: "device-code",
		expiresAt:  time.Now().Add(-time.Second),
	})
	if _, ok := service.pendingByInput("poll-token", ""); ok {
		t.Fatal("expired pending auth should not resolve")
	}
	if len(service.pending) != 0 {
		t.Fatalf("expired pending auth was not removed: %#v", service.pending)
	}
}
