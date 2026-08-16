package teamops

import (
	"context"
	"database/sql"
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

// ownerTrimChars 是归属名的剪除集，全包唯一的一份，SQL 里凡是 btrim 归属名都要带上它。
//
// btrim(string) 的默认剪除集**只有 U+0020**：制表、换行、回车、NBSP(U+00A0)、
// 全角空格(U+3000) 全都漏过去。漏掉的后果不是「多一个空格」——
// 「王磊」/「　王磊　」(U+3000)/「 王磊 」(U+00A0) 三种写法会裂成三个独立分组，
// 同一个人在看板上出现三行，display_name 还带着不可见 padding。
// normalize(..., NFC) 救不了：NFC 不把 NBSP 折成空格（那是 NFKC 干的）。
//
// 这个字面量必须与 migrations/196a_team_key_owners.sql 里 CHECK 与
// team_key_owners_user_name_idx 的剪除集**逐字相同**：CHECK 与查询不一致会让
// 「入库时合法、查询时算空名」的行出现，索引表达式不一致则规划器直接用不上索引。
const ownerTrimChars = `E' \t\n\r\u00A0\u3000'`

// ownerNameExpr 是归属名的规范形：先 NFC 合成，再按 ownerTrimChars 剪两端。
// 分组键、display_name、搜索、owned_key_count 四处必须全都用它 ——
// 少一处就有一处的口径与另外三处对不上：normalize 只加在分组键上时，
// NFD 形式落库的 "José" 在列表里看得见，按 NFC 搜索却是 0 行（已实跑复现）。
//
// 与 migrations/196a 里 team_key_owners_user_name_idx 的表达式逐字对应
// （索引侧写的是裸列名 owner_name，这里是 o.owner_name，其余一字不差）。
const ownerNameExpr = `btrim(normalize(o.owner_name, NFC), ` + ownerTrimChars + `)`

// groupKeyExpr 是全文件唯一的分组键定义。SELECT 与 GROUP BY 必须用同一份，
// 否则会出现 "同一 group_key 两行" 的 bug（见 spec §4.1）。
// 'o:' / 'k:' 类型前缀不可省：归属名是用户自由填写的文本，可能恰好等于合成键
// （归属名 "k:7" 与令牌 7 撞车），少了前缀两行会被误并成一行。
const groupKeyExpr = `CASE WHEN COALESCE(` + ownerNameExpr + `,'') <> ''
                           THEN 'o:' || lower(` + ownerNameExpr + `)
                           ELSE 'k:' || kr.api_key_id END`

// visibleGroupCond 决定一个分组是否成行。ListRows 的 HAVING 与 Summary 的 g CTE
// **必须共用这一份**，否则 total 与分页、行数与对账条会各说各话。
//
// k 子查询刻意不过滤 deleted_at（"令牌删了但账还在"是设计意图，spec §7），
// 代价是既没账、令牌也没了的软删令牌照样成行：每删一把令牌，看板就永久多一行 $0.00，
// 对账条上的「N 行」跟着涨。真库实测 1 把在用 + 5 把零消耗软删 = 6 行。
//
// 保留「删了但花过钱」（那笔钱必须能被解释），只隐藏「删了且两期都没花过钱」。
// 判两期而不只判本期：只判本期的话，上期花过钱的软删令牌会在环比列留下一个
// 没有出处的基数。
const visibleGroupCond = `(bool_or(kr.deleted_at IS NULL)
       OR SUM(kr.cur_cost) <> 0 OR SUM(kr.prev_cost) <> 0)`

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

// ownerJoin 是全文件唯一的归属表连接。`AND o.user_id = $1` 不可省，也不可换成
// 「靠 api_keys.id 全局唯一」这条推理：
//
// team_key_owners 不挂外键、没有 CHECK、没有触发器（见 migrations/196a 的四条理由），
// 所以 team_key_owners.user_id ≡ api_keys.user_id 这条不变量没有任何数据库对象在保证它。
// api_keys.id 全局唯一保证的是「每把令牌至多一行归属」，不保证那一行里的 user_id
// 就是这把令牌的真归属人。少了这个条件，任何能往本表写行的人都可以给别人的令牌挂上
// 自己写的 owner_name：金额不会跨用户泄露（k 子查询仍锁死 WHERE user_id = $1），
// 但受害者看板上的分组名会变成攻击者写的字，分组结构也跟着被改。
//
// $1 已经是 user_id，不需要新参数；两处查询（ListRows 的 body、Summary 的 g CTE）
// 必须用同一份文本。
const ownerJoin = `LEFT JOIN team_key_owners o
    ON o.api_key_id = kr.api_key_id AND o.user_id = $1`

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

// 等价变异，别再重报：EqualFold 换成 == 不会被任何用例杀掉 —— handler 侧
// 已把 order 收敛成小写白名单，仓储层这层大小写宽容是纯冗余护栏。
func orderDir(order string) string {
	if strings.EqualFold(order, "asc") {
		return "ASC"
	}
	return "DESC"
}

// likeEscaper 把搜索词里的 LIKE 元字符转义掉。不转的话，搜 "%" 命中全部行、
// 搜 "_" 变成单字通配 —— 不是注入（搜索词一直是绑定参数），是搜出来的结果不对。
// NewReplacer 单趟替换、不会再回头处理自己换出来的文本，所以三条规则的先后顺序
// 不影响结果；反斜杠写在第一位只是表明「先转义转义符本身」这个意图。
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

	conds := []string{visibleGroupCond}
	if s := strings.TrimSpace(q.Q); s != "" {
		args = append(args, "%"+escapeLike(strings.ToLower(s))+"%")
		// 搜显示名、令牌名、密钥尾号。显示名侧走 ownerNameExpr，与分组键、display_name
		// 同一份规范形：少了 normalize，NFD 形式落库的 "José" 列表里看得见、按 NFC 搜 0 行。
		//
		// 密钥尾号侧必须带 deleted_at FILTER，令牌名侧刻意**不带**：
		// 软删会把 key 改写成 __deleted__<id>__<nano>，末 4 位是纳秒尾巴，
		// 不过滤的话搜 "3141" 会命中一个存续密钥尾号是 "1111" 的分组——用户在搜出来的
		// 那一行上找不到自己搜的那 4 位。而 name 是软删后原样保留的真实文本，
		// 用户搜「赵六-已删」想找到那笔钱是合理意图，过滤掉反而是回归。
		conds = append(conds, fmt.Sprintf(`(lower(COALESCE(NULLIF(MAX(`+ownerNameExpr+`),''), NULLIF(MAX(kr.name),''), '')) LIKE $%d
                              OR lower(COALESCE(string_agg(COALESCE(kr.name,''), ' '), '')) LIKE $%d
                              OR lower(COALESCE(string_agg(COALESCE(right(kr.key, 4),''), ' ')
                                       FILTER (WHERE kr.deleted_at IS NULL), '')) LIKE $%d)`,
			len(args), len(args), len(args)))
	}
	having := "HAVING " + strings.Join(conds, "\n  AND ")

	selectList := `
    ` + groupKeyExpr + ` AS group_key,
    COALESCE(NULLIF(MAX(` + ownerNameExpr + `),''), MAX(kr.name)) AS display_name,
    bool_or(COALESCE(` + ownerNameExpr + `,'') <> '')        AS by_owner,
    COUNT(*) FILTER (WHERE kr.deleted_at IS NULL)            AS key_count,
    COUNT(*)                                                 AS key_count_all,
    -- 等价变异，别再重报：bool_or 与 bool_and 在 by_owner / all_deleted 上互换
    -- 大多数情况下同解。组内令牌的归属名同质是 groupKeyExpr 的推论
    -- （同一个 group_key 要么都有同一个归属名，要么是同一把令牌），
    -- 所以 by_owner 那一行 bool_or == bool_and 恒成立。
    bool_and(kr.deleted_at IS NOT NULL)                      AS all_deleted,
    -- 口径必须与上面的 key_count 完全一致：都只数没被软删的行。
    -- 用未过滤的 COUNT(*) 的话，一个存续令牌 + 一个历史软删令牌的组会得出
    -- key_count=1 但 single_key=NULL，前端按 key_count==1 走「显示掩码 + 复制」分支却拿到 null。
    -- MAX 同样要带 FILTER：软删会把 key 改写成 __deleted__<id>__<nano>，
    -- 取后 4 位会渲染出纳秒尾巴，而且那是一把已经不存在的令牌的原文位置。
    CASE WHEN COUNT(*) FILTER (WHERE kr.deleted_at IS NULL) = 1
         THEN MAX(kr.key) FILTER (WHERE kr.deleted_at IS NULL)
         ELSE NULL END                                       AS single_key,
    SUM(kr.cur_cost)                                         AS current_cost,
    SUM(kr.prev_cost)                                        AS prev_cost,
    SUM(kr.cur_req)                                          AS requests,
    SUM(kr.prev_req)                                         AS prev_requests,
    -- 与同一行其余「令牌属性」字段（key_count / single_key）统一按存续口径。
    -- 不带 FILTER 时，活令牌从没用过、只有软删令牌用过的组会同时显示
    -- 「活令牌的掩码」和「软删令牌的最后使用时间」——两个字段指的不是同一把令牌。
    MAX(kr.last_used_at) FILTER (WHERE kr.deleted_at IS NULL) AS last_used_at`

	body := baseCTE + `
SELECT` + selectList + `
FROM kr
` + ownerJoin + `
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
                              AND COALESCE(` + ownerNameExpr + `,'') <> '') AS owned_key_count
    FROM kr
    ` + ownerJoin + `
    GROUP BY ` + groupKeyExpr + `
    HAVING ` + visibleGroupCond + `
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
	// 不要为 sql.ErrNoRows 开容错：这条 SELECT 没有 GROUP BY，聚合函数对空集也恒返回一行
	// （COALESCE 把 NULL 兜成 0），ErrNoRows 结构性不可达。写了那个分支的害处在将来 ——
	// 哪天有人给它加上 GROUP BY 或 WHERE，看板就会在真实故障下静默显示「本期消耗 0」而不报错。
	if err != nil {
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
//
// 唯一残留的分叉在长度口径：JS 按 UTF-16 码元计长，这里按 rune 计长，
// 只有非 BMP 字符（如 emoji）才会算出不同的长度。密钥全是 ASCII，实际取不到。
func MaskKey(key string) string {
	if key == "" {
		return ""
	}
	runes := []rune(key)
	if len(runes) <= 12 {
		// 上界必须夹住：JS 的 slice(0, 4) 在长度不足 4 时返回整个字符串，
		// 而 runes[:4] 会切进底层数组的零值区，产出 "abc\x00***" 这种带 NUL 的字符串；
		// 若某次 []rune 转换的 cap 恰好等于 len，同一行直接 panic。
		return string(runes[:min(4, len(runes))]) + "***"
	}
	return string(runes[:6]) + "..." + string(runes[len(runes)-4:])
}
