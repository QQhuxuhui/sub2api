package repository

import (
	"context"

	dbent "github.com/Wei-Shaw/sub2api/ent"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

// RestoreKeepAvailableState reads policy/status and clears automatic cooldowns
// in one statement. In particular, an admin disabling the account or turning
// the policy off after the caller's snapshot must not be undone by restoration.
// Legacy error rows do not distinguish automatic and manual schedulable=false,
// so scheduling remains an explicit administrator choice.
func (r *accountRepository) RestoreKeepAvailableState(ctx context.Context, id int64) (bool, error) {
	client := clientFromContext(ctx, r.client)
	result, err := client.ExecContext(ctx, `
		WITH updated AS (
			UPDATE accounts
			SET status = CASE WHEN status = $2 THEN $3 ELSE status END,
				error_message = CASE WHEN status = $2 THEN '' ELSE error_message END,
				rate_limited_at = NULL,
				rate_limit_reset_at = NULL,
				overload_until = NULL,
				temp_unschedulable_until = NULL,
				temp_unschedulable_reason = NULL,
				extra = extra - 'antigravity_quota_scopes' - 'model_rate_limits',
				updated_at = NOW()
			WHERE id = $1 AND deleted_at IS NULL
				AND extra->'keep_available' = 'true'::jsonb
			RETURNING id
		)
		INSERT INTO scheduler_outbox (event_type, account_id, group_id, payload)
		SELECT $4, id, NULL, NULL FROM updated
	`, id, service.StatusError, service.StatusActive, service.SchedulerOutboxEventAccountChanged)
	if err != nil {
		return false, err
	}
	affected, err := result.RowsAffected()
	if err != nil || affected == 0 {
		return false, err
	}
	// A caller-owned transaction publishes via its durable outbox after commit.
	if dbent.TxFromContext(ctx) == nil {
		r.syncSchedulerAccountSnapshot(ctx, id)
	}
	return true, nil
}
