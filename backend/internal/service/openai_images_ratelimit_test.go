package service

import (
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// 真实上游 429 报文（用户实测）：组织级 input-images 每分钟限流，message 里带 "Please try again in 15ms"。
const orgImageRateLimitBody = `{"error":{"message":"Rate limit reached for gpt-image-2-codex (for limit gpt-image) in organization org-BOvpEHVcDPTe8h4lZnwMO5Ly on input-images per min: Limit 4000, Used 4000, Requested 1. Please try again in 15ms. Visit https://platform.openai.com/account/rate-limits to learn more","type":"rate_limit_exceeded","code":"rate_limit_exceeded"}}`

// 账号级 5h/周配额限流（usage_limit_reached），不带 organization / per min。
const accountUsageLimitBody = `{"error":{"message":"You have hit your usage limit. Please try again later.","type":"usage_limit_reached","resets_in_seconds":133107}}`

func TestParseRateLimitRetryAfter_FreeTextTryAgain(t *testing.T) {
	cases := []struct {
		name string
		body string
		want time.Duration
	}{
		{"milliseconds", orgImageRateLimitBody, 15 * time.Millisecond},
		{"fractional seconds", `{"error":{"message":"slow down. Please try again in 1.5s."}}`, 1500 * time.Millisecond},
		{"whole seconds word", `{"error":{"message":"Rate limited, try again in 3 seconds"}}`, 3 * time.Second},
		{"minutes", `{"error":{"message":"try again in 2m"}}`, 2 * time.Minute},
		{"plain seconds unit s", `{"error":{"message":"please try again in 750ms"}}`, 750 * time.Millisecond},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, ok := parseRateLimitRetryAfter(nil, []byte(tc.body))
			assert.True(t, ok, "expected a parsed delay")
			assert.Equal(t, tc.want, d)
		})
	}
}

func TestParseRateLimitRetryAfter_Header(t *testing.T) {
	t.Run("integer seconds", func(t *testing.T) {
		h := http.Header{"Retry-After": []string{"2"}}
		d, ok := parseRateLimitRetryAfter(h, nil)
		assert.True(t, ok)
		assert.Equal(t, 2*time.Second, d)
	})
	t.Run("fractional seconds", func(t *testing.T) {
		h := http.Header{"Retry-After": []string{"0.5"}}
		d, ok := parseRateLimitRetryAfter(h, nil)
		assert.True(t, ok)
		assert.Equal(t, 500*time.Millisecond, d)
	})
	t.Run("header takes precedence over body", func(t *testing.T) {
		h := http.Header{"Retry-After": []string{"1"}}
		d, ok := parseRateLimitRetryAfter(h, []byte(orgImageRateLimitBody))
		assert.True(t, ok)
		assert.Equal(t, 1*time.Second, d)
	})
}

func TestParseRateLimitRetryAfter_StaleHTTPDateFallsThroughToBody(t *testing.T) {
	// 已过期的 HTTP-date 不应短路，应回退解析 body 的 "try again in 15ms"。
	h := http.Header{"Retry-After": []string{"Mon, 02 Jan 2006 15:04:05 GMT"}}
	d, ok := parseRateLimitRetryAfter(h, []byte(orgImageRateLimitBody))
	assert.True(t, ok)
	assert.Equal(t, 15*time.Millisecond, d)

	// 过期 HTTP-date 且 body 无信号 -> 无法解析。
	d, ok = parseRateLimitRetryAfter(h, []byte(`{"error":{"message":"no retry hint"}}`))
	assert.False(t, ok)
	assert.Equal(t, time.Duration(0), d)
}

func TestParseRateLimitRetryAfter_NoSignal(t *testing.T) {
	d, ok := parseRateLimitRetryAfter(nil, []byte(`{"error":{"message":"You have hit your usage limit."}}`))
	assert.False(t, ok)
	assert.Equal(t, time.Duration(0), d)
}

func TestIsOrgScopedRateLimit(t *testing.T) {
	assert.True(t, isOrgScopedRateLimit([]byte(orgImageRateLimitBody)), "org-level per-min limit should be org-scoped")
	assert.False(t, isOrgScopedRateLimit([]byte(accountUsageLimitBody)), "account usage limit is not org-scoped")
	assert.False(t, isOrgScopedRateLimit(nil))
	assert.False(t, isOrgScopedRateLimit([]byte(`{"error":{"message":"invalid api key"}}`)))
}

func TestExtractRateLimitOrgID(t *testing.T) {
	assert.Equal(t, "org-BOvpEHVcDPTe8h4lZnwMO5Ly", extractRateLimitOrgID([]byte(orgImageRateLimitBody)))
	assert.Equal(t, "", extractRateLimitOrgID([]byte(accountUsageLimitBody)))
	assert.Equal(t, "", extractRateLimitOrgID(nil))
}
