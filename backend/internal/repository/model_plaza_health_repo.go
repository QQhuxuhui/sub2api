package repository

import (
	"context"
	"database/sql"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/lib/pq"
)

// 本文件是模型广场健康数据的读取端：直接读渠道监控 V2 的小时聚合表。
// 只借用聚合结果，不受 V2 配置里的分组范围和模型名单限制（新建的分组、新上的模型自动出现），
// 也不改动 V2 的仓储代码。被忽略的错误类别沿用 V2 配置（运营已调好的口径）。

type modelPlazaHealthRepository struct{ db *sql.DB }

// NewModelPlazaHealthRepository 返回广场健康数据的读取端。
func NewModelPlazaHealthRepository(db *sql.DB) service.PlazaHealthStore {
	return &modelPlazaHealthRepository{db: db}
}

const modelPlazaHourlyHealthSQL = `
WITH ignored AS (
  SELECT e.bucket_start, e.platform, e.group_id, e.model, SUM(e.error_requests) AS n
  FROM channel_monitor_v2_error_metrics_rollup e
  WHERE e.bucket_seconds = 3600 AND e.bucket_start >= $1 AND e.bucket_start < $2
    AND e.group_id = ANY($3)
    AND e.error_category IN (
      SELECT unnest(COALESCE(c.ignored_error_categories, '{}'))
      FROM channel_monitor_v2_config c WHERE c.id = 1
    )
  GROUP BY 1, 2, 3, 4
)
SELECT m.platform, m.group_id, m.model, m.bucket_start, m.success_requests,
       GREATEST(m.error_requests - COALESCE(i.n, 0), 0), m.ttft_sum_ms, m.ttft_count
FROM channel_monitor_v2_metrics_rollup m
LEFT JOIN ignored i
  ON i.bucket_start = m.bucket_start AND i.platform = m.platform
 AND i.group_id = m.group_id AND i.model = m.model
WHERE m.bucket_seconds = 3600 AND m.bucket_start >= $1 AND m.bucket_start < $2
  AND m.group_id = ANY($3)
  AND m.success_requests + GREATEST(m.error_requests - COALESCE(i.n, 0), 0) > 0
ORDER BY m.platform, m.group_id, m.model, m.bucket_start`

// PlazaHourlyHealth 返回 [start, end) 内各线路「有调用的小时」，按线路、时间升序。
func (r *modelPlazaHealthRepository) PlazaHourlyHealth(ctx context.Context, groupIDs []int64, start, end time.Time) (*service.PlazaHourlyHealth, error) {
	out := &service.PlazaHourlyHealth{Rows: []service.PlazaHourlyRow{}}
	var through sql.NullTime
	err := r.db.QueryRowContext(ctx, `SELECT data_through FROM channel_monitor_v2_watermarks WHERE id = 1`).Scan(&through)
	if err != nil && err != sql.ErrNoRows {
		return nil, err
	}
	if through.Valid {
		out.DataThrough = through.Time
	}
	if len(groupIDs) == 0 {
		return out, nil
	}
	rows, err := r.db.QueryContext(ctx, modelPlazaHourlyHealthSQL, start, end, pq.Array(groupIDs))
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var row service.PlazaHourlyRow
		if err := rows.Scan(&row.Platform, &row.GroupID, &row.Model, &row.Hour, &row.Success, &row.Failed, &row.TTFTSumMs, &row.TTFTCount); err != nil {
			return nil, err
		}
		out.Rows = append(out.Rows, row)
	}
	return out, rows.Err()
}
