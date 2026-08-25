package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"cursor-cli-model-pool/internal/classify"
	"cursor-cli-model-pool/internal/config"
	"cursor-cli-model-pool/internal/identity"
	"cursor-cli-model-pool/internal/journal"
	"cursor-cli-model-pool/internal/paths"
)

var fakeAgent string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "cursor-cli-fake-agent-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "tempdir: %v\n", err)
		os.Exit(1)
	}
	fakeAgent = filepath.Join(dir, "agent")
	cmd := exec.Command("go", "build", "-o", fakeAgent, ".")
	cmd.Dir = filepath.Join(moduleRoot(), "testdata", "fake-agent")
	if out, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build fake agent: %v\n%s\n", err, out)
		os.Exit(1)
	}
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}

func moduleRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
}

type fixture struct {
	home    string
	fakeDir string
	repo    string
	idA     string
	idB     string
	idLogic string
	key     string
	prompt  string
}

func setupFixture(t *testing.T, mode string, allowWrite bool, models []string, behavior map[string]string) *fixture {
	t.Helper()
	fx := &fixture{
		home:    t.TempDir(),
		fakeDir: t.TempDir(),
		key:     "in-memory-only-" + t.Name(),
		prompt:  "UNIQUE-PROMPT-" + t.Name(),
	}
	t.Setenv("HOME", fx.home)
	t.Setenv("FAKE_AGENT_DIR", fx.fakeDir)
	var err error
	fx.idA, err = identity.Compute("https://example.com/a", "openai", "model-a", fx.key, "A", "")
	if err != nil {
		t.Fatal(err)
	}
	fx.idB, err = identity.Compute("https://example.com/b", "openai", "model-b", fx.key, "B", "")
	if err != nil {
		t.Fatal(err)
	}
	fx.idLogic, err = identity.Compute("https://example.com/logic", "openai", "logic", fx.key, "Logic", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(models) == 0 {
		models = []string{fx.idA, fx.idB}
	}
	dataDir := filepath.Join(fx.home, paths.DirName)
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		t.Fatal(err)
	}
	pool := fmt.Sprintf("schemaVersion: 1\nagentPath: %q\nendpoint: %s\nmode: %s\nmodels:\n", fakeAgent, paths.AllowedEndpoint, mode)
	for _, id := range models {
		pool += "  - " + id + "\n"
	}
	if allowWrite {
		pool += "safety:\n  allowWrite: true\n"
	}
	if err := os.WriteFile(filepath.Join(dataDir, paths.PoolFileName), []byte(pool), 0o600); err != nil {
		t.Fatal(err)
	}
	byok := fmt.Sprintf("modelAdapters:\n"+
		"  - displayName: A\n    type: openai\n    baseURL: https://example.com/a\n    apiKey: %s\n    modelID: model-a\n    providerFallback:\n      enabled: false\n"+
		"  - displayName: B\n    type: openai\n    baseURL: https://example.com/b\n    apiKey: %s\n    modelID: model-b\n    providerFallback:\n      enabled: false\n"+
		"  - displayName: Logic\n    type: openai\n    baseURL: https://example.com/logic\n    apiKey: %s\n    modelID: logic\n    providerFallback:\n      enabled: true\n",
		fx.key, fx.key, fx.key)
	if err := os.WriteFile(filepath.Join(dataDir, paths.BYOKFileName), []byte(byok), 0o600); err != nil {
		t.Fatal(err)
	}
	writeListedModels(t, fx.fakeDir, fx.idA, fx.idB)
	if behavior == nil {
		behavior = map[string]string{"*": "success"}
	}
	raw, _ := json.Marshal(behavior)
	if err := os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if mode == config.ModeWrite {
		fx.repo = initRepo(t)
	} else {
		fx.repo = t.TempDir()
	}
	return fx
}

func initRepo(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "README"), []byte("hi"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmds := [][]string{
		{"git", "init", "-b", "main"},
		{"git", "config", "user.email", "test@example.com"},
		{"git", "config", "user.name", "test"},
		{"git", "config", "commit.gpgsign", "false"},
		{"git", "add", "."},
		{"git", "commit", "-m", "init"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out)
		}
	}
	return repo
}

