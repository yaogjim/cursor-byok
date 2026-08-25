package snapshot

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCaptureHashesFilesDirsAndSymlinksAndIgnoresGit(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git", "objects"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".git", "HEAD"), []byte("ref"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("a.txt", filepath.Join(root, "sub", "link")); err != nil {
		t.Fatal(err)
	}
	snap, err := Capture(root)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if _, ok := snap[".git"]; ok {
		t.Fatal("captured .git")
	}
	if _, ok := snap[".git/HEAD"]; ok {
		t.Fatal("captured git object")
	}
	if snap["sub"].Kind != KindDir {
		t.Fatalf("sub = %+v", snap["sub"])
	}
	file := snap["sub/a.txt"]
	if file.Kind != KindFile || file.Size != 5 || file.SHA256 == "" {
		t.Fatalf("file = %+v", file)
	}
	if snap["sub/link"] != (Entry{Kind: KindSymlink, LinkText: "a.txt"}) {
		t.Fatalf("link = %+v", snap["sub/link"])
	}
	if err := os.WriteFile(filepath.Join(root, "sub", "a.txt"), []byte("hello!"), 0o644); err != nil {
		t.Fatal(err)
	}
	after, err := Capture(root)
	if err != nil {
		t.Fatal(err)
	}
	if !Changed(snap, after) {
		t.Fatal("content change must be mutated")
	}
}

func TestCaptureAbsentRootIsEmptyNotError(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "absent-worktree")
	snap, err := Capture(missing)
	if err != nil {
		t.Fatalf("absent root must be representable, err=%v", err)
	}
	if len(snap) != 0 {
		t.Fatalf("absent snapshot = %#v", snap)
	}
}

func TestCaptureExistingEmptyRootDiffersFromAbsent(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "worktree")
	before, err := Capture(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	after, err := Capture(root)
	if err != nil {
		t.Fatal(err)
	}
	if !Changed(before, after) {
		t.Fatal("creating an empty worktree must be mutation")
	}
}

func TestCaptureNonDirIsFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(path); err != ErrCaptureFailed {
		t.Fatalf("non-dir want capture failure, got %v", err)
	}
}
