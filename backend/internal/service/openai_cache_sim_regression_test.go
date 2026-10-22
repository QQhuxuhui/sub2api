package service

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/Wei-Shaw/sub2api/internal/pkg/apicompat"
	"github.com/Wei-Shaw/sub2api/internal/pkg/cachesim"
	"github.com/tidwall/gjson"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// HTTP POST -> WS v2 must skip simulation in both streaming modes, preserving
// upstream usage for the client, raw columns, and billing.
func TestOpenAICacheSim_WSV2LeavesRawUsage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	SetOpenAICacheSimWindowStore(&memCacheSimStore{})
	t.Cleanup(func() { SetOpenAICacheSimWindowStore(nil) })

	cases := []struct {
		name        string
		requestTier string
		stream      bool
	}{
		{name: "priority_nonstream", requestTier: "priority", stream: false},
		{name: "fast_stream", requestTier: "fast", stream: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(rec)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			c.Request = c.Request.WithContext(cachesim.WithState(c.Request.Context(), cachesim.New()))
			c.Request.Header.Set("User-Agent", "unit-test-agent/1.0")

			cfg := &config.Config{}
			cfg.Security.URLAllowlist.Enabled = false
			cfg.Security.URLAllowlist.AllowInsecureHTTP = true
			cfg.Gateway.OpenAIWS.Enabled = true
			cfg.Gateway.OpenAIWS.APIKeyEnabled = true
			cfg.Gateway.OpenAIWS.ResponsesWebsocketsV2 = true
			cfg.Gateway.OpenAIWS.MaxConnsPerAccount = 1
			cfg.Gateway.OpenAIWS.MinIdlePerAccount = 0
			cfg.Gateway.OpenAIWS.MaxIdlePerAccount = 1
			cfg.Gateway.OpenAIWS.QueueLimitPerConn = 8
			cfg.Gateway.OpenAIWS.DialTimeoutSeconds = 3
			cfg.Gateway.OpenAIWS.ReadTimeoutSeconds = 5
			cfg.Gateway.OpenAIWS.WriteTimeoutSeconds = 3

			captureConn := &openAIWSCaptureConn{
				events: [][]byte{
					[]byte(`{"type":"response.completed","response":{"id":"resp_tier_v2","model":"gpt-5.5","status":"completed","service_tier":"default","usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000},"output_tokens":1}}}`),
				},
			}
			captureDialer := &openAIWSCaptureDialer{conn: captureConn}
			pool := newOpenAIWSConnPool(cfg)
			pool.setClientDialerForTest(captureDialer)

			svc := &OpenAIGatewayService{
				cfg:              cfg,
				httpUpstream:     &httpUpstreamRecorder{},
				cache:            &stubGatewayCache{},
				openaiWSResolver: NewOpenAIWSProtocolResolver(cfg),
				toolCorrector:    NewCodexToolCorrector(),
				openaiWSPool:     pool,
			}
			account := &Account{
				ID:          5882,
				Name:        "openai-ws-v2-tier",
				Platform:    PlatformOpenAI,
				Type:        AccountTypeAPIKey,
				Status:      StatusActive,
				Schedulable: true,
				Concurrency: 1,
				Credentials: map[string]any{"api_key": "sk-test"},
				Extra:       map[string]any{"responses_websockets_v2_enabled": true, openAICacheSimulationKey: map[string]any{"enabled": true, "lo": 85, "hi": 85}},
			}

			body := []byte(fmt.Sprintf(
				`{"model":"gpt-5.5","stream":%t,"service_tier":%q,"input":[{"type":"input_text","text":"hi"}]}`,
				tc.stream, tc.requestTier,
			))
			result, err := svc.Forward(context.Background(), c, account, body)
			require.NoError(t, err)
			require.NotNil(t, result)
			require.True(t, result.OpenAIWSMode, "must take HTTP POST → forwardOpenAIWSV2, not HTTP fallback")
			require.Equal(t, tc.stream, result.Stream)
			usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
			billing := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			require.NoError(t, billing.RecordUsage(c.Request.Context(), &OpenAIRecordUsageInput{Result: result, APIKey: &APIKey{ID: 10}, User: &User{ID: 20}, Account: account}))
			require.Equal(t, 2000, *usageRepo.lastLog.RawCacheReadTokens, "raw column must preserve upstream cache count")
			require.Contains(t, rec.Body.String(), `"cached_tokens":2000`)
			require.Equal(t, 2000, usageRepo.lastLog.CacheReadTokens)
			require.Zero(t, usageRepo.lastLog.CacheSimCostDelta)
			require.False(t, cachesim.FromContext(c.Request.Context()).Decided())
		})
	}
}

