//go:build unit

package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type iqProbeHeartbeatTestRepo struct {
	IQProbeRepository
	lost     atomic.Bool
	err      error
	beats    chan struct{}
	finished chan string
}

func (r *iqProbeHeartbeatTestRepo) HeartbeatRun(ctx context.Context, _ int64) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	select {
	case r.beats <- struct{}{}:
	default:
	}
	return !r.lost.Load(), r.err
}

func (r *iqProbeHeartbeatTestRepo) FinishRun(_ context.Context, run *IQProbeRun, _ *IQProbeMonitorRunState) (bool, error) {
	r.finished <- run.Status
	return false, nil
}

func TestIQProbeHeartbeat_CancelsWorkerOnLostLeaseOrDatabaseFailure(t *testing.T) {
	for _, dbErr := range []error{nil, errors.New("database unavailable")} {
		t.Run(map[bool]string{true: "database failure", false: "lost lease"}[dbErr != nil], func(t *testing.T) {
			repo := &iqProbeHeartbeatTestRepo{err: dbErr, beats: make(chan struct{}, 8)}
			repo.lost.Store(dbErr == nil)
			svc := NewIQProbeService(repo, nil, nil, nil, nil, nil)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan struct{})
			go func() {
				defer close(done)
				svc.keepRunAlive(ctx, cancel, 7, time.Millisecond)
			}()
			select {
			case <-done:
			case <-time.After(time.Second):
				t.Fatal("heartbeat worker did not stop")
			}
			require.ErrorIs(t, ctx.Err(), context.Canceled)
		})
	}
}

func TestIQProbeExecution_QueuedRoundCanBeCancelledWithoutStartingUpstream(t *testing.T) {
	repo := &iqProbeHeartbeatTestRepo{beats: make(chan struct{}, 8), finished: make(chan string, 1)}
	svc := NewIQProbeService(repo, nil, nil, nil, nil, nil)
	for i := 0; i < cap(svc.sem); i++ {
		svc.sem <- struct{}{}
	}
	require.True(t, svc.tryMarkRunning(1))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.executeRound(ctx, &IQProbeMonitor{ID: 1}, &IQProbeRun{ID: 7})
	}()
	select {
	case <-repo.beats:
	case <-time.After(time.Second):
		t.Fatal("queued round did not renew its heartbeat")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("queued round did not stop")
	}
	require.Equal(t, IQProbeRunInvalid, <-repo.finished)
	require.Equal(t, cap(svc.sem), len(svc.sem), "other workers' slots are preserved")
	require.True(t, svc.tryMarkRunning(1), "local mark is released")
}

func TestIQProbeHeartbeat_ContinuesUntilCancelled(t *testing.T) {
	repo := &iqProbeHeartbeatTestRepo{beats: make(chan struct{}, 8)}
	svc := NewIQProbeService(repo, nil, nil, nil, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		svc.keepRunAlive(ctx, cancel, 7, time.Millisecond)
	}()
	for i := 0; i < 3; i++ {
		select {
		case <-repo.beats:
		case <-time.After(time.Second):
			t.Fatal("live round stopped heartbeating")
		}
	}
	require.NoError(t, ctx.Err())
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("heartbeat goroutine leaked")
	}
}

func (r *iqProbeHeartbeatTestRepo) GetMonitor(context.Context, int64) (*IQProbeMonitor, error) {
	return nil, ErrIQProbeMonitorNotFound
}
