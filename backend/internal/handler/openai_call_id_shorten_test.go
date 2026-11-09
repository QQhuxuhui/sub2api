package handler

import (
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func overlongCallIDBody() []byte {
	id := "call_" + strings.Repeat("a", 82)
	return []byte(`{"input":[{"type":"function_call","name":"s","arguments":"{}","call_id":"` + id + `"},` +
		`{"type":"function_call_output","call_id":"` + id + `","output":"ok"}]}`)
}

func TestOpenAICallIDShortener_ByAccountKind(t *testing.T) {
	body := overlongCallIDBody()
	oauth := &service.Account{ID: 1, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth}
	oauthPassthrough := &service.Account{ID: 2, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth,
		Extra: map[string]any{"openai_passthrough": true}}

	cases := []struct {
		name    string
		account *service.Account
		rewrite bool
	}{
		{"apikey", newOpenAIPassthroughAccount(3, false), true},
		{"apikey passthrough", newOpenAIPassthroughAccount(4, true), true},
		{"oauth", oauth, false},
		{"oauth passthrough", oauthPassthrough, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var s openAICallIDShortener
			out := s.apply(body, tc.account, nil)
			callID := gjson.GetBytes(out, "input.0.call_id").String()
			if tc.rewrite {
				require.Len(t, callID, 37)
			} else {
				require.Equal(t, string(body), string(out))
			}
		})
	}
}

func TestOpenAICallIDShortener_ReusesResultForSameBody(t *testing.T) {
	body := overlongCallIDBody()
	var s openAICallIDShortener
	first := s.apply(body, newOpenAIPassthroughAccount(3, false), nil)
	second := s.apply(body, newOpenAIPassthroughAccount(4, false), nil)
	require.True(t, &first[0] == &second[0], "same canonical body should reuse the cached result")

	normal := []byte(`{"input":[{"type":"message","role":"user","content":"hi"}]}`)
	var n openAICallIDShortener
	out := n.apply(normal, newOpenAIPassthroughAccount(5, false), nil)
	require.True(t, &out[0] == &normal[0], "normal body is forwarded as-is")
}
