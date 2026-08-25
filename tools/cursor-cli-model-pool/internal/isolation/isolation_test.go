package isolation

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestModuleDoesNotImportRootInternal(t *testing.T) {
	root := moduleRoot()
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == ".git" || d.Name() == "vendor") {
			return filepath.SkipDir
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if hasRootInternalImport(string(body)) {
			t.Errorf("%s imports root internal", path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func hasRootInternalImport(src string) bool {
	inBlock := false
	for _, line := range strings.Split(src, "\n") {
		trim := strings.TrimSpace(line)
		if strings.HasPrefix(trim, "import (") {
			inBlock = true
			continue
		}
		if inBlock {
			if trim == ")" {
				inBlock = false
				continue
			}
			if importPath(trim) == "cursor/internal" || strings.HasPrefix(importPath(trim), "cursor/internal/") {
				return true
			}
			continue
		}
		if strings.HasPrefix(trim, "import ") {
			path := importPath(strings.TrimPrefix(trim, "import "))
			if path == "cursor/internal" || strings.HasPrefix(path, "cursor/internal/") {
				return true
			}
		}
	}
	return false
}

func importPath(spec string) string {
	spec = strings.TrimSpace(spec)
	if i := strings.Index(spec, "\""); i >= 0 {
		rest := spec[i+1:]
		if j := strings.Index(rest, "\""); j >= 0 {
			return rest[:j]
		}
	}
	return ""
}

func moduleRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}
