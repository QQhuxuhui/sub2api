//go:build unit

package handler

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/pkg/ctxkey"
	"github.com/Wei-Shaw/sub2api/internal/pkg/tlsfingerprint"
	middleware "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
)

const (
	e2ePing           = "event: ping\ndata: {\"type\": \"ping\"}\n\n"
	e2eDataOnlyError  = "data: {\"type\":\"error\",\"error\":{\"type\":\"rate_limit_error\",\"code\":\"gateway_concurrency_limit\",\"message\":\"Concurrency limit exceeded for user, please retry later\"}}\n\n"
	e2eQueuedThenFail = e2ePing + e2ePing + e2eDataOnlyError
	e2eSuccessStream  = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_b\",\"model\":\"claude-sonnet-4-5\",\"usage\":{\"input_tokens\":3}}}\n\n" +
		"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
		"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"hi\"}}\n\n" +
		"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
		"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":2}}\n\n" +
		"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	e2eNoChannel422 = `{"error":{"message":"请求模型 [claude-sonnet-4-5] 在 API Key 路由分组 [claude-max] 中暂无可用渠道。","type":"invalid_request_error"},"type":"error"}`
)

type e2eUpstreamReply struct {
	status int
	body   string
	sse    bool
}

// e2ePerAccountUpstream answers by account, a fresh response per attempt.
type e2ePerAccountUpstream struct {
	mu      sync.Mutex
	replies map[int64]e2eUpstreamReply
	calls   []int64
}

func (u *e2ePerAccountUpstream) Do(req *http.Request, _ string, accountID int64, _ int) (*http.Response, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.calls = append(u.calls, accountID)
	if req != nil && req.Body != nil {
		_, _ = io.Copy(io.Discard, req.Body)
		_ = req.Body.Close()
	}
	reply := u.replies[accountID]
	header := http.Header{"Content-Type": []string{"application/json"}}
	if reply.sse {
		header.Set("Content-Type", "text/event-stream")
	}
	return &http.Response{StatusCode: reply.status, Header: header, Body: io.NopCloser(strings.NewReader(reply.body))}, nil
}

func (u *e2ePerAccountUpstream) DoWithTLS(req *http.Request, proxyURL string, accountID int64, concurrency int, _ *tlsfingerprint.Profile) (*http.Response, error) {
	return u.Do(req, proxyURL, accountID, concurrency)
}

type e2eSettingRepo struct {
	mu   sync.Mutex
	data map[string]string
}

func (r *e2eSettingRepo) Get(context.Context, string) (*service.Setting, error) {
	return nil, service.ErrSettingNotFound
}
func (r *e2eSettingRepo) GetValue(_ context.Context, key string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if v, ok := r.data[key]; ok {
		return v, nil
	}
	return "", service.ErrSettingNotFound
}
func (r *e2eSettingRepo) Set(_ context.Context, key, value string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[key] = value
	return nil
}
func (r *e2eSettingRepo) GetMultiple(context.Context, []string) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *e2eSettingRepo) SetMultiple(context.Context, map[string]string) error { return nil }
func (r *e2eSettingRepo) GetAll(context.Context) (map[string]string, error) {
	return map[string]string{}, nil
}
func (r *e2eSettingRepo) Delete(context.Context, string) error { return nil }

const e2eGroupID = int64(18)

func e2eAccount(id int64, priority int) *service.Account {
	return &service.Account{
		ID:            id,
		Name:          "relay",
		Platform:      service.PlatformAnthropic,
		Type:          service.AccountTypeAPIKey,
		Credentials:   map[string]any{"api_key": "k", "base_url": "https://relay.example.com", "pool_mode": true},
		Extra:         map[string]any{},
		Status:        service.StatusActive,
		Schedulable:   true,
		Concurrency:   10,
		Priority:      priority,
		AccountGroups: []service.AccountGroup{{AccountID: id, GroupID: e2eGroupID}},
	}
}

type e2eHarness struct {
	handler  *GatewayHandler
	upstream *e2ePerAccountUpstream
	policy   *service.AnthropicFailoverPolicyService
}

