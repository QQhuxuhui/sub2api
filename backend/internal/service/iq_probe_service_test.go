//go:build unit

package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type iqProbeDueTestRepo struct {
	IQProbeRepository
	monitor *IQProbeMonitor
	claims  int
}

func (r *iqProbeDueTestRepo) ListDueMonitorIDs(context.Context, time.Time, int) ([]int64, error) {
	return []int64{r.monitor.ID}, nil
}

func (r *iqProbeDueTestRepo) GetMonitor(context.Context, int64) (*IQProbeMonitor, error) {
	return r.monitor, nil
}

func (r *iqProbeDueTestRepo) ClaimMonitor(context.Context, int64, *time.Time, time.Time) (bool, error) {
	r.claims++
	return false, nil
}

func TestIQProbeRunDue_RechecksDueTimeAfterListing(t *testing.T) {
	// Another instance claimed the monitor after ListDueMonitorIDs took its snapshot.
	next := time.Now().Add(time.Hour)
	repo := &iqProbeDueTestRepo{monitor: &IQProbeMonitor{ID: 1, Enabled: true, IntervalMinutes: 60, NextRunAt: &next}}
	svc := NewIQProbeService(repo, nil, nil, nil, nil, nil)
	svc.RunDue(context.Background())
	require.Zero(t, repo.claims, "a future monitor must not be claimed again")
}
