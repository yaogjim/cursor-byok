//go:build unix

package engine

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"cursor-cli-model-pool/internal/classify"
	"cursor-cli-model-pool/internal/config"
)

func TestCancelKillsProcessGroup(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, nil, nil)
	raw, _ := json.Marshal(map[string]string{fx.idA: "hang", fx.idB: "success"})
	if err := os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEngine(fx)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	start := time.Now()
	out, _ := e.Run(ctx, []byte(fx.prompt))
	elapsed := time.Since(start)
	if elapsed > 6*time.Second {
		t.Fatalf("cancel took %s", elapsed)
	}
	if out.ErrorCategory != classify.CatCancel {
		t.Fatalf("category = %q out=%+v", out.ErrorCategory, out)
	}
	if len(out.Launched) != 1 {
		t.Fatalf("must stop scheduling: %+v", out)
	}
	pidBytes, err := os.ReadFile(filepath.Join(fx.fakeDir, "grandchild.pid"))
	if err != nil {
		t.Fatalf("grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild pid %d still alive", pid)
}

func TestCancelKillsGrandchildWhenParentExitsOnTerm(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, nil, nil)
	raw, _ := json.Marshal(map[string]string{fx.idA: "hang_parent_dies", fx.idB: "success"})
	if err := os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	e := newEngine(fx)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	out, _ := e.Run(ctx, []byte(fx.prompt))
	elapsed := time.Since(start)
	if elapsed < 2*time.Second {
		t.Fatalf("must wait full 2s after TERM before PGID KILL, elapsed=%s", elapsed)
	}
	if elapsed > 8*time.Second {
		t.Fatalf("cancel took %s", elapsed)
	}
	if out.ErrorCategory != classify.CatCancel {
		t.Fatalf("category = %q out=%+v", out.ErrorCategory, out)
	}
	if len(out.Launched) != 1 {
		t.Fatalf("must stop scheduling: %+v", out)
	}
	pidBytes, err := os.ReadFile(filepath.Join(fx.fakeDir, "grandchild.pid"))
	if err != nil {
		t.Fatalf("grandchild pid: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(pidBytes)))
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if err := syscall.Kill(pid, 0); err != nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("grandchild pid %d still alive after parent exited on TERM", pid)
}