func newE2EHarness(t *testing.T, accounts []*service.Account, replies map[int64]e2eUpstreamReply) *e2eHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	group := &service.Group{ID: e2eGroupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
	upstream := &e2ePerAccountUpstream{replies: replies}
	cfg := &config.Config{RunMode: config.RunModeSimple}
	cfg.Gateway.MaxLineSize = 1 << 20
	settingRepo := &e2eSettingRepo{data: map[string]string{}}
	schedulerSnapshot := service.NewSchedulerSnapshotService(&fakeSchedulerCache{accounts: accounts}, nil, nil, nil, nil)
	gwSvc := service.NewGatewayService(
		nil, &fakeGroupRepo{group: group}, nil, nil, nil, nil, nil, nil,
		cfg, schedulerSnapshot, nil, nil, nil, nil, nil,
		upstream,
		nil, nil, nil, nil, nil,
		service.NewSettingService(settingRepo, cfg),
		nil, nil, nil, nil, nil, nil, nil,
	)
	billingCacheSvc := service.NewBillingCacheService(nil, nil, nil, nil, nil, nil, cfg, nil)
	t.Cleanup(billingCacheSvc.Stop)
	policy := service.NewAnthropicFailoverPolicyService(settingRepo, nil)
	h := &GatewayHandler{
		gatewayService:                 gwSvc,
		billingCacheService:            billingCacheSvc,
		concurrencyHelper:              NewConcurrencyHelper(service.NewConcurrencyService(&fakeConcurrencyCache{}), SSEPingFormatClaude, 0),
		maxAccountSwitches:             5,
		maxAccountSwitchesGemini:       1,
		anthropicFailoverPolicyService: policy,
	}
	return &e2eHarness{handler: h, upstream: upstream, policy: policy}
}

func (h *e2eHarness) messages(t *testing.T, body string) *httptest.ResponseRecorder {
	t.Helper()
	group := &service.Group{ID: e2eGroupID, Hydrated: true, Platform: service.PlatformAnthropic, Status: service.StatusActive}
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v1/messages", bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	c.Request = req.WithContext(context.WithValue(req.Context(), ctxkey.Group, group))
	gid := e2eGroupID
	apiKey := &service.APIKey{ID: 200, UserID: 148, GroupID: &gid, Group: group, Status: service.StatusActive,
		User: &service.User{ID: 148, Concurrency: 10, Balance: 100}}
	c.Set(string(middleware.ContextKeyAPIKey), apiKey)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: apiKey.UserID, Concurrency: 10})
	h.handler.Messages(c)
	return rec
}

const e2eStreamRequest = `{"model":"claude-sonnet-4-5","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hello"}]}`

type e2eFrame struct{ event, data string }

// e2eParseSSE fails the test on any line that is not an SSE field line, so raw
// JSON appended to a started stream is caught.
func e2eParseSSE(t *testing.T, body string) []e2eFrame {
	t.Helper()
	var frames []e2eFrame
	for _, block := range strings.Split(body, "\n\n") {
		if strings.TrimSpace(block) == "" {
			continue
		}
		var frame e2eFrame
		for _, line := range strings.Split(block, "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				frame.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				frame.data = strings.TrimPrefix(line, "data: ")
			default:
				t.Fatalf("not an SSE field line: %q\nbody: %q", line, body)
			}
		}
		frames = append(frames, frame)
	}
	return frames
}

func e2eErrorFrames(frames []e2eFrame) []e2eFrame {
	var out []e2eFrame
	for _, f := range frames {
		if f.event == "error" || strings.Contains(f.data, `"type":"error"`) {
			out = append(out, f)
		}
	}
	return out
}

// Account A queues the request (pings), then fails in-stream without an event
// line. The customer must get account B's stream right after the pings.
func TestMessages_PingThenStreamError_SwitchesAccountSeamlessly(t *testing.T) {
	h := newE2EHarness(t, []*service.Account{e2eAccount(1, 1), e2eAccount(2, 2)}, map[int64]e2eUpstreamReply{
		1: {status: 200, body: e2eQueuedThenFail, sse: true},
		2: {status: 200, body: e2eSuccessStream, sse: true},
	})
	rec := h.messages(t, e2eStreamRequest)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []int64{1, 2}, h.upstream.calls)
	require.Equal(t, e2ePing+e2ePing+e2eSuccessStream, rec.Body.String(), "pings, then B's stream byte for byte, no error frame")
}

func TestMessages_PingThenStreamError_AllAccountsFail_OneErrorFrame(t *testing.T) {
	h := newE2EHarness(t, []*service.Account{e2eAccount(1, 1), e2eAccount(2, 2)}, map[int64]e2eUpstreamReply{
		1: {status: 200, body: e2eQueuedThenFail, sse: true},
		2: {status: 200, body: e2eQueuedThenFail, sse: true},
	})
	rec := h.messages(t, e2eStreamRequest)

	require.Equal(t, http.StatusOK, rec.Code)
	require.Equal(t, []int64{1, 2}, h.upstream.calls)
	errs := e2eErrorFrames(e2eParseSSE(t, rec.Body.String()))
	require.Len(t, errs, 1, "body: %q", rec.Body.String())
	require.Contains(t, errs[0].data, "rate_limit_error")
	require.NotContains(t, rec.Body.String(), "gateway_concurrency_limit", "the upstream frame itself is not forwarded")
}

