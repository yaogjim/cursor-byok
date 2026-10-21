package cursor

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestPlanSnapshotsOnlyChangingKeysAndDistinguishesUnset(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "Cursor", "User", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settingsPath), 0o755); err != nil {
		t.Fatal(err)
	}
	original := `{
  "editor.fontSize": 14,
  "http.proxy": "http://127.0.0.1:19991"
}
`
	if err := os.WriteFile(settingsPath, []byte(original), 0o644); err != nil {
		t.Fatal(err)
	}
	store := NewUserProxySettingsStore(settingsPath)
	snap, err := store.Plan("http://127.0.0.1:19992")
	if err != nil {
		t.Fatal(err)
	}
	if !snap.NeedsChange() {
		t.Fatal("expected needs change")
	}
	proxyOld, ok := snap.Keys["http.proxy"]
	if !ok || !proxyOld.Present || proxyOld.Value != "http://127.0.0.1:19991" {
		t.Fatalf("http.proxy snapshot = %+v", proxyOld)
	}
	if _, ok := snap.Keys["editor.fontSize"]; ok {
		t.Fatal("must not snapshot unmodified unrelated keys")
	}
	disableHTTP2, ok := snap.Keys["cursor.general.disableHttp2"]
	if !ok || disableHTTP2.Present {
		t.Fatalf("unset key must be distinguished: %+v", disableHTTP2)
	}
}

func TestPlanUnchangedWhenDesiredKeysAlreadyMatch(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	store := NewUserProxySettingsStore(settingsPath)
	if err := store.Apply("http://127.0.0.1:19993", "owner-a"); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Plan("http://127.0.0.1:19993")
	if err != nil {
		t.Fatal(err)
	}
	if snap.NeedsChange() {
		t.Fatalf("unchanged keys = %+v", snap.Keys)
	}
	if !snap.Owner.Present || snap.Owner.Value != "owner-a" {
		t.Fatalf("owner snapshot = %+v", snap.Owner)
	}
}

func TestRestoreRejectsAnotherInstancesSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store := NewUserProxySettingsStore(path)
	snap, err := store.Plan("http://127.0.0.1:19994")
	if err != nil {
		t.Fatal(err)
	}
	// Another instance writes after our plan, before our failed apply rolls back.
	if err := store.Apply("http://127.0.0.1:19995", "owner-other"); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(snap, "owner-new"); err == nil {
		t.Fatal("stale rollback must report an ownership conflict")
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != string(before) {
		t.Fatal("stale rollback changed another instance's settings")
	}
	owner, err := os.ReadFile(store.ownerPath())
	if err != nil || string(owner) != "owner-other\n" {
		t.Fatalf("owner changed: %q, err=%v", owner, err)
	}
}

func TestApplyPlannedRejectsInterveningSettingsChanges(t *testing.T) {
	for _, changedOwner := range []string{"owner-old", "owner-other"} {
		t.Run(changedOwner, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			store := NewUserProxySettingsStore(path)
			if err := store.Apply("http://127.0.0.1:19991", "owner-old"); err != nil {
				t.Fatal(err)
			}
			snap, err := store.Plan("http://127.0.0.1:19994")
			if err != nil {
				t.Fatal(err)
			}
			if err := store.Apply("http://127.0.0.1:19995", changedOwner); err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if err := store.ApplyPlanned("http://127.0.0.1:19994", "owner-new", snap); err == nil {
				t.Fatal("stale plan must not overwrite intervening settings")
			}
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("stale plan changed settings")
			}
		})
	}
}

func TestApplyPlannedAndRestorePreserveUnrelatedEdits(t *testing.T) {
	for _, failOwnerWrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "owner-write-failure"}[failOwnerWrite], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "settings.json")
			store := NewUserProxySettingsStore(path)
			if err := store.Apply("http://127.0.0.1:19991", "owner-old"); err != nil {
				t.Fatal(err)
			}
			snap, err := store.Plan("http://127.0.0.1:19994")
			if err != nil {
				t.Fatal(err)
			}
			settings, err := store.readSettingsMapUnlocked()
			if err != nil {
				t.Fatal(err)
			}
			settings["editor.fontSize"] = 17
			if err := writeSettingsMapAt(path, settings); err != nil {
				t.Fatal(err)
			}
			if failOwnerWrite {
				if err := os.Mkdir(store.ownerPath()+".tmp", 0o700); err != nil {
					t.Fatal(err)
				}
			}
			err = store.ApplyPlanned("http://127.0.0.1:19994", "owner-new", snap)
			if (err != nil) != failOwnerWrite {
				t.Fatalf("ApplyPlanned error=%v, want failure=%t", err, failOwnerWrite)
			}
			if failOwnerWrite {
				if err := os.Remove(store.ownerPath() + ".tmp"); err != nil {
					t.Fatal(err)
				}
			}
			for range 2 { // Repeated compensation must be a no-op.
				if err := store.Restore(snap, "owner-new"); err != nil {
					t.Fatal(err)
				}
			}
			settings, err = store.readSettingsMapUnlocked()
			if err != nil {
				t.Fatal(err)
			}
			if settings["http.proxy"] != "http://127.0.0.1:19991" || settings["editor.fontSize"] != float64(17) {
				t.Fatalf("unexpected restored settings: %#v", settings)
			}
			owner, err := store.readOwnerUnlocked()
			if err != nil || owner.Value != "owner-old" {
				t.Fatalf("unexpected restored owner: %+v, err=%v", owner, err)
			}
		})
	}
}

func TestRestoreRejectsChangesBySnapshotOwner(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	store := NewUserProxySettingsStore(path)
	if err := store.Apply("http://127.0.0.1:19991", "owner-old"); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Plan("http://127.0.0.1:19994")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Apply("http://127.0.0.1:19995", "owner-old"); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := store.Restore(snap, "owner-new"); err == nil {
		t.Fatal("rollback must detect changes even when the previous owner is unchanged")
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("rollback overwrote newer settings")
	}
}

func TestRestorePutsBackOldValuesAndLeavesUnrelatedKeys(t *testing.T) {
	settingsPath := filepath.Join(t.TempDir(), "settings.json")
	store := NewUserProxySettingsStore(settingsPath)
	if err := os.WriteFile(settingsPath, []byte(`{
  "editor.fontSize": 14,
  "http.proxy": "http://127.0.0.1:19991"
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	snap, err := store.Plan("http://127.0.0.1:19994")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Apply("http://127.0.0.1:19994", "owner-new"); err != nil {
		t.Fatal(err)
	}
	if err := store.Restore(snap, "owner-new"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(settingsPath)
	if err != nil {
		t.Fatal(err)
	}
	var settings map[string]any
	if err := json.Unmarshal(data, &settings); err != nil {
		t.Fatal(err)
	}
	if settings["http.proxy"] != "http://127.0.0.1:19991" {
		t.Fatalf("http.proxy = %#v", settings["http.proxy"])
	}
	if _, ok := settings["cursor.general.disableHttp2"]; ok {
		t.Fatal("unset key must be removed on restore")
	}
	if settings["editor.fontSize"] != float64(14) {
		t.Fatalf("unrelated key changed: %#v", settings["editor.fontSize"])
	}
	if _, err := os.Stat(settingsPath + ".cursor-byok-owner"); !os.IsNotExist(err) {
		t.Fatalf("owner file should be removed, err=%v", err)
	}
}
