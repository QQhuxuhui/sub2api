//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/xai"
	"github.com/stretchr/testify/require"
)

func keepAvailableTestAccount(keep bool) *Account {
	extra := map[string]any{}
	if keep {
		extra[KeepAvailableExtraKey] = true
	}
	return &Account{
		ID:          42,
		Platform:    PlatformOpenAI,
		Type:        AccountTypeAPIKey,
		Status:      StatusActive,
		Schedulable: true,
		Extra:       extra,
	}
}

func TestKeepAvailable_ReadsExtraFlag(t *testing.T) {
	require.False(t, (*Account)(nil).KeepAvailable())
	require.False(t, keepAvailableTestAccount(false).KeepAvailable())
	require.True(t, keepAvailableTestAccount(true).KeepAvailable())

	nonBool := keepAvailableTestAccount(false)
	nonBool.Extra[KeepAvailableExtraKey] = "true"
	require.False(t, nonBool.KeepAvailable(), "only a JSON boolean enables the switch")
}

func TestKeepAvailable_IgnoresUpstreamCooldowns(t *testing.T) {
	future := time.Now().Add(10 * time.Minute)
	for _, keep := range []bool{false, true} {
		for name, mutate := range map[string]func(*Account){
			"temp_unschedulable": func(a *Account) { a.TempUnschedulableUntil = &future },
			"rate_limited":       func(a *Account) { a.RateLimitResetAt = &future },
			"overloaded":         func(a *Account) { a.OverloadUntil = &future },
		} {
			account := keepAvailableTestAccount(keep)
			mutate(account)
			require.Equal(t, keep, account.IsSchedulable(), "%s keep=%v", name, keep)
		}
	}
}

func TestKeepAvailable_StillHonorsAdminSettings(t *testing.T) {
	disabled := keepAvailableTestAccount(true)
	disabled.Status = StatusDisabled
	require.False(t, disabled.IsSchedulable())

	manualOff := keepAvailableTestAccount(true)
	manualOff.Schedulable = false
	require.False(t, manualOff.IsSchedulable())

	past := time.Now().Add(-time.Minute)
	expired := keepAvailableTestAccount(true)
	expired.AutoPauseOnExpired = true
	expired.ExpiresAt = &past
	require.False(t, expired.IsSchedulable())
}

func TestKeepAvailable_IgnoresModelRateLimit(t *testing.T) {
	resetAt := time.Now().Add(10 * time.Minute).UTC().Format(time.RFC3339)
	limits := map[string]any{
		"gpt-5.6-terra": map[string]any{
			"rate_limited_at":     time.Now().UTC().Format(time.RFC3339),
			"rate_limit_reset_at": resetAt,
		},
	}

	normal := keepAvailableTestAccount(false)
	normal.Extra["model_rate_limits"] = limits
	require.False(t, normal.IsSchedulableForModel("gpt-5.6-terra"), "control: model limit must block a normal account")

	keep := keepAvailableTestAccount(true)
	keep.Extra["model_rate_limits"] = limits
	require.True(t, keep.IsSchedulableForModel("gpt-5.6-terra"))
}

func TestKeepAvailable_OpenAIRuntimeBlockIsNoop(t *testing.T) {
	svc := &OpenAIGatewayService{}
	until := time.Now().Add(10 * time.Minute)

	normal := keepAvailableTestAccount(false)
	svc.BlockAccountScheduling(normal, until, "transport_error")
	require.True(t, svc.isOpenAIAccountRuntimeBlocked(normal), "control: normal account gets blocked")

	keep := keepAvailableTestAccount(true)
	keep.ID = 43
	svc.BlockAccountScheduling(keep, until, "transport_error")
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(keep))
	require.False(t, svc.isOpenAIAccountRequestRuntimeBlocked(keep, "gpt-5.6-terra"))

	// 已在内存里的封禁（开关打开前写入）也不再生效。
	normal.Extra[KeepAvailableExtraKey] = true
	require.False(t, svc.isOpenAIAccountRuntimeBlocked(normal))
}

