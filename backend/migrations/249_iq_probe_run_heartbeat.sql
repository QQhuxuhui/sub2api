-- Running rows are cross-instance locks. A live worker refreshes its heartbeat
-- while queued and while probing; age alone does not imply abandonment.
ALTER TABLE iq_probe_runs ADD COLUMN IF NOT EXISTS heartbeat_at TIMESTAMPTZ NOT NULL DEFAULT NOW();
CREATE INDEX IF NOT EXISTS idx_iq_probe_runs_heartbeat ON iq_probe_runs (heartbeat_at) WHERE status = 'running';
