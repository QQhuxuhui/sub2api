-- Intent routing keyword hits, kept for reviewing which keywords pull which
-- traffic onto the routed accounts. Written off the request path in batches
-- and pruned by the service after 30 days. request_id is stored in the form
-- usage_logs.request_id uses, so a hit joins to the account and cost it ran on.
CREATE TABLE IF NOT EXISTS intent_route_hits (
    id BIGSERIAL PRIMARY KEY,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    group_id BIGINT NOT NULL,
    user_id BIGINT NOT NULL DEFAULT 0,
    api_key_id BIGINT NOT NULL DEFAULT 0,
    request_id VARCHAR(160) NOT NULL DEFAULT '',
    endpoint VARCHAR(128) NOT NULL DEFAULT '',
    model VARCHAR(255) NOT NULL DEFAULT '',
    rule_name VARCHAR(64) NOT NULL DEFAULT '',
    keyword TEXT NOT NULL DEFAULT '',
    user_turn SMALLINT NOT NULL DEFAULT 0,
    message_chars INTEGER NOT NULL DEFAULT 0,
    excerpt TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_intent_route_hits_created_at ON intent_route_hits (created_at);
CREATE INDEX IF NOT EXISTS idx_intent_route_hits_group_created ON intent_route_hits (group_id, created_at DESC);
