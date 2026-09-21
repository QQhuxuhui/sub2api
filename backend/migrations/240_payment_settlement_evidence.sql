-- Stop/drain pre-240 payment writers before deploying this migration. Old
-- application versions do not participate in the settlement advisory lock.
-- Run inside the migration runner's transaction; leave migration 239 unchanged.
LOCK TABLE payment_audit_logs, payment_transaction_claims IN SHARE ROW EXCLUSIVE MODE;
SELECT set_config('sub2api.payment_settlement_protocol', '240', true);

ALTER TABLE payment_transaction_claims
    ADD COLUMN IF NOT EXISTS source VARCHAR NOT NULL DEFAULT 'legacy',
    ADD COLUMN IF NOT EXISTS transfer_time TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS order_created_at TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS order_window_end TIMESTAMPTZ,
    ADD COLUMN IF NOT EXISTS review_pending BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS evidence JSONB;

-- Never silently pick a winner for conflicting historical ownership. Raising
-- aborts the migration transaction, retaining the original records for repair.
DO $$
DECLARE
    audit RECORD;
    detail JSONB;
    claim_key TEXT;
    raw_hash TEXT;
    owner_id BIGINT;
    existing_owner BIGINT;
    mined_at TIMESTAMPTZ;
BEGIN
    FOR audit IN SELECT a.id, a.order_id, a.action, a.detail, a.created_at, a.operator
                 FROM payment_audit_logs AS a
                 WHERE a.action IN ('ORDER_PAID', 'ORDER_MANUAL_SETTLED', 'ORDER_TX_LINKED')
                 ORDER BY a.id
    LOOP
        IF btrim(audit.detail) = '' THEN
            IF audit.action = 'ORDER_MANUAL_SETTLED' THEN
                RAISE EXCEPTION 'payment audit % missing manual settlement evidence', audit.id;
            END IF;
            CONTINUE;
        END IF;
        BEGIN
            detail := audit.detail::JSONB;
        EXCEPTION WHEN invalid_text_representation THEN
            RAISE EXCEPTION 'payment audit % has malformed JSON; repair before upgrade', audit.id;
        END;
        raw_hash := btrim(COALESCE(detail->>'txHash', ''));
        claim_key := btrim(COALESCE(detail->>'claimKey', ''));
        IF raw_hash <> '' THEN
            IF raw_hash ~* '^(0x)?[0-9a-f]{64}$' THEN
                claim_key := regexp_replace(lower(raw_hash), '^0x', '');
            ELSIF raw_hash LIKE 'epusdt:%' THEN
                claim_key := raw_hash; -- opaque references preserve case
            ELSE
                RAISE EXCEPTION 'payment audit % has an unsupported transaction reference', audit.id;
            END IF;
        ELSIF claim_key <> '' AND claim_key NOT LIKE 'epusdt-trade:%' THEN
            RAISE EXCEPTION 'payment audit % has an unsupported gateway claim', audit.id;
        END IF;
        -- Early Epusdt versions credited hashless queries before claimKey was
        -- added to audits. Preserve those confirmations too.
        IF claim_key = '' AND audit.action = 'ORDER_PAID' AND audit.operator = 'epusdt' THEN
            IF btrim(COALESCE(detail->>'tradeNo', '')) = '' THEN
                RAISE EXCEPTION 'payment audit % missing Epusdt trade number', audit.id;
            END IF;
            claim_key := 'epusdt-trade:' || btrim(detail->>'tradeNo');
        END IF;
        IF claim_key = '' THEN
            IF audit.action = 'ORDER_MANUAL_SETTLED' THEN
                RAISE EXCEPTION 'payment audit % missing transaction hash', audit.id;
            END IF;
            CONTINUE;
        END IF;
        IF audit.order_id !~ '^[1-9][0-9]*$' OR length(claim_key) > 512 THEN
            RAISE EXCEPTION 'payment audit % has invalid ownership data', audit.id;
        END IF;
        owner_id := audit.order_id::BIGINT;
        INSERT INTO payment_transaction_claims (tx_hash, order_id, created_at)
            VALUES (claim_key, owner_id, audit.created_at)
            ON CONFLICT (tx_hash) DO NOTHING;
        SELECT order_id INTO existing_owner FROM payment_transaction_claims WHERE tx_hash = claim_key;
        IF existing_owner <> owner_id THEN
            RAISE EXCEPTION 'payment audit % conflicts with order % for transaction %', audit.id, existing_owner, claim_key;
        END IF;
        IF audit.action = 'ORDER_MANUAL_SETTLED' THEN
            mined_at := NULL;
            BEGIN
                mined_at := NULLIF(detail->>'blockTime', '')::TIMESTAMPTZ;
            EXCEPTION WHEN invalid_datetime_format OR datetime_field_overflow THEN
                -- Missing/invalid time stays NULL: it must remain ambiguous.
                mined_at := NULL;
            END;
            UPDATE payment_transaction_claims SET source = 'manual',
                transfer_time = mined_at, evidence = detail WHERE tx_hash = claim_key;
        ELSE
            UPDATE payment_transaction_claims SET
                source = CASE WHEN claim_key LIKE 'epusdt-trade:%' THEN 'gateway_hashless' ELSE 'gateway_hash' END,
                evidence = detail
                WHERE tx_hash = claim_key AND source = 'legacy';
        END IF;
    END LOOP;
