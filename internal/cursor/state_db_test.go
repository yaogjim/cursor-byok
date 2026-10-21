package cursor

import (
	"context"
	"database/sql"
	"encoding/json"
	"path/filepath"
	"testing"
)

func TestSyncCursorAuthStateDBAccessTokenInjection(t *testing.T) {
	const (
		existingToken        = "synthetic-existing-access-token"
		existingRefresh      = "synthetic-existing-refresh-token"
		existingEmail        = "existing@example.com"
		existingSignUpType   = "GitHub"
		existingMembership   = "pro"
		existingSubscription = "canceled"
		injectToken          = "synthetic-inject-access-token"
		secondInject         = "synthetic-second-inject-access-token"
		email                = "local@example.com"
		secondEmail          = "second-local@example.com"
	)

	existingAuth := map[string]string{
		cursorAuthAccessTokenKey:              existingToken,
		"cursorAuth/refreshToken":             existingRefresh,
		"cursorAuth/cachedEmail":              existingEmail,
		"cursorAuth/cachedSignUpType":         existingSignUpType,
		"cursorAuth/stripeMembershipType":     existingMembership,
		"cursorAuth/stripeSubscriptionStatus": existingSubscription,
	}

	t.Run("preserves_nonempty_existing", func(t *testing.T) {
		path := newCursorStateDBForTest(t)
		writeCursorAuthStateForTest(t, path, existingAuth)

		if err := syncCursorAuthStateDB(path, buildCursorAuthStateValues(email, injectToken)); err != nil {
			t.Fatalf("sync state db: %v", err)
		}

		assertCursorAuthStateForTest(t, path, existingAuth)
	})

	t.Run("injects_when_missing", func(t *testing.T) {
		path := newCursorStateDBForTest(t)

		if err := syncCursorAuthStateDB(path, buildCursorAuthStateValues(email, injectToken)); err != nil {
			t.Fatalf("sync state db: %v", err)
		}

		assertCursorAuthStateForTest(t, path, buildCursorAuthStateValues(email, injectToken))
	})

	t.Run("injects_when_empty", func(t *testing.T) {
		path := newCursorStateDBForTest(t)
		writeCursorStateItemForTest(t, path, cursorAuthAccessTokenKey, "")

		if err := syncCursorAuthStateDB(path, buildCursorAuthStateValues(email, injectToken)); err != nil {
			t.Fatalf("sync state db: %v", err)
		}

		assertCursorAuthStateForTest(t, path, buildCursorAuthStateValues(email, injectToken))
	})

	t.Run("injects_when_whitespace", func(t *testing.T) {
		path := newCursorStateDBForTest(t)
		writeCursorStateItemForTest(t, path, cursorAuthAccessTokenKey, "   ")

		if err := syncCursorAuthStateDB(path, buildCursorAuthStateValues(email, injectToken)); err != nil {
			t.Fatalf("sync state db: %v", err)
		}

		assertCursorAuthStateForTest(t, path, buildCursorAuthStateValues(email, injectToken))
	})

	t.Run("repeated_sync_keeps_existing", func(t *testing.T) {
		path := newCursorStateDBForTest(t)
		writeCursorAuthStateForTest(t, path, existingAuth)

		if err := syncCursorAuthStateDB(path, buildCursorAuthStateValues(email, injectToken)); err != nil {
			t.Fatalf("first sync: %v", err)
		}
		if err := syncCursorAuthStateDB(path, buildCursorAuthStateValues(secondEmail, secondInject)); err != nil {
			t.Fatalf("second sync: %v", err)
		}

		assertCursorAuthStateForTest(t, path, existingAuth)
	})

	t.Run("repeated_sync_keeps_injected", func(t *testing.T) {
		path := newCursorStateDBForTest(t)
		firstAuth := buildCursorAuthStateValues(email, injectToken)

		if err := syncCursorAuthStateDB(path, firstAuth); err != nil {
			t.Fatalf("first sync: %v", err)
		}
		if err := syncCursorAuthStateDB(path, buildCursorAuthStateValues(secondEmail, secondInject)); err != nil {
			t.Fatalf("second sync: %v", err)
		}

		assertCursorAuthStateForTest(t, path, firstAuth)
	})

	t.Run("preserves_auth_still_overrides_statsig", func(t *testing.T) {
		path := newCursorStateDBForTest(t)
		writeCursorAuthStateForTest(t, path, existingAuth)
		writeCursorStatsigBootstrapForTest(t, path, map[string]any{
			"feature_gates": map[string]any{
				"disable_terminal_output_ui_streaming": map[string]any{
					"value":     true,
					"rule_id":   "local_enabled",
					"groupName": "local_enabled",
				},
			},
			"hash_used": "none",
		})

		if err := syncCursorAuthStateDB(path, buildCursorAuthStateValues(email, injectToken)); err != nil {
			t.Fatalf("sync state db: %v", err)
		}

		assertCursorAuthStateForTest(t, path, existingAuth)
		assertCursorStatsigGateValueForTest(t, readCursorStatsigBootstrapForTest(t, path), "disable_terminal_output_ui_streaming", false)
	})
}

