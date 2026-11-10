-- Intent routing cache smoothing: when a turn is moved to another account by
-- intent routing (or comes back from one), report the cache hit the
-- conversation would have had without the switch. Off by default.
ALTER TABLE intent_routers ADD COLUMN IF NOT EXISTS cache_smooth_enabled BOOLEAN NOT NULL DEFAULT false;
