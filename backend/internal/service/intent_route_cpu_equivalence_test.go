//go:build unit

package service

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

// referenceIntentContentText 是基线 4d58662ec 的实现。
func referenceIntentContentText(r gjson.Result) string {
	switch {
	case r.Type == gjson.String:
		return strings.TrimSpace(r.String())
	case r.IsObject():
		if parts := r.Get("parts"); parts.Exists() {
			return referenceIntentContentText(parts)
		}
		return strings.TrimSpace(r.Get("text").String())
	case r.IsArray():
		var b strings.Builder
		for _, block := range r.Array() {
			var text string
			switch {
			case block.Type == gjson.String:
				text = block.String()
			case block.Get("functionCall").Exists() || block.Get("functionResponse").Exists():
				continue
			default:
				switch block.Get("type").String() {
				case "", "text", "input_text", "output_text":
					text = block.Get("text").String()
				}
			}
			if text = strings.TrimSpace(text); text != "" {
				if b.Len() > 0 {
					_ = b.WriteByte('\n')
				}
				_, _ = b.WriteString(text)
			}
		}
		return b.String()
	}
	return ""
}

func intentContentSamples() []string {
	return []string{
		`"plain"`, `  spaced  `, `null`, `1`, `true`, `{}`, `[]`, `{"text":" t "}`, `{"parts":[{"text":"p"}]}`, `{"parts":"s"}`,
		`["a","b"]`, `[1,null,true,[],{}]`,
		`[{"type":"text","text":"a"},{"type":"input_text","text":"b"},{"type":"output_text","text":"c"},{"text":"d"}]`,
		`[{"type":"input_image","image_url":"data:image/png;base64,AAAA","text":"hidden"}]`,
		`[{"type":"image_url","text":"hidden"},{"type":"text","text":"shown"}]`,
		`[{"type":"text","functionCall":{},"text":"hidden"}]`,
		`[{"functionCall":{"name":"f"},"text":"hidden"},{"functionResponse":{},"text":"hidden"},{"text":"shown"}]`,
		`[{"type":"input_image","functionCall":{},"text":"hidden"}]`,
		`[{"type":1,"text":"num type"},{"type":null,"text":"null type"},{"type":"","text":"empty type"},{"type":{"a":1},"text":"object type"}]`,
		`[{"type":"text","text":"escaped"},{"type":"image","text":"escaped key"}]`,
		`[{"type":"text","type":"image","text":"dup"},{"type":"image","type":"text","text":"dup2"}]`,
		`[{"type":"TEXT","text":"case"},{"type":" text","text":"space"}]`,
		`[{"type":"text","text":"  "},{"type":"text","text":1},{"type":"text","text":{"a":1}}]`,
		`[{"type":"text","text":"unterminated`, `[{"type":`,
	}
}

func assertIntentContentMatches(t *testing.T, raw string) {
	t.Helper()
	r := gjson.Parse(raw)
	require.Equal(t, referenceIntentContentText(r), intentContentText(r), "raw: %q", raw)
}

func TestIntentContentText_MatchesBaseline(t *testing.T) {
	for _, sample := range intentContentSamples() {
		assertIntentContentMatches(t, sample)
		assertIntentContentMatches(t, `{"parts":`+sample+`}`)
	}
}

func FuzzIntentContentTextMatchesBaseline(f *testing.F) {
	for _, sample := range intentContentSamples() {
		f.Add(sample)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		assertIntentContentMatches(t, raw)
	})
}

// referenceMatchMonitorChallenge 是基线 4d58662ec 的实现。
func referenceMatchMonitorChallenge(path string, body []byte) (MonitorChallenge, bool) {
	path = strings.TrimRight(path, "/")
	var protocol monitorProtocol
	model := ""
	switch {
	case strings.HasSuffix(path, "/responses"):
		protocol = monitorProtocolResponses
	case strings.HasSuffix(path, "/chat/completions"):
		protocol = monitorProtocolChat
	case strings.HasSuffix(path, "/messages"):
		protocol = monitorProtocolAnthropic
	case strings.HasSuffix(path, ":generateContent"):
		protocol = monitorProtocolGemini
		_, rest, ok := strings.Cut(path, "/models/")
		if !ok {
			return MonitorChallenge{}, false
		}
		model, _, _ = strings.Cut(rest, ":")
	default:
		return MonitorChallenge{}, false
	}
	if !gjson.ValidBytes(body) {
		return MonitorChallenge{}, false
	}
	root := gjson.ParseBytes(body)
	if protocol != monitorProtocolGemini {
		model = root.Get("model").String()
	}
	model = strings.TrimSpace(model)
	if model == "" || root.Get("stream").Bool() || root.Get("tools").Exists() {
		return MonitorChallenge{}, false
	}
	prompt, ok := monitorChallengePrompt(protocol, root)
	if !ok {
		return MonitorChallenge{}, false
	}
	prompt = strings.TrimSpace(prompt)
	m := monitorChallengePattern.FindStringSubmatch(prompt)
	if m == nil {
		return MonitorChallenge{}, false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[3])
	answer := a + b
	if m[2] == "-" {
		answer = a - b
	}
	return MonitorChallenge{
		Model: model, Question: m[1] + " " + m[2] + " " + m[3], Answer: strconv.Itoa(answer),
		protocol: protocol, promptRunes: utf8.RuneCountInString(prompt),
	}, true
}

