package forwarder

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"cursor/gen/aiserverv1"
	"cursor/internal/logger"
)

func TestDocsIndexReconcilePreservesUploadsAndAdditionalDocs(t *testing.T) {
	store := NewDocsIndexStore(filepath.Join(t.TempDir(), "index"))
	if _, err := store.Upsert(DocsIndexRecord{Identifier: "uploaded", Content: "keep-upload", Source: docsIndexSourceLocal}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(DocsIndexRecord{Identifier: "extra", Content: "keep-extra", Source: docsIndexSourceAdditional}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Upsert(DocsIndexRecord{Identifier: "stale-rule", Content: "gone", Source: docsIndexSourceUserRules}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReconcileRuleProjection([]UserRuleRecord{{ID: "live-rule", Knowledge: "truth"}}); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get("stale-rule"); err != nil || ok {
		t.Fatalf("stale rule projection remains: ok=%v err=%v", ok, err)
	}
	if got, ok, err := store.Get("uploaded"); err != nil || !ok || got.Content != "keep-upload" || got.Source != docsIndexSourceLocal {
		t.Fatalf("uploaded record lost: %#v ok=%v err=%v", got, ok, err)
	}
	if got, ok, err := store.Get("extra"); err != nil || !ok || got.Content != "keep-extra" {
		t.Fatalf("additional record lost: %#v ok=%v err=%v", got, ok, err)
	}
	if got, ok, err := store.Get("live-rule"); err != nil || !ok || got.Content != "truth" || got.Source != docsIndexSourceUserRules {
		t.Fatalf("rule projection missing: %#v ok=%v err=%v", got, ok, err)
	}
}

func TestLegacyLocalDocsIndexRecovery(t *testing.T) {
	rulesRoot := filepath.Join(t.TempDir(), "rules")
	if err := os.MkdirAll(rulesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rulesRoot, "legacy-rule.md"), []byte("from-rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	indexRoot := filepath.Join(t.TempDir(), "index")
	if err := os.MkdirAll(indexRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := docsIndexState{
		SchemaVersion: 1,
		Docs: map[string]DocsIndexRecord{
			"legacy-rule":  {ID: "legacy-rule", Identifier: "legacy-rule", Title: "legacy-rule", Content: "old-projection", Source: docsIndexSourceLocal},
			"uploaded-doc": {ID: "uploaded-doc", Identifier: "uploaded-doc", Title: "Upload", Content: "keep-me", Source: docsIndexSourceLocal},
			"extra":        {ID: "extra", Identifier: "extra", Content: "add", Source: docsIndexSourceAdditional},
			"stale-rule":   {ID: "stale-rule", Identifier: "stale-rule", Content: "gone", Source: docsIndexSourceUserRules},
		},
	}
	data, err := json.Marshal(legacy)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(indexRoot, "index.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}

	rules := NewUserRuleStore(rulesRoot)
	index := NewDocsIndexStore(indexRoot)
	service := &Service{rules: rules, docsIndexStore: index}
	service.reconcileRuleProjection("startup", "")

	if got, ok, err := index.Get("legacy-rule"); err != nil || !ok || got.Content != "from-rule" || got.Source != docsIndexSourceUserRules {
		t.Fatalf("legacy local_docs matching a live rule not migrated: %#v ok=%v err=%v", got, ok, err)
	}
	if got, ok, err := index.Get("uploaded-doc"); err != nil || !ok || got.Content != "keep-me" || got.Source != docsIndexSourceLocal {
		t.Fatalf("unmatched legacy local_docs was deleted: %#v ok=%v err=%v", got, ok, err)
	}
	if got, ok, err := index.Get("extra"); err != nil || !ok || got.Content != "add" {
		t.Fatalf("additional_docs lost during recovery: %#v ok=%v err=%v", got, ok, err)
	}
	if _, ok, err := index.Get("stale-rule"); err != nil || ok {
		t.Fatalf("stale user_rules projection remains: ok=%v err=%v", ok, err)
	}
}

func TestStartupReconcileRebuildsRuleProjection(t *testing.T) {
	rules := NewUserRuleStore(filepath.Join(t.TempDir(), "rules"))
	record, err := rules.Add("live-knowledge")
	if err != nil {
		t.Fatal(err)
	}
	index := NewDocsIndexStore(filepath.Join(t.TempDir(), "index"))
	service := &Service{rules: rules, docsIndexStore: index}
	service.reconcileRuleProjection("startup", "")
	got, ok, err := index.Get(record.ID)
	if err != nil || !ok || got.Content != "live-knowledge" || got.Source != docsIndexSourceUserRules {
		t.Fatalf("startup projection: %#v ok=%v err=%v", got, ok, err)
	}
}

func TestDirtyProjectionRetryRemovesDeletedRuleContent(t *testing.T) {
	rules := NewUserRuleStore(filepath.Join(t.TempDir(), "rules"))
	record, err := rules.Add("secret")
	if err != nil {
		t.Fatal(err)
	}
	index := NewDocsIndexStore(filepath.Join(t.TempDir(), "index"))
	service := &Service{rules: rules, docsIndexStore: index}
	service.reconcileRuleProjection("add", record.ID)
	if err := rules.Remove(record.ID); err != nil {
		t.Fatal(err)
	}
	index.MarkRuleProjectionDirty()
	if err := service.reconcileDirtyRuleProjection(); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := index.Get(record.ID); err != nil || ok {
		t.Fatalf("deleted rule remained queryable: ok=%v err=%v", ok, err)
	}
}

func TestDeletedRuleDocsQueryFailClosedWhileDirty(t *testing.T) {
	rules := NewUserRuleStore(filepath.Join(t.TempDir(), "rules"))
	record, err := rules.Add("secret-body")
	if err != nil {
		t.Fatal(err)
	}
	index := NewDocsIndexStore(filepath.Join(t.TempDir(), "index"))
	service := &Service{rules: rules, docsIndexStore: index}
	service.reconcileRuleProjection("add", record.ID)
	if err := rules.Remove(record.ID); err != nil {
		t.Fatal(err)
	}
	index.MarkRuleProjectionDirty()
	index.ops.rename = func(oldpath, newpath string) error {
		return errors.New("injected rename failure")
	}
	_, queryErr := service.DocumentationQuery(context.Background(), connect.NewRequest(&aiserverv1.DocumentationQueryRequest{
		DocIdentifier: record.ID,
	}))
	if queryErr == nil {
		t.Fatal("expected fail-closed DocumentationQuery while dirty reconcile fails")
	}
	got, ok, err := index.Get(record.ID)
	if err != nil || !ok || got.Content != "secret-body" {
		t.Fatalf("stale projection should remain until reconcile succeeds: %#v ok=%v err=%v", got, ok, err)
	}
}

func TestDocsIndexSaveFailureDoesNotPolluteMemory(t *testing.T) {
	store := NewDocsIndexStore(filepath.Join(t.TempDir(), "index"))
	if _, err := store.Upsert(DocsIndexRecord{Identifier: "kept", Content: "old", Source: docsIndexSourceAdditional}); err != nil {
		t.Fatal(err)
	}
	store.ops.rename = func(oldpath, newpath string) error {
		return errors.New("injected rename failure")
	}
	if _, err := store.Upsert(DocsIndexRecord{Identifier: "failed", Content: "new", Source: docsIndexSourceAdditional}); err == nil {
		t.Fatal("expected save failure")
	}
	if _, ok, err := store.Get("failed"); err != nil || ok {
		t.Fatalf("failed write polluted state: ok=%v err=%v", ok, err)
	}
	if got, ok, err := store.Get("kept"); err != nil || !ok || got.Content != "old" {
		t.Fatalf("old value lost after pre-rename failure: %#v ok=%v err=%v", got, ok, err)
	}
}

func TestDocsIndexPostRenameDurabilityKeepsMemoryAndDiskAligned(t *testing.T) {
	root := filepath.Join(t.TempDir(), "index")
	store := NewDocsIndexStore(root)
	if _, err := store.Upsert(DocsIndexRecord{Identifier: "kept", Content: "old", Source: docsIndexSourceAdditional}); err != nil {
		t.Fatal(err)
	}
	store.ops.syncDir = func(path string) error {
		return errors.New("injected dir sync failure")
	}
	updated, err := store.Upsert(DocsIndexRecord{Identifier: "kept", Content: "new", Source: docsIndexSourceAdditional})
	if err != nil {
		t.Fatalf("Upsert after post-rename durability should succeed: %v", err)
	}
	if updated.Content != "new" {
		t.Fatalf("Upsert record = %#v, want content=new", updated)
	}
	got, ok, getErr := store.Get("kept")
	if getErr != nil || !ok || got.Content != "new" {
		t.Fatalf("memory split after post-rename fault: %#v ok=%v err=%v", got, ok, getErr)
	}
	if err := store.Remove("kept"); err != nil {
		t.Fatalf("Remove after post-rename durability should succeed: %v", err)
	}
	if _, ok, err := store.Get("kept"); err != nil || ok {
		t.Fatalf("removed record still present: ok=%v err=%v", ok, err)
	}

	identifier := "https://synthetic.invalid/upload-durability"
	service := &Service{docsIndexStore: store}
	upload, err := service.UploadDocumentation(context.Background(), connect.NewRequest(&aiserverv1.UploadDocumentationRequest{
		DocIdentifier: identifier,
	}))
	if err != nil {
		t.Fatalf("UploadDocumentation after durability fault should succeed: %v", err)
	}
	if got := upload.Msg.GetStatus(); got != aiserverv1.UploadResponse_STATUS_SUCCESS {
		t.Fatalf("UploadDocumentation status = %v, want success", got)
	}
	uploaded, ok, err := store.Get(identifier)
	if err != nil || !ok || uploaded.Source != docsIndexSourceLocal {
		t.Fatalf("uploaded record missing after durability fault: %#v ok=%v err=%v", uploaded, ok, err)
	}
	if err := store.ReconcileRuleProjection(nil); err != nil {
		t.Fatalf("Reconcile after durability fault should succeed: %v", err)
	}
	reloaded := NewDocsIndexStore(root)
	disk, ok, err := reloaded.Get(identifier)
	if err != nil || !ok || disk.Identifier != identifier {
		t.Fatalf("disk split after upload durability fault: %#v ok=%v err=%v", disk, ok, err)
	}
	if _, ok, err := reloaded.Get("kept"); err != nil || ok {
		t.Fatalf("removed record still on disk: ok=%v err=%v", ok, err)
	}
}

func TestDocsIndexCorruptionIsQuarantined(t *testing.T) {
	root := filepath.Join(t.TempDir(), "index")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewDocsIndexStore(root)
	if records, err := store.List("", 0); err != nil || len(records) != 0 {
		t.Fatalf("records=%v err=%v", records, err)
	}
	matches, err := filepath.Glob(filepath.Join(root, "index.json.corrupt-*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("quarantine=%v err=%v", matches, err)
	}
}

func TestDocsIndexOversizedFileIsQuarantined(t *testing.T) {
	root := filepath.Join(t.TempDir(), "index")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("x"), maxDocsIndexFileBytes+1)
	if err := os.WriteFile(filepath.Join(root, "index.json"), payload, 0o600); err != nil {
		t.Fatal(err)
	}
	store := NewDocsIndexStore(root)
	if records, err := store.List("", 0); err != nil || len(records) != 0 {
		t.Fatalf("records=%v err=%v", records, err)
	}
	matches, err := filepath.Glob(filepath.Join(root, "index.json.corrupt-*"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("quarantine=%v err=%v", matches, err)
	}
}

func TestDocsIndexRejectsRecordAndContentCapacity(t *testing.T) {
	store := NewDocsIndexStore(filepath.Join(t.TempDir(), "index"))
	if _, err := store.Upsert(DocsIndexRecord{Identifier: "too-big", Content: strings.Repeat("x", maxDocsIndexContentBytes+1), Source: docsIndexSourceAdditional}); err == nil {
		t.Fatal("expected content capacity rejection")
	}
	root := filepath.Join(t.TempDir(), "index-records")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	docs := make(map[string]DocsIndexRecord, maxDocsIndexRecords+1)
	for i := 0; i < maxDocsIndexRecords+1; i++ {
		id := fmt.Sprintf("doc-%03d", i)
		docs[id] = DocsIndexRecord{ID: id, Identifier: id, Source: docsIndexSourceAdditional}
	}
	data, err := json.Marshal(docsIndexState{SchemaVersion: 1, Docs: docs})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.json"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded := NewDocsIndexStore(root)
	if records, err := loaded.List("", 0); err != nil || len(records) != 0 {
		t.Fatalf("over-capacity index should quarantine, records=%d err=%v", len(records), err)
	}
}

func TestRuleProjectionFailureLogsMetadataOnly(t *testing.T) {
	logger.Init()
	previous := slog.Default()
	var buf bytes.Buffer
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	secret := "SECRET_RULE_BODY_DO_NOT_LOG"
	rules := NewUserRuleStore(filepath.Join(t.TempDir(), "rules"))
	record, err := rules.Add(secret)
	if err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(t.TempDir(), "docs-index-file")
	if err := os.WriteFile(blocked, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	service := &Service{rules: rules, docsIndexStore: NewDocsIndexStore(blocked)}
	service.reconcileRuleProjection("add", record.ID)
	logged := buf.String()
	if strings.Contains(logged, secret) {
		t.Fatalf("rule body leaked into logs: %s", logged)
	}
	if !strings.Contains(logged, "err_type=") || !strings.Contains(logged, "operation=add") {
		t.Fatalf("expected metadata-only projection log, got %s", logged)
	}
}
