package historymetrics

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"
)

type usageDaily struct {
	Date              string `json:"date"`
	ProviderCalls     int64  `json:"provider_calls"`
	TurnsTotal        int64  `json:"turns_total"`
	ValidTurnsTotal   int64  `json:"valid_turns_total"`
	InvalidTurnsTotal int64  `json:"invalid_turns_total"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CacheReadTokens   int64  `json:"cache_read_tokens"`
	CacheWriteTokens  int64  `json:"cache_write_tokens"`
	TotalTokens       int64  `json:"total_tokens"`
}

type usageHourly struct {
	Hour              string `json:"hour"`
	ProviderCalls     int64  `json:"provider_calls"`
	TurnsTotal        int64  `json:"turns_total"`
	ValidTurnsTotal   int64  `json:"valid_turns_total"`
	InvalidTurnsTotal int64  `json:"invalid_turns_total"`
	InputTokens       int64  `json:"input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CacheReadTokens   int64  `json:"cache_read_tokens"`
	CacheWriteTokens  int64  `json:"cache_write_tokens"`
	TotalTokens       int64  `json:"total_tokens"`
}

type usageFileDocument struct {
	SchemaVersion int       `json:"schema_version"`
	UpdatedAt     time.Time `json:"updated_at"`
	Totals        struct {
		ProviderCalls     int64 `json:"provider_calls"`
		TurnsTotal        int64 `json:"turns_total"`
		ValidTurnsTotal   int64 `json:"valid_turns_total"`
		InvalidTurnsTotal int64 `json:"invalid_turns_total"`
		InputTokens       int64 `json:"input_tokens"`
		OutputTokens      int64 `json:"output_tokens"`
		CacheReadTokens   int64 `json:"cache_read_tokens"`
		CacheWriteTokens  int64 `json:"cache_write_tokens"`
		TotalTokens       int64 `json:"total_tokens"`
	} `json:"totals"`
	Daily  []usageDaily  `json:"daily"`
	Hourly []usageHourly `json:"hourly"`
}

func LoadUsageSummary(path string) (Summary, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Summary{Daily: []DailySummary{}, Hourly: []HourlySummary{}}, nil
		}
		return Summary{}, fmt.Errorf("read usage file: %w", err)
	}
	var doc usageFileDocument
	if err := json.Unmarshal(body, &doc); err != nil {
		return Summary{}, fmt.Errorf("decode usage file: %w", err)
	}
	schemaVersion := doc.SchemaVersion
	if schemaVersion == 0 {
		schemaVersion = 1
	}
	totals := Totals{
		InputTokens:        doc.Totals.InputTokens,
		OutputTokens:       doc.Totals.OutputTokens,
		CacheReadTokens:    doc.Totals.CacheReadTokens,
		CacheWriteTokens:   doc.Totals.CacheWriteTokens,
		PromptTokensTotal:  doc.Totals.InputTokens + doc.Totals.CacheReadTokens + doc.Totals.CacheWriteTokens,
		RequestTokensTotal: doc.Totals.TotalTokens,
	}
	return Summary{
		SchemaVersion:      schemaVersion,
		UpdatedAt:          doc.UpdatedAt,
		ProviderCallsTotal: int(doc.Totals.ProviderCalls),
		TurnsTotal:         int(doc.Totals.TurnsTotal),
		ValidTurnsTotal:    int(doc.Totals.ValidTurnsTotal),
		InvalidTurnsTotal:  int(doc.Totals.InvalidTurnsTotal),
		RequestTokensTotal: totals.RequestTokensTotal,
		PromptTokensTotal:  totals.PromptTokensTotal,
		CacheReadTokens:    totals.CacheReadTokens,
		CacheWriteTokens:   totals.CacheWriteTokens,
		CacheHitRate:       cacheHitRateFromTotals(totals),
		Daily:              normalizeDaily(doc.Daily),
		Hourly:             normalizeHourly(doc.Hourly),
	}, nil
}

func normalizeDaily(items []usageDaily) []DailySummary {
	result := make([]DailySummary, 0, len(items))
	for _, item := range items {
		result = append(result, DailySummary{
			Date: item.Date, ProviderCalls: item.ProviderCalls, TurnsTotal: item.TurnsTotal,
			ValidTurnsTotal: item.ValidTurnsTotal, InvalidTurnsTotal: item.InvalidTurnsTotal,
			InputTokens: item.InputTokens, OutputTokens: item.OutputTokens,
			RequestTokens: item.TotalTokens, PromptTokens: item.InputTokens + item.CacheReadTokens + item.CacheWriteTokens,
			CacheReadTokens: item.CacheReadTokens, CacheWriteTokens: item.CacheWriteTokens,
		})
	}
	return result
}

func normalizeHourly(items []usageHourly) []HourlySummary {
	merged := make(map[string]HourlySummary, len(items))
	order := make([]string, 0, len(items))
	for _, item := range items {
		hour, ok := parseHourKey(item.Hour)
		if !ok {
			continue
		}
		key := hour.Format(time.RFC3339)
		next := HourlySummary{
			Hour: key, ProviderCalls: item.ProviderCalls, TurnsTotal: item.TurnsTotal,
			ValidTurnsTotal: item.ValidTurnsTotal, InvalidTurnsTotal: item.InvalidTurnsTotal,
			InputTokens: item.InputTokens, OutputTokens: item.OutputTokens,
			RequestTokens: item.TotalTokens, PromptTokens: item.InputTokens + item.CacheReadTokens + item.CacheWriteTokens,
			CacheReadTokens: item.CacheReadTokens, CacheWriteTokens: item.CacheWriteTokens,
		}
		if existing, found := merged[key]; found {
			merged[key] = addHourlySummary(existing, next)
			continue
		}
		merged[key] = next
		order = append(order, key)
	}
	result := make([]HourlySummary, 0, len(order))
	for _, key := range order {
		result = append(result, merged[key])
	}
	return result
}
