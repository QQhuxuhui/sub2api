package service

import (
	"context"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 本文件是模型广场的健康数据扩展：读渠道监控 V2 的小时聚合结果，通过自己的接口和
// 白名单结构对外，不改动 V2 的服务、仓储和返回类型。
//
// 展示口径（与 new-api 模型广场一致）：
//   - 一个采样 = 一条线路「有调用的一个小时」；没调用的小时不产生采样，也不占位；
//   - 每条线路取最近 plazaHealthSampleLimit 个采样，最远回看 plazaHealthLookback；
//   - 没有最低样本数门槛，有 1 次调用就计入。

const (
	plazaHealthHour        = time.Hour
	plazaHealthSampleLimit = 24
	plazaHealthLookback    = 7 * 24 * time.Hour
	plazaHealthRankWindow  = 24 * time.Hour
	plazaHealthCacheTTL    = 30 * time.Second
	// plazaListedLinesTTL 是广场清单（上架线路集合）在健康接口里的复用时间：
	// 模型下架后最多这么久，健康接口就不再返回它。
	plazaListedLinesTTL = 5 * time.Second
	// plazaHealthMaxLag 是聚合数据允许落后的最长时间。超过后宁可不展示健康，
	// 也不把过期的「正常」当作当前状态（聚合任务停滞时旧数据仍可查询）。
	plazaHealthMaxLag = 15 * time.Minute

	plazaHealthHealthy  = "healthy"
	plazaHealthWarning  = "warning"
	plazaHealthCritical = "critical"
)

// PlazaHealthStore 是广场读取健康数据的来源（小时聚合表）。
type PlazaHealthStore interface {
	PlazaHourlyHealth(ctx context.Context, groupIDs []int64, start, end time.Time) (*PlazaHourlyHealth, error)
}

// PlazaHourlyHealth 是存储层返回的原始小时数据，只在服务内部使用，不对外。
type PlazaHourlyHealth struct {
	// DataThrough 是聚合数据的截止时间；零值表示尚无聚合数据。
	DataThrough time.Time
	// Rows 按（平台、分组、模型、小时）升序，且每行 Success+Failed+Ignored > 0。
	Rows []PlazaHourlyRow
}

// PlazaHourlyRow 是一条线路在一个小时内的计数。Failed 已扣除被忽略的错误类别，
// Ignored 是被扣除的那部分：不计入成功率，只参与热度名次。
type PlazaHourlyRow struct {
	Platform  string
	GroupID   int64
	Model     string
	Hour      time.Time
	Success   int64
	Failed    int64
	Ignored   int64
	TTFTSumMs int64
	TTFTCount int64
}

// PlazaHealth 是广场健康接口的返回体。
// 结构是白名单：不含任何请求数、token、RPM/TPM 字段，脱敏由结构本身保证。
type PlazaHealth struct {
	Available bool              `json:"available"`
	Lines     []PlazaHealthLine `json:"lines"`
	// Ranks 是模型名 → 近 24 小时热度名次（1 起）。独立于 Lines：一个模型即使没有可计采样
	// （例如调用全部落在被忽略的错误类别里）也有名次。只有名次，没有数量。
	Ranks map[string]int `json:"ranks"`
}

// PlazaHealthLine 是一条线路（平台 + 分组 + 模型）最近若干个有调用小时的健康摘要。
type PlazaHealthLine struct {
	Platform string `json:"platform"`
	GroupID  int64  `json:"group_id"`
	Model    string `json:"model"`
	// State 按 Samples 合计的成功率判定：≥90% healthy，≥70% warning，其余 critical。
	State string `json:"state"`
	// ErrorRate 是 Samples 合计的失败占比（0~1）。
	ErrorRate float64 `json:"error_rate"`
	// TTFTMs 是 Samples 内的平均首字耗时；没有首字样本为 null。
	TTFTMs *int64 `json:"ttft_ms"`
	// Rank 是近 24 小时的热度名次（1 起）；近 24 小时无调用为 null。只有名次，没有数量。
	Rank    *int                `json:"rank"`
	Samples []PlazaHealthSample `json:"samples"`
}

// PlazaHealthSample 是一个有调用的小时：t = 小时起点（Unix 秒），e = 该小时失败占比（0~1）。
type PlazaHealthSample struct {
	T int64   `json:"t"`
	E float64 `json:"e"`
}

type plazaHealthCacheEntry struct {
	value     *PlazaHealth
	expiresAt time.Time
}

// SetHealthStore 注入健康数据来源；未注入时 Health 返回 available=false。仅在装配阶段调用。
func (s *ModelPlazaService) SetHealthStore(store PlazaHealthStore) {
	if s == nil || store == nil {
		return
	}
	s.plazaExt().healthStore = store
}

// Health 返回 allowedGroupIDs 范围内各线路的健康摘要与热度名次。
// allowedGroupIDs 必须是调用者可用分组的 ID；为空时返回空线路。
func (s *ModelPlazaService) Health(ctx context.Context, allowedGroupIDs []int64) (*PlazaHealth, error) {
	if s == nil || s.ext == nil || s.ext.healthStore == nil {
		return &PlazaHealth{Available: false, Lines: []PlazaHealthLine{}}, nil
	}
	ext := s.ext
	now := time.Now()
	if ext.now != nil {
		now = ext.now()
	}
	// 窗口含当前未走完的这个小时。
	end := now.UTC().Truncate(plazaHealthHour).Add(plazaHealthHour)
	start := end.Add(-plazaHealthLookback)

	ids := normalizeInt64Set(allowedGroupIDs)
	if len(ids) == 0 {
		return &PlazaHealth{Available: true, Lines: []PlazaHealthLine{}, Ranks: map[string]int{}}, nil
	}
	// 先取上架线路：结果缓存按它的指纹区分，清单一变（模型下架等）旧结果立即失效。
	listed, err := s.plazaListedLines(ctx, now)
	if err != nil {
		return nil, err
	}
	key := plazaHealthCacheKey(ids, end, listed)
	if cached := ext.cachedHealth(key, now); cached != nil {
		return cached, nil
	}
	v, err, _ := ext.healthFlight.Do(key, func() (any, error) {
		data, err := ext.healthStore.PlazaHourlyHealth(ctx, ids, start, end)
		if err != nil {
			return nil, err
		}
		if plazaHealthStale(data, now) {
			// 不缓存：聚合恢复后下一次请求即可恢复展示。
			return &PlazaHealth{Available: false, Lines: []PlazaHealthLine{}}, nil
		}
		lines, ranks := buildPlazaHealthLines(plazaHealthListedRows(data.Rows, listed), end.Add(-plazaHealthRankWindow))
		result := &PlazaHealth{Available: true, Lines: lines, Ranks: ranks}
		ext.storeHealth(key, result, now)
		return result, nil
	})
	if err != nil {
		return nil, err
	}
	return v.(*PlazaHealth), nil
}

// plazaHealthStale 判断聚合数据是否已过期：尚无聚合数据，或数据截止时间落后当前时间超过 plazaHealthMaxLag。
func plazaHealthStale(data *PlazaHourlyHealth, now time.Time) bool {
	if data == nil || data.DataThrough.IsZero() {
		return true
	}
	return now.UTC().Sub(data.DataThrough.UTC()) > plazaHealthMaxLag
}

// plazaHealthCacheKey 由小时边界、分组集合和这些分组内上架线路的指纹组成。
func plazaHealthCacheKey(sortedIDs []int64, end time.Time, listed map[plazaHealthLineKey]struct{}) string {
	var b strings.Builder
	b.WriteString(strconv.FormatInt(end.Unix(), 10))
	scope := make(map[int64]struct{}, len(sortedIDs))
	for _, id := range sortedIDs {
		b.WriteByte(',')
		b.WriteString(strconv.FormatInt(id, 10))
		scope[id] = struct{}{}
	}
	lines := make([]string, 0, len(listed))
	for key := range listed {
		if _, ok := scope[key.groupID]; ok {
			lines = append(lines, key.platform+"\x00"+strconv.FormatInt(key.groupID, 10)+"\x00"+key.model)
		}
	}
	sort.Strings(lines)
	h := fnv.New64a()
	for _, line := range lines {
		_, _ = h.Write([]byte(line))
		_, _ = h.Write([]byte{0xff})
	}
	b.WriteByte('#')
	b.WriteString(strconv.FormatUint(h.Sum64(), 16))
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

type plazaHealthLineKey struct {
	platform string
	groupID  int64
	model    string
}

// plazaListedLines 返回广场清单里的全部线路（平台 + 分组 + 模型）。
// 聚合表的模型名取自客户端提交的 requested_model，可能是任何字符串（误填的名字、未上架的
// 内部部署名）；健康接口只允许返回清单内的线路，其余一律不出现在线路和名次里。
func (s *ModelPlazaService) plazaListedLines(ctx context.Context, now time.Time) (map[plazaHealthLineKey]struct{}, error) {
	ext := s.plazaExt()
	ext.listedMu.Lock()
	if ext.listedCache != nil && now.Before(ext.listedUntil) {
		cached := ext.listedCache
		ext.listedMu.Unlock()
		return cached, nil
	}
	ext.listedMu.Unlock()

	listed, err := s.loadPlazaListedLines(ctx)
	if err != nil {
		return nil, err
	}
	ext.listedMu.Lock()
	ext.listedCache, ext.listedUntil = listed, now.Add(plazaListedLinesTTL)
	ext.listedMu.Unlock()
	return listed, nil
}

func (s *ModelPlazaService) loadPlazaListedLines(ctx context.Context) (map[plazaHealthLineKey]struct{}, error) {
	if s.ext != nil && s.ext.listedLines != nil {
		return s.ext.listedLines(ctx)
	}
	groups, err := s.ListGroups(ctx)
	if err != nil {
		return nil, err
	}
	listed := make(map[plazaHealthLineKey]struct{})
	for i := range groups {
		for j := range groups[i].Models {
			m := &groups[i].Models[j]
			listed[plazaHealthLineKey{m.Platform, groups[i].ID, m.Name}] = struct{}{}
		}
	}
	return listed, nil
}

// plazaHealthListedRows 只保留广场清单内线路的行。
func plazaHealthListedRows(rows []PlazaHourlyRow, listed map[plazaHealthLineKey]struct{}) []PlazaHourlyRow {
	out := make([]PlazaHourlyRow, 0, len(rows))
	for i := range rows {
		if _, ok := listed[plazaHealthLineKey{rows[i].Platform, rows[i].GroupID, rows[i].Model}]; ok {
			out = append(out, rows[i])
		}
	}
	return out
}

// buildPlazaHealthLines 把小时计数转成广场的白名单线路列表和按模型名的热度名次。
// 名次按精确模型名跨平台、跨分组汇总 rankSince 之后的请求数排出；请求数本身不外传。
func buildPlazaHealthLines(rows []PlazaHourlyRow, rankSince time.Time) ([]PlazaHealthLine, map[string]int) {
	order := []plazaHealthLineKey{}
	grouped := make(map[plazaHealthLineKey][]PlazaHourlyRow)
	recent := make(map[string]int64)
	for i := range rows {
		row := rows[i]
		if !plazaHealthRowUsable(&row) {
			continue
		}
		key := plazaHealthLineKey{row.Platform, row.GroupID, row.Model}
		// 名次按原始调用数排（含被忽略类错误），与「最多人用」的含义一致。
		if !row.Hour.Before(rankSince) {
			recent[row.Model] += row.Success + row.Failed + row.Ignored
		}
		// 采样只收有可计调用的小时：只有被忽略类错误的小时没有可定义的成功率。
		if row.Success+row.Failed <= 0 {
			continue
		}
		if _, ok := grouped[key]; !ok {
			order = append(order, key)
		}
		grouped[key] = append(grouped[key], row)
	}
	ranked := make([]string, 0, len(recent))
	for model := range recent {
		ranked = append(ranked, model)
	}
	sort.Slice(ranked, func(i, j int) bool {
		if recent[ranked[i]] != recent[ranked[j]] {
			return recent[ranked[i]] > recent[ranked[j]]
		}
		return ranked[i] < ranked[j]
	})
	ranks := make(map[string]int, len(ranked))
	for i, model := range ranked {
		ranks[model] = i + 1
	}

	lines := make([]PlazaHealthLine, 0, len(order))
	for _, key := range order {
		hours := grouped[key]
		sort.Slice(hours, func(i, j int) bool { return hours[i].Hour.Before(hours[j].Hour) })
		if len(hours) > plazaHealthSampleLimit {
			hours = hours[len(hours)-plazaHealthSampleLimit:]
		}
		var success, failed, ttftSum, ttftCount int64
		samples := make([]PlazaHealthSample, 0, len(hours))
		for i := range hours {
			h := &hours[i]
			success += h.Success
			failed += h.Failed
			ttftSum += h.TTFTSumMs
			ttftCount += h.TTFTCount
			samples = append(samples, PlazaHealthSample{T: h.Hour.Unix(), E: plazaHealthRatio(h.Failed, h.Success+h.Failed)})
		}
		line := PlazaHealthLine{
			Platform:  key.platform,
			GroupID:   key.groupID,
			Model:     key.model,
			State:     plazaHealthState(success, failed),
			ErrorRate: plazaHealthRatio(failed, success+failed),
			Samples:   samples,
		}
		if ttftCount > 0 {
			avg := ttftSum / ttftCount
			line.TTFTMs = &avg
		}
		if rank, ok := ranks[key.model]; ok {
			r := rank
			line.Rank = &r
		}
		lines = append(lines, line)
	}
	return lines, ranks
}

func plazaHealthRowUsable(row *PlazaHourlyRow) bool {
	if row == nil || row.GroupID <= 0 || row.Success < 0 || row.Failed < 0 || row.Ignored < 0 || row.Success+row.Failed+row.Ignored <= 0 {
		return false
	}
	return row.Model != "" && row.Model != "unknown" && row.Model != ChannelMonitorV2OtherModel
}

// plazaHealthRatio 返回 part/total，保留四位小数并向上取整（整数运算）。
// 向上取整保证对外的失败占比不会低于真实值：89.997% 的成功率不会显示成 90.00%，
// 前端按这个值分档时也不会把未达阈值的小时画成更好的一档。
func plazaHealthRatio(part, total int64) float64 {
	if total <= 0 || part <= 0 {
		return 0
	}
	if part >= total {
		return 1
	}
	return float64((part*10000+total-1)/total) / 10000
}

// plazaHealthState 按成功率分档，用整数比较避免浮点边界误差。
func plazaHealthState(success, failed int64) string {
	total := success + failed
	switch {
	case success*100 >= total*90:
		return plazaHealthHealthy
	case success*100 >= total*70:
		return plazaHealthWarning
	default:
		return plazaHealthCritical
	}
}
