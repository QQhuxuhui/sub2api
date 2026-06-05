package service

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// OpenAI 生图 429 限流的原地重试 / 组织级故障转移辅助。
//
// 背景：OpenAI 的 gpt-image（gpt-image-*，OAuth/Codex 走 chatgpt.com/backend-api/codex/responses）
// 经常返回组织级每分钟限流，例如：
//
//	Rate limit reached for gpt-image-2-codex (for limit gpt-image) in organization
//	org-XXXX on input-images per min: Limit 4000, Used 4000, Requested 1. Please try again in 15ms.
//
// 上游通常会给出一个非常短的重置窗口（毫秒级）。现有逻辑既不解析这个窗口、也不原地重试，
// 而是直接冷却账号并故障转移——对同一 org 下的其它账号转移是无效的（共享同一配额）。
// 这里提供：
//  1. parseRateLimitRetryAfter：从 Retry-After 头或报文 "try again in Xms/s/m" 文本解析重置时长；
//  2. isOrgScopedRateLimit / extractRateLimitOrgID：识别组织级限流并提取 org 标识，用于按 org 冷却。

var (
	// 形如 "try again in 15ms" / "try again in 1.5s" / "try again in 2 minutes"。
	tryAgainInRe = regexp.MustCompile(`(?i)try again in\s+(\d+(?:\.\d+)?)\s*([a-z]+)`)
	// 组织级限流的速率短语，例如 "per min" / "per minute" / "per day"。
	orgRateLimitPerUnitRe = regexp.MustCompile(`per\s+(ms|millisecond|msec|sec|second|secs|min|minute|mins|hour|hr|day)s?\b`)
	// OpenAI 组织标识，例如 org-BOvpEHVcDPTe8h4lZnwMO5Ly。
	orgIDRe = regexp.MustCompile(`org-[A-Za-z0-9_-]+`)
)

// parseRateLimitRetryAfter 从 429 响应中解析建议的重试等待时长。
// 优先级：Retry-After 头（整数/小数秒，或 HTTP-date）> 报文中的 "try again in X<unit>" 文本。
// 返回 (时长, true) 表示成功解析；否则 (0, false)。
func parseRateLimitRetryAfter(headers http.Header, body []byte) (time.Duration, bool) {
	if headers != nil {
		if ra := strings.TrimSpace(headers.Get("Retry-After")); ra != "" {
			if secs, err := strconv.ParseFloat(ra, 64); err == nil && secs >= 0 {
				return time.Duration(secs * float64(time.Second)), true
			}
			if t, err := http.ParseTime(ra); err == nil {
				if d := time.Until(t); d > 0 {
					return d, true
				}
				// 已过期的 HTTP-date：不短路，继续尝试解析报文里的 "try again in X" 文本。
			}
		}
	}
	if d, ok := parseTryAgainDuration(body); ok {
		return d, true
	}
	return 0, false
}

// parseTryAgainDuration 解析报文 message 里的 "try again in X<unit>" 文本。
func parseTryAgainDuration(body []byte) (time.Duration, bool) {
	if len(body) == 0 {
		return 0, false
	}
	msg := extractUpstreamErrorMessage(body)
	if strings.TrimSpace(msg) == "" {
		msg = string(body)
	}
	m := tryAgainInRe.FindStringSubmatch(msg)
	if len(m) != 3 {
		return 0, false
	}
	value, err := strconv.ParseFloat(m[1], 64)
	if err != nil || value < 0 {
		return 0, false
	}
	unit, ok := rateLimitUnitDuration(m[2])
	if !ok {
		return 0, false
	}
	return time.Duration(value * float64(unit)), true
}

// rateLimitUnitDuration 把单位词归一化成 time.Duration 基准。
func rateLimitUnitDuration(unit string) (time.Duration, bool) {
	switch strings.ToLower(unit) {
	case "ms", "msec", "msecs", "millisecond", "milliseconds":
		return time.Millisecond, true
	case "s", "sec", "secs", "second", "seconds":
		return time.Second, true
	case "m", "min", "mins", "minute", "minutes":
		return time.Minute, true
	case "h", "hr", "hrs", "hour", "hours":
		return time.Hour, true
	default:
		return 0, false
	}
}

// isOrgScopedRateLimit 判断 429 是否为组织级限流（vs 账号级配额）。
// 组织级限流对同一 org 下所有账号生效，因此故障转移到同 org 账号无意义。
func isOrgScopedRateLimit(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	msg := strings.ToLower(extractUpstreamErrorMessage(body))
	if strings.TrimSpace(msg) == "" {
		msg = strings.ToLower(string(body))
	}
	if !strings.Contains(msg, "organization") {
		return false
	}
	return orgRateLimitPerUnitRe.MatchString(msg)
}

// extractRateLimitOrgID 从限流报文中提取 OpenAI 组织标识（org-XXXX），提取不到返回空串。
func extractRateLimitOrgID(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	return orgIDRe.FindString(string(body))
}

const (
	// 组织级生图限流冷却的兜底时长与上限。reset 文本通常是毫秒级，但解析不到时给一个保守兜底，
	// 并对解析到的超长 reset 做封顶，避免长时间错误屏蔽整个 org。
	openAIOrgImageRateLimitFallbackCooldown = 5 * time.Second
	openAIOrgImageRateLimitMaxCooldown      = 60 * time.Second

	// 生图 429 原地重试参数。
	openAIImageRateLimitMaxRetries = 3
	openAIImageRateLimitMaxWait    = 2 * time.Second
	openAIImageRateLimitMinDelay   = 50 * time.Millisecond
)

