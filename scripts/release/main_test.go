package main

import (
	"archive/zip"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestReleaseManifestUsesForkRepository(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve release test source path")
	}
	repositoryRoot := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "..", ".."))
	taskfileContent, err := os.ReadFile(filepath.Join(repositoryRoot, "Taskfile.yml"))
	if err != nil {
		t.Fatalf("read Taskfile.yml: %v", err)
	}

	var taskfile struct {
		Vars map[string]any `yaml:"vars"`
	}
	if err := yaml.Unmarshal(taskfileContent, &taskfile); err != nil {
		t.Fatalf("parse Taskfile.yml: %v", err)
	}
	const forkRepo = "yaogjim/cursor-byok"
	if got, ok := taskfile.Vars["RELEASE_REPO"].(string); !ok || got != forkRepo {
		t.Fatalf("Taskfile RELEASE_REPO = %#v, want %q", taskfile.Vars["RELEASE_REPO"], forkRepo)
	}

	assetPath := filepath.Join(t.TempDir(), "cursor-byok-0.0.45-macos-arm64.tar.gz")
	payload := []byte("release asset")
	if err := os.WriteFile(assetPath, payload, 0o600); err != nil {
		t.Fatalf("write test asset: %v", err)
	}
	asset, err := buildManifestAsset(assetPath, forkRepo, "0.0.45", filepath.Base(assetPath))
	if err != nil {
		t.Fatalf("build manifest asset: %v", err)
	}
	const expectedURL = "https://github.com/yaogjim/cursor-byok/releases/download/v0.0.45/cursor-byok-0.0.45-macos-arm64.tar.gz"
	if asset.URL != expectedURL {
		t.Fatalf("manifest asset URL = %q, want %q", asset.URL, expectedURL)
	}
	if asset.Size != int64(len(payload)) {
		t.Fatalf("manifest asset size = %d, want %d", asset.Size, len(payload))
	}
}

func TestWriteZipDirArchivesDirectoryContents(t *testing.T) {
	root := t.TempDir()
	staging := filepath.Join(root, "bin", ".zip-amd64")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatalf("create staging dir: %v", err)
	}
	payload := []byte("windows binary")
	binaryName := "cursor-byok.exe"
	if err := os.WriteFile(filepath.Join(staging, binaryName), payload, 0o755); err != nil {
		t.Fatalf("write staging binary: %v", err)
	}

	outPath := filepath.Join(root, "bin", "release", "1.77.0", "cursor-byok-1.77.0-windows-amd64.zip")
	if err := writeZipDir(staging, outPath); err != nil {
		t.Fatalf("write zip: %v", err)
	}

	reader, err := zip.OpenReader(outPath)
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	defer reader.Close()

	if len(reader.File) != 1 {
		t.Fatalf("zip entry count = %d, want 1", len(reader.File))
	}
	// 归档根目录应为暂存目录内容，例如 cursor-byok.exe，不带 bin/.zip-amd64 前缀。
	if got := reader.File[0].Name; got != binaryName {
		t.Fatalf("zip entry name = %q, want %q", got, binaryName)
	}

	entry, err := reader.File[0].Open()
	if err != nil {
		t.Fatalf("open zip entry: %v", err)
	}
	defer entry.Close()
	content, err := io.ReadAll(entry)
	if err != nil {
		t.Fatalf("read zip entry: %v", err)
	}
	if string(content) != string(payload) {
		t.Fatalf("zip entry content = %q, want %q", content, payload)
	}
}
