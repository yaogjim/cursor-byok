// capacity_limiter.go 实现按物理上游组共享的进程内短超时并发槽。
// 组身份仅存在于内存：provider type + NormalizeBaseURL(baseURL) + API key 的 SHA-256。
// 不承诺严格 FIFO，不维护显式队列，不在热更新时主动唤醒旧等待者。
package modeladapter

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"sync"
	"time"

	"cursor/internal/modelchannel"
)

const (
	upstreamCapacityWait     = 2 * time.Second
	maxUpstreamCapacityLimit = 16
	upstreamCapacityKeySep   = "\n"
)

type capacityLimiter struct {
	mu     sync.Mutex
	groups map[string]*capacityGroup
}

type capacityGroup struct {
	mu     sync.Mutex
	cond   *sync.Cond
	active int
}

var processCapacityLimiter = newCapacityLimiter()

func newCapacityLimiter() *capacityLimiter {
	return &capacityLimiter{groups: make(map[string]*capacityGroup)}
}

func normalizeMaxConcurrentRequests(limit int) int {
	if limit <= 0 {
		return 0
	}
	if limit > maxUpstreamCapacityLimit {
		return maxUpstreamCapacityLimit
	}
	return limit
}

func upstreamCapacityGroupKey(provider, baseURL, apiKey string) string {
	provider = strings.ToLower(strings.TrimSpace(provider))
	normalizedBaseURL, err := modelchannel.NormalizeBaseURL(baseURL)
	if err != nil {
		normalizedBaseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	}
	payload := provider + upstreamCapacityKeySep + normalizedBaseURL + upstreamCapacityKeySep + strings.TrimSpace(apiKey)
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])
}

func streamRequestCapacityGroupKey(req StreamRequest) string {
	if key := strings.TrimSpace(req.UpstreamCapacityGroupKey); key != "" {
		return key
	}
	return upstreamCapacityGroupKey(req.Provider, req.BaseURL, req.APIKey)
}

func (limiter *capacityLimiter) group(key string) *capacityGroup {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if group, ok := limiter.groups[key]; ok {
		return group
	}
	group := &capacityGroup{}
	group.cond = sync.NewCond(&group.mu)
	limiter.groups[key] = group
	return group
}

func acquireUpstreamCapacity(ctx context.Context, req StreamRequest) (func(), error) {
	return processCapacityLimiter.acquire(ctx, req)
}

func (limiter *capacityLimiter) acquire(ctx context.Context, req StreamRequest) (func(), error) {
	limit := normalizeMaxConcurrentRequests(req.MaxConcurrentRequests)
	if limit <= 0 {
		return func() {}, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	key := streamRequestCapacityGroupKey(req)
	group := limiter.group(key)
	if err := group.acquire(ctx, limit); err != nil {
		return func() {}, err
	}
	var once sync.Once
	return func() {
		once.Do(func() { group.release() })
	}, nil
}

func (group *capacityGroup) acquire(ctx context.Context, limit int) error {
	waitCtx, cancel := context.WithTimeout(ctx, upstreamCapacityWait)
	defer cancel()

	stop := context.AfterFunc(waitCtx, func() {
		group.mu.Lock()
		group.cond.Broadcast()
		group.mu.Unlock()
	})
	defer stop()

	group.mu.Lock()
	defer group.mu.Unlock()
	for group.active >= limit {
		if waitCtx.Err() != nil {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return &CapacityUnavailableError{}
		}
		group.cond.Wait()
	}
	group.active++
	return nil
}

func (group *capacityGroup) release() {
	group.mu.Lock()
	if group.active > 0 {
		group.active--
	}
	group.cond.Broadcast()
	group.mu.Unlock()
}

func upstreamCapacityActive(groupKey string) int {
	processCapacityLimiter.mu.Lock()
	group := processCapacityLimiter.groups[strings.TrimSpace(groupKey)]
	processCapacityLimiter.mu.Unlock()
	if group == nil {
		return 0
	}
	group.mu.Lock()
	defer group.mu.Unlock()
	return group.active
}
