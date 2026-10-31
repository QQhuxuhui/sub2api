//go:build unit

package service

import (
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func failoverRule(status int, action string, keywords ...string) OpenAIFailoverRule {
	return OpenAIFailoverRule{ID: "r", Enabled: true, StatusCode: status, Keywords: keywords, Action: action}
}

func TestOpenAIFailoverPolicyMatch(t *testing.T) {
	policy := newOpenAIFailoverPolicy(OpenAIFailoverConfig{Rules: []OpenAIFailoverRule{
		{ID: "off", Enabled: false, Keywords: []string{"boom"}, Action: OpenAIFailoverActionFailover},
		{ID: "keep", Enabled: true, Keywords: []string{"must contain the word"}, Action: OpenAIFailoverActionReturn},
		{ID: "any", Enabled: true, Keywords: []string{"Could Not Be Verified", "continuation"}, Action: OpenAIFailoverActionFailover},
		{ID: "only-400", Enabled: true, StatusCode: 400, Keywords: []string{"invalid parameters"}, Action: OpenAIFailoverActionFailover},
		{ID: "only-status", Enabled: true, StatusCode: 408, Action: OpenAIFailoverActionFailover},
	}})

	require.Equal(t, "any", policy.match(400, "item could not be verified"), "case insensitive")
	require.Equal(t, "any", policy.match(0, "encrypted continuation context"), "no status: streams too")
	require.Equal(t, "only-400", policy.match(400, "Invalid parameters. Please modify"))
	require.Empty(t, policy.match(0, "Invalid parameters"), "a set status never matches a stream failure")
	require.Equal(t, "only-status", policy.match(408, "anything"))
	require.Empty(t, policy.match(400, "boom"), "disabled rule")
	require.Empty(t, policy.match(400, "must contain the word json, could not be verified"), "first match decides")
	require.Empty(t, (*OpenAIFailoverPolicy)(nil).match(400, "x"))
}

func TestNormalizeOpenAIFailoverConfig(t *testing.T) {
	base := defaultOpenAIFailoverConfig()

	normalized, err := NormalizeOpenAIFailoverConfig(base)
	require.NoError(t, err)
	require.Len(t, normalized.Rules, len(DefaultOpenAIFailoverRules()))

	for name, change := range map[string]func(*OpenAIFailoverConfig){
		"wait too short": func(c *OpenAIFailoverConfig) { c.SlotWaitMs = 1 },
		"zero rule cap":  func(c *OpenAIFailoverConfig) { c.MaxRuleSwitches = 0 },
		"negative slot":  func(c *OpenAIFailoverConfig) { c.MaxSlotSwitches = -1 },
		"bad group":      func(c *OpenAIFailoverConfig) { c.EnabledGroupIDs = []int64{0} },
		"status 500": func(c *OpenAIFailoverConfig) {
			c.Rules = []OpenAIFailoverRule{failoverRule(500, OpenAIFailoverActionFailover, "x")}
		},
		"bad action": func(c *OpenAIFailoverConfig) { c.Rules = []OpenAIFailoverRule{failoverRule(400, "ignore", "x")} },
		"empty condition": func(c *OpenAIFailoverConfig) {
			c.Rules = []OpenAIFailoverRule{failoverRule(0, OpenAIFailoverActionFailover, "  ")}
		},
		"duplicate id": func(c *OpenAIFailoverConfig) {
			c.Rules = []OpenAIFailoverRule{failoverRule(400, OpenAIFailoverActionFailover), failoverRule(404, OpenAIFailoverActionFailover)}
		},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := base
			change(&cfg)
			_, err := NormalizeOpenAIFailoverConfig(cfg)
			require.Error(t, err)
		})
	}

	cfg := base
	cfg.EnabledGroupIDs = []int64{4, 4, 3}
	cfg.Rules = []OpenAIFailoverRule{{Enabled: true, Keywords: []string{" a ", ""}, Action: OpenAIFailoverActionFailover}}
	normalized, err = NormalizeOpenAIFailoverConfig(cfg)
	require.NoError(t, err)
	require.Equal(t, []int64{4, 3}, normalized.EnabledGroupIDs)
	require.Equal(t, []string{"a"}, normalized.Rules[0].Keywords)
	require.NotEmpty(t, normalized.Rules[0].ID)
}

func TestDefaultOpenAIFailoverRulesCoverObservedErrors(t *testing.T) {
	policy := newOpenAIFailoverPolicy(defaultOpenAIFailoverConfig())
	for _, c := range []struct {
		status int
		text   string
		rule   string
	}{
		{0, "Encrypted function output content could not be decrypted or decoded.", "encrypted-content"},
		{400, "The encrypted content for item amsg_x could not be verified. Reason: encrypted content hydration failed", "encrypted-content"},
		{400, "The upstream could not validate encrypted continuation context. Rebuild the conversation", "encrypted-content"},
		{400, "stream error: stream disconnected before completion: stream closed", "stream-disconnected"},
		{400, "The ChatGPT user has reached their subscription sharing usage limit", "sharing-usage-limit"},
		{400, "Unsupported parameter: enable_thinking", "enable-thinking"},
		{400, "Invalid parameters. Please modify and try again.", "invalid-parameters"},
		{400, "x-openai-internal-codex-responses-lite requires reasoning.context", "responses-lite"},
		{400, "Upstream request failed", "upstream-request-failed"},
		{408, "request body read timed out", "http-408"},
	} {
		require.Equal(t, c.rule, policy.match(c.status, c.text), c.text)
	}
	for _, text := range []string{
		"Your input exceeds the context window of this model",
		"Response input messages must contain the word 'json' in some form",
		"The image you provided requires 33919 patches after processing",
		"Invalid prompt: your prompt was flagged as potentially violating our usage policy",
		"This content was flagged for possible biological risk",
	} {
		require.Empty(t, policy.match(400, text), text)
	}
	require.Empty(t, policy.match(404, "Item with id 'ig_1' not found"), "404 stays with the existing logic")
}