END $$;

UPDATE payment_transaction_claims SET source = 'gateway_hashless'
    WHERE source = 'legacy' AND tx_hash LIKE 'epusdt-trade:%';

UPDATE payment_transaction_claims AS claim SET order_created_at = orders.created_at,
    order_window_end = GREATEST(orders.expires_at, orders.created_at + INTERVAL '24 hours')
    FROM payment_orders AS orders
    WHERE claim.order_id = orders.id AND claim.order_created_at IS NULL;

-- Unknown provenance cannot safely be treated as a permanent global conflict,
-- nor silently ignored. Keep the old database intact until evidence is repaired.
DO $$
DECLARE bad_id BIGINT;
BEGIN
    SELECT id INTO bad_id FROM payment_transaction_claims
        WHERE source = 'legacy'
           OR (source = 'manual' AND transfer_time IS NULL)
           OR (source = 'gateway_hashless' AND (order_created_at IS NULL OR order_window_end IS NULL))
        ORDER BY id LIMIT 1;
    IF bad_id IS NOT NULL THEN
        RAISE EXCEPTION 'payment claim % lacks settlement evidence; restore its payment audit/order evidence before upgrade', bad_id;
    END IF;
END $$;

CREATE INDEX IF NOT EXISTS paymenttransactionclaim_source_transfer_time
    ON payment_transaction_claims (source, transfer_time);
CREATE INDEX IF NOT EXISTS paymenttransactionclaim_source_order_created_at
    ON payment_transaction_claims (source, order_created_at);
CREATE INDEX IF NOT EXISTS paymenttransactionclaim_source_order_window_end
    ON payment_transaction_claims (source, order_window_end);

-- Fail closed if an old process is left running during rollout. A legacy
-- same-order retry may skip INSERT, so fence the PAID transition as well.
CREATE OR REPLACE FUNCTION enforce_payment_settlement_protocol() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
    -- Visible method names are not provider identities: EasyPay can expose a
    -- custom 'usdt' method. Snapshot identity takes precedence over the column.
    -- Legacy rows without either identity remain conservatively fenced.
    IF TG_TABLE_NAME = 'payment_orders' AND lower(COALESCE(
        NULLIF(to_jsonb(NEW)->'provider_snapshot'->>'provider_key', ''),
        NULLIF(to_jsonb(NEW)->>'provider_key', ''), 'epusdt')) <> 'epusdt' THEN
        RETURN NEW;
    END IF;
    IF current_setting('sub2api.payment_settlement_protocol', true) IS DISTINCT FROM '240' THEN
        RAISE EXCEPTION 'payment writer predates settlement protocol 240; restart the application';
    END IF;
    RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS payment_claim_protocol ON payment_transaction_claims;
CREATE TRIGGER payment_claim_protocol BEFORE INSERT OR UPDATE ON payment_transaction_claims
    FOR EACH ROW EXECUTE FUNCTION enforce_payment_settlement_protocol();
DROP TRIGGER IF EXISTS payment_order_protocol ON payment_orders;
CREATE TRIGGER payment_order_protocol BEFORE UPDATE ON payment_orders
    FOR EACH ROW WHEN (NEW.status = 'PAID' AND OLD.status <> 'PAID' AND NEW.payment_type = 'usdt')
    EXECUTE FUNCTION enforce_payment_settlement_protocol();
