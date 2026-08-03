package project

import (
	"context"
	"errors"
	"fmt"

	"cursor-log-analyzer/internal/analyze"
	"cursor-log-analyzer/internal/load"
	"cursor-log-analyzer/internal/report"
	"cursor-log-analyzer/internal/source"
	"cursor-log-analyzer/internal/workspace"
)

type Stage string

const (
	StageWorkspaceOpened Stage = "workspace_opened"
	StageCurrentLoaded   Stage = "current_loaded"
	StageBaselineLoaded  Stage = "baseline_loaded"
	StageAnalyzed        Stage = "analyzed"
	StageReportStaged    Stage = "report_staged"
	StageClosed          Stage = "closed"
)

type Progress struct {
	Stage   Stage
	Summary analyze.Summary
}

type Observer func(Progress)

type OpenRequest struct {
	Inputs             []string
	Baselines          []string
	AllowUnknownSchema bool
	TempDir            string
	Observer           Observer
}

type Project struct {
	store           *workspace.Workspace
	summary         analyze.Summary
	includeBaseline bool
	observer        Observer
}

func Open(ctx context.Context, request OpenRequest) (*Project, error) {
	store, err := workspace.Open(ctx, workspace.Options{TempDir: request.TempDir})
	if err != nil {
		return nil, err
	}
	value := &Project{
		store:           store,
		includeBaseline: len(request.Baselines) > 0,
		observer:        request.Observer,
	}
	value.notify(StageWorkspaceOpened)
	loadOptions := load.Options{AllowUnknownSchema: request.AllowUnknownSchema}
	if err := load.IntoWorkspace(ctx, store, workspace.DatasetCurrent, request.Inputs, loadOptions); err != nil {
		return nil, errors.Join(err, value.Close())
	}
	value.notify(StageCurrentLoaded)
	if value.includeBaseline {
		if err := load.IntoWorkspace(ctx, store, workspace.DatasetBaseline, request.Baselines, loadOptions); err != nil {
			return nil, errors.Join(fmt.Errorf("load baseline: %w", err), value.Close())
		}
		value.notify(StageBaselineLoaded)
	}
	value.summary, err = analyze.Workspace(ctx, store, value.includeBaseline)
	if err != nil {
		return nil, errors.Join(err, value.Close())
	}
	value.notify(StageAnalyzed)
	return value, nil
}

func (project *Project) Summary() analyze.Summary {
	if project == nil {
		return analyze.Summary{}
	}
	return project.summary
}

func (project *Project) SearchEvents(ctx context.Context, kind workspace.DatasetKind, request workspace.EventSearchRequest) (workspace.EventSearchPage, error) {
	datasetID, err := project.datasetID(ctx, kind)
	if err != nil {
		return workspace.EventSearchPage{}, err
	}
	request.DatasetID = datasetID
	return project.store.SearchEvents(ctx, request)
}

func (project *Project) ListFindings(ctx context.Context, kind workspace.DatasetKind, afterID int64, limit int) (workspace.FindingPage, error) {
	datasetID, err := project.datasetID(ctx, kind)
	if err != nil {
		return workspace.FindingPage{}, err
	}
	return project.store.ListFindings(ctx, datasetID, afterID, limit)
}

func (project *Project) ListDiagnosticMetrics(ctx context.Context, kind workspace.DatasetKind, after string, limit int) (workspace.DiagnosticMetricPage, error) {
	datasetID, err := project.datasetID(ctx, kind)
	if err != nil {
		return workspace.DiagnosticMetricPage{}, err
	}
	return project.store.ListDiagnosticMetrics(ctx, datasetID, after, limit)
}

func (project *Project) SearchAppLogs(ctx context.Context, kind workspace.DatasetKind, request workspace.AppLogSearchRequest) (workspace.AppLogSearchPage, error) {
	datasetID, err := project.datasetID(ctx, kind)
	if err != nil {
		return workspace.AppLogSearchPage{}, err
	}
	request.DatasetID = datasetID
	return project.store.SearchAppLogs(ctx, request)
}

func (project *Project) ReadEventPayload(ctx context.Context, kind workspace.DatasetKind, ingestOrder int64, maxBytes int64) (source.PayloadDocument, error) {
	datasetID, err := project.datasetID(ctx, kind)
	if err != nil {
		return source.PayloadDocument{}, err
	}
	locator, err := project.store.EventPayloadLocator(ctx, datasetID, ingestOrder)
	if err != nil {
		return source.PayloadDocument{}, err
	}
	return source.ReadPayload(source.PayloadRequest{
		EventsFilePath: locator.EventsFilePath,
		Reference:      locator.Reference,
		MaxBytes:       maxBytes,
	})
}

func (project *Project) datasetID(ctx context.Context, kind workspace.DatasetKind) (int64, error) {
	if project == nil || project.store == nil {
		return 0, errors.New("analysis project is closed")
	}
	return project.store.DatasetID(ctx, kind)
}

func (project *Project) StageReport(ctx context.Context, output string) (*report.StagedReport, error) {
	if project == nil || project.store == nil {
		return nil, errors.New("analysis project is closed")
	}
	staged, err := report.StageWorkspace(ctx, output, project.store, project.includeBaseline)
	if err != nil {
		return nil, err
	}
	project.notify(StageReportStaged)
	return staged, nil
}

func (project *Project) Close() error {
	if project == nil || project.store == nil {
		return nil
	}
	store := project.store
	project.store = nil
	err := store.CloseAndRemove()
	project.notify(StageClosed)
	return err
}

func (project *Project) notify(stage Stage) {
	if project != nil && project.observer != nil {
		project.observer(Progress{Stage: stage, Summary: project.summary})
	}
}
