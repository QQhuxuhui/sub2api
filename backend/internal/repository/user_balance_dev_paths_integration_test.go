//go:build integration

package repository

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/payment"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// Balance paths that exist on dev and were not part of the original pending
// design: paid orders, affiliate rebates and the full image hold lifecycle.

// paidUSDTOrder creates an order in the state AdminSettleOrderByTxHash leaves it
// after verifying the transfer, right before fulfillment.
func (f *userBalanceFixture) paidUSDTOrder(t *testing.T, amount float64) int64 {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	order, err := f.client.PaymentOrder.Create().
		SetUserID(f.user.ID).SetUserEmail(f.user.Email).SetUserName("user-balance").
		SetAmount(amount).SetPayAmount(amount).SetFeeRate(0).
		SetRechargeCode(suffix).
		SetOutTradeNo("sub2_" + suffix).
		SetPaymentType(payment.TypeUSDT).
		SetPaymentTradeNo("trade-" + suffix).
		SetOrderType(payment.OrderTypeBalance).
		SetStatus(service.OrderStatusPaid).
		SetExpiresAt(time.Now().Add(time.Hour)).
		SetClientIP("127.0.0.1").SetSrcHost("api.example.com").
		Save(context.Background())
	require.NoError(t, err)
	return order.ID
}

func (f *userBalanceFixture) paymentService() *service.PaymentService {
	redeem := service.NewRedeemService(NewRedeemCodeRepository(f.client), f.users, nil, nil, nil, f.client, nil, nil)
	return service.NewPaymentService(f.client, payment.NewRegistry(), nil, redeem, nil, nil, f.users, nil, nil)
}

func TestUserBalanceSettle_USDTFulfillment(t *testing.T) {
	ctx := context.Background()

	t.Run("credits on top of pending without settling it", func(t *testing.T) {
		f := newUserBalanceFixture(t, 100)
		f.deferCharge(t, 30)
		orderID := f.paidUSDTOrder(t, 25.5)

		require.NoError(t, f.paymentService().ExecuteBalanceFulfillment(ctx, orderID))

		f.requireLedger(t, "125.5", "30")
		f.requireEffective(t, 95.5)
		f.settleAll(t)
		f.requireLedger(t, "95.5", "0")
	})

	t.Run("waits for a running settlement and keeps both amounts", func(t *testing.T) {
		f := newUserBalanceFixture(t, 100)
		f.deferCharge(t, 10)
		f.deferCharge(t, 20)
		orderID := f.paidUSDTOrder(t, 25.5)
		svc := f.paymentService()

		paused := pauseSettlement(t, f)
		f.deferCharge(t, 5)
		credited := make(chan error, 1)
		go func() { credited <- svc.ExecuteBalanceFulfillment(ctx, orderID) }()
		paused.waitBlocked()
		paused.finish()
		require.NoError(t, <-credited)

		f.requireLedger(t, "95.5", "5")
		f.settleAll(t)
		f.requireLedger(t, "90.5", "0")
	})

	t.Run("running fulfillment twice credits once", func(t *testing.T) {
		f := newUserBalanceFixture(t, 100)
		f.deferCharge(t, 30)
		orderID := f.paidUSDTOrder(t, 25.5)
		svc := f.paymentService()

		require.NoError(t, svc.ExecuteBalanceFulfillment(ctx, orderID))
		require.NoError(t, svc.ExecuteBalanceFulfillment(ctx, orderID))
		f.settleAll(t)
		f.requireLedger(t, "95.5", "0")
	})
}

