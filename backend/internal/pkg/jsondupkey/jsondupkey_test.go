//go:build unit

package jsondupkey

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/Wei-Shaw/sub2api/internal/pkg/httputil"
	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFind(t *testing.T) {
	cases := []struct {
		name     string
		body     string
		opts     Options
		want     Result
		path     string
		complete bool
	}{
		{"top-level model", `{"model":"cheap","stream":true,"model":"pricey"}`, Options{}, ResultDuplicate, "/model", true},
		{"nested reasoning effort", `{"model":"m","reasoning":{"effort":"low","effort":"max"}}`, Options{}, ResultDuplicate, "/reasoning/effort", true},
		{"gemini image size", `{"generationConfig":{"imageConfig":{"imageSize":"1K","imageSize":"4K"}}}`, Options{}, ResultDuplicate, "/generationConfig/imageConfig/imageSize", true},
		{"tool type", `{"tools":[{"type":"function","type":"image_generation"}]}`, Options{}, ResultDuplicate, "/tools/0/type", true},
		{"escaped key", `{"model":"a","model":"b"}`, Options{}, ResultDuplicate, "/model", true},
		{"pointer escaping", `{"a/b":{"c~d":1,"c~d":2}}`, Options{}, ResultDuplicate, "/a~1b/c~0d", true},
		{"messages content skipped", `{"model":"m","messages":[{"role":"user","role":"system"}]}`, Options{}, ResultOK, "", true},
		{"input skipped", `{"input":[{"type":"message","type":"x"}]}`, Options{}, ResultOK, "", true},
		{"tool parameters pattern", `{"tools":[{"type":"function","parameters":{"type":"string","pattern":"a","pattern":"b"}}]}`, Options{}, ResultOK, "", true},
		{"function parameters", `{"tools":[{"type":"function","function":{"name":"f","parameters":{"x":1,"x":2}}}]}`, Options{}, ResultOK, "", true},
		{"text format schema", `{"text":{"format":{"type":"json_schema","schema":{"a":1,"a":2}}}}`, Options{}, ResultOK, "", true},
		{"input examples", `{"tools":[{"name":"t","input_examples":[{"q":1,"q":2}]}]}`, Options{}, ResultOK, "", true},
		{"tool own key next to examples", `{"tools":[{"type":"a","input_examples":[],"type":"b"}]}`, Options{}, ResultDuplicate, "/tools/0/type", true},
		{"root parameters checked", `{"parameters":{"sampleCount":1,"sampleCount":4}}`, Options{}, ResultDuplicate, "/parameters/sampleCount", true},
		{"same key in sibling objects", `{"a":{"x":1},"b":{"x":2},"c":[{"x":1},{"x":2}]}`, Options{}, ResultOK, "", true},
		{"string holding json", `{"a":"{\"x\":1,\"x\":2}"}`, Options{}, ResultOK, "", true},
		{"bom and raw control byte", "\xef\xbb\xbf {\"a\":\"line\nbreak\",\"b\":1}", Options{}, ResultOK, "", true},
		{"unicode leading space", " 　{\"model\":1,\"model\":2}", Options{}, ResultDuplicate, "/model", true},
		{"syntax error", `{"a":1,}`, Options{}, ResultInvalid, "", false},
		{"missing colon", `{"a" 1}`, Options{}, ResultInvalid, "", false},
		{"unterminated", `{"a":"b`, Options{}, ResultInvalid, "", false},
		{"trailing garbage", `{"a":1} x`, Options{}, ResultInvalid, "", false},
		{"duplicate then trailing garbage", `{"model":1,"model":2} x`, Options{}, ResultDuplicate, "/model", false},
		{"duplicate then broken literal", `{"model":1,"model":2,"x":tru}`, Options{}, ResultDuplicate, "/model", true},
		{"duplicate then syntax error", `{"model":1,"model":2,`, Options{}, ResultDuplicate, "/model", false},
		{"root case fold", `{"model":"a","Model":"b"}`, Options{}, ResultDuplicate, "/Model", true},
		{"root case fold kelvin", "{\"k\":1,\"K\":2}", Options{}, ResultDuplicate, "/K", true},
		{"nested case variants allowed", `{"metadata":{"Trace":"1","trace":"2"},"questions":{"Q1":{},"q1":{}}}`, Options{}, ResultOK, "", true},
		{"nested exact duplicate", `{"metadata":{"trace":"1","trace":"2"}}`, Options{}, ResultDuplicate, "/metadata/trace", true},
		{"not json", `--boundary\r\n`, Options{}, ResultOK, "", false},
		{"scalar root", `"model"`, Options{}, ResultOK, "", false},
		{"array root", `[{"a":1,"a":2}]`, Options{}, ResultDuplicate, "/0/a", true},
		{"generateContentRequest contents", `{"generateContentRequest":{"model":"m","contents":[{"role":"user","role":"x"}]}}`, Options{}, ResultOK, "", true},
		{"generateContentRequest model", `{"generateContentRequest":{"model":"a","model":"b"}}`, Options{}, ResultDuplicate, "/generateContentRequest/model", true},
		{"seedance content skipped", `{"model":"m","content":[{"type":"text","type":"x"}]}`, Options{SkipRootContent: true}, ResultOK, "", true},
		{"seedance model checked", `{"model":"a","content":[],"model":"b"}`, Options{SkipRootContent: true}, ResultDuplicate, "/model", true},
		{"content checked by default", `{"content":{"a":1,"a":2}}`, Options{}, ResultDuplicate, "/content/a", true},
		{"many keys", wideObject(40, true), Options{}, ResultDuplicate, "/k0", true},
		{"many keys case fold", `{` + strings.TrimSuffix(strings.TrimPrefix(wideObject(40, false), "{"), "}") + `,"K5":1}`, Options{}, ResultDuplicate, "/K5", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			result, path, _, complete := FindWithOptions([]byte(tc.body), tc.opts)
			require.Equal(t, tc.want, result)
			require.Equal(t, tc.path, path)
			require.Equal(t, tc.complete, complete)
		})
	}
}

