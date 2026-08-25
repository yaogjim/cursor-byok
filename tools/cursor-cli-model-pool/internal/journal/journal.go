package journal

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"cursor-cli-model-pool/internal/paths"
)

const SchemaVersion = 1
const Unknown = "unknown"

type Record struct {
	SchemaVersion    int    `json:"schema_version"`
	OrchestrationID  string `json:"orchestration_id"`
	ModelID          string `json:"model_id"`
	ModelIndex       int    `json:"model_index"`
	Phase            string `json:"phase"`
	SessionID        string `json:"session_id"`
	RequestID        string `json:"request_id"`
	ExitCode         *int   `json:"exit_code"`
	ErrorCategory    string `json:"error_category"`
	OutputObserved   bool   `json:"output_observed"`
	MutationObserved bool   `json:"mutation_observed"`
	WorktreeName     string `json:"worktree_name"`
	Time             string `json:"time"`
}

type Writer struct {
	mu   sync.Mutex
	file *os.File
}

func Open() (*Writer, error) {
	path, err := paths.JournalPath()
	if err != nil {
		return nil, err
	}
	return OpenPath(path)
}

func OpenPath(path string) (*Writer, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开 journal 失败")
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("设置 journal 权限失败")
	}
	return &Writer{file: file}, nil
}

func (w *Writer) Append(rec Record) error {
	if w == nil {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if rec.SchemaVersion == 0 {
		rec.SchemaVersion = SchemaVersion
	}
	rec.OrchestrationID = nonEmpty(rec.OrchestrationID)
	rec.ModelID = nonEmpty(rec.ModelID)
	rec.Phase = nonEmpty(rec.Phase)
	rec.SessionID = nonEmpty(rec.SessionID)
	rec.RequestID = nonEmpty(rec.RequestID)
	rec.ErrorCategory = nonEmpty(rec.ErrorCategory)
	rec.WorktreeName = nonEmpty(rec.WorktreeName)
	if rec.Time == "" {
		rec.Time = time.Now().UTC().Format(time.RFC3339)
	}
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("编码 journal 失败")
	}
	if _, err := w.file.Write(append(line, '\n')); err != nil {
		return fmt.Errorf("写入 journal 失败")
	}
	return w.file.Sync()
}

func (w *Writer) Close() error {
	if w == nil || w.file == nil {
		return nil
	}
	return w.file.Close()
}

func nonEmpty(value string) string {
	if value == "" {
		return Unknown
	}
	return value
}
