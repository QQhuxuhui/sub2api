-- Keywords only count in a user message of at most this many characters
-- (0 = no limit). Long messages are mostly coding-agent context where generic
-- words such as svg / 动画 match real work, not probes.
ALTER TABLE intent_routers ADD COLUMN IF NOT EXISTS keyword_max_chars INTEGER NOT NULL DEFAULT 0;