func newEngine(fx *fixture) *Engine {
	return &Engine{
		Stdout: io.Discard,
		Stderr: io.Discard,
		Jitter: func() time.Duration { return 0 },
		Sleep: func(ctx context.Context, d time.Duration) error {
			if d == 0 {
				return ctx.Err()
			}
			return SleepCtx(ctx, d)
		},
		NewID: func() string { return "aaaaaaaaaaaaaaaa" },
		CWD:   fx.repo,
	}
}

func TestBuildArgvAskIncludesModeAndOmitsPrompt(t *testing.T) {
	args := BuildArgv("/bin/agent", paths.AllowedEndpoint, "0123456789abcdef", config.ModeAsk, "")
	if !contains(args, "--mode") || !contains(args, "ask") || contains(args, "--worktree") {
		t.Fatalf("argv = %v", args)
	}
	if contains(args, "--force") || contains(args, "--yolo") || contains(args, "--printenv") {
		t.Fatalf("forbidden flags: %v", args)
	}
}

func TestBuildArgvWriteOmitsModeAndForcesWorktree(t *testing.T) {
	args := BuildArgv("/bin/agent", paths.AllowedEndpoint, "0123456789abcdef", config.ModeWrite, "cursor-poolaaaaaaaaaaaaaaaa")
	if contains(args, "--mode") {
		t.Fatalf("write must omit --mode: %v", args)
	}
	if !contains(args, "--worktree") || !contains(args, "cursor-poolaaaaaaaaaaaaaaaa") {
		t.Fatalf("write must pass generated worktree: %v", args)
	}
}

func TestRunOrderAndOnceAndStdinNotInArgv(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, nil, map[string]string{
		"*": "preoutput_429",
	})
	behavior := map[string]string{fx.idA: "preoutput_429", fx.idB: "success"}
	raw, _ := json.Marshal(behavior)
	if err := os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var jitterCalls int
	e := newEngine(fx)
	e.Jitter = func() time.Duration {
		jitterCalls++
		return 0
	}
	out, err := e.Run(context.Background(), []byte(fx.prompt))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !out.Success || strings.Join(out.Launched, ",") != fx.idA+","+fx.idB {
		t.Fatalf("outcome = %+v", out)
	}
	if jitterCalls != 1 {
		t.Fatalf("jitter calls = %d, want 1", jitterCalls)
	}
	launches := readLines(t, filepath.Join(fx.fakeDir, "launch-order.txt"))
	if strings.Join(launches, ",") != fx.idA+","+fx.idB {
		t.Fatalf("launches = %v", launches)
	}
	inv := readInvocations(t, fx.fakeDir)
	if len(inv) != 2 {
		t.Fatalf("invocations = %d", len(inv))
	}
	for _, item := range inv {
		joined := strings.Join(item.Argv, "\x00")
		if strings.Contains(joined, fx.prompt) {
			t.Fatal("prompt leaked into argv")
		}
		if item.Stdin != fx.prompt {
			t.Fatalf("stdin = %q", item.Stdin)
		}
		if !contains(item.Argv, "--print") || !contains(item.Argv, "stream-json") {
			t.Fatalf("missing print flags: %v", item.Argv)
		}
	}
}

func TestSuccessResultWithHTTP200IsNotAnError(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, nil, nil)
	raw, _ := json.Marshal(map[string]string{fx.idA: "success_200", fx.idB: "preoutput_429"})
	if err := os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := newEngine(fx).Run(context.Background(), []byte(fx.prompt))
	if err != nil || !out.Success || out.ErrorCategory != classify.CatNone || len(out.Launched) != 1 {
		t.Fatalf("HTTP 200 success misclassified: out=%+v err=%v", out, err)
	}
}

func TestPreOutputSwitchAndRetryConnectionStayOpen(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, nil, nil)
	raw, _ := json.Marshal(map[string]string{fx.idA: "retry_then_429", fx.idB: "success"})
	_ = os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600)
	e := newEngine(fx)
	out, err := e.Run(context.Background(), []byte(fx.prompt))
	if err != nil || !out.Success || len(out.Launched) != 2 {
		t.Fatalf("out=%+v err=%v", out, err)
	}
}

