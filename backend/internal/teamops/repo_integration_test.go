//go:build integration

package teamops_test

import (
	"context"
	"database/sql"
	"log"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/repository"
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
	// ⚠️ 迁移只在这里跑一次。v1 让每个测试开头都调 ApplyMigrations——
	// 每次都要抢全库 advisory lock + 比对 196 个文件的 SHA256，纯浪费。
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

func TestTeamKeyOwnersTableExists(t *testing.T) {
	ctx := context.Background() // 迁移已在 TestMain 跑过一次，这里不要重复调 ApplyMigrations

	// ⚠️ 必须取**全部**主键列并断言长度为 1。
	// v1 用 QueryRow 只取首行 —— 对 PRIMARY KEY (api_key_id, user_id) 也会返回 api_key_id，
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

	// 表**不应该**有任何外键（挂 FK 的四个副作用见迁移文件注释）
	var fkCount int
	require.NoError(t, integrationDB.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM pg_constraint
		WHERE conrelid = 'team_key_owners'::regclass AND contype = 'f'
	`).Scan(&fkCount))
	require.Equal(t, 0, fkCount, "team_key_owners 不能挂外键")

	// CHECK 约束：空白归属名必须被拒。
	// ⚠️ v1 用了不存在的 api_key_id=999999 —— 表还挂着 FK 时先炸的是外键不是 CHECK，
	// 把 CHECK 整句从迁移里删掉测试照样绿。现在表无 FK，但仍要断言**错误码**而不是只断 require.Error。
	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO team_key_owners (api_key_id, user_id, owner_name) VALUES (900001, 1, '   ')
	`)
	require.Error(t, err)
	var pqErr *pq.Error
	require.ErrorAs(t, err, &pqErr)
	require.Equal(t, pq.ErrorCode("23514"), pqErr.Code, "必须是 CHECK 违反（23514），不是别的约束")
	require.Equal(t, "team_key_owners_name_not_blank", pqErr.Constraint)

	// 反向对照：合法名字必须能插进去（证明没有过度拦截）
	_, err = integrationDB.ExecContext(ctx, `
		INSERT INTO team_key_owners (api_key_id, user_id, owner_name) VALUES (900002, 1, ' 王磊 ')
	`)
	require.NoError(t, err)
}
