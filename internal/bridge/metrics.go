package bridge

import (
	"strings"
	"time"

	"cursor/internal/appdata"
	"cursor/internal/backend/forwarder"
	"cursor/internal/historymetrics"
	"cursor/internal/requestlog"
)

// requestListRetention 与请求索引写入端的保留期保持一致（30 天）。
const requestListRetention = 30 * 24 * time.Hour

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
type MetricsService struct {
	// historyRoot 为空时使用应用默认 history 根目录；测试可注入临时目录。
	historyRoot string
}

// RequestListQuery 描述请求列表查询参数。Range 取 today/24h/7d/30d。
type RequestListQuery struct {
	Range    string `json:"range"`
	Provider string `json:"provider"`
	Model    string `json:"model"`
	Status   string `json:"status"`
	Page     int    `json:"page"`
	PageSize int    `json:"pageSize"`
}

// RequestListRow 是请求列表的一行。CacheHitRate 为 nil 表示无缓存口径可算。
type RequestListRow struct {
	At               string   `json:"at"`
	RequestID        string   `json:"requestId"`
	ConversationID   string   `json:"conversationId,omitempty"`
	ModelCallID      string   `json:"modelCallId"`
	RequestModel     string   `json:"requestModel"`
	Provider         string   `json:"provider"`
	Model            string   `json:"model"`
	InputTokens      int64    `json:"inputTokens"`
	OutputTokens     int64    `json:"outputTokens"`
	CacheReadTokens  int64    `json:"cacheReadTokens"`
	CacheWriteTokens int64    `json:"cacheWriteTokens"`
	CacheHitRate     *float64 `json:"cacheHitRate"`
	TTFTMS           int64    `json:"ttftMs"`
	DurationMS       int64    `json:"durationMs"`
	StatusCode       int      `json:"statusCode"`
	ErrorCategory    string   `json:"errorCategory,omitempty"`
	Status           string   `json:"status"`
}

// CacheHitRateValue 返回缓存命中率，未知时为 0。
func (row RequestListRow) CacheHitRateValue() float64 {
	if row.CacheHitRate == nil {
		return 0
	}
	return *row.CacheHitRate
}

// RequestListSummary 汇总当前筛选条件下的全部命中行，而不是当前页。
type RequestListSummary struct {
	Requests         int   `json:"requests"`
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

type RequestListResult struct {
	Items    []RequestListRow   `json:"items"`
	Total    int                `json:"total"`
	Page     int                `json:"page"`
	PageSize int                `json:"pageSize"`
	Summary  RequestListSummary `json:"summary"`
}

// RequestListFilterOptions 是当前时间窗口内出现过的筛选项。
type RequestListFilterOptions struct {
	Providers []string `json:"providers"`
	Models    []string `json:"models"`
}

// ListRequests 返回请求列表当前页，以及全部命中行的总数与 token 汇总。
func (service *MetricsService) ListRequests(query RequestListQuery) (RequestListResult, error) {
	result := RequestListResult{Items: make([]RequestListRow, 0)}
	store := service.requestIndexStore()
	listed, err := store.List(requestlog.Query{
		Range:    query.Range,
		Provider: query.Provider,
		Model:    query.Model,
		Status:   query.Status,
		Page:     query.Page,
		PageSize: query.PageSize,
	})
	if err != nil {
		return result, err
	}
	result.Total = listed.Total
	result.Page = listed.Page
	result.PageSize = listed.PageSize
	result.Summary = RequestListSummary{
		Requests:         listed.Summary.Requests,
		InputTokens:      listed.Summary.InputTokens,
		OutputTokens:     listed.Summary.OutputTokens,
		CacheReadTokens:  listed.Summary.CacheReadTokens,
		CacheWriteTokens: listed.Summary.CacheWriteTokens,
	}
	for _, item := range listed.Items {
		result.Items = append(result.Items, requestListRow(item))
	}
	return result, nil
}

// ListRequestFilterOptions 返回指定窗口内可用于筛选的供应商与模型。
func (service *MetricsService) ListRequestFilterOptions(rangeName string) (RequestListFilterOptions, error) {
	options := RequestListFilterOptions{Providers: make([]string, 0), Models: make([]string, 0)}
	providers, models, err := service.requestIndexStore().Distinct(rangeName)
	if err != nil {
		return options, err
	}
	options.Providers = providers
	options.Models = models
	return options, nil
}

func (service *MetricsService) requestIndexStore() *requestlog.Store {
	root := ""
	if service != nil {
		root = strings.TrimSpace(service.historyRoot)
	}
	if root == "" {
		root = appdata.HistoryRootPath()
	}
	return requestlog.NewStore(root, requestListRetention)
}

func requestListRow(item requestlog.Record) RequestListRow {
	return RequestListRow{
		At:               item.At.UTC().Format(time.RFC3339),
		RequestID:        item.RequestID,
		ConversationID:   item.ConversationID,
		ModelCallID:      item.ModelCallID,
		RequestModel:     item.RequestModel,
		Provider:         item.Provider,
		Model:            item.Model,
		InputTokens:      item.InputTokens,
		OutputTokens:     item.OutputTokens,
		CacheReadTokens:  item.CacheReadTokens,
		CacheWriteTokens: item.CacheWriteTokens,
		CacheHitRate:     requestListCacheHitRate(item),
		TTFTMS:           item.TTFTMS,
		DurationMS:       item.DurationMS,
		StatusCode:       item.StatusCode,
		ErrorCategory:    item.ErrorCategory,
		Status:           item.Status,
	}
}

// requestListCacheHitRate 使用首页默认口径：缓存读取 /（缓存读取 + 非缓存输入）。
// 分母为零时返回 nil，表示该行无法计算。
func requestListCacheHitRate(item requestlog.Record) *float64 {
	nonCachedInput := item.InputTokens
	if nonCachedInput < 0 {
		nonCachedInput = 0
	}
	denominator := item.CacheReadTokens + nonCachedInput
	if denominator <= 0 {
		return nil
	}
	rate := float64(item.CacheReadTokens) / float64(denominator)
	return &rate
}

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
