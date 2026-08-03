package gui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"cursor-log-analyzer/internal/analyze"
	"cursor-log-analyzer/internal/project"
	"cursor-log-analyzer/internal/savedquery"
	"cursor-log-analyzer/internal/workspace"

	"github.com/wailsapp/wails/v3/pkg/application"
)

const (
	payloadReadLimit  = 2 << 20
	clientDataDirName = ".cursor-local-assistant-v2"
	clientLogsDirName = "logs"
)

type OpenRequest struct {
	Input              string `json:"input"`
	Baseline           string `json:"baseline"`
	AllowUnknownSchema bool   `json:"allow_unknown_schema"`
}

type State struct {
	Opened      bool            `json:"opened"`
	Input       string          `json:"input,omitempty"`
	Baseline    string          `json:"baseline,omitempty"`
	Summary     analyze.Summary `json:"summary"`
	HasBaseline bool            `json:"has_baseline"`
}

type Initialization struct {
	State        State  `json:"state"`
	DefaultInput string `json:"default_input"`
	Warning      string `json:"warning,omitempty"`
}

type EventRequest struct {
	Query      string                 `json:"query"`
	After      *workspace.EventCursor `json:"after,omitempty"`
	Limit      int                    `json:"limit"`
	Descending bool                   `json:"descending"`
}

type AppLogRequest struct {
	Keyword  string `json:"keyword"`
	Severity string `json:"severity"`
	AfterID  int64  `json:"after_id"`
	Limit    int    `json:"limit"`
}

type PayloadDocument struct {
	Content   string `json:"content"`
	Sensitive bool   `json:"sensitive"`
}

type projectOpener func(context.Context, project.OpenRequest) (*project.Project, error)

type Service struct {
	mu               sync.RWMutex
	app              *application.App
	current          *project.Project
	state            State
	savedQueries     *savedquery.Store
	defaultInputPath func() (string, error)
	openProject      projectOpener
	openCancel       context.CancelFunc
	openGeneration   uint64
}

func NewService(app *application.App, savedQueryPath string) (*Service, error) {
	store, err := savedquery.Open(savedQueryPath)
	if err != nil {
		return nil, err
	}
	return &Service{
		app:              app,
		savedQueries:     store,
		defaultInputPath: DefaultClientLogsPath,
		openProject:      project.Open,
	}, nil
}

func DefaultClientLogsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve user home directory: %w", err)
	}
	if strings.TrimSpace(home) == "" {
		return "", errors.New("user home directory is empty")
	}
	return filepath.Join(home, clientDataDirName, clientLogsDirName), nil
}

func DefaultSavedQueryPath() (string, error) {
	root, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "Cursor Log Analyzer", "saved-queries.json"), nil
}

func (service *Service) setApp(app *application.App) {
	service.mu.Lock()
	defer service.mu.Unlock()
	service.app = app
}

func (service *Service) Initialize() Initialization {
	result := Initialization{State: service.GetState()}
	if result.State.Opened {
		result.DefaultInput = result.State.Input
		return result
	}
	if service.defaultInputPath == nil {
		result.Warning = "无法解析客户端默认日志目录，请手动选择"
		return result
	}
	input, err := service.defaultInputPath()
	result.DefaultInput = strings.TrimSpace(input)
	if err != nil {
		result.Warning = fmt.Sprintf("无法解析客户端默认日志目录：%v，请手动选择", err)
		return result
	}
	info, err := os.Stat(result.DefaultInput)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			result.Warning = "客户端默认日志目录尚不存在，请先启用日志采集或手动选择目录"
		} else {
			result.Warning = fmt.Sprintf("无法访问客户端默认日志目录：%v，请手动选择", err)
		}
		return result
	}
	if !info.IsDir() {
		result.Warning = "客户端默认日志路径不是目录，请手动选择日志目录"
		return result
	}
	state, err := service.OpenProject(OpenRequest{Input: result.DefaultInput})
	if err != nil {
		result.Warning = fmt.Sprintf("客户端默认日志目录自动加载失败：%v；可手动选择其他目录", err)
		return result
	}
	result.State = state
	return result
}

func (service *Service) SelectInputDirectory() (string, error) {
	if service == nil || service.app == nil {
		return "", errors.New("native dialog is unavailable")
	}
	return service.app.Dialog.OpenFile().
		SetTitle("选择日志目录").
		CanChooseFiles(false).
		CanChooseDirectories(true).
		PromptForSingleSelection()
}

func (service *Service) SelectExportDirectory() (string, error) {
	if service == nil || service.app == nil {
		return "", errors.New("native dialog is unavailable")
	}
	return service.app.Dialog.OpenFile().
		SetTitle("选择报告输出目录").
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		PromptForSingleSelection()
}

