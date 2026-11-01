//go:build unit

package service

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"strings"
	"testing"

	"bytes"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestJSONBodyMayContainToken(t *testing.T) {
	cases := []struct {
		name string
		body string
		want bool
	}{
		{"literal", `{"type":"compaction_trigger"}`, true},
		{"absent", `{"type":"message"}`, false},
		{"non_ascii_escape_is_not_token", `{"text":"中文"}`, false},
		{"other_ascii_escape_is_not_token", `{"text":"<&"}`, false},
		{"short_escapes_are_not_token", `{"text":"a\nb\"c\\d\/e"}`, false},
		{"lower_hex_escape", `{"type":"compaction_trigger"}`, true},
		{"upper_hex_escape", `{"type":"compaction_trigger"}`, true},
		{"truncated_unicode_escape", `{"type":"\u00`, true},
		{"invalid_unicode_escape", `{"type":"\u00zz"}`, true},
		{"trailing_backslash", `{"type":"\`, true},
		{"invalid_short_escape", `{"type":"\q"}`, true},
		{"empty", ``, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, JSONBodyMayContainToken([]byte(tc.body), "compaction_trigger"))
		})
	}
}

func TestJSONBodyMayContainToken_ShortEscapeInTokenCharset(t *testing.T) {
	require.True(t, JSONBodyMayContainToken([]byte(`{"a":"x\/y"}`), "x/y"))
	require.True(t, JSONBodyMayContainToken([]byte(`{"a":"x\"y"}`), `x"y`))
	require.True(t, JSONBodyMayContainToken([]byte(`{"a":"x\ny"}`), "x\ny"))
}

func TestJSONBodyMayHaveKey(t *testing.T) {
	require.True(t, jsonBodyMayHaveKey([]byte(`{"messages":[]}`), "messages"))
	require.True(t, jsonBodyMayHaveKey([]byte(`{"messages":[]}`), "messages"))
	require.False(t, jsonBodyMayHaveKey([]byte(`{"text":"my messages here"}`), "messages"))
	require.False(t, jsonBodyMayHaveKey([]byte(`{"text":"say \"hi\""}`), "messages"))
}

func TestJSONBodyRootIsObjectOrNull(t *testing.T) {
	require.True(t, jsonBodyRootIsObjectOrNull([]byte(" \n{}")))
	require.True(t, jsonBodyRootIsObjectOrNull([]byte("null")))
	require.False(t, jsonBodyRootIsObjectOrNull([]byte("[]")))
	require.False(t, jsonBodyRootIsObjectOrNull([]byte("1")))
	require.False(t, jsonBodyRootIsObjectOrNull(nil))
}

// 以下 reference* 是基线 f50c98ac7 的实现，差分测试以它们为准。

func referenceHasCompactionTrigger(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return false
	}
	found := false
	input.ForEach(func(_, item gjson.Result) bool {
		if item.Get("type").String() == "compaction_trigger" {
			found = true
			return false
		}
		return true
	})
	return found
}

func referenceNormalizeCompactionTrigger(body []byte) ([]byte, bool, error) {
	if len(body) == 0 {
		return body, false, nil
	}
	var payload map[string]any
	if err := referenceDecodeUseNumber(body, &payload); err != nil {
		return body, false, err
	}
	input, ok := payload["input"].([]any)
	if !ok || len(input) == 0 {
		return body, false, nil
	}
	triggerCount := 0
	normalized := make([]any, 0, len(input))
	for _, raw := range input {
		item, itemOK := raw.(map[string]any)
		if itemOK && item["type"] == "compaction_trigger" {
			triggerCount++
			continue
		}
		normalized = append(normalized, raw)
	}
	if triggerCount == 0 {
		return body, false, nil
	}
	if triggerCount == 1 {
		if last, ok := input[len(input)-1].(map[string]any); ok && last["type"] == "compaction_trigger" {
			return body, false, nil
		}
	}
	normalized = append(normalized, map[string]any{"type": "compaction_trigger"})
	payload["input"] = normalized
	encoded, err := marshalOpenAIUpstreamJSON(payload)
	if err != nil {
		return body, false, err
	}
	return encoded, true, nil
}

func referenceDecodeUseNumber(data []byte, dst any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(dst); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values are not allowed")
		}
		return err
	}
	return nil
}

func referenceMayContainEmptyBase64InputImage(body []byte) bool {
	if len(body) == 0 {
		return false
	}
	if !bytes.Contains(body, []byte("input_image")) && !bytes.Contains(body, []byte("\\u")) {
		return false
	}
	input := gjson.GetBytes(body, "input")
	if !input.Exists() {
		return false
	}
	return openAIJSONValueMayContainEmptyBase64InputImage(input)
}

