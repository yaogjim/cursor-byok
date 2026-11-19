// Package requestlog 维护本机请求索引：每次业务模型调用追加一行，供请求列表分页查询。
//
// 索引按本地日期分片（requests-YYYYMMDD.jsonl），只追加不重写；行内不含消息正文、
// prompt、tool result、图片字节或费用，只保留计数与标识。写入与读取失败都只影响
// 列表展示，不阻断主链路。
package requestlog

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	shardDirname    = "requests"
	shardPrefix     = "requests-"
	shardExtension  = ".jsonl"
	shardDateLayout = "20060102"

	defaultPageSize = 50
	maxPageSize     = 500
	// maxScanRecords 限制单次查询解析的行数：时间范围越大命中越多，
	// 超过上限的旧行不再计入，保证界面查询耗时有界。
	maxScanRecords = 200_000
)

// statusFailed 是所有非成功收口的统一筛选值。
const statusFailed = "failed"

type Record struct {
	RequestID        string    `json:"request_id"`
	ConversationID   string    `json:"conversation_id,omitempty"`
	ModelCallID      string    `json:"model_call_id"`
	RequestModel     string    `json:"request_model,omitempty"`
	RequestModelName string    `json:"request_model_name,omitempty"`
	Provider         string    `json:"provider,omitempty"`
	Model            string    `json:"model,omitempty"`
	InputTokens      int64     `json:"input_tokens"`
	OutputTokens     int64     `json:"output_tokens"`
	CacheReadTokens  int64     `json:"cache_read_tokens"`
	CacheWriteTokens int64     `json:"cache_write_tokens"`
	TTFTMS           int64     `json:"ttft_ms"`
	DurationMS       int64     `json:"duration_ms"`
	StatusCode       int       `json:"status_code,omitempty"`
	ErrorCategory    string    `json:"error_category,omitempty"`
	Status           string    `json:"status"`
	At               time.Time `json:"at"`
}

// Query 描述一次列表查询。Page 从 1 开始；PageSize<=0 时使用默认页大小。
type Query struct {
	Range    string
	Provider string
	Model    string
	Status   string
	Page     int
	PageSize int
}

type Summary struct {
	Requests         int   `json:"requests"`
	InputTokens      int64 `json:"inputTokens"`
	OutputTokens     int64 `json:"outputTokens"`
	CacheReadTokens  int64 `json:"cacheReadTokens"`
	CacheWriteTokens int64 `json:"cacheWriteTokens"`
}

// Result 的 Items 只含当前页；Total 与 Summary 覆盖全部命中行。
type Result struct {
	Items    []Record `json:"items"`
	Total    int      `json:"total"`
	Page     int      `json:"page"`
	PageSize int      `json:"pageSize"`
	Summary  Summary  `json:"summary"`
}

type Store struct {
	dir       string
	retention time.Duration
	now       func() time.Time

	mu sync.Mutex
	// lastPrune 记录上次清理的日期，把目录扫描从每次追加降为每天一次，
	// 避免主链路每次模型调用收口都承担可变磁盘开销。
	lastPrune time.Time
	pruneBusy bool
	// pruneDone 在后台清理结束时被关闭，供 Close/测试等待，避免进程退出时清理未落地。
	pruneDone chan struct{}
}

func NewStore(historyRoot string, retention time.Duration) *Store {
	return &Store{
		dir:       filepath.Join(strings.TrimSpace(historyRoot), shardDirname),
		retention: retention,
		now:       time.Now,
	}
}

func (store *Store) Dir() string {
	if store == nil {
		return ""
	}
	return store.dir
}

// Append 追加一条请求记录。缺少 ModelCallID 的记录视为无效并直接忽略。
func (store *Store) Append(record Record) error {
	if store == nil || strings.TrimSpace(store.dir) == "" {
		return nil
	}
	record = normalizeRecord(record)
	if record.ModelCallID == "" {
		return nil
	}
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal request record: %w", err)
	}
	line = append(line, '\n')

	store.mu.Lock()
	defer store.mu.Unlock()
	if err := os.MkdirAll(store.dir, 0o755); err != nil {
		return fmt.Errorf("create request index dir: %w", err)
	}
	store.maybePruneLocked(record.At)

	path := filepath.Join(store.dir, shardName(record.At))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open request shard: %w", err)
	}
	defer file.Close()
	if _, err := file.Write(line); err != nil {
		return fmt.Errorf("append request shard: %w", err)
	}
	return nil
}

