package engine

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"cursor-cli-model-pool/internal/byok"
	"cursor-cli-model-pool/internal/classify"
	"cursor-cli-model-pool/internal/config"
	"cursor-cli-model-pool/internal/journal"
	"cursor-cli-model-pool/internal/paths"
	"cursor-cli-model-pool/internal/snapshot"
)

type Engine struct {
	Stdout        io.Writer
	Stderr        io.Writer
	Jitter        func() time.Duration
	Sleep         func(context.Context, time.Duration) error
	NewID         func() string
	CWD           string
	platformOK    func() bool
	now           func() time.Time
	appendJournal func(journal.Record) error
}

type Outcome struct {
	Phase            classify.Phase
	ModelID          string
	ModelIndex       int
	Launched         []string
	Success          bool
	NeedsReview      bool
	ErrorCategory    classify.Category
	OutputObserved   bool
	MutationObserved bool
	WorktreeName     string
	ExitCode         int
	SessionID        string
	RequestID        string
}

type DryRunPlan struct {
	OrchestrationID string
	WorktreeName    string
	Models          []ModelPlan
}

type ModelPlan struct {
	ModelID string
	Argv    []string
}

func (e *Engine) Validate(ctx context.Context) error {
	_, _, err := e.preflight(ctx)
	return err
}

func (e *Engine) DryRun(ctx context.Context) (DryRunPlan, error) {
	cfg, orchID, err := e.preflight(ctx)
	if err != nil {
		return DryRunPlan{}, err
	}
	plan := DryRunPlan{OrchestrationID: orchID}
	if cfg.Mode == config.ModeWrite {
		plan.WorktreeName = cfg.WorktreeNamePrefix + orchID
	}
	for _, model := range cfg.Models {
		plan.Models = append(plan.Models, ModelPlan{
			ModelID: model,
			Argv:    BuildArgv(cfg.AgentPath, cfg.Endpoint, model, cfg.Mode, plan.WorktreeName),
		})
	}
	return plan, nil
}

func (e *Engine) Run(ctx context.Context, prompt []byte) (Outcome, error) {
	cfg, orchID, err := e.preflight(ctx)
	if err != nil {
		outcome := Outcome{Phase: classify.PhaseTerminal, ErrorCategory: classify.CatConfig}
		if errors.Is(err, errUnsupported) {
			outcome.ErrorCategory = classify.CatUnsupported
		}
		if orchID != "" {
			e.writeJournal(nil, journal.Record{
				OrchestrationID: orchID,
				Phase:           string(classify.PhaseTerminal),
				ErrorCategory:   string(outcome.ErrorCategory),
			})
		}
		return outcome, err
	}
	jw, err := journal.Open()
	if err != nil {
		return Outcome{Phase: classify.PhaseTerminal, ErrorCategory: classify.CatConfig}, err
	}
	defer jw.Close()

	worktreeName := ""
	worktreePath := ""
	childDir := e.cwd()
	var baseline snapshot.Snapshot
	if cfg.Mode == config.ModeWrite {
		name, path, top, err := e.writeWorktreeTarget(cfg, orchID)
		if err != nil {
			fail := Outcome{Phase: classify.PhaseTerminal, ErrorCategory: classify.CatConfig, WorktreeName: name}
			if _, jerr := e.commitJournal(jw, fail, orchID, 0, "", name); jerr != nil {
				return fail, jerr
			}
			return fail, err
		}
		exists, err := pathExists(path)
		if err != nil || exists {
			if err == nil {
				err = fmt.Errorf("worktree 名称冲突")
			}
			fail := Outcome{Phase: classify.PhaseTerminal, ErrorCategory: classify.CatConfig, WorktreeName: name}
			if _, jerr := e.commitJournal(jw, fail, orchID, 0, "", name); jerr != nil {
				return fail, jerr
			}
			return fail, err
		}
		worktreeName = name
		worktreePath = path
		childDir = top
		snap, err := snapshot.Capture(path)
		if err != nil {
			fail := Outcome{Phase: classify.PhaseTerminal, ErrorCategory: classify.CatConfig, WorktreeName: worktreeName}
			if _, jerr := e.commitJournal(jw, fail, orchID, 0, "", worktreeName); jerr != nil {
				return fail, jerr
			}
			return fail, err
		}
		baseline = snap
	}

	var launched []string
	last := Outcome{Phase: classify.PhasePreflight, WorktreeName: worktreeName}
	for i, model := range cfg.Models {
		if i > 0 {
			delay := e.jitter()
			if err := e.sleep(ctx, delay); err != nil {
				last = last.withCancel()
				last.Launched = launched
				last, jerr := e.commitJournal(jw, last, orchID, i, model, worktreeName)
				if jerr != nil {
					return last, jerr
				}
				return last, err
			}
		}
		if ctx.Err() != nil {
			last = last.withCancel()
			last.Launched = launched
			last, jerr := e.commitJournal(jw, last, orchID, i, model, worktreeName)
			if jerr != nil {
				return last, jerr
			}
			return last, ctx.Err()
		}
		launched = append(launched, model)
		last = e.runModel(ctx, cfg, prompt, i, model, worktreeName, worktreePath, childDir, baseline)
		last.Launched = append([]string(nil), launched...)
		last, jerr := e.commitJournal(jw, last, orchID, i, model, worktreeName)
		if jerr != nil {
			return last, jerr
		}
		if last.Success {
			return last, nil
		}
		if classify.Switchable(last.Phase, last.ErrorCategory) && i < len(cfg.Models)-1 {
			continue
		}
		return last, categoryError(last)
	}
	return last, categoryError(last)
}

