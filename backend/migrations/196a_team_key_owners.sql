-- 团队消耗看板：令牌 → 归属名 的映射。
-- 设计约束（见 docs/superpowers/specs/2026-08-15-team-usage-console-design.md §3）：
--   1) 主键是 api_key_id 单列。api_keys.id 全局唯一且只属于一个用户；
--      用 (user_id, api_key_id) 复合键会让数据库层允许两个用户认领同一把令牌。
--   2) **不挂任何外键**（复查后改）。挂 FK 会带来四个副作用：ON DELETE CASCADE 破坏
--      「令牌被物理清理后归属仍在」这个 spec §7 定义的状态；CREATE TABLE ... REFERENCES
--      会在每次进程启动跑迁移时锁 api_keys/users；旧备份用 --clean --single-transaction
--      恢复时会因不认识的依赖整体回滚；上游对 api_keys/users 做表级 DDL 会让本 fork 起不来。
--      越权校验在应用层做（PUT /owners 的 WHERE id = ANY($1) AND user_id = $2），
--      孤儿行是惰性的：主查询的 k 子查询已把可见范围锁死在 WHERE user_id = :uid。
--   3) owner_name 为空 = 没有记录（DELETE），不是写空串。CHECK 拦住全空白。
--   4) 分组比较用 lower(btrim(normalize(owner_name, NFC)))，索引按它建。
--      normalize() 需 PG13+（README 要求 15+），IMMUTABLE，可进表达式索引。
--      用它代替 Go 侧的 golang.org/x/text，避免把 indirect 依赖提升为 direct。
CREATE TABLE IF NOT EXISTS team_key_owners (
    api_key_id BIGINT      PRIMARY KEY,
    user_id    BIGINT      NOT NULL,
    owner_name VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT team_key_owners_name_not_blank CHECK (btrim(owner_name) <> '')
);

CREATE INDEX IF NOT EXISTS team_key_owners_user_name_idx
    ON team_key_owners (user_id, lower(btrim(normalize(owner_name, NFC))));