func TestOpenAICacheSim_ImageResponseAndBillingStayRaw(t *testing.T) {
	SetOpenAICacheSimWindowStore(&memCacheSimStore{})
	t.Cleanup(func() { SetOpenAICacheSimWindowStore(nil) })
	c, rec := newRewriteTestContext(t, "gpt-5.4")
	c.Request = c.Request.WithContext(cachesim.WithState(c.Request.Context(), cachesim.New()))
	account := simTestAccount(simEnabledExtra(85, 85))
	body := "data: " + `{"type":"response.completed","response":{"id":"resp_img","model":"gpt-5.4","status":"completed","output":[],"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000,"image_tokens":1000},"output_tokens":7}}}` + "\n\ndata: [DONE]\n\n"
	result, err := newRewriteTestService().handleStreamingResponse(context.Background(), newRewriteTestUpstreamResponse("text/event-stream", body), c, account, time.Now(), "gpt-5.4", "gpt-5.4")
	require.NoError(t, err)
	require.Contains(t, rec.Body.String(), `"cached_tokens":2000`)
	usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	require.NoError(t, svc.RecordUsage(c.Request.Context(), &OpenAIRecordUsageInput{Result: &OpenAIForwardResult{RequestID: "resp_img", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: *result.usage, Duration: time.Second}, APIKey: &APIKey{ID: 10}, User: &User{ID: 20}, Account: account}))
	require.Equal(t, 2000, usageRepo.lastLog.CacheReadTokens, "wire and billing cache counts must agree")
}
func TestOpenAICacheSim_PreservesCreationBudget(t *testing.T) {
	SetOpenAICacheSimWindowStore(&memCacheSimStore{})
	t.Cleanup(func() { SetOpenAICacheSimWindowStore(nil) })
	c, _ := newRewriteTestContext(t, "gpt-5.4")
	c.Request = c.Request.WithContext(cachesim.WithState(c.Request.Context(), cachesim.New()))
	account := simTestAccount(simEnabledExtra(85, 85))
	account.Extra[openAIStripCacheCreationKey] = false
	data := []byte(`{"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000},"cache_creation_input_tokens":5000,"output_tokens":7}}`)
	out := newOpenAIResponseModelRewrite(c, account, "gpt-5.4", "gpt-5.4").applyJSON(data)
	cached := int(gjson.GetBytes(out, "usage.input_tokens_details.cached_tokens").Int())
	creation := int(gjson.GetBytes(out, "usage.cache_creation_input_tokens").Int())
	require.LessOrEqual(t, cached+creation, 10000, "cache read and write must remain disjoint")
}

func TestOpenAICacheSim_TypedImageAndAudioStayRawAfterConversion(t *testing.T) {
	for _, field := range []string{"image_tokens", "audio_tokens"} {
		t.Run(field, func(t *testing.T) {
			c, rec := newRewriteTestContext(t, "gpt-5.4")
			c.Request = c.Request.WithContext(cachesim.WithState(c.Request.Context(), cachesim.New()))
			account := simTestAccount(simEnabledExtra(85, 85))
			rewrite := anthropicUsageResponseRewrite(c, account)
			body := fmt.Sprintf(`{"id":"cc1","choices":[],"usage":{"prompt_tokens":10000,"prompt_tokens_details":{"cached_tokens":2000,"%s":1000},"completion_tokens":7}}`, field)
			svc := newRewriteTestService()
			cc, _, err := svc.readCCUpstreamJSONResponse(c, newRewriteTestUpstreamResponse("application/json", body), writeAnthropicError)
			require.NoError(t, err)
			cc.Usage = rewrite.applyChatUsage(cc.Usage)
			response := apicompat.ChatCompletionsResponseToAnthropic(cc, "gpt-5.4")
			require.NoError(t, rewrite.writeJSON(c, http.StatusOK, response))
			require.Equal(t, int64(2000), gjson.Get(rec.Body.String(), "usage.cache_read_input_tokens").Int())
		})
	}
}

func TestOpenAICacheSim_ImageToolExcludesFollowingUsage(t *testing.T) {
	for _, item := range []string{
		`{"type":"response.output_item.added","item":{"type":"image_generation_call"}}`,
		`{"type":"response.image_generation_call.in_progress"}`,
		`{"type":"response.completed","response":{"output":[{"type":"image_generation_call","usage":{"input_tokens":5}}]}}`,
		`{"response":{"tool_usage":{"image_gen":{"input_tokens":5}}}}`,
	} {
		t.Run(item, func(t *testing.T) {
			c, _ := newRewriteTestContext(t, "gpt-5.4")
			c.Request = c.Request.WithContext(cachesim.WithState(c.Request.Context(), cachesim.New()))
			rw := newOpenAIResponseModelRewrite(c, simTestAccount(simEnabledExtra(85, 85)), "gpt-5.4", "gpt-5.4")
			rw.applySSELine("data: " + item)
			out := rw.applyJSON([]byte(`{"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000}}}`))
			require.Equal(t, int64(2000), gjson.GetBytes(out, "usage.input_tokens_details.cached_tokens").Int())
		})
	}
}

