-- Length of the latest user message at the time of a hit. A keyword in the
-- message before it only counts when the latest one is a short follow-up such
-- as "继续"; this column shows which latest messages let such hits through.
ALTER TABLE intent_route_hits ADD COLUMN IF NOT EXISTS newest_chars INTEGER NOT NULL DEFAULT 0;
