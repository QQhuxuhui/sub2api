-- Intent routing: doubtful Jev verdicts grouped by message template for an
-- admin to judge, and the judgements, which apply to every group at once.
--
-- A template is the latest user message with digits and random-looking tokens
-- replaced by placeholders (normalization v1, see intent_route_template.go),
-- so one probe sent with a fresh nonce each time is one row. Rows are written
-- off the request path only for doubtful verdicts (reasons a/b/c); a row an
-- admin judged (status probe|health|normal) decides that template from then on
-- without asking Jev. Pending rows not seen for 30 days are pruned; judged rows
-- are kept until revoked.
CREATE TABLE IF NOT EXISTS intent_route_templates (
    template_key   CHAR(64) PRIMARY KEY,
    template       TEXT NOT NULL DEFAULT '',
    sample         TEXT NOT NULL DEFAULT '',
    text_chars     INTEGER NOT NULL DEFAULT 0,
    status         VARCHAR(16) NOT NULL DEFAULT 'pending',
    first_seen_at  TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    last_seen_at   TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    seen_count     BIGINT NOT NULL DEFAULT 0,
    reason_a       BIGINT NOT NULL DEFAULT 0,
    reason_b       BIGINT NOT NULL DEFAULT 0,
    reason_c       BIGINT NOT NULL DEFAULT 0,
    verdict_route  BIGINT NOT NULL DEFAULT 0,
    verdict_health BIGINT NOT NULL DEFAULT 0,
    verdict_normal BIGINT NOT NULL DEFAULT 0,
    probe_min      REAL,
    probe_max      REAL,
    health_min     REAL,
    health_max     REAL,
    user_ids       BIGINT[] NOT NULL DEFAULT '{}',
    group_ids      BIGINT[] NOT NULL DEFAULT '{}',
    decided_at     TIMESTAMPTZ,
    decided_by     BIGINT,
    revision       BIGINT NOT NULL DEFAULT 0,
    CONSTRAINT intent_route_templates_status_check CHECK (status IN ('pending', 'probe', 'health', 'normal'))
);

CREATE INDEX IF NOT EXISTS idx_intent_route_templates_status_seen ON intent_route_templates (status, last_seen_at DESC);