func TestOpenAICacheSim_BufferedImageTerminalWithoutDelimiter(t *testing.T) {
	for _, suffix := range []string{"", "\n\n"} {
		t.Run(fmt.Sprintf("suffix=%q", suffix), func(t *testing.T) {
			c, rec := newRewriteTestContext(t, "gpt-5.4")
			c.Request = c.Request.WithContext(cachesim.WithState(c.Request.Context(), cachesim.New()))
			account := simTestAccount(simEnabledExtra(85, 85))
			body := `data: {"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"image_generation_call"}],"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000},"output_tokens":7}}}` + suffix
			_, err := newRewriteTestService().handleAnthropicBufferedStreamingResponse(newRewriteTestUpstreamResponse("text/event-stream", body), c, account, "gpt-5.4", "gpt-5.4", "gpt-5.4", time.Now())
			require.NoError(t, err)
			require.Equal(t, int64(2000), gjson.Get(rec.Body.String(), "usage.cache_read_input_tokens").Int())
		})
	}
}

func TestOpenAICacheSim_CostDeltaUsesCustomerCharge(t *testing.T) {
	for _, rate := range []float64{0, 0.5, 1.1} {
		t.Run(fmt.Sprint(rate), func(t *testing.T) {
			repo := &openAIRecordUsageLogRepoStub{inserted: true}
			svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
			svc.cfg.Default.RateMultiplier = rate
			account := simTestAccount(simEnabledExtra(85, 85))
			state := cachesim.New()
			state.Decide(10000, 2000, func(int, int) int { return 8448 })
			require.NoError(t, svc.RecordUsage(cachesim.WithState(context.Background(), state), &OpenAIRecordUsageInput{Result: &OpenAIForwardResult{RequestID: "delta", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 2000, OutputTokens: 7}}, APIKey: &APIKey{ID: 10}, User: &User{ID: 20}, Account: account}))
			raw, err := svc.billingService.CalculateCost("gpt-5.4", UsageTokens{InputTokens: 8000, CacheReadTokens: 2000, OutputTokens: 7}, rate)
			require.NoError(t, err)
			require.InDelta(t, raw.ActualCost-repo.lastLog.ActualCost, repo.lastLog.CacheSimCostDelta, 1e-10)
		})
	}
}

func TestOpenAICacheSim_TypedCreationBudget(t *testing.T) {
	for _, shape := range []string{"responses", "chat", "anthropic"} {
		t.Run(shape, func(t *testing.T) {
			c, _ := newRewriteTestContext(t, "gpt-5.4")
			c.Request = c.Request.WithContext(cachesim.WithState(c.Request.Context(), cachesim.New()))
			account := simTestAccount(simEnabledExtra(85, 85))
			account.Extra[openAIStripCacheCreationKey] = false
			rw := cacheWriteResponseRewrite(c, account)
			cached := 0
			switch shape {
			case "responses":
				var u apicompat.ResponsesUsage
				require.NoError(t, json.Unmarshal([]byte(`{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000,"cache_write_tokens":5000}}`), &u))
				cached = rw.applyResponsesUsage(&u).InputTokensDetails.CachedTokens
			case "chat":
				u := &apicompat.ChatUsage{PromptTokens: 10000, PromptTokensDetails: &apicompat.ChatTokenDetails{CachedTokens: 2000, CacheWriteTokens: 5000}}
				cached = rw.applyChatUsage(u).PromptTokensDetails.CachedTokens
			case "anthropic":
				rw.anthropicShape = true
				out := rw.applyJSON([]byte(`{"usage":{"input_tokens":3000,"cache_read_input_tokens":2000,"cache_creation_input_tokens":5000}}`))
				cached = int(gjson.GetBytes(out, "usage.cache_read_input_tokens").Int())
				require.Equal(t, int64(10000), gjson.GetBytes(out, "usage.input_tokens").Int()+int64(cached)+5000)
			}
			require.Greater(t, cached, 2000)
			require.LessOrEqual(t, cached+5000, 10000)
			require.Zero(t, cached%128)
		})
	}
}

