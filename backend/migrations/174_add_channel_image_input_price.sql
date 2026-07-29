-- 渠道 token 定价新增图片输入价（每 token，USD）。
-- 语义：NULL = 未配置，图片输入 token 回退按 input_price 计费（既有行为不变）；
-- 显式配置后按该价计费（含显式 0 = 图片输入免费，不回退）。
-- 精度对齐 input_price（NUMERIC(20,12)，per-token 单价）。
ALTER TABLE channel_model_pricing
    ADD COLUMN IF NOT EXISTS image_input_price NUMERIC(20,12);