// escapeRandomly 把 s 的部分字符写成 \uXXXX，大小写随机，模拟客户端的各种转义。
func escapeRandomly(rng *rand.Rand, s string) string {
	var b strings.Builder
	for _, r := range s {
		switch rng.Intn(5) {
		case 0:
			fmt.Fprintf(&b, `\u%04x`, r)
		case 1:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func tokenFilterSampleBodies() []string {
	base := []string{
		``, ` `, `null`, ` null `, `nullx`, `[]`, `1`, `"x"`, `true`, `{`, `{}`, `{} {}`, `{}x`, `{"a":1}{"b":2}`,
		`{"input":[]}`, `{"input":"compaction_trigger"}`, `{"input":null}`, `{"input":{"type":"compaction_trigger"}}`,
		`{"input":[{"type":"compaction_trigger"}]}`,
		`{"input":[{"type":"compaction_trigger"},{"type":"message"}]}`,
		`{"input":[{"type":"message"},{"type":"compaction_trigger"}]}`,
		`{"input":[{"type":"compaction_trigger"},{"type":"compaction_trigger"}]}`,
		`{"input":[{"type":"compaction_trigger"},{"type":"message"}]}`,
		`{"input":[{"type":"message","text":"compaction_trigger"}]}`,
		`{"input":[{"type":"message","type":"compaction_trigger"},{"type":"x"}]}`,
		`{"input":[{"type":"compaction_trigger"}],"input":[]}`,
		`{"n":12345678901234567890123,"input":[{"type":"message"}]}`,
		"{\"text\":\"\xff\xfe\",\"input\":[{\"type\":\"message\"}]}",
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,  "}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,\n"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64, "}]}`,
		"{\"input\":[{\"type\":\"input_image\",\"image_url\":\"data:image/png;base64, \"}]}",
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image\/png;base64,"}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,"}]}]}`,
		`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AA"},{"type":"input_image","image_url":"data:image/jpeg;base64,"}]}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,"}]}`,
		`{"input":[{"type":"input_image","image_url":"data:image/png;base64,"}]}`,
		`{"input":[{"type":"input_text","text":"中 base64,"}]}`,
	}
	rng := rand.New(rand.NewSource(1))
	out := append([]string{}, base...)
	for _, s := range base {
		out = append(out, s[:rng.Intn(len(s)+1)])
		if strings.Contains(s, `\`) {
			continue
		}
		// 只转义 JSON 字符串内容，结构字符保持原样。
		out = append(out, escapeStringContents(rng, s))
	}
	return out
}

func escapeStringContents(rng *rand.Rand, s string) string {
	parts := strings.Split(s, `"`)
	for i := 1; i < len(parts); i += 2 {
		parts[i] = escapeRandomly(rng, parts[i])
	}
	return strings.Join(parts, `"`)
}

func assertCompactionFiltersMatchReference(t *testing.T, body []byte) {
	t.Helper()
	require.Equal(t, referenceHasCompactionTrigger(body), HasCompactionTriggerInInput(body), "has: %q", body)
	wantBody, wantChanged, wantErr := referenceNormalizeCompactionTrigger(body)
	gotBody, gotChanged, gotErr := NormalizeCompactionTriggerInputOrder(body)
	require.Equal(t, wantChanged, gotChanged, "changed: %q", body)
	require.Equal(t, wantBody, gotBody, "body: %q", body)
	if wantErr == nil {
		require.NoError(t, gotErr, "err: %q", body)
	} else {
		require.EqualError(t, gotErr, wantErr.Error(), "err: %q", body)
	}
}

func assertEmptyBase64FilterMatchesReference(t *testing.T, body []byte) {
	t.Helper()
	require.Equal(t, referenceMayContainEmptyBase64InputImage(body), openAIRequestBodyMayContainEmptyBase64InputImage(body), "body: %q", body)
}

func TestBodyTokenFilters_MatchBaseline(t *testing.T) {
	for _, sample := range tokenFilterSampleBodies() {
		assertCompactionFiltersMatchReference(t, []byte(sample))
		assertEmptyBase64FilterMatchesReference(t, []byte(sample))
	}
}

func FuzzCompactionFiltersMatchBaseline(f *testing.F) {
	for _, sample := range tokenFilterSampleBodies() {
		f.Add([]byte(sample))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		assertCompactionFiltersMatchReference(t, body)
	})
}

func FuzzEmptyBase64FilterMatchesBaseline(f *testing.F) {
	for _, sample := range tokenFilterSampleBodies() {
		f.Add([]byte(sample))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		assertEmptyBase64FilterMatchesReference(t, body)
	})
}

// 基线放行、结构扫描为假的请求（普通带图请求）应该被字节过滤挡住，这是优化本身。
func TestOpenAIRequestBodyMayHaveEmptyBase64Token_SkipsOrdinaryImages(t *testing.T) {
	body := []byte(`{"input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,AAAA"},{"type":"input_text","text":"中文"}]}]}`)
	require.False(t, openAIRequestBodyMayHaveEmptyBase64Token(body))
}
