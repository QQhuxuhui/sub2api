-- 团队消耗看板：令牌 → 归属名 的映射。
-- 设计约束（见 docs/superpowers/specs/2026-08-15-team-usage-console-design.md §3）：
--   1) 主键是 api_key_id 单列。api_keys.id 全局唯一且只属于一个用户；
--      用 (user_id, api_key_id) 复合键会让数据库层允许两个用户认领同一把令牌。
--   2) 本表不挂任何外键。原因四条：ON DELETE CASCADE 会破坏「令牌被物理清理后
--      归属仍在」这个状态；CREATE TABLE ... REFERENCES 会在每次进程启动跑迁移时
--      锁 api_keys/users；旧备份用 --clean --single-transaction 恢复时会因不认识的
--      依赖整体回滚；上游对 api_keys/users 做表级 DDL 会让本 fork 起不来。
--      越权校验在应用层做（PUT /owners 的 WHERE id = ANY($1) AND user_id = $2），
--      孤儿行是惰性的：主查询的 k 子查询已把可见范围锁死在 WHERE user_id = :uid。
--   3) owner_name 为空 = 没有记录（DELETE），不是写空串。CHECK 拦住纯空白的名字。
--      btrim 必须带显式剪除集：btrim(string) 的默认剪除集只有 U+0020，制表、换行、
--      回车、NBSP(U+00A0)、全角空格(U+3000) 都能漏过去，在看板上渲染成一行空名字的
--      归属组；normalize(..., NFC) 救不了，NFC 不把 NBSP 折成空格（那是 NFKC 干的）。
--      剪除集用 \u 转义而不是字面量，避免不可见字节进入这个被 SHA256 锁死的文件；
--      E'' 的 \u 转义要求 server_encoding 为 UTF8。
--      不要用 owner_name ~ '\S' 代替：PG 的 \s 等价 [[:space:]]，locale 相关，仍漏 NBSP。
--   4) 分组比较用 lower(btrim(normalize(owner_name, NFC), E' \t\n\r\u00A0\u3000'))，
--      索引按它建。剪除集必须与上面 CHECK 的那一份、以及 internal/teamops/repo.go 的
--      ownerTrimChars 逐字相同：查询侧用默认剪除集（只剪 U+0020）时，
--      两侧带全角空格(U+3000) 或 NBSP(U+00A0) 的名字会与它的紧凑写法裂成两个分组，
--      而 CHECK 认为它们是同一个合法名字；
--      索引表达式差一个字符则规划器直接用不上这个索引 —— 不报错，只是慢。
--      normalize() 需 PG13+（README 要求 15+），IMMUTABLE，可进表达式索引。
--      用它代替 Go 侧的 golang.org/x/text，避免把 indirect 依赖提升为 direct。
CREATE TABLE IF NOT EXISTS team_key_owners (
    api_key_id BIGINT      PRIMARY KEY,
    user_id    BIGINT      NOT NULL,
    owner_name VARCHAR(64) NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CONSTRAINT team_key_owners_name_not_blank CHECK (btrim(owner_name, E' \t\n\r\u00A0\u3000') <> '')
);

CREATE INDEX IF NOT EXISTS team_key_owners_user_name_idx
    ON team_key_owners (user_id, lower(btrim(normalize(owner_name, NFC), E' \t\n\r\u00A0\u3000')));