func wideObject(n int, duplicateFirst bool) string {
	var b strings.Builder
	b.WriteString("{")
	for i := 0; i < n; i++ {
		if i > 0 {
			b.WriteString(",")
		}
		b.WriteString(`"k`)
		b.WriteString(strconv.Itoa(i))
		b.WriteString(`":1`)
	}
	if duplicateFirst {
		b.WriteString(`,"k0":2`)
	}
	b.WriteString("}")
	return b.String()
}

func TestFindDepth(t *testing.T) {
	nested := func(depth int) []byte {
		return []byte(strings.Repeat("[", depth) + strings.Repeat("]", depth))
	}
	result, _, _, complete := Find(nested(MaxDepth))
	require.Equal(t, ResultOK, result)
	require.True(t, complete)
	result, _, _, _ = Find(nested(MaxDepth + 1))
	require.Equal(t, ResultTooDeep, result)
}

// A key mixing raw control bytes and escapes must compare like the lenient
// request path decodes it.
func TestFindKeyWithControlByteAndEscape(t *testing.T) {
	body := []byte("{\"a\tb\\u0063\":1,\"a\\u0009bc\":2}")
	normalized, err := httputil.NormalizeLenientJSONRequestBody(body, 0)
	require.NoError(t, err)
	var decoded map[string]int
	require.NoError(t, json.Unmarshal(normalized, &decoded))
	require.Len(t, decoded, 1, "the lenient path sees one key")

	result, path, _, _ := Find(body)
	require.Equal(t, ResultDuplicate, result)
	require.Equal(t, "/a\tbc", path)
}

