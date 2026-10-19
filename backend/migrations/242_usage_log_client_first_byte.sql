-- 客户端实际首字节耗时：从请求进入网关到第一段响应体写回客户端的毫秒数，
-- 包含鉴权、选号、同账号重试与换号的全部等待。first_token_ms 只从最后一次
-- 成功的上游转发起算，两者相减即为用户看不见的「隐藏等待」。
ALTER TABLE usage_logs ADD COLUMN IF NOT EXISTS client_first_byte_ms INTEGER;
