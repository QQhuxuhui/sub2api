package service

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestAccount_StripsOpenAICacheCreation(t *testing.T) {
	require.True(t, (&Account{Platform: PlatformOpenAI}).StripsOpenAICacheCreation(), "缺省开启")
	require.True(t, (&Account{Platform: PlatformOpenAI, Extra: map[string]any{}}).StripsOpenAICacheCreation())
	require.True(t, (&Account{Platform: PlatformOpenAI, Extra: map[string]any{openAIStripCacheCreationKey: true}}).StripsOpenAICacheCreation())
	require.False(t, (&Account{Platform: PlatformOpenAI, Extra: map[string]any{openAIStripCacheCreationKey: false}}).StripsOpenAICacheCreation(), "只有显式 false 才关闭")
	require.False(t, (&Account{Platform: PlatformAnthropic}).StripsOpenAICacheCreation(), "非 OpenAI 平台不参与")
	var nilAccount *Account
	require.False(t, nilAccount.StripsOpenAICacheCreation())
}

func TestValidateOpenAIStripCacheCreationExtra(t *testing.T) {
	require.NoError(t, ValidateOpenAIStripCacheCreationExtra(PlatformOpenAI, nil))
	require.NoError(t, ValidateOpenAIStripCacheCreationExtra(PlatformOpenAI, map[string]any{openAIStripCacheCreationKey: false}))
	require.Error(t, ValidateOpenAIStripCacheCreationExtra(PlatformOpenAI, map[string]any{openAIStripCacheCreationKey: "false"}))
	require.NoError(t, ValidateOpenAIStripCacheCreationExtra(PlatformAnthropic, map[string]any{openAIStripCacheCreationKey: "false"}))
	// 长上下文校验入口顺带校验本开关，所有 Create/Update/Bulk 路径都经过它。
	require.Error(t, ValidateOpenAILongContextBillingExtra(PlatformOpenAI, map[string]any{openAIStripCacheCreationKey: 1}))
}

const cacheWriteCompletedEvent = `data: {"type":"response.completed","response":{"id":"resp_1","model":"gpt-6.1-sol","status":"completed","output":[],"usage":{"input_tokens":3,"input_tokens_details":{"cached_tokens":95374,"cache_write_tokens":2677},"cache_creation_input_tokens":2677,"output_tokens":212,"total_tokens":98266}}}`

func TestOpenAIResponseRewrite_StripsCacheWriteUsageFields(t *testing.T) {
	c, _ := newRewriteTestContext(t, "gpt-6.1-sol")
	rw := newOpenAIResponseModelRewrite(c, openAIRewriteTestAccount(AccountTypeAPIKey), "gpt-6.1-sol", "gpt-6.1-sol")
	require.True(t, rw.stripCacheWrite)
	require.True(t, rw.active())

	out := strings.TrimPrefix(rw.applySSELine(cacheWriteCompletedEvent), "data: ")
	usage := gjson.Get(out, "response.usage")
	require.False(t, usage.Get("input_tokens_details.cache_write_tokens").Exists())
	require.False(t, usage.Get("cache_creation_input_tokens").Exists())
	require.Equal(t, int64(95374), usage.Get("input_tokens_details.cached_tokens").Int(), "缓存读保留")
	require.Equal(t, int64(3), usage.Get("input_tokens").Int())
	require.Equal(t, int64(212), usage.Get("output_tokens").Int())

	// Chat Completions 形态（prompt_tokens_details）与顶层 usage。
	chat := []byte(`{"id":"c1","model":"gpt-6.1-sol","usage":{"prompt_tokens":100,"prompt_tokens_details":{"cached_tokens":80,"cache_creation_tokens":20},"cache_write_input_tokens":20,"completion_tokens":5}}`)
	chatOut := rw.forcedOnly().applyJSON(chat)
	require.False(t, gjson.GetBytes(chatOut, "usage.prompt_tokens_details.cache_creation_tokens").Exists())
	require.False(t, gjson.GetBytes(chatOut, "usage.cache_write_input_tokens").Exists())
	require.Equal(t, int64(80), gjson.GetBytes(chatOut, "usage.prompt_tokens_details.cached_tokens").Int())

	// WS 帧：response.completed 走同一入口。
	frame := []byte(`{"type":"response.completed","response":{"model":"gpt-6.1-sol","usage":{"input_tokens":1,"input_tokens_details":{"cache_creation_tokens":9}}}}`)
	require.True(t, rw.appliesToWSEvent("response.completed"))
	require.True(t, rw.mayApplyBytes(frame))
	require.False(t, gjson.GetBytes(rw.applyJSON(frame), "response.usage.input_tokens_details.cache_creation_tokens").Exists())
}

