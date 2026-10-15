package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 上游偷换成带日期快照名的场景：客户端请求 my-alias，渠道映射成 chan-model，
// 账号映射成 gpt-5.4，上游实际回 gpt-5.4-2026-03-05。
const (
	rewriteTestClientModel   = "my-alias"
	rewriteTestChannelModel  = "chan-model"
	rewriteTestUpstreamModel = "gpt-5.4"
	rewriteTestSwappedModel  = "gpt-5.4-2026-03-05"
)

func newRewriteTestContext(t *testing.T, clientModel string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
	if clientModel != "" {
		c.Request = c.Request.WithContext(context.WithValue(c.Request.Context(), ctxkey.Model, clientModel))
	}
	return c, rec
}

func openAIRewriteTestAccount(accountType string) *Account {
	return &Account{ID: 1, Platform: PlatformOpenAI, Type: accountType}
}

func TestOpenAIResponseModelRewrite_ForcesClientModelOnOpenAI(t *testing.T) {
	c, _ := newRewriteTestContext(t, rewriteTestClientModel)
	rw := newOpenAIResponseModelRewrite(c, openAIRewriteTestAccount(AccountTypeAPIKey), rewriteTestChannelModel, rewriteTestUpstreamModel)
	require.True(t, rw.force)
	require.Equal(t, rewriteTestClientModel, rw.to)

	body := []byte(`{"model":"gpt-5.4-2026-03-05","response":{"model":"other"},"message":{"model":"x"},"output":[{"model":"keep"}]}`)
	out := rw.applyJSON(body)
	require.Equal(t, rewriteTestClientModel, gjson.GetBytes(out, "model").String())
	require.Equal(t, rewriteTestClientModel, gjson.GetBytes(out, "response.model").String())
	require.Equal(t, rewriteTestClientModel, gjson.GetBytes(out, "message.model").String())
	require.Equal(t, "keep", gjson.GetBytes(out, "output.0.model").String(), "只改约定的结构化字段")
}

func TestOpenAIResponseModelRewrite_FallsBackToForwardModelWithoutClientContext(t *testing.T) {
	c, _ := newRewriteTestContext(t, "")
	rw := newOpenAIResponseModelRewrite(c, openAIRewriteTestAccount(AccountTypeOAuth), rewriteTestClientModel, rewriteTestUpstreamModel)
	require.True(t, rw.force)
	require.Equal(t, rewriteTestClientModel, rw.to)
}

func TestOpenAIResponseModelRewrite_NoRenameLeavesResponseUntouched(t *testing.T) {
	c, _ := newRewriteTestContext(t, rewriteTestUpstreamModel)
	rw := newOpenAIResponseModelRewrite(c, openAIRewriteTestAccount(AccountTypeOAuth), rewriteTestUpstreamModel, rewriteTestUpstreamModel)
	require.False(t, rw.active())

	body := []byte(`{"model":"gpt-5.4-2026-03-05"}`)
	require.Equal(t, string(body), string(rw.applyJSON(body)))
	line := `data: {"type":"response.created","response":{"model":"gpt-5.4-2026-03-05"}}`
	require.Equal(t, line, rw.applySSELine(line))
}

// 渠道把 A 映射成 B、账号又把 B 映射回 A：净效果没有改名，不能把响应改成中间名 B。
func TestOpenAIResponseModelRewrite_RoundTripMappingDoesNotExposeIntermediateName(t *testing.T) {
	c, _ := newRewriteTestContext(t, rewriteTestUpstreamModel)
	rw := newOpenAIResponseModelRewrite(c, openAIRewriteTestAccount(AccountTypeAPIKey), rewriteTestChannelModel, rewriteTestUpstreamModel)
	require.False(t, rw.active())
}