type keepAvailableRestoreRepo struct {
	mockAccountRepoForGemini
	calls []string
}

func (r *keepAvailableRestoreRepo) RestoreKeepAvailableState(context.Context, int64) (bool, error) {
	r.calls = append(r.calls, "RestoreKeepAvailableState")
	return true, nil
}

func TestKeepAvailable_RestoreClearsAutomaticStateOnly(t *testing.T) {
	restoration := []string{"RestoreKeepAvailableState"}

	errored := keepAvailableTestAccount(true)
	errored.Status = StatusError
	errored.Schedulable = false
	repo := &keepAvailableRestoreRepo{}
	require.NoError(t, (&adminServiceImpl{accountRepo: repo}).restoreKeepAvailableAccountState(context.Background(), errored))
	require.Equal(t, restoration, repo.calls,
		"clearing an error must preserve a manually disabled scheduling switch")

	disabled := keepAvailableTestAccount(true)
	disabled.Status = StatusDisabled
	repo = &keepAvailableRestoreRepo{}
	require.NoError(t, (&adminServiceImpl{accountRepo: repo}).restoreKeepAvailableAccountState(context.Background(), disabled))
	require.Equal(t, restoration, repo.calls, "manual disable must not be reverted")

	off := keepAvailableTestAccount(false)
	repo = &keepAvailableRestoreRepo{}
	require.NoError(t, (&adminServiceImpl{accountRepo: repo}).restoreKeepAvailableAccountState(context.Background(), off))
	require.Empty(t, repo.calls)
}

func TestKeepAvailable_GrokModelCooldown(t *testing.T) {
	model := "grok-4-keep-available-test"
	account := keepAvailableTestAccount(false)
	account.ID = 980041
	account.Platform = PlatformGrok
	account.Type = AccountTypeOAuth
	decision := GrokUpstreamFailureDecision{Model: model, Cooldown: time.Minute}
	require.True(t, persistGrokTransientModelCooldown(account, decision))
	require.Empty(t, filterGrokModelQuotaBlockedAccounts([]Account{*account}, model, time.Now()))
	account.Extra[KeepAvailableExtraKey] = true
	require.Len(t, filterGrokModelQuotaBlockedAccounts([]Account{*account}, model, time.Now()), 1,
		"enabling the switch bypasses a pre-existing model cooldown")
	account.ID++
	persistGrokTransientModelCooldown(account, decision)
	require.False(t, isGrokModelQuotaBlocked(account.ID, model, time.Now()),
		"new capacity failures must not install cooldowns")
}

func TestKeepAvailable_GrokTeamCooldown(t *testing.T) {
	account := keepAvailableTestAccount(false)
	account.Platform = PlatformGrok
	account.Type = AccountTypeOAuth
	account.Credentials = map[string]any{"team_id": "keep-available-regression-team"}
	model := "grok-4-test"
	markGrokTeamModelRateLimit(account, model, time.Now().Add(time.Minute))
	require.True(t, isGrokTeamModelRateLimited(account, model, time.Now()))
	account.Extra[KeepAvailableExtraKey] = true
	require.False(t, isGrokTeamModelRateLimited(account, model, time.Now()))
	require.Len(t, filterGrokTeamModelRateLimitedAccounts([]Account{*account}, model, time.Now()), 1)
}

