-- Intelligence probe: periodically ask fixed-answer questions to a chosen
-- account + model through the admin test path (observe-only, unbilled) and
-- score the answers, to spot upstreams that silently swap in a weaker model.
-- Answers are pruned by the runner after 30 days, runs after 180 days.
CREATE TABLE IF NOT EXISTS iq_probe_question_sets (
    id BIGSERIAL PRIMARY KEY,
    name VARCHAR(100) NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

-- judge_type: exact | choice | numeric
CREATE TABLE IF NOT EXISTS iq_probe_questions (
    id BIGSERIAL PRIMARY KEY,
    set_id BIGINT NOT NULL REFERENCES iq_probe_question_sets(id) ON DELETE CASCADE,
    title VARCHAR(200) NOT NULL DEFAULT '',
    prompt TEXT NOT NULL,
    system_prompt TEXT NOT NULL DEFAULT '',
    expected_answer TEXT NOT NULL,
    judge_type VARCHAR(20) NOT NULL DEFAULT 'exact',
    numeric_tolerance DOUBLE PRECISION NOT NULL DEFAULT 0,
    weight DOUBLE PRECISION NOT NULL DEFAULT 1,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    note TEXT NOT NULL DEFAULT '',
    sort_order INTEGER NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_iq_probe_questions_set ON iq_probe_questions (set_id, sort_order, id);

-- alert_state: ok | low | invalid (what the last alert e-mail reported)
CREATE TABLE IF NOT EXISTS iq_probe_monitors (
    id BIGSERIAL PRIMARY KEY,
    account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE,
    group_id BIGINT,
    model VARCHAR(200) NOT NULL,
    set_id BIGINT NOT NULL REFERENCES iq_probe_question_sets(id) ON DELETE RESTRICT,
    interval_minutes INTEGER NOT NULL DEFAULT 60,
    reasoning_effort VARCHAR(20) NOT NULL DEFAULT '',
    max_tokens INTEGER NOT NULL DEFAULT 0,
    enabled BOOLEAN NOT NULL DEFAULT TRUE,
    alert_enabled BOOLEAN NOT NULL DEFAULT FALSE,
    alert_threshold DOUBLE PRECISION NOT NULL DEFAULT 0.7,
    alert_low_rounds INTEGER NOT NULL DEFAULT 2,
    alert_invalid_rounds INTEGER NOT NULL DEFAULT 3,
    alert_state VARCHAR(20) NOT NULL DEFAULT 'ok',
    consecutive_low INTEGER NOT NULL DEFAULT 0,
    consecutive_invalid INTEGER NOT NULL DEFAULT 0,
    last_run_at TIMESTAMPTZ,
    next_run_at TIMESTAMPTZ,
    last_status VARCHAR(20) NOT NULL DEFAULT '',
    last_score DOUBLE PRECISION,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE UNIQUE INDEX IF NOT EXISTS uq_iq_probe_monitors_target ON iq_probe_monitors (account_id, model, set_id);
CREATE INDEX IF NOT EXISTS idx_iq_probe_monitors_due ON iq_probe_monitors (next_run_at) WHERE enabled = TRUE;

-- status: scored | invalid | skipped | running
CREATE TABLE IF NOT EXISTS iq_probe_runs (
    id BIGSERIAL PRIMARY KEY,
    monitor_id BIGINT NOT NULL REFERENCES iq_probe_monitors(id) ON DELETE CASCADE,
    account_id BIGINT NOT NULL,
    model VARCHAR(200) NOT NULL,
    set_id BIGINT NOT NULL,
    trigger VARCHAR(20) NOT NULL DEFAULT 'schedule',
    status VARCHAR(20) NOT NULL,
    score DOUBLE PRECISION,
    total_count INTEGER NOT NULL DEFAULT 0,
    correct_count INTEGER NOT NULL DEFAULT 0,
    wrong_count INTEGER NOT NULL DEFAULT 0,
    format_error_count INTEGER NOT NULL DEFAULT 0,
    error_count INTEGER NOT NULL DEFAULT 0,
    earned_weight DOUBLE PRECISION NOT NULL DEFAULT 0,
    total_weight DOUBLE PRECISION NOT NULL DEFAULT 0,
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    message TEXT NOT NULL DEFAULT '',
    started_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    finished_at TIMESTAMPTZ
);
CREATE INDEX IF NOT EXISTS idx_iq_probe_runs_monitor ON iq_probe_runs (monitor_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_iq_probe_runs_started ON iq_probe_runs (started_at DESC);

-- verdict: correct | wrong | format_error | error | unsupported
-- Question fields are snapshotted so edits/deletes keep old rounds readable.
CREATE TABLE IF NOT EXISTS iq_probe_answers (
    id BIGSERIAL PRIMARY KEY,
    run_id BIGINT NOT NULL REFERENCES iq_probe_runs(id) ON DELETE CASCADE,
    question_id BIGINT NOT NULL,
    question_title VARCHAR(200) NOT NULL DEFAULT '',
    expected_answer TEXT NOT NULL DEFAULT '',
    judge_type VARCHAR(20) NOT NULL DEFAULT 'exact',
    weight DOUBLE PRECISION NOT NULL DEFAULT 1,
    verdict VARCHAR(20) NOT NULL,
    extracted_answer TEXT NOT NULL DEFAULT '',
    response_text TEXT NOT NULL DEFAULT '',
    error_message TEXT NOT NULL DEFAULT '',
    upstream_model VARCHAR(200) NOT NULL DEFAULT '',
    response_model VARCHAR(200) NOT NULL DEFAULT '',
    input_tokens BIGINT NOT NULL DEFAULT 0,
    output_tokens BIGINT NOT NULL DEFAULT 0,
    reasoning_tokens BIGINT NOT NULL DEFAULT 0,
    latency_ms BIGINT NOT NULL DEFAULT 0,
    first_token_ms BIGINT NOT NULL DEFAULT 0,
    cost_usd DOUBLE PRECISION NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_iq_probe_answers_run ON iq_probe_answers (run_id);
CREATE INDEX IF NOT EXISTS idx_iq_probe_answers_created ON iq_probe_answers (created_at);
