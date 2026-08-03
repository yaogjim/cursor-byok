package cursoraccount

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCredentialPersistenceIsPrivateAndDisconnectRemovesIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cursor-account.json")
	stored := credentials{
		AccessToken:  "access-token-secret",
		RefreshToken: "refresh-token-secret",
		AuthID:       "auth-user",
		Email:        "user@example.com",
	}
	manager := NewManager(path, nil)
	if err := manager.save(stored); err != nil {
		t.Fatalf("save credentials: %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatalf("widen credential permissions for migration test: %v", err)
	}

	loaded := NewManager(path, nil)
	if !loaded.SignedIn() {
		t.Fatal("persisted credentials were not loaded")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat credential file: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("credential permissions = %o, want 600", info.Mode().Perm())
	}

	statusJSON, err := json.Marshal(loaded.Status())
	if err != nil {
		t.Fatalf("marshal account status: %v", err)
	}
	for _, secret := range []string{stored.AccessToken, stored.RefreshToken} {
		if strings.Contains(string(statusJSON), secret) {
			t.Fatalf("account status exposed credential %q", secret)
		}
	}

	status, err := loaded.Disconnect()
	if err != nil {
		t.Fatalf("disconnect account: %v", err)
	}
	if status.State != StateSignedOut {
		t.Fatalf("disconnect state = %q, want %q", status.State, StateSignedOut)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("credential file still exists after disconnect: %v", err)
	}
}
