-- OpenAI 账号「缓存模拟」留底：
--   raw_input_tokens / raw_cache_read_tokens 记上游原始的输入 / 缓存读（互斥口径，与
--   input_tokens / cache_read_tokens 同口径），NULL 表示与现有列相同（历史行、非 OpenAI 平台）。
--   cache_sim_cost_delta 记本笔因模拟少收的费用（按原始 usage 本该收的 − 实收），未模拟为 0。
-- 三列只在管理端接口/页面/导出出现，用户端不带。
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS raw_input_tokens INTEGER;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS raw_cache_read_tokens INTEGER;
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS cache_sim_cost_delta DECIMAL(20,10) NOT NULL DEFAULT 0;
