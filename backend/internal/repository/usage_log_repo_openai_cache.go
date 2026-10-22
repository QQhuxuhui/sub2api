package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// openAICacheStatsRepository 读取 usage_logs 的缓存口径聚合（管理页「上游缓存」）。
type openAICacheStatsRepository struct {
	sql sqlExecutor
}

// NewOpenAICacheStatsRepository 构造。
func NewOpenAICacheStatsRepository(sqlDB *sql.DB) service.OpenAICacheStatsRepository {
	return &openAICacheStatsRepository{sql: sqlDB}
}

// openAICacheTextRequestPredicate 判定「文本对话」请求：排除生图、视频、embeddings 和 WebSocket。
// 与 service.openAICacheSimEligibleResult 的口径对齐（搜索/音频在 usage_logs 没有独立列，
// 占比可忽略）。
const openAICacheTextRequestPredicate = `(openai_ws_mode = false AND image_count = 0 AND image_output_tokens = 0 AND image_input_tokens = 0 AND video_count = 0 AND COALESCE(inbound_endpoint, '') NOT ILIKE '%embeddings%')`

// openAICacheBucketSelect 生成一个口径（all / text）的聚合列；cond 为 SQL 布尔表达式。
func openAICacheBucketSelect(cond string) string {
	return fmt.Sprintf(`
		COUNT(*) FILTER (WHERE %[1]s),
		COUNT(*) FILTER (WHERE %[1]s AND cache_read_tokens = 0),
		COALESCE(SUM(input_tokens) FILTER (WHERE %[1]s), 0),
		COALESCE(SUM(cache_read_tokens) FILTER (WHERE %[1]s), 0),
		COALESCE(SUM(cache_creation_tokens) FILTER (WHERE %[1]s), 0),
		COALESCE(SUM(COALESCE(raw_input_tokens, input_tokens)) FILTER (WHERE %[1]s), 0),
		COALESCE(SUM(COALESCE(raw_cache_read_tokens, cache_read_tokens)) FILTER (WHERE %[1]s), 0),
		COALESCE(SUM(actual_cost) FILTER (WHERE %[1]s), 0),
		COALESCE(SUM(cache_sim_cost_delta) FILTER (WHERE %[1]s), 0)`, cond)
}

func scanOpenAICacheBucket(b *service.OpenAICacheTokenBucket) []any {
	return []any{&b.Requests, &b.ZeroCacheReqs, &b.Input, &b.CacheRead, &b.CacheCreation, &b.RawInput, &b.RawCacheRead, &b.BilledCost, &b.CostDelta}
}

func (r *openAICacheStatsRepository) ListOpenAICacheAccountUsage(ctx context.Context, startTime, endTime time.Time) ([]service.OpenAICacheAccountUsage, error) {
	query := `
		SELECT account_id,` + openAICacheBucketSelect("TRUE") + `,` + openAICacheBucketSelect(openAICacheTextRequestPredicate) + `
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
		  AND account_id IN (SELECT id FROM accounts WHERE platform = 'openai')
		GROUP BY account_id
	`
	rows, err := r.sql.QueryContext(ctx, query, startTime, endTime)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []service.OpenAICacheAccountUsage
	for rows.Next() {
		var u service.OpenAICacheAccountUsage
		dest := append([]any{&u.AccountID}, scanOpenAICacheBucket(&u.All)...)
		dest = append(dest, scanOpenAICacheBucket(&u.Text)...)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func (r *openAICacheStatsRepository) ListOpenAICacheDailyUsage(ctx context.Context, startTime, endTime time.Time, accountID int64) ([]service.OpenAICacheDailyUsage, error) {
	// The handler parses the range in the user's timezone. Group in that same
	// timezone rather than the database connection's session timezone.
	tzName := startTime.Location().String()
	if tzName == "" || tzName == "Local" {
		tzName = resolveUsageStatsTimezone()
	}
	args := []any{startTime, endTime, tzName}
	accountCond := "account_id IN (SELECT id FROM accounts WHERE platform = 'openai')"
	if accountID > 0 {
		accountCond = "account_id = $4"
		args = append(args, accountID)
	}
	query := `
		SELECT TO_CHAR(created_at AT TIME ZONE $3, 'YYYY-MM-DD') AS date,` + openAICacheBucketSelect("TRUE") + `,` + openAICacheBucketSelect(openAICacheTextRequestPredicate) + `
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2 AND ` + accountCond + `
		GROUP BY date
		ORDER BY date
	`
	rows, err := r.sql.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []service.OpenAICacheDailyUsage
	for rows.Next() {
		var d service.OpenAICacheDailyUsage
		dest := append([]any{&d.Date}, scanOpenAICacheBucket(&d.All)...)
		dest = append(dest, scanOpenAICacheBucket(&d.Text)...)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}
