package bridge

import (
	"time"

	"cursor/internal/appdata"
	"cursor/internal/backend/forwarder"
	"cursor/internal/historymetrics"
)

// HomeMetricsSummary defines the aggregate metrics shown on the overview page.
type HomeMetricsSummary struct {
	ProviderCallsTotal int      `json:"providerCallsTotal"`
	TurnsTotal         int      `json:"turnsTotal"`
	ValidTurnsTotal    int      `json:"validTurnsTotal"`
	InvalidTurnsTotal  int      `json:"invalidTurnsTotal"`
	RequestTokensTotal int64    `json:"requestTokensTotal"`
	PromptTokensTotal  int64    `json:"promptTokensTotal"`
	CacheReadTokens    int64    `json:"cacheReadTokens"`
	CacheWriteTokens   int64    `json:"cacheWriteTokens"`
	CacheHitRate       *float64 `json:"cacheHitRate"`
}

// MetricsService defines overview metrics methods exposed through Wails.
type MetricsService struct{}

// HomeMetricsReport contains a unified range report.
// Daily remains populated for 7d/30d/all so existing daily consumers keep working.
type HomeMetricsReport struct {
	Range       string                        `json:"range"`
	Granularity string                        `json:"granularity"`
	Timezone    string                        `json:"timezone"`
	Start       string                        `json:"start"`
	End         string                        `json:"end"`
	GeneratedAt string                        `json:"generatedAt"`
	DataVersion string                        `json:"dataVersion"`
	Summary     HomeMetricsSummary            `json:"summary"`
	Daily       []historymetrics.DailySummary `json:"daily"`
	Buckets     []historymetrics.Bucket       `json:"buckets"`
}

// NewMetricsService creates the metrics service.
func NewMetricsService() *MetricsService {
	return &MetricsService{}
}

// GetHomeMetricsSummary returns the full aggregate summary.
func (service *MetricsService) GetHomeMetricsSummary() (HomeMetricsSummary, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return HomeMetricsSummary{}, err
	}
	summary, err := historymetrics.LoadUsageSummary(appdata.UsageFilePath())
	if err != nil {
		return HomeMetricsSummary{}, err
	}
	return HomeMetricsSummary{
		ProviderCallsTotal: summary.ProviderCallsTotal,
		TurnsTotal:         summary.TurnsTotal,
		ValidTurnsTotal:    summary.ValidTurnsTotal,
		InvalidTurnsTotal:  summary.InvalidTurnsTotal,
		RequestTokensTotal: summary.RequestTokensTotal,
		PromptTokensTotal:  summary.PromptTokensTotal,
		CacheReadTokens:    summary.CacheReadTokens,
		CacheWriteTokens:   summary.CacheWriteTokens,
		CacheHitRate:       summary.CacheHitRate,
	}, nil
}

// GetHomeMetricsReport returns 24h hourly buckets or 7d/30d/all daily buckets.
func (service *MetricsService) GetHomeMetricsReport(rangeName string) (HomeMetricsReport, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return HomeMetricsReport{}, err
	}
	summary, err := historymetrics.LoadUsageSummary(appdata.UsageFilePath())
	if err != nil {
		return HomeMetricsReport{}, err
	}
	built := historymetrics.BuildRangeReport(summary, rangeName, time.Now().UTC())
	return homeMetricsReportFromRange(built), nil
}

func homeMetricsReportFromRange(built historymetrics.RangeReport) HomeMetricsReport {
	return HomeMetricsReport{
		Range:       built.Range,
		Granularity: built.Granularity,
		Timezone:    built.Timezone,
		Start:       built.Start,
		End:         built.End,
		GeneratedAt: built.GeneratedAt,
		DataVersion: built.DataVersion,
		Daily:       built.Daily,
		Buckets:     built.Buckets,
		Summary: HomeMetricsSummary{
			ProviderCallsTotal: built.Summary.ProviderCallsTotal,
			TurnsTotal:         built.Summary.TurnsTotal,
			ValidTurnsTotal:    built.Summary.ValidTurnsTotal,
			InvalidTurnsTotal:  built.Summary.InvalidTurnsTotal,
			RequestTokensTotal: built.Summary.RequestTokensTotal,
			PromptTokensTotal:  built.Summary.PromptTokensTotal,
			CacheReadTokens:    built.Summary.CacheReadTokens,
			CacheWriteTokens:   built.Summary.CacheWriteTokens,
			CacheHitRate:       built.Summary.CacheHitRate,
		},
	}
}

// ResetHomeMetricsSummary clears overview metrics without deleting session history.
func (service *MetricsService) ResetHomeMetricsSummary() error {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return err
	}
	return forwarder.NewUsageFileStore(appdata.HistoryRootPath()).Reset()
}
