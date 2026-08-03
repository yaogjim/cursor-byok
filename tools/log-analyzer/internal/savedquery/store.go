package savedquery

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	querydsl "cursor-log-analyzer/internal/query"
)

const schemaVersion = 1

type Query struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	DSL       string    `json:"dsl"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Store struct {
	mu      sync.RWMutex
	path    string
	queries map[string]Query
	now     func() time.Time
}

type document struct {
	SchemaVersion int     `json:"schema_version"`
	Queries       []Query `json:"queries"`
}

func Open(path string) (*Store, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if path == "." || path == "" {
		return nil, errors.New("saved query path is required")
	}
	store := &Store{path: path, queries: make(map[string]Query), now: time.Now}
	payload, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return store, nil
	}
	if err != nil {
		return nil, err
	}
	var persisted document
	if err := json.Unmarshal(payload, &persisted); err != nil {
		return nil, fmt.Errorf("decode saved queries: %w", err)
	}
	if persisted.SchemaVersion != schemaVersion {
		return nil, fmt.Errorf("unsupported saved query schema_version=%d", persisted.SchemaVersion)
	}
	for _, item := range persisted.Queries {
		if err := validate(item); err != nil {
			return nil, fmt.Errorf("validate saved query %q: %w", item.ID, err)
		}
		if _, err := querydsl.Compile(item.DSL); err != nil {
			return nil, fmt.Errorf("compile saved query %q: %w", item.ID, err)
		}
		if _, exists := store.queries[item.ID]; exists {
			return nil, fmt.Errorf("duplicate saved query id %q", item.ID)
		}
		store.queries[item.ID] = item
	}
	return store, nil
}

func (store *Store) List() []Query {
	if store == nil {
		return nil
	}
	store.mu.RLock()
	defer store.mu.RUnlock()
	result := make([]Query, 0, len(store.queries))
	for _, item := range store.queries {
		result = append(result, item)
	}
	sort.Slice(result, func(left int, right int) bool {
		if result[left].UpdatedAt.Equal(result[right].UpdatedAt) {
			return result[left].ID < result[right].ID
		}
		return result[left].UpdatedAt.After(result[right].UpdatedAt)
	})
	return result
}

func (store *Store) Put(item Query) (Query, error) {
	if store == nil {
		return Query{}, errors.New("saved query store is nil")
	}
	item.ID = strings.TrimSpace(item.ID)
	item.Name = strings.TrimSpace(item.Name)
	item.DSL = strings.TrimSpace(item.DSL)
	if _, err := querydsl.Compile(item.DSL); err != nil {
		return Query{}, fmt.Errorf("invalid query DSL: %w", err)
	}
	if err := validate(item); err != nil {
		return Query{}, err
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	now := store.now().UTC()
	if item.ID == "" {
		item.ID = randomID()
		item.CreatedAt = now
	} else if existing, ok := store.queries[item.ID]; ok {
		item.CreatedAt = existing.CreatedAt
	} else if item.CreatedAt.IsZero() {
		item.CreatedAt = now
	}
	item.UpdatedAt = now
	previous, existed := store.queries[item.ID]
	store.queries[item.ID] = item
	if err := store.persistLocked(); err != nil {
		if existed {
			store.queries[item.ID] = previous
		} else {
			delete(store.queries, item.ID)
		}
		return Query{}, err
	}
	return item, nil
}

func (store *Store) Delete(id string) error {
	if store == nil {
		return errors.New("saved query store is nil")
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return errors.New("saved query id is required")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	previous, exists := store.queries[id]
	if !exists {
		return nil
	}
	delete(store.queries, id)
	if err := store.persistLocked(); err != nil {
		store.queries[id] = previous
		return err
	}
	return nil
}

func (store *Store) persistLocked() error {
	items := make([]Query, 0, len(store.queries))
	for _, item := range store.queries {
		items = append(items, item)
	}
	sort.Slice(items, func(left int, right int) bool { return items[left].ID < items[right].ID })
	payload, err := json.MarshalIndent(document{SchemaVersion: schemaVersion, Queries: items}, "", "  ")
	if err != nil {
		return err
	}
	payload = append(payload, '\n')
	dir := filepath.Dir(store.path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	temporary := store.path + ".tmp"
	if err := os.WriteFile(temporary, payload, 0o600); err != nil {
		return err
	}
	if err := os.Chmod(temporary, 0o600); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	if err := os.Rename(temporary, store.path); err != nil {
		_ = os.Remove(temporary)
		return err
	}
	return nil
}

func validate(item Query) error {
	switch {
	case len(strings.TrimSpace(item.ID)) > 128:
		return errors.New("saved query id exceeds 128 characters")
	case strings.TrimSpace(item.Name) == "":
		return errors.New("saved query name is required")
	case len(item.Name) > 128:
		return errors.New("saved query name exceeds 128 characters")
	case strings.TrimSpace(item.DSL) == "":
		return errors.New("saved query DSL is required")
	case len(item.DSL) > 4096:
		return errors.New("saved query DSL exceeds 4096 characters")
	}
	return nil
}

func randomID() string {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return fmt.Sprintf("query-%d", time.Now().UTC().UnixNano())
	}
	return hex.EncodeToString(buffer)
}