func TestThinkingAssistantToolProhibitSwitch(t *testing.T) {
	cases := []string{"thinking_fail", "assistant_fail", "tool_fail"}
	for _, behavior := range cases {
		t.Run(behavior, func(t *testing.T) {
			fx := setupFixture(t, config.ModeAsk, false, nil, map[string]string{"*": behavior})
			raw, _ := json.Marshal(map[string]string{fx.idA: behavior, fx.idB: "success"})
			_ = os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600)
			e := newEngine(fx)
			out, err := e.Run(context.Background(), []byte(fx.prompt))
			if err == nil || out.Success {
				t.Fatalf("expected failure, out=%+v", out)
			}
			if !out.NeedsReview || out.Phase != classify.PhaseNeedsReview {
				t.Fatalf("expected needs_review, out=%+v", out)
			}
			if len(out.Launched) != 1 || out.Launched[0] != fx.idA {
				t.Fatalf("switched after output: %+v", out)
			}
		})
	}
}

func TestUntypedAnd500AndStderrDoNotSwitch(t *testing.T) {
	cases := []string{"untyped", "http_500", "http_401", "stderr_429"}
	for _, behavior := range cases {
		t.Run(behavior, func(t *testing.T) {
			fx := setupFixture(t, config.ModeAsk, false, nil, nil)
			raw, _ := json.Marshal(map[string]string{fx.idA: behavior, fx.idB: "success"})
			_ = os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600)
			e := newEngine(fx)
			out, _ := e.Run(context.Background(), []byte(fx.prompt))
			if out.Success || len(out.Launched) != 1 {
				t.Fatalf("must fail-closed without switch: %+v", out)
			}
		})
	}
}

func TestFallbackAliasRejected(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, nil, nil)
	writeListedModels(t, fx.fakeDir, fx.idA, fx.idB, fx.idLogic)
	replacePoolModels(t, fx, []string{fx.idLogic})
	e := newEngine(fx)
	err := e.Validate(context.Background())
	if err == nil || !strings.Contains(err.Error(), "providerFallback") {
		t.Fatalf("expected fallback reject, got %v", err)
	}
}

func TestJournalRedactsPromptNDJSONKeyAndAbsolutePath(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, nil, map[string]string{"*": "success"})
	raw, _ := json.Marshal(map[string]string{fx.idA: "success"})
	_ = os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600)
	replacePoolModels(t, fx, []string{fx.idA})
	e := newEngine(fx)
	if _, err := e.Run(context.Background(), []byte(fx.prompt)); err != nil {
		t.Fatal(err)
	}
	journalPath := filepath.Join(fx.home, paths.DirName, paths.JournalFileName)
	body, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, secret := range []string{fx.prompt, fx.key, fx.home, fx.repo, "thinking", "UNIQUE-PROMPT"} {
		if strings.Contains(text, secret) && secret != "thinking" {
			t.Fatalf("journal leaked %q: %s", secret, text)
		}
	}
	if strings.Contains(text, fx.prompt) || strings.Contains(text, fx.key) || strings.Contains(text, fx.home) {
		t.Fatalf("journal leaked secrets: %s", text)
	}
	if strings.Contains(text, `{"type":`) {
		t.Fatal("journal stored NDJSON")
	}
}

