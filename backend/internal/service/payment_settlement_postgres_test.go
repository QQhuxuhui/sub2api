//go:build unit

package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymenttransactionclaim"
	"github.com/Wei-Shaw/sub2api/internal/payment"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
)

func settlementPostgresClient(t *testing.T, full bool) *dbent.Client {
	t.Helper()
	dsn := os.Getenv("SUB2API_TEST_PAYMENT_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set SUB2API_TEST_PAYMENT_POSTGRES_DSN to run PostgreSQL settlement tests")
	}
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	schema := fmt.Sprintf("settlement_test_%d", time.Now().UnixNano())
	_, err = db.Exec("CREATE SCHEMA " + pq.QuoteIdentifier(schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := db.Exec("DROP SCHEMA " + pq.QuoteIdentifier(schema) + " CASCADE")
		require.NoError(t, err)
	})
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	scoped, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	scoped.SetMaxOpenConns(10)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, scoped)))
	t.Cleanup(func() { require.NoError(t, client.Close()) })
	if full {
		require.NoError(t, client.Schema.Create(context.Background()))
		require.NoError(t, applySettlementMigration(context.Background(), client))
	} else {
		for _, name := range []string{"092_payment_orders.sql", "093_payment_audit_logs.sql", "239_payment_transaction_claims.sql"} {
			body, err := dbmigrations.FS.ReadFile(name)
			require.NoError(t, err)
			_, err = scoped.Exec(string(body))
			require.NoError(t, err)
		}
	}
	return client
}

func applySettlementMigration(ctx context.Context, client *dbent.Client) error {
	body, err := dbmigrations.FS.ReadFile("240_payment_settlement_evidence.sql")
	if err != nil {
		return err
	}
	tx, err := client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Client().ExecContext(ctx, string(body)); err != nil {
		return err
	}
	return tx.Commit()
}

func TestPaymentSettlementMigrationPostgres(t *testing.T) {
	ctx := context.Background()
	t.Run("backfill_and_idempotency", func(t *testing.T) {
		c := settlementPostgresClient(t, false)
		hash := strings.Repeat("a", 64)
		require.NoError(t, writePaymentAudit(ctx, c, 11, "ORDER_MANUAL_SETTLED", "admin", map[string]any{"txHash": "0x" + strings.ToUpper(hash), "blockTime": "2026-09-20T10:00:00Z"}))
		require.NoError(t, writePaymentAudit(ctx, c, 12, "ORDER_TX_LINKED", "epusdt", map[string]any{"txHash": "epusdt:AbCdEf"}))
		require.NoError(t, writePaymentAudit(ctx, c, 13, "ORDER_PAID", "epusdt", map[string]any{"claimKey": "epusdt-trade:old-13"}))
		require.NoError(t, writePaymentAudit(ctx, c, 14, "ORDER_PAID", "epusdt", map[string]any{"tradeNo": "pre-claims-14", "paidAmount": 30}))
		_, err := c.ExecContext(ctx, "INSERT INTO payment_orders (id, user_id, amount, pay_amount, created_at, expires_at) VALUES (13, 1, 30, 30, NOW(), NOW() + INTERVAL '1 hour')")
		require.NoError(t, err)
		_, err = c.ExecContext(ctx, "INSERT INTO payment_orders (id, user_id, amount, pay_amount, created_at, expires_at) VALUES (14, 1, 30, 30, NOW(), NOW() + INTERVAL '1 hour')")
		require.NoError(t, err)
		require.NoError(t, applySettlementMigration(ctx, c))
		require.NoError(t, applySettlementMigration(ctx, c))
		claim, err := c.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.TxHashEQ(hash)).Only(ctx)
		require.NoError(t, err)
		require.EqualValues(t, 11, claim.OrderID)
		require.Equal(t, claimSourceManual, claim.Source)
		require.NotNil(t, claim.TransferTime)
		require.Nil(t, claim.OrderCreatedAt, "deleted order must not erase ownership")
		used, err := paymentTransactionUsedBy(ctx, c, "epusdt:AbCdEf", 0)
		require.NoError(t, err)
		require.Equal(t, "12", used)
		used, err = paymentTransactionUsedBy(ctx, c, "epusdt:abcdef", 0)
		require.NoError(t, err)
		require.Empty(t, used)
		used, err = paymentTransactionUsedBy(ctx, c, "epusdt-trade:pre-claims-14", 0)
		require.NoError(t, err)
		require.Equal(t, "14", used)
	})
	t.Run("orphan_evidence_requires_remediation", func(t *testing.T) {
		c := settlementPostgresClient(t, false)
		_, err := c.ExecContext(ctx, "INSERT INTO payment_transaction_claims (tx_hash, order_id) VALUES ($1, 999)", strings.Repeat("e", 64))
		require.NoError(t, err)
		require.ErrorContains(t, applySettlementMigration(ctx, c), "lacks settlement evidence")
	})
	t.Run("old_writer_fenced_and_marker_is_transaction_local", func(t *testing.T) {
		c := settlementPostgresClient(t, false)
		require.NoError(t, applySettlementMigration(ctx, c))
		_, err := c.ExecContext(ctx, "INSERT INTO payment_transaction_claims (tx_hash, order_id) VALUES ($1, 999)", strings.Repeat("f", 64))
		require.ErrorContains(t, err, "predates settlement protocol")
		_, err = c.ExecContext(ctx, "INSERT INTO payment_orders (id, user_id, amount, pay_amount, payment_type, expires_at) VALUES (1, 1, 30, 30, 'usdt', NOW())")
		require.NoError(t, err)
		_, err = c.ExecContext(ctx, "UPDATE payment_orders SET status='PAID' WHERE id=1")
		require.ErrorContains(t, err, "predates settlement protocol")
		tx, finish, err := beginPaymentSettlement(ctx, c)
		require.NoError(t, err)
		defer finish()
		_, err = tx.Client().ExecContext(ctx, "UPDATE payment_orders SET status='PAID' WHERE id=1")
		require.NoError(t, err)
		require.NoError(t, tx.Commit())
		finish()
		_, err = c.ExecContext(ctx, "INSERT INTO payment_transaction_claims (tx_hash, order_id) VALUES ($1, 999)", strings.Repeat("f", 64))
		require.ErrorContains(t, err, "predates settlement protocol")
	})
	t.Run("conflict_aborts_without_picking_winner", func(t *testing.T) {
		c := settlementPostgresClient(t, false)
		hash := strings.Repeat("b", 64)
		for _, id := range []int64{21, 22} {
			require.NoError(t, writePaymentAudit(ctx, c, id, "ORDER_PAID", "epusdt", map[string]any{"txHash": hash}))
		}
		err := applySettlementMigration(ctx, c)
		require.ErrorContains(t, err, "conflicts with order")
		rows, err := c.QueryContext(ctx, "SELECT count(*) FROM payment_transaction_claims")
		require.NoError(t, err)
		defer rows.Close()
		require.True(t, rows.Next())
		var n int
		require.NoError(t, rows.Scan(&n))
		require.Zero(t, n)
	})
	t.Run("malformed_audit_is_preserved", func(t *testing.T) {
		c := settlementPostgresClient(t, false)
		_, err := c.PaymentAuditLog.Create().SetOrderID("1").SetAction("ORDER_PAID").SetDetail("{bad json").Save(ctx)
		require.NoError(t, err)
		require.ErrorContains(t, applySettlementMigration(ctx, c), "malformed JSON")
		n, err := c.PaymentAuditLog.Query().Count(ctx)
		require.NoError(t, err)
		require.Equal(t, 1, n)
	})
}

