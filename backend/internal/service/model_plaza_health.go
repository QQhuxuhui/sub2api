package service

import (
	"context"
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 本文件是模型广场的健康数据扩展：复用渠道监控 V2 的被动聚合，但通过自己的接口和
// 白名单结构对外，不改动 V2 的服务、仓储和返回类型。

const (
	plazaHealthBucket      = 20 * time.Minute
	plazaHealthWindow      = 24 * time.Hour
	plazaHealthBucketCount = int(plazaHealthWindow / plazaHealthBucket)
	plazaHealthCacheTTL    = 30 * time.Second
	plazaHealthRange       = "24h_20m"
	plazaHealthUnknown     = "unknown"
	// plazaHealthMaxLag 是聚合数据允许落后的最长时间。超过后宁可不展示健康，
	// 也不把过期的「正常」当作当前状态（聚合任务停滞时旧数据仍可查询）。
	plazaHealthMaxLag = 15 * time.Minute
)

// PlazaHealthSource 是广场读取健康数据的来源，由 *ChannelMonitorV2Service 满足。
type PlazaHealthSource interface {
	Matrix(ctx context.Context, filter ChannelMonitorV2Filter, groupBy ChannelMonitorV2GroupBy, admin bool) (*ChannelMonitorV2Matrix, error)
}

// PlazaHealth 是广场健康接口的返回体。
// 结构是白名单：不含任何请求数、token、RPM/TPM 字段，脱敏由结构本身保证。
type PlazaHealth struct {
	Available bool               `json:"available"`
	Window    *PlazaHealthWindow `json:"window,omitempty"`
	Lines     []PlazaHealthLine  `json:"lines"`
}

type PlazaHealthWindow struct {
	Start         time.Time `json:"start"`
	End           time.Time `json:"end"`
	BucketSeconds int       `json:"bucket_seconds"`
}

// PlazaHealthLine 是一条线路（平台 + 分组 + 模型）近 24 小时的健康摘要。
type PlazaHealthLine struct {
	Platform  string              `json:"platform"`
	GroupID   int64               `json:"group_id"`
	Model     string              `json:"model"`
	State     string              `json:"state"`
	ErrorRate *float64            `json:"error_rate"`
	TTFTP50Ms *int64              `json:"ttft_p50_ms"`
	Rank      *int                `json:"rank"`
	Buckets   []PlazaHealthBucket `json:"buckets"`
}

// PlazaHealthBucket 是一个 20 分钟统计格：状态（只按错误率判定）和错误率。
type PlazaHealthBucket struct {
	State     string   `json:"s"`
	ErrorRate *float64 `json:"e"`
}

type plazaHealthCacheEntry struct {
	value     *PlazaHealth
	expiresAt time.Time
}

// SetHealthSource 注入健康数据来源；未注入时 Health 返回 available=false。仅在装配阶段调用。
func (s *ModelPlazaService) SetHealthSource(src PlazaHealthSource) {
	if s == nil || src == nil {
		return
	}
	s.plazaExt().healthSource = src
}

// Health 返回 allowedGroupIDs 范围内各线路近 24 小时、每 20 分钟一格的健康摘要与热度名次。
// allowedGroupIDs 必须是调用者可用分组的 ID；为空时返回空线路。
func (s *ModelPlazaService) Health(ctx context.Context, allowedGroupIDs []int64) (*PlazaHealth, error) {
	if s == nil || s.ext == nil || s.ext.healthSource == nil {
		return &PlazaHealth{Available: false, Lines: []PlazaHealthLine{}}, nil
	}
	ext := s.ext
	now := time.Now()
	if ext.now != nil {
		now = ext.now()
	}
	end := now.UTC().Truncate(plazaHealthBucket).Add(plazaHealthBucket)
	start := end.Add(-plazaHealthWindow)
	window := &PlazaHealthWindow{Start: start, End: end, BucketSeconds: int(plazaHealthBucket.Seconds())}

	ids := normalizeInt64Set(allowedGroupIDs)
	if len(ids) == 0 {
		return &PlazaHealth{Available: true, Window: window, Lines: []PlazaHealthLine{}}, nil
	}

	key := plazaHealthCacheKey(ids, end)
	if cached := ext.cachedHealth(key, now); cached != nil {
		return cached, nil
	}
	v, err, _ := ext.healthFlight.Do(key, func() (any, error) {
		matrix, err := ext.healthSource.Matrix(ctx, ChannelMonitorV2Filter{
			Range:           plazaHealthRange,
			AllowedGroupIDs: ids,
			RestrictGroups:  true,
			Start:           start,
			End:             end,
			Bucket:          plazaHealthBucket,
		}, ChannelMonitorV2GroupByPlatformGroupModel, true)
		if err != nil {
			if errors.Is(err, ErrChannelMonitorDisabled) {
				return &PlazaHealth{Available: false, Lines: []PlazaHealthLine{}}, nil
			}
			return nil, err
		}
		if plazaHealthStale(matrix, now) {
			// 不缓存：聚合恢复后下一次请求即可恢复展示。
			return &PlazaHealth{Available: false, Lines: []PlazaHealthLine{}}, nil
		}
		result := &PlazaHealth{Available: true, Window: window, Lines: buildPlazaHealthLines(matrix, start)}
		ext.storeHealth(key, result, now)
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*PlazaHealth), nil
}

// plazaHealthStale 判断矩阵背后的聚合数据是否已过期：没有覆盖信息、尚无聚合数据，
// 或数据截止时间落后当前时间超过 plazaHealthMaxLag。
func plazaHealthStale(matrix *ChannelMonitorV2Matrix, now time.Time) bool {
	if matrix == nil {
		return true
	}
	through := matrix.Coverage.DataThrough
	if through.IsZero() {
		return true
	}
	return now.UTC().Sub(through.UTC()) > plazaHealthMaxLag
}

func plazaHealthCacheKey(sortedIDs []int64, end time.Time) string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(end.Unix(), 10))
	for _, id := range sortedIDs {
		b.WriteByte(',')
		b.WriteString(strconv.FormatInt(id, 10))
	}
	return b.String()
}