func TestOpenAICacheSim_FreeFastDeltaUsesStandardCharge(t *testing.T) {
	repo := &openAIRecordUsageLogRepoStub{inserted: true}
	svc := newOpenAIRecordUsageServiceForTest(repo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
	svc.resolver = NewModelPricingResolver(nil, svc.billingService)
	inputPrice, outputPrice, cachePrice, fastMultiplier := 0.001, 0.002, 0.0001, 3.0
	groupID := int64(77)
	apiKey := &APIKey{ID: 10, GroupID: &groupID, Group: &Group{ID: groupID, Platform: PlatformOpenAI, Status: StatusActive, Hydrated: true, RateMultiplier: 0.5, FreeOpenAIFast: true, ModelPricing: []ChannelModelPricing{{Models: []string{"gpt-5.6-sol"}, BillingMode: BillingModeToken, InputPrice: &inputPrice, OutputPrice: &outputPrice, CacheReadPrice: &cachePrice, FastMultiplier: &fastMultiplier}}}}
	account := simTestAccount(simEnabledExtra(85, 85))
	account.Type = AccountTypeOAuth
	tier := "priority"
	state := cachesim.New()
	state.Decide(10000, 2000, func(int, int) int { return 8448 })
	require.NoError(t, svc.RecordUsage(cachesim.WithState(context.Background(), state), &OpenAIRecordUsageInput{Result: &OpenAIForwardResult{RequestID: "freefast", Model: "gpt-5.6-sol", UpstreamModel: "gpt-5.6-sol", ServiceTier: &tier, Usage: OpenAIUsage{InputTokens: 10000, CacheReadInputTokens: 2000, OutputTokens: 7}}, APIKey: apiKey, User: &User{ID: 20}, Account: account}))
	require.InDelta(t, (8000*inputPrice+2000*cachePrice+7*outputPrice)*0.5-repo.lastLog.ActualCost, repo.lastLog.CacheSimCostDelta, 1e-10)
}

func TestOpenAICacheSim_ChatTopLevelCreationBudget(t *testing.T) {
	for _, field := range []string{"cache_creation_input_tokens", "cache_write_tokens", "cache_write_input_tokens", "cache_creation_tokens"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", field, stream), func(t *testing.T) {
				c, _ := newRewriteTestContext(t, "gpt-5.4")
				c.Request = c.Request.WithContext(cachesim.WithState(c.Request.Context(), cachesim.New()))
				account := simTestAccount(simEnabledExtra(85, 85))
				account.Extra[openAIStripCacheCreationKey] = false
				rw := cacheWriteResponseRewrite(c, account)
				body := fmt.Sprintf(`{"id":"cc1","choices":[],"usage":{"prompt_tokens":10000,"prompt_tokens_details":{"cached_tokens":2000},"%s":5000,"completion_tokens":7}}`, field)
				svc := newRewriteTestService()
				cached := 0
				if stream {
					scan := svc.scanCCStream(c, newRewriteTestUpstreamResponse("text/event-stream", "data: "+body+"\n\ndata: [DONE]\n\n"), "test", "cc1", time.Now(), func(chunk *apicompat.ChatCompletionsChunk) {
						cached = rw.applyChatUsage(chunk.Usage).PromptTokensDetails.CachedTokens
					})
					require.NoError(t, scan.Err)
					require.Equal(t, 5000, scan.Usage.CacheCreationInputTokens)
				} else {
					cc, usage, err := svc.readCCUpstreamJSONResponse(c, newRewriteTestUpstreamResponse("application/json", body), writeAnthropicError)
					require.NoError(t, err)
					require.Equal(t, 5000, usage.CacheCreationInputTokens)
					cached = rw.applyChatUsage(cc.Usage).PromptTokensDetails.CachedTokens
				}
				require.Greater(t, cached, 2000)
				require.LessOrEqual(t, cached+5000, 10000)
			})
		}
	}
}

func TestOpenAICacheSim_SSEToJSONObservesIntermediateImages(t *testing.T) {
	for _, passthrough := range []bool{false, true} {
		t.Run(fmt.Sprint(passthrough), func(t *testing.T) {
			c, rec := newRewriteTestContext(t, "gpt-5.4")
			c.Request = c.Request.WithContext(cachesim.WithState(c.Request.Context(), cachesim.New()))
			account := simTestAccount(simEnabledExtra(85, 85))
			body := "data: " + `{"type":"response.output_item.done","item":{"type":"image_generation_call","id":"img1","status":"completed","result":"aW1n"}}` + "\n\ndata: " + `{"type":"response.completed","response":{"id":"r1","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"done"}]}],"usage":{"input_tokens":10000,"input_tokens_details":{"cached_tokens":2000},"output_tokens":7}}}` + "\n\n"
			svc := newRewriteTestService()
			resp := newRewriteTestUpstreamResponse("text/event-stream", body)
			if passthrough {
				result, err := svc.handlePassthroughSSEToJSON(resp, c, account, []byte(body), "gpt-5.4", "gpt-5.4")
				require.NoError(t, err)
				require.Equal(t, 1, result.imageCount)
			} else {
				result, err := svc.handleSSEToJSON(resp, c, account, []byte(body), "gpt-5.4", "gpt-5.4")
				require.NoError(t, err)
				require.Equal(t, 1, result.imageCount)
			}
			require.Equal(t, int64(2000), gjson.Get(rec.Body.String(), "usage.input_tokens_details.cached_tokens").Int())
		})
	}
}
