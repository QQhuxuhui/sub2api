//go:build unit

package service

import (
	"context"
	"errors"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/stretchr/testify/require"
)

func TestBillingCircuitBreaker_FallsBackToFiftyFailures(t *testing.T) {
	for _, threshold := range []int{0, -1} {
		b := newBillingCircuitBreaker(config.CircuitBreakerConfig{Enabled: true, FailureThreshold: threshold, ResetTimeoutSeconds: 30, HalfOpenRequests: 3})
		require.Equal(t, 50, b.failureThreshold)
	}
}

func TestBillingCircuitBreaker_OpensOnlyAtThreshold(t *testing.T) {
	b := newBillingCircuitBreaker(config.CircuitBreakerConfig{Enabled: true, FailureThreshold: 50, ResetTimeoutSeconds: 30, HalfOpenRequests: 3})
	failure := errors.New("get user balance: context deadline exceeded")
	for i := 0; i < 49; i++ {
		b.OnFailure(failure)
		require.True(t, b.Allow(), "failure %d", i+1)
	}
	b.OnFailure(failure)
	require.False(t, b.Allow())
}

func TestBillingCircuitBreaker_SuccessResetsConsecutiveCount(t *testing.T) {
	b := newBillingCircuitBreaker(config.CircuitBreakerConfig{Enabled: true, FailureThreshold: 50, ResetTimeoutSeconds: 30, HalfOpenRequests: 3})
	failure := errors.New("timeout")
	for round := 0; round < 3; round++ {
		for i := 0; i < 49; i++ {
			b.OnFailure(failure)
		}
		b.OnSuccess()
		require.True(t, b.Allow())
	}
}

func TestBillingCircuitBreaker_ExplicitThresholdIsKept(t *testing.T) {
	b := newBillingCircuitBreaker(config.CircuitBreakerConfig{Enabled: true, FailureThreshold: 5, ResetTimeoutSeconds: 30, HalfOpenRequests: 3})
	failure := errors.New("timeout")
	for i := 0; i < 5; i++ {
		b.OnFailure(failure)
	}
	require.False(t, b.Allow())
}

type billingBreakerFailingUserRepo struct {
	UserRepository
}

func (billingBreakerFailingUserRepo) GetEffectiveBalance(context.Context, int64) (float64, error) {
	return 0, context.DeadlineExceeded
}

func TestCheckBalanceEligibility_FailedLookupStaysRejectedBelowThreshold(t *testing.T) {
	cfg := &config.Config{}
	cfg.Billing.CircuitBreaker = config.CircuitBreakerConfig{Enabled: true, FailureThreshold: 50, ResetTimeoutSeconds: 30, HalfOpenRequests: 3}
	svc := NewBillingCacheService(&billingCacheWorkerStub{}, billingBreakerFailingUserRepo{}, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(svc.Stop)
	for i := 0; i < 49; i++ {
		err := svc.checkBalanceEligibility(context.Background(), int64(i+1))
		require.ErrorIs(t, err, ErrBillingServiceUnavailable, "a failed lookup must never pass")
		require.True(t, svc.circuitBreaker.Allow())
	}
	require.ErrorIs(t, svc.checkBalanceEligibility(context.Background(), 50), ErrBillingServiceUnavailable)
	require.False(t, svc.circuitBreaker.Allow())
}