func TestOpenAIResponseModelRewrite_NonOpenAIKeepsExactMatchSemantics(t *testing.T) {
	c, _ := newRewriteTestContext(t, rewriteTestClientModel)
	account := &Account{ID: 1, Platform: PlatformGrok, Type: AccountTypeAPIKey}
	rw := newOpenAIResponseModelRewrite(c, account, "grok-alias", "grok-4.5")
	require.False(t, rw.force)

	swapped := []byte(`{"model":"grok-4.5-0709"}`)
	require.Equal(t, string(swapped), string(rw.applyJSON(swapped)), "非 OpenAI 平台不做强制覆盖")
	exact := rw.applyJSON([]byte(`{"model":"grok-4.5"}`))
	require.Equal(t, "grok-alias", gjson.GetBytes(exact, "model").String())
	require.Equal(t, "grok-alias", openAIClientVisibleModel(c, account, "grok-alias"), "非 OpenAI 平台不读客户端上下文")
	require.False(t, rw.forcedOnly().active())
}

func TestOpenAIResponseModelRewrite_SSE(t *testing.T) {
	c, _ := newRewriteTestContext(t, rewriteTestClientModel)
	rw := newOpenAIResponseModelRewrite(c, openAIRewriteTestAccount(AccountTypeOAuth), rewriteTestClientModel, rewriteTestUpstreamModel)

	created := `data: {"type":"response.created","response":{"model":"gpt-5.4-2026-03-05"}}`
	require.Equal(t, rewriteTestClientModel, gjson.Get(strings.TrimPrefix(rw.applySSELine(created), "data: "), "response.model").String())

	delta := `data: {"type":"response.output_text.delta","delta":"model"}`
	require.Equal(t, delta, rw.applySSELine(delta))
	require.Equal(t, "data: [DONE]", rw.applySSELine("data: [DONE]"))
	require.Equal(t, "event: response.created", rw.applySSELine("event: response.created"))
	nullModel := `data: {"model":null}`
	require.Equal(t, nullModel, rw.applySSELine(nullModel))

	body := "event: response.created\n" + created + "\n\n" + `data: {"type":"response.completed","response":{"model":"gpt-5.4"}}` + "\n\n"
	out := rw.applySSEBody(body)
	require.NotContains(t, out, "gpt-5.4")
	require.Equal(t, 2, strings.Count(out, rewriteTestClientModel))
}

// requireClientSeesAliasAndLogKeepsUpstream 锁住核心约定：客户端拿到别名，
// 管理端日志字段（observer）记录的是上游原值。
func requireClientSeesAliasAndLogKeepsUpstream(t *testing.T, c *gin.Context, clientBody string) {
	t.Helper()
	require.NotContains(t, clientBody, rewriteTestSwappedModel)
	require.Contains(t, clientBody, rewriteTestClientModel)
	require.Equal(t, rewriteTestSwappedModel, observedUpstreamResponseModel(c))
}