func TestOpenAIResponseRewrite_StripCacheWriteIsOptInByAccountAndCheap(t *testing.T) {
	c, _ := newRewriteTestContext(t, "gpt-6.1-sol")
	off := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{openAIStripCacheCreationKey: false}}
	rw := newOpenAIResponseModelRewrite(c, off, "gpt-6.1-sol", "gpt-6.1-sol")
	require.False(t, rw.stripCacheWrite)
	require.Equal(t, cacheWriteCompletedEvent, rw.applySSELine(cacheWriteCompletedEvent), "账号关掉开关时原样透传")

	other := newOpenAIResponseModelRewrite(c, &Account{ID: 2, Platform: PlatformGrok}, "grok-4.5", "grok-4.5")
	require.False(t, other.stripCacheWrite, "非 OpenAI 平台不参与")

	on := newOpenAIResponseModelRewrite(c, openAIRewriteTestAccount(AccountTypeOAuth), "gpt-6.1-sol", "gpt-6.1-sol")
	plain := []byte(`{"type":"response.completed","response":{"model":"gpt-6.1-sol","usage":{"input_tokens":1,"input_tokens_details":{"cached_tokens":1}}}}`)
	got := on.applyJSON(plain)
	require.Equal(t, string(plain), string(got))
	require.Same(t, &plain[0], &got[0], "没有缓存写入字段的响应返回同一切片，不做 JSON 改写")
	delta := `data: {"type":"response.output_text.delta","delta":"cache_write"}`
	require.Equal(t, delta, on.applySSELine(delta), "正文里出现同名词只会触发预检，字段不存在就不改")

	// 没有客户端模型（不改 model）时，仅凭开关也要工作。
	noModel := newOpenAIResponseModelRewrite(nil, openAIRewriteTestAccount(AccountTypeOAuth), "", "")
	require.True(t, noModel.active())
	require.False(t, noModel.rewritesModel())
	out := noModel.applyJSON([]byte(`{"usage":{"cache_creation_tokens":5,"output_tokens":1}}`))
	require.False(t, gjson.GetBytes(out, "usage.cache_creation_tokens").Exists())
	require.Equal(t, int64(1), gjson.GetBytes(out, "usage.output_tokens").Int())
}

func TestHandleStreamingResponse_StripsCacheWriteFromClientStream(t *testing.T) {
	c, rec := newRewriteTestContext(t, "gpt-6.1-sol")
	body := "event: response.completed\n" + cacheWriteCompletedEvent + "\n\ndata: [DONE]\n\n"
	resp := newRewriteTestUpstreamResponse("text/event-stream", body)
	_, err := newRewriteTestService().handleStreamingResponse(context.Background(), resp, c, openAIRewriteTestAccount(AccountTypeAPIKey), time.Now(), "gpt-6.1-sol", "gpt-6.1-sol")
	require.NoError(t, err)
	client := rec.Body.String()
	require.NotContains(t, client, "cache_write_tokens")
	require.NotContains(t, client, "cache_creation_input_tokens")
	require.Contains(t, client, `"cached_tokens":95374`)
}

// 账号开关开着：按 Anthropic 口径回报的 cache_creation 既不记入用量也不计费；
// 关掉则照旧按缓存写入价计费（这正是 llmgw / yjapi 这类中转此前多收的部分）。
func TestOpenAIGatewayServiceRecordUsage_StripsCacheCreationByDefault(t *testing.T) {
	relayUsage := OpenAIUsage{InputTokens: 3, CacheReadInputTokens: 95374, CacheCreationInputTokens: 2677, OutputTokens: 212}
	run := func(t *testing.T, account *Account) (*UsageLog, *OpenAIGatewayService) {
		usageRepo := &openAIRecordUsageLogRepoStub{inserted: true}
		svc := newOpenAIRecordUsageServiceForTest(usageRepo, &openAIRecordUsageUserRepoStub{}, &openAIRecordUsageSubRepoStub{}, nil)
		usage := relayUsage
		err := svc.RecordUsage(context.Background(), &OpenAIRecordUsageInput{
			Result:  &OpenAIForwardResult{RequestID: "resp_cw", Model: "gpt-5.4", UpstreamModel: "gpt-5.4", Usage: usage, Duration: time.Second},
			APIKey:  &APIKey{ID: 10},
			User:    &User{ID: 20},
			Account: account,
		})
		require.NoError(t, err)
		require.NotNil(t, usageRepo.lastLog)
		return usageRepo.lastLog, svc
	}

	t.Run("default on", func(t *testing.T) {
		log, svc := run(t, &Account{ID: 30, Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
		require.Zero(t, log.CacheCreationTokens)
		require.Zero(t, log.CacheCreationCost)
		require.Equal(t, 95374, log.CacheReadTokens)
		require.Equal(t, 212, log.OutputTokens)
		// OpenAI 口径 input_tokens 含缓存读；中转按 Anthropic 口径报的 3 个输入扣除缓存读后归零（既有行为）。
		expected, err := svc.billingService.CalculateCost("gpt-5.4", UsageTokens{InputTokens: 0, CacheReadTokens: 95374, OutputTokens: 212}, 1.1)
		require.NoError(t, err)
		require.InDelta(t, expected.ActualCost, log.ActualCost, 1e-12)
	})

	t.Run("explicitly off", func(t *testing.T) {
		onLog, _ := run(t, &Account{ID: 30, Platform: PlatformOpenAI, Type: AccountTypeAPIKey})
		log, _ := run(t, &Account{ID: 30, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Extra: map[string]any{openAIStripCacheCreationKey: false}})
		require.Equal(t, 2677, log.CacheCreationTokens)
		require.Greater(t, log.CacheCreationCost, 0.0, "关掉开关就回到原来的缓存写入计费")
		require.Greater(t, log.ActualCost, onLog.ActualCost)
	})

	t.Run("non openai platform untouched", func(t *testing.T) {
		log, _ := run(t, &Account{ID: 31, Platform: PlatformGrok, Type: AccountTypeAPIKey})
		require.Equal(t, 2677, log.CacheCreationTokens)
	})
}
