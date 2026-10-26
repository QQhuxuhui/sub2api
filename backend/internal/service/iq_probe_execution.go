package service

import (
	"context"
	"errors"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/logger"
)

const iqProbeHeartbeatInterval = 30 * time.Second
const iqProbeHeartbeatTimeout = 10 * time.Second

var errIQProbeRunLeaseLost = errors.New("probe round no longer owns its running slot")

func (s *IQProbeService) heartbeatRun(ctx context.Context, runID int64) error {
	ctx, cancel := context.WithTimeout(ctx, iqProbeHeartbeatTimeout)
	defer cancel()
	owned, err := s.repo.HeartbeatRun(ctx, runID)
	if err != nil {
		return err
	}
	if !owned {
		return errIQProbeRunLeaseLost
	}
	return nil
}

// keepRunAlive covers both the concurrency queue and upstream requests. Any
// loss of ownership (or inability to renew it) cancels the worker context.
func (s *IQProbeService) keepRunAlive(ctx context.Context, cancel context.CancelFunc, runID int64, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.heartbeatRun(ctx, runID); err != nil {
				logger.LegacyPrintf("service.iq_probe", "[IQProbe] run=%d heartbeat stopped: %v", runID, err)
				cancel()
				return
			}
		}
	}
}

func (s *IQProbeService) executeRound(parent context.Context, m *IQProbeMonitor, run *IQProbeRun) {
	defer s.unmarkRunning(m.ID)
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	err := s.heartbeatRun(ctx, run.ID)
	if err == nil {
		done := make(chan struct{})
		go func() {
			defer close(done)
			s.keepRunAlive(ctx, cancel, run.ID, iqProbeHeartbeatInterval)
		}()
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case s.sem <- struct{}{}:
			// Check again after waiting, before any work starts.
			err = s.heartbeatRun(ctx, run.ID)
			if err == nil {
				err = s.runRound(ctx, m, run)
			}
			<-s.sem
		}
		if err == nil {
			err = ctx.Err()
		}
		cancel()
		<-done
	}
	if err != nil {
		logger.LegacyPrintf("service.iq_probe", "[IQProbe] run=%d interrupted: %v", run.ID, err)
		run.Status, run.Score = IQProbeRunInvalid, nil
		run.Message = "运行中断（运行租约失效或任务取消）"
	}
	// Stop and join the heartbeat before changing the row's status. Otherwise
	// a normal completion could make our own heartbeat cancel finalization.
	// FinishRun's ownership condition also fences a worker retired by cleanup.
	cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
	defer stop()
	s.finishRound(cleanup, m, run)
}
