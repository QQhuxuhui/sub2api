//go:build integration

package teamops_test

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"math"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/repository"
	"github.com/Wei-Shaw/sub2api/internal/teamops"
	// lib/pq 既提供 database/sql 的 "postgres" 驱动（init 注册），也提供 pq.Array / pq.Error。
	"github.com/lib/pq"
	"github.com/stretchr/testify/require"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
)

// integrationDB 由 TestMain 注入。
var integrationDB *sql.DB

// 本 harness 逐条照抄 backend/internal/repository/integration_harness_test.go:45-92 的四个要点，
// 少一条就会改变 `make test-integration`（go test -tags=integration ./...）的既有行为：
//  1. Docker 不可用时 os.Exit(0) 优雅跳过 —— 既有 harness 有这个守卫。少了它，
//     今天本地没起 Docker 跑 make test-integration 是绿的，合进本 PR 之后**必红**。
//     这是「一行现有文件都没改、却实实在在改了现有命令语义」的典型侵入。
//     CI=1 时与既有 harness 一样改为 os.Exit(1) 大声失败，避免集成测试在 CI 里被静默跳过。
//  2. 镜像 tag 钉版 + 读环境变量覆盖 —— 既有 harness 用钉版 tag 并支持降到 15/16 验兼容。
//     用浮动 tag 会让「昨天绿今天红」无从归因，且本功能最难的 SQL 只在 PG18 上验过，
//     而 README 三处承诺支持 15+。
//  3. timezone.Init("UTC") + DSN 带 TimeZone=UTC —— 否则跨 UTC 午夜那条「最重要的测试」
//     在本地与 CI 结果不一致。
//  4. 用 modules/postgres wrapper 而不是裸 GenericContainer —— 后者要 import
//     testcontainers-go 根包，会把它从 indirect 提升为 direct 并改 go.mod。
//     同理，Docker 探活用既有 harness 的 `docker info` 子进程，而不是 docker/docker/client
//     （该模块在 go.mod 里是 indirect，import 它同样会改 go.mod）。
const postgresImageTag = "postgres:18.1-alpine3.23" // 与既有 harness 同一个钉版 tag

func postgresImage() string {
	if v := strings.TrimSpace(os.Getenv("SUB2API_TEST_POSTGRES_IMAGE")); v != "" {
		return v
	}
	return postgresImageTag
}

func dockerIsAvailable(ctx context.Context) bool {
	probeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	cmd := exec.CommandContext(probeCtx, "docker", "info")
	cmd.Env = os.Environ()
	return cmd.Run() == nil
}

