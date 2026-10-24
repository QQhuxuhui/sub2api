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
	"github.com/Wei-Shaw/sub2api/internal/pkg/erroridentity"
	coderws "github.com/coder/websocket"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

const identityFailedEvent = `{"type":"response.failed","response":{"id":"resp_identity","model":"hidden","status":"failed","error":{"code":"cyber_policy","message":"hidden codex2api"},"usage":{"input_tokens":9,"output_tokens":2}}}`

func identityTestCache(t *testing.T) *erroridentity.Cache {
	t.Helper()
	c := erroridentity.NewCache(func(context.Context) ([]string, error) { return []string{"hidden"}, nil })
	require.NoError(t, c.Refresh(context.Background()))
	return c
}
func assertIdentityEvent(t *testing.T, event []byte, model string) {
	t.Helper()
	require.Equal(t, model, gjson.GetBytes(event, "response.model").String())
	require.Equal(t, model+" upstream", gjson.GetBytes(event, "response.error.message").String())
	require.Equal(t, "cyber_policy", gjson.GetBytes(event, "response.error.code").String())
}

func TestErrorIdentityHTTPBridgeWireAndAudit(t *testing.T) {
	svc := &OpenAIGatewayService{cfg: &config.Config{}, errorIdentity: identityTestCache(t)}
	account := &Account{ID: 112, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Concurrency: 1}
	for _, model := range []string{"first", "second"} {
		svc.httpUpstream = &httpUpstreamRecorder{resp: &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader("data: " + identityFailedEvent + "\n\n"))}}
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "/v1/responses", nil)
		payload := []byte(`{"type":"response.create","model":"` + model + `","input":"hi"}`)
		var writes [][]byte
		_, err := svc.proxyOpenAIWSHTTPBridgeTurn(context.Background(), c, account, "sk-test", payload, len(payload), model, "", "", "", "", 1, func(b []byte) error { writes = append(writes, b); return nil })
		require.NoError(t, err)
		require.Len(t, writes, 1)
		assertIdentityEvent(t, writes[0], model)
		mark := GetOpsCyberPolicy(c)
		require.NotNil(t, mark)
		require.Contains(t, mark.Body, "hidden codex2api")
	}
}

func TestErrorIdentityPassthroughWireAndAudit(t *testing.T) {
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(context.Canceled)
	upstream := newStagedPassthroughConn()
	upstream.Send(identityFailedEvent)
	svc := newPassthroughLifecycleService(passthroughLifecycleConfig(), upstream)
	svc.errorIdentity = identityTestCache(t)
	marks := make(chan *CyberPolicyMark, 1)
	server, done := startPassthroughLifecycleServerWithHooks(t, ctx, svc, passthroughLifecycleAccount(), func(c *gin.Context) *OpenAIWSIngressHooks {
		return &OpenAIWSIngressHooks{AfterTurn: func(int, *OpenAIForwardResult, error) { marks <- GetOpsCyberPolicy(c) }}
	})
	defer server.Close()
	client := dialPassthroughLifecycleClient(t, server)
	defer client.CloseNow()
	event, err := readPassthroughLifecycleFrame(t, client, 3*time.Second)
	require.NoError(t, err)
	assertIdentityEvent(t, event, "gpt-5.1")
	select {
	case mark := <-marks:
		require.NotNil(t, mark)
		require.Contains(t, mark.Body, "hidden codex2api")
	case <-time.After(3 * time.Second):
		t.Fatal("missing audit")
	}
	_ = client.Close(coderws.StatusNormalClosure, "")
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("shutdown")
	}
}

func TestErrorIdentityCtxPoolWireAndAudit(t *testing.T) {
	cfg := newOpenAIWSV2TestConfig()
	cfg.Security.URLAllowlist.Enabled = false
	cfg.Security.URLAllowlist.AllowInsecureHTTP = true
	cfg.Gateway.OpenAIWS.ModeRouterV2Enabled = true
	cfg.Gateway.OpenAIWS.IngressModeDefault = OpenAIWSIngressModeCtxPool
	cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
	pool := newOpenAIWSConnPool(cfg)
	pool.setClientDialerForTest(&openAIWSCaptureDialer{conn: &openAIWSCaptureConn{events: [][]byte{[]byte(identityFailedEvent)}}})
	svc := &OpenAIGatewayService{cfg: cfg, httpUpstream: &httpUpstreamRecorder{}, cache: &stubGatewayCache{}, openaiWSResolver: NewOpenAIWSProtocolResolver(cfg), toolCorrector: NewCodexToolCorrector(), openaiWSPool: pool, errorIdentity: identityTestCache(t)}
	defer svc.CloseOpenAIWSPool()
	account := &Account{ID: 5402, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Concurrency: 1, Credentials: map[string]any{"api_key": "sk-test"}, Extra: map[string]any{"openai_apikey_responses_websockets_v2_mode": OpenAIWSIngressModeCtxPool}}
	marks := make(chan *CyberPolicyMark, 1)
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
		done <- svc.ProxyResponsesWebSocketFromClient(r.Context(), c, conn, account, "sk-test", first, &OpenAIWSIngressHooks{AfterTurn: func(int, *OpenAIForwardResult, error) { marks <- GetOpsCyberPolicy(c) }})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client, _, err := coderws.Dial(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), nil)
	require.NoError(t, err)
	defer client.CloseNow()
	require.NoError(t, client.Write(ctx, coderws.MessageText, []byte(`{"type":"response.create","model":"public","stream":false}`)))
	_, event, err := client.Read(ctx)
	require.NoError(t, err)
	assertIdentityEvent(t, event, "public")
	select {
	case mark := <-marks:
		require.NotNil(t, mark)
		require.Contains(t, mark.Body, "hidden codex2api")
	case <-ctx.Done():
		t.Fatal("missing audit")
	}
	_ = client.CloseNow()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("shutdown")
	}
}
