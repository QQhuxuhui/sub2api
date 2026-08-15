package teamops

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// queryTimeout 是本包每条聚合查询的自己的上限。看板的查询扫的是 usage_logs，
// 与网关的计费 INSERT 同一个连接池：一条跑飞的看板查询会把连接占着不放，
// 拖慢的是计费写入这条真正要紧的链路。宁可让看板报超时，也不能拖累计费。
const queryTimeout = 15 * time.Second

// maxPageSize / defaultPageSize / maxPage 是仓储层自己的分页护栏，见 ListRows 的前置条件。
const (
	defaultPageSize = 20
	maxPageSize     = 1000
	// maxPage 挡的是 (Page-1)*PageSize 的整数溢出：Page 取 math.MaxInt 时乘出来是负数，
	// 同样会拼出负 OFFSET。夹到 2^20 页之后，offset 最大约 1e9，离溢出还差得远。
	maxPage = 1 << 20
)

// Repo 是团队消耗看板的只读仓储。*sql.DB 由 wire 注入（repository.ProvideSQLDB）。
// 本包用裸 SQL 而不是 ent：分组键是一个 CASE 表达式，GROUP BY 与 SELECT 必须用同一份
// 文本，ORM 表达不了这种约束。
type Repo struct {
	db *sql.DB
}

func NewRepo(db *sql.DB) *Repo {
	return &Repo{db: db}
}

// groupKeyExpr 是全文件唯一的分组键定义。SELECT 与 GROUP BY 必须用同一份，
// 否则会出现 "同一 group_key 两行" 的 bug（见 spec §4.1）。
// 'o:' / 'k:' 类型前缀不可省：归属名是用户自由填写的文本，可能恰好等于合成键
// （归属名 "k:7" 与令牌 7 撞车），少了前缀两行会被误并成一行。
//
// 归属侧的表达式 lower(btrim(normalize(o.owner_name, NFC))) 必须与
// migrations/196a_team_key_owners.sql 里 team_key_owners_user_name_idx 的表达式**逐字相同**，
// 差一个字符规划器就用不上那个表达式索引 —— 不报错，只是慢。
const groupKeyExpr = `CASE WHEN COALESCE(btrim(o.owner_name),'') <> ''
                           THEN 'o:' || lower(btrim(normalize(o.owner_name, NFC)))
                           ELSE 'k:' || kr.api_key_id END`

// baseCTE 是本包所有聚合查询共用的前置 CTE。
// 参数顺序固定：$1=user_id, $2=cur_start, $3=cur_end, $4=prev_start, $5=prev_end
//
// 金额一律取 usage_logs.actual_cost（实际扣费）。total_cost 是折扣前的原始费用，
// 用它会让看板与账单对不上。
const baseCTE = `
WITH k AS (
    SELECT id, name, last_used_at, deleted_at, key
    FROM api_keys WHERE user_id = $1
),
cur AS (
    SELECT api_key_id, SUM(actual_cost) AS cost, COUNT(*) AS requests
    FROM usage_logs
    WHERE user_id = $1 AND created_at >= $2 AND created_at < $3
    GROUP BY api_key_id
),
prev AS (
    SELECT api_key_id, SUM(actual_cost) AS cost, COUNT(*) AS requests
    FROM usage_logs
    WHERE user_id = $1 AND created_at >= $4 AND created_at < $5
    GROUP BY api_key_id
),
kr AS (
    -- LEFT JOIN 而不是 FULL OUTER JOIN：usage_logs.api_key_id 有
    -- REFERENCES api_keys(id) ON DELETE CASCADE（001_init.sql），
    -- 所以「日志有而 api_keys 无」的孤儿行不可达，k 就是完整的行集。
    SELECT
        k.id AS api_key_id,
        k.name, k.last_used_at, k.deleted_at, k.key,
        COALESCE(cur.cost, 0)      AS cur_cost,
        COALESCE(prev.cost, 0)     AS prev_cost,
        COALESCE(cur.requests, 0)  AS cur_req,
        COALESCE(prev.requests, 0) AS prev_req
    FROM k
    LEFT JOIN cur  ON cur.api_key_id  = k.id
    LEFT JOIN prev ON prev.api_key_id = k.id
)`

