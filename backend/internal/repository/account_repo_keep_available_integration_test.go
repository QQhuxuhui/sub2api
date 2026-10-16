//go:build integration

package repository

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

func TestAccountKeepAvailableStateWrites(t *testing.T) {
	ctx := context.Background()
	until := time.Now().Add(time.Hour)
	type mutation func(*accountRepository, *service.Account) error
	mutations := map[string]mutation{
		"error":            func(r *accountRepository, a *service.Account) error { return r.SetError(ctx, a.ID, "upstream auth") },
		"rate_limit":       func(r *accountRepository, a *service.Account) error { return r.SetRateLimited(ctx, a.ID, until) },
		"rate_limit_later": func(r *accountRepository, a *service.Account) error { return r.SetRateLimitedIfLater(ctx, a.ID, until) },
		"rate_limit_cas": func(r *accountRepository, a *service.Account) error {
			_, err := r.SetRateLimitedIfUnchanged(ctx, a.ID, a.UpdatedAt, nil, nil, until)
			return err
		},
		"overload": func(r *accountRepository, a *service.Account) error { return r.SetOverloaded(ctx, a.ID, until) },
		"temporary": func(r *accountRepository, a *service.Account) error {
			return r.SetTempUnschedulable(ctx, a.ID, until, "upstream")
		},
		"model": func(r *accountRepository, a *service.Account) error {
			return r.SetModelRateLimit(ctx, a.ID, "grok-test", until)
		},
		"grok_refresh_error": func(r *accountRepository, a *service.Account) error {
			_, err := r.SetGrokOAuthRefreshErrorIfCredentialsUnchanged(ctx, a.ID, a.Credentials, nil, "invalid_grant")
			return err
		},
		"grok_reconciliation_error": func(r *accountRepository, a *service.Account) error {
			_, err := r.SetGrokOAuthErrorIfCredentialsUnchanged(ctx, a.ID, a.Credentials, "missing refresh token")
			return err
		},
		"grok_refresh_temporary": func(r *accountRepository, a *service.Account) error {
			_, err := r.SetGrokOAuthRefreshTempUnschedulableIfCredentialsUnchanged(ctx, a.ID, a.Credentials, nil, until, "upstream")
			return err
		},
		"grok_credential_error": func(r *accountRepository, a *service.Account) error {
			creds, err := json.Marshal(a.Credentials)
			if err != nil {
				return err
			}
			_, err = r.SetGrokCredentialErrorIfMatch(ctx, a.ID, service.GrokCredentialMutationSnapshot{CredentialsJSON: string(creds)}, "upstream")
			return err
		},
		"grok_credential_temporary": func(r *accountRepository, a *service.Account) error {
			creds, err := json.Marshal(a.Credentials)
			if err != nil {
				return err
			}
			_, err = r.SetGrokCredentialTempUnschedulableIfMatch(ctx, a.ID, service.GrokCredentialMutationSnapshot{CredentialsJSON: string(creds)}, until, "upstream")
			return err
		},
	}
	for name, mutate := range mutations {
		for _, flag := range []struct {
			name  string
			value any
			keep  bool
		}{
			{"enabled", true, true}, {"disabled", false, false}, {"string", "true", false}, {"null", nil, false},
		} {
			t.Run(name+"/"+flag.name, func(t *testing.T) {
				tx := testEntTx(t)
				r := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
				a := mustCreateAccount(t, tx.Client(), &service.Account{
					Name: "keep-available", Platform: service.PlatformGrok, Type: service.AccountTypeOAuth,
					Status: service.StatusActive, Schedulable: true,
					Credentials: map[string]any{"access_token": "test-token"},
					Extra:       map[string]any{service.KeepAvailableExtraKey: flag.value},
				})
				a, err := r.GetByID(ctx, a.ID)
				require.NoError(t, err)
				require.NoError(t, mutate(r, a))
				got, err := r.GetByID(ctx, a.ID)
				require.NoError(t, err)
				changed := got.Status != a.Status || got.Schedulable != a.Schedulable ||
					got.RateLimitResetAt != nil || got.OverloadUntil != nil || got.TempUnschedulableUntil != nil || got.Extra["model_rate_limits"] != nil
				require.Equal(t, !flag.keep, changed, "only a boolean true suppresses upstream state writes")
			})
		}
	}
}

// Hold the enabling UPDATE uncommitted while a failing request tries to write.
// The old SELECT-then-UPDATE implementation sees false and overwrites the
// account after commit. An UPDATE predicate is rechecked after the row lock.
func TestAccountKeepAvailableConcurrentEnable(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client := testEntClient(t)
	a := mustCreateAccount(t, client, &service.Account{Name: "keep-available-race", Status: service.StatusActive, Schedulable: true})
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM scheduler_outbox WHERE account_id = $1", a.ID)
		_, _ = integrationDB.ExecContext(context.Background(), "DELETE FROM accounts WHERE id = $1", a.ID)
	})
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer tx.Rollback()
	var blockerPID int
	require.NoError(t, tx.QueryRowContext(ctx, "SELECT pg_backend_pid()").Scan(&blockerPID))
	_, err = tx.ExecContext(ctx, `UPDATE accounts SET extra = '{"keep_available":true}'::jsonb WHERE id = $1`, a.ID)
	require.NoError(t, err)
	done := make(chan error, 1)
	go func() {
		done <- newAccountRepositoryWithSQL(client, integrationDB, nil).SetError(ctx, a.ID, "late upstream error")
	}()
	require.Eventually(t, func() bool {
		var waiting bool
		err := integrationDB.QueryRowContext(ctx, `SELECT EXISTS (
			SELECT 1 FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))
		)`, blockerPID).Scan(&waiting)
		return err == nil && waiting
	}, 5*time.Second, 10*time.Millisecond)
	require.NoError(t, tx.Commit())
	require.NoError(t, <-done)
	got, err := newAccountRepositoryWithSQL(client, integrationDB, nil).GetByID(ctx, a.ID)
	require.NoError(t, err)
	require.Equal(t, service.StatusActive, got.Status)
	require.True(t, got.Schedulable)
}

