-- Answering sub2api channel-monitor challenges by program, per group and
-- scoped to users / API keys. Standalone table: independent of intent_routers,
-- no foreign key to groups. Off unless a row says otherwise.
CREATE TABLE IF NOT EXISTS monitor_probe_answers (
    id BIGSERIAL PRIMARY KEY,
    group_id BIGINT NOT NULL,
    config JSONB,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX IF NOT EXISTS monitorprobeanswer_group_id ON monitor_probe_answers (group_id);
