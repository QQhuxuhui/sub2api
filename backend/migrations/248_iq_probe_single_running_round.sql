-- At most one round of a monitor may be running at a time, across instances:
-- inserting a second "running" row fails, so a manual run and a scheduled run
-- (or two instances) can never probe the same monitor at once. Rows left
-- "running" by a crashed instance are closed by the hourly maintenance.
UPDATE iq_probe_runs r SET status = 'invalid', message = '运行中断（实例重启或超时）', finished_at = NOW()
WHERE r.status = 'running'
  AND EXISTS (SELECT 1 FROM iq_probe_runs o WHERE o.monitor_id = r.monitor_id AND o.status = 'running' AND o.id > r.id);
CREATE UNIQUE INDEX IF NOT EXISTS uq_iq_probe_runs_monitor_running ON iq_probe_runs (monitor_id) WHERE status = 'running';