func sortExpr(sort string) string {
	switch sort {
	case "name":
		return "display_name"
	case "delta":
		// prev=0 时返回 NULL，配合 NULLS LAST 让"新令牌"沉底
		return "(SUM(kr.cur_cost) - SUM(kr.prev_cost)) / NULLIF(SUM(kr.prev_cost), 0)"
	default:
		return "SUM(kr.cur_cost)"
	}
}

func orderDir(order string) string {
	if strings.EqualFold(order, "asc") {
		return "ASC"
	}
	return "DESC"
}

// likeEscaper 把搜索词里的 LIKE 元字符转义掉。不转的话，搜 "%" 命中全部行、
// 搜 "_" 变成单字通配 —— 不是注入（搜索词一直是绑定参数），是搜出来的结果不对。
// 反斜杠必须放在第一位：NewReplacer 单趟替换、不会再回头处理换出来的文本，
// 所以顺序只影响可读性，但把它写在前面能表明「先转义转义符本身」这个意图。
// PostgreSQL 的 LIKE 默认转义符就是反斜杠，不需要额外的 ESCAPE 子句。
var likeEscaper = strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)

func escapeLike(s string) string { return likeEscaper.Replace(s) }

// ListRows 返回分页后的分组行与分组总行数。
// 软删的令牌仍然计入金额（钱花掉了），但不计入 KeyCount（那把令牌已经不在了）。
func (r *Repo) ListRows(ctx context.Context, q RowQuery) ([]Row, int64, error) {
	// 前置条件保护：Page=0 会拼出 OFFSET -50 直接 500。
	// handler 路径走 response.ParsePagination 已夹住（page>0、page_size 1..1000），
	// 但仓储层不该依赖调用方，这几行是本层自己的护栏。
	if q.Page < 1 {
		q.Page = 1
	}
	if q.Page > maxPage {
		q.Page = maxPage
	}
	if q.PageSize < 1 || q.PageSize > maxPageSize {
		q.PageSize = defaultPageSize
	}

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	args := []any{q.UserID, q.Cur.Start, q.Cur.End, q.Prev.Start, q.Prev.End}

	having := ""
	if s := strings.TrimSpace(q.Q); s != "" {
		args = append(args, "%"+escapeLike(strings.ToLower(s))+"%")
		// 搜显示名、令牌名、密钥尾号
		having = fmt.Sprintf(`HAVING lower(COALESCE(NULLIF(MAX(btrim(o.owner_name)),''), NULLIF(MAX(kr.name),''), '')) LIKE $%d
                              OR lower(COALESCE(string_agg(COALESCE(kr.name,''), ' '), '')) LIKE $%d
                              OR lower(COALESCE(string_agg(COALESCE(right(kr.key, 4),''), ' '), '')) LIKE $%d`,
			len(args), len(args), len(args))
	}

	selectList := `
    ` + groupKeyExpr + ` AS group_key,
    COALESCE(NULLIF(MAX(btrim(o.owner_name)),''), MAX(kr.name)) AS display_name,
    bool_or(COALESCE(btrim(o.owner_name),'') <> '')          AS by_owner,
    COUNT(*) FILTER (WHERE kr.deleted_at IS NULL)            AS key_count,
    COUNT(*)                                                 AS key_count_all,
    bool_and(kr.deleted_at IS NOT NULL)                      AS all_deleted,
    CASE WHEN COUNT(*) = 1 AND bool_and(kr.deleted_at IS NULL)
         THEN MAX(kr.key) ELSE NULL END                      AS single_key,
    SUM(kr.cur_cost)                                         AS current_cost,
    SUM(kr.prev_cost)                                        AS prev_cost,
    SUM(kr.cur_req)                                          AS requests,
    SUM(kr.prev_req)                                         AS prev_requests,
    MAX(kr.last_used_at)                                     AS last_used_at`

	body := baseCTE + `
SELECT` + selectList + `
FROM kr
LEFT JOIN team_key_owners o ON o.api_key_id = kr.api_key_id
GROUP BY ` + groupKeyExpr + `
` + having

	// total：分组后的行数
	var total int64
	countSQL := `SELECT COUNT(*) FROM (` + body + `) t`
	if err := r.db.QueryRowContext(ctx, countSQL, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count team rows: %w", err)
	}

	// ORDER BY 必须带稳定 tiebreaker，否则大批等值行（本期零消耗）翻页会重复/漏行
	offset := (q.Page - 1) * q.PageSize
	pageSQL := body + fmt.Sprintf(`
ORDER BY %s %s NULLS LAST, group_key ASC
LIMIT %d OFFSET %d`, sortExpr(q.Sort), orderDir(q.Order), q.PageSize, offset)

	rows, err := r.db.QueryContext(ctx, pageSQL, args...)
	if err != nil {
		return nil, 0, fmt.Errorf("list team rows: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var out []Row
	for rows.Next() {
		var it Row
		var singleKey sql.NullString
		var lastUsed sql.NullTime
		if err := rows.Scan(
			&it.GroupKey, &it.DisplayName, &it.ByOwner,
			&it.KeyCount, &it.KeyCountAll, &it.AllDeleted,
			&singleKey, &it.CurrentCost, &it.PrevCost,
			&it.Requests, &it.PrevRequests, &lastUsed,
		); err != nil {
			return nil, 0, fmt.Errorf("scan team row: %w", err)
		}
		if singleKey.Valid {
			m := MaskKey(singleKey.String)
			it.MaskedKey = &m
		}
		if lastUsed.Valid {
			t := lastUsed.Time
			it.LastUsedAt = &t
		}
		out = append(out, it)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate team rows: %w", err)
	}
	return out, total, nil
}

// Summary 给出看板顶部的汇总。口径必须与 ListRows 完全一致：同一份 groupKeyExpr、
// 同一份 baseCTE，否则「Σ各行 == 总额」这条恒等式就断了。
func (r *Repo) Summary(ctx context.Context, userID int64, cur, prev Period) (Summary, error) {
	q := baseCTE + `,
g AS (
    SELECT ` + groupKeyExpr + ` AS group_key,
           SUM(kr.cur_cost)  AS current_cost,
           SUM(kr.prev_cost) AS prev_cost,
           SUM(kr.cur_req)   AS requests,
           SUM(kr.prev_req)  AS prev_requests,
           COUNT(*) FILTER (WHERE kr.deleted_at IS NULL) AS key_count,
           COUNT(*) FILTER (WHERE kr.deleted_at IS NULL
                              AND COALESCE(btrim(o.owner_name),'') <> '') AS owned_key_count
    FROM kr
    LEFT JOIN team_key_owners o ON o.api_key_id = kr.api_key_id
    GROUP BY ` + groupKeyExpr + `
)
SELECT COALESCE(SUM(current_cost),0), COALESCE(SUM(prev_cost),0),
       COALESCE(MAX(current_cost),0), COUNT(*),
       COALESCE(SUM(key_count),0), COALESCE(SUM(owned_key_count),0),
       COALESCE(SUM(requests),0), COALESCE(SUM(prev_requests),0)
FROM g`

	ctx, cancel := context.WithTimeout(ctx, queryTimeout)
	defer cancel()

	var s Summary
	err := r.db.QueryRowContext(ctx, q, userID, cur.Start, cur.End, prev.Start, prev.End).Scan(
		&s.TotalCost, &s.PrevCost, &s.TopRowCost, &s.RowCount,
		&s.KeyCount, &s.OwnedKeyCount, &s.Requests, &s.PrevRequests,
	)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return Summary{}, fmt.Errorf("team summary: %w", err)
	}
	return s, nil
}

// MaskKey 与 frontend/src/utils/maskApiKey.ts 逐字等价：
//
//	if (!key) return ''
//	if (key.length <= 12) return `${key.slice(0, 4)}***`
//	return `${key.slice(0, 6)}...${key.slice(-4)}`
//
// 软删的令牌 key 被改写成 __deleted__<id>__<nano>，取后 4 位会渲染出纳秒尾巴，
// 所以调用方必须在软删时短路，不要走到这里 —— ListRows 的 single_key 已经把
// 「组里唯一一把且未软删」作为出码条件。
func MaskKey(key string) string {
	if key == "" {
		return ""
	}
	runes := []rune(key)
	if len(runes) <= 12 {
		return string(runes[:4]) + "***"
	}
	return string(runes[:6]) + "..." + string(runes[len(runes)-4:])
}
