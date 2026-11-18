package requestlog

import (
	"testing"
	"time"
)

// 查询期间写路径不得被阻塞：List 与 Append 使用互不重叠的状态。
func TestAppendNotBlockedWhileListRuns(t *testing.T) {
	store := testStore(t)
	base := fixedNow()
	for index := 0; index < 300; index++ {
		record := sampleRecord(base.Add(-time.Duration(index)*time.Second), "deepseek-flash")
		record.ModelCallID = "call-" + string(rune('a'+index%26)) + string(rune('0'+index/26))
		appendRecord(t, store, record)
	}

	listDone := make(chan struct{})
	go func() {
		defer close(listDone)
		for iteration := 0; iteration < 20; iteration++ {
			if _, err := store.List(Query{Range: "24h", Page: 1, PageSize: 50}); err != nil {
				t.Errorf("list: %v", err)
				return
			}
		}
	}()

	deadline := time.Now().Add(2 * time.Second)
	appended := 0
	for appended < 50 {
		if time.Now().After(deadline) {
			t.Fatalf("append blocked by concurrent list after %d writes", appended)
		}
		record := sampleRecord(base.Add(-time.Duration(appended)*time.Second), "deepseek-flash")
		record.ModelCallID = "live-" + string(rune('a'+appended%26)) + string(rune('0'+appended/26))
		appendRecord(t, store, record)
		appended++
	}
	<-listDone
	if appended != 50 {
		t.Fatalf("appended = %d, want 50", appended)
	}
}

// 查询不得在持有写锁的情况下进行长期磁盘扫描而阻塞下一次收口。
func TestAppendLatencyUnaffectedByLargeHistory(t *testing.T) {
	store := testStore(t)
	base := fixedNow()
	for index := 0; index < 2000; index++ {
		record := sampleRecord(base.Add(-time.Duration(index)*time.Second), "deepseek-flash")
		record.ModelCallID = "bulk-" + string(rune('a'+index%26)) + string(rune('0'+index/26%10)) + string(rune('0'+index/260))
		appendRecord(t, store, record)
	}

	started := time.Now()
	for index := 0; index < 100; index++ {
		record := sampleRecord(base, "deepseek-flash")
		record.ModelCallID = "single-" + string(rune('a'+index%26)) + string(rune('0'+index/26))
		appendRecord(t, store, record)
	}
	elapsed := time.Since(started)
	// 100 次追加应在秒级内完成；若每次收口都做全目录扫描会明显超时。
	if elapsed > 2*time.Second {
		t.Fatalf("100 appends took %v with 2000 existing rows", elapsed)
	}
}

// 清理扫描每天至多触发一次：同一天内的大量追加不应反复扫描目录。
func TestPruneRunsAtMostOncePerDay(t *testing.T) {
	store := testStore(t)
	base := fixedNow()
	for index := 0; index < 5; index++ {
		record := sampleRecord(base.Add(time.Duration(index)*time.Minute), "deepseek-flash")
		record.ModelCallID = "same-day-" + string(rune('a'+index))
		appendRecord(t, store, record)
	}
	store.WaitPrune()

	store.mu.Lock()
	firstPrune := store.lastPrune
	store.mu.Unlock()
	if firstPrune.IsZero() {
		t.Fatal("expected a prune to have been scheduled on first append")
	}

	// 同一天稍后的追加不得改写 lastPrune（说明没有再次触发扫描）。
	for index := 5; index < 10; index++ {
		record := sampleRecord(base.Add(time.Duration(index)*time.Minute), "deepseek-flash")
		record.ModelCallID = "same-day-" + string(rune('a'+index))
		appendRecord(t, store, record)
	}
	store.mu.Lock()
	secondPrune := store.lastPrune
	store.mu.Unlock()
	if !secondPrune.Equal(firstPrune) {
		t.Fatalf("prune rescheduled within the same day: %v -> %v", firstPrune, secondPrune)
	}
}
