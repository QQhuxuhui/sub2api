-- Early first byte for OpenAI streaming requests: a protocol-neutral event is
-- sent before the upstream answers, per group and scoped to users / API keys.
-- Standalone table: independent of intent_routers, no foreign key to groups.
-- Off unless a row says otherwise.
CREATE TABLE IF NOT EXISTS openai_stream_preludes (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL,
    config JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS openaistreamprelude_group_id ON openai_stream_preludes (group_id);
