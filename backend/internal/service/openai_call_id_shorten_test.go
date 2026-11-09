//go:build unit

package service

import (
	"fmt"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

var longCallID = "call_" + strings.Repeat("a", 82) // 87 characters

func callIDAt(t *testing.T, body []byte, index int) string {
	t.Helper()
	return gjson.GetBytes(body, "input."+strconv.Itoa(index)+".call_id").String()
}

func pairBody(callID string) []byte {
	return []byte(`{"model":"gpt-5","input":[{"type":"message","role":"user","content":"hi"},` +
		`{"type":"function_call","name":"shell","arguments":"{}","call_id":"` + callID + `"},` +
		`{"type":"function_call_output","call_id":"` + callID + `","output":"ok"}]}`)
}

func requireSameSlice(t *testing.T, want, got []byte) {
	t.Helper()
	require.Equal(t, len(want), len(got))
	if len(want) > 0 {
		require.True(t, &want[0] == &got[0], "expected the original slice back")
	}
}

func TestShortenOpenAIOverlongCallIDs_UnchangedBodies(t *testing.T) {
	for name, body := range map[string][]byte{
		"no call_id":   []byte(`{"model":"gpt-5","input":[{"type":"message","role":"user","content":"hi"}]}`),
		"short":        pairBody("call_abc"),
		"exactly 64":   pairBody("call_" + strings.Repeat("b", 59)),
		"64 runes":     pairBody(strings.Repeat("中", 64)),
		"empty":        nil,
		"not json":     []byte(`"call_id": "` + strings.Repeat("x", 80)),
		"input absent": []byte(`{"call_id":"` + longCallID + `"}`),
	} {
		t.Run(name, func(t *testing.T) {
			out, n := ShortenOpenAIOverlongCallIDs(body)
			require.Zero(t, n)
			requireSameSlice(t, body, out)
		})
	}
}

func TestShortenOpenAIOverlongCallIDs_PairRewritten(t *testing.T) {
	body := pairBody(longCallID)
	original := append([]byte(nil), body...)
	out, n := ShortenOpenAIOverlongCallIDs(body)
	require.Equal(t, 2, n)
	short := callIDAt(t, out, 1)
	require.Len(t, short, 37)
	require.True(t, strings.HasPrefix(short, "call_"))
	require.Equal(t, short, callIDAt(t, out, 2))
	require.Equal(t, string(original), string(body), "input slice must not be mutated")
	require.Equal(t, strings.Replace(string(original), longCallID, short, -1), string(out), "only call_id values change")

	again, _ := ShortenOpenAIOverlongCallIDs(pairBody(longCallID))
	require.Equal(t, string(out), string(again), "deterministic")
}

func TestShortenOpenAIOverlongCallIDs_AnyCallType(t *testing.T) {
	body := []byte(`{"input":[{"type":"custom_tool_call","name":"apply_patch","input":"x","call_id":"` + longCallID + `"},` +
		`{"type":"custom_tool_call_output","call_id":"` + longCallID + `","output":"ok"}]}`)
	out, n := ShortenOpenAIOverlongCallIDs(body)
	require.Equal(t, 2, n)
	require.Len(t, callIDAt(t, out, 0), 37)
	require.Equal(t, callIDAt(t, out, 0), callIDAt(t, out, 1))
}

func TestShortenOpenAIOverlongCallIDs_OutputWithoutCallUntouched(t *testing.T) {
	body := []byte(`{"previous_response_id":"resp_1","input":[{"type":"function_call_output","call_id":"` + longCallID + `","output":"ok"}]}`)
	out, n := ShortenOpenAIOverlongCallIDs(body)
	require.Zero(t, n)
	require.Equal(t, string(body), string(out))
}

func TestShortenOpenAIOverlongCallIDs_PreviousResponseWithLocalPair(t *testing.T) {
	stored := "call_" + strings.Repeat("s", 80)
	body := []byte(`{"previous_response_id":"resp_1","input":[` +
		`{"type":"function_call_output","call_id":"` + stored + `","output":"ok"},` +
		`{"type":"function_call","name":"shell","arguments":"{}","call_id":"` + longCallID + `"},` +
		`{"type":"function_call_output","call_id":"` + longCallID + `","output":"ok"}]}`)
	out, n := ShortenOpenAIOverlongCallIDs(body)
	require.Equal(t, 2, n)
	require.Equal(t, stored, callIDAt(t, out, 0))
	require.Len(t, callIDAt(t, out, 1), 37)
	require.Equal(t, callIDAt(t, out, 1), callIDAt(t, out, 2))
}

func TestShortenOpenAIOverlongCallIDs_ItemReferenceKeepsValue(t *testing.T) {
	other := "call_" + strings.Repeat("r", 80)
	body := []byte(`{"input":[` +
		`{"type":"function_call","name":"shell","arguments":"{}","call_id":"` + other + `"},` +
		`{"type":"item_reference","id":"` + other + `"},` +
		`{"type":"function_call_output","call_id":"` + other + `","output":"ok"},` +
		`{"type":"function_call","name":"shell","arguments":"{}","call_id":"` + longCallID + `"},` +
		`{"type":"function_call_output","call_id":"` + longCallID + `","output":"ok"}]}`)
	out, n := ShortenOpenAIOverlongCallIDs(body)
	require.Equal(t, 2, n)
	require.Equal(t, other, callIDAt(t, out, 0))
	require.Equal(t, other, callIDAt(t, out, 2))
	require.Len(t, callIDAt(t, out, 3), 37)
}

func TestShortenOpenAIOverlongCallIDs_WhitespaceAndEscapes(t *testing.T) {
	escaped := `call_` + strings.Repeat("q", 60) + `\"x\"`
	body := []byte("{\"input\":[\n {\"type\":\"function_call\",\"name\":\"s\",\"arguments\":\"{}\",\"call_id\" :\n \"" + escaped + "\"},\n" +
		" {\"type\":\"function_call_output\",\"call_id\":\t\"" + escaped + "\",\"output\":\"ok\"}]}")
	out, n := ShortenOpenAIOverlongCallIDs(body)
	require.Equal(t, 2, n)
	require.Len(t, callIDAt(t, out, 0), 37)
	require.Equal(t, callIDAt(t, out, 0), callIDAt(t, out, 1))
}

func TestOpenAIBodyMayHaveOverlongCallID(t *testing.T) {
	require.False(t, openAIBodyMayHaveOverlongCallID([]byte(`{"call_id":"short","x":"`+strings.Repeat("z", 100)+`"}`)))
	require.False(t, openAIBodyMayHaveOverlongCallID([]byte(`{"text":"mentions \"call_id\" in prose `+strings.Repeat("z", 100)+`"}`)))
	require.True(t, openAIBodyMayHaveOverlongCallID([]byte(`{"a":{"call_id":"short"},"b":{"call_id" : "`+longCallID+`"}}`)))
	require.False(t, openAIBodyMayHaveOverlongCallID([]byte(`{"call_id":"`+strings.Repeat("u", 40))))
}

// A long history full of overlong ids must be rewritten in one pass: copying the
// body once per field would allocate thousands of times its size.
func TestShortenOpenAIOverlongCallIDs_LargeHistoryIsLinear(t *testing.T) {
	output := strings.Repeat("o", 16*1024)
	var b strings.Builder
	b.WriteString(`{"model":"gpt-5","input":[`)
	for i := 0; i < 1000; i++ {
		id := fmt.Sprintf("call_%s%06d", strings.Repeat("a", 76), i)
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteString(`{"type":"function_call","name":"s","arguments":"{}","call_id":"` + id + `"},`)
		b.WriteString(`{"type":"function_call_output","call_id":"` + id + `","output":"` + output + `"}`)
	}
	b.WriteString(`]}`)
	body := []byte(b.String())

	var before, after runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&before)
	out, n := ShortenOpenAIOverlongCallIDs(body)
	runtime.ReadMemStats(&after)

	require.Equal(t, 2000, n)
	require.Less(t, after.TotalAlloc-before.TotalAlloc, uint64(4*len(body)))
	first := gjson.GetBytes(out, "input.0.call_id").String()
	require.Len(t, first, 37)
	require.Equal(t, first, gjson.GetBytes(out, "input.1.call_id").String())
	require.NotEqual(t, first, gjson.GetBytes(out, "input.2.call_id").String())
	require.Equal(t, output, gjson.GetBytes(out, "input.1999.output").String())
	require.True(t, gjson.ValidBytes(out))
}
