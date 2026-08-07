package main

import (
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
