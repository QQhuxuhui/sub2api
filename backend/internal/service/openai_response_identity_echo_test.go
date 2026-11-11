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
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// 上游回显里夹带的自身身份：Codex 内嵌头里的辅助模型名，以及它自己注入的工具
// （spawn_agent 描述里列着模型清单）。
const (
	echoTestUpstreamTools = `[{"type":"namespace","name":"collaboration","tools":[{"type":"function","name":"spawn_agent","description":"- ` + "`gpt-6-luna`" + `: Fast model"}]}]`
	echoTestClientTools   = `[{"type":"function","name":"read_file","parameters":{"type":"object","properties":{"n":{"type":"integer","const":9007199254740993}}}}]`
	echoTestMetadataEvent = `{"type":"codex.response.metadata","headers":{"x-codex-safety-buffering-enabled":"true","x-codex-safety-buffering-faster-model":"gpt-6-luna","X-Codex-Other-MODEL":["gpt-6-luna"],"a.b-model":"gpt-6-luna","openai-model":"gpt-6-luna","x-codex-turn-state":"gAAAA"}}`
)

func echoTestCreatedEvent(model string) string {
	return `{"type":"response.created","response":{"id":"resp_1","object":"response","model":"` + model + `","status":"in_progress","tools":` + echoTestUpstreamTools + `}}`
}

func echoTestCompletedEvent(model string) string {
	return `{"type":"response.completed","response":{"id":"resp_1","object":"response","model":"` + model + `","status":"completed","tools":` + echoTestUpstreamTools + `,"usage":{"input_tokens":3,"output_tokens":1}}}`
}

func newEchoTestRewrite(t *testing.T, clientBody string) openAIResponseModelRewrite {
	t.Helper()
	c, _ := newRewriteTestContext(t, rewriteTestClientModel)
	c.Request = c.Request.WithContext(WithOpenAIClientResponsesTools(c.Request.Context(), []byte(clientBody)))
	return newOpenAIResponseModelRewrite(c, openAIRewriteTestAccount(AccountTypeAPIKey), rewriteTestChannelModel, rewriteTestUpstreamModel)
}

func TestOpenAIResponseModelRewrite_DropsAuxModelHeaders(t *testing.T) {
	rw := newOpenAIResponseModelRewrite(nil, openAIRewriteTestAccount(AccountTypeOAuth), rewriteTestClientModel, rewriteTestUpstreamModel)
	require.True(t, rw.mayApplyBytes([]byte(echoTestMetadataEvent)))
	out := rw.applyJSON([]byte(echoTestMetadataEvent))
	require.NotContains(t, string(out), "luna")
	headers := gjson.GetBytes(out, "headers")
	require.False(t, headers.Get("x-codex-safety-buffering-faster-model").Exists())
	require.False(t, headers.Get("X-Codex-Other-MODEL").Exists())
	require.False(t, headers.Get(`a\.b-model`).Exists())
	require.Equal(t, rewriteTestClientModel, headers.Get("openai-model").String())
	require.Equal(t, "true", headers.Get("x-codex-safety-buffering-enabled").String())
	require.Equal(t, "gAAAA", headers.Get("x-codex-turn-state").String())

	// response.headers 同样处理。
	nested := []byte(`{"type":"response.created","response":{"id":"r","headers":{"x-codex-safety-buffering-faster-model":"gpt-6-luna","x-request-id":"q"}}}`)
	out = rw.applyJSON(nested)
	require.False(t, gjson.GetBytes(out, "response.headers.x-codex-safety-buffering-faster-model").Exists())
	require.Equal(t, "q", gjson.GetBytes(out, "response.headers.x-request-id").String())

	// 非强制模式（非 OpenAI 平台）不动。
	legacy := newOpenAIResponseModelRewrite(nil, &Account{Platform: PlatformGrok}, "grok-alias", "grok-4.5")
	require.Equal(t, echoTestMetadataEvent, string(legacy.applyJSON([]byte(echoTestMetadataEvent))))
}

func TestIsOpenAIAuxModelHeaderName(t *testing.T) {
	require.True(t, isOpenAIAuxModelHeaderName("x-codex-safety-buffering-faster-model"))
	require.True(t, isOpenAIAuxModelHeaderName("X-Foo-MODEL"))
	require.False(t, isOpenAIAuxModelHeaderName("openai-model"))
	require.False(t, isOpenAIAuxModelHeaderName("X-OpenAI-Model"))
	require.False(t, isOpenAIAuxModelHeaderName("-model"))
	require.False(t, isOpenAIAuxModelHeaderName("x-model-version"))
}