func TestPaymentSettlementLargeHistoryPostgres(t *testing.T) {
	c := settlementPostgresClient(t, false)
	ctx := context.Background()
	require.NoError(t, applySettlementMigration(ctx, c))
	tx, finish, err := beginPaymentSettlement(ctx, c)
	require.NoError(t, err)
	defer finish()
	// More history than PostgreSQL's parameter limit: the previous ALL + IN
	// implementation fails here even though every record is outside the window.
	_, err = tx.Client().ExecContext(ctx, `INSERT INTO payment_transaction_claims
		(tx_hash, order_id, source, order_created_at, order_window_end)
		SELECT 'epusdt-trade:history-' || n, n, 'gateway_hashless', NOW() - INTERVAL '10 days', NOW() - INTERVAL '9 days'
		FROM generate_series(1, 70000) AS n`)
	require.NoError(t, err)
	require.NoError(t, tx.Commit())
	finish()
	owner, err := hashlessPaidOrderNear(ctx, c, time.Now(), 999999)
	require.NoError(t, err)
	require.Zero(t, owner)
}

func TestPaymentSettlementInterleavingsPostgres(t *testing.T) {
	for _, manualFirst := range []bool{true, false} {
		t.Run(fmt.Sprintf("manual_first_%t", manualFirst), func(t *testing.T) {
			c := settlementPostgresClient(t, true)
			f := newManualSettleFixture(t, OrderStatusPending, "4.48")
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			user, err := c.User.Create().SetEmail(f.order.UserEmail).SetUsername(f.order.UserName).SetPasswordHash("test").Save(ctx)
			require.NoError(t, err)
			f.userRepo.getByIDUser.ID = user.ID
			o, err := c.PaymentOrder.Create().SetUserID(user.ID).SetUserEmail(f.order.UserEmail).SetUserName(f.order.UserName).
				SetAmount(30).SetPayAmount(30).SetFeeRate(0).SetRechargeCode(f.order.RechargeCode).SetOutTradeNo(f.order.OutTradeNo).
				SetPaymentType(payment.TypeUSDT).SetPaymentTradeNo(f.order.PaymentTradeNo).SetOrderType(payment.OrderTypeBalance).
				SetStatus(OrderStatusPending).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("test").Save(ctx)
			require.NoError(t, err)
			f.client, f.svc.entClient, f.order = c, c, o
			second := newSecondSettlementOrder(t, f)
			entered, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			unblock := func() { releaseOnce.Do(func() { close(release) }) }
			defer unblock()
			c.PaymentTransactionClaim.Use(func(next dbent.Mutator) dbent.Mutator {
				return dbent.MutateFunc(func(ctx context.Context, m dbent.Mutation) (dbent.Value, error) {
					if m.Op().Is(dbent.OpCreate) {
						v, _ := m.Field("tx_hash")
						key, _ := v.(string)
						if isGatewayTradeClaimKey(key) != manualFirst {
							close(entered)
							select {
							case <-release:
							case <-ctx.Done():
								return nil, ctx.Err()
							}
						}
					}
					return next.Mutate(ctx, m)
				})
			})
			manual := func() error {
				_, err := f.svc.AdminSettleOrderByTxHash(ctx, o.ID, ManualSettleRequest{TxHash: manualSettleTxHash})
				return err
			}
			hashless := func() error {
				return f.svc.confirmPayment(ctx, second.ID, second.PaymentTradeNo, 30, payment.TypeEpusdt, nil)
			}
			first, next := manual, hashless
			if !manualFirst {
				first, next = hashless, manual
			}
			firstDone, nextDone := make(chan error, 1), make(chan error, 1)
			go func() { firstDone <- first() }()
			select {
			case <-entered:
			case <-ctx.Done():
				t.Fatal("first transaction never reached claim write")
			}
			go func() { nextDone <- next() }()
			// Observe a real PostgreSQL lock waiter, rather than depending on sleep.
			require.Eventually(t, func() bool {
				rows, err := c.QueryContext(ctx, "SELECT count(*) FROM pg_locks WHERE locktype='advisory' AND NOT granted")
				if err != nil {
					return false
				}
				defer rows.Close()
				if !rows.Next() {
					return false
				}
				var n int
				return rows.Scan(&n) == nil && n > 0
			}, 5*time.Second, 10*time.Millisecond)
			unblock()
			require.NoError(t, <-firstDone)
			err = <-nextDone
			if manualFirst {
				require.Equal(t, "PAYMENT_REVIEW_REQUIRED", infraerrors.Reason(err))
			} else {
				require.Equal(t, "HASHLESS_PAYMENT_NEARBY", infraerrors.Reason(err))
			}
			require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance, "one transfer credits one order")
		})
	}
}

