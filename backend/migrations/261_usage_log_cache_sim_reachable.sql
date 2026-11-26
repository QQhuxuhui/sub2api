-- 按用户缓存模拟：本笔真实可达缓存读（上限 = 请求中上一轮输入折算的 token 与单笔上限取小，
-- 不低于上游原始缓存读）。NULL 表示不是按用户模拟的请求（历史行、账号模式、明确关闭、
-- 兼容入口、非文本）。只在管理端出现。
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS cache_sim_reachable_tokens INTEGER;
