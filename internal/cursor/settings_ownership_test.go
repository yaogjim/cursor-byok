package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestUserProxySettingsStoreClearOwnedRequiresCurrentOwner(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "Cursor", "User", "settings.json")
	store := NewUserProxySettingsStore(settingsPath)
	if err := store.Apply("http://127.0.0.1:18080", "owner-new"); err != nil {
		t.Fatalf("Apply() error = %v", err)
	}

	beforeClearCalled := false
	cleared, err := store.ClearOwned("owner-old", func() error {
		beforeClearCalled = true
		return nil
	})
	if err != nil {
		t.Fatalf("ClearOwned(old) error = %v", err)
	}
	if cleared {
		t.Fatal("ClearOwned(old) cleared settings owned by a newer instance")
	}
	if beforeClearCalled {
		t.Fatal("ClearOwned(old) ran cleanup owned by a newer instance")
	}
	assertProxySetting(t, settingsPath, "http://127.0.0.1:18080")

	cleared, err = store.ClearOwned("owner-new", nil)
	if err != nil {
		t.Fatalf("ClearOwned(current) error = %v", err)
	}
	if !cleared {
		t.Fatal("ClearOwned(current) did not clear owned settings")
	}
	assertProxySetting(t, settingsPath, "")
}

func TestUserProxySettingsStoreClearOwnedWithoutClaimPreservesSettings(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "Cursor", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(settingsPath, []byte("{\n  \"http.proxy\": \"http://127.0.0.1:18080\"\n}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	cleared, err := NewUserProxySettingsStore(settingsPath).ClearOwned("never-applied", nil)
	if err != nil {
		t.Fatalf("ClearOwned() error = %v", err)
	}
	if cleared {
		t.Fatal("ClearOwned() cleared settings without an ownership claim")
	}
	assertProxySetting(t, settingsPath, "http://127.0.0.1:18080")
}

func assertProxySetting(t *testing.T, settingsPath string, want string) {
	t.Helper()
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatalf("ReadFile(%s) error = %v", settingsPath, err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatalf("Unmarshal settings error = %v", err)
	}
	got, _ := settings["http.proxy"].(string)
	if got != want {
		t.Fatalf("http.proxy = %q, want %q", got, want)
	}
}
