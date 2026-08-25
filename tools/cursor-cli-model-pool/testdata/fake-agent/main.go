package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

type invocation struct {
	Argv  []string `json:"argv"`
	Stdin string   `json:"stdin"`
	CWD   string   `json:"cwd"`
	Model string   `json:"model"`
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "--ignore-term-hang" {
		hangIgnoreTerm()
		return
	}
	dir := os.Getenv("FAKE_AGENT_DIR")
	if dir == "" {
		fmt.Fprintln(os.Stderr, "FAKE_AGENT_DIR missing")
		os.Exit(2)
	}
	args := os.Args[1:]
	if isModels(args) {
		listModels(dir)
		return
	}
	model := flagValue(args, "--model")
	stdin, _ := io.ReadAll(os.Stdin)
	record(dir, invocation{Argv: os.Args, Stdin: string(stdin), CWD: mustCwd(), Model: model})
	appendLaunch(dir, model)
	recordWorktreePresence(dir, args)
	runBehavior(lookupBehavior(dir, model), args, dir)
}

func isModels(args []string) bool {
	skip := false
	for _, arg := range args {
		if skip {
			skip = false
			continue
		}
		if strings.HasPrefix(arg, "--") {
			if arg == "--endpoint" || arg == "--model" || arg == "--mode" || arg == "--worktree" || arg == "--output-format" {
				skip = true
			}
			continue
		}
		if arg == "models" {
			return true
		}
	}
	return false
}

func listModels(dir string) {
	data, err := os.ReadFile(filepath.Join(dir, "models.txt"))
	if err != nil {
		os.Exit(1)
	}
	os.Stdout.Write(data)
}

func lookupBehavior(dir, model string) string {
	raw, err := os.ReadFile(filepath.Join(dir, "behavior.json"))
	if err != nil {
		return "success"
	}
	var table map[string]string
	if err := json.Unmarshal(raw, &table); err != nil {
		return "success"
	}
	if b, ok := table[model]; ok {
		return b
	}
	if b, ok := table["*"]; ok {
		return b
	}
	return "success"
}

