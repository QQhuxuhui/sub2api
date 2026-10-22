package service

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type cacheStatsAccountRepo struct {
	AccountRepository
	accounts []Account
}

func (r *cacheStatsAccountRepo) ListByPlatform(_ context.Context, platform string) ([]Account, error) {
	var accounts []Account
	for _, account := range r.accounts {
		if account.Platform == platform && account.Status == StatusActive {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

func (r *cacheStatsAccountRepo) ListAllWithFilters(_ context.Context, platform, accountType, status, search string, groupID int64, privacyMode string) ([]Account, error) {
	var accounts []Account
	for _, account := range r.accounts {
		if account.Platform == platform && (status == "" || account.Status == status) {
			accounts = append(accounts, account)
		}
	}
	return accounts, nil
}

type cacheStatsUsageRepo struct {
	OpenAICacheStatsRepository
	usage []OpenAICacheAccountUsage
}

func (r *cacheStatsUsageRepo) ListOpenAICacheAccountUsage(context.Context, time.Time, time.Time) ([]OpenAICacheAccountUsage, error) {
	return r.usage, nil
}

func TestOpenAICacheStatsListAccountsIncludesInactiveBeyondPageLimit(t *testing.T) {
	accounts := []Account{{ID: 1, Platform: PlatformOpenAI, Status: StatusActive}}
	for id := int64(2); id <= 1002; id++ {
		status := StatusDisabled
		if id == 1002 {
			status = StatusError
		}
		accounts = append(accounts, Account{ID: id, Platform: PlatformOpenAI, Status: status})
	}
	accounts = append(accounts, Account{ID: 1003, Platform: PlatformAnthropic, Status: StatusActive})
	usage := []OpenAICacheAccountUsage{
		{AccountID: 1, All: OpenAICacheTokenBucket{Requests: 1, BilledCost: 2}},
		{AccountID: 2, All: OpenAICacheTokenBucket{Requests: 2, BilledCost: 3, CostDelta: 1}},
		{AccountID: 1002, All: OpenAICacheTokenBucket{Requests: 3, BilledCost: 4, CostDelta: 2}},
	}
	svc := NewOpenAICacheStatsService(&cacheStatsUsageRepo{usage: usage}, &cacheStatsAccountRepo{accounts: accounts}, nil)
	result, err := svc.ListAccounts(context.Background(), time.Now().Add(-time.Hour), time.Now())
	require.NoError(t, err)
	require.Len(t, result.Accounts, 1002)
	require.Equal(t, 1002, result.Summary.Accounts)
	require.Equal(t, int64(6), result.Summary.All.Requests)
	require.Equal(t, 9.0, result.Summary.BilledCost)
	require.Equal(t, 12.0, result.Summary.ExpectedCost)
	require.Equal(t, int64(1002), result.Accounts[0].AccountID)
	require.Equal(t, StatusError, result.Accounts[0].Status)
	require.Equal(t, StatusDisabled, result.Accounts[1].Status)
}
