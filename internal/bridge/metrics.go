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

// HomeMetricsReport contains range-filtered aggregate and daily metrics.
type HomeMetricsReport struct {
	Range    string                        `json:"range"`
	Timezone string                        `json:"timezone"`
	Summary  HomeMetricsSummary            `json:"summary"`
	Daily    []historymetrics.DailySummary `json:"daily"`
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

// GetHomeMetricsReport returns daily metrics for 7d, 30d, or all.
func (service *MetricsService) GetHomeMetricsReport(rangeName string) (HomeMetricsReport, error) {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return HomeMetricsReport{}, err
	}
	summary, err := historymetrics.LoadUsageSummary(appdata.UsageFilePath())
	if err != nil {
		return HomeMetricsReport{}, err
	}
	selected := historymetrics.SelectDailyRange(summary.Daily, rangeName, time.Now().UTC())
	return HomeMetricsReport{
		Range:    historymetrics.NormalizeRange(rangeName),
		Timezone: "UTC",
		Daily:    selected,
		Summary:  summarizeDaily(selected),
	}, nil
}

func summarizeDaily(items []historymetrics.DailySummary) HomeMetricsSummary {
	var providerCalls, turns, valid, invalid int
	var requestTokens, promptTokens, cacheRead, cacheWrite int64
	for _, item := range items {
		providerCalls += int(item.ProviderCalls)
		turns += int(item.TurnsTotal)
		valid += int(item.ValidTurnsTotal)
		invalid += int(item.InvalidTurnsTotal)
		requestTokens += item.RequestTokens
		promptTokens += item.PromptTokens
		cacheRead += item.CacheReadTokens
		cacheWrite += item.CacheWriteTokens
	}
	var rate *float64
	if cacheRead+promptTokens-cacheRead-cacheWrite > 0 {
		input := promptTokens - cacheRead - cacheWrite
		value := float64(cacheRead) / float64(cacheRead+input)
		rate = &value
	}
	return HomeMetricsSummary{
		ProviderCallsTotal: providerCalls,
		TurnsTotal:         turns,
		ValidTurnsTotal:    valid,
		InvalidTurnsTotal:  invalid,
		RequestTokensTotal: requestTokens,
		PromptTokensTotal:  promptTokens,
		CacheReadTokens:    cacheRead,
		CacheWriteTokens:   cacheWrite,
		CacheHitRate:       rate,
	}
}

// ResetHomeMetricsSummary clears overview metrics without deleting session history.
func (service *MetricsService) ResetHomeMetricsSummary() error {
	if err := appdata.EnsureAssistantHome(); err != nil {
		return err
	}
	return forwarder.NewUsageFileStore(appdata.HistoryRootPath()).Reset()
}