func (service *Service) OpenProject(request OpenRequest) (State, error) {
	input := strings.TrimSpace(request.Input)
	if input == "" {
		return State{}, errors.New("请选择日志目录")
	}
	inputs := []string{input}
	baselines := []string(nil)
	if baseline := strings.TrimSpace(request.Baseline); baseline != "" {
		baselines = []string{baseline}
	}

	ctx, cancel := context.WithCancel(context.Background())
	service.mu.Lock()
	if service.openCancel != nil {
		service.openCancel()
	}
	service.openGeneration++
	generation := service.openGeneration
	service.openCancel = cancel
	service.mu.Unlock()

	opened, err := service.openProject(ctx, project.OpenRequest{
		Inputs:             inputs,
		Baselines:          baselines,
		AllowUnknownSchema: request.AllowUnknownSchema,
	})
	cancel()
	if err != nil {
		service.mu.Lock()
		if service.openGeneration == generation {
			service.openCancel = nil
		}
		service.mu.Unlock()
		return State{}, err
	}

	service.mu.Lock()
	if service.openGeneration != generation {
		service.mu.Unlock()
		_ = opened.Close()
		return State{}, context.Canceled
	}
	service.openCancel = nil
	previous := service.current
	service.current = opened
	service.state = State{
		Opened:      true,
		Input:       input,
		Baseline:    strings.TrimSpace(request.Baseline),
		Summary:     opened.Summary(),
		HasBaseline: len(baselines) > 0,
	}
	state := service.state
	service.mu.Unlock()
	if previous != nil {
		_ = previous.Close()
	}
	return state, nil
}

func (service *Service) GetState() State {
	service.mu.RLock()
	defer service.mu.RUnlock()
	return service.state
}

func (service *Service) CloseProject() error {
	service.mu.Lock()
	if service.openCancel != nil {
		service.openCancel()
		service.openCancel = nil
	}
	service.openGeneration++
	current := service.current
	service.current = nil
	service.state = State{}
	service.mu.Unlock()
	if current == nil {
		return nil
	}
	return current.Close()
}

func (service *Service) SearchEvents(request EventRequest) (workspace.EventSearchPage, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.current == nil {
		return workspace.EventSearchPage{}, errors.New("请先打开日志项目")
	}
	return service.current.SearchEvents(context.Background(), workspace.DatasetCurrent, workspace.EventSearchRequest{
		Query: request.Query, After: request.After, Limit: request.Limit, Descending: request.Descending,
	})
}

func (service *Service) ListFindings(afterID int64, limit int) (workspace.FindingPage, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.current == nil {
		return workspace.FindingPage{}, errors.New("请先打开日志项目")
	}
	return service.current.ListFindings(context.Background(), workspace.DatasetCurrent, afterID, limit)
}

func (service *Service) ListDiagnosticMetrics(after string, limit int) (workspace.DiagnosticMetricPage, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.current == nil {
		return workspace.DiagnosticMetricPage{}, errors.New("请先打开日志项目")
	}
	return service.current.ListDiagnosticMetrics(context.Background(), workspace.DatasetCurrent, after, limit)
}

func (service *Service) SearchAppLogs(request AppLogRequest) (workspace.AppLogSearchPage, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.current == nil {
		return workspace.AppLogSearchPage{}, errors.New("请先打开日志项目")
	}
	return service.current.SearchAppLogs(context.Background(), workspace.DatasetCurrent, workspace.AppLogSearchRequest{
		Keyword: request.Keyword, Severity: request.Severity, AfterID: request.AfterID, Limit: request.Limit,
	})
}

func (service *Service) ReadEventPayload(ingestOrder int64) (PayloadDocument, error) {
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.current == nil {
		return PayloadDocument{}, errors.New("请先打开日志项目")
	}
	if ingestOrder <= 0 {
		return PayloadDocument{}, errors.New("ingest order is required")
	}
	document, err := service.current.ReadEventPayload(context.Background(), workspace.DatasetCurrent, ingestOrder, payloadReadLimit)
	if err != nil {
		return PayloadDocument{}, err
	}
	return PayloadDocument{Content: string(document.Content), Sensitive: document.Sensitive}, nil
}

func (service *Service) ExportReport(output string) error {
	output = strings.TrimSpace(output)
	if output == "" {
		return errors.New("请选择报告输出目录")
	}
	service.mu.RLock()
	defer service.mu.RUnlock()
	if service.current == nil {
		return errors.New("请先打开日志项目")
	}
	staged, err := service.current.StageReport(context.Background(), output)
	if err != nil {
		return err
	}
	if err := staged.Publish(); err != nil {
		return fmt.Errorf("publish report: %w", err)
	}
	return nil
}

func (service *Service) ListSavedQueries() []savedquery.Query {
	if service == nil || service.savedQueries == nil {
		return nil
	}
	return service.savedQueries.List()
}

func (service *Service) SaveQuery(item savedquery.Query) (savedquery.Query, error) {
	if service == nil || service.savedQueries == nil {
		return savedquery.Query{}, errors.New("saved query store is unavailable")
	}
	return service.savedQueries.Put(item)
}

func (service *Service) DeleteSavedQuery(id string) error {
	if service == nil || service.savedQueries == nil {
		return errors.New("saved query store is unavailable")
	}
	return service.savedQueries.Delete(id)
}

func (service *Service) shutdown() {
	_ = service.CloseProject()
}