// Distinct 返回当前窗口内出现过的供应商与实际模型，供筛选下拉使用；不含跨窗口的历史值。
func (store *Store) Distinct(rangeName string) (providers []string, models []string, err error) {
	providers = make([]string, 0)
	models = make([]string, 0)
	if store == nil || strings.TrimSpace(store.dir) == "" {
		return providers, models, nil
	}
	start, end := rangeWindow(rangeName, store.now())
	shards, listErr := store.shardsSince(start)
	if listErr != nil {
		return providers, models, listErr
	}
	providerSet := make(map[string]struct{})
	modelSet := make(map[string]struct{})
	scanned := 0
	for index := len(shards) - 1; index >= 0 && scanned < maxScanRecords; index-- {
		readErr := scanShard(shards[index], func(record Record) bool {
			scanned++
			if !record.At.Before(start) && !record.At.After(end) {
				if record.Provider != "" {
					providerSet[record.Provider] = struct{}{}
				}
				if record.Model != "" {
					modelSet[record.Model] = struct{}{}
				}
				if record.RequestModel != "" {
					modelSet[record.RequestModel] = struct{}{}
				}
				if record.RequestModelName != "" {
					modelSet[record.RequestModelName] = struct{}{}
				}
			}
			return scanned < maxScanRecords
		})
		if readErr != nil {
			return providers, models, readErr
		}
	}
	return sortedKeys(providerSet), sortedKeys(modelSet), nil
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// List 按时间倒序返回当前页，并给出全部命中行的总数与 token 汇总。
func (store *Store) List(query Query) (Result, error) {
	result := Result{Items: make([]Record, 0)}
	if store == nil || strings.TrimSpace(store.dir) == "" {
		result.Page, result.PageSize = normalizePage(query)
		return result, nil
	}
	result.Page, result.PageSize = normalizePage(query)
	start, end := rangeWindow(query.Range, store.now())
	provider := strings.ToLower(strings.TrimSpace(query.Provider))
	model := strings.TrimSpace(query.Model)
	status := strings.ToLower(strings.TrimSpace(query.Status))
	if status != "" && status != statusFailed {
		// 目前只支持“失败”这一非全量状态；其它取值按全量处理。
		status = ""
	}

	shards, err := store.shardsSince(start)
	if err != nil {
		return result, err
	}
	// 分片按日期升序，倒序读入可优先拿到最新行；扫描行数有界，
	// 保证即便跨 30 天满量历史，单次查询耗时也不会无限增长。
	matched := make([]Record, 0, 256)
	scanned := 0
	for index := len(shards) - 1; index >= 0 && scanned < maxScanRecords; index-- {
		readErr := scanShard(shards[index], func(record Record) bool {
			scanned++
			if !record.At.Before(start) && !record.At.After(end) &&
				matchesProvider(record, provider) &&
				matchesModel(record, model) &&
				matchesStatus(record, status) {
				matched = append(matched, record)
			}
			return scanned < maxScanRecords
		})
		if readErr != nil {
			return result, readErr
		}
	}
	// 分片内的行按写入顺序递增，整体倒序后即为时间倒序。
	sort.SliceStable(matched, func(left, right int) bool {
		return matched[left].At.After(matched[right].At)
	})

	for _, record := range matched {
		result.Summary.Requests++
		result.Summary.InputTokens += record.InputTokens
		result.Summary.OutputTokens += record.OutputTokens
		result.Summary.CacheReadTokens += record.CacheReadTokens
		result.Summary.CacheWriteTokens += record.CacheWriteTokens
	}
	result.Total = len(matched)

	offset := (result.Page - 1) * result.PageSize
	if offset >= len(matched) {
		return result, nil
	}
	limit := offset + result.PageSize
	if limit > len(matched) {
		limit = len(matched)
	}
	result.Items = append(result.Items, matched[offset:limit]...)
	return result, nil
}

func normalizePage(query Query) (int, int) {
	page := query.Page
	if page <= 0 {
		page = 1
	}
	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}

func normalizeRecord(record Record) Record {
	record.RequestID = strings.TrimSpace(record.RequestID)
	record.ConversationID = strings.TrimSpace(record.ConversationID)
	record.ModelCallID = strings.TrimSpace(record.ModelCallID)
	record.RequestModel = strings.TrimSpace(record.RequestModel)
	record.RequestModelName = strings.TrimSpace(record.RequestModelName)
	record.Provider = strings.TrimSpace(record.Provider)
	record.Model = strings.TrimSpace(record.Model)
	record.ErrorCategory = strings.TrimSpace(record.ErrorCategory)
	record.Status = strings.TrimSpace(record.Status)
	record.InputTokens = nonNegative(record.InputTokens)
	record.OutputTokens = nonNegative(record.OutputTokens)
	record.CacheReadTokens = nonNegative(record.CacheReadTokens)
	record.CacheWriteTokens = nonNegative(record.CacheWriteTokens)
	record.TTFTMS = nonNegative(record.TTFTMS)
	record.DurationMS = nonNegative(record.DurationMS)
	if record.StatusCode < 0 {
		record.StatusCode = 0
	}
	if record.At.IsZero() {
		record.At = time.Now().UTC()
	} else {
		record.At = record.At.UTC()
	}
	return record
}

func nonNegative(value int64) int64 {
	if value < 0 {
		return 0
	}
	return value
}

// rangeWindow 按本地日期分片边界计算窗口：起始与结束均含。
func rangeWindow(rangeName string, now time.Time) (time.Time, time.Time) {
	now = now.UTC()
	end := now
	local := now.In(time.Local)
	todayStart := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.Local).UTC()
	switch strings.ToLower(strings.TrimSpace(rangeName)) {
	case "", "all", "30d":
		return todayStart.AddDate(0, 0, -(shardRetentionDays - 1)), end
	case "today":
		return todayStart, end
	case "24h":
		return now.Add(-24 * time.Hour), end
	case "7d":
		return todayStart.AddDate(0, 0, -6), end
	default:
		return todayStart.AddDate(0, 0, -(shardRetentionDays - 1)), end
	}
}

