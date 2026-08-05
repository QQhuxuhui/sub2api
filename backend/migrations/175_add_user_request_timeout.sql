-- Add per-user gateway request timeout override.
-- request_timeout_seconds: 用户级网关请求整体超时（秒）。
--   0  = 继承全局设置（管理台运行时设置 / gateway.request_timeout_seconds）
--   -1 = 不限制（该用户豁免全局超时；唯一合法的负值哨兵）
--   >0 = 用户专属超时秒数（上限 86400）
-- CHECK 约束防止非法值入库；中间件对越界负值也会回退全局并告警。
ALTER TABLE users ADD COLUMN IF NOT EXISTS request_timeout_seconds integer NOT NULL DEFAULT 0
    CHECK (request_timeout_seconds BETWEEN -1 AND 86400);

COMMENT ON COLUMN users.request_timeout_seconds IS '用户级网关请求整体超时（秒）；0 = 继承全局；-1 = 不限制；>0 = 用户专属值（≤86400）。';