func TestSyncCursorAuthStateDBDisablesCachedTerminalOutputUIStreamingIdempotently(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temporary state db: %v", err)
	}
	if _, err := db.Exec("CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)"); err != nil {
		db.Close()
		t.Fatalf("create ItemTable: %v", err)
	}
	bootstrap := map[string]any{
		"feature_gates": map[string]any{
			"disable_terminal_output_ui_streaming": map[string]any{
				"value":     true,
				"rule_id":   "local_enabled",
				"groupName": "local_enabled",
			},
			"unrelated_gate": map[string]any{"value": true},
		},
		"hash_used": "none",
	}
	raw, err := json.Marshal(bootstrap)
	if err != nil {
		db.Close()
		t.Fatalf("encode bootstrap: %v", err)
	}
	if _, err := db.Exec("INSERT INTO ItemTable(key, value) VALUES(?, ?)", cursorStateStatsigBootstrapKey, raw); err != nil {
		db.Close()
		t.Fatalf("insert bootstrap: %v", err)
	}
	if err := db.Close(); err != nil {
		t.Fatalf("close setup db: %v", err)
	}

	values := map[string]string{"cursorAuth/cachedEmail": "local@example.com"}
	if err := syncCursorAuthStateDB(path, values); err != nil {
		t.Fatalf("first state sync: %v", err)
	}
	first := readCursorStatsigBootstrapForTest(t, path)
	assertCursorStatsigGateValueForTest(t, first, "disable_terminal_output_ui_streaming", false)
	assertCursorStatsigGateValueForTest(t, first, "unrelated_gate", true)

	if err := syncCursorAuthStateDB(path, values); err != nil {
		t.Fatalf("second state sync: %v", err)
	}
	second := readCursorStatsigBootstrapForTest(t, path)
	if string(second) != string(first) {
		t.Fatalf("repeated sync changed bootstrap:\nfirst:  %s\nsecond: %s", first, second)
	}
}

func readCursorStatsigBootstrapForTest(t *testing.T, path string) []byte {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()

	var raw []byte
	if err := db.QueryRowContext(context.Background(), "SELECT value FROM ItemTable WHERE key = ?", cursorStateStatsigBootstrapKey).Scan(&raw); err != nil {
		t.Fatalf("read bootstrap: %v", err)
	}
	return raw
}

func assertCursorStatsigGateValueForTest(t *testing.T, raw []byte, name string, want bool) {
	t.Helper()
	var payload struct {
		FeatureGates map[string]struct {
			Value bool `json:"value"`
		} `json:"feature_gates"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("decode bootstrap: %v", err)
	}
	gate, ok := payload.FeatureGates[name]
	if !ok {
		t.Fatalf("missing gate %q", name)
	}
	if gate.Value != want {
		t.Fatalf("gate %q value=%t, want %t", name, gate.Value, want)
	}
}

func newCursorStateDBForTest(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.vscdb")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open temporary state db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("CREATE TABLE ItemTable (key TEXT UNIQUE ON CONFLICT REPLACE, value BLOB)"); err != nil {
		t.Fatalf("create ItemTable: %v", err)
	}
	return path
}

func writeCursorStateItemForTest(t *testing.T, path, key, value string) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec("INSERT INTO ItemTable(key, value) VALUES(?, ?)", key, value); err != nil {
		t.Fatalf("insert %s: %v", key, err)
	}
}

func writeCursorAuthStateForTest(t *testing.T, path string, values map[string]string) {
	t.Helper()
	for key, value := range values {
		writeCursorStateItemForTest(t, path, key, value)
	}
}

func writeCursorStatsigBootstrapForTest(t *testing.T, path string, bootstrap map[string]any) {
	t.Helper()
	raw, err := json.Marshal(bootstrap)
	if err != nil {
		t.Fatalf("encode bootstrap: %v", err)
	}
	writeCursorStateItemForTest(t, path, cursorStateStatsigBootstrapKey, string(raw))
}

func assertCursorAuthStateForTest(t *testing.T, path string, want map[string]string) {
	t.Helper()
	for key, value := range want {
		if got := readCursorStateItemForTest(t, path, key); got != value {
			t.Fatalf("%s = %q, want %q", key, got, value)
		}
	}
}

func readCursorStateItemForTest(t *testing.T, path, key string) string {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open state db: %v", err)
	}
	defer db.Close()

	var raw []byte
	if err := db.QueryRowContext(context.Background(), "SELECT value FROM ItemTable WHERE key = ?", key).Scan(&raw); err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return string(raw)
}
