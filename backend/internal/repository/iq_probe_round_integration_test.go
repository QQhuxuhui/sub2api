//go:build integration

package repository

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// iqProbeFakeUpstream answers OpenAI Responses requests by looking at the question.
type iqProbeFakeUpstream struct {
	mu      sync.Mutex
	prompts []string
}

func (u *iqProbeFakeUpstream) Do(req *http.Request, proxyURL string, accountID int64, c int) (*http.Response, error) {
	return u.DoWithTLS(req, proxyURL, accountID, c, nil)
}

func (u *iqProbeFakeUpstream) DoWithTLS(req *http.Request, _ string, _ int64, _ int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	body, _ := io.ReadAll(req.Body)
	prompt := gjson.GetBytes(body, "input.@reverse.0.content.0.text").String()
	u.mu.Lock()
	u.prompts = append(u.prompts, prompt)
	u.mu.Unlock()

	sse := func(text string) *http.Response {
		events := fmt.Sprintf("data: {\"type\":\"response.output_text.delta\",\"delta\":%q}\n\n", text) +
			"data: {\"type\":\"response.completed\",\"response\":{\"model\":\"gpt-5.4-mini-2026\",\"usage\":{\"input_tokens\":100,\"output_tokens\":20}}}\n\n"
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(events))}
	}
	switch {
	case strings.Contains(prompt, "1+1"):
		return sse("想一想……1+1\n答案：2"), nil
	case strings.Contains(prompt, "pick"):
		return sse("答案：A"), nil
	default:
		return &http.Response{StatusCode: http.StatusInternalServerError, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"boom"}}`))}, nil
	}
}

func TestIQProbeRoundEndToEnd(t *testing.T) {
	ctx := context.Background()
	client := testEntClient(t)
	suffix := time.Now().UnixNano()

	account, err := client.Account.Create().
		SetName(fmt.Sprintf("iq-round-acc-%d", suffix)).
		SetPlatform(service.PlatformOpenAI).
		SetType(service.AccountTypeAPIKey).
		SetStatus(service.StatusActive).
		SetSchedulable(true).
		SetConcurrency(1).
		SetCredentials(map[string]any{"api_key": "k", "base_url": "https://relay.example.com"}).
		SetExtra(map[string]any{}).
		Save(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM iq_probe_monitors WHERE account_id = $1", account.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM iq_probe_question_sets WHERE name = $1", fmt.Sprintf("iq-round-%d", suffix))
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", account.ID)
	})

	upstream := &iqProbeFakeUpstream{}
	accountRepo := newAccountRepositoryWithSQL(client, integrationDB, nil)
	testSvc := service.NewAccountTestService(accountRepo, nil, nil, nil, nil, upstream, &config.Config{}, nil)
	svc := service.NewIQProbeService(NewIQProbeRepository(integrationDB), accountRepo, testSvc, nil, nil, nil)

	set, err := svc.ImportSet(ctx, &service.IQProbeSetExport{
		Name: fmt.Sprintf("iq-round-%d", suffix),
		Questions: []service.IQProbeQuestionExport{
			{Title: "add", Prompt: "1+1=?", ExpectedAnswer: "2", JudgeType: "numeric", Weight: 1},
			{Title: "choice", Prompt: "pick one", ExpectedAnswer: "B", JudgeType: "choice", Weight: 2},
			{Title: "broken", Prompt: "boom", ExpectedAnswer: "x", JudgeType: "exact", Weight: 1},
		},
	})
	require.NoError(t, err)
	require.Equal(t, 3, set.QuestionCount)

	created, skipped, err := svc.CreateMonitors(ctx, &service.IQProbeMonitor{
		Model: "gpt-5.5", SetID: set.ID, IntervalMinutes: 60, Enabled: true,
		AlertEnabled: true, AlertThreshold: 0.7, AlertLowRounds: 1, AlertInvalidRounds: 3,
	}, []int64{account.ID})
	require.NoError(t, err)
	require.Empty(t, skipped)
	require.Len(t, created, 1)
	monitorID := created[0].ID

	require.NoError(t, svc.RunNow(ctx, monitorID))
	require.ErrorIs(t, svc.RunNow(ctx, monitorID), service.ErrIQProbeMonitorBusy)

	var m *service.IQProbeMonitor
	require.Eventually(t, func() bool {
		m, err = svc.GetMonitor(ctx, monitorID)
		return err == nil && m.LastStatus != ""
	}, 10*time.Second, 50*time.Millisecond)

	require.Equal(t, service.IQProbeRunScored, m.LastStatus)
	require.NotNil(t, m.LastScore)
	require.InDelta(t, 1.0/3.0, *m.LastScore, 1e-9, "1 of weight 3; the failed request is outside the denominator")
	require.Equal(t, service.IQProbeAlertLow, m.AlertState)
	require.Equal(t, 1, m.ConsecutiveLow)

	require.Len(t, upstream.prompts, 3)
	for _, p := range upstream.prompts {
		require.Contains(t, p, "答案：<你的最终答案>", "answer-format instruction appended")
	}

	matrix, err := svc.GetMatrix(ctx, monitorID, 10)
	require.NoError(t, err)
	require.Len(t, matrix.Runs, 1)
	run := matrix.Runs[0]
	require.Equal(t, "manual", run.Trigger)
	require.Equal(t, 1, run.CorrectCount)
	require.Equal(t, 1, run.WrongCount)
	require.Equal(t, 1, run.ErrorCount)
	require.EqualValues(t, 200, run.InputTokens)
	require.Len(t, matrix.Answers, 3)
	verdicts := map[string]string{}
	for _, a := range matrix.Answers {
		verdicts[a.QuestionTitle] = a.Verdict
		if a.Verdict != service.IQProbeVerdictError {
			require.Equal(t, "gpt-5.5", a.UpstreamModel)
			require.Equal(t, "gpt-5.4-mini-2026", a.ResponseModel)
		}
	}
	require.Equal(t, map[string]string{"add": "correct", "choice": "wrong", "broken": "error"}, verdicts)

	// Probe errors never touch the account.
	var status string
	require.NoError(t, integrationDB.QueryRowContext(ctx, "SELECT status FROM accounts WHERE id = $1", account.ID).Scan(&status))
	require.Equal(t, service.StatusActive, status)
}
