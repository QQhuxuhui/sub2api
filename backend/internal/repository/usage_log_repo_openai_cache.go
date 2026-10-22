package repository

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

// openAICacheStatsRepository 读取 usage_logs 的缓存口径聚合（管理页「上游缓存」）。
// 只提供「一段时间内按账号聚合」一种查询：service 层按自然日分片调用并缓存，
// 账号表与按天走势都从同一份分片结果推导，避免对 usage_logs 扫两遍。
// 走 idx_usage_logs_created_at_cache_cover 覆盖索引做 index-only scan（迁移 244）。
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
