package forwarder

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"

	"cursor/gen/aiserverv1"
)

func TestUserRuleStoreEnforcesLimitsAndPrivateFiles(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rules")
	store := NewUserRuleStore(root)
	if _, err := store.Add(strings.Repeat("x", maxUserRuleBytes+1)); err == nil {
		t.Fatal("expected oversized rule to fail")
	}
	record, err := store.Add("first")
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(filepath.Join(root, record.ID+sharedUserRuleExtension))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	if _, ok, err := store.Update(record.ID, "second"); err != nil || !ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	loaded, err := store.List()
	if err != nil || len(loaded) != 1 || loaded[0].Knowledge != "second" {
		t.Fatalf("list = %#v, err=%v", loaded, err)
	}
}

func TestUserRuleStoreRejectsSymlinkAndIsolatesOversizedLegacyFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside.md")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "linked.md")); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	oversized := filepath.Join(root, "legacy.md")
	if err := os.WriteFile(oversized, []byte(strings.Repeat("x", maxUserRuleBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	records, err := NewUserRuleStore(root).List()
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 0 {
		t.Fatalf("unexpected records: %#v", records)
	}
	if _, err := os.Lstat(oversized); err != nil {
		t.Fatalf("oversized legacy file was removed: %v", err)
	}
}

func TestUserRulePromptIsNotTruncated(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		name := filepath.Join(root, string(rune('a'+i))+".md")
		if err := os.WriteFile(name, []byte(strings.Repeat("x", 90<<10)), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	prompt, _, visible, err := NewUserRuleStore(root).BuildSystemPromptSection()
	if err == nil || prompt != "" || visible != 0 {
		t.Fatalf("expected whole prompt rejection, got bytes=%d visible=%d err=%v", len(prompt), visible, err)
	}
}

func TestUserRuleRenameFailureKeepsOldValue(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rules")
	store := NewUserRuleStore(root)
	record, err := store.Add("old-body")
	if err != nil {
		t.Fatal(err)
	}
	store.ops.rename = func(oldpath, newpath string) error {
		return errors.New("injected rename failure")
	}
	if _, ok, err := store.Update(record.ID, "new-body"); err == nil || ok {
		t.Fatalf("update: ok=%v err=%v", ok, err)
	}
	loaded, err := store.List()
	if err != nil || len(loaded) != 1 || loaded[0].Knowledge != "old-body" {
		t.Fatalf("pre-rename failure changed rule: %#v err=%v", loaded, err)
	}
	data, err := os.ReadFile(filepath.Join(root, record.ID+sharedUserRuleExtension))
	if err != nil || string(data) != "old-body" {
		t.Fatalf("disk changed after pre-rename failure: %q err=%v", data, err)
	}
}

func TestUserRulePostRenameDurabilityCommitsFactSource(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rules")
	store := NewUserRuleStore(root)
	store.ops.syncDir = func(path string) error {
		return errors.New("injected dir sync failure")
	}
	record, err := store.Add("committed-body")
	if err != nil {
		t.Fatalf("Add after post-rename durability should succeed: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(root, record.ID+sharedUserRuleExtension))
	if err != nil || string(data) != "committed-body" {
		t.Fatalf("disk=%q err=%v", data, err)
	}
	loaded, err := store.List()
	if err != nil || len(loaded) != 1 || loaded[0].Knowledge != "committed-body" {
		t.Fatalf("memory/list split: %#v err=%v", loaded, err)
	}
}

func TestKnowledgeBaseAddSucceedsWhenRuleDurabilityFails(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rules")
	rules := NewUserRuleStore(root)
	rules.ops.syncDir = func(path string) error {
		return errors.New("injected dir sync failure")
	}
	service := &Service{
		rules:          rules,
		docsIndexStore: NewDocsIndexStore(filepath.Join(t.TempDir(), "index")),
	}
	response, err := service.KnowledgeBaseAdd(context.Background(), connect.NewRequest(&aiserverv1.KnowledgeBaseAddRequest{
		Knowledge: "rpc-committed",
	}))
	if err != nil {
		t.Fatalf("KnowledgeBaseAdd() error = %v", err)
	}
	if !response.Msg.GetSuccess() || response.Msg.GetId() == "" {
		t.Fatalf("KnowledgeBaseAdd() = %#v", response.Msg)
	}
	listed, err := rules.List()
	if err != nil || len(listed) != 1 || listed[0].Knowledge != "rpc-committed" {
		t.Fatalf("rule fact source missing after durability fault: %#v err=%v", listed, err)
	}
}

func TestUserRuleStoreConcurrentCRUD(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rules")
	store := NewUserRuleStore(root)
	const workers = 8
	var wg sync.WaitGroup
	errCh := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			record, err := store.Add(fmt.Sprintf("body-%d", n))
			if err != nil {
				errCh <- err
				return
			}
			if _, err := store.List(); err != nil {
				errCh <- err
				return
			}
			if _, ok, err := store.Update(record.ID, fmt.Sprintf("updated-%d", n)); err != nil || !ok {
				errCh <- fmt.Errorf("update: ok=%v err=%v", ok, err)
				return
			}
			if n%2 == 0 {
				if err := store.Remove(record.ID); err != nil {
					errCh <- err
				}
			}
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Fatal(err)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".rule-*.tmp"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("leftover tmp files: %v err=%v", matches, err)
	}
	loaded, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded) != workers/2 {
		t.Fatalf("final count=%d want %d records=%#v", len(loaded), workers/2, loaded)
	}
	for _, record := range loaded {
		if !strings.HasPrefix(record.Knowledge, "updated-") {
			t.Fatalf("unparseable or stale record: %#v", record)
		}
	}
}

func TestUserRuleStoreCountCapacity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxUserRuleCount; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.md", i)), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	extra := filepath.Join(root, "zzz.md")
	if err := os.WriteFile(extra, []byte("isolated"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewUserRuleStore(root)
	loaded, err := store.List()
	if err != nil || len(loaded) != maxUserRuleCount {
		t.Fatalf("list count=%d err=%v", len(loaded), err)
	}
	if _, err := store.Add("new"); err == nil {
		t.Fatal("expected count rejection")
	}
	if _, err := os.Lstat(extra); err != nil {
		t.Fatalf("isolated extra file was removed: %v", err)
	}
}

func TestUserRuleStoreTotalBytesCapacity(t *testing.T) {
	root := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	chunk := strings.Repeat("x", maxUserRuleBytes)
	n := maxUserRulesTotalBytes / maxUserRuleBytes
	for i := 0; i < n; i++ {
		if err := os.WriteFile(filepath.Join(root, fmt.Sprintf("%03d.md", i)), []byte(chunk), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	overflow := filepath.Join(root, "zzz.md")
	if err := os.WriteFile(overflow, []byte("overflow"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewUserRuleStore(root)
	loaded, err := store.List()
	if err != nil || len(loaded) != n {
		t.Fatalf("list count=%d want %d err=%v", len(loaded), n, err)
	}
	if _, err := store.Add("y"); err == nil {
		t.Fatal("expected total bytes rejection")
	}
	if _, err := os.Lstat(overflow); err != nil {
		t.Fatalf("overflow fixture was removed: %v", err)
	}
}
