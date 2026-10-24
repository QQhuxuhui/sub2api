package erroridentity

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

type Health struct {
	Coverage    Coverage  `json:"coverage"`
	Ready       bool      `json:"ready"`
	Version     uint64    `json:"version"`
	Failures    uint64    `json:"failures"`
	LastSuccess time.Time `json:"last_success"`
}

// Cache owns the refresh lifecycle. Notify is nonblocking and coalesces bursts.
type Cache struct {
	load    func(context.Context) ([]string, error)
	current atomic.Pointer[Scrubber]
	mu      sync.Mutex
	health  Health
	notify  chan struct{}
	cancel  context.CancelFunc
	done    chan struct{}
	start   sync.Once
}

func NewCache(load func(context.Context) ([]string, error)) *Cache {
	return &Cache{load: load, notify: make(chan struct{}, 1)}
}
func (c *Cache) Snapshot() *Scrubber {
	if c == nil {
		return nil
	}
	return c.current.Load()
}
func (c *Cache) Health() Health {
	if c == nil {
		return Health{Coverage: CoverageStats()}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	health := c.health
	health.Coverage = CoverageStats()
	return health
}
func (c *Cache) Refresh(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	models, err := c.load(ctx)
	if err != nil {
		c.health.Failures++
		return err
	}
	c.current.Store(New(models))
	c.health.Ready = true
	c.health.Version++
	c.health.LastSuccess = time.Now()
	return nil
}
func (c *Cache) Notify() {
	if c != nil {
		select {
		case c.notify <- struct{}{}:
		default:
		}
	}
}
func (c *Cache) Start(ctx context.Context, onError func(error)) {
	c.start.Do(func() {
		ctx, c.cancel = context.WithCancel(ctx)
		c.done = make(chan struct{})
		refresh := func() {
			if err := c.Refresh(ctx); err != nil && onError != nil {
				onError(err)
			}
		}
		refresh()
		go func() {
			defer close(c.done)
			ticker := time.NewTicker(time.Minute)
			defer ticker.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-ticker.C:
					refresh()
				case <-c.notify:
					refresh()
				}
			}
		}()
	})
}
func (c *Cache) Stop() {
	if c != nil && c.cancel != nil {
		c.cancel()
		<-c.done
	}
}
