-- Deferred balance deductions. In deferred mode a request's billing transaction
-- inserts one row here instead of updating users.balance, and a background
-- settler aggregates the rows per user. Effective balance = users.balance minus
-- the sum of a user's rows.
-- (request_id, api_key_id) is the same idempotency key as usage_billing_dedup.
-- No foreign key: the insert must not touch the users row.
-- Before rolling back to a binary that does not know this table, drain it:
-- SELECT count(*) FROM user_balance_pending must be 0.
CREATE TABLE IF NOT EXISTS user_balance_pending (
    id BIGSERIAL PRIMARY KEY,
    user_id BIGINT NOT NULL,
    request_id TEXT NOT NULL,
    api_key_id BIGINT NOT NULL,
    amount NUMERIC(20,8) NOT NULL CHECK (amount > 0),
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    UNIQUE (request_id, api_key_id)
);

CREATE INDEX IF NOT EXISTS idx_user_balance_pending_user ON user_balance_pending (user_id, id);
