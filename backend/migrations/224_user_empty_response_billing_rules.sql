-- 用户级「空返回不扣费」规则。
--
-- 命中条件：一次按图计费的生图请求，上游实际没有回吐任何图片数据。此时整笔记 $0
-- （余额不扣、订阅额度不占），usage_logs 仍然落行，便于对账区分「本来就免费」与
-- 「因空返回免费」。
--
-- 作用域两个维度，都可留空表示不限制：
--   group_id IS NULL  -> 该用户在所有分组生效
--   model = ''        -> 该用户在所有模型生效
-- 两者都留空即「该用户全局生效」。模型名大小写不敏感，匹配时会同时比对请求模型、
-- 计费模型与上游模型（见 service.MatchEmptyResponseBillingRule）。
CREATE TABLE IF NOT EXISTS user_empty_response_billing_rules (
    id         BIGSERIAL PRIMARY KEY,
    user_id    BIGINT       NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    group_id   BIGINT       REFERENCES groups(id) ON DELETE CASCADE,
    model      VARCHAR(200) NOT NULL DEFAULT '',
    enabled    BOOLEAN      NOT NULL DEFAULT TRUE,
    note       TEXT,
    created_at TIMESTAMPTZ  NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ  NOT NULL DEFAULT NOW()
);

-- 唯一键要把 NULL group_id 折叠成 0，否则 Postgres 认为多行 NULL 互不相等，
-- 同一个「全分组」规则可以重复插入。
CREATE UNIQUE INDEX IF NOT EXISTS idx_user_empty_response_billing_rules_unique
    ON user_empty_response_billing_rules (user_id, COALESCE(group_id, 0), LOWER(model));

CREATE INDEX IF NOT EXISTS idx_user_empty_response_billing_rules_user
    ON user_empty_response_billing_rules (user_id) WHERE enabled;

CREATE INDEX IF NOT EXISTS idx_user_empty_response_billing_rules_group
    ON user_empty_response_billing_rules (group_id) WHERE group_id IS NOT NULL;

-- 与 usage_logs 同行原子写入，避免重放冲突给旧收费行补审计，或进程在两次写入之间
-- 退出造成免单行缺少对账信息。rule_id 是命中时的快照，不建立到规则表的外键。
ALTER TABLE usage_logs
    ADD COLUMN IF NOT EXISTS empty_response_billing_waived BOOLEAN NOT NULL DEFAULT FALSE,
    ADD COLUMN IF NOT EXISTS empty_response_billing_rule_id BIGINT,
    ADD COLUMN IF NOT EXISTS empty_response_waived_cost NUMERIC(20,10) NOT NULL DEFAULT 0;
