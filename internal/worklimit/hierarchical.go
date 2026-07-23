package worklimit

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// HierarchicalLimiter atomically bounds expensive work across the whole
// process, within one workspace, and against one provider. Waiting callers do
// not hold a partial quota, so a saturated provider cannot consume the global
// capacity needed by unrelated providers.
type HierarchicalLimiter struct {
	mu sync.Mutex

	globalLimit       int
	workspaceLimit    int
	providerLimit     int
	active            int
	activeByWorkspace map[uint64]int
	activeByProvider  map[string]int
	changed           chan struct{}
}

func NewHierarchical(globalLimit, workspaceLimit, providerLimit int) (*HierarchicalLimiter, error) {
	if globalLimit < 1 || workspaceLimit < 1 || providerLimit < 1 {
		return nil, fmt.Errorf("work concurrency limits must be positive")
	}
	if workspaceLimit > globalLimit {
		return nil, fmt.Errorf("workspace work concurrency cannot exceed global concurrency")
	}
	if providerLimit > globalLimit {
		return nil, fmt.Errorf("provider work concurrency cannot exceed global concurrency")
	}
	return &HierarchicalLimiter{
		globalLimit:       globalLimit,
		workspaceLimit:    workspaceLimit,
		providerLimit:     providerLimit,
		activeByWorkspace: make(map[uint64]int),
		activeByProvider:  make(map[string]int),
		changed:           make(chan struct{}),
	}, nil
}

// Acquire waits for all three quotas in one atomic step. A canceled waiter is
// removed without beginning work. The returned release function is idempotent.
func (l *HierarchicalLimiter) Acquire(ctx context.Context, workspaceID uint64, provider string) (func(), error) {
	if l == nil {
		return func() {}, nil
	}
	if ctx == nil {
		return nil, fmt.Errorf("work limit context is required")
	}
	if workspaceID == 0 {
		return nil, fmt.Errorf("work limit workspace is required")
	}
	provider = strings.TrimSpace(provider)
	if provider == "" {
		provider = "unknown"
	}

	for {
		l.mu.Lock()
		if l.active < l.globalLimit &&
			l.activeByWorkspace[workspaceID] < l.workspaceLimit &&
			l.activeByProvider[provider] < l.providerLimit {
			l.active++
			l.activeByWorkspace[workspaceID]++
			l.activeByProvider[provider]++
			l.mu.Unlock()

			var once sync.Once
			return func() {
				once.Do(func() { l.release(workspaceID, provider) })
			}, nil
		}
		changed := l.changed
		l.mu.Unlock()

		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-changed:
		}
	}
}

func (l *HierarchicalLimiter) release(workspaceID uint64, provider string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.active <= 0 || l.activeByWorkspace[workspaceID] <= 0 || l.activeByProvider[provider] <= 0 {
		return
	}
	l.active--
	l.activeByWorkspace[workspaceID]--
	if l.activeByWorkspace[workspaceID] == 0 {
		delete(l.activeByWorkspace, workspaceID)
	}
	l.activeByProvider[provider]--
	if l.activeByProvider[provider] == 0 {
		delete(l.activeByProvider, provider)
	}
	close(l.changed)
	l.changed = make(chan struct{})
}
