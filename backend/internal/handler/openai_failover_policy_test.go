//go:build unit

package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"

	"github.com/Wei-Shaw/sub2api/internal/service"
)

const failoverTestGroupID = int64(3132)

const encryptedErrorBody = `{"error":{"type":"invalid_request_error","code":"thinking_signature_invalid","message":"Encrypted function output content could not be decrypted or decoded."}}`

type failoverPolicySettings struct {
	service.SettingRepository
	raw string
}

func (s *failoverPolicySettings) GetValue(_ context.Context, key string) (string, error) {
	if key != service.SettingKeyOpenAIFailoverPolicy {
		return "", service.ErrSettingNotFound
	}
	return s.raw, nil
}

func enableFailoverPolicy(t *testing.T, h *OpenAIGatewayHandler, groups []int64, mutate ...func(*service.OpenAIFailoverConfig)) {
	t.Helper()
	cfg := service.OpenAIFailoverConfig{
		EnabledGroupIDs: groups,
		SlotWaitMs:      3000,
		MaxRuleSwitches: 3,
		MaxSlotSwitches: 5,
		Rules:           service.DefaultOpenAIFailoverRules(),
	}
	for _, change := range mutate {
		change(&cfg)
	}
	raw, err := json.Marshal(cfg)
	require.NoError(t, err)
	h.failoverPolicy = service.NewOpenAIFailoverPolicyService(&failoverPolicySettings{raw: string(raw)}, nil, nil, nil)
}

func upstreamJSON(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

func upstreamFailedStream(message string) *http.Response {
	payload, _ := json.Marshal(map[string]any{
		"type":            "response.failed",
		"sequence_number": 1,
		"response":        map[string]any{"status": "failed", "error": map[string]any{"code": "thinking_signature_invalid", "type": "invalid_request_error", "message": message}},
	})
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body: io.NopCloser(strings.NewReader("event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"id\":\"resp_1\",\"status\":\"in_progress\"}}\n\n" +
			"event: response.failed\ndata: " + string(payload) + "\n\n")),
	}
}

func TestOpenAIFailoverPolicy_HTTPRuleSwitchesAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newAstraProCapturedUpstream(upstreamJSON(http.StatusBadRequest, encryptedErrorBody), astra200())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream)
	enableFailoverPolicy(t, h, []int64{failoverTestGroupID})
	c, rec := newAstraProFailoverContext(t, astraProRequestBody)

	h.Responses(c)

	_, accountIDs, _ := upstream.snapshot()
	require.ElementsMatch(t, []int64{1, 2}, accountIDs)
	require.Equal(t, http.StatusOK, rec.Code)
	require.NotContains(t, rec.Body.String(), "decrypted")
}

func TestOpenAIFailoverPolicy_StreamFailedEventSwitchesAccount(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newAstraProCapturedUpstream(upstreamFailedStream("Encrypted function output content could not be decrypted or decoded."), preludeUpstreamStream())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream)
	enableFailoverPolicy(t, h, []int64{failoverTestGroupID})
	c, rec := newAstraProFailoverContext(t, preludeStreamRequestBody)

	h.Responses(c)

	_, accountIDs, _ := upstream.snapshot()
	require.ElementsMatch(t, []int64{1, 2}, accountIDs)
	require.Contains(t, rec.Body.String(), "event: response.completed")
	require.NotContains(t, rec.Body.String(), "decrypted")
}

func TestOpenAIFailoverPolicy_GroupNotEnabledKeepsCurrentBehavior(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newAstraProCapturedUpstream(upstreamJSON(http.StatusBadRequest, encryptedErrorBody), astra200())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream)
	enableFailoverPolicy(t, h, []int64{failoverTestGroupID + 1})
	c, rec := newAstraProFailoverContext(t, astraProRequestBody)

	h.Responses(c)

	_, accountIDs, _ := upstream.snapshot()
	require.Equal(t, []int64{accountIDs[0]}, accountIDs)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestOpenAIFailoverPolicy_ExhaustedReturnsRealError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newAstraProCapturedUpstream(upstreamJSON(http.StatusBadRequest, encryptedErrorBody), upstreamJSON(http.StatusBadRequest, encryptedErrorBody))
	h := newOpenAIResponsesFailoverTestHandler(t, upstream)
	enableFailoverPolicy(t, h, []int64{failoverTestGroupID})
	c, rec := newAstraProFailoverContext(t, astraProRequestBody)

	h.Responses(c)

	_, accountIDs, _ := upstream.snapshot()
	require.ElementsMatch(t, []int64{1, 2}, accountIDs, "each account once: rules never retry on the same account")
	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, gjson.GetBytes(rec.Body.Bytes(), "error.message").String(), "could not be decrypted")
	require.Equal(t, "thinking_signature_invalid", gjson.GetBytes(rec.Body.Bytes(), "error.code").String())
}

