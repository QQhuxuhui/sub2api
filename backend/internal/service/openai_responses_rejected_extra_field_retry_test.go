package service

import (
	"context"
	"net/http"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyRemovesEnableThinking(t *testing.T) {
	body := []byte(`{"model":"gpt-6.2-sol","stream":true,"enable_thinking":true,"max_output_tokens":100,"input":"hi"}`)
	for name, response := range map[string]string{
		"error message":             `{"error":{"message":"Unsupported parameter: enable_thinking","type":"invalid_request_error"}}`,
		"detail":                    `{"detail":"Unsupported parameter: enable_thinking"}`,
		"blank message then detail": `{"error":{"message":" "},"detail":"Unsupported parameter: enable_thinking"}`,
		"top level message":         `{"message":"Unsupported parameter: enable_thinking"}`,
		"quoted with full stop":     `{"error":{"message":"Unsupported parameter: 'enable_thinking'."}}`,
		"unquoted with full stop":   `{"error":{"message":"Unknown parameter: enable_thinking."}}`,
		"is separator":              `{"error":{"message":"Unknown parameter is enable_thinking"}}`,
		"matching param":            `{"error":{"message":"Unsupported parameter: enable_thinking","param":"enable_thinking","code":"unsupported_parameter"}}`,
		"wrapped detail":            `{"error":{"message":"{\"detail\":\"Unsupported parameter: enable_thinking\"}"}}`,
		"wrapped error with param":  `{"error":{"message":"{\"error\":{\"message\":\"Unsupported parameter: enable_thinking\",\"param\":\"enable_thinking\"}}"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			retryBody, reason, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, []byte(response))
			require.NoError(t, err)
			require.True(t, changed)
			require.Equal(t, "enable_thinking parameter rejection", reason)
			require.JSONEq(t, `{"model":"gpt-6.2-sol","stream":true,"max_output_tokens":100,"input":"hi"}`, string(retryBody))
		})
	}
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyKeepsEnableThinking(t *testing.T) {
	body := []byte(`{"model":"gpt-6.2-sol","enable_thinking":true,"ENABLE_THINKING":true,"isenable_thinking":true,"max_output_tokens":100,"prompt_cache_breakpoint":true,"input":"hi"}`)
	for name, response := range map[string]string{
		"value restriction":        `{"error":{"code":"unsupported_parameter","param":"enable_thinking","message":"parameter.enable_thinking must be set to false for non-streaming calls"}}`,
		"dotted name":              `{"error":{"message":"Unsupported parameter: 'enable_thinking.mode'"}}`,
		"slashed name":             `{"error":{"message":"Unsupported parameter: 'enable_thinking/mode'"}}`,
		"dot inside quotes":        `{"error":{"message":"Unsupported parameter: 'enable_thinking.'"}}`,
		"indexed name":             `{"error":{"message":"Unsupported parameter: enable_thinking[0]"}}`,
		"unquoted slashed name":    `{"error":{"message":"Unsupported parameter: enable_thinking/mode"}}`,
		"upper case name":          `{"error":{"message":"Unsupported parameter: ENABLE_THINKING"}}`,
		"glued is":                 `{"error":{"message":"Unknown parameter isenable_thinking"}}`,
		"unclosed quote":           `{"error":{"message":"Unsupported parameter: 'enable_thinking"}}`,
		"param conflict":           `{"error":{"message":"Unsupported parameter: enable_thinking","param":"max_output_tokens"}}`,
		"cache param conflict":     `{"error":{"code":"invalid_parameter","message":"Unsupported parameter: enable_thinking","param":"prompt_cache_breakpoint"}}`,
		"reverse conflict":         `{"error":{"message":"Unsupported parameter: max_output_tokens","param":"enable_thinking"}}`,
		"wrapped param conflict":   `{"error":{"message":"{\"error\":{\"message\":\"Unsupported parameter: enable_thinking\",\"param\":\"max_output_tokens\"}}"}}`,
		"wrapped reverse conflict": `{"error":{"message":"{\"error\":{\"message\":\"Unsupported parameter: max_output_tokens\",\"param\":\"enable_thinking\"}}"}}`,
		"array detail":             `{"detail":[{"msg":"invalid input","input":"Unsupported parameter: 'enable_thinking'"}]}`,
		"wrapped array detail":     `{"error":{"message":"{\"detail\":[{\"msg\":\"invalid input\",\"input\":\"Unsupported parameter: 'enable_thinking'\"}]}"}}`,
		"serialized array message": `{"error":{"message":"[{\"msg\":\"invalid input\",\"input\":\"Unsupported parameter: 'enable_thinking'\"}]"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body, []byte(response))
			require.NoError(t, err)
			require.False(t, changed)
			require.Nil(t, retryBody)
		})
	}

	t.Run("field absent", func(t *testing.T) {
		retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest,
			[]byte(`{"model":"gpt-6.2-sol","chat_template_kwargs":{"enable_thinking":true}}`),
			[]byte(`{"error":{"message":"Unsupported parameter: enable_thinking"}}`))
		require.NoError(t, err)
		require.False(t, changed)
		require.Nil(t, retryBody)
	})
	t.Run("not a 400", func(t *testing.T) {
		retryBody, _, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusInternalServerError, body,
			[]byte(`{"error":{"message":"Unsupported parameter: enable_thinking"}}`))
		require.NoError(t, err)
		require.False(t, changed)
		require.Nil(t, retryBody)
	})
}

func TestNormalizeOpenAIResponsesRejectedFieldRetryBodyStillRemovesMaxOutputTokens(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","enable_thinking":true,"max_output_tokens":100}`)
	retryBody, reason, changed, err := normalizeOpenAIResponsesRejectedFieldRetryBody(http.StatusBadRequest, body,
		[]byte(`{"error":{"code":"unsupported_parameter","message":"Unsupported parameter: 'max_output_tokens'.","param":"max_output_tokens"}}`))
	require.NoError(t, err)
	require.True(t, changed)
	require.Equal(t, "max_output_tokens parameter rejection", reason)
	require.JSONEq(t, `{"model":"gpt-5.5","enable_thinking":true}`, string(retryBody))
}

func TestOpenAIGatewayService_APIKeyRetriesWithoutRejectedEnableThinking(t *testing.T) {
	body := []byte(`{"model":"gpt-5.5","stream":false,"enable_thinking":true,"input":[{"type":"message","role":"user","content":"hello"}]}`)
	upstream := &httpUpstreamRecorder{responses: []*http.Response{
		newOpenAIRejectedFieldTestResponse(http.StatusBadRequest, `{"detail":"Unsupported parameter: enable_thinking"}`),
		newOpenAIRejectedFieldTestResponse(http.StatusOK, `{"output":[],"usage":{"input_tokens":1,"output_tokens":1,"input_tokens_details":{"cached_tokens":0}}}`),
	}}

	result, err := newOpenAIRejectedFieldTestService(upstream).Forward(
		context.Background(), newOpenAIRejectedFieldTestContext(body), newOpenAIRejectedFieldTestAccount(), body,
	)

	require.NoError(t, err)
	require.NotNil(t, result)
	require.Len(t, upstream.bodies, 2)
	require.True(t, gjson.GetBytes(upstream.bodies[0], "enable_thinking").Bool())
	require.False(t, gjson.GetBytes(upstream.bodies[1], "enable_thinking").Exists())
	require.Equal(t, "hello", gjson.GetBytes(upstream.bodies[1], "input.0.content").String())
	require.Equal(t, gjson.GetBytes(upstream.bodies[0], "model").String(), gjson.GetBytes(upstream.bodies[1], "model").String())
}