func TestMain(m *testing.M) {
	ctx := context.Background()

	if !dockerIsAvailable(ctx) {
		// In CI we expect Docker to be available so integration tests should fail loudly.
		if os.Getenv("CI") != "" {
			log.Printf("docker is not available (CI=true); failing integration tests")
			os.Exit(1)
		}
		log.Printf("docker is not available; skipping teamops integration tests (start Docker to enable)")
		os.Exit(0) // 优雅跳过，不改变 make test-integration 的既有行为
	}

	if err := timezone.Init("UTC"); err != nil {
		log.Printf("failed to init timezone: %v", err)
		os.Exit(1)
	}

	container, err := tcpostgres.Run(ctx, postgresImage(),
		tcpostgres.WithDatabase("sub2api_test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		log.Printf("failed to start postgres container: %v", err)
		os.Exit(1)
	}

	dsn, err := container.ConnectionString(ctx, "sslmode=disable", "TimeZone=UTC")
	if err != nil {
		log.Printf("failed to get postgres dsn: %v", err)
		_ = container.Terminate(ctx)
		os.Exit(1)
	}
	integrationDB, err = sql.Open("postgres", dsn)
	if err != nil {
		log.Printf("failed to open sql db: %v", err)
		_ = container.Terminate(ctx)
		os.Exit(1)
	}
	// 迁移只在 TestMain 跑一次。ApplyMigrations 每次调用都要抢一次全库 advisory lock，
	// 并逐个重算、比对全部迁移文件的 SHA256；把它放进每个测试开头纯属浪费。
	if err := repository.ApplyMigrations(ctx, integrationDB); err != nil {
		log.Printf("failed to apply db migrations: %v", err)
		_ = integrationDB.Close()
		_ = container.Terminate(ctx)
		os.Exit(1)
	}

	code := m.Run()

	_ = integrationDB.Close()
	_ = container.Terminate(ctx)
	os.Exit(code)
}

// testTx 返回一个测试结束时自动回滚的事务，形态同
// backend/internal/repository/integration_harness_test.go:203-212。
// 所有写入都必须走它：TestMain 只跑一次而 m.Run() 可以跑多轮（go test -count=2），
// 直接往 integrationDB 里写会让第二轮撞主键，红成「合法名字被拒」的假象。
func testTx(t *testing.T) *sql.Tx {
	t.Helper()

	tx, err := integrationDB.BeginTx(context.Background(), nil)
	require.NoError(t, err, "begin tx")
	t.Cleanup(func() {
		_ = tx.Rollback()
	})
	return tx
}

func TestTeamKeyOwnersTableExists(t *testing.T) {
	ctx := context.Background() // 迁移已在 TestMain 跑过一次，这里不要重复调 ApplyMigrations

	// 必须取**全部**主键列再断言整个数组。只取首行的写法对
	// PRIMARY KEY (api_key_id, user_id) 也会返回 api_key_id，
	// 于是「防复合键」的断言在复合键下照样 PASS，等于没有守卫。
	var pkCols []string
	err := integrationDB.QueryRowContext(ctx, `
		SELECT array_agg(a.attname ORDER BY a.attnum)
		FROM pg_index i
		JOIN pg_attribute a ON a.attrelid = i.indrelid AND a.attnum = ANY(i.indkey)
		WHERE i.indrelid = 'team_key_owners'::regclass AND i.indisprimary
	`).Scan(pq.Array(&pkCols))
	require.NoError(t, err)
	require.Equal(t, []string{"api_key_id"}, pkCols,
		"主键必须是 api_key_id 单列（复合键会允许跨用户重复认领）")

	// 表**不应该**有任何外键（不挂外键的四条原因见迁移文件注释）
	var fkCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pg_constraint
		WHERE conrelid = 'team_key_owners'::regclass AND contype = 'f'
	`).Scan(&fkCount))
	require.Equal(t, 0, fkCount, "team_key_owners 不能挂外键")

	// CHECK 约束：只由空白构成的归属名必须被拒，否则看板上会出现一行「空名字」的归属组。
	//
	// 断言错误码而不是只断 require.Error：本表没有外键、没有唯一约束以外的其他约束，
	// 只断 require.Error 的话，插入因任何别的原因失败都会让测试变绿 ——
	// 把 CHECK 整句从迁移里删掉都未必红。断到 23514 + 约束名才能证明拦截确实来自这条 CHECK。
	//
	// 剪除集必须覆盖非 ASCII 空白：btrim(string) 的默认剪除集只有 U+0020，
	// 制表/换行/NBSP/全角空格都会漏过去，而 normalize(..., NFC) 不折叠 NBSP（那是 NFKC 的事）。
	blankNames := []struct {
		desc  string
		value string
	}{
		{"ASCII 空格", "   "},
		{"制表符", "\t\t"},
		{"换行", "\n"},
		{"回车", "\r"},
		{"NBSP U+00A0", "\u00a0"},
		{"全角空格 U+3000", "\u3000"},
		{"混合空白", " \t\u00a0\u3000\n"},
	}
	for i, tc := range blankNames {
		t.Run("拒绝纯空白归属名/"+tc.desc, func(t *testing.T) {
			tx := testTx(t)

			_, err := tx.ExecContext(ctx,
				`INSERT INTO team_key_owners (api_key_id, user_id, owner_name) VALUES ($1, 1, $2)`,
				900100+i, tc.value)
			require.Error(t, err)
			var pqErr *pq.Error
			require.ErrorAs(t, err, &pqErr)
			require.Equal(t, pq.ErrorCode("23514"), pqErr.Code, "必须是 CHECK 违反（23514），不是别的约束")
			require.Equal(t, "team_key_owners_name_not_blank", pqErr.Constraint)
		})
	}

	// 反向对照：合法名字必须能插进去，证明剪除集没有过度拦截 ——
	// 只有**整个字符串**都是空白才该被拒，名字两侧带空白（含全角空格）不该被拒。
	legalNames := []struct {
		desc  string
		value string
	}{
		{"两侧 ASCII 空格", " 王磊 "},
		{"两侧全角空格", "\u3000王磊\u3000"},
	}
	for i, tc := range legalNames {
		t.Run("接受合法归属名/"+tc.desc, func(t *testing.T) {
			tx := testTx(t)

			_, err := tx.ExecContext(ctx,
				`INSERT INTO team_key_owners (api_key_id, user_id, owner_name) VALUES ($1, 1, $2)`,
				900200+i, tc.value)
			require.NoError(t, err)
		})
	}
}

// ============================================================================
// 分组查询（Repo.ListRows / Repo.Summary）
// ============================================================================

func TestListRows_GroupsByOwnerOrKeyName(t *testing.T) {
	ctx := context.Background() // 迁移已在 TestMain 跑过一次，这里不要重复调 ApplyMigrations
	seedBasic(t, ctx)

	repo := teamops.NewRepo(integrationDB)
	cur, err := teamops.ParsePeriod("2026-08-01", "2026-08-15", "UTC", time.Now())
	require.NoError(t, err)
	rows, total, err := repo.ListRows(ctx, teamops.RowQuery{
		UserID: 1, Cur: cur, Prev: teamops.DerivePrev(cur),
		Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	require.NoError(t, err)
	require.EqualValues(t, 3, total, "王磊(2把合并) + 李然(未设归属) + 张三(未设归属) = 3 行")

	byKey := map[string]teamops.Row{}
	for _, r := range rows {
		byKey[r.GroupKey] = r
	}

	wl, ok := byKey["o:王磊"]
	require.True(t, ok, "有归属的按 'o:'+lower(归属) 分组")
	require.Equal(t, "王磊", wl.DisplayName)
	require.True(t, wl.ByOwner)
	require.Equal(t, 2, wl.KeyCount)
	require.InDelta(t, 300.0, wl.CurrentCost, 0.0001)

	lr, ok := byKey["k:3"]
	require.True(t, ok, "没归属的按 'k:'+api_key_id 分组")
	require.Equal(t, "李然", lr.DisplayName, "没归属时显示名 = 令牌名")
	require.False(t, lr.ByOwner)
}

func TestListRows_SameNamedKeysDoNotMerge(t *testing.T) {
	ctx := context.Background() // 迁移已在 TestMain 跑过一次，这里不要重复调 ApplyMigrations
	seedSameName(t, ctx)        // 两把都叫「测试」、都没归属

	repo := teamops.NewRepo(integrationDB)
	cur, err := teamops.ParsePeriod("2026-08-01", "2026-08-15", "UTC", time.Now())
	require.NoError(t, err)
	_, total, err := repo.ListRows(ctx, teamops.RowQuery{
		UserID: 2, Cur: cur, Prev: teamops.DerivePrev(cur),
		Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, total, "两把同名但未设归属的令牌必须各自成行，不能误合并")
}

// TestListRows_OwnerNameCollidingWithSyntheticKey 钉住 'o:' / 'k:' 类型前缀。
//
// 断言选的是「两行不合并」，不是「group_key 唯一」：GROUP BY 天然保证每个键只出一行，
// 所以「唯一」这条无论实现怎么错都不会红，等于没有守卫。前缀塌掉（比如归属侧不带 'o:'）
// 的真实后果是归属名 "k:7" 与令牌 7 的合成键撞成同一个值、两行被合并成一行，
// 总行数从 2 掉到 1 —— 这才是能红的断言。
func TestListRows_OwnerNameCollidingWithSyntheticKey(t *testing.T) {
	ctx := context.Background() // 迁移已在 TestMain 跑过一次，这里不要重复调 ApplyMigrations
	seedCollision(t, ctx)       // 令牌 id=7 无归属；另一把令牌归属名恰好是 "k:7"

	repo := teamops.NewRepo(integrationDB)
	cur, err := teamops.ParsePeriod("2026-08-01", "2026-08-15", "UTC", time.Now())
	require.NoError(t, err)
	rows, total, err := repo.ListRows(ctx, teamops.RowQuery{
		UserID: 3, Cur: cur, Prev: teamops.DerivePrev(cur),
		Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	require.NoError(t, err)
	require.EqualValues(t, 2, total, "归属名 'k:7' 与令牌 7 的合成键必须落在不同的分组键上")

	seen := map[string]int{}
	byName := map[string]teamops.Row{}
	for _, r := range rows {
		seen[r.GroupKey]++
		byName[r.DisplayName] = r
	}
	for k, n := range seen {
		require.Equal(t, 1, n, "group_key %q 出现了 %d 次，必须唯一", k, n)
	}
	require.Contains(t, byName, "七号令牌", "无归属的令牌 7 单独成行")
	require.Contains(t, byName, "k:7", "归属名叫 'k:7' 的那一行单独成行")
	require.InDelta(t, 3.0, byName["七号令牌"].CurrentCost, 0.0001)
	require.InDelta(t, 4.0, byName["k:7"].CurrentCost, 0.0001)
}

func TestSummaryEqualsSumOfRows(t *testing.T) {
	ctx := context.Background() // 迁移已在 TestMain 跑过一次，这里不要重复调 ApplyMigrations
	seedBasic(t, ctx)

	repo := teamops.NewRepo(integrationDB)
	cur, err := teamops.ParsePeriod("2026-08-01", "2026-08-15", "UTC", time.Now())
	require.NoError(t, err)
	prev := teamops.DerivePrev(cur)

	rows, _, err := repo.ListRows(ctx, teamops.RowQuery{
		UserID: 1, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 1000,
	})
	require.NoError(t, err)
	s, err := repo.Summary(ctx, 1, cur, prev)
	require.NoError(t, err)

	var sum float64
	for _, r := range rows {
		sum += r.CurrentCost
	}
	require.InDelta(t, s.TotalCost, sum, 1e-9, "恒等式：Σ各行 == 总额")
	// 只断恒等式的话，两边一起塌成 0 也是绿的。钉住绝对值，金额列取错（total_cost 恒为 0）
	// 或区间写错都会红。
	require.InDelta(t, 360.0, s.TotalCost, 1e-9, "seedBasic 的四条日志合计 360")
	require.Equal(t, 3, s.RowCount)
	require.Equal(t, 4, s.KeyCount)
	require.Equal(t, 2, s.OwnedKeyCount, "只有令牌 1、2 设了归属")
	require.InDelta(t, 300.0, s.TopRowCost, 1e-9, "最大一行是王磊的 300")
}

// ---------------------------------------------------------------------------
// seed 辅助：直接裸 SQL 造数据。每组测试用**独立的 user_id 段**做隔离，
// 并用 t.Cleanup 删掉自己造的行，保证 go test -count=2 第二轮不撞主键。
// 造数据一律不改 schema（不 ALTER TABLE / DROP CONSTRAINT）：这是共享测试容器，
// 改了就永久污染，同包后续测试全跑在被改过的库上。
// ---------------------------------------------------------------------------

func seedUser(t *testing.T, ctx context.Context, userID int64) {
	t.Helper()
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO users (id, email, password_hash, role, status)
		VALUES ($1, $2, 'x', 'user', 'active') ON CONFLICT (id) DO NOTHING
	`, userID, fmt.Sprintf("u%d@example.com", userID))
	require.NoError(t, err)
	t.Cleanup(func() {
		// api_keys.user_id 与 usage_logs.user_id 都是 ON DELETE CASCADE，删用户即连根拔起。
		_, _ = integrationDB.ExecContext(context.Background(), `DELETE FROM users WHERE id = $1`, userID)
	})
}

