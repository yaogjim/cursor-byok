package snapshot

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMonitorKeepsCreateDeleteMutationSticky(t *testing.T) {
	parent := t.TempDir()
	target := filepath.Join(parent, "worktree")
	monitor, err := Watch(target)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(target, 0o755); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if err := os.RemoveAll(target); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	observed, failed := monitor.Close()
	if failed || !observed {
		t.Fatalf("create/delete observation = %v failed=%v", observed, failed)
	}
}

func TestRelatedPathRejectsSibling(t *testing.T) {
	root := filepath.Join(string(filepath.Separator), "tmp", "pool")
	if !relatedPath(filepath.Join(root, "target"), filepath.Join(root, "target", "file")) {
		t.Fatal("target child must be related")
	}
	if relatedPath(filepath.Join(root, "target"), filepath.Join(root, "sibling")) {
		t.Fatal("sibling must not be related")
	}
}
