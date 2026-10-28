-- TypeSafe Jev probe detection per intent router: keyword misses of short
-- conversations are judged by Jev, and probes are routed like a keyword hit.
-- The API key has its own column so the JSON settings can be shown to admins.
ALTER TABLE intent_routers ADD COLUMN IF NOT EXISTS jev_api_key VARCHAR(512) NOT NULL DEFAULT '';
ALTER TABLE intent_routers ADD COLUMN IF NOT EXISTS jev JSONB NOT NULL DEFAULT '{}'::jsonb;
