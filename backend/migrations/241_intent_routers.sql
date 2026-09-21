-- Intent routing: classify a request with a cheap model, then send it to the
-- accounts configured for that intent. Standalone table on purpose: no column
-- is added to groups or accounts, and no foreign key ties it to them.
CREATE TABLE IF NOT EXISTS intent_routers (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL,
    enabled BOOLEAN NOT NULL DEFAULT FALSE,
    classifier_base_url VARCHAR(512) NOT NULL DEFAULT '',
    classifier_api_key VARCHAR(512) NOT NULL DEFAULT '',
    classifier_protocol VARCHAR(32) NOT NULL DEFAULT 'openai_chat',
    classifier_model VARCHAR(128) NOT NULL DEFAULT '',
    classifier_timeout_ms INTEGER NOT NULL DEFAULT 3000,
    cache_ttl_seconds INTEGER NOT NULL DEFAULT 7200,
    max_input_chars INTEGER NOT NULL DEFAULT 2000,
    rules JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS intentrouter_group_id ON intent_routers (group_id);
