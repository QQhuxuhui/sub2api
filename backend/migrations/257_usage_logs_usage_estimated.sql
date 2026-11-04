-- usage_logs.usage_estimated：本条用量是否为本地估算值。
--
-- 上游 HTTP 200 并交付了内容，却完全没有返回用量（Gemini 原生路径缺 usageMetadata）时，
-- 网关按请求文本与输出字符本地估算 token 计费，并在此标记，便于与上游账单对账时
-- 区分「上游报告的用量」与「本地估算的用量」。
--
-- 带常量默认值的 ADD COLUMN 在 PostgreSQL 11+ 只改目录元数据，不重写分区数据；
-- 但仍需要在 usage_logs 及其分区上短暂获取 ACCESS EXCLUSIVE 锁，长事务会让它排队。
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS usage_estimated BOOLEAN NOT NULL DEFAULT FALSE;
