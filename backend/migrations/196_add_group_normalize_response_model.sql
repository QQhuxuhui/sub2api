-- 分组级开关：开启后把回给客户端的响应体 model 字段无条件归一化为客户端请求的模型。
-- 用途：上游（如自建 new-api）把 gemini-3.1-pro 偷换成 gemini-3.1-pro-low 返回时，
--       默认实现只在"上游返回值 == 我方转发模型"时才改写，因此被偷换的模型名会原样
--       透传给下游；下游若同为 sub2api，其上游响应模型审计会标记 mismatch。
-- 开启本开关后响应对下游一致，但 usage_logs.upstream_response_model /
-- upstream_model_mismatch 仍记录上游真实模型（观测点在改写之前），本地监控能力不受影响。
ALTER TABLE groups ADD COLUMN IF NOT EXISTS normalize_response_model BOOLEAN NOT NULL DEFAULT FALSE;

COMMENT ON COLUMN groups.normalize_response_model IS '响应模型名归一化：true = 响应体 model 无条件回写为客户端请求的模型；false（默认）= 原样透传上游返回的模型名。';