func TestAccountKeepAvailableRestoreState(t *testing.T) {
	for _, tc := range []struct {
		name, status, wantStatus string
		schedulable, keep        bool
	}{
		{"manually_paused_error", service.StatusError, service.StatusActive, false, true},
		{"enabled_error", service.StatusError, service.StatusActive, true, true},
		{"disabled_after_snapshot", service.StatusDisabled, service.StatusDisabled, true, true},
		{"turned_off_after_snapshot", service.StatusError, service.StatusError, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx := testEntTx(t)
			r := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
			restorer, ok := any(r).(interface {
				RestoreKeepAvailableState(context.Context, int64) (bool, error)
			})
			require.True(t, ok, "restore must use a conditional repository mutation")
			until := time.Now().Add(time.Hour)
			a := mustCreateAccount(t, tx.Client(), &service.Account{
				Name: "keep-restore", Status: tc.status, Schedulable: tc.schedulable,
				Extra: map[string]any{service.KeepAvailableExtraKey: tc.keep, "model_rate_limits": map[string]any{"model": true}, "quota_used": float64(5)},
			})
			_, err := tx.Client().Account.UpdateOneID(a.ID).SetStatus(tc.status).SetSchedulable(tc.schedulable).
				SetErrorMessage("original error").SetRateLimitResetAt(until).SetOverloadUntil(until).
				SetTempUnschedulableUntil(until).Save(context.Background())
			require.NoError(t, err)
			applied, err := restorer.RestoreKeepAvailableState(context.Background(), a.ID)
			require.NoError(t, err)
			require.Equal(t, tc.keep, applied)
			got, err := r.GetByID(context.Background(), a.ID)
			require.NoError(t, err)
			require.Equal(t, tc.wantStatus, got.Status)
			require.Equal(t, tc.schedulable, got.Schedulable)
			require.Equal(t, float64(5), got.Extra["quota_used"])
			if tc.keep {
				require.Nil(t, got.RateLimitResetAt)
				require.Nil(t, got.OverloadUntil)
				require.Nil(t, got.TempUnschedulableUntil)
				require.Nil(t, got.Extra["model_rate_limits"])
			} else {
				require.NotNil(t, got.RateLimitResetAt)
				require.NotNil(t, got.TempUnschedulableUntil)
				require.NotNil(t, got.Extra["model_rate_limits"])
			}
		})
	}
}

func TestAccountKeepAvailableBulkEnableRestoresScheduling(t *testing.T) {
	ctx := context.Background()
	tx := testEntTx(t)
	r := newAccountRepositoryWithSQL(tx.Client(), tx, nil)
	admin := service.NewAdminService(
		nil, nil, nil, r, nil, nil, nil, nil, nil, nil, nil, nil,
		nil, tx.Client(), nil, nil, nil, nil, nil, nil, nil, nil, nil,
	)
	ids := make([]int64, 0, 4)
	for _, status := range []string{service.StatusActive, service.StatusError, service.StatusDisabled} {
		a := mustCreateAccount(t, tx.Client(), &service.Account{
			Name: "bulk-keep-available", Platform: service.PlatformOpenAI,
			Type: service.AccountTypeAPIKey, Status: status, Schedulable: true,
			Extra: map[string]any{"quota_used": float64(5)},
		})
		_, err := tx.Client().Account.UpdateOneID(a.ID).SetStatus(status).SetSchedulable(true).
			SetRateLimitResetAt(time.Now().Add(time.Hour)).Save(ctx)
		require.NoError(t, err)
		ids = append(ids, a.ID)
	}
	paused := mustCreateAccount(t, tx.Client(), &service.Account{
		Name: "manual-pause", Platform: service.PlatformOpenAI, Status: service.StatusActive,
	})
	_, err := tx.Client().Account.UpdateOneID(paused.ID).SetSchedulable(false).Save(ctx)
	require.NoError(t, err)
	ids = append(ids, paused.ID)

	result, err := admin.BulkUpdateAccounts(ctx, &service.BulkUpdateAccountsInput{
		AccountIDs: ids, Extra: map[string]any{service.KeepAvailableExtraKey: true},
	})
	require.NoError(t, err)
	require.Equal(t, ids, result.SuccessIDs)
	require.Zero(t, result.Failed)
	for _, id := range ids[:3] {
		a, err := r.GetByID(ctx, id)
		require.NoError(t, err)
		require.True(t, a.KeepAvailable())
		require.Nil(t, a.RateLimitResetAt)
		require.Equal(t, float64(5), a.Extra["quota_used"])
	}
	accounts, err := r.ListSchedulableByPlatform(ctx, service.PlatformOpenAI)
	require.NoError(t, err)
	schedulable := map[int64]bool{}
	for _, a := range accounts {
		schedulable[a.ID] = true
	}
	require.True(t, schedulable[ids[0]], "cooled account must immediately return to the scheduling query")
	require.True(t, schedulable[ids[1]], "automatic error must be restored")
	require.False(t, schedulable[ids[2]], "manual disable remains in effect")
	require.False(t, schedulable[ids[3]], "manual pause remains in effect")
}
