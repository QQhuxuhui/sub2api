package repository

import (
	"context"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// ListOpenAICacheUserUsage returns [startTime, endTime) aggregated by (time
// bucket, user) for OpenAI accounts: the same two scopes as the account query,
// plus the by-user simulation's reachable cache reads (rows without a value
// count their actual cache reads) and the input those values cover.
func (r *openAICacheStatsRepository) ListOpenAICacheUserUsage(ctx context.Context, startTime, endTime time.Time, bucketSeconds int) ([]service.OpenAICacheUserUsageRow, error) {
	if bucketSeconds <= 0 {
		bucketSeconds = int(endTime.Sub(startTime).Seconds()) + 1
	}
	query := `
		SELECT $1::timestamptz + FLOOR(EXTRACT(EPOCH FROM (created_at - $1::timestamptz)) / $3) * ($3 * INTERVAL '1 second') AS bucket_start,
		       user_id,` + openAICacheBucketSelect("TRUE") + `,` + openAICacheBucketSelect(openAICacheTextRequestPredicate) + `,
		       COALESCE(SUM(COALESCE(cache_sim_reachable_tokens, cache_read_tokens)), 0),
		       COALESCE(SUM(input_tokens + cache_read_tokens + cache_creation_tokens) FILTER (WHERE cache_sim_reachable_tokens IS NOT NULL), 0)
		FROM usage_logs
		WHERE created_at >= $1 AND created_at < $2
		  AND account_id IN (SELECT id FROM accounts WHERE platform = 'openai')
		GROUP BY bucket_start, user_id
		ORDER BY bucket_start, user_id
	`
	rows, err := r.sql.QueryContext(ctx, query, startTime, endTime, bucketSeconds)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []service.OpenAICacheUserUsageRow
	for rows.Next() {
		var u service.OpenAICacheUserUsageRow
		dest := append([]any{&u.Bucket, &u.UserID}, scanOpenAICacheBucket(&u.All)...)
		dest = append(dest, scanOpenAICacheBucket(&u.Text)...)
		dest = append(dest, &u.Reachable, &u.Covered)
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListOpenAICacheUserNames returns the e-mail and username of users.
func (r *openAICacheStatsRepository) ListOpenAICacheUserNames(ctx context.Context, ids []int64) (map[int64]service.OpenAICacheUserName, error) {
	out := make(map[int64]service.OpenAICacheUserName, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := r.sql.QueryContext(ctx, `SELECT id, email, COALESCE(username, '') FROM users WHERE id = ANY($1)`, pq.Array(ids))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var id int64
		var n service.OpenAICacheUserName
		if err := rows.Scan(&id, &n.Email, &n.Username); err != nil {
			return nil, err
		}
		out[id] = n
	}
	return out, rows.Err()
}