func (e *Engine) preflight(ctx context.Context) (config.Config, string, error) {
	orchID, err := e.newID()
	if err != nil {
		return config.Config{}, "", err
	}
	if !e.supported() {
		return config.Config{}, orchID, errUnsupported
	}
	cfg, err := config.Load()
	if err != nil {
		return config.Config{}, orchID, err
	}
	physical, err := byok.Load()
	if err != nil {
		return config.Config{}, orchID, err
	}
	listed, err := listAgentModels(ctx, cfg.AgentPath, cfg.Endpoint)
	if err != nil {
		return config.Config{}, orchID, err
	}
	if err := CrossCheck(cfg.Models, listed, physical); err != nil {
		return config.Config{}, orchID, err
	}
	if cfg.Mode == config.ModeWrite {
		_, path, _, err := e.writeWorktreeTarget(cfg, orchID)
		if err != nil {
			return config.Config{}, orchID, err
		}
		exists, err := pathExists(path)
		if err != nil {
			return config.Config{}, orchID, err
		}
		if exists {
			return config.Config{}, orchID, fmt.Errorf("worktree 名称冲突")
		}
	}
	return cfg, orchID, nil
}

func CrossCheck(pool []string, listed []string, physical []byok.Physical) error {
	listedCount := map[string]int{}
	for _, id := range listed {
		listedCount[strings.ToLower(strings.TrimSpace(id))]++
	}
	for _, n := range listedCount {
		if n > 1 {
			return fmt.Errorf("agent models 模型 ID 重复")
		}
	}
	physCount := map[string]int{}
	physByID := map[string]byok.Physical{}
	for _, item := range physical {
		physCount[item.ID]++
		physByID[item.ID] = item
	}
	for _, n := range physCount {
		if n > 1 {
			return fmt.Errorf("派生渠道 ID 重复")
		}
	}
	for _, id := range pool {
		if listedCount[id] != 1 {
			return fmt.Errorf("模型未在 agent models 中精确唯一匹配")
		}
		if physCount[id] != 1 {
			return fmt.Errorf("模型未在 BYOK 配置中精确唯一匹配")
		}
		if physByID[id].FallbackEnabled {
			return fmt.Errorf("禁止引用 providerFallback.enabled=true 的逻辑适配器")
		}
	}
	return nil
}

func BuildArgv(agentPath, endpoint, model, mode, worktreeName string) []string {
	args := []string{
		agentPath,
		"--print",
		"--output-format", "stream-json",
		"--endpoint", endpoint,
		"--model", model,
	}
	switch mode {
	case config.ModeAsk, config.ModePlan:
		args = append(args, "--mode", mode)
	case config.ModeWrite:
		args = append(args, "--worktree", worktreeName)
	}
	return args
}

func listAgentModels(ctx context.Context, agentPath, endpoint string) ([]string, error) {
	cmd := exec.CommandContext(ctx, agentPath, "--endpoint", endpoint, "models")
	cmd.Stdin = nil
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = io.Discard
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("agent models 预检失败")
	}
	ids := parseModelList(stdout.Bytes())
	if len(ids) == 0 {
		return nil, fmt.Errorf("agent models 未列出物理模型")
	}
	return ids, nil
}