func TestKeepAvailable_GrokUpstreamQuotaCooldown(t *testing.T) {
	retryAfter := 120
	zero, limit, reset := int64(0), int64(10), time.Now().Add(time.Minute).Unix()
	for _, snapshot := range []xai.QuotaSnapshot{
		{RetryAfterSeconds: &retryAfter, UpdatedAt: time.Now().UTC().Format(time.RFC3339)},
		{Requests: &xai.QuotaWindow{Limit: &limit, Remaining: &zero, ResetUnix: &reset}, UpdatedAt: time.Now().UTC().Format(time.RFC3339)},
	} {
		account := keepAvailableTestAccount(false)
		account.Platform, account.Type = PlatformGrok, AccountTypeOAuth
		account.Extra[grokQuotaSnapshotExtraKey] = snapshot
		paused, _ := shouldAutoPauseGrokAccountByQuota(account)
		require.True(t, paused)
		account.Extra[KeepAvailableExtraKey] = true
		paused, _ = shouldAutoPauseGrokAccountByQuota(account)
		require.False(t, paused)
	}
}

func TestKeepAvailable_PreservesConfiguredQuotaCap(t *testing.T) {
	account := keepAvailableTestAccount(true)
	account.Extra["quota_limit"] = float64(10)
	account.Extra["quota_used"] = float64(10)
	require.False(t, account.IsSchedulable())
}

type keepAvailableBulkRepo struct {
	accountRepoStubForBulkUpdate
	restored      []int64
	restoreErrors map[int64]error
}

func (r *keepAvailableBulkRepo) RestoreKeepAvailableState(_ context.Context, id int64) (bool, error) {
	r.restored = append(r.restored, id)
	return r.restoreErrors[id] == nil, r.restoreErrors[id]
}

func TestKeepAvailable_BulkEnableRestoresEveryAccount(t *testing.T) {
	repo := &keepAvailableBulkRepo{}
	repo.getByIDAccounts = map[int64]*Account{}
	for _, id := range []int64{41, 42} {
		account := keepAvailableTestAccount(true)
		account.ID = id
		repo.getByIDAccounts[id] = account
	}
	svc := &adminServiceImpl{accountRepo: repo}
	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{41, 42}, Extra: map[string]any{KeepAvailableExtraKey: true},
	})
	require.NoError(t, err)
	require.Equal(t, true, repo.lastBulkUpdate.Extra[KeepAvailableExtraKey])
	require.Equal(t, []int64{41, 42}, repo.restored)
	require.Equal(t, []int64{41, 42}, result.SuccessIDs)
	require.Zero(t, result.Failed)
}

func TestKeepAvailable_BulkRestoreReportsPerAccountFailures(t *testing.T) {
	repo := &keepAvailableBulkRepo{restoreErrors: map[int64]error{42: errors.New("restore failed")}}
	repo.getByIDAccounts = map[int64]*Account{}
	for _, id := range []int64{41, 42, 43} {
		account := keepAvailableTestAccount(true)
		account.ID = id
		repo.getByIDAccounts[id] = account
	}
	repo.getByIDErrByID = map[int64]error{41: errors.New("read failed")}
	svc := &adminServiceImpl{accountRepo: repo}
	result, err := svc.BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
		AccountIDs: []int64{41, 42, 43}, Extra: map[string]any{KeepAvailableExtraKey: true},
	})
	require.NoError(t, err)
	require.Equal(t, []int64{41, 42}, result.FailedIDs)
	require.Equal(t, []int64{43}, result.SuccessIDs)
	require.Equal(t, 2, result.Failed)
	require.Equal(t, 1, result.Success)
	require.Equal(t, "read failed", result.Results[0].Error)
	require.Equal(t, "restore failed", result.Results[1].Error)
}

func TestKeepAvailable_BulkOtherEditsDoNotRestore(t *testing.T) {
	for _, extra := range []map[string]any{nil, {KeepAvailableExtraKey: false}, {KeepAvailableExtraKey: "true"}} {
		repo := &keepAvailableBulkRepo{}
		result, err := (&adminServiceImpl{accountRepo: repo}).BulkUpdateAccounts(context.Background(), &BulkUpdateAccountsInput{
			AccountIDs: []int64{42}, Name: "renamed", Extra: extra,
		})
		require.NoError(t, err)
		require.Equal(t, 1, result.Success)
		require.Empty(t, repo.restored)
		require.Empty(t, repo.getByIDCalled)
	}
}