func TestOpenAIFailoverPolicy_MaxRuleSwitchesCapsAttempts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newAstraProCapturedUpstream(
		upstreamJSON(http.StatusBadRequest, encryptedErrorBody),
		upstreamJSON(http.StatusBadRequest, encryptedErrorBody),
		astra200(),
	)
	third := service.Account{
		ID: 3, Name: "responses-account-3", Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Status: service.StatusActive, Schedulable: true, Priority: 2, Credentials: map[string]any{"access_token": "token-3"},
	}
	h := newOpenAIResponsesFailoverTestHandler(t, upstream, third)
	enableFailoverPolicy(t, h, []int64{failoverTestGroupID}, func(cfg *service.OpenAIFailoverConfig) { cfg.MaxRuleSwitches = 1 })
	c, rec := newAstraProFailoverContext(t, astraProRequestBody)

	h.Responses(c)

	_, accountIDs, _ := upstream.snapshot()
	require.Len(t, accountIDs, 2, "one switch, then the third account is not tried")
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestOpenAIFailoverPolicy_BrokenSettingLeavesGroupsOff(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newAstraProCapturedUpstream(upstreamJSON(http.StatusBadRequest, encryptedErrorBody), astra200())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream)
	enableFailoverPolicy(t, h, []int64{failoverTestGroupID}, func(cfg *service.OpenAIFailoverConfig) { cfg.MaxRuleSwitches = 0 })
	c, rec := newAstraProFailoverContext(t, astraProRequestBody)

	h.Responses(c)

	_, accountIDs, _ := upstream.snapshot()
	require.Len(t, accountIDs, 1)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestOpenAIFailoverPolicy_ReturnRuleShieldsBroaderFailoverRule(t *testing.T) {
	gin.SetMode(gin.TestMode)
	upstream := newAstraProCapturedUpstream(upstreamJSON(http.StatusBadRequest, encryptedErrorBody), astra200())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream)
	enableFailoverPolicy(t, h, []int64{failoverTestGroupID}, func(cfg *service.OpenAIFailoverConfig) {
		cfg.Rules = append([]service.OpenAIFailoverRule{{ID: "keep", Enabled: true, StatusCode: 400, Keywords: []string{"DECRYPTED"}, Action: service.OpenAIFailoverActionReturn}}, cfg.Rules...)
	})
	c, rec := newAstraProFailoverContext(t, astraProRequestBody)

	h.Responses(c)

	_, accountIDs, _ := upstream.snapshot()
	require.Len(t, accountIDs, 1)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestOpenAIFailoverPolicy_ContextWindowErrorCanNotBeOverridden(t *testing.T) {
	gin.SetMode(gin.TestMode)
	body := `{"error":{"type":"invalid_request_error","code":"context_length_exceeded","message":"Your input exceeds the context window of this model."}}`
	upstream := newAstraProCapturedUpstream(upstreamJSON(http.StatusBadRequest, body), astra200())
	h := newOpenAIResponsesFailoverTestHandler(t, upstream)
	enableFailoverPolicy(t, h, []int64{failoverTestGroupID}, func(cfg *service.OpenAIFailoverConfig) {
		cfg.Rules = []service.OpenAIFailoverRule{{ID: "all-400", Enabled: true, StatusCode: 400, Action: service.OpenAIFailoverActionFailover}}
	})
	c, rec := newAstraProFailoverContext(t, astraProRequestBody)

	h.Responses(c)

	_, accountIDs, _ := upstream.snapshot()
	require.Len(t, accountIDs, 1)
	require.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestOpenAIFailoverPolicy_PlaintextEncryptedContentRewrittenBeforeForward(t *testing.T) {
	gin.SetMode(gin.TestMode)
	request := `{"model":"gpt-6-astra","stream":false,"input":[{"type":"agent_message","author":"/root/w","recipient":"/root","content":[{"type":"input_text","text":"Payload:"},{"type":"encrypted_content","encrypted_content":"plain draft"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`

	run := func(groups []int64) []byte {
		upstream := newAstraProCapturedUpstream(astra200())
		h := newOpenAIResponsesFailoverTestHandler(t, upstream)
		enableFailoverPolicy(t, h, groups)
		c, _ := newAstraProFailoverContext(t, request)
		h.Responses(c)
		_, _, bodies := upstream.snapshot()
		require.Len(t, bodies, 1)
		return bodies[0]
	}

	repaired := gjson.GetBytes(run([]int64{failoverTestGroupID}), `input.#(type=="agent_message").content.1`)
	require.Equal(t, "input_text", repaired.Get("type").String())
	require.Equal(t, "plain draft", repaired.Get("text").String())
	require.False(t, repaired.Get("encrypted_content").Exists())

	untouched := gjson.GetBytes(run(nil), `input.#(type=="agent_message").content.1`)
	require.Equal(t, "encrypted_content", untouched.Get("type").String(), "a group that has not opted in keeps its body")
}

type busySlotCache struct {
	fakeConcurrencyCache
	queueFull bool
}

func (c *busySlotCache) AcquireAccountSlot(context.Context, int64, int, string) (bool, error) {
	return false, nil
}

func (c *busySlotCache) IncrementAccountWaitCount(context.Context, int64, int) (bool, error) {
	return !c.queueFull, nil
}

func TestAcquireOpenAIAccountSlot_BusyUnderPolicy(t *testing.T) {
	gin.SetMode(gin.TestMode)
	account := &service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey, Status: service.StatusActive, Schedulable: true, Concurrency: 1}
	groupID := int64(4)
	selection := func() *service.AccountSelectionResult {
		return &service.AccountSelectionResult{
			Account:  account,
			WaitPlan: &service.AccountWaitPlan{AccountID: account.ID, MaxConcurrency: 1, Timeout: 30 * time.Second, MaxWaiting: 2},
		}
	}
	policy := func(t *testing.T) *service.OpenAIFailoverPolicy {
		raw, err := json.Marshal(service.OpenAIFailoverConfig{EnabledGroupIDs: []int64{groupID}, SlotWaitMs: 500, MaxRuleSwitches: 3, MaxSlotSwitches: 5})
		require.NoError(t, err)
		p := service.NewOpenAIFailoverPolicyService(&failoverPolicySettings{raw: string(raw)}, nil, nil, nil).PolicyForGroup(context.Background(), &groupID)
		require.NotNil(t, p)
		return p
	}
	run := func(t *testing.T, cache *busySlotCache, ctx context.Context) (openAISlotAcquireResult, *gin.Context, *httptest.ResponseRecorder, time.Duration) {
		h := &OpenAIGatewayHandler{
			gatewayService:    &service.OpenAIGatewayService{},
			concurrencyHelper: NewConcurrencyHelper(service.NewConcurrencyService(cache), SSEPingFormatClaude, 0),
		}
		rec := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(rec)
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil).WithContext(ctx)
		streamStarted := false
		start := time.Now()
		_, result := h.acquireResponsesAccountSlot(c, &groupID, "", selection(), false, &streamStarted, zap.NewNop())
		return result, c, rec, time.Since(start)
	}

	t.Run("wait timeout asks for a switch after the shorter wait", func(t *testing.T) {
		ctx := service.WithOpenAIFailoverPolicy(context.Background(), policy(t))
		result, c, rec, elapsed := run(t, &busySlotCache{}, ctx)
		require.Equal(t, openAISlotAcquireBusy, result)
		require.Zero(t, rec.Body.Len(), "nothing reaches the client yet")
		require.Less(t, elapsed, 5*time.Second, "the policy's wait replaces the plan's 30s")
		require.Equal(t, gatewayConcurrencyLimitCode, openAISlotBusyErrorFrom(c).code)
	})

	t.Run("full queue asks for a switch", func(t *testing.T) {
		ctx := service.WithOpenAIFailoverPolicy(context.Background(), policy(t))
		result, c, rec, _ := run(t, &busySlotCache{queueFull: true}, ctx)
		require.Equal(t, openAISlotAcquireBusy, result)
		require.Zero(t, rec.Body.Len())
		require.Equal(t, gatewayQueueFullCode, openAISlotBusyErrorFrom(c).code)
	})

	t.Run("client gone is not a switch", func(t *testing.T) {
		ctx, cancel := context.WithCancel(service.WithOpenAIFailoverPolicy(context.Background(), policy(t)))
		cancel()
		result, _, _, _ := run(t, &busySlotCache{}, ctx)
		require.Equal(t, openAISlotAcquireFailed, result)
	})

	t.Run("without a policy the error is written as before", func(t *testing.T) {
		result, _, rec, _ := run(t, &busySlotCache{queueFull: true}, context.Background())
		require.Equal(t, openAISlotAcquireFailed, result)
		require.Equal(t, http.StatusTooManyRequests, rec.Code)
	})
}
