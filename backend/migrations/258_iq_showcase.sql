-- IQ showcase: an admin-configured prompt is sent periodically through the
-- real gateway (loopback, with a chosen API key of the group, so auth, intent
-- routing, scheduling, failover and billing all apply) and the SVG the model
-- draws is shown to users as a card, so they can see whether a group's model
-- has been quietly downgraded.
--
-- config_rev goes up whenever something that changes the output or where it
-- belongs changes; results keep the rev, group and model they ran with, and
-- the user page only shows the current rev.
CREATE TABLE IF NOT EXISTS iq_showcase_items (
    id BIGSERIAL PRIMARY KEY,
    title VARCHAR(100) NOT NULL,
    group_id BIGINT NOT NULL REFERENCES groups(id) ON DELETE CASCADE,
    api_key_id BIGINT REFERENCES api_keys(id) ON DELETE SET NULL,
    -- openai_responses | openai_chat | anthropic_messages | gemini
    protocol VARCHAR(32) NOT NULL,
    model VARCHAR(200) NOT NULL,
    prompt TEXT NOT NULL,
    reasoning_effort VARCHAR(20) NOT NULL DEFAULT '',
    max_output_tokens INTEGER NOT NULL DEFAULT 0,
    interval_minutes INTEGER NOT NULL DEFAULT 60,
    max_cards INTEGER NOT NULL DEFAULT 12,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    sort_order INTEGER NOT NULL DEFAULT 0,
    next_run_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    lease_until TIMESTAMPTZ,
    lease_token VARCHAR(64),
    config_rev INTEGER NOT NULL DEFAULT 1,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_iq_showcase_items_due ON iq_showcase_items (next_run_at) WHERE enabled;

-- Failed rounds are admin-only; error never contains the API key.
CREATE TABLE IF NOT EXISTS iq_showcase_results (
    id BIGSERIAL PRIMARY KEY,
    item_id BIGINT NOT NULL REFERENCES iq_showcase_items(id) ON DELETE CASCADE,
    config_rev INTEGER NOT NULL,
    group_id BIGINT NOT NULL,
    model VARCHAR(200) NOT NULL,
    success BOOLEAN NOT NULL,
    svg TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    http_status INTEGER NOT NULL DEFAULT 0,
    latency_ms INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_iq_showcase_results_item
    ON iq_showcase_results (item_id, config_rev, success, created_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_iq_showcase_results_failed_at
    ON iq_showcase_results (created_at) WHERE NOT success;