func TestWriteForcesWorktreeAndMutationStopsSwitch(t *testing.T) {
	fx := setupFixture(t, config.ModeWrite, true, nil, nil)
	raw, _ := json.Marshal(map[string]string{fx.idA: "mutate_then_429", fx.idB: "success"})
	_ = os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600)
	e := newEngine(fx)
	out, _ := e.Run(context.Background(), []byte(fx.prompt))
	if !out.MutationObserved || !out.NeedsReview || len(out.Launched) != 1 {
		t.Fatalf("mutation must stop switch: %+v", out)
	}
	inv := readInvocations(t, fx.fakeDir)
	if len(inv) != 1 {
		t.Fatalf("launches = %d", len(inv))
	}
	if contains(inv[0].Argv, "--mode") {
		t.Fatalf("write argv leaked --mode: %v", inv[0].Argv)
	}
	if !contains(inv[0].Argv, "--worktree") || !contains(inv[0].Argv, "cursor-poolaaaaaaaaaaaaaaaa") {
		t.Fatalf("missing generated worktree: %v", inv[0].Argv)
	}
	top := gitToplevelForTest(t, fx.repo)
	if inv[0].CWD != top {
		t.Fatalf("write child Dir = %q, want git toplevel %q", inv[0].CWD, top)
	}
	name := "cursor-poolaaaaaaaaaaaaaaaa"
	want := expectedWriteWorktreePath(t, fx.repo, fx.home, name)
	if _, err := os.Stat(filepath.Join(want, "pool-mutated.txt")); err != nil {
		t.Fatalf("fake agent must mutate Cursor worktree path %s: %v", want, err)
	}
	if _, err := os.Stat(want); err != nil {
		t.Fatalf("write must retain worktree for review: %v", err)
	}
	atStart := readLines(t, filepath.Join(fx.fakeDir, "worktree-at-start.txt"))
	if len(atStart) < 1 || atStart[0] != "absent" {
		t.Fatalf("baseline must be absent at child start: %v", atStart)
	}
	sibling := filepath.Join(filepath.Dir(fx.repo), name)
	if _, err := os.Stat(sibling); err == nil {
		t.Fatalf("controller created sibling git worktree %s", sibling)
	}
	if n := gitWorktreeCount(t, fx.repo); n != 1 {
		t.Fatalf("git worktree count = %d, controller must not git worktree add", n)
	}
	journalPath := filepath.Join(fx.home, paths.DirName, paths.JournalFileName)
	body, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, name) {
		t.Fatalf("journal missing worktree name: %s", text)
	}
	if strings.Contains(text, want) || strings.Contains(text, filepath.Join(fx.home, ".cursor", "worktrees")) {
		t.Fatalf("journal stored worktree path: %s", text)
	}
}

func TestWriteEmptyOrDeletedWorktreeMutationStopsSwitch(t *testing.T) {
	for _, behavior := range []string{"empty_worktree_then_429", "create_delete_worktree_then_429"} {
		t.Run(behavior, func(t *testing.T) {
			fx := setupFixture(t, config.ModeWrite, true, nil, nil)
			raw, _ := json.Marshal(map[string]string{fx.idA: behavior, fx.idB: "success"})
			if err := os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600); err != nil {
				t.Fatal(err)
			}
			out, err := newEngine(fx).Run(context.Background(), []byte(fx.prompt))
			if err == nil || !out.MutationObserved || !out.NeedsReview || len(out.Launched) != 1 {
				t.Fatalf("ephemeral mutation must stop switch: out=%+v err=%v", out, err)
			}
		})
	}
}

func TestAskDoesNotCreateWorktree(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, []string{}, map[string]string{"": "success"})
	replacePoolModels(t, fx, []string{fx.idA})
	raw, _ := json.Marshal(map[string]string{fx.idA: "success"})
	_ = os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600)
	e := newEngine(fx)
	out, err := e.Run(context.Background(), []byte("hi"))
	if err != nil || !out.Success {
		t.Fatalf("out=%+v err=%v", out, err)
	}
	if out.WorktreeName != "" && out.WorktreeName != "unknown" {
		if _, err := os.Stat(filepath.Join(filepath.Dir(fx.repo), out.WorktreeName)); err == nil {
			t.Fatal("ask created worktree")
		}
	}
	if entries, err := os.ReadDir(filepath.Join(fx.home, ".cursor", "worktrees")); err == nil && len(entries) > 0 {
		t.Fatalf("ask created Cursor worktrees: %v", names(entries))
	}
	inv := readInvocations(t, fx.fakeDir)
	if contains(inv[0].Argv, "--worktree") {
		t.Fatalf("ask passed --worktree: %v", inv[0].Argv)
	}
}

