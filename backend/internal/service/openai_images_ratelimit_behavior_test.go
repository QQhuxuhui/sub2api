package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// scriptedUpstream 是一个按序返回预设响应的 HTTPUpstream 假实现。
type scriptedUpstream struct {
	responses []*http.Response
	calls     int
}

func (f *scriptedUpstream) Do(_ *http.Request, _ string, _ int64, _ int) (*http.Response, error) {
	i := f.calls
	f.calls++
	if i < len(f.responses) {
		return f.responses[i], nil
	}
	return f.responses[len(f.responses)-1], nil
}

func (f *scriptedUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, accountConcurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return f.Do(req, proxyURL, accountID, accountConcurrency)
}

func newResp(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func buildTestImageReq() func() (*http.Request, error) {
	return func() (*http.Request, error) {
		return httptest.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", strings.NewReader("{}")), nil
	}
}

func TestDoImageUpstreamWithRateLimitRetry_RetriesShort429ThenSucceeds(t *testing.T) {
	fake := &scriptedUpstream{responses: []*http.Response{
		newResp(http.StatusTooManyRequests, orgImageRateLimitBody), // "try again in 15ms"
		newResp(http.StatusOK, `{"ok":true}`),
	}}
	s := &OpenAIGatewayService{httpUpstream: fake}

	resp, err := s.doImageUpstreamWithRateLimitRetry(context.Background(), buildTestImageReq(), "", 1, 1, false)
	require.NoError(t, err)
	require.NotNil(t, resp)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 2, fake.calls, "should retry once then succeed")
}

func TestDoImageUpstreamWithRateLimitRetry_NoRetryOnLongReset(t *testing.T) {
	longBody := `{"error":{"message":"Rate limited. Please try again in 30s."}}`
	fake := &scriptedUpstream{responses: []*http.Response{
		newResp(http.StatusTooManyRequests, longBody),
		newResp(http.StatusOK, `{"ok":true}`),
	}}
	s := &OpenAIGatewayService{httpUpstream: fake}

	resp, err := s.doImageUpstreamWithRateLimitRetry(context.Background(), buildTestImageReq(), "", 1, 1, false)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	assert.Equal(t, 1, fake.calls, "long reset should not be retried in place")
	// body 应被恢复，供上层读取
	body, _ := io.ReadAll(resp.Body)
	assert.Contains(t, string(body), "try again in 30s")
}

func TestDoImageUpstreamWithRateLimitRetry_StopsAtMaxRetries(t *testing.T) {
	resps := make([]*http.Response, 0, openAIImageRateLimitMaxRetries+2)
	for i := 0; i < openAIImageRateLimitMaxRetries+2; i++ {
		resps = append(resps, newResp(http.StatusTooManyRequests, orgImageRateLimitBody))
	}
	fake := &scriptedUpstream{responses: resps}
	s := &OpenAIGatewayService{httpUpstream: fake}

	resp, err := s.doImageUpstreamWithRateLimitRetry(context.Background(), buildTestImageReq(), "", 1, 1, false)
	require.NoError(t, err)
	assert.Equal(t, http.StatusTooManyRequests, resp.StatusCode)
	// attempt 0,1,2 retry；attempt 3 命中 attempt>=max 直接返回 => 共 4 次调用
	assert.Equal(t, openAIImageRateLimitMaxRetries+1, fake.calls)
}

func TestDoImageUpstreamWithRateLimitRetry_Non429PassesThrough(t *testing.T) {
	fake := &scriptedUpstream{responses: []*http.Response{
		newResp(http.StatusBadRequest, `{"error":{"message":"bad"}}`),
	}}
	s := &OpenAIGatewayService{httpUpstream: fake}

	resp, err := s.doImageUpstreamWithRateLimitRetry(context.Background(), buildTestImageReq(), "", 1, 1, false)
	require.NoError(t, err)
	assert.Equal(t, http.StatusBadRequest, resp.StatusCode)
	assert.Equal(t, 1, fake.calls)
}

func TestDoImageUpstreamWithRateLimitRetry_ImpersonateFallsBackWhenUnsupported(t *testing.T) {
	// useImpersonate=true，但 fake 未实现 chromeImpersonatingUpstream → 应回退到普通 Do，仍正常返回。
	fake := &scriptedUpstream{responses: []*http.Response{
		newResp(http.StatusOK, `{"ok":true}`),
	}}
	s := &OpenAIGatewayService{httpUpstream: fake}

	resp, err := s.doImageUpstreamWithRateLimitRetry(context.Background(), buildTestImageReq(), "", 1, 1, true)
	require.NoError(t, err)
	assert.Equal(t, http.StatusOK, resp.StatusCode)
	assert.Equal(t, 1, fake.calls)
}

func TestOrgImageScheduleBlock_SetExpireKeepLatest(t *testing.T) {
	s := &OpenAIGatewayService{}

	assert.False(t, s.isOrgImageScheduleBlocked("org-x"), "unset org not blocked")
	assert.False(t, s.isOrgImageScheduleBlocked(""), "empty org never blocked")

	// 过去/零值时间 -> 应用兜底冷却（5s），处于封禁中
	s.blockOrgImageScheduling("org-x", time.Time{})
	assert.True(t, s.isOrgImageScheduleBlocked("org-x"))

	// 较短到期，到期后自动解封
	s2 := &OpenAIGatewayService{}
	s2.blockOrgImageScheduling("org-y", time.Now().Add(60*time.Millisecond))
	assert.True(t, s2.isOrgImageScheduleBlocked("org-y"))
	time.Sleep(90 * time.Millisecond)
	assert.False(t, s2.isOrgImageScheduleBlocked("org-y"), "should auto-clear after expiry")
}

func TestRecordImageOrgRateLimit_OrgScopedOnly(t *testing.T) {
	// 组织级限流：按报文中的 org 设置冷却（account 传 nil，走 body 提取）
	s := &OpenAIGatewayService{}
	s.recordImageOrgRateLimit(nil, http.Header{}, []byte(orgImageRateLimitBody))
	assert.True(t, s.isOrgImageScheduleBlocked("org-BOvpEHVcDPTe8h4lZnwMO5Ly"))

	// 账号级配额限流：不应设置任何 org 冷却
	s2 := &OpenAIGatewayService{}
	s2.recordImageOrgRateLimit(nil, http.Header{}, []byte(accountUsageLimitBody))
	assert.False(t, s2.isOrgImageScheduleBlocked("org-BOvpEHVcDPTe8h4lZnwMO5Ly"))
}