// Long member names are neither decoded nor compared, so a name made of
// control bytes cannot make the scanner allocate several times its size.
func TestFindLongKeysAreNotCompared(t *testing.T) {
	long := strings.Repeat("\x01", MaxComparedKeyBytes+1)
	body := []byte(`{"` + long + `":1,"` + long + `":2,"model":"a","model":"b"}`)
	result, path, _, complete := Find(body)
	require.Equal(t, ResultDuplicate, result)
	require.Equal(t, "/model", path)
	require.True(t, complete)

	huge := []byte(`{"` + strings.Repeat("\x01", 8<<20) + `":1}`)
	allocs := testing.Benchmark(func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			Find(huge)
		}
	})
	require.Less(t, allocs.AllocedBytesPerOp(), int64(64<<10), "decoding cost must not grow with the key")

	// A long parent name still yields a bounded path.
	nested := []byte(`{"` + strings.Repeat("p", MaxComparedKeyBytes+1) + `":{"x":1,"x":2}}`)
	result, path, _, _ = Find(nested)
	require.Equal(t, ResultDuplicate, result)
	require.LessOrEqual(t, len(path), MaxPathBytes)
	require.True(t, strings.HasSuffix(path, "/x"))
}

func TestFindTruncatesLongPath(t *testing.T) {
	key := strings.Repeat("键", 200)
	body := []byte(`{"` + key + `":1,"` + key + `":2}`)
	result, path, truncated, _ := Find(body)
	require.Equal(t, ResultDuplicate, result)
	require.True(t, truncated)
	require.LessOrEqual(t, len(path), MaxPathBytes)
	require.True(t, utf8.ValidString(path))
	require.True(t, strings.HasSuffix(path, "…"))

	deep := strings.Repeat(`{"`+strings.Repeat("a", 50)+`":`, 20) + `{"x":1,"x":2}` + strings.Repeat("}", 20)
	result, path, truncated, _ = Find([]byte(deep))
	require.Equal(t, ResultDuplicate, result)
	require.True(t, truncated)
	require.LessOrEqual(t, len(path), MaxPathBytes)
}

func TestLeadingByte(t *testing.T) {
	b, ok := LeadingByte([]byte("\xef\xbb\xbf  {"))
	require.True(t, ok)
	require.Equal(t, byte('{'), b)
	_, ok = LeadingByte([]byte(" \n\t"))
	require.False(t, ok)
	end, more := LeadingSpaceEnd([]byte(" \xe3\x80"), 0)
	require.True(t, more, "incomplete U+3000")
	require.Equal(t, 1, end)
	end, more = LeadingSpaceEnd([]byte(" \xe3\x80\x80{"), end)
	require.False(t, more)
	require.Equal(t, 4, end)
	_, more = LeadingSpaceEnd([]byte("\xef\xbb"), 0)
	require.True(t, more, "incomplete BOM")
	end, more = LeadingSpaceEnd([]byte("\xef\xbb\xbf x"), 0)
	require.False(t, more)
	require.Equal(t, 4, end)
}

func benchmarkBodies() map[string][]byte {
	longText := strings.Repeat("lorem ipsum dolor sit amet ", 160000) // ~4 MB
	image := strings.Repeat("QUJDRA", 700000)                         // ~4 MB base64
	return map[string][]byte{
		"long_text": []byte(`{"model":"m","input":[{"role":"user","content":"` + longText + `"}],"reasoning":{"effort":"high"}}`),
		"image":     []byte(`{"model":"m","input":[{"role":"user","content":[{"type":"input_image","image_url":"data:image/png;base64,` + image + `"}]}]}`),
		"wide":      []byte(wideObject(5000, false)),
	}
}

func BenchmarkFind(b *testing.B) {
	for name, body := range benchmarkBodies() {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				if result, _, _, _ := Find(body); result != ResultOK {
					b.Fatal(result)
				}
			}
		})
	}
}

func BenchmarkGJSONValidBytes(b *testing.B) {
	for name, body := range benchmarkBodies() {
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(body)))
			for i := 0; i < b.N; i++ {
				if !gjson.ValidBytes(body) {
					b.Fatal("invalid")
				}
			}
		})
	}
}
