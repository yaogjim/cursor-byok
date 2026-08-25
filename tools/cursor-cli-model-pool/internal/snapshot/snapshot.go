package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

var ErrCaptureFailed = errors.New("worktree snapshot capture failed")

type Kind string

const (
	KindFile        Kind = "file"
	KindDir         Kind = "dir"
	KindSymlink     Kind = "symlink"
	KindUnsupported Kind = "unsupported"
)

type Entry struct {
	Kind     Kind
	Size     int64
	SHA256   string
	LinkText string
}

type Snapshot map[string]Entry

func Capture(root string) (Snapshot, error) {
	info, err := os.Stat(root)
	if os.IsNotExist(err) {
		return Snapshot{}, nil
	}
	if err != nil {
		return nil, ErrCaptureFailed
	}
	if !info.IsDir() {
		return nil, ErrCaptureFailed
	}
	out := Snapshot{".": {Kind: KindDir}}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if skipGit(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		mode := d.Type()
		switch {
		case mode&os.ModeSymlink != 0:
			text, err := os.Readlink(path)
			if err != nil {
				return err
			}
			out[rel] = Entry{Kind: KindSymlink, LinkText: text}
		case d.IsDir():
			out[rel] = Entry{Kind: KindDir}
		case mode.IsRegular() || mode == 0:
			info, err := d.Info()
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() {
				out[rel] = Entry{Kind: KindUnsupported}
				return ErrCaptureFailed
			}
			sum, size, err := hashFile(path)
			if err != nil {
				return err
			}
			out[rel] = Entry{Kind: KindFile, Size: size, SHA256: sum}
		default:
			out[rel] = Entry{Kind: KindUnsupported}
			return ErrCaptureFailed
		}
		return nil
	})
	if err != nil {
		return nil, ErrCaptureFailed
	}
	return out, nil
}

func Changed(before, after Snapshot) bool {
	if len(before) != len(after) {
		return true
	}
	for key, left := range before {
		right, ok := after[key]
		if !ok || left != right {
			return true
		}
	}
	return false
}

func skipGit(rel string) bool {
	for _, part := range strings.Split(rel, "/") {
		if part == ".git" {
			return true
		}
	}
	return false
}

func hashFile(path string) (string, int64, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer file.Close()
	sum := sha256.New()
	n, err := io.Copy(sum, file)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(sum.Sum(nil)), n, nil
}