func (f *userBalanceFixture) accrueRebate(t *testing.T, amount float64) service.AffiliateRepository {
	t.Helper()
	ctx := context.Background()
	repo := NewAffiliateRepository(f.client, integrationDB)
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO user_affiliates (user_id, aff_code, created_at, updated_at)
		VALUES ($1, $2, NOW(), NOW())
	`, f.user.ID, "AFF"+uuid.NewString()[:12])
	require.NoError(t, err)
	invitee := mustCreateUser(t, f.client, &service.User{
		Email:        "user-balance-invitee-" + uuid.NewString() + "@example.com",
		PasswordHash: "hash",
	})
	applied, err := repo.AccrueQuota(ctx, f.user.ID, invitee.ID, amount, 0, nil)
	require.NoError(t, err)
	require.True(t, applied)
	return repo
}

func TestUserBalanceSettle_AffiliateRebateTransfer(t *testing.T) {
	ctx := context.Background()

	t.Run("credits on top of pending without settling it", func(t *testing.T) {
		f := newUserBalanceFixture(t, 100)
		f.deferCharge(t, 30)
		repo := f.accrueRebate(t, 12.5)

		transferred, balance, err := repo.TransferQuotaToBalance(ctx, f.user.ID)
		require.NoError(t, err)
		require.Equal(t, 12.5, transferred)
		require.Equal(t, 112.5, balance, "like every page, the ledger shows users.balance")

		f.requireLedger(t, "112.5", "30")
		f.requireEffective(t, 82.5)
		f.settleAll(t)
		f.requireLedger(t, "82.5", "0")
	})

	t.Run("waits for a running settlement and keeps both amounts", func(t *testing.T) {
		f := newUserBalanceFixture(t, 100)
		f.deferCharge(t, 10)
		f.deferCharge(t, 20)
		repo := f.accrueRebate(t, 12.5)

		paused := pauseSettlement(t, f)
		f.deferCharge(t, 5)
		type transfer struct {
			transferred, balance float64
			err                  error
		}
		done := make(chan transfer, 1)
		go func() {
			var result transfer
			result.transferred, result.balance, result.err = repo.TransferQuotaToBalance(ctx, f.user.ID)
			done <- result
		}()
		paused.waitBlocked()
		paused.finish()

		result := <-done
		require.NoError(t, result.err)
		require.Equal(t, 12.5, result.transferred)
		require.Equal(t, 82.5, result.balance)
		f.requireLedger(t, "82.5", "5")
		f.settleAll(t)
		f.requireLedger(t, "77.5", "0")
	})
}

func (f *userBalanceFixture) reserveImageHold(t *testing.T, batchID string, amount float64) {
	t.Helper()
	result, err := f.billing.ReserveBatchImageBalance(context.Background(), &service.BatchImageBalanceHoldCommand{
		RequestID:  service.BatchImageHoldRequestID(batchID),
		APIKeyID:   f.apiKey.ID,
		UserID:     f.user.ID,
		BatchID:    batchID,
		HoldAmount: amount,
	})
	require.NoError(t, err)
	require.True(t, result.Applied)
}

func (f *userBalanceFixture) captureCommand(batchID string, hold, actual float64) *service.BatchImageBalanceHoldCommand {
	return &service.BatchImageBalanceHoldCommand{
		RequestID:    service.BatchImageCaptureRequestID(batchID),
		APIKeyID:     f.apiKey.ID,
		UserID:       f.user.ID,
		BatchID:      batchID,
		HoldAmount:   hold,
		ActualAmount: actual,
	}
}

func (f *userBalanceFixture) releaseCommand(batchID string, hold float64) *service.BatchImageBalanceHoldCommand {
	return &service.BatchImageBalanceHoldCommand{
		RequestID:  service.BatchImageReleaseRequestID(batchID),
		APIKeyID:   f.apiKey.ID,
		UserID:     f.user.ID,
		BatchID:    batchID,
		HoldAmount: hold,
	}
}

func (f *userBalanceFixture) requireFrozen(t *testing.T, want string) {
	t.Helper()
	var frozen decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(context.Background(),
		`SELECT frozen_balance FROM users WHERE id = $1`, f.user.ID).Scan(&frozen))
	require.True(t, decimal.RequireFromString(want).Equal(frozen), "frozen_balance = %s, want %s", frozen, want)
}

func TestUserBalanceSettle_ImageHoldLifecycle(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name string
		run  func(f *userBalanceFixture, batchID string) error
		// B=100, charges of 30 are settled and 5 stay pending, the hold takes 40.
		wantBalance string
	}{
		{
			name: "capture below the hold refunds the difference",
			run: func(f *userBalanceFixture, batchID string) error {
				_, err := f.billing.CaptureBatchImageBalance(ctx, f.captureCommand(batchID, 40, 25))
				return err
			},
			wantBalance: "45",
		},
		{
			name: "capture of the whole hold",
			run: func(f *userBalanceFixture, batchID string) error {
				_, err := f.billing.CaptureBatchImageBalance(ctx, f.captureCommand(batchID, 40, 40))
				return err
			},
			wantBalance: "30",
		},
		{
			name: "release returns the hold",
			run: func(f *userBalanceFixture, batchID string) error {
				_, err := f.billing.ReleaseBatchImageBalance(ctx, f.releaseCommand(batchID, 40))
				return err
			},
			wantBalance: "70",
		},
	} {
		t.Run(tc.name+" against a running settlement", func(t *testing.T) {
			f := newUserBalanceFixture(t, 100)
			batchID := uuid.NewString()
			f.deferCharge(t, 10)
			f.deferCharge(t, 20)
			f.reserveImageHold(t, batchID, 40)
			f.requireLedger(t, "60", "30")
			f.requireFrozen(t, "40")

			paused := pauseSettlement(t, f)
			f.deferCharge(t, 5)
			opErr := make(chan error, 1)
			go func() { opErr <- tc.run(f, batchID) }()
			paused.waitBlocked()
			paused.finish()
			require.NoError(t, <-opErr)

			f.requireLedger(t, tc.wantBalance, "5")
			f.requireFrozen(t, "0")
			effective, err := f.users.GetEffectiveBalance(ctx, f.user.ID)
			require.NoError(t, err)
			f.settleAll(t)
			f.requireEffective(t, effective)
		})

		t.Run(tc.name+" before any settlement", func(t *testing.T) {
			f := newUserBalanceFixture(t, 100)
			batchID := uuid.NewString()
			f.deferCharge(t, 30)
			f.reserveImageHold(t, batchID, 40)
			f.deferCharge(t, 5)

			require.NoError(t, tc.run(f, batchID))

			// Nothing here looks at pending: the wanted balance plus the 30 not settled yet.
			want := decimal.RequireFromString(tc.wantBalance)
			f.requireLedger(t, want.Add(decimal.NewFromInt(30)).String(), "35")
			f.settleAll(t)
			f.requireLedger(t, want.Sub(decimal.NewFromInt(5)).String(), "0")
			f.requireFrozen(t, "0")
		})
	}

	t.Run("replayed capture and release change nothing", func(t *testing.T) {
		f := newUserBalanceFixture(t, 100)
		captured, released := uuid.NewString(), uuid.NewString()
		f.deferCharge(t, 30)
		f.reserveImageHold(t, captured, 20)
		f.reserveImageHold(t, released, 10)

		for range 2 {
			_, err := f.billing.CaptureBatchImageBalance(ctx, f.captureCommand(captured, 20, 15))
			require.NoError(t, err)
			_, err = f.billing.ReleaseBatchImageBalance(ctx, f.releaseCommand(released, 10))
			require.NoError(t, err)
		}
		f.settleAll(t)
		f.requireLedger(t, "55", "0")
		f.requireFrozen(t, "0")
	})
}

func TestUserBalanceImageHoldAndCredits_RaceSettlementExactly(t *testing.T) {
	f := newUserBalanceFixture(t, 1000)
	ctx := context.Background()
	repo := f.accrueRebate(t, 12.5)
	orderID := f.paidUSDTOrder(t, 25.5)
	svc := f.paymentService()

	stop := make(chan struct{})
	settled := make(chan error, 1)
	go func() {
		for {
			_, err := f.pending.SettleUser(ctx, f.user.ID, 3)
			if err != nil && !errors.Is(err, service.ErrUserBalanceSettleBusy) {
				settled <- err
				return
			}
			select {
			case <-stop:
				settled <- nil
				return
			default:
			}
		}
	}()

	var wg sync.WaitGroup
	wg.Add(3)
	go func() {
		defer wg.Done()
		for range 120 {
			_, err := f.billing.Apply(ctx, f.command(0.12345678))
			assert.NoError(t, err)
		}
	}()
	go func() {
		defer wg.Done()
		for i := range 40 {
			batchID := uuid.NewString()
			_, err := f.billing.ReserveBatchImageBalance(ctx, &service.BatchImageBalanceHoldCommand{
				RequestID:  service.BatchImageHoldRequestID(batchID),
				APIKeyID:   f.apiKey.ID,
				UserID:     f.user.ID,
				BatchID:    batchID,
				HoldAmount: 2,
			})
			assert.NoError(t, err)
			if i%2 == 0 {
				_, err = f.billing.CaptureBatchImageBalance(ctx, f.captureCommand(batchID, 2, 0.5))
			} else {
				_, err = f.billing.ReleaseBatchImageBalance(ctx, f.releaseCommand(batchID, 2))
			}
			assert.NoError(t, err)
		}
	}()
	go func() {
		defer wg.Done()
		assert.NoError(t, svc.ExecuteBalanceFulfillment(ctx, orderID))
		_, _, err := repo.TransferQuotaToBalance(ctx, f.user.ID)
		assert.NoError(t, err)
	}()
	wg.Wait()
	close(stop)
	require.NoError(t, <-settled)

	f.settleAll(t)
	// 1000 + 25.5 + 12.5 - 120*0.12345678 - 20*0.5
	f.requireLedger(t, "1013.18518640", "0")
	f.requireFrozen(t, "0")
}

func TestUsageBillingApply_EmptyImageWaiverWritesNoPending(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	cmd := f.command(0)

	result, err := f.billing.Apply(context.Background(), cmd)

	require.NoError(t, err)
	require.True(t, result.Applied, "the request is still claimed")
	require.False(t, result.BalanceDeferred)
	require.Nil(t, result.NewBalance)
	require.Zero(t, countRows(t, `SELECT COUNT(*) FROM user_balance_pending WHERE user_id = $1`, f.user.ID))
	f.requireLedger(t, "100", "0")
}
