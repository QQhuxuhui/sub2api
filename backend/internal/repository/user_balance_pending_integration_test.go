//go:build integration

package repository

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/lib/pq"
	"github.com/shopspring/decimal"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

type userBalanceFixture struct {
	client  *dbent.Client
	user    *service.User
	apiKey  *service.APIKey
	account *service.Account
	billing service.UsageBillingRepository
	pending service.UserBalancePendingRepository
	users   service.UserRepository
}

func newUserBalanceFixture(t *testing.T, balance float64) *userBalanceFixture {
	t.Helper()
	clearUserBalancePending(t)
	t.Cleanup(func() { clearUserBalancePending(t) })

	client := testEntClient(t)
	user := mustCreateUser(t, client, &service.User{
		Email:        "user-balance-" + uuid.NewString() + "@example.com",
		PasswordHash: "hash",
		Balance:      balance,
	})
	apiKey := mustCreateApiKey(t, client, &service.APIKey{
		UserID: user.ID,
		Key:    "sk-user-balance-" + uuid.NewString(),
		Name:   "user-balance",
	})
	account := mustCreateAccount(t, client, &service.Account{
		Name: "user-balance-" + uuid.NewString(),
		Type: service.AccountTypeAPIKey,
	})
	return &userBalanceFixture{
		client:  client,
		user:    user,
		apiKey:  apiKey,
		account: account,
		billing: NewUsageBillingRepository(client, integrationDB),
		pending: NewUserBalancePendingRepository(integrationDB),
		users:   NewUserRepository(client, integrationDB),
	}
}

func clearUserBalancePending(t *testing.T) {
	t.Helper()
	_, err := integrationDB.ExecContext(context.Background(), `DELETE FROM user_balance_pending`)
	require.NoError(t, err)
}

func (f *userBalanceFixture) command(amount float64) *service.UsageBillingCommand {
	return &service.UsageBillingCommand{
		RequestID:    uuid.NewString(),
		APIKeyID:     f.apiKey.ID,
		UserID:       f.user.ID,
		AccountID:    f.account.ID,
		AccountType:  service.AccountTypeAPIKey,
		BalanceCost:  amount,
		DeferBalance: true,
	}
}

func (f *userBalanceFixture) deferCharge(t *testing.T, amount float64) {
	t.Helper()
	result, err := f.billing.Apply(context.Background(), f.command(amount))
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.True(t, result.BalanceDeferred)
}

func (f *userBalanceFixture) settleAll(t *testing.T) {
	t.Helper()
	for {
		settlement, err := f.pending.SettleUser(context.Background(), f.user.ID, 500)
		require.NoError(t, err)
		if settlement.Entries == 0 {
			return
		}
	}
}

func (f *userBalanceFixture) requireLedger(t *testing.T, balance, pending string) {
	t.Helper()
	requireUserLedger(t, f.user.ID, balance, pending)
}

func requireUserLedger(t *testing.T, userID int64, balance, pending string) {
	t.Helper()
	var gotBalance, gotPending decimal.Decimal
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), `
		SELECT u.balance, COALESCE((SELECT SUM(p.amount) FROM user_balance_pending p WHERE p.user_id = u.id), 0)
		FROM users u WHERE u.id = $1
	`, userID).Scan(&gotBalance, &gotPending))
	require.Equal(t, decimal.RequireFromString(balance).String(), gotBalance.String(), "users.balance")
	require.Equal(t, decimal.RequireFromString(pending).String(), gotPending.String(), "pending")
}

func (f *userBalanceFixture) requireEffective(t *testing.T, want float64) {
	t.Helper()
	effective, err := f.users.GetEffectiveBalance(context.Background(), f.user.ID)
	require.NoError(t, err)
	require.Equal(t, want, effective)
}

func countRows(t *testing.T, query string, args ...any) int {
	t.Helper()
	var count int
	require.NoError(t, integrationDB.QueryRowContext(context.Background(), query, args...).Scan(&count))
	return count
}

// failPendingDelete makes the last statement of a settlement fail for this user.
func failPendingDelete(t *testing.T, userID int64) (restore func()) {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("test_fail_pending_delete_%d", userID)
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %[1]s() RETURNS trigger AS $$
		BEGIN
			RAISE EXCEPTION 'injected settlement failure';
		END $$ LANGUAGE plpgsql;
		CREATE TRIGGER %[1]s BEFORE DELETE ON user_balance_pending
			FOR EACH ROW WHEN (OLD.user_id = %[2]d) EXECUTE FUNCTION %[1]s();
	`, name, userID))
	require.NoError(t, err)
	var once sync.Once
	restore = func() {
		once.Do(func() {
			_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
				DROP TRIGGER %[1]s ON user_balance_pending;
				DROP FUNCTION %[1]s();
			`, name))
			require.NoError(t, err)
		})
	}
	t.Cleanup(restore)
	return restore
}

