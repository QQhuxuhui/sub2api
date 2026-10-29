-- Status reported when a request matched an intent rule but none of the
-- rule's accounts can take it. It used to be a hard-coded 429, which makes a
-- downstream sub2api cool the whole key down; 503 (the new default) only makes
-- it fail over.
ALTER TABLE intent_routers ADD COLUMN IF NOT EXISTS unavailable_status INTEGER NOT NULL DEFAULT 503;
