package upstream

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	agentSessionTTL        = 30 * time.Minute
	agentSessionMaxSize    = 4096
	agentSessionMaxWaiters = 16
)

type agentSession struct {
	dest    AgentDestination
	decided bool
	waiters []chan AgentDestination
	updated time.Time
}

// AgentSessionStore keeps per-request_id Local/Official decisions so RunSSE
// can wait for BidiAppend instead of guessing the first local adapter.
type AgentSessionStore struct {
	mu       sync.Mutex
	sessions map[string]*agentSession
}

func NewAgentSessionStore() *AgentSessionStore {
	return &AgentSessionStore{sessions: make(map[string]*agentSession)}
}

func (store *AgentSessionStore) Lookup(requestID string) (AgentDestination, bool) {
	requestID = strings.TrimSpace(requestID)
	if store == nil || requestID == "" {
		return AgentDestinationUnknown, false
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	session := store.sessions[requestID]
	if session == nil || !session.decided {
		return AgentDestinationUnknown, false
	}
	session.updated = time.Now()
	return session.dest, true
}

// Remember records the first Local/Official decision for a request.
// Unknown does not close the session; later model-bearing appends may still decide.
func (store *AgentSessionStore) Remember(requestID string, dest AgentDestination) AgentDestination {
	requestID = strings.TrimSpace(requestID)
	if store == nil || requestID == "" {
		return dest
	}
	if dest != AgentDestinationLocal && dest != AgentDestinationOfficial {
		if existing, ok := store.Lookup(requestID); ok {
			return existing
		}
		return dest
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	store.evictLocked(time.Now())
	session := store.ensureLocked(requestID)
	session.updated = time.Now()
	if session.decided {
		return session.dest
	}
	session.dest = dest
	session.decided = true
	waiters := session.waiters
	session.waiters = nil
	for _, waiter := range waiters {
		waiter <- dest
	}
	return dest
}

func (store *AgentSessionStore) Wait(ctx context.Context, requestID string) (AgentDestination, bool) {
	requestID = strings.TrimSpace(requestID)
	if store == nil || requestID == "" {
		return AgentDestinationUnknown, false
	}
	if ctx == nil {
		ctx = context.Background()
	}
	waiter := make(chan AgentDestination, 1)
	store.mu.Lock()
	store.evictLocked(time.Now())
	session := store.ensureLocked(requestID)
	session.updated = time.Now()
	if session.decided {
		dest := session.dest
		store.mu.Unlock()
		return dest, true
	}
	if len(session.waiters) >= agentSessionMaxWaiters {
		store.mu.Unlock()
		return AgentDestinationUnknown, false
	}
	session.waiters = append(session.waiters, waiter)
	store.mu.Unlock()

	select {
	case dest := <-waiter:
		if dest != AgentDestinationLocal && dest != AgentDestinationOfficial {
			return AgentDestinationUnknown, false
		}
		return dest, true
	case <-ctx.Done():
		store.dropWaiter(requestID, waiter)
		return AgentDestinationUnknown, false
	}
}

func (store *AgentSessionStore) ensureLocked(requestID string) *agentSession {
	session := store.sessions[requestID]
	if session == nil {
		session = &agentSession{updated: time.Now()}
		store.sessions[requestID] = session
	}
	return session
}

func (store *AgentSessionStore) dropWaiter(requestID string, waiter chan AgentDestination) {
	store.mu.Lock()
	defer store.mu.Unlock()
	session := store.sessions[requestID]
	if session == nil {
		return
	}
	filtered := session.waiters[:0]
	for _, candidate := range session.waiters {
		if candidate != waiter {
			filtered = append(filtered, candidate)
		}
	}
	session.waiters = filtered
}

func (session *agentSession) notifyWaiters(dest AgentDestination) {
	if session == nil {
		return
	}
	for _, waiter := range session.waiters {
		if waiter == nil {
			continue
		}
		waiter <- dest
	}
	session.waiters = nil
}

func (store *AgentSessionStore) evictLocked(now time.Time) {
	if len(store.sessions) == 0 {
		return
	}
	for requestID, session := range store.sessions {
		if session == nil {
			delete(store.sessions, requestID)
			continue
		}
		if now.Sub(session.updated) > agentSessionTTL {
			session.notifyWaiters(AgentDestinationUnknown)
			delete(store.sessions, requestID)
		}
	}
	if len(store.sessions) <= agentSessionMaxSize {
		return
	}
	// Overflow evicts oldest undecided sessions first, including waiters.
	// Decided routes stay until activity TTL.
	type candidate struct {
		id      string
		updated time.Time
	}
	undecided := make([]candidate, 0)
	for requestID, session := range store.sessions {
		if session == nil || session.decided {
			continue
		}
		undecided = append(undecided, candidate{id: requestID, updated: session.updated})
	}
	sort.Slice(undecided, func(i, j int) bool {
		return undecided[i].updated.Before(undecided[j].updated)
	})
	for _, item := range undecided {
		if session := store.sessions[item.id]; session != nil {
			session.notifyWaiters(AgentDestinationUnknown)
		}
		delete(store.sessions, item.id)
		if len(store.sessions) <= agentSessionMaxSize {
			return
		}
	}
}