func TestDryRunWritePlanOmitsMode(t *testing.T) {
	fx := setupFixture(t, config.ModeWrite, true, []string{""}, nil)
	replacePoolModels(t, fx, []string{fx.idA})
	e := newEngine(fx)
	plan, err := e.DryRun(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if plan.WorktreeName != "cursor-poolaaaaaaaaaaaaaaaa" {
		t.Fatalf("worktree name = %q", plan.WorktreeName)
	}
	if contains(plan.Models[0].Argv, "--mode") || !contains(plan.Models[0].Argv, "--worktree") || !contains(plan.Models[0].Argv, "cursor-poolaaaaaaaaaaaaaaaa") {
		t.Fatalf("plan argv = %v", plan.Models[0].Argv)
	}
}

func TestUnsupportedPlatformPreflight(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, []string{""}, nil)
	replacePoolModels(t, fx, []string{fx.idA})
	e := newEngine(fx)
	e.platformOK = func() bool { return false }
	_, err := e.Run(context.Background(), []byte("x"))
	if !errors.Is(err, errUnsupported) {
		t.Fatalf("err = %v", err)
	}
}

func TestSpawnFailureIsSwitchable(t *testing.T) {
	e := &Engine{Stdout: io.Discard, Stderr: io.Discard}
	cfg := config.Config{
		AgentPath: filepath.Join(t.TempDir(), "missing-agent"),
		Endpoint:  paths.AllowedEndpoint,
		Mode:      config.ModeAsk,
	}
	out := e.runModel(context.Background(), cfg, nil, 0, "0123456789abcdef", "", "", "", nil)
	if out.ErrorCategory != classify.CatSpawnFailure {
		t.Fatalf("category = %q phase=%s", out.ErrorCategory, out.Phase)
	}
	if !classify.Switchable(out.Phase, out.ErrorCategory) {
		t.Fatal("spawn failure should allow next model")
	}
}

func TestEachModelLaunchedAtMostOnce(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, nil, nil)
	raw, _ := json.Marshal(map[string]string{fx.idA: "preoutput_502", fx.idB: "preoutput_503"})
	_ = os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600)
	e := newEngine(fx)
	out, _ := e.Run(context.Background(), []byte(fx.prompt))
	launches := readLines(t, filepath.Join(fx.fakeDir, "launch-order.txt"))
	if len(launches) != 2 || launches[0] == launches[1] {
		t.Fatalf("launches = %v out=%+v", launches, out)
	}
}

func replacePoolModels(t *testing.T, fx *fixture, models []string) {
	t.Helper()
	path := filepath.Join(fx.home, paths.DirName, paths.PoolFileName)
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(raw), "\n")
	var out []string
	skip := false
	for _, line := range lines {
		if strings.HasPrefix(line, "models:") {
			out = append(out, "models:")
			for _, id := range models {
				out = append(out, "  - "+id)
			}
			skip = true
			continue
		}
		if skip && strings.HasPrefix(line, "  - ") {
			continue
		}
		skip = false
		out = append(out, line)
	}
	if err := os.WriteFile(path, []byte(strings.Join(out, "\n")), 0o600); err != nil {
		t.Fatal(err)
	}
}

type invocation struct {
	Argv  []string `json:"argv"`
	Stdin string   `json:"stdin"`
	CWD   string   `json:"cwd"`
	Model string   `json:"model"`
}

func readInvocations(t *testing.T, dir string) []invocation {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dir, "invocations.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var out []invocation
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line == "" {
			continue
		}
		var item invocation
		if err := json.Unmarshal([]byte(line), &item); err != nil {
			t.Fatal(err)
		}
		out = append(out, item)
	}
	return out
}

func readLines(t *testing.T, path string) []string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

