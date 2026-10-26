package forwarder

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
)

const compactionLayoutVersion = 1

// compactionLayout 将归档覆盖与活动输入分开。区间是冻结的原始历史下标，
// 引用同时校验条目身份及内容；仅追加不会改变它们，重建不得猜测或静默降级。
// 归档中的输入是历史来源，PinnedInputs 是活动重放引用，不是新用户提交。
type compactionLayout struct {
	Version      int                        `json:"version"`
	PrefixStart  int                        `json:"prefix_start"`
	PrefixEnd    int                        `json:"prefix_end"`
	TailStart    int                        `json:"tail_start"`
	SourceDigest string                     `json:"source_digest"`
	PinnedInputs []compactionEntryReference `json:"pinned_inputs"`
}

type compactionEntryReference struct {
	Seq           int64  `json:"seq"`
	TurnSeq       int64  `json:"turn_seq"`
	RequestID     string `json:"request_id"`
	Kind          string `json:"kind"`
	PayloadDigest string `json:"payload_digest"`
}

func compactionDigest(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func compactionEntryRef(entry HistoryEntry) compactionEntryReference {
	// UseNumber 保全大整数；JSON 对象键序和空白不参与内容身份。
	var payload any
	decoder := json.NewDecoder(bytes.NewReader(entry.Payload))
	decoder.UseNumber()
	if err := decoder.Decode(&payload); err != nil {
		payload = string(entry.Payload)
	}
	return compactionEntryReference{Seq: entry.Seq, TurnSeq: entry.TurnSeq, RequestID: entry.RequestID, Kind: entry.Kind, PayloadDigest: compactionDigest(payload)}
}

func compactionSourceDigest(entries []HistoryEntry) string {
	type sourceIdentity struct {
		Reference        compactionEntryReference
		ModelCallID      string
		IdempotencyKey   string
		Role             string
		ToolCallID       string
		ParentToolCallID string
		CreatedAt        string
	}
	refs := make([]sourceIdentity, len(entries))
	for i, entry := range entries {
		refs[i] = sourceIdentity{Reference: compactionEntryRef(entry), ModelCallID: entry.ModelCallID, IdempotencyKey: entry.IdempotencyKey, Role: entry.Role, ToolCallID: entry.ToolCallID, ParentToolCallID: entry.ParentToolCallID, CreatedAt: entry.CreatedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00")}
	}
	return compactionDigest(refs)
}

func cloneCompactionLayout(layout *compactionLayout) *compactionLayout {
	if layout == nil {
		return nil
	}
	cloned := *layout
	cloned.PinnedInputs = append([]compactionEntryReference{}, layout.PinnedInputs...)
	return &cloned
}

func parseCompactionLayout(raw json.RawMessage) (*compactionLayout, error) {
	if len(raw) == 0 {
		return nil, nil // 字段缺省才代表旧记录；显式 null 或部分对象均损坏。
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fmt.Errorf("invalid compaction layout")
	}
	for _, key := range []string{"version", "prefix_start", "prefix_end", "tail_start", "source_digest", "pinned_inputs"} {
		value, ok := fields[key]
		if !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return nil, fmt.Errorf("invalid compaction layout: missing %s", key)
		}
	}
	var layout compactionLayout
	if err := json.Unmarshal(raw, &layout); err != nil || layout.Version != compactionLayoutVersion {
		return nil, fmt.Errorf("invalid or unsupported compaction layout version")
	}
	return &layout, nil
}

func validateCompactionLayout(entries []HistoryEntry, summaryIndex int, payload compactionSummaryEntryPayload) (*compactionLayout, map[int]struct{}, error) {
	if payload.LayoutVersion == 0 && len(payload.Layout) == 0 {
		return nil, nil, nil
	}
	if payload.LayoutVersion != compactionLayoutVersion || len(payload.Layout) == 0 {
		return nil, nil, fmt.Errorf("missing or unsupported compaction layout version")
	}
	layout, err := parseCompactionLayout(payload.Layout)
	if err != nil || layout == nil {
		return layout, nil, err
	}
	// 新版自动压缩必须独立保留当前输入；不能通过同时清空引用和翻转旧布尔
	// 标记绕过完整性检查。只有手动摘要沿既有合同允许整体归档、不保留引用。
	if !payload.PreserveCurrentTurnInputs && (strings.TrimSpace(payload.Trigger) != "manual" || len(layout.PinnedInputs) != 0) {
		return nil, nil, fmt.Errorf("compaction layout input preservation policy mismatch")
	}
	if summaryIndex < 0 || summaryIndex > len(entries) || layout.PrefixStart < 0 || layout.PrefixEnd < layout.PrefixStart || layout.PrefixEnd > summaryIndex || layout.TailStart != layout.PrefixEnd {
		return nil, nil, fmt.Errorf("invalid compaction layout range")
	}
	if layout.SourceDigest != compactionSourceDigest(entries[layout.PrefixStart:layout.PrefixEnd]) {
		return nil, nil, fmt.Errorf("compaction layout source identity mismatch")
	}
	if err := ensureCompactionPrefixClosed(entries[:summaryIndex], layout.PrefixStart, layout.PrefixEnd); err != nil {
		return nil, nil, err
	}
	source, err := parseNativeArchiveSource(payload.NativeArchiveSource)
	if err != nil {
		return nil, nil, err
	}
	if source != nil && (source.PrefixStart != layout.PrefixStart || source.PrefixEnd != layout.PrefixEnd) {
		return nil, nil, fmt.Errorf("compaction layout archive range mismatch")
	}
	pinned := map[int]struct{}{}
	dropped := canceledReplayDroppedIndexes(entries[:summaryIndex])
	previous := -1
	for _, ref := range layout.PinnedInputs {
		found := -1
		for index, entry := range entries[:layout.TailStart] {
			if entry.Seq != ref.Seq {
				continue
			}
			if compactionEntryRef(entry) != ref || found >= 0 {
				return nil, nil, fmt.Errorf("compaction layout input identity mismatch")
			}
			found = index
		}
		if found <= previous || found < 0 || !isCompactionInput(entries[found]) || entries[found].TurnSeq != payload.CurrentTurnSeq || strings.TrimSpace(entries[found].RequestID) != strings.TrimSpace(payload.CurrentRequestID) {
			return nil, nil, fmt.Errorf("compaction layout input reference missing or unordered")
		}
		if _, canceled := dropped[found]; canceled {
			return nil, nil, fmt.Errorf("compaction layout input canceled")
		}
		pinned[found] = struct{}{}
		previous = found
	}
	// 列表必须完整，不能用删除一个合法引用来静默丢输入。
	if payload.PreserveCurrentTurnInputs {
		for i, entry := range entries[:layout.TailStart] {
			if entry.TurnSeq == payload.CurrentTurnSeq && strings.TrimSpace(entry.RequestID) == strings.TrimSpace(payload.CurrentRequestID) && isCompactionInput(entry) {
				if _, ok := pinned[i]; !ok {
					return nil, nil, fmt.Errorf("compaction layout input omitted")
				}
			}
		}
	}
	return layout, pinned, nil
}

func validateCompactionLayouts(entries []HistoryEntry) error {
	for index, entry := range entries {
		if !isCompactionSummaryKind(entry.Kind) {
			continue
		}
		payload, err := decodeCompactionSummaryPayload(entry.Payload)
		if err != nil {
			return err
		}
		if _, _, err := validateCompactionLayout(entries, index, payload); err != nil {
			return fmt.Errorf("compaction layout at history entry %d: %w", entry.Seq, err)
		}
	}
	return nil
}

// remapCompactionLayouts 只用于已知逐项对应的保存/重编号路径，先校验旧引用，
// 再按同一条目的明确下标映射新身份；不按文本搜索、不修补损坏记录。
func remapCompactionLayouts(before, after []HistoryEntry, firstChanged int) error {
	versioned := false
	for _, entry := range before {
		if isCompactionSummaryKind(entry.Kind) {
			payload, err := decodeCompactionSummaryPayload(entry.Payload)
			if err != nil {
				return err
			}
			versioned = versioned || len(payload.Layout) > 0 || payload.LayoutVersion != 0
		}
	}
	if !versioned {
		return nil
	}
	if len(before) != len(after) {
		return fmt.Errorf("compaction layout rebuild changed history membership")
	}
	if err := validateCompactionLayouts(before); err != nil {
		return err
	}
	for index := firstChanged; index < len(before); index++ {
		if !isCompactionSummaryKind(before[index].Kind) {
			continue
		}
		payload, err := decodeCompactionSummaryPayload(before[index].Payload)
		if err != nil {
			return err
		}
		layout, pinned, err := validateCompactionLayout(before, index, payload)
		if err != nil {
			return err
		}
		if layout == nil {
			continue
		}
		layout.PinnedInputs = []compactionEntryReference{}
		for i := 0; i < layout.TailStart; i++ {
			if _, ok := pinned[i]; ok {
				layout.PinnedInputs = append(layout.PinnedInputs, compactionEntryRef(after[i]))
			}
		}
		layout.SourceDigest = compactionSourceDigest(after[layout.PrefixStart:layout.PrefixEnd])
		payload.Layout, _ = json.Marshal(layout)
		after[index].Payload, _ = json.Marshal(payload)
	}
	return validateCompactionLayouts(after)
}

func isCompactionInput(entry HistoryEntry) bool {
	// 持久 prompt_context 含选中命令、模式与完成门禁提醒；派生去重读取原始
	// 历史，归档后不会再生成，必须与当前用户输入一起独立保留。
	return entry.Kind == "user_message" || entry.Kind == "request_context" || entry.Kind == "prompt_context"
}

func freezeCompactionLayout(entries []HistoryEntry, plan *compactionPlan) {
	layout := &compactionLayout{Version: compactionLayoutVersion, PrefixStart: plan.FrozenPrefixStart, PrefixEnd: plan.FrozenPrefixEnd, TailStart: plan.FrozenPrefixEnd, SourceDigest: compactionSourceDigest(entries[plan.FrozenPrefixStart:plan.FrozenPrefixEnd]), PinnedInputs: []compactionEntryReference{}}
	if plan.PreserveCurrentTurnInputs {
		for _, entry := range entries[:layout.TailStart] {
			if entry.TurnSeq == plan.CurrentTurnSeq && strings.TrimSpace(entry.RequestID) == strings.TrimSpace(plan.CurrentRequestID) && isCompactionInput(entry) {
				layout.PinnedInputs = append(layout.PinnedInputs, compactionEntryRef(entry))
			}
		}
	}
	plan.Layout = layout
}

// executionTailStart 去掉输入锚点后仍保留近期工具及推理载体，并在任何
// 未完成/身份不完整的调用之前停止。完整并行批次由现有批次对齐规则保全。
func executionTailStart(entries []HistoryEntry, preserved map[int]struct{}, coveredEnd int, turnSeq int64, requestID string) int {
	execution := map[int]struct{}{}
	dropped := canceledReplayDroppedIndexes(entries)
	for index := range preserved {
		if _, canceled := dropped[index]; canceled {
			continue
		}
		if !isCompactionInput(entries[index]) {
			execution[index] = struct{}{}
		}
	}
	start := continuousTailStartIndex(entries, len(entries), execution)
	open := map[string]int{}
	for index, entry := range entries {
		if index < coveredEnd || entry.TurnSeq != turnSeq || strings.TrimSpace(entry.RequestID) != strings.TrimSpace(requestID) {
			continue // 已归档记录及已结束的旧回合不能锁住当前回合。
		}
		if _, canceled := dropped[index]; canceled {
			continue
		}
		if !isContinuousTailToolEntryKind(entry.Kind) {
			continue
		}
		id := historyEntryToolCallID(entry)
		key := fmt.Sprintf("%d/%s/%s/%s", entry.TurnSeq, entry.RequestID, entry.ModelCallID, id)
		if id == "" || strings.TrimSpace(entry.ModelCallID) == "" {
			if index < start {
				start = index
			}
		}
		switch entry.Kind {
		case "tool_call":
			open[key] = index
		case "tool_result":
			if _, ok := open[key]; ok {
				delete(open, key)
			} else if index < start {
				start = index
			}
		}
	}
	for _, index := range open {
		if index < start {
			start = index
		}
	}
	start = extendContinuousTailToNativeGroupStart(entries, extendContinuousTailToToolBatchStart(entries, start))
	return extendExecutionTailToModelCallStart(entries, start)
}

// 同一次模型调用的文本/推理与工具批次一起保留，不能只凭传统签名字段
// 判定关联；例如 Responses 推理条目可能仅带 provider 身份和 summary。
func extendExecutionTailToModelCallStart(entries []HistoryEntry, start int) int {
	if start < 0 || start >= len(entries) || strings.TrimSpace(entries[start].ModelCallID) == "" {
		return start
	}
	target := entries[start]
	for index := start - 1; index >= 0; index-- {
		entry := entries[index]
		if !isOrdinaryPromptReplayEntryKind(entry.Kind) {
			continue
		}
		if isCompactionInput(entry) || entry.TurnSeq != target.TurnSeq || entry.RequestID != target.RequestID || entry.ModelCallID != target.ModelCallID {
			break
		}
		start = index
	}
	return start
}