func seedKey(t *testing.T, ctx context.Context, id, userID int64, name string) {
	t.Helper()
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO api_keys (id, user_id, key, name, status)
		VALUES ($1, $2, $3, $4, 'active')
	`, id, userID, fmt.Sprintf("sk-test-%d", id), name)
	require.NoError(t, err)
}

// softDeleteKey 把令牌标成软删。软删令牌的消耗仍要计入金额、但不计入「几把令牌」，
// 需要这个状态的测试用它，不要去 DELETE 物理行。
//
// key 一起改写成 __deleted__<id>__<nano>，与生产的软删逐字一致
// （repository/api_key_repo.go 的 SoftDelete，为的是腾出 key 上的唯一索引）。
// 不照做的话，「软删令牌的掩码不能外泄」这类断言断的是一把现实中根本不存在的明文密钥。
func softDeleteKey(t *testing.T, ctx context.Context, id int64) {
	t.Helper()
	res, err := integrationDB.ExecContext(ctx,
		`UPDATE api_keys SET deleted_at = NOW(), key = $2 WHERE id = $1`,
		id, fmt.Sprintf("__deleted__%d__%d", id, time.Now().UnixNano()))
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n, "软删的令牌必须存在，否则测试在造一个空状态")
}

// account_id 不能硬编码成 1：usage_logs.account_id 是
//
//	account_id BIGINT NOT NULL REFERENCES accounts(id) ON DELETE CASCADE，
//
// 而 teamops 用的是全新空库、accounts 零行 —— 硬编码会让每个集成测试第一次运行就报 23503。
// accounts.type 同样是 NOT NULL 且没有默认值，必须显式给。
var seededAccountID int64

func ensureAccount(t *testing.T, ctx context.Context) int64 {
	t.Helper()
	if seededAccountID != 0 {
		return seededAccountID
	}
	err := integrationDB.QueryRowContext(ctx, `
		INSERT INTO accounts (name, platform, type, status)
		VALUES ('teamops-test', 'anthropic', 'apikey', 'active')
		RETURNING id
	`).Scan(&seededAccountID)
	require.NoError(t, err, "建 account 失败——先确认 accounts 表的必填列，照 001_init.sql 补齐")
	return seededAccountID
}

// seedLog 只写 actual_cost。total_cost 留默认 0，于是「金额列取错」这类改动会让所有
// 金额断言掉到 0 而变红。request_id 拼进了时间戳：usage_logs 上有
// UNIQUE (request_id, api_key_id)，同一把令牌同一时刻插两条会撞唯一索引。
func seedLog(t *testing.T, ctx context.Context, userID, keyID int64, cost float64, at time.Time) {
	t.Helper()
	accountID := ensureAccount(t, ctx)
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO usage_logs (user_id, api_key_id, account_id, request_id, model, actual_cost, created_at)
		VALUES ($1, $2, $3, $4, 'test-model', $5, $6)
	`, userID, keyID, accountID, fmt.Sprintf("req-%d-%d-%d", userID, keyID, at.UnixNano()), cost, at)
	require.NoError(t, err)
}

