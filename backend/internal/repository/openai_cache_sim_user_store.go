package repository

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/redis/go-redis/v9"
)

// openAICacheSimUserStore keeps the by-user cache simulation's windows in
// Redis: a decaying window per user {t, c, ts} and the current Asia/Shanghai
// hour {t, c}. Deciding and counting happen in one Lua call so concurrent
// requests of a user never fill the same gap twice.
type openAICacheSimUserStore struct {
	rdb *redis.Client
}

// NewOpenAICacheSimUserStore builds the Redis implementation.
func NewOpenAICacheSimUserStore(rdb *redis.Client) service.OpenAICacheSimUserStore {
	return &openAICacheSimUserStore{rdb: rdb}
}

const (
	openAICacheSimUserWindowTTL = 24 * time.Hour
	openAICacheSimUserHourTTL   = 2 * time.Hour
)

func openAICacheSimUserWindowKey(userID int64) string {
	return "openai:cachesim:uwin:" + strconv.FormatInt(userID, 10)
}

func openAICacheSimUserHourKey(userID int64, hour string) string {
	return "openai:cachesim:uhour:" + strconv.FormatInt(userID, 10) + ":" + hour
}

// KEYS[1]=window, KEYS[2]=hour.
// ARGV: 1 now_ms, 2 half_ms, 3 total, 4 cached, 5 ceiling, 6 lo_bp, 7 hi_bp,
// 8 target_bp, 9 window_ttl_s, 10 hour_ttl_s, 11 mode (decide|count|hour).
// Returns the shown cache reads. The decision mirrors
// service.openAICacheSimUserDecide (basis points, multiply before dividing).
var openAICacheSimUserScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local half = tonumber(ARGV[2])
local total = tonumber(ARGV[3])
local cached = tonumber(ARGV[4])
local ceiling = tonumber(ARGV[5])
local lo = tonumber(ARGV[6])
local hi = tonumber(ARGV[7])
local target = tonumber(ARGV[8])
local mode = ARGV[11]

local ht = tonumber(redis.call('HGET', KEYS[2], 't') or '0') or 0
local hc = tonumber(redis.call('HGET', KEYS[2], 'c') or '0') or 0
local wt, wc = 0, 0
if mode ~= 'hour' then
  local vals = redis.call('HMGET', KEYS[1], 't', 'c', 'ts')
  wt = tonumber(vals[1]) or 0
  wc = tonumber(vals[2]) or 0
  local ts = tonumber(vals[3]) or now
  if half > 0 and now > ts then
    local f = math.pow(0.5, (now - ts) / half)
    wt = wt * f
    wc = wc * f
  end
end

local sim = cached
if mode == 'decide' and ceiling > cached and total > 0 then
  if ht > 0 and hc * 10000 < lo * ht then
    target = hi
  end
  local wantW = (target * (wt + total) - 10000 * wc) / 10000
  local wantH = (target * (ht + total) - 10000 * hc) / 10000
  local want = math.max(wantW, wantH)
  local room = math.floor((hi * (ht + total) - 10000 * hc) / 10000)
  local upper = math.min(room, ceiling)
  if want >= 1 then
    local cand = math.min(math.floor(want), upper)
    cand = math.floor(cand / 128) * 128
    if cand > sim then sim = cand end
  end
  if (hc + sim) * 10000 < lo * (ht + total) then
    local need = math.ceil((lo * (ht + total) - 10000 * hc) / 10000)
    need = math.ceil(need / 128) * 128
    if need <= upper and need > sim then sim = need end
  end
  if sim < cached then sim = cached end
  if sim > total then sim = total end
end

if mode ~= 'hour' then
  redis.call('HSET', KEYS[1], 't', tostring(wt + total), 'c', tostring(wc + sim), 'ts', tostring(now))
  redis.call('EXPIRE', KEYS[1], tonumber(ARGV[9]))
end
redis.call('HSET', KEYS[2], 't', tostring(ht + total), 'c', tostring(hc + sim))
redis.call('EXPIRE', KEYS[2], tonumber(ARGV[10]))
return tostring(sim)
`)

func (s *openAICacheSimUserStore) run(ctx context.Context, userID int64, hour string, halfLife time.Duration, total, cached, ceiling int, lo, hi, target int64, mode string) (int, error) {
	if s == nil || s.rdb == nil {
		return cached, nil
	}
	res, err := openAICacheSimUserScript.Run(ctx, s.rdb,
		[]string{openAICacheSimUserWindowKey(userID), openAICacheSimUserHourKey(userID, hour)},
		time.Now().UnixMilli(), halfLife.Milliseconds(), total, cached, ceiling, lo, hi, target,
		int64(openAICacheSimUserWindowTTL.Seconds()), int64(openAICacheSimUserHourTTL.Seconds()), mode,
	).Result()
	if err != nil {
		return cached, fmt.Errorf("openai cache sim user %s: %w", mode, err)
	}
	v, err := strconv.ParseFloat(fmt.Sprint(res), 64)
	if err != nil {
		return cached, fmt.Errorf("openai cache sim user %s: parse %v: %w", mode, res, err)
	}
	return int(math.Round(v)), nil
}

func (s *openAICacheSimUserStore) DecideAndCount(ctx context.Context, in service.OpenAICacheSimUserDecideInput) (int, error) {
	return s.run(ctx, in.UserID, in.Hour, in.HalfLife, in.Total, in.Cached, in.Ceiling, in.LoBP, in.HiBP, in.TargetBP, "decide")
}

func (s *openAICacheSimUserStore) Count(ctx context.Context, userID int64, hour string, halfLife time.Duration, total, cached int, hourOnly bool) error {
	mode := "count"
	if hourOnly {
		mode = "hour"
	}
	_, err := s.run(ctx, userID, hour, halfLife, total, cached, cached, 0, 0, 0, mode)
	return err
}

func (s *openAICacheSimUserStore) PeekUser(ctx context.Context, userID int64, halfLife time.Duration) (service.OpenAICacheSimWindow, error) {
	var win service.OpenAICacheSimWindow
	if s == nil || s.rdb == nil {
		return win, nil
	}
	vals, err := s.rdb.HMGet(ctx, openAICacheSimUserWindowKey(userID), "t", "c", "ts").Result()
	if err != nil {
		return win, fmt.Errorf("openai cache sim user peek: %w", err)
	}
	if len(vals) != 3 || vals[0] == nil {
		return win, nil
	}
	t, _ := strconv.ParseFloat(fmt.Sprint(vals[0]), 64)
	c, _ := strconv.ParseFloat(fmt.Sprint(vals[1]), 64)
	ts, _ := strconv.ParseInt(fmt.Sprint(vals[2]), 10, 64)
	now := time.Now().UnixMilli()
	if halfLife > 0 && now > ts && ts > 0 {
		f := math.Pow(0.5, float64(now-ts)/float64(halfLife.Milliseconds()))
		t *= f
		c *= f
	}
	win.Total, win.Cached = t, c
	if ts > 0 {
		win.UpdatedAt = time.UnixMilli(ts)
	}
	return win, nil
}
