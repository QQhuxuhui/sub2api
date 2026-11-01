//go:build unit

package service

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func intentViewSamples() []string {
	return []string{
		``, `{}`, `[]`, `{`, `{"input":"hello"}`, `{"prompt":"hello"}`,
		`{"instructions":"sys","input":[{"role":"user","content":[{"type":"input_text","text":"one"}]},{"role":"assistant","content":"x"},{"role":"user","content":"two"}]}`,
		`{"messages":[{"role":"system","content":"sys"},{"role":"user","content":"a"},{"role":"user","content":[{"type":"text","text":"b"},{"type":"image_url"}]}]}`,
		`{"contents":[{"role":"user","parts":[{"text":"g"}]}],"systemInstruction":{"parts":[{"text":"s"}]}}`,
		`{"messages":"not array","contents":[{"role":"user","parts":[{"text":"g"}]}]}`,
		`{"messages":[{"role":"user","content":"escaped key"}]}`,
		`{"input":[{"role":"user","content":"<system-reminder>x</system-reminder>"},{"role":"user","content":"real"}]}`,
		`{"text":"messages contents input","input":[{"role":"user","content":"after"}]}`,
		`{"input":[{"role":"user","content":"unterminated`,
	}
}

func assertIntentViewCacheMatches(t *testing.T, body []byte) {
	t.Helper()
	plain := intentRequestView{body: body}
	cached := newIntentRequestView(body)
	for round := 0; round < 2; round++ {
		require.Equal(t, plain.recentUserTexts(2), cached.recentUserTexts(2), "body: %q", body)
		require.Equal(t, plain.recentUserTexts(1), cached.recentUserTexts(1), "body: %q", body)
		require.Equal(t, plain.lastUserText(5), cached.lastUserText(5), "body: %q", body)
		require.Equal(t, plain.firstUserText(), cached.firstUserText(), "body: %q", body)
		require.Equal(t, plain.systemText(), cached.systemText(), "body: %q", body)
		require.Equal(t, plain.rawLastUserText(), cached.rawLastUserText(), "body: %q", body)
		require.Equal(t, plain.lastUserImages(), cached.lastUserImages(), "body: %q", body)
		require.Equal(t, len(plain.turns()), len(cached.turns()), "body: %q", body)
	}
}

func TestIntentRequestView_CacheMatchesUncached(t *testing.T) {
	for _, sample := range intentViewSamples() {
		assertIntentViewCacheMatches(t, []byte(sample))
	}
}

func TestIntentParseTurns_MatchesFullPathWalk(t *testing.T) {
	// 基线实现：三个路径都用 gjson 查一遍。
	reference := func(body []byte) int {
		for _, path := range []string{"messages", "contents", "input"} {
			if r := gjson.GetBytes(body, path); r.IsArray() {
				return len(r.Array())
			}
		}
		return -1
	}
	for _, sample := range intentViewSamples() {
		got := intentParseTurns([]byte(sample))
		if want := reference([]byte(sample)); want < 0 {
			require.Nil(t, got, "body: %q", sample)
		} else {
			require.Len(t, got, want, "body: %q", sample)
		}
	}
}

func FuzzIntentViewCacheMatchesUncached(f *testing.F) {
	for _, sample := range intentViewSamples() {
		f.Add([]byte(sample))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		assertIntentViewCacheMatches(t, body)
	})
}

func TestIntentViewCache_ValueCopiesShareCache(t *testing.T) {
	view := newIntentRequestView([]byte(`{"input":[{"role":"user","content":"a"}]}`))
	copied := view
	copied.turns()
	require.True(t, view.cache.turnsLoaded)
}