func parseModelList(raw []byte) []string {
	var ids []string
	add := func(id string) {
		id = strings.ToLower(strings.TrimSpace(id))
		if !isHex16(id) {
			return
		}
		ids = append(ids, id)
	}
	trimmed := bytes.TrimSpace(raw)
	var jsonIDs []string
	if json.Unmarshal(trimmed, &jsonIDs) == nil {
		for _, id := range jsonIDs {
			add(id)
		}
		if len(ids) > 0 {
			return ids
		}
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		if m := modelListLinePattern.FindStringSubmatch(line); len(m) == 2 {
			add(m[1])
		}
	}
	return ids
}

func isHex16(id string) bool {
	if len(id) != 16 {
		return false
	}
	for _, r := range id {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return false
		}
	}
	return true
}

func (e *Engine) runModel(ctx context.Context, cfg config.Config, prompt []byte, index int, model, worktreeName, worktreePath, childDir string, baseline snapshot.Snapshot) Outcome {
	out := Outcome{
		Phase:        classify.PhaseLaunching,
		ModelID:      model,
		ModelIndex:   index,
		WorktreeName: worktreeName,
		SessionID:    journal.Unknown,
		RequestID:    journal.Unknown,
	}
	args := BuildArgv(cfg.AgentPath, cfg.Endpoint, model, cfg.Mode, worktreeName)
	cmd := exec.Command(args[0], args[1:]...)
	if childDir != "" {
		cmd.Dir = childDir
	} else {
		cmd.Dir = e.cwd()
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		out.Phase = classify.PhaseLaunching
		out.ErrorCategory = classify.CatSpawnFailure
		return out
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		out.Phase = classify.PhaseLaunching
		out.ErrorCategory = classify.CatSpawnFailure
		return out
	}
	if e.stderr() != nil {
		cmd.Stderr = e.stderr()
	} else {
		cmd.Stderr = io.Discard
	}
	setProcAttr(cmd)
	var mutationMonitor *snapshot.Monitor
	if cfg.Mode == config.ModeWrite {
		mutationMonitor, err = snapshot.Watch(worktreePath)
		if err != nil {
			out.Phase = classify.PhaseLaunching
			out.ErrorCategory = classify.CatConfig
			return out
		}
	}
	if err := cmd.Start(); err != nil {
		if mutationMonitor != nil {
			_, _ = mutationMonitor.Close()
		}
		out.Phase = classify.PhaseLaunching
		out.ErrorCategory = classify.CatSpawnFailure
		return out
	}
	out.Phase = classify.PhasePreOutput
	go func() {
		_, _ = stdin.Write(prompt)
		_ = stdin.Close()
	}()

	stopDone := make(chan struct{})
	stopFinished := make(chan struct{})
	go func() {
		defer close(stopFinished)
		select {
		case <-ctx.Done():
			stopChild(cmd)
		case <-stopDone:
		}
	}()
	consumeErr := e.consumeStdout(stdout, &out)
	waitErr := cmd.Wait()
	close(stopDone)
	<-stopFinished
	if mutationMonitor != nil {
		observed, failed := mutationMonitor.Close()
		if observed || failed {
			out.MutationObserved = true
			if out.Phase == classify.PhasePreOutput || out.Phase == classify.PhaseLaunching || out.Phase == classify.PhaseObserved {
				out.Phase = classify.PhaseMutated
			}
		}
	}
	if cmd.ProcessState != nil {
		out.ExitCode = cmd.ProcessState.ExitCode()
	}
	if ctx.Err() != nil {
		out.ErrorCategory = classify.CatCancel
		out = e.finalizeMutation(cfg, worktreePath, baseline, out)
		if out.Phase == classify.PhaseObserved || out.Phase == classify.PhaseMutated {
			out.Phase = classify.PhaseNeedsReview
			out.NeedsReview = true
		} else {
			out.Phase = classify.PhaseTerminal
		}
		return out
	}
	if consumeErr != nil {
		out.ErrorCategory = classify.CatNDJSON
	}
	out = e.finalizeMutation(cfg, worktreePath, baseline, out)
	failed := waitErr != nil || out.ExitCode != 0 || out.ErrorCategory != classify.CatNone && out.ErrorCategory != ""
	if out.ErrorCategory == classify.CatNone && waitErr != nil {
		out.ErrorCategory = classify.CatUnknown
		failed = true
	}
	if !failed && consumeErr == nil && out.ErrorCategory == classify.CatNone {
		out.Success = true
		out.Phase = classify.PhaseTerminal
		return out
	}
	if out.ErrorCategory == classify.CatNone {
		out.ErrorCategory = classify.CatUnknown
	}
	if out.Phase == classify.PhaseObserved || out.Phase == classify.PhaseMutated {
		out.Phase = classify.PhaseNeedsReview
		out.NeedsReview = true
		return out
	}
	if out.Phase == classify.PhasePreOutput || out.Phase == classify.PhaseLaunching {
		return out
	}
	out.Phase = classify.PhaseTerminal
	return out
}