func (e *modelPlazaExt) cachedHealth(key string, now time.Time) *PlazaHealth {
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	entry, ok := e.healthCache[key]
	if !ok || !now.Before(entry.expiresAt) {
		return nil
	}
	return entry.value
}

func (e *modelPlazaExt) storeHealth(key string, value *PlazaHealth, now time.Time) {
	ttl := e.healthTTL
	if ttl <= 0 {
		ttl = plazaHealthCacheTTL
	}
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	if e.healthCache == nil {
		e.healthCache = make(map[string]plazaHealthCacheEntry)
	}
	for k, entry := range e.healthCache {
		if !now.Before(entry.expiresAt) {
			delete(e.healthCache, k)
		}
	}
	e.healthCache[key] = plazaHealthCacheEntry{value: value, expiresAt: now.Add(ttl)}
}

// buildPlazaHealthLines 把 V2 矩阵（未脱敏）转成广场的白名单线路列表。
// 名次按精确模型名跨平台、跨分组汇总请求数后排出；请求数本身不外传。
func buildPlazaHealthLines(matrix *ChannelMonitorV2Matrix, start time.Time) []PlazaHealthLine {
	lines := []PlazaHealthLine{}
	if matrix == nil {
		return lines
	}
	totals := make(map[string]int64)
	for i := range matrix.Items {
		row := &matrix.Items[i]
		if !plazaHealthRowUsable(row) {
			continue
		}
		totals[row.Model] += row.Metrics.RequestCount
	}
	ranked := make([]string, 0, len(totals))
	for model, total := range totals {
		if total > 0 {
			ranked = append(ranked, model)
		}
	}
	sort.Slice(ranked, func(i, j int) bool {
		if totals[ranked[i]] != totals[ranked[j]] {
			return totals[ranked[i]] > totals[ranked[j]]
		}
		return ranked[i] < ranked[j]
	})
	ranks := make(map[string]int, len(ranked))
	for i, model := range ranked {
		ranks[model] = i + 1
	}

	for i := range matrix.Items {
		row := &matrix.Items[i]
		if !plazaHealthRowUsable(row) {
			continue
		}
		line := PlazaHealthLine{
			Platform:  row.Platform,
			GroupID:   *row.GroupID,
			Model:     row.Model,
			State:     plazaHealthState(row.Health.ErrorRate),
			TTFTP50Ms: row.Metrics.TTFT.P50Ms,
			Buckets:   make([]PlazaHealthBucket, plazaHealthBucketCount),
		}
		if line.State != plazaHealthUnknown {
			rate := row.Metrics.ErrorRate
			line.ErrorRate = &rate
		}
		if rank, ok := ranks[row.Model]; ok {
			r := rank
			line.Rank = &r
		}
		for j := range line.Buckets {
			line.Buckets[j] = PlazaHealthBucket{State: plazaHealthUnknown}
		}
		for j := range row.Buckets {
			point := &row.Buckets[j]
			offset := point.BucketStart.Sub(start)
			if offset < 0 || offset%plazaHealthBucket != 0 {
				continue
			}
			idx := int(offset / plazaHealthBucket)
			if idx >= plazaHealthBucketCount {
				continue
			}
			state := plazaHealthState(point.Health.ErrorRate)
			bucket := PlazaHealthBucket{State: state}
			if state != plazaHealthUnknown {
				rate := point.Metrics.ErrorRate
				bucket.ErrorRate = &rate
			}
			line.Buckets[idx] = bucket
		}
		lines = append(lines, line)
	}
	return lines
}

func plazaHealthRowUsable(row *ChannelMonitorV2MatrixRow) bool {
	return row != nil && row.GroupID != nil && row.Model != "" && row.Model != ChannelMonitorV2OtherModel
}

func plazaHealthState(errorRateState string) string {
	switch errorRateState {
	case "healthy", "warning", "critical":
		return errorRateState
	default:
		return plazaHealthUnknown
	}
}
