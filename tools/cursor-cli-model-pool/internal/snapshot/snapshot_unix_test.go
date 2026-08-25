//go:build unix

package snapshot

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

func TestUnsupportedSocketIsCaptureFailure(t *testing.T) {
	root, err := os.MkdirTemp("/tmp", "p")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	socket := filepath.Join(root, "s")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	_, err = Capture(root)
	if err != ErrCaptureFailed {
		t.Fatalf("unsupported type want capture failure, got %v", err)
	}
}
