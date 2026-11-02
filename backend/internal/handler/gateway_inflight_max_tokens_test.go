//go:build unit

package handler

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequestMaxOutputTokens_PriorityAndShapes(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		{"anthropic", `{"max_tokens":10}`, 10},
		{"chat", `{"max_completion_tokens":11}`, 11},
		{"responses", `{"max_output_tokens":12}`, 12},
		{"gemini camel", `{"generationConfig":{"maxOutputTokens":13}}`, 13},
		{"gemini snake", `{"generation_config":{"max_output_tokens":14}}`, 14},
		{"priority", `{"max_output_tokens":3,"max_completion_tokens":2,"max_tokens":1}`, 1},
		{"skips non positive", `{"max_tokens":0,"max_output_tokens":5}`, 5},
		{"skips non number", `{"max_tokens":"9","max_output_tokens":6}`, 6},
		{"nested text ignored", `{"input":"{\"max_tokens\":99}","tool":{"max_tokens":99}}`, 0},
		{"not json", `not json`, 0},
		{"empty", ``, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, requestMaxOutputTokens([]byte(tc.body)))
		})
	}
}

func BenchmarkRequestMaxOutputTokensLargeBody(b *testing.B) {
	body := []byte(`{"model":"m","input":"` + strings.Repeat("x", 8<<20) + `"}`)
	b.SetBytes(int64(len(body)))
	for b.Loop() {
		requestMaxOutputTokens(body)
	}
}
