//go:build unit

package service

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 上游对 stream=false 偶发回 text/event-stream 头，但体是裸 JSON（线上账号 223/136 抓到过）。
// 这时必须按 JSON 处理：usage 正常计费、model 改回客户端名、写回 application/json。
const sseHeaderBareJSONBody = `{"access_programs":[],"id":"resp_bare","object":"response","model":"gpt-5.4-2026-03-05","status":"completed","output":[{"type":"message","content":[{"type":"output_text","text":"hi"}]}],"usage":{"input_tokens":120,"output_tokens":30,"total_tokens":150,"input_tokens_details":{"cached_tokens":20}}}`

func requireBareJSONHandledAsJSON(t *testing.T, usage *OpenAIUsage, body, contentType string) {
	t.Helper()
	require.NotNil(t, usage)
	require.Equal(t, 120, usage.InputTokens)
	require.Equal(t, 30, usage.OutputTokens)
	require.Equal(t, 20, usage.CacheReadInputTokens)
	require.True(t, gjson.Valid(body), "client must receive JSON, got %q", body)
	require.Equal(t, rewriteTestClientModel, gjson.Get(body, "model").String())
	require.NotContains(t, body, rewriteTestSwappedModel)
	require.Contains(t, contentType, "application/json")
}

func TestHandleNonStreamingResponse_SSEHeaderWithBareJSONBody(t *testing.T) {
	for _, accountType := range []string{AccountTypeAPIKey, AccountTypeOAuth} {
		c, rec := newRewriteTestContext(t, rewriteTestClientModel)
		resp := newRewriteTestUpstreamResponse("text/event-stream; charset=utf-8", sseHeaderBareJSONBody)

		result, err := newRewriteTestService().handleNonStreamingResponse(context.Background(), resp, c, openAIRewriteTestAccount(accountType), rewriteTestChannelModel, rewriteTestUpstreamModel)
		require.NoError(t, err, accountType)
		requireBareJSONHandledAsJSON(t, result.usage, rec.Body.String(), rec.Header().Get("Content-Type"))
		require.Equal(t, rewriteTestSwappedModel, observedUpstreamResponseModel(c))
	}
}

func TestHandleNonStreamingResponsePassthrough_SSEHeaderWithBareJSONBody(t *testing.T) {
	c, rec := newRewriteTestContext(t, rewriteTestClientModel)
	resp := newRewriteTestUpstreamResponse("text/event-stream", sseHeaderBareJSONBody)

	result, err := newRewriteTestService().handleNonStreamingResponsePassthrough(context.Background(), resp, c, openAIRewriteTestAccount(AccountTypeAPIKey), rewriteTestChannelModel, rewriteTestUpstreamModel)
	require.NoError(t, err)
	requireBareJSONHandledAsJSON(t, result.usage, rec.Body.String(), rec.Header().Get("Content-Type"))
}

// 真正的 SSE 体仍走 SSE 转换，不受影响。
func TestSSEHeaderWithFramedBodyStillConverts(t *testing.T) {
	require.False(t, openAISSEHeaderCarriesBareJSON(newRewriteTestUpstreamResponse("text/event-stream", swappedResponsesSSE()).Header, []byte(swappedResponsesSSE())))
	require.True(t, openAISSEHeaderCarriesBareJSON(newRewriteTestUpstreamResponse("text/event-stream", "").Header, []byte(" \n"+sseHeaderBareJSONBody+"\n")))
	require.False(t, openAISSEHeaderCarriesBareJSON(newRewriteTestUpstreamResponse("application/json", "").Header, []byte(sseHeaderBareJSONBody)))
	require.False(t, openAISSEHeaderCarriesBareJSON(newRewriteTestUpstreamResponse("text/event-stream", "").Header, []byte(`not json`)))
	require.False(t, openAISSEHeaderCarriesBareJSON(newRewriteTestUpstreamResponse("text/event-stream", "").Header, []byte(`[1,2]`)))
}

// 没有 usage 的裸 JSON（如 compact 响应）仍要成功返回并能桥接成 SSE，用量记 0。
func TestHandleNonStreamingResponse_SSEHeaderBareJSONWithoutUsage(t *testing.T) {
	for _, body := range []string{
		`{"id":"resp_1","model":"gpt-5.4-2026-03-05","output":[{"type":"compaction","encrypted_content":"x"}]}`,
		`{"id":"resp_1","model":"gpt-5.4-2026-03-05","output":[{"type":"compaction","encrypted_content":"x"}],"usage":null}`,
	} {
		c, rec := newRewriteTestContext(t, rewriteTestClientModel)
		resp := newRewriteTestUpstreamResponse("text/event-stream", body)
		result, err := newRewriteTestService().handleNonStreamingResponse(context.Background(), resp, c, openAIRewriteTestAccount(AccountTypeAPIKey), rewriteTestChannelModel, rewriteTestUpstreamModel)
		require.NoError(t, err, body)
		require.Zero(t, result.usage.InputTokens)
		require.Equal(t, rewriteTestClientModel, gjson.Get(rec.Body.String(), "model").String())

		bc, brec := newCompactBridgeTestContext(t, true)
		bc.Request = bc.Request.WithContext(c.Request.Context())
		_, err = newRewriteTestService().handleNonStreamingResponse(context.Background(), newRewriteTestUpstreamResponse("text/event-stream", body), bc, openAIRewriteTestAccount(AccountTypeAPIKey), rewriteTestChannelModel, rewriteTestUpstreamModel)
		require.NoError(t, err, body)
		require.Contains(t, brec.Body.String(), "event: response.completed", "compact client stream still gets the SSE bridge")
	}
}
