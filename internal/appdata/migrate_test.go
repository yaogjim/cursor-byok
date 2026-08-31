//go:build !windows

package appdata

import (
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestCopyLegacyFileIsPrivateAndRejectsSymlinkTarget(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.md")
	targetDir := filepath.Join(root, "target")
	target := filepath.Join(targetDir, "rule.md")
	if err := os.WriteFile(source, []byte("rule"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !copyLegacyFile(source, target, maxLegacyRuleBytes) {
		t.Fatal("copy failed")
	}
	assertNoMigrateTemp(t, targetDir)
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("file mode = %o, want 600", info.Mode().Perm())
	}
	outside := filepath.Join(root, "outside")
	if err := os.WriteFile(outside, []byte("unchanged"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, target); err != nil {
		t.Fatal(err)
	}
	if copyLegacyFile(source, target, maxLegacyRuleBytes) {
		t.Fatal("symlink target was accepted")
	}
	assertNoMigrateTemp(t, targetDir)
	got, err := os.ReadFile(outside)
	if err != nil || string(got) != "unchanged" {
		t.Fatalf("outside file changed: %q err=%v", got, err)
	}
}

func TestEnsurePrivateDirRejectsSymlink(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "private")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if err := EnsurePrivateDir(link); err == nil {
		t.Fatal("private directory symlink was accepted")
	}
	info, err := os.Stat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("symlink target mode changed to %o", info.Mode().Perm())
	}
}

func TestCopyLegacyFileLeavesOversizedSource(t *testing.T) {
	source := filepath.Join(t.TempDir(), "oversized.md")
	if err := os.WriteFile(source, []byte(strings.Repeat("x", maxLegacyRuleBytes+1)), 0o600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "rule.md")
	if copyLegacyFile(source, target, maxLegacyRuleBytes) {
		t.Fatal("oversized source was copied")
	}
	if _, err := os.Lstat(source); err != nil {
		t.Fatalf("oversized source was removed: %v", err)
	}
	if _, err := os.Lstat(target); !os.IsNotExist(err) {
		t.Fatal("oversized source created a target")
	}
}

func TestCopyLegacyFileDoesNotOverwriteExistingRegularFile(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.md")
	target := filepath.Join(root, "target.md")
	if err := os.WriteFile(source, []byte("legacy"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("current"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !copyLegacyFile(source, target, maxLegacyRuleBytes) {
		t.Fatal("existing regular target should be treated as complete")
	}
	got, err := os.ReadFile(target)
	if err != nil || string(got) != "current" {
		t.Fatalf("existing target overwritten: %q err=%v", got, err)
	}
	info, err := os.Lstat(target)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("existing target mode = %o, want 600", info.Mode().Perm())
	}
	assertNoMigrateTemp(t, root)
}

func TestCopyLegacyRulesCopiesDirectChildrenOnly(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "top.md"), []byte("top"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(source, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "nested", "inner.md"), []byte("inner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(source, "top.md"), filepath.Join(source, "link.md")); err != nil {
		t.Fatal(err)
	}
	if copyLegacyRules(source, target) {
		t.Fatal("unsupported nested/symlink children should keep migration incomplete")
	}
	got, err := os.ReadFile(filepath.Join(target, "top.md"))
	if err != nil || string(got) != "top" {
		t.Fatalf("direct child not copied: %q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(target, "nested")); !os.IsNotExist(err) {
		t.Fatal("nested directory was copied")
	}
	if _, err := os.Lstat(filepath.Join(target, "nested", "inner.md")); !os.IsNotExist(err) {
		t.Fatal("nested file was copied")
	}
	if _, err := os.Lstat(filepath.Join(target, "link.md")); !os.IsNotExist(err) {
		t.Fatal("symlink child was copied")
	}
	assertNoMigrateTemp(t, target)
}

func TestCopyLegacyRulesRejectsNamedPipe(t *testing.T) {
	source := t.TempDir()
	target := t.TempDir()
	if err := os.WriteFile(filepath.Join(source, "ok.md"), []byte("ok"), 0o600); err != nil {
		t.Fatal(err)
	}
	pipe := filepath.Join(source, "pipe.md")
	if err := syscall.Mkfifo(pipe, 0o600); err != nil {
		t.Fatal(err)
	}
	if copyLegacyRules(source, target) {
		t.Fatal("named pipe should keep migration incomplete")
	}
	got, err := os.ReadFile(filepath.Join(target, "ok.md"))
	if err != nil || string(got) != "ok" {
		t.Fatalf("regular sibling not copied: %q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(target, "pipe.md")); !os.IsNotExist(err) {
		t.Fatal("named pipe was copied")
	}
}

func TestCopyLegacyRulesRejectsSymlinkRoot(t *testing.T) {
	real := t.TempDir()
	if err := os.WriteFile(filepath.Join(real, "a.md"), []byte("a"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "rules")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	target := t.TempDir()
	if copyLegacyRules(link, target) {
		t.Fatal("symlink source root was accepted")
	}
	entries, err := os.ReadDir(target)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("followed symlink source root: %v", names(entries))
	}
}

func TestMigrateLegacyPreservesNewValuesWhenUnsupportedRemains(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyRoot := filepath.Join(home, ".cursor-local-assistant")
	legacyRules := filepath.Join(legacyRoot, "rules")
	if err := os.MkdirAll(legacyRules, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, "config.yaml"), []byte("old-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyRules, "keep.md"), []byte("old-rule"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(legacyRules, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyRules, "nested", "inner.md"), []byte("inner"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(home, "outside.md"), filepath.Join(legacyRules, "link.md")); err != nil {
		t.Fatal(err)
	}

	migrateLegacyAssistantHome()

	newRoot := filepath.Join(home, ".cursor-local-assistant-v2")
	newConfig := filepath.Join(newRoot, "config.yaml")
	newRule := filepath.Join(newRoot, "rules", "keep.md")
	if got, err := os.ReadFile(newConfig); err != nil || string(got) != "old-config" {
		t.Fatalf("first config migrate = %q err=%v", got, err)
	}
	if got, err := os.ReadFile(newRule); err != nil || string(got) != "old-rule" {
		t.Fatalf("first rule migrate = %q err=%v", got, err)
	}
	if _, err := os.Lstat(filepath.Join(newRoot, "rules", "nested")); !os.IsNotExist(err) {
		t.Fatal("nested rule directory was copied")
	}
	if _, err := os.Lstat(legacyRoot); err != nil {
		t.Fatalf("legacy root removed despite unsupported children: %v", err)
	}

	if err := os.WriteFile(newConfig, []byte("new-config"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newRule, []byte("new-rule"), 0o600); err != nil {
		t.Fatal(err)
	}

	migrateLegacyAssistantHome()

	if got, err := os.ReadFile(newConfig); err != nil || string(got) != "new-config" {
		t.Fatalf("repeat migrate overwrote config: %q err=%v", got, err)
	}
	if got, err := os.ReadFile(newRule); err != nil || string(got) != "new-rule" {
		t.Fatalf("repeat migrate overwrote rule: %q err=%v", got, err)
	}
	if _, err := os.Lstat(legacyRoot); err != nil {
		t.Fatalf("legacy root removed on repeat migrate: %v", err)
	}
	assertNoMigrateTemp(t, filepath.Join(newRoot, "rules"))
}

func TestMigrateLegacyDeletesRootOnlyWhenComplete(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	legacyRoot := filepath.Join(home, ".cursor-local-assistant")
	legacyRules := filepath.Join(legacyRoot, "rules")
	if err := os.MkdirAll(legacyRules, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyRoot, "config.yaml"), []byte("cfg"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyRules, "a.md"), []byte("rule"), 0o600); err != nil {
		t.Fatal(err)
	}

	migrateLegacyAssistantHome()

	if _, err := os.Lstat(legacyRoot); !os.IsNotExist(err) {
		t.Fatalf("complete migrate left legacy root: %v", err)
	}
	newRoot := filepath.Join(home, ".cursor-local-assistant-v2")
	if got, err := os.ReadFile(filepath.Join(newRoot, "config.yaml")); err != nil || string(got) != "cfg" {
		t.Fatalf("migrated config = %q err=%v", got, err)
	}
	if got, err := os.ReadFile(filepath.Join(newRoot, "rules", "a.md")); err != nil || string(got) != "rule" {
		t.Fatalf("migrated rule = %q err=%v", got, err)
	}
	info, err := os.Lstat(filepath.Join(newRoot, "rules", "a.md"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("migrated rule mode = %o, want 600", info.Mode().Perm())
	}

	if err := os.WriteFile(filepath.Join(newRoot, "config.yaml"), []byte("edited"), 0o600); err != nil {
		t.Fatal(err)
	}
	migrateLegacyAssistantHome()
	if got, err := os.ReadFile(filepath.Join(newRoot, "config.yaml")); err != nil || string(got) != "edited" {
		t.Fatalf("post-success repeat overwrote config: %q err=%v", got, err)
	}
}

func assertNoMigrateTemp(t *testing.T, dir string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".migrate-") && strings.HasSuffix(name, ".tmp") {
			t.Fatalf("leftover migrate temp %q", name)
		}
	}
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}
