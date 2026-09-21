//go:build unit

package service

import (
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/Wei-Shaw/sub2api/internal/domain"
)

func TestIntentRequestView_LastUserTextAcrossProtocols(t *testing.T) {
	cases := map[string]struct{ body, want string }{
		"claude string content": {
			`{"system":"be brief","messages":[{"role":"user","content":"first"},{"role":"assistant","content":"ok"},{"role":"user","content":"fix this bug"}]}`, "fix this bug"},
		"claude blocks, skipping images": {
			`{"messages":[{"role":"user","content":[{"type":"image","source":{"data":"AAAA"}},{"type":"text","text":"what is in the picture"}]}]}`, "what is in the picture"},
		"claude agent loop: last user turn is only a tool result": {
			`{"messages":[{"role":"user","content":"refactor main.go"},{"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"read","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t1","content":"package main"}]}]}`, "refactor main.go"},
		"openai chat with system turn": {
			`{"messages":[{"role":"system","content":"you are helpful"},{"role":"user","content":[{"type":"text","text":"translate to french"}]}]}`, "translate to french"},
		"openai responses string input": {`{"model":"gpt-5","input":"write a poem"}`, "write a poem"},
		"openai responses item list": {
			`{"input":[{"role":"user","content":[{"type":"input_text","text":"plan my trip"}]},{"type":"function_call_output","call_id":"c","output":"42"}]}`, "plan my trip"},
		"gemini contents": {
			`{"systemInstruction":{"parts":[{"text":"sys"}]},"contents":[{"role":"user","parts":[{"text":"hello"}]},{"role":"model","parts":[{"text":"hi"}]},{"role":"user","parts":[{"inlineData":{"data":"AAAA"}},{"text":"draw a cat"}]}]}`, "draw a cat"},
		"gemini single turn without role": {`{"contents":[{"parts":[{"text":"summarize this"}]}]}`, "summarize this"},
		"gemini function response turn is skipped": {
			`{"contents":[{"role":"user","parts":[{"text":"weather?"}]},{"role":"user","parts":[{"functionResponse":{"name":"w","response":{}}}]}]}`, "weather?"},
		"nothing to classify": {`{"messages":[{"role":"assistant","content":"hi"}]}`, ""},
		"not json":            {`not json at all`, ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			require.Equal(t, tc.want, intentRequestView{body: []byte(tc.body)}.lastUserText(2000))
		})
	}
}

func TestIntentRequestView_TruncatesByRunes(t *testing.T) {
	body := `{"messages":[{"role":"user","content":"` + strings.Repeat("中", 50) + `"}]}`
	got := intentRequestView{body: []byte(body)}.lastUserText(10)
	require.Equal(t, strings.Repeat("中", 10), got, "cut on characters, never inside a multi-byte one")
}

func TestIntentSessionKey(t *testing.T) {
	turn1 := intentRequestView{body: []byte(`{"system":"s","messages":[{"role":"user","content":"hello"}]}`)}
	turn3 := intentRequestView{body: []byte(`{"system":"s","messages":[{"role":"user","content":"hello"},{"role":"assistant","content":"hi"},{"role":"user","content":"now write code"}]}`)}
	other := intentRequestView{body: []byte(`{"system":"s","messages":[{"role":"user","content":"different opening"}]}`)}
	none := http.Header{}

	key := intentSessionKey(7, none, turn1)
	require.NotEmpty(t, key)
	require.Equal(t, key, intentSessionKey(7, none, turn3), "later turns of a conversation map to the same key")
	require.NotEqual(t, key, intentSessionKey(7, none, other))
	require.NotEqual(t, key, intentSessionKey(8, none, turn1), "never shared between API keys")

	// Explicit identifiers win over content, and are scoped by API key too.
	withHeader := http.Header{}
	withHeader.Set("session_id", "abc")
	require.Equal(t, intentSessionKey(7, withHeader, turn1), intentSessionKey(7, withHeader, other))
	require.NotEqual(t, intentSessionKey(7, withHeader, turn1), intentSessionKey(8, withHeader, turn1))
	claudeCode := intentRequestView{body: []byte(`{"metadata":{"user_id":"user_x_session_y"},"messages":[{"role":"user","content":"a"}]}`)}
	claudeCodeLater := intentRequestView{body: []byte(`{"metadata":{"user_id":"user_x_session_y"},"messages":[{"role":"user","content":"compacted history"}]}`)}
	require.Equal(t, intentSessionKey(7, none, claudeCode), intentSessionKey(7, none, claudeCodeLater))

	require.Empty(t, intentSessionKey(7, none, intentRequestView{body: []byte(`{}`)}), "nothing to recognize a conversation by")
}

func TestMatchIntentLabel(t *testing.T) {
	rules := []domain.IntentRule{{Name: "coding"}, {Name: "闲聊"}, {Name: "Image Gen"}}
	cases := []struct {
		answer     string
		intent     string
		understood bool
	}{
		{"coding", "coding", true},
		{"  CODING.\n", "coding", true},
		{`"闲聊"`, "闲聊", true},
		{"**image gen**", "Image Gen", true},
		{"The label is: coding", "coding", true},
		{"NONE", "", true},
		{"none.", "", true},
		{"I think none of them apply", "", true},
		{"coding or 闲聊", "", false},
		{"banana", "", false},
		{"", "", false},
	}
	for _, tc := range cases {
		intent, understood := matchIntentLabel(tc.answer, rules)
		require.Equal(t, tc.understood, understood, tc.answer)
		require.Equal(t, tc.intent, intent, tc.answer)
	}
}
