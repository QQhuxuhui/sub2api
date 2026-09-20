-- Durable cross-order payment deduplication. No cascading foreign key: a
-- transaction remains consumed even if its order is refunded or removed.
CREATE TABLE IF NOT EXISTS payment_transaction_claims (
    id BIGSERIAL PRIMARY KEY,
    tx_hash VARCHAR(512) NOT NULL,
    order_id BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS paymenttransactionclaim_tx_hash
    ON payment_transaction_claims (tx_hash);
CREATE INDEX IF NOT EXISTS paymenttransactionclaim_order_id
    ON payment_transaction_claims (order_id);

-- Pre-upgrade claims are also checked against ORDER_PAID/ORDER_MANUAL_SETTLED
-- audit records by the application. Preserve those records during upgrades.