func monitorSampleBodies() []string {
	const prompt = "Calculate and respond with ONLY the number, nothing else.\nQ: 3 + 4 = ?\nA:"
	quoted := strconv.Quote(prompt)
	return []string{
		``, `{`, `[]`, `null`, `1`, `"x"`,
		`{"model":"m","input":` + quoted + `}`,
		`{"model":"m","stream":false,"input":` + quoted + `}`,
		`{"model":"m","stream":true,"input":` + quoted + `}`,
		`{"model":"m","stream":"true","input":` + quoted + `}`,
		`{"model":"m","stream":1,"input":` + quoted + `}`,
		`{"model":"m","stream":null,"input":` + quoted + `}`,
		`{"model":"m","tools":[],"input":` + quoted + `}`,
		`{"model":"m","tools":null,"input":` + quoted + `}`,
		`{"model":"m","stream":true,"input":` + quoted + `}`,
		`{"model":"m","tools":[],"input":` + quoted + `}`,
		`{"model":"m","stream":false,"stream":true,"input":` + quoted + `}`,
		`{"model":"m","stream":true,"stream":false,"input":` + quoted + `}`,
		`{"model":"m","stream":true,"input":` + quoted,
		`{"model":"m","tools":[],"input":` + quoted,
		`{"model":"m","input":` + quoted,
		`{"model":"","input":` + quoted + `}`,
		`{"model":"m","messages":[{"role":"user","content":` + quoted + `}]}`,
		`{"model":"m","contents":[{"parts":[{"text":` + quoted + `}]}]}`,
		`{"contents":[{"parts":[{"text":` + quoted + `}]}]}`,
		`{"model":"m","input":[{"role":"user","content":[{"type":"input_text","text":` + quoted + `}]}]}`,
	}
}

func assertMonitorMatches(t *testing.T, path string, body []byte) {
	t.Helper()
	want, wantOK := referenceMatchMonitorChallenge(path, body)
	got, gotOK := MatchMonitorChallenge(path, body)
	require.Equal(t, wantOK, gotOK, "path %s body %q", path, body)
	require.Equal(t, want, got, "path %s body %q", path, body)
}

var monitorSamplePaths = []string{"/v1/responses", "/v1/chat/completions", "/v1/messages", "/v1beta/models/gemini-x:generateContent", "/v1/other"}

func TestMatchMonitorChallenge_MatchesBaseline(t *testing.T) {
	matched := 0
	for _, path := range monitorSamplePaths {
		for _, body := range monitorSampleBodies() {
			assertMonitorMatches(t, path, []byte(body))
			if _, ok := MatchMonitorChallenge(path, []byte(body)); ok {
				matched++
			}
		}
	}
	require.Greater(t, matched, 0, "样例要覆盖识别成功的分支")
}

func FuzzMatchMonitorChallengeMatchesBaseline(f *testing.F) {
	for i, body := range monitorSampleBodies() {
		f.Add(monitorSamplePaths[i%len(monitorSamplePaths)], body)
	}
	f.Fuzz(func(t *testing.T, path, body string) {
		assertMonitorMatches(t, path, []byte(body))
	})
}

func BenchmarkIntentContentTextImages(b *testing.B) {
	blocks := []string{`{"type":"input_text","text":"hello"}`}
	for i := 0; i < 19; i++ {
		blocks = append(blocks, fmt.Sprintf(`{"type":"input_image","detail":"auto","image_url":"data:image/jpeg;base64,%s"}`, strings.Repeat("QUJD", 37500)))
	}
	r := gjson.Parse("[" + strings.Join(blocks, ",") + "]")
	b.Run("baseline", func(b *testing.B) {
		for b.Loop() {
			_ = referenceIntentContentText(r)
		}
	})
	b.Run("optimized", func(b *testing.B) {
		for b.Loop() {
			_ = intentContentText(r)
		}
	})
}