// pausedSettlement is a real SettleUser stopped at its UPDATE: it holds the user
// row and has read its batch, nothing else.
type pausedSettlement struct {
	t       *testing.T
	pid     int
	gate    *sql.Conn
	userID  int64
	done    chan error
	release func()
}

func pauseSettlement(t *testing.T, f *userBalanceFixture) *pausedSettlement {
	t.Helper()
	ctx := context.Background()
	name := fmt.Sprintf("test_pause_settlement_%d", f.user.ID)
	_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
		CREATE OR REPLACE FUNCTION %[1]s() RETURNS trigger AS $$
		BEGIN
			PERFORM pg_advisory_xact_lock(NEW.id);
			RETURN NEW;
		END $$ LANGUAGE plpgsql;
		CREATE TRIGGER %[1]s BEFORE UPDATE ON users
			FOR EACH ROW WHEN (NEW.id = %[2]d AND NEW.balance IS DISTINCT FROM OLD.balance)
			EXECUTE FUNCTION %[1]s();
	`, name, f.user.ID))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
			DROP TRIGGER %[1]s ON users;
			DROP FUNCTION %[1]s();
		`, name))
		require.NoError(t, err)
	})

	gate, err := integrationDB.Conn(ctx)
	require.NoError(t, err)
	t.Cleanup(func() { _ = gate.Close() })
	_, err = gate.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, f.user.ID)
	require.NoError(t, err)

	paused := &pausedSettlement{t: t, gate: gate, userID: f.user.ID, done: make(chan error, 1)}
	go func() {
		_, err := f.pending.SettleUser(ctx, f.user.ID, 500)
		paused.done <- err
	}()
	require.Eventually(t, func() bool {
		err := integrationDB.QueryRowContext(ctx, `
			SELECT pid FROM pg_locks
			WHERE locktype = 'advisory' AND NOT granted AND classid = 0 AND objid::bigint = $1
		`, f.user.ID).Scan(&paused.pid)
		return err == nil
	}, 10*time.Second, time.Millisecond, "the settlement must reach its UPDATE")
	return paused
}

// waitBlocked returns once some session waits for the paused settlement's row lock.
func (p *pausedSettlement) waitBlocked() {
	p.t.Helper()
	require.Eventually(p.t, func() bool {
		var waiting int
		err := integrationDB.QueryRowContext(context.Background(),
			`SELECT COUNT(*) FROM pg_stat_activity WHERE $1 = ANY(pg_blocking_pids(pid))`, p.pid).Scan(&waiting)
		return err == nil && waiting > 0
	}, 10*time.Second, time.Millisecond, "the operation must wait for the settlement's row lock")
}

func (p *pausedSettlement) finish() {
	p.t.Helper()
	_, err := p.gate.ExecContext(context.Background(), `SELECT pg_advisory_unlock($1)`, p.userID)
	require.NoError(p.t, err)
	require.NoError(p.t, <-p.done)
}

func TestUsageBillingApply_DeferredWritesPendingAndNotUsers(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	ctx := context.Background()
	var updatedAt time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT updated_at FROM users WHERE id = $1`, f.user.ID).Scan(&updatedAt))

	cmd := f.command(1.25)
	result, err := f.billing.Apply(ctx, cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.True(t, result.BalanceDeferred)
	require.Nil(t, result.NewBalance)
	require.False(t, result.BalanceOverdrafted)

	f.requireLedger(t, "100", "1.25")
	f.requireEffective(t, 98.75)

	var after time.Time
	require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT updated_at FROM users WHERE id = $1`, f.user.ID).Scan(&after))
	require.True(t, updatedAt.Equal(after), "the users row must stay untouched")

	require.Equal(t, 1, countRows(t, `
		SELECT COUNT(*) FROM user_balance_pending
		WHERE user_id = $1 AND request_id = $2 AND api_key_id = $3 AND amount = 1.25
	`, f.user.ID, cmd.RequestID, f.apiKey.ID))
}

