//go:build unit

package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// 回归：部分中转 / 号池（如经 CLIProxyAPIPlus 的 antigravity 号池）会把上游错误包装成
// HTTP 200 + {"type":"error",...} 返回。这类响应必须被识别为上游错误并触发 failover，
// 否则会被记成 usage 全 0 的“成功”用量、且完全不进 Ops 错误日志。
func newClaudeError2xxService() *GatewayService {
	return &GatewayService{
		cfg:              &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		rateLimitService: nil, // 置 nil：helper 会跳过 HandleUpstreamError，聚焦检测与返回类型
	}
}

func newClaudeErr2xxResp(status int, body string) (*http.Response, []byte) {
	resp := &http.Response{StatusCode: status, Header: make(http.Header)}
	return resp, []byte(body)
}

func TestClaudeErrorBodyIn2xxFailover(t *testing.T) {
	svc := newClaudeError2xxService()
	acct := &Account{ID: 1107, Name: "cpa", Platform: "anthropic"}

	t.Run("200 error body triggers failover", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		resp, body := newClaudeErr2xxResp(http.StatusOK,
			`{"type":"error","error":{"type":"invalid_request_error","message":"This version of Antigravity is no longer supported. Please upgrade to receive the latest features."}}`)

		err := svc.claudeErrorBodyIn2xxFailover(context.Background(), resp, c, acct, body, true)
		require.Error(t, err, "200 + Claude error body 必须返回错误而非 nil")

		var failoverErr *UpstreamFailoverError
		require.ErrorAs(t, err, &failoverErr, "应为 UpstreamFailoverError 以触发账号切换")
		require.Equal(t, http.StatusBadGateway, failoverErr.StatusCode)
	})

	t.Run("200 normal message returns nil", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		resp, body := newClaudeErr2xxResp(http.StatusOK,
			`{"type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"usage":{"input_tokens":10,"output_tokens":5}}`)

		err := svc.claudeErrorBodyIn2xxFailover(context.Background(), resp, c, acct, body, true)
		require.NoError(t, err, "正常 assistant 消息不应被误判为错误")
	})

	t.Run("non-2xx error body returns nil (handled elsewhere)", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		resp, body := newClaudeErr2xxResp(http.StatusBadGateway,
			`{"type":"error","error":{"type":"upstream_error","message":"bad gateway"}}`)

		err := svc.claudeErrorBodyIn2xxFailover(context.Background(), resp, c, acct, body, true)
		require.NoError(t, err, "非 2xx 由 handleErrorResponse 处理，本 helper 应放行")
	})

	t.Run("200 non-claude json returns nil", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		resp, body := newClaudeErr2xxResp(http.StatusOK, `{"choices":[{"message":{"content":"ok"}}]}`)

		err := svc.claudeErrorBodyIn2xxFailover(context.Background(), resp, c, acct, body, false)
		require.NoError(t, err, "非 Claude 形状的 200 响应不应触发 failover")
	})

	t.Run("error message is extracted for ops", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		resp, body := newClaudeErr2xxResp(http.StatusOK,
			`{"type":"error","error":{"type":"invalid_request_error","message":"no longer supported"}}`)

		err := svc.claudeErrorBodyIn2xxFailover(context.Background(), resp, c, acct, body, true)
		require.Error(t, err)
		// 确认 Ops 错误上下文被写入（message 非空）
		msg := strings.TrimSpace(extractUpstreamErrorMessage(body))
		require.Equal(t, "no longer supported", msg)
	})
}