// blockOrgImageScheduling 为某个组织设置一个生图调度冷却窗口（保留较晚的到期时间）。
func (s *OpenAIGatewayService) blockOrgImageScheduling(org string, until time.Time) {
	if s == nil || strings.TrimSpace(org) == "" {
		return
	}
	now := time.Now()
	blockUntil := until
	if blockUntil.IsZero() || !blockUntil.After(now) {
		blockUntil = now.Add(openAIOrgImageRateLimitFallbackCooldown)
	}
	if maxUntil := now.Add(openAIOrgImageRateLimitMaxCooldown); blockUntil.After(maxUntil) {
		blockUntil = maxUntil
	}

	for {
		current, ok := s.openaiOrgImageBlockUntil.Load(org)
		if !ok {
			actual, loaded := s.openaiOrgImageBlockUntil.LoadOrStore(org, blockUntil)
			if !loaded {
				return
			}
			current = actual
		}
		currentUntil, ok := current.(time.Time)
		if !ok || currentUntil.IsZero() {
			if s.openaiOrgImageBlockUntil.CompareAndSwap(org, current, blockUntil) {
				return
			}
			continue
		}
		if currentUntil.After(blockUntil) {
			return
		}
		if s.openaiOrgImageBlockUntil.CompareAndSwap(org, current, blockUntil) {
			return
		}
	}
}

// isOrgImageScheduleBlocked 判断某组织当前是否处于生图调度冷却中（顺带清理已过期项）。
func (s *OpenAIGatewayService) isOrgImageScheduleBlocked(org string) bool {
	if s == nil || strings.TrimSpace(org) == "" {
		return false
	}
	value, ok := s.openaiOrgImageBlockUntil.Load(org)
	if !ok {
		return false
	}
	until, ok := value.(time.Time)
	if !ok || until.IsZero() {
		s.openaiOrgImageBlockUntil.Delete(org)
		return false
	}
	if time.Now().Before(until) {
		return true
	}
	s.openaiOrgImageBlockUntil.Delete(org)
	return false
}

// recordImageOrgRateLimit 在生图请求遭遇「组织级」429 限流时，按 org 设置调度冷却窗口，
// 使调度器在窗口内跳过同一 org 的账号——对组织级配额而言，转移到同 org 账号是无效的。
func (s *OpenAIGatewayService) recordImageOrgRateLimit(account *Account, headers http.Header, body []byte) {
	if s == nil || !isOrgScopedRateLimit(body) {
		return
	}
	org := ""
	if account != nil {
		org = account.GetOpenAIOrganizationID()
	}
	if strings.TrimSpace(org) == "" {
		org = extractRateLimitOrgID(body)
	}
	if strings.TrimSpace(org) == "" {
		return
	}
	var until time.Time
	if d, ok := parseRateLimitRetryAfter(headers, body); ok && d > 0 {
		until = time.Now().Add(d)
	}
	s.blockOrgImageScheduling(org, until)
}

// chromeImpersonatingUpstream 是 HTTPUpstream 的可选扩展能力：用 Chrome 指纹（imroc/req）发请求。
// 生产实现 httpUpstreamService 满足该接口；测试 fake 通常不实现，此时自动回退到普通 Do。
type chromeImpersonatingUpstream interface {
	DoImpersonateChrome(req *http.Request, proxyURL string, accountID int64, accountConcurrency int) (*http.Response, error)
}

// sendImageUpstream 执行一次生图上游请求；useImpersonate=true 且底层支持时走 Chrome 指纹伪装，否则普通 Do。
func (s *OpenAIGatewayService) sendImageUpstream(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, useImpersonate bool) (*http.Response, error) {
	if useImpersonate {
		if imp, ok := s.httpUpstream.(chromeImpersonatingUpstream); ok {
			return imp.DoImpersonateChrome(req, proxyURL, accountID, accountConcurrency)
		}
	}
	return s.httpUpstream.Do(req, proxyURL, accountID, accountConcurrency)
}

// doImageUpstreamWithRateLimitRetry 执行生图上游请求，并在遇到「重置窗口很短」的 429 时原地重试。
// buildReq 每次必须返回一个全新、body 可重读的 *http.Request（生图请求体已是缓冲的 []byte，满足要求）。
// useImpersonate 为 true 时走 Chrome TLS/HTTP2 指纹伪装（仅 OAuth/Codex 生图路径需要）。
// 返回的 *http.Response 由调用方负责关闭 Body。
func (s *OpenAIGatewayService) doImageUpstreamWithRateLimitRetry(
	ctx context.Context,
	buildReq func() (*http.Request, error),
	proxyURL string,
	accountID int64,
	accountConcurrency int,
	useImpersonate bool,
) (*http.Response, error) {
	for attempt := 0; ; attempt++ {
		req, err := buildReq()
		if err != nil {
			return nil, err
		}
		resp, err := s.sendImageUpstream(req, proxyURL, accountID, accountConcurrency, useImpersonate)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode != http.StatusTooManyRequests || attempt >= openAIImageRateLimitMaxRetries {
			return resp, nil
		}
		peek, _ := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		delay, ok := parseRateLimitRetryAfter(resp.Header, peek)
		if !ok || delay > openAIImageRateLimitMaxWait {
			// 非短窗口限流（或解析不到）：恢复 body，交回上层走正常失败/故障转移逻辑。
			resp.Body = io.NopCloser(bytes.NewReader(peek))
			return resp, nil
		}
		if delay < openAIImageRateLimitMinDelay {
			delay = openAIImageRateLimitMinDelay
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			resp.Body = io.NopCloser(bytes.NewReader(peek))
			return resp, nil
		case <-timer.C:
		}
	}
}
