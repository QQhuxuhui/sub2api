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

type iqProbeFinishTestRepo struct {
	IQProbeRepository
	monitor    *IQProbeMonitor
	stateSets  []IQProbeMonitorRunState
	createErr  error
	finishLost bool
}

func (r *iqProbeFinishTestRepo) GetMonitor(context.Context, int64) (*IQProbeMonitor, error) {
	if r.monitor == nil {
		return nil, ErrIQProbeMonitorNotFound
	}
	return r.monitor, nil
}

func (r *iqProbeFinishTestRepo) FinishRun(_ context.Context, _ *IQProbeRun, state *IQProbeMonitorRunState) (bool, error) {
	if r.finishLost {
		return false, nil
	}
	if state != nil {
		r.stateSets = append(r.stateSets, *state)
	}
	return true, nil
}

func (r *iqProbeFinishTestRepo) CreateRun(context.Context, *IQProbeRun) (int64, error) {
	return 1, r.createErr
}

func (r *iqProbeFinishTestRepo) UpdateMonitorRunState(_ context.Context, _ int64, st IQProbeMonitorRunState) (bool, error) {
	r.stateSets = append(r.stateSets, st)
	return true, nil
}

func TestIQProbeFinishRound_IgnoresRoundWhoseConfigurationChanged(t *testing.T) {
	score := 0.1
	started := &IQProbeMonitor{ID: 1, Model: "gpt-old", SetID: 1, AlertEnabled: true, AlertThreshold: 0.7, AlertLowRounds: 1}
	run := &IQProbeRun{ID: 9, Status: IQProbeRunScored, Score: &score, StartedAt: time.Now()}

	for name, edit := range map[string]func(m *IQProbeMonitor){
		"model":            func(m *IQProbeMonitor) { m.Model = "gpt-new" },
		"question set":     func(m *IQProbeMonitor) { m.SetID = 2 },
		"reasoning effort": func(m *IQProbeMonitor) { m.ReasoningEffort = "high" },
	} {
		t.Run(name, func(t *testing.T) {
			edited := *started
			edit(&edited)
			repo := &iqProbeFinishTestRepo{monitor: &edited}
			NewIQProbeService(repo, nil, nil, nil, nil, nil).finishRound(context.Background(), started, run)
			require.Empty(t, repo.stateSets, "an orphaned round must not write score or streaks onto the new configuration")
		})
	}

	t.Run("threshold only: the round still counts", func(t *testing.T) {
		edited := *started
		edited.AlertThreshold = 0.5
		repo := &iqProbeFinishTestRepo{monitor: &edited}
		NewIQProbeService(repo, nil, nil, nil, nil, nil).finishRound(context.Background(), started, run)
		require.Len(t, repo.stateSets, 1)
		st := repo.stateSets[0]
		require.Equal(t, "gpt-old", st.Model, "the write is conditioned on what the round measured")
		require.Equal(t, int64(1), st.SetID)
		require.Equal(t, 1, st.ConsecutiveLow)
	})

	t.Run("monitor deleted", func(t *testing.T) {
		repo := &iqProbeFinishTestRepo{}
		NewIQProbeService(repo, nil, nil, nil, nil, nil).finishRound(context.Background(), started, run)
		require.Empty(t, repo.stateSets)
	})
}

func TestIQProbeRunNow_ReportsRoundRunningOnAnotherInstance(t *testing.T) {
	repo := &iqProbeFinishTestRepo{monitor: &IQProbeMonitor{ID: 1, Model: "m", SetID: 1}, createErr: ErrIQProbeMonitorBusy}
	svc := NewIQProbeService(repo, nil, nil, nil, nil, nil)
	_, err := svc.RunNow(context.Background(), 1)
	require.ErrorIs(t, err, ErrIQProbeMonitorBusy)
	require.True(t, svc.tryMarkRunning(1), "the local mark is released when the claim fails")
}

func TestIQProbeFinishRound_LostLeaseDoesNotUpdateMonitor(t *testing.T) {
	m := &IQProbeMonitor{ID: 1, Model: "m", SetID: 1}
	repo := &iqProbeFinishTestRepo{monitor: m, finishLost: true}
	score := 0.0
	NewIQProbeService(repo, nil, nil, nil, nil, nil).finishRound(context.Background(), m, &IQProbeRun{ID: 9, Status: IQProbeRunScored, Score: &score})
	require.Empty(t, repo.stateSets)
}