func (e *Engine) consumeStdout(r io.Reader, out *Outcome) error {
	reader := io.Reader(r)
	if e.stdout() != nil {
		reader = io.TeeReader(r, e.stdout())
	}
	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		ev, err := classify.ParseLine(line)
		if err != nil {
			out.ErrorCategory = classify.CatNDJSON
			return err
		}
		if ev.SessionID != "" {
			out.SessionID = ev.SessionID
		}
		if ev.RequestID != "" {
			out.RequestID = ev.RequestID
		}
		if classify.ClosesSwitchWindow(ev) {
			out.Phase = classify.PhaseObserved
			out.OutputObserved = true
		}
		if ev.HasResult {
			if ev.Category != classify.CatNone {
				out.ErrorCategory = ev.Category
			} else if ev.Subtype == "error" || ev.Subtype == "error_during_execution" {
				out.ErrorCategory = classify.CatUnknown
			}
		}
	}
	if err := scanner.Err(); err != nil {
		out.ErrorCategory = classify.CatNDJSON
		return err
	}
	return nil
}

func (e *Engine) finalizeMutation(cfg config.Config, worktreePath string, baseline snapshot.Snapshot, out Outcome) Outcome {
	if cfg.Mode != config.ModeWrite || worktreePath == "" {
		return out
	}
	after, err := snapshot.Capture(worktreePath)
	if err != nil || snapshot.Changed(baseline, after) {
		out.MutationObserved = true
		if out.Phase == classify.PhasePreOutput || out.Phase == classify.PhaseLaunching || out.Phase == classify.PhaseObserved {
			out.Phase = classify.PhaseMutated
		}
	}
	return out
}

func (e *Engine) record(orchID string, out Outcome, index int, model, worktreeName string) journal.Record {
	code := out.ExitCode
	var codePtr *int
	if out.Phase == classify.PhaseTerminal || out.Phase == classify.PhaseNeedsReview || out.ErrorCategory != "" {
		codePtr = &code
	}
	cat := string(out.ErrorCategory)
	if cat == "" {
		cat = journal.Unknown
	}
	return journal.Record{
		OrchestrationID:  orchID,
		ModelID:          model,
		ModelIndex:       index,
		Phase:            string(out.Phase),
		SessionID:        nonUnknown(out.SessionID),
		RequestID:        nonUnknown(out.RequestID),
		ExitCode:         codePtr,
		ErrorCategory:    cat,
		OutputObserved:   out.OutputObserved,
		MutationObserved: out.MutationObserved,
		WorktreeName:     nonUnknown(worktreeName),
		Time:             e.nowTime().UTC().Format(time.RFC3339),
	}
}

func (e *Engine) writeJournal(w *journal.Writer, rec journal.Record) {
	_ = e.appendRecord(w, rec)
}

func (e *Engine) appendRecord(w *journal.Writer, rec journal.Record) error {
	if e != nil && e.appendJournal != nil {
		return e.appendJournal(rec)
	}
	if w == nil {
		opened, err := journal.Open()
		if err != nil {
			return err
		}
		defer opened.Close()
		return opened.Append(rec)
	}
	return w.Append(rec)
}

func (e *Engine) commitJournal(w *journal.Writer, last Outcome, orchID string, index int, model, worktreeName string) (Outcome, error) {
	if err := e.appendRecord(w, e.record(orchID, last, index, model, worktreeName)); err != nil {
		last.Success = false
		last.NeedsReview = false
		last.ErrorCategory = classify.CatConfig
		last.Phase = classify.PhaseTerminal
		return last, err
	}
	return last, nil
}