func TestOpenAIResponseModelRewrite_RestoresClientTools(t *testing.T) {
	// 客户端没传 tools：回显换成 []。
	rw := newEchoTestRewrite(t, `{"model":"my-alias","input":"hi"}`)
	out := rw.applyJSON([]byte(echoTestCreatedEvent("gpt-6.1-sol")))
	require.Equal(t, `[]`, gjson.GetBytes(out, "response.tools").Raw)
	require.Equal(t, rewriteTestClientModel, gjson.GetBytes(out, "response.model").String())

	// 客户端传了 tools：换回原样（包括大整数）。
	rw = newEchoTestRewrite(t, `{"model":"my-alias","tools":`+echoTestClientTools+`}`)
	out = rw.applyJSON([]byte(echoTestCreatedEvent("gpt-6.1-sol")))
	require.Equal(t, echoTestClientTools, gjson.GetBytes(out, "response.tools").Raw)

	// 不带 model 的事件也不能漏（D6）。
	noModel := `{"type":"response.in_progress","response":{"id":"resp_1","tools":` + echoTestUpstreamTools + `}}`
	require.True(t, rw.mayApplyBytes([]byte(noModel)))
	require.Equal(t, echoTestClientTools, gjson.Get(strings.TrimPrefix(rw.applySSELine("data: "+noModel), "data: "), "response.tools").Raw)

	// 非流式 Responses 体的顶层 tools。
	body := `{"id":"resp_1","object":"response","model":"gpt-6.1-sol","tools":` + echoTestUpstreamTools + `}`
	out = rw.applyJSON([]byte(body))
	require.Equal(t, echoTestClientTools, gjson.GetBytes(out, "tools").Raw)

	// 非 response 对象的顶层 tools 不动。
	other := `{"object":"something","model":"gpt-6.1-sol","tools":` + echoTestUpstreamTools + `}`
	require.Equal(t, echoTestUpstreamTools, gjson.GetBytes(rw.applyJSON([]byte(other)), "tools").Raw)

	// 回显与客户端原值相同：字节不变。
	same := `{"type":"response.created","response":{"id":"resp_1","model":"my-alias","tools":` + echoTestClientTools + `}}`
	require.Equal(t, same, string(rw.applyJSON([]byte(same))))

	// 没有 tools 字段时不新增。
	bare := `{"type":"response.created","response":{"id":"resp_1","model":"gpt-6.1-sol"}}`
	require.False(t, gjson.GetBytes(rw.applyJSON([]byte(bare)), "response.tools").Exists())
}

func TestOpenAIResponseModelRewrite_UnknownClientToolsLeavesEcho(t *testing.T) {
	event := echoTestCreatedEvent("gpt-6.1-sol")
	// 没记录客户端 tools（例如非 Responses 入口）：不处理。
	rw := newOpenAIResponseModelRewrite(nil, openAIRewriteTestAccount(AccountTypeOAuth), rewriteTestClientModel, rewriteTestUpstreamModel)
	require.Equal(t, echoTestUpstreamTools, gjson.GetBytes(rw.applyJSON([]byte(event)), "response.tools").Raw)
	// 客户端 tools 不是数组：不处理。
	rw = newEchoTestRewrite(t, `{"model":"my-alias","tools":"bad"}`)
	require.Equal(t, echoTestUpstreamTools, gjson.GetBytes(rw.applyJSON([]byte(event)), "response.tools").Raw)
	// 非强制模式：withClientTools 不生效。
	legacy := newOpenAIResponseModelRewrite(nil, &Account{Platform: PlatformGrok}, "grok-alias", "grok-4.5").withClientTools([]byte(`[]`))
	require.False(t, legacy.replacesTools())
}

func TestOpenAIClientResponsesToolsRaw(t *testing.T) {
	raw, ok := openAIClientResponsesToolsRaw([]byte(`{"model":"m"}`))
	require.True(t, ok)
	require.Equal(t, `[]`, string(raw))
	raw, ok = openAIClientResponsesToolsRaw([]byte(`{"tools":null}`))
	require.True(t, ok)
	require.Equal(t, `[]`, string(raw))
	raw, ok = openAIClientResponsesToolsRaw([]byte(`{"tools":` + echoTestClientTools + `}`))
	require.True(t, ok)
	require.Equal(t, echoTestClientTools, string(raw))
	_, ok = openAIClientResponsesToolsRaw([]byte(`{"tools":{}}`))
	require.False(t, ok)
	require.Nil(t, openAIWSClientFrameTools([]byte(`{"tools":1}`)))
}

