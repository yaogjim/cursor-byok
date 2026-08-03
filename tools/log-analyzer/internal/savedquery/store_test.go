package savedquery

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStorePersistsUpdatesListsAndDeletesQueries(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "saved-queries.json")
	store, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	clock := time.Date(2026, 3, 14, 10, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return clock }
	created, err := store.Put(Query{Name: "工具错误", DSL: `capability:tool severity:error`})
	if err != nil {
		t.Fatalf("Put(create) error = %v", err)
	}
	if created.ID == "" || created.CreatedAt != clock || created.UpdatedAt != clock {
		t.Fatalf("unexpected created query: %+v", created)
	}
	clock = clock.Add(time.Minute)
	created.Name = "工具异常"
	updated, err := store.Put(created)
	if err != nil {
		t.Fatalf("Put(update) error = %v", err)
	}
	if updated.CreatedAt != created.CreatedAt || updated.UpdatedAt != clock {
		t.Fatalf("unexpected updated query: %+v", updated)
	}

	reopened, err := Open(path)
	if err != nil {
		t.Fatalf("Open(reopen) error = %v", err)
	}
	items := reopened.List()
	if len(items) != 1 || items[0].Name != "工具异常" || items[0].DSL != created.DSL {
		t.Fatalf("reopened queries = %+v", items)
	}
	if err := reopened.Delete(created.ID); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if len(reopened.List()) != 0 {
		t.Fatalf("queries remain after delete: %+v", reopened.List())
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("saved query mode = %04o, want 0600", info.Mode().Perm())
		}
	}
}

func TestStoreRejectsInvalidDSLWithoutChangingState(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "saved-queries.json"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if _, err := store.Put(Query{Name: "unsafe", DSL: `unknown_field:value`}); err == nil {
		t.Fatal("Put() accepted unsupported query field")
	}
	if len(store.List()) != 0 {
		t.Fatalf("invalid query changed state: %+v", store.List())
	}
}