func (o Outcome) withCancel() Outcome {
	o.ErrorCategory = classify.CatCancel
	if o.Phase == classify.PhaseObserved || o.Phase == classify.PhaseMutated {
		o.Phase = classify.PhaseNeedsReview
		o.NeedsReview = true
	} else {
		o.Phase = classify.PhaseTerminal
	}
	return o
}

func (e *Engine) supported() bool {
	if e != nil && e.platformOK != nil {
		return e.platformOK()
	}
	return PlatformSupported()
}

func (e *Engine) cwd() string {
	if e != nil && e.CWD != "" {
		return e.CWD
	}
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	return dir
}

func (e *Engine) stdout() io.Writer {
	if e != nil && e.Stdout != nil {
		return e.Stdout
	}
	return os.Stdout
}

func (e *Engine) stderr() io.Writer {
	if e != nil && e.Stderr != nil {
		return e.Stderr
	}
	return os.Stderr
}

func (e *Engine) jitter() time.Duration {
	if e != nil && e.Jitter != nil {
		return e.Jitter()
	}
	return FullJitter()
}

func (e *Engine) sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	if e != nil && e.Sleep != nil {
		return e.Sleep(ctx, d)
	}
	return SleepCtx(ctx, d)
}

func (e *Engine) newID() (string, error) {
	if e != nil && e.NewID != nil {
		id := strings.ToLower(strings.TrimSpace(e.NewID()))
		if !validOrchestrationID(id) {
			return "", errOrchestrationID
		}
		return id, nil
	}
	id, err := NewOrchestrationID()
	if err != nil {
		return "", errOrchestrationID
	}
	return id, nil
}

func (e *Engine) nowTime() time.Time {
	if e != nil && e.now != nil {
		return e.now()
	}
	return time.Now()
}

func FullJitter() time.Duration {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return 0
	}
	u := float64(binary.BigEndian.Uint64(buf[:])) / float64(^uint64(0))
	return time.Duration(u * float64(time.Second))
}

func SleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func NewOrchestrationID() (string, error) {
	var buf [8]byte
	if _, err := readRandom(buf[:]); err != nil {
		return "", err
	}
	id := hex.EncodeToString(buf[:])
	if !validOrchestrationID(id) {
		return "", errOrchestrationID
	}
	return id, nil
}

func validOrchestrationID(id string) bool {
	return isHex16(id) && id != "0000000000000000"
}

func gitToplevel(dir string) (string, error) {
	cmd := exec.Command("git", "rev-parse", "--show-toplevel")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("write 模式需要 git 仓库")
	}
	top := strings.TrimSpace(string(out))
	if top == "" {
		return "", fmt.Errorf("write 模式需要 git 仓库")
	}
	return top, nil
}

func (e *Engine) writeWorktreeTarget(cfg config.Config, orchID string) (name, path, toplevel string, err error) {
	if cfg.Mode != config.ModeWrite {
		return "", "", "", nil
	}
	top, err := gitToplevel(e.cwd())
	if err != nil {
		return "", "", "", err
	}
	name = cfg.WorktreeNamePrefix + orchID
	path, err = paths.CursorWorktreePath(filepath.Base(top), name)
	if err != nil {
		return name, "", "", err
	}
	return name, path, top, nil
}

func pathExists(path string) (bool, error) {
	_, err := os.Lstat(path)
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

func nonUnknown(value string) string {
	if strings.TrimSpace(value) == "" {
		return journal.Unknown
	}
	return value
}

func categoryError(out Outcome) error {
	if out.Success {
		return nil
	}
	if out.ErrorCategory == classify.CatCancel {
		return context.Canceled
	}
	return fmt.Errorf("orchestration ended: %s", out.ErrorCategory)
}

var (
	errUnsupported       = errors.New("当前平台不支持独立进程组，预检失败")
	errOrchestrationID   = errors.New("orchestration id 生成失败")
	readRandom           = rand.Read
	modelListLinePattern = regexp.MustCompile(`(?i)^(?:[*+-]\s+)?([0-9a-f]{16})(?:\s+-\s+.*)?$`)
)
