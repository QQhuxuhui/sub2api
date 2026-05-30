-- 分组级开关：开启后对该分组的 Vertex 等非原生渠道响应做原生信封规范化
ALTER TABLE groups ADD COLUMN normalize_anthropic_envelope BOOLEAN NOT NULL DEFAULT FALSE;
