package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// IQShowcaseRunner ticks every minute to start due showcase rounds and runs
// hourly maintenance. Cross-instance exclusion comes from the lease in
// ClaimItem, so every instance can tick safely.
type IQShowcaseRunner struct {
	svc       *IQShowcaseService
	stopCh    chan struct{}
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
}

func NewIQShowcaseRunner(svc *IQShowcaseService) *IQShowcaseRunner {
	return &IQShowcaseRunner{svc: svc, stopCh: make(chan struct{})}
}

func (r *IQShowcaseRunner) Start() {
	if r == nil || r.svc == nil {
		return
	}
	r.startOnce.Do(func() {
		r.wg.Add(1)
		go r.loop()
		logger.LegacyPrintf("service.iq_showcase", "[IQShowcase] runner started (tick=1m)")
	})
}

// Stop ends the ticker, then cancels running rounds and waits for them.
func (r *IQShowcaseRunner) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.stopCh)
		r.wg.Wait()
		r.svc.Stop()
	})
}

func (r *IQShowcaseRunner) loop() {
	defer r.wg.Done()
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	lastMaintenance := time.Time{}
	for {
		select {
		case <-r.stopCh:
			return
		case <-tick.C:
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			if time.Since(lastMaintenance) >= time.Hour {
				r.svc.Maintenance(ctx)
				lastMaintenance = time.Now()
			}
			r.svc.RunDue(ctx)
			cancel()
		}
	}
}
