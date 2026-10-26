//go:build integration

package repository

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestIQProbeRepositoryLifecycle(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	repo := NewIQProbeRepository(integrationDB)
	suffix := time.Now().UnixNano()

	group, err := client.Group.Create().SetName(fmt.Sprintf("iq-probe-%d", suffix)).SetPlatform(service.PlatformOpenAI).Save(ctx)
	require.NoError(t, err)
	account, err := client.Account.Create().
		SetName(fmt.Sprintf("iq-probe-acc-%d", suffix)).
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetCredentials(map[string]any{"api_key": "k"}).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM iq_probe_monitors WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM iq_probe_question_sets WHERE name LIKE $1", fmt.Sprintf("iq-%d%%", suffix))
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM groups WHERE id = $1", group.ID)
	})

	// Sets and questions
	set, err := repo.CreateSet(ctx, &service.IQProbeQuestionSet{Name: fmt.Sprintf("iq-%d-set", suffix), Description: "d"})
	require.NoError(t, err)
	q1, err := repo.CreateQuestion(ctx, &service.IQProbeQuestion{SetID: set.ID, Title: "a", Prompt: "1+1", ExpectedAnswer: "2", JudgeType: service.IQProbeJudgeNumeric, Weight: 1, Enabled: true, SortOrder: 1})
	require.NoError(t, err)
	_, err = repo.CreateQuestion(ctx, &service.IQProbeQuestion{SetID: set.ID, Title: "b", Prompt: "pick", ExpectedAnswer: "B", JudgeType: service.IQProbeJudgeChoice, Weight: 2, Enabled: false, SortOrder: 0})
	require.NoError(t, err)
	questions, err := repo.ListQuestions(ctx, set.ID)
	require.NoError(t, err)
	require.Len(t, questions, 2)
	require.Equal(t, "b", questions[0].Title, "ordered by sort_order")

	q1.ExpectedAnswer = "2||二"
	updated, err := repo.UpdateQuestion(ctx, q1)
	require.NoError(t, err)
	require.Equal(t, "2||二", updated.ExpectedAnswer)

	// Monitors
	next := time.Now().Add(-time.Minute).UTC().Truncate(time.Microsecond)
	m, err := repo.CreateMonitor(ctx, &service.IQProbeMonitor{
		AccountID: account.ID, GroupID: &group.ID, Model: "gpt-5.5", SetID: set.ID, IntervalMinutes: 60,
		Enabled: true, AlertThreshold: 0.7, AlertLowRounds: 2, AlertInvalidRounds: 3, NextRunAt: &next,
	})
	require.NoError(t, err)
	require.Equal(t, account.Name, m.AccountName)
	require.Equal(t, group.Name, m.GroupName)
	require.Equal(t, set.Name, m.SetName)
	require.Equal(t, service.IQProbeAlertOK, m.AlertState)

	_, err = repo.CreateMonitor(ctx, &service.IQProbeMonitor{AccountID: account.ID, Model: "gpt-5.5", SetID: set.ID, IntervalMinutes: 60, AlertThreshold: 0.7, AlertLowRounds: 2, AlertInvalidRounds: 3})
	require.ErrorIs(t, err, service.ErrIQProbeMonitorDuplicate)

	got, err := repo.GetSet(ctx, set.ID)
	require.NoError(t, err)
	require.Equal(t, 2, got.QuestionCount)
	require.Equal(t, 1, got.EnabledCount)
	require.Equal(t, 1, got.MonitorCount)
	require.ErrorIs(t, repo.DeleteSet(ctx, set.ID), service.ErrIQProbeSetInUse)

	// Claiming a due round succeeds exactly once.
	due, err := repo.ListDueMonitorIDs(ctx, time.Now(), 100)
	require.NoError(t, err)
	require.Contains(t, due, m.ID)
	newNext := time.Now().Add(time.Hour)
	ok, err := repo.ClaimMonitor(ctx, m.ID, m.NextRunAt, newNext)
	require.NoError(t, err)
	require.True(t, ok)
	ok, err = repo.ClaimMonitor(ctx, m.ID, m.NextRunAt, newNext.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, ok, "second instance must lose the claim")
	// A second instance may read the updated timestamp after listing due IDs.
	fresh, err := repo.GetMonitor(ctx, m.ID)
	require.NoError(t, err)
	ok, err = repo.ClaimMonitor(ctx, m.ID, fresh.NextRunAt, newNext.Add(time.Minute))
	require.NoError(t, err)
	require.False(t, ok, "matching the timestamp is insufficient when the monitor is no longer due")
	_, err = integrationDB.ExecContext(ctx, "UPDATE iq_probe_monitors SET enabled = FALSE, next_run_at = NULL WHERE id = $1", m.ID)
	require.NoError(t, err)
	ok, err = repo.ClaimMonitor(ctx, m.ID, nil, newNext)
	require.NoError(t, err)
	require.False(t, ok, "a monitor disabled after listing must not be claimed")
	_, err = integrationDB.ExecContext(ctx, "UPDATE iq_probe_monitors SET enabled = TRUE WHERE id = $1", m.ID)
	require.NoError(t, err)
	ok, err = repo.ClaimMonitor(ctx, m.ID, nil, newNext)
	require.NoError(t, err)
	require.True(t, ok, "enabled monitors with no scheduled timestamp remain eligible")

	// Runs and answers
	run := &service.IQProbeRun{MonitorID: m.ID, AccountID: account.ID, Model: m.Model, SetID: set.ID, Trigger: "schedule", Status: service.IQProbeRunRunning, StartedAt: time.Now()}
	run.ID, err = repo.CreateRun(ctx, run)
	require.NoError(t, err)
	require.NoError(t, repo.InsertAnswer(ctx, &service.IQProbeAnswer{RunID: run.ID, QuestionID: q1.ID, QuestionTitle: "a", ExpectedAnswer: "2", JudgeType: "numeric", Weight: 1, Verdict: service.IQProbeVerdictCorrect, ExtractedAnswer: "2", ResponseText: "答案：2", InputTokens: 10, OutputTokens: 3}))
	score := 1.0
	finished := time.Now()
	run.Status, run.Score, run.CorrectCount, run.TotalCount, run.FinishedAt = service.IQProbeRunScored, &score, 1, 1, &finished
	require.NoError(t, repo.FinishRun(ctx, run))
	require.NoError(t, repo.UpdateMonitorRunState(ctx, m.ID, service.IQProbeMonitorRunState{LastRunAt: run.StartedAt, LastStatus: run.Status, LastScore: &score, AlertState: service.IQProbeAlertOK}))

	runs, total, err := repo.ListRuns(ctx, service.IQProbeRunFilter{MonitorID: m.ID, Status: service.IQProbeRunScored, Page: 1, PageSize: 10})
	require.NoError(t, err)
	require.EqualValues(t, 1, total)
	require.Equal(t, account.Name, runs[0].AccountName)
	require.InDelta(t, 1.0, *runs[0].Score, 1e-9)

	answers, err := repo.ListAnswersByRuns(ctx, []int64{run.ID}, false)
	require.NoError(t, err)
	require.Len(t, answers, 1)
	require.Empty(t, answers[0].ResponseText, "matrix listing omits reply text")
	full, err := repo.GetAnswer(ctx, answers[0].ID)
	require.NoError(t, err)
	require.Equal(t, "答案：2", full.ResponseText)

	points, err := repo.ListScorePoints(ctx, time.Now().Add(-time.Hour))
	require.NoError(t, err)
	require.Len(t, points[m.ID], 1)

	mon, err := repo.GetMonitor(ctx, m.ID)
	require.NoError(t, err)
	require.Equal(t, service.IQProbeRunScored, mon.LastStatus)
	require.NotNil(t, mon.LastScore)

	// Stale running rounds are closed; pruning removes old rows.
	stale := &service.IQProbeRun{MonitorID: m.ID, AccountID: account.ID, Model: m.Model, SetID: set.ID, Trigger: "schedule", Status: service.IQProbeRunRunning, StartedAt: time.Now().Add(-3 * time.Hour)}
	stale.ID, err = repo.CreateRun(ctx, stale)
	require.NoError(t, err)
	require.NoError(t, repo.MarkStaleRunsInvalid(ctx, time.Now().Add(-2*time.Hour)))
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT status FROM iq_probe_runs WHERE id = $1", stale.ID).Scan(&status))
	require.Equal(t, service.IQProbeRunInvalid, status)

	n, err := repo.PruneRuns(ctx, time.Now().Add(-2*time.Hour))
	require.NoError(t, err)
	require.EqualValues(t, 1, n)

	// Deleting the monitor frees the set.
	require.NoError(t, repo.DeleteMonitor(ctx, m.ID))
	require.NoError(t, repo.DeleteSet(ctx, set.ID))
	_, err = repo.GetSet(ctx, set.ID)
	require.ErrorIs(t, err, service.ErrIQProbeSetNotFound)
}