// After the switch the stream has started: whatever the next account answers
// must stay valid SSE with exactly one terminal error.
func TestMessages_PingThenStreamError_NextAccountFailures_StayValidSSE(t *testing.T) {
	t.Run("upstream 400", func(t *testing.T) {
		h := newE2EHarness(t, []*service.Account{e2eAccount(1, 1), e2eAccount(2, 2)}, map[int64]e2eUpstreamReply{
			1: {status: 200, body: e2eQueuedThenFail, sse: true},
			2: {status: 400, body: "{\n \"type\":\"error\",\n \"error\":{\"type\":\"invalid_request_error\",\"message\":\"max_tokens: too large\"}\n}"},
		})
		rec := h.messages(t, e2eStreamRequest)
		require.Equal(t, http.StatusOK, rec.Code)
		errs := e2eErrorFrames(e2eParseSSE(t, rec.Body.String()))
		require.Len(t, errs, 1, "body: %q", rec.Body.String())
		require.Equal(t, "error", errs[0].event)
		require.JSONEq(t, `{"type":"error","error":{"type":"invalid_request_error","message":"max_tokens: too large"}}`, errs[0].data)
	})

	t.Run("local Claude 5.5 validation on the mapped model", func(t *testing.T) {
		second := e2eAccount(2, 2)
		second.Credentials["model_mapping"] = map[string]any{"claude-sonnet-4-5": "claude-opus-5-5"}
		h := newE2EHarness(t, []*service.Account{e2eAccount(1, 1), second}, map[int64]e2eUpstreamReply{
			1: {status: 200, body: e2eQueuedThenFail, sse: true},
			2: {status: 200, body: e2eSuccessStream, sse: true},
		})
		rec := h.messages(t, `{"model":"claude-sonnet-4-5","max_tokens":4096,"stream":true,"thinking":{"type":"enabled","budget_tokens":2048},"messages":[{"role":"user","content":"hello"}]}`)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, []int64{1}, h.upstream.calls, "account 2 is rejected before any upstream call")
		errs := e2eErrorFrames(e2eParseSSE(t, rec.Body.String()))
		require.Len(t, errs, 1, "body: %q", rec.Body.String())
		require.Contains(t, errs[0].data, "invalid_request_error")
		require.Contains(t, errs[0].data, "adaptive thinking")
	})
}

// Real content already went out: nothing changes, no switch.
func TestMessages_ErrorAfterRealContent_DoesNotSwitch(t *testing.T) {
	started := e2ePing + "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_a\",\"model\":\"claude-sonnet-4-5\",\"usage\":{\"input_tokens\":3}}}\n\n"
	h := newE2EHarness(t, []*service.Account{e2eAccount(1, 1), e2eAccount(2, 2)}, map[int64]e2eUpstreamReply{
		1: {status: 200, body: started + "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n", sse: true},
		2: {status: 200, body: e2eSuccessStream, sse: true},
	})
	rec := h.messages(t, e2eStreamRequest)
	require.Equal(t, []int64{1}, h.upstream.calls)
	require.True(t, strings.HasPrefix(rec.Body.String(), started))
	require.NotContains(t, rec.Body.String(), "msg_b")
	require.Len(t, e2eErrorFrames(e2eParseSSE(t, rec.Body.String())), 1)
}

func e2eEnableRule(t *testing.T, h *e2eHarness, maxSwitches int) {
	t.Helper()
	_, err := h.policy.UpdateConfig(context.Background(), service.AnthropicFailoverConfig{
		EnabledGroupIDs: []int64{e2eGroupID},
		MaxRuleSwitches: maxSwitches,
		Rules: []service.AnthropicFailoverRule{{
			ID: "no-channel", Name: "no channel", Enabled: true, StatusCode: 422,
			Keywords: []string{"暂无可用渠道"}, Action: service.AnthropicFailoverActionFailover,
		}},
	})
	require.NoError(t, err)
}