func TestUsageBillingApply_SyncModeWritesNoPending(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	cmd := f.command(1.25)
	cmd.DeferBalance = false

	result, err := f.billing.Apply(context.Background(), cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.False(t, result.BalanceDeferred)
	require.NotNil(t, result.NewBalance)
	require.Equal(t, 98.75, *result.NewBalance)
	f.requireLedger(t, "98.75", "0")
}

func TestUsageBillingApply_DeferredQuantizesLikeSyncPath(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	f.deferCharge(t, 0.000078125)
	f.requireLedger(t, "100", "0.00007813")

	// Below the scale there is nothing to charge, and amount > 0 must hold.
	result, err := f.billing.Apply(context.Background(), f.command(0.000000001))
	require.NoError(t, err)
	require.True(t, result.Applied)
	require.False(t, result.BalanceDeferred)
	f.requireLedger(t, "100", "0.00007813")
}

func TestUsageBillingApply_ClaimAndPendingAreAtomic(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	ctx := context.Background()

	t.Run("a later effect failing removes the pending row", func(t *testing.T) {
		cmd := f.command(1)
		cmd.APIKeyQuotaCost = 1
		cmd.APIKeyID = f.apiKey.ID + 1_000_000 // quota update finds no key

		_, err := f.billing.Apply(ctx, cmd)
		require.ErrorIs(t, err, service.ErrAPIKeyNotFound)
		require.Zero(t, countRows(t, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1`, cmd.RequestID))
		require.Zero(t, countRows(t, `SELECT COUNT(*) FROM user_balance_pending WHERE request_id = $1`, cmd.RequestID))
	})

	t.Run("the pending insert failing removes the claim", func(t *testing.T) {
		_, err := integrationDB.ExecContext(ctx, fmt.Sprintf(`
			CREATE OR REPLACE FUNCTION test_fail_pending_insert() RETURNS trigger AS $$
			BEGIN
				RAISE EXCEPTION 'injected insert failure';
			END $$ LANGUAGE plpgsql;
			CREATE TRIGGER test_fail_pending_insert BEFORE INSERT ON user_balance_pending
				FOR EACH ROW WHEN (NEW.user_id = %d) EXECUTE FUNCTION test_fail_pending_insert();
		`, f.user.ID))
		require.NoError(t, err)
		t.Cleanup(func() {
			_, err := integrationDB.ExecContext(ctx, `
				DROP TRIGGER test_fail_pending_insert ON user_balance_pending;
				DROP FUNCTION test_fail_pending_insert();
			`)
			require.NoError(t, err)
		})

		cmd := f.command(1)
		_, err = f.billing.Apply(ctx, cmd)
		require.ErrorContains(t, err, "injected insert failure")
		require.Zero(t, countRows(t, `SELECT COUNT(*) FROM usage_billing_dedup WHERE request_id = $1`, cmd.RequestID))
	})
}

func TestUsageBillingApply_ConcurrentSameKeyWritesOneRow(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	base := f.command(1.5)

	const workers = 16
	var applied atomic.Int32
	var wg sync.WaitGroup
	start := make(chan struct{})
	for range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			cmd := *base
			result, err := f.billing.Apply(context.Background(), &cmd)
			if err == nil && result.Applied {
				applied.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()

	require.Equal(t, int32(1), applied.Load())
	f.requireLedger(t, "100", "1.5")
}

// A replay after settlement finds no pending row to collide with: only the claim
// stands between it and a second charge.
func TestUsageBillingApply_ReplayAfterSettlementChargesNothing(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	cmd := f.command(2)

	result, err := f.billing.Apply(context.Background(), cmd)
	require.NoError(t, err)
	require.True(t, result.Applied)
	f.settleAll(t)
	f.requireLedger(t, "98", "0")

	replay := *cmd
	result, err = f.billing.Apply(context.Background(), &replay)
	require.NoError(t, err)
	require.False(t, result.Applied)
	f.requireLedger(t, "98", "0")
}

func TestUsageBillingApply_ReplayAcrossModeSwitchIsNotAConflict(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	for _, first := range []bool{true, false} {
		cmd := f.command(1)
		cmd.DeferBalance = first
		result, err := f.billing.Apply(context.Background(), cmd)
		require.NoError(t, err)
		require.True(t, result.Applied)

		// A fresh command, as a replay after a restart would build.
		replay := *cmd
		replay.RequestFingerprint = ""
		replay.DeferBalance = !first
		result, err = f.billing.Apply(context.Background(), &replay)
		require.NoError(t, err, "the mode must not reach the fingerprint")
		require.False(t, result.Applied)
	}
	f.requireLedger(t, "99", "1")
}

func TestUserBalanceSettle_MovesPendingExactlyOnceAndKeepsEffectiveBalance(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	amounts := []float64{0.1, 0.2, 0.3, 0.00000001, 1.23456789}
	for _, amount := range amounts {
		f.deferCharge(t, amount)
	}
	f.requireLedger(t, "100", "1.83456790")
	f.requireEffective(t, 98.1654321)

	settlement, err := f.pending.SettleUser(context.Background(), f.user.ID, 500)
	require.NoError(t, err)
	require.Equal(t, len(amounts), settlement.Entries)
	require.Equal(t, 1.8345679, settlement.Amount)
	require.Equal(t, 100.0, settlement.OldBalance)
	require.Equal(t, 98.1654321, settlement.NewBalance)
	f.requireLedger(t, "98.16543210", "0")
	f.requireEffective(t, 98.1654321)

	settlement, err = f.pending.SettleUser(context.Background(), f.user.ID, 500)
	require.NoError(t, err)
	require.Zero(t, settlement.Entries)
	f.requireLedger(t, "98.16543210", "0")
}

func TestUserBalanceSettle_BatchTakesOldestRowsAndLeavesTheRest(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	const rows = 7
	for i := 1; i <= rows; i++ {
		f.deferCharge(t, float64(i))
	}

	settlement, err := f.pending.SettleUser(context.Background(), f.user.ID, 5)
	require.NoError(t, err)
	require.Equal(t, 5, settlement.Entries)
	require.Equal(t, 15.0, settlement.Amount)
	f.requireLedger(t, "85", "13")

	f.settleAll(t)
	f.requireLedger(t, "72", "0")
}

// The row commits after the settlement read its batch and before it deletes.
func TestUserBalanceSettle_RowCommittedDuringSettlementSurvives(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	f.deferCharge(t, 10)

	paused := pauseSettlement(t, f)
	f.deferCharge(t, 3)
	paused.finish()

	f.requireLedger(t, "90", "3")
	f.settleAll(t)
	f.requireLedger(t, "87", "0")
}

func TestUserBalanceSettle_ConcurrentSettlersSettleOnce(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	const rows = 60
	for range rows {
		f.deferCharge(t, 0.11111111)
	}

	var settled atomic.Int64
	var wg sync.WaitGroup
	errs := make(chan error, 4)
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				settlement, err := f.pending.SettleUser(context.Background(), f.user.ID, 7)
				if errors.Is(err, service.ErrUserBalanceSettleBusy) {
					continue
				}
				if err != nil {
					errs <- err
					return
				}
				if settlement.Entries == 0 {
					return
				}
				settled.Add(int64(settlement.Entries))
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	require.Equal(t, int64(rows), settled.Load())
	f.requireLedger(t, "93.33333340", "0")
}

func TestUserBalanceSettle_LockedRowReturnsBusyAtOnce(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	f.deferCharge(t, 1)
	ctx := context.Background()

	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, f.user.ID)
	require.NoError(t, err)

	_, err = f.pending.SettleUser(ctx, f.user.ID, 500)
	require.ErrorIs(t, err, service.ErrUserBalanceSettleBusy)
	require.NoError(t, tx.Rollback())
	f.requireLedger(t, "100", "1")
}

func TestUserBalanceSettle_SoftDeletedUserSettles(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	f.deferCharge(t, 4)
	ctx := context.Background()
	_, err := integrationDB.ExecContext(ctx, `UPDATE users SET deleted_at = NOW() WHERE id = $1`, f.user.ID)
	require.NoError(t, err)

	settlement, err := f.pending.SettleUser(ctx, f.user.ID, 500)
	require.NoError(t, err)
	require.Equal(t, 1, settlement.Entries)
	f.requireLedger(t, "96", "0")

	_, err = f.users.GetEffectiveBalance(ctx, f.user.ID)
	require.ErrorIs(t, err, service.ErrUserNotFound)
	_, err = f.users.AdjustBalance(ctx, f.user.ID, 1)
	require.ErrorIs(t, err, service.ErrUserNotFound)
}

func TestUserBalanceSettle_FailureRollsBackAndRerunSettlesOnce(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	f.deferCharge(t, 2)
	f.deferCharge(t, 3)
	ctx := context.Background()

	restore := failPendingDelete(t, f.user.ID)
	_, err := f.pending.SettleUser(ctx, f.user.ID, 500)
	require.ErrorContains(t, err, "injected settlement failure")
	f.requireLedger(t, "100", "5")
	f.requireEffective(t, 95)

	// A crash at the same point looks the same: the transaction is gone.
	restore()
	settlement, err := f.pending.SettleUser(ctx, f.user.ID, 500)
	require.NoError(t, err)
	require.Equal(t, 2, settlement.Entries)
	f.requireLedger(t, "95", "0")
}

func TestUserBalanceSettle_ConditionalOperationsAgainstARunningSettlement(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name        string
		run         func(t *testing.T, f *userBalanceFixture) error
		wantErr     error
		wantBalance string
		wantFrozen  string
	}{
		{
			name: "SetBalance",
			run: func(t *testing.T, f *userBalanceFixture) error {
				change, err := f.users.SetBalance(ctx, f.user.ID, 50)
				assert.Equal(t, service.BalanceChange{Old: 65, New: 50}, change)
				return err
			},
			wantBalance: "55",
		},
		{
			name: "AdjustBalance down to exactly zero",
			run: func(t *testing.T, f *userBalanceFixture) error {
				change, err := f.users.AdjustBalance(ctx, f.user.ID, -65)
				assert.Equal(t, service.BalanceChange{Old: 65, New: 0}, change)
				return err
			},
			wantBalance: "5",
		},
		{
			name: "AdjustBalance below zero",
			run: func(t *testing.T, f *userBalanceFixture) error {
				change, err := f.users.AdjustBalance(ctx, f.user.ID, -65.01)
				assert.Equal(t, 65.0, change.Old)
				return err
			},
			wantErr:     service.ErrBalanceNegative,
			wantBalance: "70",
		},
		{
			name: "DeductAvailableBalance is capped",
			run: func(t *testing.T, f *userBalanceFixture) error {
				deducted, err := f.users.(*userRepository).DeductAvailableBalance(ctx, f.user.ID, 1000)
				assert.Equal(t, 65.0, deducted)
				return err
			},
			wantBalance: "5",
		},
		{
			name: "DeductBalanceExact has enough",
			run: func(t *testing.T, f *userBalanceFixture) error {
				deducted, err := f.users.(*userRepository).DeductBalanceExact(ctx, f.user.ID, 65)
				assert.Equal(t, 65.0, deducted)
				return err
			},
			wantBalance: "5",
		},
		{
			name: "DeductBalanceExact is short",
			run: func(t *testing.T, f *userBalanceFixture) error {
				_, err := f.users.(*userRepository).DeductBalanceExact(ctx, f.user.ID, 65.01)
				return err
			},
			wantErr:     service.ErrInsufficientBalance,
			wantBalance: "70",
		},
		{
			name: "redeem floors at zero",
			run: func(t *testing.T, f *userBalanceFixture) error {
				return f.users.(*userRepository).ApplyRedeemBalanceAdjustment(ctx, f.user.ID, -1000)
			},
			wantBalance: "5",
		},
		{
			name: "redeem adds",
			run: func(t *testing.T, f *userBalanceFixture) error {
				return f.users.(*userRepository).ApplyRedeemBalanceAdjustment(ctx, f.user.ID, 10)
			},
			wantBalance: "80",
		},
		{
			name: "image hold fits",
			run: func(t *testing.T, f *userBalanceFixture) error {
				_, err := f.billing.ReserveBatchImageBalance(ctx, f.imageHold(65))
				return err
			},
			wantBalance: "5",
			wantFrozen:  "65",
		},
		{
			name: "image hold is short",
			run: func(t *testing.T, f *userBalanceFixture) error {
				_, err := f.billing.ReserveBatchImageBalance(ctx, f.imageHold(65.01))
				return err
			},
			wantErr:     service.ErrBatchImageInsufficientBalance,
			wantBalance: "70",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newUserBalanceFixture(t, 100)
			f.deferCharge(t, 10)
			f.deferCharge(t, 20)

			// B=100 P=30 now; the settlement turns that into B=70, and a row
			// outside its batch leaves the effective balance at 65.
			paused := pauseSettlement(t, f)
			f.deferCharge(t, 5)

			opErr := make(chan error, 1)
			go func() { opErr <- tc.run(t, f) }()
			paused.waitBlocked()
			paused.finish()

			err := <-opErr
			if tc.wantErr != nil {
				require.ErrorIs(t, err, tc.wantErr)
			} else {
				require.NoError(t, err)
			}
			f.requireLedger(t, tc.wantBalance, "5")
			if tc.wantFrozen != "" {
				var frozen decimal.Decimal
				require.NoError(t, integrationDB.QueryRowContext(ctx, `SELECT frozen_balance FROM users WHERE id = $1`, f.user.ID).Scan(&frozen))
				require.True(t, decimal.RequireFromString(tc.wantFrozen).Equal(frozen))
			}

			before, err := f.users.GetEffectiveBalance(ctx, f.user.ID)
			require.NoError(t, err)
			f.settleAll(t)
			f.requireEffective(t, before)
		})
	}
}

func (f *userBalanceFixture) imageHold(amount float64) *service.BatchImageBalanceHoldCommand {
	return &service.BatchImageBalanceHoldCommand{
		RequestID:  uuid.NewString(),
		APIKeyID:   f.apiKey.ID,
		UserID:     f.user.ID,
		BatchID:    uuid.NewString(),
		HoldAmount: amount,
	}
}

func TestUserBalanceConditionalOperations_UsePendingWithoutSettlement(t *testing.T) {
	ctx := context.Background()

	t.Run("SetBalance", func(t *testing.T) {
		f := newUserBalanceFixture(t, 100)
		f.deferCharge(t, 30)
		change, err := f.users.SetBalance(ctx, f.user.ID, 10)
		require.NoError(t, err)
		require.Equal(t, service.BalanceChange{Old: 70, New: 10}, change)
		f.requireLedger(t, "40", "30")

		change, err = f.users.SetBalance(ctx, f.user.ID, -1)
		require.ErrorIs(t, err, service.ErrBalanceNegative)
		require.Equal(t, 10.0, change.Old)
	})

	t.Run("a caller's transaction is reused and rolls back", func(t *testing.T) {
		f := newUserBalanceFixture(t, 100)
		f.deferCharge(t, 30)
		tx, err := f.client.Tx(ctx)
		require.NoError(t, err)
		defer func() { _ = tx.Rollback() }()
		txCtx := dbent.NewTxContext(ctx, tx)

		_, err = f.users.AdjustBalance(txCtx, f.user.ID, -60)
		require.NoError(t, err)
		require.NoError(t, tx.Rollback())
		f.requireLedger(t, "100", "30")
	})

	t.Run("redeem floor keeps pending intact", func(t *testing.T) {
		f := newUserBalanceFixture(t, 100)
		f.deferCharge(t, 120) // overdrawn: E = -20
		require.NoError(t, f.users.(*userRepository).ApplyRedeemBalanceAdjustment(ctx, f.user.ID, -5))
		f.requireLedger(t, "120", "120")
		f.requireEffective(t, 0)
	})

	t.Run("DeductAvailableBalance deducts nothing from a deficit", func(t *testing.T) {
		f := newUserBalanceFixture(t, 100)
		f.deferCharge(t, 120)
		deducted, err := f.users.(*userRepository).DeductAvailableBalance(ctx, f.user.ID, 10)
		require.NoError(t, err)
		require.Zero(t, deducted)
		f.requireLedger(t, "100", "120")
	})

	t.Run("image hold", func(t *testing.T) {
		f := newUserBalanceFixture(t, 100)
		f.deferCharge(t, 80)
		_, err := f.billing.ReserveBatchImageBalance(ctx, f.imageHold(21))
		require.ErrorIs(t, err, service.ErrBatchImageInsufficientBalance)
		_, err = f.billing.ReserveBatchImageBalance(ctx, f.imageHold(20))
		require.NoError(t, err)
		f.requireLedger(t, "80", "80")
	})
}

func TestUserBalanceConditionalOperations_RaceSettlementExactly(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	ctx := context.Background()

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
	wg.Add(2)
	go func() {
		defer wg.Done()
		for range 150 {
			_, err := f.billing.Apply(ctx, f.command(0.12345678))
			assert.NoError(t, err)
		}
	}()
	go func() {
		defer wg.Done()
		for range 50 {
			_, err := f.users.AdjustBalance(ctx, f.user.ID, 0.5)
			assert.NoError(t, err)
		}
	}()
	wg.Wait()
	close(stop)
	require.NoError(t, <-settled)

	f.settleAll(t)
	f.requireLedger(t, "106.48148300", "0")
}

type userBalanceClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *userBalanceClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *userBalanceClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

type userBalanceModeSetting struct {
	service.SettingRepository
	value string
}

func (s *userBalanceModeSetting) GetValue(context.Context, string) (string, error) {
	return s.value, nil
}

type userBalanceRig struct {
	*userBalanceFixture
	settler      *service.UserBalanceSettler
	billingCache *service.BillingCacheService
	clock        *userBalanceClock
	cache        service.BillingCache
}

func newUserBalanceRig(t *testing.T, balance float64, mode string) *userBalanceRig {
	t.Helper()
	f := newUserBalanceFixture(t, balance)
	return newUserBalanceRigFor(t, f, NewBillingCache(testRedis(t)), mode)
}

func newUserBalanceRigFor(t *testing.T, f *userBalanceFixture, cache service.BillingCache, mode string) *userBalanceRig {
	t.Helper()
	billingCache := service.NewBillingCacheService(cache, f.users, nil, nil, nil, nil, &config.Config{}, nil)
	t.Cleanup(billingCache.Stop)
	clock := &userBalanceClock{now: time.Unix(1_700_000_000, 0)}
	writeMode := service.NewBillingBalanceWriteModeService(&userBalanceModeSetting{value: mode})
	return &userBalanceRig{
		userBalanceFixture: f,
		settler:            service.NewUserBalanceSettlerWithClock(f.pending, f.users, billingCache, nil, writeMode, clock.Now),
		billingCache:       billingCache,
		clock:              clock,
		cache:              cache,
	}
}

// deferred is the part of applyUsageBilling these tests depend on.
func (r *userBalanceRig) deferred(t *testing.T, amount float64) {
	t.Helper()
	r.deferCharge(t, amount)
	r.settler.MarkBalanceDirty(r.user.ID)
}

func (r *userBalanceRig) cachedBalance(t *testing.T) (float64, error) {
	t.Helper()
	return r.cache.GetUserBalance(context.Background(), r.user.ID)
}

// loadBalance is a cache miss going to the database, ending when the asynchronous
// backfill landed.
func (r *userBalanceRig) loadBalance(t *testing.T, want float64) {
	t.Helper()
	balance, err := r.billingCache.GetUserBalance(context.Background(), r.user.ID)
	require.NoError(t, err)
	require.Equal(t, want, balance)
	require.Eventually(t, func() bool {
		cached, err := r.cachedBalance(t)
		return err == nil && cached == want
	}, 5*time.Second, time.Millisecond)
}

func TestUserBalanceReconcile_DelayedDeductionAfterBackfillRecovers(t *testing.T) {
	r := newUserBalanceRig(t, 100, `{"mode":"all"}`)
	ctx := context.Background()
	r.settler.Tick(ctx) // the first timer fires here, the next is 15 seconds away

	r.deferred(t, 10)
	r.loadBalance(t, 90) // the backfill already includes the 10
	require.NoError(t, r.cache.DeductUserBalance(ctx, r.user.ID, 10))

	cached, err := r.cachedBalance(t)
	require.NoError(t, err)
	require.Equal(t, 80.0, cached, "the delayed deduction counted the 10 twice")

	r.clock.Advance(14 * time.Second)
	r.settler.Tick(ctx)
	cached, err = r.cachedBalance(t)
	require.NoError(t, err)
	require.Equal(t, 80.0, cached, "not yet")

	r.clock.Advance(time.Second)
	r.settler.Tick(ctx)
	r.loadBalance(t, 90)
	r.requireLedger(t, "90", "0")
}

func TestUserBalanceReconcile_LastBatchIsCorrectedWithoutFurtherTraffic(t *testing.T) {
	r := newUserBalanceRig(t, 100, `{"mode":"all"}`)
	ctx := context.Background()
	r.settler.Startup(ctx)
	r.loadBalance(t, 100)
	r.settler.Tick(ctx)

	// The queued cache deduction of this request was lost.
	r.clock.Advance(time.Second)
	r.deferred(t, 10)
	r.settler.Tick(ctx) // settles it; the reconcile timer is not due
	r.requireLedger(t, "90", "0")
	cached, err := r.cachedBalance(t)
	require.NoError(t, err)
	require.Equal(t, 100.0, cached)

	r.clock.Advance(15 * time.Second)
	r.settler.Tick(ctx)
	r.loadBalance(t, 90)
}

type invalidateFailingCache struct {
	service.BillingCache
	failing atomic.Bool
}

func (c *invalidateFailingCache) InvalidateUserBalance(ctx context.Context, userID int64) error {
	if c.failing.Load() {
		return errors.New("redis down")
	}
	return c.BillingCache.InvalidateUserBalance(ctx, userID)
}

func TestUserBalanceReconcile_RedisFailureRecoversWithoutNewRows(t *testing.T) {
	f := newUserBalanceFixture(t, 100)
	cache := &invalidateFailingCache{BillingCache: NewBillingCache(testRedis(t))}
	r := newUserBalanceRigFor(t, f, cache, `{"mode":"all"}`)
	ctx := context.Background()
	r.loadBalance(t, 100)
	r.settler.Tick(ctx)

	r.deferred(t, 10)
	cache.failing.Store(true)
	r.clock.Advance(15 * time.Second)
	r.settler.Tick(ctx)
	cached, err := r.cachedBalance(t)
	require.NoError(t, err)
	require.Equal(t, 100.0, cached)

	cache.failing.Store(false)
	r.clock.Advance(15 * time.Second)
	r.settler.Tick(ctx)
	r.loadBalance(t, 90)
}

func TestUserBalanceReconcile_PersistentlyFailingFirstSettlementStillCorrects(t *testing.T) {
	r := newUserBalanceRig(t, 100, `{"mode":"all"}`)
	ctx := context.Background()
	failPendingDelete(t, r.user.ID)
	r.settler.Startup(ctx)
	r.loadBalance(t, 100)

	r.deferred(t, 10)
	r.settler.Tick(ctx)
	r.requireLedger(t, "100", "10")
	r.loadBalance(t, 90)
}

func TestUserBalanceSettler_StartupPurgesEveryCachedBalanceOutsideSyncMode(t *testing.T) {
	ctx := context.Background()
	f := newUserBalanceFixture(t, 100)
	for _, tc := range []struct {
		name     string
		mode     string
		wantGone bool
	}{
		{name: "sync", mode: `{"mode":"sync"}`},
		{name: "list", mode: `{"mode":"list","user_ids":[14]}`, wantGone: true},
		{name: "all", mode: `{"mode":"all"}`, wantGone: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rdb := testRedis(t)
			cache := NewBillingCache(rdb)
			const keys = 2500 // more than one SCAN batch
			pipe := rdb.Pipeline()
			for id := 1; id <= keys; id++ {
				pipe.Set(ctx, billingBalanceKey(int64(id)), 5, time.Minute)
			}
			_, err := pipe.Exec(ctx)
			require.NoError(t, err)
			require.NoError(t, cache.SetSubscriptionCache(ctx, 1, 1, &service.SubscriptionCacheData{Status: "active", Version: 1}))

			r := newUserBalanceRigFor(t, f, cache, tc.mode)
			r.settler.Startup(ctx)

			for _, id := range []int64{1, 1000, keys} {
				_, err := cache.GetUserBalance(ctx, id)
				if tc.wantGone {
					require.Error(t, err)
				} else {
					require.NoError(t, err)
				}
			}
			_, err = cache.GetSubscriptionCache(ctx, 1, 1)
			require.NoError(t, err, "only balance keys go")
		})
	}
}

func TestUserBalanceSettler_StartupSettlesBacklogAndMarksItDirty(t *testing.T) {
	r := newUserBalanceRig(t, 100, `{"mode":"sync"}`)
	ctx := context.Background()
	r.deferCharge(t, 10) // committed by a process that is gone
	r.loadBalance(t, 90)
	require.NoError(t, r.cache.DeductUserBalance(ctx, r.user.ID, 10))

	r.settler.Startup(ctx)
	r.settler.Tick(ctx)

	r.requireLedger(t, "90", "0")
	r.loadBalance(t, 90)
}

func TestUserBalanceSettler_MissingUserRowsStayAndOthersSettle(t *testing.T) {
	r := newUserBalanceRig(t, 100, `{"mode":"all"}`)
	ctx := context.Background()
	const missingUser = int64(2_000_000_000)
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO user_balance_pending (user_id, request_id, api_key_id, amount) VALUES ($1, $2, 1, 7)
	`, missingUser, uuid.NewString())
	require.NoError(t, err)
	r.deferCharge(t, 10)

	result, err := r.settler.RunRound(ctx)
	require.NoError(t, err)
	require.Equal(t, 2, result.Attempted)
	require.Equal(t, 1, result.Settled)
	r.requireLedger(t, "90", "0")
	require.Equal(t, 1, countRows(t, `SELECT COUNT(*) FROM user_balance_pending WHERE user_id = $1`, missingUser))
}

func TestUserBalanceSettler_LockedFirstBatchDoesNotHoldBackOtherUsers(t *testing.T) {
	r := newUserBalanceRig(t, 100, `{"mode":"all"}`)
	ctx := context.Background()

	const locked, free = 200, 25
	ids := make([]int64, 0, locked+free)
	for i := 0; i < locked+free; i++ {
		var id int64
		require.NoError(t, integrationDB.QueryRowContext(ctx, `
			INSERT INTO users (email, password_hash, balance)
			VALUES ($1, 'hash', 100) RETURNING id
		`, "settler-"+uuid.NewString()+"@example.com").Scan(&id))
		ids = append(ids, id)
		_, err := integrationDB.ExecContext(ctx, `
			INSERT INTO user_balance_pending (user_id, request_id, api_key_id, amount) VALUES ($1, $2, 1, 1)
		`, id, uuid.NewString())
		require.NoError(t, err)
	}

	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `SELECT 1 FROM users WHERE id = ANY($1) FOR UPDATE`, pq.Array(ids[:locked]))
	require.NoError(t, err)

	remaining := func() int {
		return countRows(t, `SELECT COUNT(*) FROM user_balance_pending WHERE user_id = ANY($1)`, pq.Array(ids[locked:]))
	}

	result, err := r.settler.RunRound(ctx)
	require.NoError(t, err)
	require.Equal(t, locked, result.Attempted)
	require.Zero(t, result.Settled)
	require.Equal(t, free, remaining())

	// The locked users back off and leave the listing to the others.
	r.clock.Advance(200 * time.Millisecond)
	result, err = r.settler.RunRound(ctx)
	require.NoError(t, err)
	require.Equal(t, free, result.Settled)
	require.Zero(t, remaining())

	// Still locked a second later: retried, still not in anyone's way.
	r.clock.Advance(time.Second)
	result, err = r.settler.RunRound(ctx)
	require.NoError(t, err)
	require.Zero(t, result.Settled)
	require.Equal(t, locked, result.Attempted)

	require.NoError(t, tx.Rollback())
	r.clock.Advance(time.Second)
	result, err = r.settler.RunRound(ctx)
	require.NoError(t, err)
	require.Equal(t, locked, result.Settled)
	require.Zero(t, countRows(t, `SELECT COUNT(*) FROM user_balance_pending WHERE user_id = ANY($1)`, pq.Array(ids)))
	requireUserLedger(t, ids[0], "99", "0")
}

// Holds the user row for the whole burst: a request that needed it would never
// finish, and with a pool this small the rest would queue behind it.
func TestUsageBillingApply_HotUserDoesNotWaitForUserRowOnSmallPool(t *testing.T) {
	f := newUserBalanceFixture(t, 1000)
	integrationDB.SetMaxOpenConns(5)
	t.Cleanup(func() { integrationDB.SetMaxOpenConns(0) })

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	tx, err := integrationDB.BeginTx(ctx, nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(ctx, `SELECT 1 FROM users WHERE id = $1 FOR UPDATE`, f.user.ID)
	require.NoError(t, err)

	const requests = 300
	var wg sync.WaitGroup
	errs := make(chan error, requests)
	sum := decimal.Zero
	amounts := make([]float64, requests)
	for i := range amounts {
		amounts[i] = decimal.New(int64(10_000_000+i*7), -8).InexactFloat64()
		sum = sum.Add(decimal.NewFromFloat(amounts[i]))
	}
	for _, amount := range amounts {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := f.billing.Apply(ctx, f.command(amount))
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}

	f.requireLedger(t, "1000", sum.String())
	require.NoError(t, tx.Rollback())

	f.settleAll(t)
	f.requireLedger(t, decimal.NewFromInt(1000).Sub(sum).String(), "0")
}

// A usage_logs insert holds FOR KEY SHARE on the users row through its foreign
// key, and a busy user always has one open. Settlement must get past it, or the
// users this table exists for would never settle.
func TestUserBalanceSettleIsNotBlockedByForeignKeyLocks(t *testing.T) {
	f := newUserBalanceFixture(t, 10)
	f.deferCharge(t, 1.25)

	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()
	_, err = tx.ExecContext(context.Background(), `SELECT 1 FROM users WHERE id = $1 FOR KEY SHARE`, f.user.ID)
	require.NoError(t, err)

	f.settleAll(t)
	f.requireLedger(t, "8.75", "0")
}
