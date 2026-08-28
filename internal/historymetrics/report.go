package historymetrics

import (
	"sort"
	"strings"
	"time"
)

type DailySummary struct {
	Date              string `json:"date"`
	ProviderCalls     int64  `json:"providerCalls"`
	TurnsTotal        int64  `json:"turnsTotal"`
	ValidTurnsTotal   int64  `json:"validTurnsTotal"`
	InvalidTurnsTotal int64  `json:"invalidTurnsTotal"`
	InputTokens       int64  `json:"inputTokens"`
	OutputTokens      int64  `json:"outputTokens"`
	RequestTokens     int64  `json:"requestTokens"`
	PromptTokens      int64  `json:"promptTokens"`
	CacheReadTokens   int64  `json:"cacheReadTokens"`
	CacheWriteTokens  int64  `json:"cacheWriteTokens"`
}

type HourlySummary struct {
	Hour              string `json:"hour"`
	ProviderCalls     int64  `json:"providerCalls"`
	TurnsTotal        int64  `json:"turnsTotal"`
	ValidTurnsTotal   int64  `json:"validTurnsTotal"`
	InvalidTurnsTotal int64  `json:"invalidTurnsTotal"`
	InputTokens       int64  `json:"inputTokens"`
	OutputTokens      int64  `json:"outputTokens"`
	RequestTokens     int64  `json:"requestTokens"`
	PromptTokens      int64  `json:"promptTokens"`
	CacheReadTokens   int64  `json:"cacheReadTokens"`
	CacheWriteTokens  int64  `json:"cacheWriteTokens"`
}

type Summary struct {
	SchemaVersion      int             `json:"-"`
	UpdatedAt          time.Time       `json:"-"`
	ProviderCallsTotal int             `json:"providerCallsTotal"`
	TurnsTotal         int             `json:"turnsTotal"`
	ValidTurnsTotal    int             `json:"validTurnsTotal"`
	InvalidTurnsTotal  int             `json:"invalidTurnsTotal"`
	RequestTokensTotal int64           `json:"requestTokensTotal"`
	PromptTokensTotal  int64           `json:"promptTokensTotal"`
	CacheReadTokens    int64           `json:"cacheReadTokens"`
	CacheWriteTokens   int64           `json:"cacheWriteTokens"`
	CacheHitRate       *float64        `json:"cacheHitRate"`
	Daily              []DailySummary  `json:"daily"`
	Hourly             []HourlySummary `json:"hourly"`
}

type Totals struct {
	InputTokens        int64
	OutputTokens       int64
	CacheReadTokens    int64
	CacheWriteTokens   int64
	PromptTokensTotal  int64
	RequestTokensTotal int64
}

func cacheHitRateFromTotals(totals Totals) *float64 {
	inputCacheTokensTotal := totals.CacheReadTokens + totals.InputTokens
	if inputCacheTokensTotal <= 0 {
		return nil
	}
	value := float64(totals.CacheReadTokens) / float64(inputCacheTokensTotal)
	return &value
}

func NormalizeRange(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "24h", "7d", "30d":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return "all"
	}
}

func GranularityForRange(rangeName string) string {
	if NormalizeRange(rangeName) == "24h" {
		return "hour"
	}
	return "day"
}

// SelectDailyRange returns UTC daily buckets for 7d, 30d, or all.
// Missing dates inside the selected window are filled with zeros.
// 24h is not a daily range and returns an empty slice.
func SelectDailyRange(items []DailySummary, rangeName string, now time.Time) []DailySummary {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	byDate := make(map[string]DailySummary, len(items))
	dates := make([]string, 0, len(items))
	for _, item := range items {
		date := strings.TrimSpace(item.Date)
		if date == "" {
			continue
		}
		item.Date = date
		if _, exists := byDate[date]; !exists {
			dates = append(dates, date)
		}
		byDate[date] = item
	}
	name := NormalizeRange(rangeName)
	if name == "24h" {
		return []DailySummary{}
	}
	if name == "all" {
		if len(dates) == 0 {
			return []DailySummary{}
		}
		sort.Strings(dates)
		return fillDailyRange(byDate, dates[0], dates[len(dates)-1])
	}
	days := 7
	if name == "30d" {
		days = 30
	}
	end := now.Format("2006-01-02")
	start := now.AddDate(0, 0, -(days - 1)).Format("2006-01-02")
	return fillDailyRange(byDate, start, end)
}

func parseHourKey(value string) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}
	return parsed.UTC().Truncate(time.Hour), true
}

func addHourlySummary(base, extra HourlySummary) HourlySummary {
	base.ProviderCalls += extra.ProviderCalls
	base.TurnsTotal += extra.TurnsTotal
	base.ValidTurnsTotal += extra.ValidTurnsTotal
	base.InvalidTurnsTotal += extra.InvalidTurnsTotal
	base.InputTokens += extra.InputTokens
	base.OutputTokens += extra.OutputTokens
	base.RequestTokens += extra.RequestTokens
	base.PromptTokens += extra.PromptTokens
	base.CacheReadTokens += extra.CacheReadTokens
	base.CacheWriteTokens += extra.CacheWriteTokens
	return base
}

func fillDailyRange(byDate map[string]DailySummary, startDate, endDate string) []DailySummary {
	start, err := time.Parse("2006-01-02", startDate)
	if err != nil {
		return []DailySummary{}
	}
	end, err := time.Parse("2006-01-02", endDate)
	if err != nil || end.Before(start) {
		return []DailySummary{}
	}
	result := make([]DailySummary, 0, int(end.Sub(start).Hours()/24)+1)
	for cursor := start; !cursor.After(end); cursor = cursor.AddDate(0, 0, 1) {
		date := cursor.Format("2006-01-02")
		if item, ok := byDate[date]; ok {
			result = append(result, item)
			continue
		}
		result = append(result, DailySummary{Date: date})
	}
	return result
}