func contains(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func TestParseModelListExtractsHexDashDisplayName(t *testing.T) {
	ids := parseModelList([]byte("0123456789abcdef - Display A\ndef0123456789abc - Other Model\n"))
	if len(ids) != 2 || ids[0] != "0123456789abcdef" || ids[1] != "def0123456789abc" {
		t.Fatalf("ids = %v", ids)
	}
}

func TestParseModelListKeepsDuplicates(t *testing.T) {
	ids := parseModelList([]byte("0123456789abcdef - Display A\n0123456789abcdef - Display A again\n"))
	if len(ids) != 2 || ids[0] != ids[1] || ids[0] != "0123456789abcdef" {
		t.Fatalf("duplicates must be kept, ids = %v", ids)
	}
}

func TestParseModelListJSONArrayKeepsDuplicates(t *testing.T) {
	ids := parseModelList([]byte(`["0123456789abcdef","0123456789abcdef"]`))
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("json duplicates must be kept, ids = %v", ids)
	}
}

func TestDuplicateAgentModelsFailPreflight(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, nil, nil)
	writeListedModels(t, fx.fakeDir, fx.idA, fx.idA, fx.idB)
	replacePoolModels(t, fx, []string{fx.idA})
	e := newEngine(fx)
	err := e.Validate(context.Background())
	if err == nil {
		t.Fatal("duplicate agent models IDs must fail preflight")
	}
}

func TestWriteExistingCursorWorktreeFailsPreflight(t *testing.T) {
	fx := setupFixture(t, config.ModeWrite, true, nil, nil)
	replacePoolModels(t, fx, []string{fx.idA})
	name := "cursor-poolaaaaaaaaaaaaaaaa"
	dest := expectedWriteWorktreePath(t, fx.repo, fx.home, name)
	if err := os.MkdirAll(dest, 0o755); err != nil {
		t.Fatal(err)
	}
	e := newEngine(fx)
	out, err := e.Run(context.Background(), []byte(fx.prompt))
	if err == nil {
		t.Fatal("existing Cursor worktree path must fail preflight")
	}
	if len(out.Launched) != 0 {
		t.Fatalf("must not launch models: %+v", out)
	}
	if _, statErr := os.Stat(filepath.Join(fx.fakeDir, "launch-order.txt")); statErr == nil {
		t.Fatal("existing worktree must not start agent")
	}
	if n := gitWorktreeCount(t, fx.repo); n != 1 {
		t.Fatalf("must not git worktree add, count=%d", n)
	}
}

func TestWriteNestedCwdUsesRepoToplevelBasename(t *testing.T) {
	fx := setupFixture(t, config.ModeWrite, true, nil, nil)
	replacePoolModels(t, fx, []string{fx.idA})
	raw, _ := json.Marshal(map[string]string{fx.idA: "mutate_then_429"})
	_ = os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600)
	nested := filepath.Join(fx.repo, "nested")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	e := newEngine(fx)
	e.CWD = nested
	out, _ := e.Run(context.Background(), []byte(fx.prompt))
	if !out.MutationObserved {
		t.Fatalf("mutation at Cursor worktree path not observed: %+v", out)
	}
	top := gitToplevelForTest(t, nested)
	name := "cursor-poolaaaaaaaaaaaaaaaa"
	want := filepath.Join(fx.home, ".cursor", "worktrees", filepath.Base(top), name)
	if _, err := os.Stat(filepath.Join(want, "pool-mutated.txt")); err != nil {
		t.Fatalf("expected %s: %v", want, err)
	}
	wrong := filepath.Join(fx.home, ".cursor", "worktrees", "nested", name)
	if _, err := os.Stat(wrong); err == nil {
		t.Fatal("used cwd basename instead of git toplevel basename")
	}
	inv := readInvocations(t, fx.fakeDir)
	if len(inv) != 1 || inv[0].CWD != top {
		t.Fatalf("child Dir = %v want toplevel %q", inv, top)
	}
}

func TestEngineDoesNotGitWorktreeAdd(t *testing.T) {
	body, err := os.ReadFile(filepath.Join(moduleRoot(), "internal", "engine", "engine.go"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), `"worktree", "add"`) || strings.Contains(string(body), "worktree add") || strings.Contains(string(body), "git worktree") {
		t.Fatal("controller must not invoke git worktree add")
	}
}