func record(dir string, inv invocation) {
	f, err := os.OpenFile(filepath.Join(dir, "invocations.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(inv)
	_, _ = f.Write(append(line, '\n'))
}

func appendLaunch(dir, model string) {
	f, err := os.OpenFile(filepath.Join(dir, "launch-order.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = fmt.Fprintln(f, model)
}

func runBehavior(behavior string, args []string, dir string) {
	switch behavior {
	case "preoutput_429":
		emit(`{"type":"system","subtype":"init","session_id":"sess-1"}`)
		emit(`{"type":"user"}`)
		emit(`{"type":"result","subtype":"error","error_category":"http_429","http_status":429}`)
		os.Exit(1)
	case "preoutput_transport":
		emit(`{"type":"system","subtype":"init","session_id":"sess-1"}`)
		emit(`{"type":"result","subtype":"error","error_category":"transport"}`)
		os.Exit(1)
	case "preoutput_502":
		emit(`{"type":"system","subtype":"init"}`)
		emit(`{"type":"result","subtype":"error","error_category":"http_502"}`)
		os.Exit(1)
	case "preoutput_503":
		emit(`{"type":"result","subtype":"error","http_status":503}`)
		os.Exit(1)
	case "preoutput_504":
		emit(`{"type":"result","subtype":"error","error":{"category":"http_504"}}`)
		os.Exit(1)
	case "retry_then_429":
		emit(`{"type":"system","subtype":"init"}`)
		emit(`{"type":"user"}`)
		emit(`{"type":"retry"}`)
		emit(`{"type":"connection"}`)
		emit(`{"type":"result","subtype":"error","error_category":"http_429"}`)
		os.Exit(1)
	case "http_500":
		emit(`{"type":"system","subtype":"init"}`)
		emit(`{"type":"result","subtype":"error","error_category":"http_500","http_status":500}`)
		os.Exit(1)
	case "http_401":
		emit(`{"type":"result","subtype":"error","http_status":401}`)
		os.Exit(1)
	case "untyped":
		emit(`{"type":"system","subtype":"init"}`)
		emit(`{"type":"result","subtype":"error","error":"HTTP 429 rate limited"}`)
		os.Exit(1)
	case "stderr_429":
		fmt.Fprintln(os.Stderr, "HTTP 429 rate limit please retry")
		emit(`{"type":"result","subtype":"error","message":"look at stderr"}`)
		os.Exit(1)
	case "thinking_fail":
		emit(`{"type":"system","subtype":"init"}`)
		emit(`{"type":"user"}`)
		emit(`{"type":"thinking"}`)
		emit(`{"type":"result","subtype":"error","error_category":"http_429"}`)
		os.Exit(1)
	case "assistant_fail":
		emit(`{"type":"assistant"}`)
		emit(`{"type":"result","subtype":"error","error_category":"http_429"}`)
		os.Exit(1)
	case "tool_fail":
		emit(`{"type":"tool_call","subtype":"started"}`)
		emit(`{"type":"result","subtype":"error","error_category":"http_429"}`)
		os.Exit(1)
	case "mutate_then_429":
		mutate(args)
		emit(`{"type":"system","subtype":"init"}`)
		emit(`{"type":"result","subtype":"error","error_category":"http_429"}`)
		os.Exit(1)
	case "empty_worktree_then_429":
		mutateEmpty(args, false)
		emit(`{"type":"result","subtype":"error","error_category":"http_429"}`)
		os.Exit(1)
	case "create_delete_worktree_then_429":
		mutateEmpty(args, true)
		emit(`{"type":"result","subtype":"error","error_category":"http_429"}`)
		os.Exit(1)
	case "hang_parent_dies":
		hangParentDies(dir)
	case "success":
		emit(`{"type":"system","subtype":"init","session_id":"sess-ok"}`)
		emit(`{"type":"user"}`)
		emit(`{"type":"thinking"}`)
		emit(`{"type":"assistant"}`)
		emit(`{"type":"result","subtype":"success"}`)
		os.Exit(0)
	case "success_200":
		emit(`{"type":"system","subtype":"init","session_id":"sess-ok"}`)
		emit(`{"type":"result","subtype":"success","http_status":200}`)
		os.Exit(0)
	case "hang":
		hang(dir)
	default:
		emit(`{"type":"result","subtype":"error","error_category":"unknown"}`)
		os.Exit(1)
	}
}

func mutate(args []string) {
	dest := cursorWorktreePath(flagValue(args, "--worktree"))
	if dest == "" {
		return
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dest, "pool-mutated.txt"), []byte("changed"), 0o644)
}

func mutateEmpty(args []string, remove bool) {
	dest := cursorWorktreePath(flagValue(args, "--worktree"))
	if dest == "" {
		return
	}
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return
	}
	time.Sleep(20 * time.Millisecond)
	if remove {
		_ = os.RemoveAll(dest)
		time.Sleep(20 * time.Millisecond)
	}
}

func recordWorktreePresence(dir string, args []string) {
	name := flagValue(args, "--worktree")
	if name == "" {
		return
	}
	dest := cursorWorktreePath(name)
	state := "absent"
	if _, err := os.Lstat(dest); err == nil {
		state = "exists"
	}
	_ = os.WriteFile(filepath.Join(dir, "worktree-at-start.txt"), []byte(state+"\n"+dest+"\n"), 0o600)
}

func cursorWorktreePath(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}
	top := gitToplevel()
	base := filepath.Base(top)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return ""
	}
	return filepath.Join(home, ".cursor", "worktrees", base, name)
}

func gitToplevel() string {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return mustCwd()
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return mustCwd()
	}
	return top
}

func hang(dir string) {
	signal.Ignore(syscall.SIGTERM)
	if bin, err := exec.LookPath("sleep"); err == nil {
		cmd := exec.Command(bin, "30")
		if err := cmd.Start(); err == nil && cmd.Process != nil {
			_ = os.WriteFile(filepath.Join(dir, "grandchild.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
		}
	}
	emit(`{"type":"system","subtype":"init","session_id":"hang"}`)
	time.Sleep(30 * time.Second)
}

func hangParentDies(dir string) {
	bin, err := os.Executable()
	if err != nil {
		bin = os.Args[0]
	}
	cmd := exec.Command(bin, "--ignore-term-hang")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		os.Exit(1)
	}
	_ = os.WriteFile(filepath.Join(dir, "grandchild.pid"), []byte(strconv.Itoa(cmd.Process.Pid)), 0o600)
	emit(`{"type":"system","subtype":"init","session_id":"hang-parent"}`)
	time.Sleep(30 * time.Second)
}

func hangIgnoreTerm() {
	signal.Ignore(syscall.SIGTERM)
	signal.Ignore(syscall.SIGINT)
	select {}
}

func emit(line string) {
	fmt.Println(line)
	_ = os.Stdout.Sync()
}

func flagValue(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

func mustCwd() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}
