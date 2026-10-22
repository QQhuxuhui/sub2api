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

// openAICacheSimWindowStore 用 Redis 衰减计数器实现「账号最近一段时间的展示缓存率」窗口：
// 每个账号一个 hash {t: 总输入, c: 缓存读, ts: 上次更新毫秒}，读写时按半衰期把旧值
// 衰减到当前时刻再累加。Lua 原子更新，多实例共享。
type openAICacheSimWindowStore struct {
	rdb *redis.Client
}

// NewOpenAICacheSimWindowStore 构造 Redis 实现。
func NewOpenAICacheSimWindowStore(rdb *redis.Client) service.OpenAICacheSimWindowStore {
	return &openAICacheSimWindowStore{rdb: rdb}
}

const openAICacheSimWindowTTL = 24 * time.Hour

func openAICacheSimWindowKey(accountID int64) string {
	return "openai:cachesim:win:" + strconv.FormatInt(accountID, 10)
}

// 衰减并累加：KEYS[1]=key, ARGV[1]=now_ms, ARGV[2]=half_life_ms, ARGV[3]=add_total, ARGV[4]=add_cached, ARGV[5]=ttl_sec
var openAICacheSimAddScript = redis.NewScript(`
local now = tonumber(ARGV[1])
local half = tonumber(ARGV[2])
local addT = tonumber(ARGV[3])
local addC = tonumber(ARGV[4])
local vals = redis.call('HMGET', KEYS[1], 't', 'c', 'ts')
local t = tonumber(vals[1]) or 0
local c = tonumber(vals[2]) or 0
local ts = tonumber(vals[3]) or now
if half > 0 and now > ts then
  local f = math.pow(0.5, (now - ts) / half)
  t = t * f
  c = c * f
end
t = t + addT
c = c + addC
redis.call('HSET', KEYS[1], 't', tostring(t), 'c', tostring(c), 'ts', tostring(now))
redis.call('EXPIRE', KEYS[1], tonumber(ARGV[5]))
return {tostring(t), tostring(c)}
`)

func (s *openAICacheSimWindowStore) Add(ctx context.Context, accountID int64, total, cached int, halfLife time.Duration) error {
	if s == nil || s.rdb == nil {
		return nil
	}
	now := time.Now().UnixMilli()
	_, err := openAICacheSimAddScript.Run(ctx, s.rdb, []string{openAICacheSimWindowKey(accountID)},
		now, halfLife.Milliseconds(), total, cached, int64(openAICacheSimWindowTTL.Seconds())).Result()
	if err != nil {
		return fmt.Errorf("openai cache sim window add: %w", err)
	}
	return nil
}

func (s *openAICacheSimWindowStore) Peek(ctx context.Context, accountID int64, halfLife time.Duration) (service.OpenAICacheSimWindow, error) {
	var win service.OpenAICacheSimWindow
	if s == nil || s.rdb == nil {
		return win, nil
	}
	vals, err := s.rdb.HMGet(ctx, openAICacheSimWindowKey(accountID), "t", "c", "ts").Result()
	if err != nil {
		return win, fmt.Errorf("openai cache sim window peek: %w", err)
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
	win.Total = t
	win.Cached = c
	if ts > 0 {
		win.UpdatedAt = time.UnixMilli(ts)
	}
	return win, nil
}
