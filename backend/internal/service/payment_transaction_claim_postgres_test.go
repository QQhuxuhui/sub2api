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
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/ent/paymenttransactionclaim"
	infraerrors "github.com/Wei-Shaw/sub2api/internal/pkg/errors"
	dbmigrations "github.com/Wei-Shaw/sub2api/migrations"
)

// Set SUB2API_TEST_PAYMENT_POSTGRES_DSN to a disposable PostgreSQL database.
// Each run creates and removes its own schema; no existing tables are touched.
func TestPaymentTransactionClaimsPostgres(t *testing.T) {
	dsn := os.Getenv("SUB2API_TEST_PAYMENT_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set SUB2API_TEST_PAYMENT_POSTGRES_DSN to exercise PostgreSQL concurrency")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := sql.Open("postgres", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	schema := fmt.Sprintf("payment_claim_test_%d", time.Now().UnixNano())
	_, err = db.ExecContext(ctx, "CREATE SCHEMA "+pq.QuoteIdentifier(schema))
	require.NoError(t, err)
	t.Cleanup(func() {
		_, err := db.ExecContext(context.Background(), "DROP SCHEMA "+pq.QuoteIdentifier(schema)+" CASCADE")
		require.NoError(t, err)
	})
	u, err := url.Parse(dsn)
	require.NoError(t, err)
	q := u.Query()
	q.Set("search_path", schema)
	u.RawQuery = q.Encode()
	scoped, err := sql.Open("postgres", u.String())
	require.NoError(t, err)
	scoped.SetMaxOpenConns(20)
	client := dbent.NewClient(dbent.Driver(entsql.OpenDB(dialect.Postgres, scoped)))
	t.Cleanup(func() { require.NoError(t, client.Close()) })

	// Execute the actual forward-only migration twice in transactions.
	for _, file := range []string{"092_payment_orders.sql", "093_payment_audit_logs.sql", "239_payment_transaction_claims.sql", "239_payment_transaction_claims.sql", "240_payment_settlement_evidence.sql"} {
		migration, err := dbmigrations.FS.ReadFile(file)
		require.NoError(t, err)
		tx, err := scoped.BeginTx(ctx, nil)
		require.NoError(t, err)
		_, err = tx.ExecContext(ctx, string(migration))
		if err != nil {
			_ = tx.Rollback()
			t.Fatal(err)
		}
		require.NoError(t, tx.Commit())
	}

	claim := func(orderID int64, hash string) error {
		tx, finish, err := beginPaymentSettlement(ctx, client)
		if err != nil {
			return err
		}
		defer finish()
		if _, err := claimPaymentTransaction(ctx, tx.Client(), orderID, hash); err != nil {
			return err
		}
		if _, err := tx.Client().PaymentTransactionClaim.Update().Where(paymenttransactionclaim.TxHashEQ(hash)).SetSource(claimSourceGateway).Save(ctx); err != nil {
			return err
		}
		return tx.Commit()
	}
	for _, sameOrder := range []bool{false, true} {
		t.Run(fmt.Sprintf("concurrent_same_order_%t", sameOrder), func(t *testing.T) {
			hash := strings.Repeat("a", 64)
			if sameOrder {
				hash = strings.Repeat("b", 64)
			}
			const workers = 12
			errs := make([]error, workers)
			start := make(chan struct{})
			var wg sync.WaitGroup
			for i := range errs {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					orderID := int64(i + 1)
					if sameOrder {
						orderID = 99
					}
					errs[i] = claim(orderID, hash)
				}(i)
			}
			close(start)
			wg.Wait()
			successes := 0
			for _, err := range errs {
				if err == nil {
					successes++
				} else {
					require.Equal(t, "TX_ALREADY_USED", infraerrors.Reason(err), err.Error())
				}
			}
			if sameOrder {
				require.Equal(t, workers, successes, "retries for the same order are idempotent")
			} else {
				require.Equal(t, 1, successes, "one order owns the transaction across independent DB connections")
			}
			count, err := client.PaymentTransactionClaim.Query().Where(paymenttransactionclaim.TxHashEQ(hash)).Count(ctx)
			require.NoError(t, err)
			require.Equal(t, 1, count)
		})
	}

	t.Run("rollback_releases_claim", func(t *testing.T) {
		hash := strings.Repeat("c", 64)
		tx, finish, err := beginPaymentSettlement(ctx, client)
		require.NoError(t, err)
		defer finish()
		created, err := claimPaymentTransaction(ctx, tx.Client(), 1, hash)
		require.NoError(t, err)
		require.True(t, created)
		require.NoError(t, tx.Rollback())
		require.NoError(t, claim(2, hash))
	})
	t.Run("legacy_audit_is_still_consumed", func(t *testing.T) {
		hash := strings.Repeat("d", 64)
		require.NoError(t, writePaymentAudit(ctx, client, 71, "ORDER_PAID", "epusdt", map[string]any{"txHash": "0x" + strings.ToUpper(hash)}))
		migration, err := dbmigrations.FS.ReadFile("240_payment_settlement_evidence.sql")
		require.NoError(t, err)
		_, err = scoped.ExecContext(ctx, string(migration))
		require.NoError(t, err)
		require.Equal(t, "TX_ALREADY_USED", infraerrors.Reason(claim(72, hash)))
		require.NoError(t, claim(71, hash))
	})
}
