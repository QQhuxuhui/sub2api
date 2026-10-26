package service

import (
	"context"
	"sync"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

// IQProbeRunner ticks every minute to start due probe rounds and runs hourly
// maintenance. Cross-instance exclusion comes from ClaimMonitor, so every
// instance can tick safely.
type IQProbeRunner struct {
	svc       *IQProbeService
	stopCh    chan struct{}
	wg        sync.WaitGroup
	startOnce sync.Once
	stopOnce  sync.Once
}

func NewIQProbeRunner(svc *IQProbeService) *IQProbeRunner {
	return &IQProbeRunner{svc: svc, stopCh: make(chan struct{})}
}

func (r *IQProbeRunner) Start() {
	if r == nil || r.svc == nil {
		return
	}
	r.startOnce.Do(func() {
		r.wg.Add(1)
		go r.loop()
		logger.LegacyPrintf("service.iq_probe", "[IQProbe] runner started (tick=1m)")
	})
}

func (r *IQProbeRunner) Stop() {
	if r == nil {
		return
	}
	r.stopOnce.Do(func() {
		close(r.stopCh)
		r.wg.Wait()
	})
}

func (r *IQProbeRunner) loop() {
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