func TestPaymentSettlementFenceAllowsEasyPayCustomUSDTPostgres(t *testing.T) {
	c := settlementPostgresClient(t, true)
	ctx := context.Background()
	user, err := c.User.Create().SetEmail("custom-usdt@example.com").SetUsername("custom-usdt").SetPasswordHash("test").Save(ctx)
	require.NoError(t, err)
	for _, snapshotOnly := range []bool{false, true} {
		create := c.PaymentOrder.Create().SetUserID(user.ID).SetUserEmail(user.Email).SetUserName(user.Username).
			SetAmount(30).SetPayAmount(30).SetRechargeCode("CUSTOM").SetOutTradeNo(fmt.Sprintf("custom-%t", snapshotOnly)).
			SetPaymentType(payment.TypeUSDT).SetPaymentTradeNo("custom-trade").SetOrderType(payment.OrderTypeBalance).
			SetStatus(OrderStatusPending).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("test")
		if snapshotOnly {
			create.SetProviderSnapshot(map[string]any{"provider_key": payment.TypeEasyPay})
		} else {
			create.SetProviderKey(payment.TypeEasyPay)
		}
		o, err := create.Save(ctx)
		require.NoError(t, err)
		// Non-Epusdt providers legitimately have no crypto claim or protocol marker.
		_, err = c.PaymentOrder.UpdateOneID(o.ID).SetStatus(OrderStatusPaid).Save(ctx)
		require.NoError(t, err)
	}
	f := newManualSettleFixture(t, OrderStatusPending, "4.48")
	f.svc.entClient = c
	f.userRepo.getByIDUser.ID = user.ID
	o, err := c.PaymentOrder.Create().SetUserID(user.ID).SetUserEmail(user.Email).SetUserName(user.Username).
		SetAmount(30).SetPayAmount(30).SetRechargeCode(f.order.RechargeCode).SetOutTradeNo("legacy-custom").
		SetPaymentType(payment.TypeUSDT).SetPaymentTradeNo("legacy-custom-trade").SetOrderType(payment.OrderTypeBalance).
		SetStatus(OrderStatusPending).SetExpiresAt(time.Now().Add(time.Hour)).SetClientIP("127.0.0.1").SetSrcHost("test").Save(ctx)
	require.NoError(t, err)
	// Legacy orders can lack both identities; the new service still completes
	// the regular provider's fulfillment, without inventing a crypto claim.
	require.NoError(t, f.svc.toPaid(ctx, o, o.PaymentTradeNo, 30, payment.TypeEasyPay, "", nil))
	require.Equal(t, 30.0, f.userRepo.getByIDUser.Balance)
	n, err := c.PaymentTransactionClaim.Query().Count(ctx)
	require.NoError(t, err)
	require.Zero(t, n)
}
