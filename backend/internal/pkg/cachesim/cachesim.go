// Package cachesim 承载「OpenAI 缓存模拟」的每请求状态：响应改写层在第一次看到
// 上游 usage 时做出一次决策（把 cached_tokens 抬到多少），计费落库沿用同一份决策，
// 两边永远一致。状态挂在 request context 上，由中间件创建、usageRecordContext 拷贝到
// 用量落库任务里。
package cachesim

import (
	"context"
	"sync"
)

type ctxKey struct{}

// State 是一次请求的模拟决策。决策只做一次：以首次看到的上游原始 (total, cached) 为准。
// total 是 OpenAI 口径的总输入（含缓存读），cached 是缓存读。
type State struct {
	mu        sync.Mutex
	decided   bool
	excluded  bool
	rawTotal  int
	rawCached int
	simCached int
}

// New 返回空状态。
func New() *State { return &State{} }

// WithState 把状态挂到 context 上。
func WithState(ctx context.Context, s *State) context.Context {
	if s == nil {
		return ctx
	}
	return context.WithValue(ctx, ctxKey{}, s)
}

// FromContext 取出状态，没有时返回 nil。
func FromContext(ctx context.Context) *State {
	if ctx == nil {
		return nil
	}
	s, _ := ctx.Value(ctxKey{}).(*State)
	return s
}

// Decide 用首次看到的原始值做一次决策；之后再调用直接返回已定的结果。
// decide 回调只在第一次被调用，返回模拟后的 cached（不得小于 rawCached）。
func (s *State) Decide(rawTotal, rawCached int, decide func(rawTotal, rawCached int) int) int {
	if s == nil {
		return rawCached
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.excluded {
		return rawCached
	}
	if s.decided {
		return s.simCached
	}
	sim := rawCached
	if decide != nil {
		sim = decide(rawTotal, rawCached)
	}
	if sim < rawCached {
		sim = rawCached
	}
	if sim > rawTotal && rawTotal > 0 {
		sim = rawTotal
	}
	s.decided = true
	s.rawTotal = rawTotal
	s.rawCached = rawCached
	s.simCached = sim
	return sim
}

// Decided 报告是否已做过决策。
func (s *State) Decided() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decided
}

// Snapshot 返回决策内容。
func (s *State) Snapshot() (rawTotal, rawCached, simCached int, decided bool) {
	if s == nil {
		return 0, 0, 0, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rawTotal, s.rawCached, s.simCached, s.decided
}

// Apply 把一个 usage 对象里的 (total, cached) 映射成决策后的 cached，幂等：
//   - 与原始值一致 → 返回模拟值（changed=true 当且仅当模拟值不同）
//   - 已是模拟值 → 原样返回（已经改过）
//   - 其他（不同的 usage 对象）→ 原样返回
func (s *State) Apply(total, cached int) (int, bool) {
	if s == nil {
		return cached, false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.excluded || !s.decided || total != s.rawTotal {
		return cached, false
	}
	if cached == s.rawCached {
		return s.simCached, s.simCached != cached
	}
	return cached, false
}

// IsApplied 报告 (total, cached) 是否已是模拟后的值（用于落库时还原原始值）。
func (s *State) IsApplied(total, cached int) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.decided && total == s.rawTotal && cached == s.simCached && s.simCached != s.rawCached
}

// Exclude prevents simulation after non-text usage is observed, including after
// protocol conversion drops the upstream modality fields.
func (s *State) Exclude() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.excluded = true
}

func (s *State) Excluded() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.excluded
}