func TestHandleStreamingResponse_ScrubsUpstreamEcho(t *testing.T) {
	c, rec := newRewriteTestContext(t, rewriteTestClientModel)
	c.Request = c.Request.WithContext(WithOpenAIClientResponsesTools(c.Request.Context(), []byte(`{"model":"my-alias","input":"hi"}`)))
	upstream := "event: codex.response.metadata\ndata: " + echoTestMetadataEvent + "\n\n" +
		"event: response.created\ndata: " + echoTestCreatedEvent("gpt-6.1-sol") + "\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_1\",\"object\":\"response\",\"model\":\"gpt-6.1-sol\",\"status\":\"completed\",\"tools\":" + echoTestUpstreamTools + ",\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n"
	_, err := newRewriteTestService().handleStreamingResponse(context.Background(), newRewriteTestUpstreamResponse("text/event-stream", upstream), c, openAIRewriteTestAccount(AccountTypeAPIKey), time.Now(), rewriteTestChannelModel, rewriteTestUpstreamModel)
	require.NoError(t, err)
	out := rec.Body.String()
	require.NotContains(t, out, "luna")
	require.NotContains(t, out, "spawn_agent")
	require.Contains(t, out, `"tools":[]`)
	require.Contains(t, out, `x-codex-turn-state`)
}

// 入站 WS 走 HTTP 桥：本轮客户端帧的 tools 经 ctx 传入。
func TestOpenAIWSHTTPBridge_RestoresTurnClientTools(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}}
	account := &Account{ID: 112, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: " + echoTestCompletedEvent("hidden") + "\n\n"))}}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
	payload := []byte(`{"type":"response.create","model":"public","input":"hi","tools":` + echoTestClientTools + `}`)
	ctx := withOpenAIClientResponsesToolsRaw(context.Background(), openAIWSClientFrameTools(payload))
	var writes [][]byte
	_, err := svc.proxyOpenAIWSHTTPBridgeTurn(ctx, c, account, "sk-test", payload, len(payload), "public", "", "", "", "", 1, func(b []byte) error { writes = append(writes, b); return nil })
	require.NoError(t, err)
	require.NotEmpty(t, writes)
	last := writes[len(writes)-1]
	require.Equal(t, echoTestClientTools, gjson.GetBytes(last, "response.tools").Raw)
	require.Equal(t, "public", gjson.GetBytes(last, "response.model").String())
}

// 入站 WS 透传：首帧没传 tools，回显换成 []。
func TestOpenAIWSPassthrough_RestoresTurnClientTools(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(echoTestMetadataEvent)
	upstream.Send(echoTestCreatedEvent("hidden"))
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	server, done := startPassthroughLifecycleServerWithHooks(t, ctx, svc, passthroughLifecycleAccount(), func(c *gin.Context) *OpenAIWSIngressHooks { return nil })
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer client.CloseNow()
	meta, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
	require.NoError(t, err)
	require.NotContains(t, string(meta), "luna")
	created, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
	require.NoError(t, err)
	require.Equal(t, `[]`, gjson.GetBytes(created, "response.tools").Raw)
	require.Equal(t, "gpt-5.1", gjson.GetBytes(created, "response.model").String())
	_ = client.Close(coderws.StatusNormalClosure, "")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown")
	}
}

// 入站 WS ctx_pool（非透传 ingress）：本轮客户端帧的 tools 换回回显。
func TestOpenAIWSIngressCtxPool_RestoresTurnClientTools(t *testing.T) {
	cfg := newOpenAIWSV2TestConfig()
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	completed := `{"type":"response.completed","response":{"id":"resp_1","object":"response","model":"hidden","status":"completed","tools":` + echoTestUpstreamTools + `,"usage":{"input_tokens":3,"output_tokens":1}}}`
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: &openAIWSCaptureConn{events: [][]byte{[]byte(echoTestMetadataEvent), []byte(completed)}}})
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool}
	defer svc.CloseOpenAIWSPool()
	account := &Account{ID: 5402, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "sk-test"}, Extra: map[string]any{"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool}}
	done := make(chan error, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := coderws.Accept(w, r, nil)
		if err != nil {
			done <- err
			return
		}
		defer conn.CloseNow()
		_, first, err := conn.Read(r.Context())
		if err != nil {
			done <- err
			return
		}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = r
		done <- svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, "sk-test", first, nil)
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer client.CloseNow()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"public","stream":false,"tools":`+echoTestClientTools+`}`)))
	var frames []string
	for len(frames) < 2 {
		_, event, err := client.Read(ctx)
		require.NoError(t, err)
		frames = append(frames, string(event))
	}
	all := strings.Join(frames, "\n")
	require.NotContains(t, all, "luna")
	require.NotContains(t, all, "spawn_agent")
	require.Equal(t, echoTestClientTools, gjson.Get(frames[1], "response.tools").Raw)
	_ = client.CloseNow()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("shutdown")
	}
}
