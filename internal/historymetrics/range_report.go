package historymetrics

import (
	"strconv"
	"strings"
	"time"
)

const (
	granularityHour = "hour"
	granularityDay  = "day"
	hourlyWindow    = 24
)

type Bucket struct {
	Start             string `json:"start"`
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

type RangeAggregate struct {
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

type RangeReport struct {
	Range       string         `json:"range"`
	Granularity string         `json:"granularity"`
	Timezone    string         `json:"timezone"`
	Start       string         `json:"start"`
	End         string         `json:"end"`
	GeneratedAt string         `json:"generatedAt"`
	DataVersion string         `json:"dataVersion"`
	Summary     RangeAggregate `json:"summary"`
	Daily       []DailySummary `json:"daily"`
	Buckets     []Bucket       `json:"buckets"`
}

func BuildRangeReport(summary Summary, rangeName string, now time.Time) RangeReport {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	name := NormalizeRange(rangeName)
	report := RangeReport{
		Range:       name,
		Granularity: GranularityForRange(name),
		Timezone:    "UTC",
		GeneratedAt: now.Format(time.RFC3339),
		DataVersion: dataVersionFromSummary(summary),
		Daily:       []DailySummary{},
		Buckets:     []Bucket{},
	}
	if name == "24h" {
		hourly := SelectHourlyRange(summary.Hourly, now)
		report.Buckets = bucketsFromHourly(hourly)
		if start, end, ok := hourWindowBounds(now); ok {
			report.Start = start
			report.End = end
		}
		report.Summary = aggregateBuckets(report.Buckets)
		return report
	}
	daily := SelectDailyRange(summary.Daily, name, now)
	report.Daily = daily
	report.Buckets = bucketsFromDaily(daily)
	if start, end, ok := dailyWindowBounds(daily); ok {
		report.Start = start
		report.End = end
	}
	report.Summary = aggregateBuckets(report.Buckets)
	return report
}

// SelectHourlyRange returns 24 UTC hour buckets ending at the current hour.
// Missing hours are filled with zeros. Stored daily totals are never used.
func SelectHourlyRange(items []HourlySummary, now time.Time) []HourlySummary {
	if now.IsZero() {
		now = time.Now().UTC()
	} else {
		now = now.UTC()
	}
	end := now.Truncate(time.Hour)
	start := end.Add(-time.Duration(hourlyWindow-1) * time.Hour)
	byHour := make(map[string]HourlySummary, len(items))
	for _, item := range items {
		hour, ok := parseHourKey(item.Hour)
		if !ok {
			continue
		}
		key := hour.Format(time.RFC3339)
		item.Hour = key
		if existing, found := byHour[key]; found {
			byHour[key] = addHourlySummary(existing, item)
			continue
		}
		byHour[key] = item
	}
	result := make([]HourlySummary, 0, hourlyWindow)
	for cursor := start; !cursor.After(end); cursor = cursor.Add(time.Hour) {
		key := cursor.Format(time.RFC3339)
		if item, ok := byHour[key]; ok {
			result = append(result, item)
			continue
		}
		result = append(result, HourlySummary{Hour: key})
	}
	return result
}

func hourWindowBounds(now time.Time) (string, string, bool) {
	endHour := now.UTC().Truncate(time.Hour)
	startHour := endHour.Add(-time.Duration(hourlyWindow-1) * time.Hour)
	return startHour.Format(time.RFC3339), endHour.Add(time.Hour).Format(time.RFC3339), true
}

func dailyWindowBounds(items []DailySummary) (string, string, bool) {
	if len(items) == 0 {
		return "", "", false
	}
	start, err := time.Parse("2006-01-02", strings.TrimSpace(items[0].Date))
	if err != nil {
		return "", "", false
	}
	end, err := time.Parse("2006-01-02", strings.TrimSpace(items[len(items)-1].Date))
	if err != nil {
		return "", "", false
	}
	return start.UTC().Format(time.RFC3339), end.AddDate(0, 0, 1).UTC().Format(time.RFC3339), true
}

func bucketsFromHourly(items []HourlySummary) []Bucket {
	result := make([]Bucket, 0, len(items))
	for _, item := range items {
		result = append(result, Bucket{
			Start:             item.Hour,
			ProviderCalls:     item.ProviderCalls,
			TurnsTotal:        item.TurnsTotal,
			ValidTurnsTotal:   item.ValidTurnsTotal,
			InvalidTurnsTotal: item.InvalidTurnsTotal,
			InputTokens:       item.InputTokens,
			OutputTokens:      item.OutputTokens,
			RequestTokens:     item.RequestTokens,
			PromptTokens:      item.PromptTokens,
			CacheReadTokens:   item.CacheReadTokens,
			CacheWriteTokens:  item.CacheWriteTokens,
		})
	}
	return result
}

func bucketsFromDaily(items []DailySummary) []Bucket {
	result := make([]Bucket, 0, len(items))
	for _, item := range items {
		start := ""
		if date := strings.TrimSpace(item.Date); date != "" {
			start = date + "T00:00:00Z"
		}
		result = append(result, Bucket{
			Start:             start,
			ProviderCalls:     item.ProviderCalls,
			TurnsTotal:        item.TurnsTotal,
			ValidTurnsTotal:   item.ValidTurnsTotal,
			InvalidTurnsTotal: item.InvalidTurnsTotal,
			InputTokens:       item.InputTokens,
			OutputTokens:      item.OutputTokens,
			RequestTokens:     item.RequestTokens,
			PromptTokens:      item.PromptTokens,
			CacheReadTokens:   item.CacheReadTokens,
			CacheWriteTokens:  item.CacheWriteTokens,
		})
	}
	return result
}

func aggregateBuckets(items []Bucket) RangeAggregate {
	var totals Totals
	var providerCalls, turns, valid, invalid int
	for _, item := range items {
		providerCalls += int(item.ProviderCalls)
		turns += int(item.TurnsTotal)
		valid += int(item.ValidTurnsTotal)
		invalid += int(item.InvalidTurnsTotal)
		totals.InputTokens += item.InputTokens
		totals.OutputTokens += item.OutputTokens
		totals.CacheReadTokens += item.CacheReadTokens
		totals.CacheWriteTokens += item.CacheWriteTokens
		totals.PromptTokensTotal += item.PromptTokens
		totals.RequestTokensTotal += item.RequestTokens
	}
	if totals.PromptTokensTotal == 0 {
		totals.PromptTokensTotal = totals.InputTokens + totals.CacheReadTokens + totals.CacheWriteTokens
	}
	if totals.RequestTokensTotal == 0 {
		totals.RequestTokensTotal = totals.PromptTokensTotal + totals.OutputTokens
	}
	return RangeAggregate{
		ProviderCallsTotal: providerCalls,
		TurnsTotal:         turns,
		ValidTurnsTotal:    valid,
		InvalidTurnsTotal:  invalid,
		RequestTokensTotal: totals.RequestTokensTotal,
		PromptTokensTotal:  totals.PromptTokensTotal,
		CacheReadTokens:    totals.CacheReadTokens,
		CacheWriteTokens:   totals.CacheWriteTokens,
		CacheHitRate:       cacheHitRateFromTotals(totals),
	}
}

func dataVersionFromSummary(summary Summary) string {
	version := summary.SchemaVersion
	if version <= 0 {
		version = 1
	}
	if summary.UpdatedAt.IsZero() {
		return strconv.Itoa(version)
	}
	return strconv.Itoa(version) + "@" + summary.UpdatedAt.UTC().Format(time.RFC3339)
}
