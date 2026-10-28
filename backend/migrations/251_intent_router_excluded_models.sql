-- Models whose requests skip intent routing entirely (no keywords, no Jev),
-- e.g. codex-auto-review, which carries the whole conversation and so keeps
-- matching keywords the user never just typed.
ALTER TABLE intent_routers ADD COLUMN IF NOT EXISTS excluded_models JSONB NOT NULL DEFAULT '[]'::jsonb;

-- Jev scope: api_key_ids (keys alone, in addition to users) became api_keys
-- [{id, user_id}] that narrow a listed user to some of their keys. Rewrite
-- old documents so they cover exactly what they covered before: a key whose
-- owner was already listed adds nothing (the owner covered all their keys);
-- a key of an unlisted owner lists that owner, narrowed to such keys. Keys
-- whose row is gone covered nothing and are dropped.
UPDATE intent_routers r
SET jev = (r.jev - 'api_key_ids') || jsonb_build_object(
    'user_ids', COALESCE((
        SELECT jsonb_agg(u ORDER BY u) FROM (
            SELECT jsonb_array_elements_text(COALESCE(r.jev->'user_ids', '[]'::jsonb))::bigint AS u
            UNION
            SELECT k.user_id FROM api_keys k
            WHERE k.id IN (SELECT jsonb_array_elements_text(r.jev->'api_key_ids')::bigint)
        ) owners), '[]'::jsonb),
    'api_keys', COALESCE((
        SELECT jsonb_agg(jsonb_build_object('id', k.id, 'user_id', k.user_id) ORDER BY k.id)
        FROM api_keys k
        WHERE k.id IN (SELECT jsonb_array_elements_text(r.jev->'api_key_ids')::bigint)
          AND NOT (COALESCE(r.jev->'user_ids', '[]'::jsonb) @> to_jsonb(k.user_id))
    ), '[]'::jsonb))
WHERE jsonb_typeof(r.jev->'api_key_ids') = 'array';