func TestZeroOrchestrationIDFailsPreflight(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, nil, nil)
	replacePoolModels(t, fx, []string{fx.idA})
	e := newEngine(fx)
	e.NewID = func() string { return "0000000000000000" }
	out, err := e.Run(context.Background(), []byte(fx.prompt))
	if err == nil {
		t.Fatal("all-zero orchestration id must fail preflight")
	}
	if len(out.Launched) != 0 {
		t.Fatalf("must not start models: %+v", out)
	}
	if _, statErr := os.Stat(filepath.Join(fx.fakeDir, "launch-order.txt")); statErr == nil {
		t.Fatal("zero id continued into agent launch")
	}
}

func TestEmptyOrchestrationIDFailsPreflight(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, nil, nil)
	replacePoolModels(t, fx, []string{fx.idA})
	e := newEngine(fx)
	e.NewID = func() string { return "" }
	if _, err := e.Run(context.Background(), []byte(fx.prompt)); err == nil {
		t.Fatal("empty orchestration id must fail preflight")
	}
	if _, statErr := os.Stat(filepath.Join(fx.fakeDir, "launch-order.txt")); statErr == nil {
		t.Fatal("empty id continued into agent launch")
	}
}

func TestNewOrchestrationIDRandFailure(t *testing.T) {
	orig := readRandom
	t.Cleanup(func() { readRandom = orig })
	readRandom = func([]byte) (int, error) { return 0, errors.New("rand fail") }
	id, err := NewOrchestrationID()
	if err == nil || id != "" {
		t.Fatalf("rand failure must not continue, id=%q err=%v", id, err)
	}
}

func TestNewOrchestrationIDRejectsAllZero(t *testing.T) {
	orig := readRandom
	t.Cleanup(func() { readRandom = orig })
	readRandom = func(b []byte) (int, error) {
		for i := range b {
			b[i] = 0
		}
		return len(b), nil
	}
	id, err := NewOrchestrationID()
	if err == nil || id != "" || id == "0000000000000000" {
		t.Fatalf("all-zero id must not continue, id=%q err=%v", id, err)
	}
}

func TestJournalAppendFailureStopsNextModel(t *testing.T) {
	fx := setupFixture(t, config.ModeAsk, false, nil, nil)
	raw, _ := json.Marshal(map[string]string{fx.idA: "preoutput_429", fx.idB: "success"})
	_ = os.WriteFile(filepath.Join(fx.fakeDir, "behavior.json"), raw, 0o600)
	e := newEngine(fx)
	e.appendJournal = func(journal.Record) error {
		return errors.New("journal append failed")
	}
	out, err := e.Run(context.Background(), []byte(fx.prompt))
	if err == nil {
		t.Fatal("journal append failure must not be silent success")
	}
	if out.Success {
		t.Fatalf("journal failure reported success: %+v", out)
	}
	if out.ErrorCategory != classify.CatConfig {
		t.Fatalf("want config error, got %+v", out)
	}
	launches := readLines(t, filepath.Join(fx.fakeDir, "launch-order.txt"))
	if len(launches) != 1 || launches[0] != fx.idA {
		t.Fatalf("must not start next model after journal failure: %v", launches)
	}
}

func writeListedModels(t *testing.T, dir string, ids ...string) {
	t.Helper()
	var b strings.Builder
	for i, id := range ids {
		fmt.Fprintf(&b, "%s - fixture-%d\n", id, i)
	}
	if err := os.WriteFile(filepath.Join(dir, "models.txt"), []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
}

func gitToplevelForTest(t *testing.T, dir string) string {
	t.Helper()
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git rev-parse --show-toplevel: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func expectedWriteWorktreePath(t *testing.T, repo, home, name string) string {
	t.Helper()
	top := gitToplevelForTest(t, repo)
	return filepath.Join(home, ".cursor", "worktrees", filepath.Base(top), name)
}

func gitWorktreeCount(t *testing.T, repo string) int {
	t.Helper()
	cmd := exec.Command("git", "worktree", "list", "--porcelain")
	cmd.Dir = repo
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	n := 0
	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "worktree ") {
			n++
		}
	}
	return n
}

func names(entries []os.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		out = append(out, entry.Name())
	}
	return out
}