// seedOwner 必须自带清理：team_key_owners 不挂外键，删用户不会级联到它，
// 少了这个 Cleanup，go test -count=2 第二轮就会撞 api_key_id 主键。
func seedOwner(t *testing.T, ctx context.Context, keyID, userID int64, owner string) {
	t.Helper()
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO team_key_owners (api_key_id, user_id, owner_name) VALUES ($1, $2, $3)
	`, keyID, userID, owner)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = integrationDB.ExecContext(context.Background(),
			`DELETE FROM team_key_owners WHERE api_key_id = $1`, keyID)
	})
}

var (
	inPeriod   = time.Date(2026, 8, 10, 12, 0, 0, 0, time.UTC)
	inPeriod2  = time.Date(2026, 8, 11, 12, 0, 0, 0, time.UTC)
	inPrevOnly = time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC) // 落在 DerivePrev 推出的上期里
)

func seedBasic(t *testing.T, ctx context.Context) {
	seedUser(t, ctx, 1)
	seedKey(t, ctx, 1, 1, "王磊")
	seedKey(t, ctx, 2, 1, "王磊-批处理")
	seedKey(t, ctx, 3, 1, "李然")
	seedKey(t, ctx, 4, 1, "张三")
	seedOwner(t, ctx, 1, 1, "王磊")
	seedOwner(t, ctx, 2, 1, "王磊")
	seedLog(t, ctx, 1, 1, 200.0, inPeriod)
	seedLog(t, ctx, 1, 2, 100.0, inPeriod)
	seedLog(t, ctx, 1, 3, 50.0, inPeriod)
	seedLog(t, ctx, 1, 4, 10.0, inPeriod)
}

func seedSameName(t *testing.T, ctx context.Context) {
	seedUser(t, ctx, 2)
	seedKey(t, ctx, 11, 2, "测试")
	seedKey(t, ctx, 12, 2, "测试")
	seedLog(t, ctx, 2, 11, 5.0, inPeriod)
	seedLog(t, ctx, 2, 12, 7.0, inPeriod)
}

func seedCollision(t *testing.T, ctx context.Context) {
	seedUser(t, ctx, 3)
	seedKey(t, ctx, 7, 3, "七号令牌") // 无归属 → group_key 会是 "k:7"
	seedKey(t, ctx, 8, 3, "撞名令牌")
	seedOwner(t, ctx, 8, 3, "k:7") // 归属名恰好等于另一把令牌的合成键
	seedLog(t, ctx, 3, 7, 3.0, inPeriod)
	seedLog(t, ctx, 3, 8, 4.0, inPeriod)
}

// ---------------------------------------------------------------------------
// 以下测试补的是「brief 那四条测不到、但改坏了会静默上线」的部分：
// 上期窗口、请求数、软删语义、翻页 tiebreaker、搜索、排序分支、跨用户隔离。
// 每条都做过「实现改坏这条会不会红」的自检，结论记在
// .superpowers/sdd/2026-08-15-team-usage-console/task-3-report.md。
// ---------------------------------------------------------------------------

func newTestPeriods(t *testing.T) (teamops.Period, teamops.Period) {
	t.Helper()
	cur, err := teamops.ParsePeriod("2026-08-01", "2026-08-15", "UTC", time.Now())
	require.NoError(t, err)
	return cur, teamops.DerivePrev(cur)
}

func listRows(t *testing.T, ctx context.Context, q teamops.RowQuery) ([]teamops.Row, int64) {
	t.Helper()
	rows, total, err := teamops.NewRepo(integrationDB).ListRows(ctx, q)
	require.NoError(t, err)
	return rows, total
}

func rowsByGroupKey(rows []teamops.Row) map[string]teamops.Row {
	m := map[string]teamops.Row{}
	for _, r := range rows {
		m[r.GroupKey] = r
	}
	return m
}

func groupKeys(rows []teamops.Row) []string {
	out := make([]string, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.GroupKey)
	}
	return out
}

// seedPrevPeriod 造一把跨两个区间都有消耗的令牌：本期 2 笔共 30，上期 1 笔 7。
// 上期与本期的金额、笔数都不相等，所以「上期窗口取成了本期」这种错会被算出来。
func seedPrevPeriod(t *testing.T, ctx context.Context) {
	seedUser(t, ctx, 4)
	seedKey(t, ctx, 20, 4, "跨期令牌")
	seedLog(t, ctx, 4, 20, 10.0, inPeriod)
	seedLog(t, ctx, 4, 20, 20.0, inPeriod2)
	seedLog(t, ctx, 4, 20, 7.0, inPrevOnly)
}

func TestListRows_SplitsCurrentAndPreviousPeriod(t *testing.T) {
	ctx := context.Background()
	seedPrevPeriod(t, ctx)

	cur, prev := newTestPeriods(t)
	rows, total := listRows(t, ctx, teamops.RowQuery{
		UserID: 4, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	require.EqualValues(t, 1, total)
	row := rowsByGroupKey(rows)["k:20"]

	require.InDelta(t, 30.0, row.CurrentCost, 1e-9, "本期只认落在 [Cur.Start, Cur.End) 里的两笔")
	require.InDelta(t, 7.0, row.PrevCost, 1e-9, "上期那笔在 7-20，只有 Prev 窗口能框住它")
	require.EqualValues(t, 2, row.Requests)
	require.EqualValues(t, 1, row.PrevRequests)
}

func TestSummary_SplitsCurrentAndPreviousPeriod(t *testing.T) {
	ctx := context.Background()
	seedPrevPeriod(t, ctx)

	cur, prev := newTestPeriods(t)
	s, err := teamops.NewRepo(integrationDB).Summary(ctx, 4, cur, prev)
	require.NoError(t, err)
	require.InDelta(t, 30.0, s.TotalCost, 1e-9)
	require.InDelta(t, 7.0, s.PrevCost, 1e-9)
	require.EqualValues(t, 2, s.Requests)
	require.EqualValues(t, 1, s.PrevRequests)
}

// liveKeyOfMixedGroup 是 o:赵六 组里**存续**那把令牌的密钥原文，liveKeyOfMixedGroupMask 是它的掩码
// （35 字符，走 MaskKey 的长分支：前 6 + "..." + 后 4）。
//
// 数字开头是刻意的：同组另一把令牌软删后 key 变成 __deleted__31__<nano>，而数字在 C 与
// en_US 两种排序规则下都排在下划线/字母之前。于是「MAX(kr.key) 忘了带 FILTER」这种半吊子修法
// 一定会取到软删那把、掩码对不上而变红。反过来若让存续那把排在最大，MAX 碰巧取对，
// 测试就成了空转。
const (
	liveKeyOfMixedGroup     = "0030-zhaoliu-abcdefghijklmnopqrstuv"
	liveKeyOfMixedGroupMask = "0030-z...stuv"
)

// seedSoftDeleted 造两组：
//   - o:赵六 —— 一把在、一把软删，用来分「金额算进来」「令牌数不算」「掩码取存续那把」三件事。
//   - k:32   —— 唯一一把且已软删，整组是「令牌都没了但账还在」。
func seedSoftDeleted(t *testing.T, ctx context.Context) {
	seedUser(t, ctx, 5)
	seedKeyWithSecret(t, ctx, 30, 5, "赵六-主力", liveKeyOfMixedGroup)
	seedKey(t, ctx, 31, 5, "赵六-已删")
	seedKey(t, ctx, 32, 5, "孤儿已删")
	seedOwner(t, ctx, 30, 5, "赵六")
	seedOwner(t, ctx, 31, 5, "赵六")
	seedLog(t, ctx, 5, 30, 12.0, inPeriod)
	seedLog(t, ctx, 5, 31, 8.0, inPeriod)
	seedLog(t, ctx, 5, 32, 3.0, inPeriod)
	softDeleteKey(t, ctx, 31)
	softDeleteKey(t, ctx, 32)
}

func TestListRows_SoftDeletedKeyCostStillCounts(t *testing.T) {
	ctx := context.Background()
	seedSoftDeleted(t, ctx)

	cur, prev := newTestPeriods(t)
	rows, _ := listRows(t, ctx, teamops.RowQuery{
		UserID: 5, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	byKey := rowsByGroupKey(rows)
	require.Contains(t, byKey, "o:赵六")
	require.InDelta(t, 20.0, byKey["o:赵六"].CurrentCost, 1e-9,
		"钱花掉了就是花掉了：软删令牌的 8 必须仍然计入归属组的金额")
}

func TestListRows_SoftDeletedKeyNotCountedInKeyCount(t *testing.T) {
	ctx := context.Background()
	seedSoftDeleted(t, ctx)

	cur, prev := newTestPeriods(t)
	rows, _ := listRows(t, ctx, teamops.RowQuery{
		UserID: 5, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	row := rowsByGroupKey(rows)["o:赵六"]
	require.Equal(t, 1, row.KeyCount, "只剩 1 把没被软删")
	require.Equal(t, 2, row.KeyCountAll, "历史上一共 2 把")
	require.False(t, row.AllDeleted, "还有一把活着")

	// key_count 与 single_key 必须同一口径。single_key 用未过滤的 COUNT(*) 时，
	// 这一组会得出 key_count=1 但 masked_key=null：前端按 key_count==1 走
	// 「显示掩码 + 复制按钮」的分支，拿到的却是 null。
	require.NotNil(t, row.MaskedKey, "组里只剩一把存续令牌，必须出掩码")
	// 断字面量而不是 teamops.MaskKey(...)：拿函数自己去断函数，等式恒成立。
	require.Equal(t, liveKeyOfMixedGroupMask, *row.MaskedKey, "出的必须是**存续**那把的掩码")
	require.NotContains(t, *row.MaskedKey, "__dele",
		"软删那把的 key 是 __deleted__31__<nano>，一个字符都不能外泄")
}

func TestListRows_GroupWithOnlyDeletedKeysIsMarked(t *testing.T) {
	ctx := context.Background()
	seedSoftDeleted(t, ctx)

	cur, prev := newTestPeriods(t)
	rows, _ := listRows(t, ctx, teamops.RowQuery{
		UserID: 5, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	row := rowsByGroupKey(rows)["k:32"]
	require.True(t, row.AllDeleted)
	require.Equal(t, 0, row.KeyCount)
	require.Equal(t, 1, row.KeyCountAll)
	require.InDelta(t, 3.0, row.CurrentCost, 1e-9)
	require.Nil(t, row.MaskedKey,
		"软删令牌的 key 已被改写成 __deleted__<id>__<nano>，掩码不能出这种码")
}

func TestSummary_KeyCountExcludesSoftDeleted(t *testing.T) {
	ctx := context.Background()
	seedSoftDeleted(t, ctx)

	cur, prev := newTestPeriods(t)
	s, err := teamops.NewRepo(integrationDB).Summary(ctx, 5, cur, prev)
	require.NoError(t, err)
	require.Equal(t, 1, s.KeyCount, "3 把里只有 1 把没被软删")
	require.Equal(t, 1, s.OwnedKeyCount, "设了归属且没被软删的只有令牌 30")
	require.InDelta(t, 23.0, s.TotalCost, 1e-9, "软删不影响金额口径")
}

func TestListRows_MaskedKeyOnlyForSingleLiveKeyGroup(t *testing.T) {
	ctx := context.Background()
	seedBasic(t, ctx)

	cur, prev := newTestPeriods(t)
	rows, _ := listRows(t, ctx, teamops.RowQuery{
		UserID: 1, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	byKey := rowsByGroupKey(rows)

	require.Nil(t, byKey["o:王磊"].MaskedKey, "合并了 2 把令牌的组不该出任何一把的掩码")
	require.NotNil(t, byKey["k:3"].MaskedKey)
	// 断字面量，不断 teamops.MaskKey(...)：拿函数自己去断函数，等式恒成立，
	// 把 MaskKey 的函数体换成 `return key`（明文密钥直接下发）都不会红。
	// "sk-test-3" 是 9 个字符，走 len<=12 的短分支：前 4 位 + "***"。
	require.Equal(t, "sk-t***", *byKey["k:3"].MaskedKey)
}

// productionShapedKey 是一把 35 字符的长密钥，用来走 MaskKey 的长分支。
// seed 里其他令牌都是 sk-test-<id>（≤10 字符），只走短分支；长分支
// （前 6 + "..." + 后 4）在这条测试之前一次都没被执行过，
// 而 repo.go 的注释承诺它与 frontend/src/utils/maskApiKey.ts 逐字等价。
// 生产里真正生成的密钥是 sk- + 32 字节的 hex，共 67 个字符
// （service/api_key_service.go 的 GenerateKey），比这个常量更长；
// 两者都远超 12，走的是同一条分支，67 字符的形态由 mask_test.go 直接覆盖。
const productionShapedKey = "sk-abcdefghijklmnopqrstuvwxyz012345"

func TestListRows_MaskedKeyOfProductionLengthKey(t *testing.T) {
	ctx := context.Background()
	seedUser(t, ctx, 14)
	seedKeyWithSecret(t, ctx, 110, 14, "生产形态令牌", productionShapedKey)
	seedLog(t, ctx, 14, 110, 1.0, inPeriod)

	cur, prev := newTestPeriods(t)
	rows, _ := listRows(t, ctx, teamops.RowQuery{
		UserID: 14, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	row := rowsByGroupKey(rows)["k:110"]
	require.NotNil(t, row.MaskedKey)
	require.Equal(t, "sk-abc...2345", *row.MaskedKey,
		"长密钥走前 6 + \"...\" + 后 4，与 frontend/src/utils/maskApiKey.ts 一致")
}

// tiebreakGroups 是等额分组的个数。刻意取大：8 个分组时，规划器碰巧就按 group_key
// 顺序吐行，把 `, group_key ASC` 整条删掉测试照样绿 —— 那样的测试等于没守。
// 分组多到几十个之后，HashAggregate 的出行顺序（按哈希桶）不可能再与字典序重合，
// 删掉 tiebreaker 必红。带 tiebreaker 的正确实现顺序是完全确定的，所以调大不引入抖动。
const tiebreakGroups = 30

// seedTiebreak 造 tiebreakGroups 个等额分组，且**归属名的字典序与令牌 id 顺序相反**，
// 免得「按 id 出行」这种顺序碰巧与期望一致。
func seedTiebreak(t *testing.T, ctx context.Context) {
	seedUser(t, ctx, 7)
	for i := 0; i < tiebreakGroups; i++ {
		keyID := int64(300 + i)
		seedKey(t, ctx, keyID, 7, fmt.Sprintf("等额令牌-%d", i))
		seedOwner(t, ctx, keyID, 7, fmt.Sprintf("u%02d", tiebreakGroups-1-i))
		seedLog(t, ctx, 7, keyID, 1.0, inPeriod)
	}
}

func TestListRows_EqualCostRowsPaginateWithStableTiebreaker(t *testing.T) {
	ctx := context.Background()
	seedTiebreak(t, ctx)

	const pageSize = 10
	cur, prev := newTestPeriods(t)
	page := func(n int) []string {
		rows, total := listRows(t, ctx, teamops.RowQuery{
			UserID: 7, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: n, PageSize: pageSize,
		})
		require.EqualValues(t, tiebreakGroups, total)
		return groupKeys(rows)
	}

	// 期望序列就是 group_key 的字典序切片：金额全相等，顺序只能由 tiebreaker 决定。
	want := make([]string, 0, tiebreakGroups)
	for i := 0; i < tiebreakGroups; i++ {
		want = append(want, fmt.Sprintf("o:u%02d", i))
	}
	require.Equal(t, want[:pageSize], page(1))
	require.Equal(t, want[pageSize:2*pageSize], page(2), "第二页必须紧接第一页，不重不漏")
	require.Equal(t, want[2*pageSize:], page(3))
}

func seedSearch(t *testing.T, ctx context.Context) {
	seedUser(t, ctx, 8)
	seedKey(t, ctx, 50, 8, "支付网关")
	seedKey(t, ctx, 51, 8, "结算脚本")
	seedKey(t, ctx, 52, 8, "闲置令牌") // 本期零消耗，仍要成行
	seedOwner(t, ctx, 50, 8, "钱七")
	seedLog(t, ctx, 8, 50, 9.0, inPeriod)
	seedLog(t, ctx, 8, 51, 6.0, inPeriod)
}

func TestListRows_Search(t *testing.T) {
	ctx := context.Background()
	seedSearch(t, ctx)
	cur, prev := newTestPeriods(t)

	query := func(q string) ([]teamops.Row, int64) {
		return listRows(t, ctx, teamops.RowQuery{
			UserID: 8, Cur: cur, Prev: prev, Sort: "cost", Order: "desc",
			Page: 1, PageSize: 50, Q: q,
		})
	}

	t.Run("空搜索不过滤", func(t *testing.T) {
		rows, total := query("")
		require.EqualValues(t, 3, total, "零消耗的令牌也要成行，否则用户看不到闲置的令牌")
		require.Len(t, rows, 3)
	})
	t.Run("按归属名搜", func(t *testing.T) {
		rows, total := query("钱七")
		require.EqualValues(t, 1, total)
		require.Equal(t, []string{"o:钱七"}, groupKeys(rows))
	})
	t.Run("按令牌名搜", func(t *testing.T) {
		rows, total := query("结算")
		require.EqualValues(t, 1, total)
		require.Equal(t, []string{"k:51"}, groupKeys(rows))
	})
	t.Run("按密钥尾号搜", func(t *testing.T) {
		rows, total := query("t-52")
		require.EqualValues(t, 1, total)
		require.Equal(t, []string{"k:52"}, groupKeys(rows))
	})
	t.Run("搜不到就是空", func(t *testing.T) {
		rows, total := query("查无此人")
		require.EqualValues(t, 0, total)
		require.Empty(t, rows)
	})
}

func TestListRows_OnlyReturnsCallersOwnKeys(t *testing.T) {
	ctx := context.Background()
	seedUser(t, ctx, 9)
	seedUser(t, ctx, 10)
	seedKey(t, ctx, 60, 9, "我的令牌")
	seedKey(t, ctx, 61, 10, "别人的令牌")
	seedLog(t, ctx, 9, 60, 5.0, inPeriod)
	seedLog(t, ctx, 10, 61, 500.0, inPeriod)

	cur, prev := newTestPeriods(t)
	rows, total := listRows(t, ctx, teamops.RowQuery{
		UserID: 9, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	require.EqualValues(t, 1, total, "只能看到自己的令牌")
	require.Equal(t, []string{"k:60"}, groupKeys(rows))
	require.InDelta(t, 5.0, rows[0].CurrentCost, 1e-9, "别人的 500 不能漏进来")
}

// 排序名用纯 ASCII：中文的字典序取决于数据库 collation，钉住它等于钉住容器镜像的 locale。
func TestListRows_SortByName(t *testing.T) {
	ctx := context.Background()
	seedUser(t, ctx, 11)
	seedKey(t, ctx, 70, 11, "charlie")
	seedKey(t, ctx, 71, 11, "alpha")
	seedKey(t, ctx, 72, 11, "bravo")
	seedLog(t, ctx, 11, 70, 100.0, inPeriod) // 金额顺序与名字顺序刻意相反
	seedLog(t, ctx, 11, 71, 10.0, inPeriod)
	seedLog(t, ctx, 11, 72, 50.0, inPeriod)

	cur, prev := newTestPeriods(t)
	names := func(order string) []string {
		rows, _ := listRows(t, ctx, teamops.RowQuery{
			UserID: 11, Cur: cur, Prev: prev, Sort: "name", Order: order, Page: 1, PageSize: 50,
		})
		out := make([]string, 0, len(rows))
		for _, r := range rows {
			out = append(out, r.DisplayName)
		}
		return out
	}
	require.Equal(t, []string{"alpha", "bravo", "charlie"}, names("asc"))
	require.Equal(t, []string{"charlie", "bravo", "alpha"}, names("desc"))
}

func TestListRows_SortByDeltaPutsNoBaselineRowsLast(t *testing.T) {
	ctx := context.Background()
	seedUser(t, ctx, 12)
	seedKey(t, ctx, 80, 12, "翻倍")
	seedKey(t, ctx, 81, 12, "微涨")
	seedKey(t, ctx, 82, 12, "新令牌")
	seedLog(t, ctx, 12, 80, 200.0, inPeriod)
	seedLog(t, ctx, 12, 80, 100.0, inPrevOnly)
	seedLog(t, ctx, 12, 81, 110.0, inPeriod)
	seedLog(t, ctx, 12, 81, 100.0, inPrevOnly)
	seedLog(t, ctx, 12, 82, 150.0, inPeriod) // 上期为 0：环比无基数

	cur, prev := newTestPeriods(t)
	rows, _ := listRows(t, ctx, teamops.RowQuery{
		UserID: 12, Cur: cur, Prev: prev, Sort: "delta", Order: "desc", Page: 1, PageSize: 50,
	})
	require.Equal(t, []string{"k:80", "k:81", "k:82"}, groupKeys(rows),
		"按环比降序：+100% > +10% > 无基数（NULLS LAST 沉底，不能因为金额大就排前面）")
}

// TestListRows_OwnerNamesNormalizeBeforeGrouping 钉住分组键归属侧的
// lower(btrim(normalize(owner_name, NFC)))：这三层缺一层，"Alice" 与 "alice"、
// 或者两种 Unicode 写法的同一个名字就会裂成两行。它同时也是与
// migrations/196a_team_key_owners.sql 里表达式索引「逐字相同」的行为侧证据。
func TestListRows_OwnerNamesNormalizeBeforeGrouping(t *testing.T) {
	ctx := context.Background()
	seedUser(t, ctx, 13)
	for i, owner := range []string{"Alice", "alice", " Alice ", "\u00e9", "e\u0301"} {
		keyID := int64(90 + i)
		seedKey(t, ctx, keyID, 13, fmt.Sprintf("令牌-%d", keyID))
		seedOwner(t, ctx, keyID, 13, owner)
		seedLog(t, ctx, 13, keyID, 1.0, inPeriod)
	}

	cur, prev := newTestPeriods(t)
	rows, total := listRows(t, ctx, teamops.RowQuery{
		UserID: 13, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	require.EqualValues(t, 2, total, "5 把令牌只该合成 2 个人：alice 与 é")
	byKey := rowsByGroupKey(rows)
	require.Contains(t, byKey, "o:alice", "大小写与两侧空格都不该产生新分组")
	require.Equal(t, 3, byKey["o:alice"].KeyCount)
	require.Contains(t, byKey, "o:\u00e9", "NFC 合成形是分组键的规范形")
	require.Equal(t, 2, byKey["o:\u00e9"].KeyCount, "预组合 é 与 e+U+0301 是同一个人")
}

// ---------------------------------------------------------------------------
// 修复轮次 1：评审跑出的三条「变异存活」+ 三条 Minor 的守卫。
// ---------------------------------------------------------------------------

// seedKeyWithSecret 与 seedKey 的区别只在于密钥原文由调用方给，
// 用来构造 sk- + 32 位这种生产形态的密钥。
func seedKeyWithSecret(t *testing.T, ctx context.Context, id, userID int64, name, secret string) {
	t.Helper()
	_, err := integrationDB.ExecContext(ctx, `
		INSERT INTO api_keys (id, user_id, key, name, status)
		VALUES ($1, $2, $3, $4, 'active')
	`, id, userID, secret, name)
	require.NoError(t, err)
}

func setKeyLastUsed(t *testing.T, ctx context.Context, id int64, at time.Time) {
	t.Helper()
	res, err := integrationDB.ExecContext(ctx,
		`UPDATE api_keys SET last_used_at = $2 WHERE id = $1`, id, at)
	require.NoError(t, err)
	n, err := res.RowsAffected()
	require.NoError(t, err)
	require.EqualValues(t, 1, n)
}

var (
	lastUsedEarly = time.Date(2026, 8, 9, 8, 0, 0, 0, time.UTC)
	lastUsedLate  = time.Date(2026, 8, 12, 9, 30, 0, 0, time.UTC)
)

// seedLastUsed 造一个归属组里两把「最后使用时间」不同的令牌，外加一把从没用过的。
// 两个时间刻意不等距也不同日，MIN / MAX / MIN+1天 三种取法两两不同。
func seedLastUsed(t *testing.T, ctx context.Context) {
	seedUser(t, ctx, 15)
	seedKey(t, ctx, 120, 15, "孙九-旧")
	seedKey(t, ctx, 121, 15, "孙九-新")
	seedKey(t, ctx, 122, 15, "从未使用")
	seedOwner(t, ctx, 120, 15, "孙九")
	seedOwner(t, ctx, 121, 15, "孙九")
	setKeyLastUsed(t, ctx, 120, lastUsedEarly)
	setKeyLastUsed(t, ctx, 121, lastUsedLate)
	seedLog(t, ctx, 15, 120, 1.0, inPeriod)
	seedLog(t, ctx, 15, 121, 2.0, inPeriod)
}

func TestListRows_LastUsedAtTakesLatestInGroup(t *testing.T) {
	ctx := context.Background()
	seedLastUsed(t, ctx)

	cur, prev := newTestPeriods(t)
	rows, _ := listRows(t, ctx, teamops.RowQuery{
		UserID: 15, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	row := rowsByGroupKey(rows)["o:孙九"]
	require.NotNil(t, row.LastUsedAt, "组里有令牌用过，这一列不能是空")
	require.True(t, row.LastUsedAt.Equal(lastUsedLate),
		"「最后使用」取组内最晚的一把，实际拿到 %v，期望 %v", row.LastUsedAt, lastUsedLate)
}

func TestListRows_LastUsedAtNilWhenNeverUsed(t *testing.T) {
	ctx := context.Background()
	seedLastUsed(t, ctx)

	cur, prev := newTestPeriods(t)
	rows, _ := listRows(t, ctx, teamops.RowQuery{
		UserID: 15, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	row := rowsByGroupKey(rows)["k:122"]
	require.Nil(t, row.LastUsedAt, "从没用过的令牌必须是 null，不能拿别的时间顶上")
}

// 分页护栏：这些入参在 handler 层会被 response.ParsePagination 夹住，
// 但仓储层不该依赖调用方 —— 少了护栏，Page=0 会拼出 `OFFSET -50`，
// PostgreSQL 直接报 `OFFSET must not be negative`，一个 ?page=0 就是 500。
func TestListRows_ClampsPaginationInputs(t *testing.T) {
	ctx := context.Background()
	seedTiebreak(t, ctx) // 30 个等额分组
	cur, prev := newTestPeriods(t)

	query := func(page, pageSize int) []teamops.Row {
		rows, total := listRows(t, ctx, teamops.RowQuery{
			UserID: 7, Cur: cur, Prev: prev, Sort: "cost", Order: "desc",
			Page: page, PageSize: pageSize,
		})
		require.EqualValues(t, tiebreakGroups, total)
		return rows
	}

	t.Run("Page=0 当作第一页", func(t *testing.T) {
		require.Equal(t, []string{"o:u00", "o:u01", "o:u02"}, groupKeys(query(0, 3)))
	})
	t.Run("Page 为负当作第一页", func(t *testing.T) {
		require.Equal(t, []string{"o:u00", "o:u01", "o:u02"}, groupKeys(query(-7, 3)))
	})
	t.Run("PageSize=0 回落到默认页长", func(t *testing.T) {
		require.Len(t, query(1, 0), 20, "默认页长 20")
	})
	t.Run("PageSize 超上限回落到默认页长", func(t *testing.T) {
		require.Len(t, query(1, 5000), 20)
	})
	t.Run("Page 极大不溢出成负 OFFSET", func(t *testing.T) {
		// (Page-1)*PageSize 在 int 上会溢出成负数，同样拼出负 OFFSET。
		require.Empty(t, query(math.MaxInt, 10), "翻到天边就是没有行，不是报错")
	})
}

func seedLikeWildcards(t *testing.T, ctx context.Context) {
	seedUser(t, ctx, 16)
	seedKey(t, ctx, 130, 16, "50%折扣")
	seedKey(t, ctx, 131, 16, "5005 号") // 含 "50" 但不含 "50%"
	seedKey(t, ctx, 132, 16, "a_b")
	seedKey(t, ctx, 133, 16, "axb") // 单字通配下会被 "a_b" 误命中
	// 名字里同时含反斜杠和一个会被它转义的 "%"。没有这一把，反斜杠的两条用例
	// 转义与不转义都返空 —— 搜索词恒被包成 %…%，末尾那个 % 兜住了所有非法模式，
	// 「守不住任何东西」的空断言。有了它，两条用例的期望结果才会随实现变化。
	seedKey(t, ctx, 134, 16, `c\%d`)
	for id := int64(130); id <= 134; id++ {
		seedLog(t, ctx, 16, id, 1.0, inPeriod)
	}
}

// 搜索词里的 LIKE 元字符必须转义。不是注入（搜索词一直走绑定参数），
// 是搜出来的结果不对：搜 "%" 命中全部、搜 "_" 变单字通配。
func TestListRows_SearchEscapesLikeWildcards(t *testing.T) {
	ctx := context.Background()
	seedLikeWildcards(t, ctx)
	cur, prev := newTestPeriods(t)

	search := func(q string) []string {
		rows, total := listRows(t, ctx, teamops.RowQuery{
			UserID: 16, Cur: cur, Prev: prev, Sort: "cost", Order: "desc",
			Page: 1, PageSize: 50, Q: q,
		})
		require.EqualValues(t, len(rows), total)
		return groupKeys(rows)
	}

	t.Run("百分号是字面量", func(t *testing.T) {
		require.Equal(t, []string{"k:130"}, search("50%"), "不该把 '5005 号' 也捞上来")
	})
	t.Run("下划线是字面量", func(t *testing.T) {
		require.Equal(t, []string{"k:132"}, search("a_b"), "不该把 'axb' 也捞上来")
	})
	t.Run("反斜杠是字面量", func(t *testing.T) {
		// 转义后是 %\\%（任意 + 一个字面反斜杠 + 任意），只命中名字里真有反斜杠的那把。
		// 不转义则是 %\%（任意 + 一个字面百分号），只命中以 % 结尾的名字 —— 一把都没有。
		require.Equal(t, []string{"k:134"}, search(`\`), "反斜杠要当字面量搜，不是当转义符吃掉后面的字符")
	})
	t.Run("反斜杠不吞掉后面的元字符", func(t *testing.T) {
		// 转义后是 %\\\%%：字面反斜杠 + 字面百分号，只命中 `c\%d`。
		// 不转义则是 %\%%：退化成「含有百分号」，会把 "50%折扣" 一起捞上来。
		require.Equal(t, []string{"k:134"}, search(`\%`), "不该把 '50%折扣' 也捞上来")
	})
}

// 显示名要 btrim：归属名两侧的空格是合法的（迁移的 CHECK 只拦纯空白），
// 但看板上不能渲染成「 周八 」，也不能让它与 "周八" 显示成两个不同的人。
func TestListRows_DisplayNameIsTrimmed(t *testing.T) {
	ctx := context.Background()
	seedUser(t, ctx, 17)
	seedKey(t, ctx, 140, 17, "令牌-140")
	seedOwner(t, ctx, 140, 17, " 周八 ")
	seedLog(t, ctx, 17, 140, 1.0, inPeriod)

	cur, prev := newTestPeriods(t)
	rows, _ := listRows(t, ctx, teamops.RowQuery{
		UserID: 17, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})
	require.Len(t, rows, 1)
	require.Equal(t, "周八", rows[0].DisplayName, "显示名必须去掉两侧空格")
	require.Equal(t, "o:周八", rows[0].GroupKey)
}

// ---------------------------------------------------------------------------
// 修复轮次 2：全分支回扫（真 PG18 实跑 + 变异测试）确认的 6 条 P0 的守卫。
// ---------------------------------------------------------------------------

// P0-1：LEFT JOIN team_key_owners 必须校验 o.user_id。
//
// team_key_owners 不挂外键、没有 CHECK、没有触发器，唯一的写入端点（PUT /owners）
// 还不存在，所以 team_key_owners.user_id ≡ api_keys.user_id 这条不变量没有任何
// 数据库对象在保证。JOIN 不带 o.user_id 的话，任何能往本表写行的人都能给别人的令牌
// 挂上自己写的归属名——金额不跨用户泄露（k 子查询仍锁死 user_id），但受害者看板上
// 的分组名会变成攻击者写的字，分组结构也跟着被改。
//
// 断言选的是「攻击者的名字不出现在任何 display_name 里 + 那一行按令牌分组」，
// 不是「total 不变」：受害者只有一把令牌时，挂不挂归属都是 1 行，行数钉不住任何东西。
func TestListRows_IgnoresOwnerRowsBelongingToAnotherUser(t *testing.T) {
	ctx := context.Background()
	seedUser(t, ctx, 18) // 受害者
	seedUser(t, ctx, 19) // 攻击者
	seedKey(t, ctx, 150, 18, "受害者令牌-A")
	seedKey(t, ctx, 151, 18, "受害者令牌-B")
	// 两行归属都指向受害者的令牌，但 user_id 写的是攻击者自己。
	// 同一个 owner_name 落在两把令牌上：JOIN 漏判 user_id 时两行会被合并成一行。
	seedOwner(t, ctx, 150, 19, "攻击者注入名")
	seedOwner(t, ctx, 151, 19, "攻击者注入名")
	seedLog(t, ctx, 18, 150, 11.0, inPeriod)
	seedLog(t, ctx, 18, 151, 13.0, inPeriod)

	cur, prev := newTestPeriods(t)
	rows, total := listRows(t, ctx, teamops.RowQuery{
		UserID: 18, Cur: cur, Prev: prev, Sort: "cost", Order: "desc", Page: 1, PageSize: 50,
	})

	require.EqualValues(t, 2, total, "别人写的归属行不能把受害者的两把令牌并成一组")
	for _, r := range rows {
		require.NotEqual(t, "攻击者注入名", r.DisplayName, "别人写的归属名不能出现在受害者看板上")
		require.False(t, r.ByOwner, "受害者自己没设过归属，这一行不该被标成按归属分组")
		require.True(t, strings.HasPrefix(r.GroupKey, "k:"),
			"没有合法归属时必须按令牌分组，实际 group_key=%q", r.GroupKey)
	}

	s, err := teamops.NewRepo(integrationDB).Summary(ctx, 18, cur, prev)
	require.NoError(t, err)
	require.Equal(t, 2, s.RowCount, "Summary 的 g CTE 走的是同一个 JOIN，必须一起校验 user_id")
	require.Equal(t, 0, s.OwnedKeyCount, "受害者没有任何一把令牌是自己设的归属")
}