func TestMessages_RuleFailover(t *testing.T) {
	accounts := func() []*service.Account {
		return []*service.Account{e2eAccount(1, 1), e2eAccount(2, 2), e2eAccount(3, 3)}
	}
	noChannel := e2eUpstreamReply{status: 422, body: e2eNoChannel422}

	t.Run("group not opted in: unchanged", func(t *testing.T) {
		h := newE2EHarness(t, accounts(), map[int64]e2eUpstreamReply{1: noChannel, 2: {status: 200, body: e2eSuccessStream, sse: true}})
		rec := h.messages(t, e2eStreamRequest)
		require.Equal(t, []int64{1}, h.upstream.calls)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		require.JSONEq(t, `{"type":"error","error":{"type":"upstream_error","message":"Upstream request failed"}}`, rec.Body.String())
	})

	t.Run("matched: next account serves the request", func(t *testing.T) {
		h := newE2EHarness(t, accounts(), map[int64]e2eUpstreamReply{1: noChannel, 2: {status: 200, body: e2eSuccessStream, sse: true}})
		e2eEnableRule(t, h, 3)
		rec := h.messages(t, e2eStreamRequest)
		require.Equal(t, []int64{1, 2}, h.upstream.calls)
		require.Equal(t, http.StatusOK, rec.Code)
		require.Equal(t, e2eSuccessStream, rec.Body.String())
	})

	t.Run("every account fails: the customer sees today's error", func(t *testing.T) {
		h := newE2EHarness(t, accounts(), map[int64]e2eUpstreamReply{1: noChannel, 2: noChannel, 3: noChannel})
		e2eEnableRule(t, h, 5)
		rec := h.messages(t, e2eStreamRequest)
		require.Equal(t, []int64{1, 2, 3}, h.upstream.calls)
		require.Equal(t, http.StatusBadGateway, rec.Code)
		require.JSONEq(t, `{"type":"error","error":{"type":"upstream_error","message":"Upstream request failed"}}`, rec.Body.String())
	})

	t.Run("rule budget caps the switches", func(t *testing.T) {
		h := newE2EHarness(t, accounts(), map[int64]e2eUpstreamReply{1: noChannel, 2: noChannel, 3: {status: 200, body: e2eSuccessStream, sse: true}})
		e2eEnableRule(t, h, 1)
		rec := h.messages(t, e2eStreamRequest)
		require.Equal(t, []int64{1, 2}, h.upstream.calls, "one rule switch, then the error is returned")
		require.Equal(t, http.StatusBadGateway, rec.Code)
	})

	t.Run("rule switch after pings ends as one SSE error", func(t *testing.T) {
		h := newE2EHarness(t, accounts()[:2], map[int64]e2eUpstreamReply{1: {status: 200, body: e2eQueuedThenFail, sse: true}, 2: noChannel})
		e2eEnableRule(t, h, 3)
		rec := h.messages(t, e2eStreamRequest)
		require.Equal(t, http.StatusOK, rec.Code)
		errs := e2eErrorFrames(e2eParseSSE(t, rec.Body.String()))
		require.Len(t, errs, 1, "body: %q", rec.Body.String())
		require.JSONEq(t, `{"type":"error","error":{"type":"upstream_error","message":"Upstream request failed"}}`, errs[0].data)
	})
}

func TestAnthropicFailoverTrackerKeepsFirstReason(t *testing.T) {
	gid := e2eGroupID
	tracker := newAnthropicFailoverTracker(nil, 148, 200)
	tracker.note(&service.UpstreamFailoverError{StatusCode: 503}, 9, &gid)
	require.Empty(t, tracker.outcome.Reason, "ordinary failovers are not counted here")
	tracker.switched()
	require.Zero(t, tracker.outcome.Switches)

	tracker.note(&service.UpstreamFailoverError{StatusCode: 429, SafeToFailoverAfterWrite: true}, 1177, &gid)
	tracker.switched()
	tracker.note(&service.UpstreamFailoverError{StatusCode: 422, FailoverRuleID: "no-channel"}, 1128, &gid)
	tracker.switched()
	require.Equal(t, service.AnthropicFailoverReasonStream, tracker.outcome.Reason, "the first cause is never overwritten")
	require.Equal(t, 429, tracker.outcome.StatusCode)
	require.Equal(t, []int64{1177, 1128}, tracker.outcome.Accounts)
	require.Equal(t, 2, tracker.outcome.Switches)
	require.Equal(t, e2eGroupID, tracker.outcome.GroupID)

	tracker.succeeded(&service.ForwardResult{ClientDisconnect: true})
	require.False(t, tracker.outcome.Recovered, "a disconnected client is not a recovery")
	tracker.succeeded(&service.ForwardResult{})
	require.True(t, tracker.outcome.Recovered)

	// A cancelled request is never a recovery; a nil service is safe.
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil).WithContext(ctx)
	tracker.finish(c)
	require.True(t, tracker.outcome.Canceled)
	require.False(t, tracker.outcome.Recovered)
}

func TestHandleFailoverExhausted_RuleErrorKeepsOriginalPresentation(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	h := &GatewayHandler{gatewayService: service.NewGatewayService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)}
	const upstream = `{"type":"error","error":{"type":"invalid_request_error","message":"当前会话不可继续使用，请新建会话后重试"}}`
	h.handleFailoverExhausted(c, &service.UpstreamFailoverError{StatusCode: 400, ResponseBody: []byte(upstream), FailoverRuleID: "session"}, service.PlatformAnthropic, false)
	require.Equal(t, http.StatusBadRequest, rec.Code, "without the rule table a 400 reaches the customer as is")
	require.Equal(t, upstream, rec.Body.String())
}
