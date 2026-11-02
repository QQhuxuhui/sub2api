//go:build unit

package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func invalidEncryptedContentResponse() *http.Response {
	return &http.Response{
		StatusCode: http.StatusBadRequest,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body: io.NopCloser(strings.NewReader(
			`{"error":{"code":"invalid_encrypted_content","type":"invalid_request_error","message":"The encrypted content could not be verified."}}`,
		)),
	}
}

// encrypted 修复在 Forward 内先做一次，不计换号次数；修复后仍失败才交给规则换号。
// 换到新账号时修复次数重新计算。
func TestForwardEncryptedRecoveryRunsBeforeRuleFailoverAndResetsPerAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := &httpUpstreamSequenceRecorder{responses: []*http.Response{
		invalidEncryptedContentResponse(),
		invalidEncryptedContentResponse(),
		invalidEncryptedContentResponse(),
		{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"id":"resp_ok","usage":{"input_tokens":1,"output_tokens":2}}`)),
		},
	}}
	cfg := &config.Config{}
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: upstream}

	policy := enabledFailoverPolicy(OpenAIFailoverRule{
		ID: "encrypted", Enabled: true, StatusCode: http.StatusBadRequest,
		Keywords: []string{"invalid_encrypted_content"}, Action: OpenAIFailoverActionFailover,
	})
	newContext := func() *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		ctx := WithOpenAIFailoverPolicy(context.Background(), policy)
		c.Request = httptest.NewRequest(http.MethodPost, "/openai/v1/responses", nil).WithContext(ctx)
		SetOpenAIClientTransport(c, OpenAIClientTransportHTTP)
		return c
	}
	account := func(id int64) *Account {
		return &Account{
			ID: id, Name: "a", Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1,
			Credentials: map[string]any{"api_key": "sk-test", "base_url": "http://upstream.example"},
		}
	}
	body := []byte(`{"model":"gpt-5.1","stream":false,"input":[{"type":"reasoning","encrypted_content":"gAAA","summary":[{"type":"summary_text","text":"k"}]},{"type":"input_text","text":"hello"}]}`)

	_, err := svc.Forward(context.Background(), newContext(), account(1), body)
	var failoverErr *UpstreamFailoverError
	require.ErrorAs(t, err, &failoverErr)
	require.Equal(t, "encrypted", failoverErr.FailoverRuleID)
	require.False(t, failoverErr.RetryableOnSameAccount)
	require.Equal(t, 2, upstream.callCount, "修复一次后仍失败才走规则")

	// 已判定失效的密文同会话会被预先剥离，这里换一份新密文才能再次触发修复。
	nextBody := []byte(strings.ReplaceAll(string(body), "gAAA", "gBBB"))
	result, err := svc.Forward(context.Background(), newContext(), account(2), nextBody)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 4, upstream.callCount, "新账号可以再修复一次")
}