func swappedResponsesSSE() string {
	return strings.Join([]string{
		"event: response.created",
		`data: {"type":"response.created","response":{"id":"resp_1","model":"gpt-5.4-2026-03-05","status":"in_progress","output":[]}}`,
		"",
		"event: response.output_text.delta",
		`data: {"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"hi"}`,
		"",
		"event: response.completed",
		`data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.4-2026-03-05","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","status":"completed","content":[{"type":"output_text","text":"hi","annotations":[]}]}],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
}

func newRewriteTestUpstreamResponse(contentType, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{contentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func newRewriteTestService() *OpenAIGatewayService {
	return &OpenAIGatewayService{
		cfg:           &config.Config{Gateway: config.GatewayConfig{MaxLineSize: defaultMaxLineSize}},
		toolCorrector: NewCodexToolCorrector(),
	}
}

func TestHandleStreamingResponse_ForcesClientModelAndObservesUpstream(t *testing.T) {
	c, rec := newRewriteTestContext(t, rewriteTestClientModel)
	resp := newRewriteTestUpstreamResponse("text/event-stream", swappedResponsesSSE())

	_, err := newRewriteTestService().handleStreamingResponse(context.Background(), resp, c, openAIRewriteTestAccount(AccountTypeOAuth), time.Now(), rewriteTestChannelModel, rewriteTestUpstreamModel)
	require.NoError(t, err)
	requireClientSeesAliasAndLogKeepsUpstream(t, c, rec.Body.String())
	require.NotContains(t, rec.Body.String(), rewriteTestChannelModel, "渠道映射的中间名也不能外泄")
}

func TestHandleNonStreamingResponse_ForcesClientModelAndObservesUpstream(t *testing.T) {
	c, rec := newRewriteTestContext(t, rewriteTestClientModel)
	body := `{"id":"resp_1","object":"response","model":"gpt-5.4-2026-03-05","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`
	resp := newRewriteTestUpstreamResponse("application/json", body)

	_, err := newRewriteTestService().handleNonStreamingResponse(context.Background(), resp, c, openAIRewriteTestAccount(AccountTypeAPIKey), rewriteTestChannelModel, rewriteTestUpstreamModel)
	require.NoError(t, err)
	requireClientSeesAliasAndLogKeepsUpstream(t, c, rec.Body.String())
	require.Equal(t, rewriteTestClientModel, gjson.Get(rec.Body.String(), "model").String())
}

func TestHandleNonStreamingResponse_SSEBodyForcesClientModel(t *testing.T) {
	c, rec := newRewriteTestContext(t, rewriteTestClientModel)
	resp := newRewriteTestUpstreamResponse("text/event-stream", swappedResponsesSSE())

	_, err := newRewriteTestService().handleNonStreamingResponse(context.Background(), resp, c, openAIRewriteTestAccount(AccountTypeOAuth), rewriteTestChannelModel, rewriteTestUpstreamModel)
	require.NoError(t, err)
	requireClientSeesAliasAndLogKeepsUpstream(t, c, rec.Body.String())
}

func TestHandleStreamingResponsePassthrough_ForcesClientModelAndObservesUpstream(t *testing.T) {
	c, rec := newRewriteTestContext(t, rewriteTestClientModel)
	resp := newRewriteTestUpstreamResponse("text/event-stream", swappedResponsesSSE())

	_, err := newRewriteTestService().handleStreamingResponsePassthrough(context.Background(), resp, c, openAIRewriteTestAccount(AccountTypeAPIKey), time.Now(), rewriteTestChannelModel, rewriteTestUpstreamModel)
	require.NoError(t, err)
	requireClientSeesAliasAndLogKeepsUpstream(t, c, rec.Body.String())
}

func TestHandleNonStreamingResponsePassthrough_ForcesClientModelAndObservesUpstream(t *testing.T) {
	c, rec := newRewriteTestContext(t, rewriteTestClientModel)
	body := `{"id":"resp_1","object":"response","model":"gpt-5.4-2026-03-05","status":"completed","output":[],"usage":{"input_tokens":3,"output_tokens":1,"total_tokens":4}}`
	resp := newRewriteTestUpstreamResponse("application/json", body)

	_, err := newRewriteTestService().handleNonStreamingResponsePassthrough(context.Background(), resp, c, openAIRewriteTestAccount(AccountTypeAPIKey), rewriteTestChannelModel, rewriteTestUpstreamModel)
	require.NoError(t, err)
	requireClientSeesAliasAndLogKeepsUpstream(t, c, rec.Body.String())
}

func TestRawChatCompletions_ForcesClientModelOnlyForOpenAI(t *testing.T) {
	ccBody := `{"id":"chatcmpl_1","object":"chat.completion","model":"gpt-5.4-2026-03-05","choices":[{"index":0,"message":{"role":"assistant","content":"hi"},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`

	c, rec := newRewriteTestContext(t, rewriteTestClientModel)
	_, err := newRewriteTestService().bufferRawChatCompletions(c, newRewriteTestUpstreamResponse("application/json", ccBody), openAIRewriteTestAccount(AccountTypeAPIKey), rewriteTestChannelModel, rewriteTestUpstreamModel, rewriteTestUpstreamModel, nil, nil, time.Now())
	require.NoError(t, err)
	requireClientSeesAliasAndLogKeepsUpstream(t, c, rec.Body.String())

	chunks := strings.Join([]string{
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-5.4-2026-03-05","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"}}]}`,
		"",
		`data: {"id":"chatcmpl_1","object":"chat.completion.chunk","model":"gpt-5.4-2026-03-05","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":3,"completion_tokens":1,"total_tokens":4}}`,
		"",
		"data: [DONE]",
		"",
	}, "\n")
	c, rec = newRewriteTestContext(t, rewriteTestClientModel)
	_, err = newRewriteTestService().streamRawChatCompletions(c, newRewriteTestUpstreamResponse("text/event-stream", chunks), openAIRewriteTestAccount(AccountTypeAPIKey), rewriteTestChannelModel, rewriteTestUpstreamModel, rewriteTestUpstreamModel, nil, nil, time.Now(), 0)
	require.NoError(t, err)
	requireClientSeesAliasAndLogKeepsUpstream(t, c, rec.Body.String())

	// 国产供应商的 raw Chat Completions 维持原样透传。
	c, rec = newRewriteTestContext(t, rewriteTestClientModel)
	cnAccount := &Account{ID: 2, Platform: PlatformDeepseek, Type: AccountTypeAPIKey}
	_, err = newRewriteTestService().bufferRawChatCompletions(c, newRewriteTestUpstreamResponse("application/json", ccBody), cnAccount, rewriteTestChannelModel, rewriteTestUpstreamModel, rewriteTestUpstreamModel, nil, nil, time.Now())
	require.NoError(t, err)
	require.Equal(t, rewriteTestSwappedModel, gjson.Get(rec.Body.String(), "model").String())
}

func TestOpenAIWSMessageRewrite_ForcesClientModel(t *testing.T) {
	rw := newOpenAIResponseModelRewrite(nil, openAIRewriteTestAccount(AccountTypeOAuth), rewriteTestClientModel, rewriteTestUpstreamModel)
	frame := []byte(`{"type":"response.completed","response":{"id":"resp_1","model":"gpt-5.4-2026-03-05"}}`)
	require.True(t, rw.mayApplyBytes(frame))
	require.Equal(t, rewriteTestClientModel, gjson.GetBytes(rw.applyJSON(frame), "response.model").String())
}

// 格式转换路径本就用转发层模型名生成响应，这里锁住改用客户端原始请求名，
// 渠道映射的中间名不外泄。
func TestConversionPaths_UseClientRequestedModel(t *testing.T) {
	account := openAIRewriteTestAccount(AccountTypeOAuth)

	c, rec := newRewriteTestContext(t, rewriteTestClientModel)
	_, err := newRewriteTestService().handleChatBufferedStreamingResponse(newRewriteTestUpstreamResponse("text/event-stream", swappedResponsesSSE()), c, account, rewriteTestChannelModel, rewriteTestUpstreamModel, rewriteTestUpstreamModel, time.Now())
	require.NoError(t, err)
	requireClientSeesAliasAndLogKeepsUpstream(t, c, rec.Body.String())
	require.Equal(t, rewriteTestClientModel, gjson.Get(rec.Body.String(), "model").String())

	c, rec = newRewriteTestContext(t, rewriteTestClientModel)
	_, err = newRewriteTestService().handleChatStreamingResponse(newRewriteTestUpstreamResponse("text/event-stream", swappedResponsesSSE()), c, account, rewriteTestChannelModel, rewriteTestUpstreamModel, rewriteTestUpstreamModel, time.Now(), 0)
	require.NoError(t, err)
	requireClientSeesAliasAndLogKeepsUpstream(t, c, rec.Body.String())
	require.NotContains(t, rec.Body.String(), rewriteTestChannelModel)

	c, rec = newRewriteTestContext(t, rewriteTestClientModel)
	_, err = newRewriteTestService().handleAnthropicBufferedStreamingResponse(newRewriteTestUpstreamResponse("text/event-stream", swappedResponsesSSE()), c, account, rewriteTestChannelModel, rewriteTestUpstreamModel, rewriteTestUpstreamModel, time.Now())
	require.NoError(t, err)
	requireClientSeesAliasAndLogKeepsUpstream(t, c, rec.Body.String())
	require.Equal(t, rewriteTestClientModel, gjson.Get(rec.Body.String(), "model").String())

	c, rec = newRewriteTestContext(t, rewriteTestClientModel)
	_, err = newRewriteTestService().handleAnthropicStreamingResponse(newRewriteTestUpstreamResponse("text/event-stream", swappedResponsesSSE()), c, account, rewriteTestChannelModel, rewriteTestUpstreamModel, rewriteTestUpstreamModel, time.Now())
	require.NoError(t, err)
	requireClientSeesAliasAndLogKeepsUpstream(t, c, rec.Body.String())
	require.NotContains(t, rec.Body.String(), rewriteTestChannelModel)
}