func TestRewriteOpenAIPlaintextEncryptedContent(t *testing.T) {
	body := []byte(`{"input":[
		{"type":"message","role":"user","content":[{"type":"input_text","text":"encrypted_content agent_message"}]},
		{"type":"agent_message","content":[{"type":"input_text","text":"head"},{"type":"encrypted_content","encrypted_content":"plain \"quoted\" 中文"}]},
		{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"gAAAAABcipher"}]},
		{"type":"reasoning","encrypted_content":"not-touched"}
	]}`)
	out, count := RewriteOpenAIPlaintextEncryptedContent(body)
	require.Equal(t, 1, count)
	require.True(t, gjson.ValidBytes(out))
	part := gjson.GetBytes(out, "input.1.content.1")
	require.Equal(t, "input_text", part.Get("type").String())
	require.Equal(t, `plain "quoted" 中文`, part.Get("text").String())
	require.False(t, part.Get("encrypted_content").Exists())
	require.Equal(t, "gAAAAABcipher", gjson.GetBytes(out, "input.2.content.0.encrypted_content").String())
	require.Equal(t, "not-touched", gjson.GetBytes(out, "input.3.encrypted_content").String())

	untouched := []byte(`{"input":[{"type":"agent_message","content":[{"type":"encrypted_content","encrypted_content":"gAAAAAB"},{"type":"encrypted_content","encrypted_content":""}]}]}`)
	same, count := RewriteOpenAIPlaintextEncryptedContent(untouched)
	require.Zero(t, count)
	require.Equal(t, untouched, same)
}

func TestOpenAIRequestDeclaresEncryptedArgs(t *testing.T) {
	require.True(t, OpenAIRequestDeclaresEncryptedArgs([]byte(`{"message":{"type":"string","encrypted":true}}`)))
	require.True(t, OpenAIRequestDeclaresEncryptedArgs([]byte(`{"encrypted": true}`)))
	require.False(t, OpenAIRequestDeclaresEncryptedArgs([]byte(`{"encrypted":false}`)))
}

func TestEligibilityExcludesMarkedAccountOnlyWhenRequired(t *testing.T) {
	marked := &Account{ID: 1, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true, Extra: map[string]any{OpenAINoEncryptedArgsExtraKey: true}}
	plain := &Account{ID: 2, Platform: PlatformOpenAI, Type: AccountTypeAPIKey, Status: StatusActive, Schedulable: true}
	required := WithOpenAIEncryptedArgsRequired(context.Background())

	require.Equal(t, "encrypted_args_unsupported", openAICompatibleAccountEligibilityFailureReasonBeforeProfit(required, marked, PlatformOpenAI, "gpt-6-astra", false, ""))
	require.NotEqual(t, "encrypted_args_unsupported", openAICompatibleAccountEligibilityFailureReasonBeforeProfit(context.Background(), marked, PlatformOpenAI, "gpt-6-astra", false, ""))
	require.NotEqual(t, "encrypted_args_unsupported", openAICompatibleAccountEligibilityFailureReasonBeforeProfit(required, plain, PlatformOpenAI, "gpt-6-astra", false, ""))
}

func TestParseOpenAIEncryptedArgsCheckStream(t *testing.T) {
	cipher := "data: {\"type\":\"response.output_item.done\",\"item\":{\"type\":\"function_call\",\"arguments\":\"{\\\"target\\\":\\\"a\\\",\\\"message\\\":\\\"gAAAAABx\\\"}\"}}\n\n"
	message, err := parseOpenAIEncryptedArgsCheckStream(strings.NewReader(cipher))
	require.NoError(t, err)
	require.Equal(t, "gAAAAABx", message)

	_, err = parseOpenAIEncryptedArgsCheckStream(strings.NewReader("data: {\"type\":\"response.failed\",\"response\":{\"error\":{\"message\":\"nope\"}}}\n\n"))
	require.ErrorContains(t, err, "nope")

	_, err = parseOpenAIEncryptedArgsCheckStream(strings.NewReader("data: {\"type\":\"response.completed\"}\n\n"))
	require.Error(t, err)

	require.Equal(t, OpenAIEncryptedArgsSupported, openAIEncryptedArgsVerdict(3, 0))
	require.Equal(t, OpenAIEncryptedArgsUnsupported, openAIEncryptedArgsVerdict(0, 3))
	require.Equal(t, OpenAIEncryptedArgsMixed, openAIEncryptedArgsVerdict(2, 1))
	require.Equal(t, OpenAIEncryptedArgsUnknown, openAIEncryptedArgsVerdict(0, 0))
}

func TestCheckEncryptedArgsDeclaresEncryptedParameter(t *testing.T) {
	body, err := openAIEncryptedArgsCheckBody("gpt-6-astra")
	require.NoError(t, err)
	require.True(t, OpenAIRequestDeclaresEncryptedArgs(body))
	require.Equal(t, "required", gjson.GetBytes(body, "tool_choice").String())
}