const shardRetentionDays = 30

func matchesProvider(record Record, provider string) bool {
	return provider == "" || strings.ToLower(record.Provider) == provider
}

// matchesModel 同时匹配实际模型、请求模型与请求模型名称，方便按客户端看到的模型名过滤。
func matchesModel(record Record, model string) bool {
	if model == "" {
		return true
	}
	return record.Model == model || record.RequestModel == model || record.RequestModelName == model
}

func matchesStatus(record Record, status string) bool {
	if status == "" {
		return true
	}
	return strings.ToLower(record.Status) != "completed"
}

func shardName(at time.Time) string {
	return shardPrefix + at.In(time.Local).Format(shardDateLayout) + shardExtension
}

func shardDate(name string) (time.Time, bool) {
	if !strings.HasPrefix(name, shardPrefix) || !strings.HasSuffix(name, shardExtension) {
		return time.Time{}, false
	}
	value := strings.TrimSuffix(strings.TrimPrefix(name, shardPrefix), shardExtension)
	parsed, err := time.ParseInLocation(shardDateLayout, value, time.Local)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}

// pruneLocked 在写入前删除超出保留期的分片。分片为空目录时无操作。
// maybePruneLocked 每个本地日期至多触发一次清理，且不阻塞调用方：
// 真正删除放入独立 goroutine，只在后台短暂持锁，避免拖慢主链路收口。
func (store *Store) maybePruneLocked(now time.Time) {
	if store.retention <= 0 || store.pruneBusy {
		return
	}
	today := now.In(time.Local).Truncate(24 * time.Hour)
	if !store.lastPrune.IsZero() && !today.After(store.lastPrune.In(time.Local).Truncate(24*time.Hour)) {
		return
	}
	store.lastPrune = now
	store.pruneBusy = true
	done := make(chan struct{})
	store.pruneDone = done
	go func(at time.Time) {
		defer close(done)
		store.mu.Lock()
		defer store.mu.Unlock()
		defer func() { store.pruneBusy = false }()
		store.pruneLocked(at)
	}(now)
}

// WaitPrune 等待进行中的后台清理结束；没有清理在进行时立即返回。
// 供应用关闭与测试使用，保证清理结果确定性可见。
func (store *Store) WaitPrune() {
	if store == nil {
		return
	}
	store.mu.Lock()
	done := store.pruneDone
	store.mu.Unlock()
	if done != nil {
		<-done
	}
}

func (store *Store) pruneLocked(now time.Time) {
	if store.retention <= 0 {
		return
	}
	cutoff := now.Add(-store.retention)
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		windowStart, ok := shardDate(entry.Name())
		if !ok || !windowStart.Before(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(store.dir, entry.Name()))
	}
}

// shards 返回按日期升序排列的分片路径。
func (store *Store) shards() ([]string, error) {
	return store.shardsSince(time.Time{})
}

// shardsSince 只返回窗口起点所在日及之后的分片，避免为小窗口读入全部历史。
func (store *Store) shardsSince(since time.Time) ([]string, error) {
	entries, err := os.ReadDir(store.dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read request index dir: %w", err)
	}
	type shardEntry struct {
		path string
		date time.Time
	}
	shards := make([]shardEntry, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		date, ok := shardDate(entry.Name())
		if !ok {
			continue
		}
		if !since.IsZero() && date.Before(since.In(time.Local).Truncate(24*time.Hour)) {
			continue
		}
		shards = append(shards, shardEntry{path: filepath.Join(store.dir, entry.Name()), date: date})
	}
	sort.Slice(shards, func(left, right int) bool {
		return shards[left].date.Before(shards[right].date)
	})
	paths := make([]string, 0, len(shards))
	for _, shard := range shards {
		paths = append(paths, shard.path)
	}
	return paths, nil
}

// scanShard 逐行解析分片并回调；回调返回 false 时提前结束。
// 无法解析的行（例如写入中断）被跳过，不使整个列表失败。
func scanShard(path string, visit func(Record) bool) error {
	file, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open request shard: %w", err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		var record Record
		if err := json.Unmarshal(line, &record); err != nil || strings.TrimSpace(record.ModelCallID) == "" {
			continue
		}
		if !visit(record) {
			return nil
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scan request shard: %w", err)
	}
	return nil
}
