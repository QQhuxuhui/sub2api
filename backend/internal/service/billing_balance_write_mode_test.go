//go:build unit

package service

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type balanceWriteSettingStub struct {
	SettingRepository
	value string
	err   error
	calls int
}

func (s *balanceWriteSettingStub) GetValue(context.Context, string) (string, error) {
	s.calls++
	return s.value, s.err
}

func TestParseBillingBalanceWriteConfig(t *testing.T) {
	for _, tc := range []struct {
		name    string
		raw     string
		mode    string
		users   []int64
		wantErr bool
	}{
		{name: "empty", raw: "  ", mode: BillingBalanceWriteModeSync},
		{name: "sync", raw: `{"mode":"sync"}`, mode: BillingBalanceWriteModeSync},
		{name: "mode missing", raw: `{"user_ids":[14]}`, mode: BillingBalanceWriteModeSync, users: []int64{14}},
		{name: "list", raw: `{"mode":"list","user_ids":[14,15]}`, mode: BillingBalanceWriteModeList, users: []int64{14, 15}},
		{name: "all", raw: `{"mode":" ALL "}`, mode: BillingBalanceWriteModeAll},
		{name: "malformed json", raw: `{"mode":`, mode: BillingBalanceWriteModeSync, wantErr: true},
		{name: "wrong type", raw: `{"mode":"list","user_ids":"14"}`, mode: BillingBalanceWriteModeSync, wantErr: true},
		{name: "unknown mode", raw: `{"mode":"later","user_ids":[14]}`, mode: BillingBalanceWriteModeSync, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := ParseBillingBalanceWriteConfig(tc.raw)
			require.Equal(t, tc.wantErr, err != nil)
			require.Equal(t, tc.mode, cfg.Mode)
			if !tc.wantErr {
				require.Equal(t, tc.users, cfg.UserIDs)
			}
		})
	}
}

func TestBillingBalanceWriteMode_ShouldDefer(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name  string
		value string
		err   error
		user  int64
		want  bool
	}{
		{name: "setting missing", err: ErrSettingNotFound, user: 14},
		{name: "sync", value: `{"mode":"sync","user_ids":[14]}`, user: 14},
		{name: "listed", value: `{"mode":"list","user_ids":[14]}`, user: 14, want: true},
		{name: "not listed", value: `{"mode":"list","user_ids":[14]}`, user: 15},
		{name: "all", value: `{"mode":"all"}`, user: 15, want: true},
		{name: "malformed", value: `not json`, user: 14},
		{name: "first read fails", err: errors.New("db down"), user: 14},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewBillingBalanceWriteModeService(&balanceWriteSettingStub{value: tc.value, err: tc.err})
			require.Equal(t, tc.want, svc.ShouldDefer(ctx, tc.user))
		})
	}

	var missing *BillingBalanceWriteModeService
	require.False(t, missing.ShouldDefer(ctx, 14))
}

func TestBillingBalanceWriteMode_ReadFailureKeepsPreviousSnapshot(t *testing.T) {
	ctx := context.Background()
	settings := &balanceWriteSettingStub{value: `{"mode":"list","user_ids":[14]}`}
	svc := NewBillingBalanceWriteModeService(settings)
	require.True(t, svc.ShouldDefer(ctx, 14))

	settings.err = errors.New("db down")
	mode, err := svc.Refresh(ctx)
	require.Error(t, err)
	require.Equal(t, BillingBalanceWriteModeList, mode)
	require.True(t, svc.ShouldDefer(ctx, 14))
}

func TestBillingBalanceWriteMode_InvalidValueReplacesPreviousSnapshotWithSync(t *testing.T) {
	ctx := context.Background()
	settings := &balanceWriteSettingStub{value: `{"mode":"all"}`}
	svc := NewBillingBalanceWriteModeService(settings)
	require.True(t, svc.ShouldDefer(ctx, 14))

	settings.value = `{"mode":`
	mode, err := svc.Refresh(ctx)
	require.NoError(t, err)
	require.Equal(t, BillingBalanceWriteModeSync, mode)
	require.False(t, svc.ShouldDefer(ctx, 14))
}

func TestBillingBalanceWriteMode_RefreshesAfterTTLWithoutBlocking(t *testing.T) {
	ctx := context.Background()
	settings := &balanceWriteSettingStub{value: `{"mode":"sync"}`}
	now := time.Unix(1000, 0)
	svc := NewBillingBalanceWriteModeService(settings)
	svc.now = func() time.Time { return now }

	require.False(t, svc.ShouldDefer(ctx, 14))
	settings.value = `{"mode":"all"}`

	now = now.Add(billingBalanceWriteModeTTL - time.Second)
	require.False(t, svc.ShouldDefer(ctx, 14), "the snapshot holds for 30 seconds")
	require.Equal(t, 1, settings.calls)

	now = now.Add(time.Second)
	require.False(t, svc.ShouldDefer(ctx, 14), "the request that notices keeps the old snapshot")
	require.Eventually(t, func() bool { return svc.ShouldDefer(ctx, 14) }, 2*time.Second, time.Millisecond)
}
