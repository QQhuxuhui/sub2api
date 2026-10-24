package erroridentity

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestText(t *testing.T) {
	s := New([]string{"gpt-6", "gpt-6-luna", "codex-auto-review", "gpt-6.1-sol"})
	cases := []struct{ in, model, want string }{
		{"model 'codex-auto-review' is not recognized. No hop.", "public", "model 'public' is not recognized. No hop."},
		{"gpt-6-sol gpt-6.1 xgpt-6 gpt-6-luna.", "public", "gpt-6-sol gpt-6.1 xgpt-6 public."},
		{"GPT-6-LUNA gpt-6", "gpt-6-luna", "GPT-6-LUNA gpt-6-luna"},
		{"codex-auto-review", "", "codex-auto-review"},
		{"gpt-6-luna", `public$1"\`, `public$1"\`},
		{"The ChatGPT user has reached their Subscription Sharing usage limit.", "p", "Usage limit reached, please retry later"},
		{"The 'gpt-6.1-sol' model is not supported when using Codex with a ChatGPT account.", "p", "The 'p' model is not supported."},
		{"Your requests to gpt-6-luna for gpt-6-luna in eastus2 have exceeded token rate limit.", "p", "Rate limit exceeded, please retry later"},
		{"contact us through an Azure support request at: https://example.test/support", "p", "contact support"},
		{"triggering Azure OpenAI’s content management policy", "p", "triggering content management policy"},
		{"Rate limited in organization org-abc123", "p", "Rate limited"},
		{"auth_unavailable: no auth available (providers=codex, model=x; last upstream error: bad (nested); gpt-6-luna)", "p", "bad (nested); p"},
		{"auth_unavailable: no auth available (providers=codex, model=x)", "p", "Service temporarily unavailable"},
		{"X-OpenAI-Internal-Test requires access", "p", "This request requires access"},
		{"端点 [codex/codex] (trace: abc)", "p", "端点 (trace: abc)"},
		{"[codex_output_schema] [mcp__codex_apps__foo] [my-codex-tool]", "p", "[codex_output_schema] [mcp__codex_apps__foo] [my-codex-tool]"},
		{"summary=codex2api.custom my_codex2api_tool", "p", "summary=upstream.custom my_codex2api_tool"},
		{`invalid SSE data JSON (len=999): "secret \" prompt\\\n"`, "p", "invalid SSE data JSON (len=999)"},
		{`invalid SSE data JSON (len=999): "truncated`, "p", "invalid SSE data JSON (len=999)"},
	}
	for _, tt := range cases {
		t.Run(tt.in, func(t *testing.T) {
			got := s.Text(tt.in, tt.model)
			if got != tt.want {
				t.Fatalf("got %q want %q", got, tt.want)
			}
			if twice := s.Text(got, tt.model); twice != got {
				t.Fatalf("not idempotent: %q", twice)
			}
		})
	}
}

func TestJSONProtectsContent(t *testing.T) {
	s := New([]string{"hidden"})
	raw := []byte(`{"type":"response.failed","response":{"model":"hidden","error":{"message":"hidden [codex/codex]","type":"server_error","code":"rate_limit","headers":{"X-MS-Region":"eastus2","Retry-After":"30","x-request-id":"abc"}},"output":[{"text":"hidden codex2api"}]}}`)
	got := s.JSON(raw, "public", false)
	var obj map[string]any
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"message":"public"`) || strings.Contains(string(got), "X-MS-") {
		t.Fatal(string(got))
	}
	for _, keep := range []string{`"code":"rate_limit"`, `"Retry-After":"30"`, `"text":"hidden codex2api"`} {
		if !strings.Contains(string(got), keep) {
			t.Fatal(string(got))
		}
	}
	if !strings.Contains(string(raw), "X-MS-") {
		t.Fatal("mutated original")
	}
	for _, keep := range []string{`{"type":"response.output_text.delta","delta":"hidden codex2api"}`, `{"message":"hidden","headers":{"x-ms-test":"x"}}`, `{"error":`, `{"output":[{"error":{"message":"hidden"}}]}`} {
		if string(s.JSON([]byte(keep), "public", false)) != keep {
			t.Fatalf("changed content: %s", keep)
		}
	}
	nested := `{"error":{"message":"upstream: {\"error\":{\"message\":\"hidden\",\"headers\":{\"x-ms-test\":\"secret\",\"Retry-After\":\"2\"}}}"}}`
	cleaned := string(s.JSON([]byte(nested), "public", true))
	if strings.Contains(cleaned, "x-ms-test") || strings.Contains(cleaned, "hidden") {
		t.Fatal(cleaned)
	}
}

func TestJSONDetailError(t *testing.T) {
	s := New([]string{"hidden"})
	raw := []byte(`{"detail":"The 'hidden' model is not supported when using Codex with a ChatGPT account.","code":"hidden"}`)
	before := CoverageStats()
	got := s.Body(raw, "public", true)
	var obj map[string]string
	if err := json.Unmarshal(got, &obj); err != nil {
		t.Fatal(err)
	}
	if obj["detail"] != "The 'public' model is not supported." || obj["code"] != "hidden" {
		t.Fatalf("unexpected error body: %s", got)
	}
	if CoverageStats().UnknownShape != before.UnknownShape {
		t.Fatal("recognized detail error counted as unknown")
	}
	if string(s.JSON(raw, "public", false)) != string(raw) {
		t.Fatal("detail outside error context was modified")
	}
}

func TestTextEmbeddedJSONProtectsFields(t *testing.T) {
	s := New([]string{"hidden"})
	for _, model := range []string{"public", `custom"hidden`, `custom\hidden$1`} {
		t.Run(model, func(t *testing.T) {
			raw := `hidden codex2api: {"error":{"message":"hidden codex2api","code":"hidden","type":"codex2api"},"output":[{"text":"hidden codex2api"}],"tool":{"headers":{"x-ms-test":"keep"}}}`
			got := s.Text(raw, model)
			prefix := model + " upstream: "
			if !strings.HasPrefix(got, prefix) {
				t.Fatalf("prefix not scrubbed: %s", got)
			}
			var obj struct {
				Error  map[string]string            `json:"error"`
				Output []map[string]string          `json:"output"`
				Tool   map[string]map[string]string `json:"tool"`
			}
			if err := json.Unmarshal([]byte(strings.TrimPrefix(got, prefix)), &obj); err != nil {
				t.Fatalf("invalid embedded JSON: %s: %v", got, err)
			}
			if obj.Error["message"] != model+" upstream" || obj.Error["code"] != "hidden" || obj.Error["type"] != "codex2api" || obj.Output[0]["text"] != "hidden codex2api" || obj.Tool["headers"]["x-ms-test"] != "keep" {
				t.Fatalf("unexpected embedded fields: %s", got)
			}
		})
	}
	// A complete object with no known error must remain byte-identical too.
	raw := `{"output":[{"text":"hidden codex2api"}]}`
	if got := s.Text(raw, "public"); got != raw {
		t.Fatalf("arbitrary JSON changed: %s", got)
	}
	for _, message := range []string{
		`auth_unavailable: no auth available (providers=codex, model=hidden)`,
		`invalid SSE data JSON (len=10): "hidden"`,
	} {
		payload, err := json.Marshal(map[string]any{
			"error":  map[string]string{"message": "hidden"},
			"output": []map[string]string{{"text": message}},
		})
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Error  map[string]string   `json:"error"`
			Output []map[string]string `json:"output"`
		}
		clean := s.Text(string(payload), "public")
		if err := json.Unmarshal([]byte(clean), &got); err != nil {
			t.Fatalf("embedded JSON damaged by diagnostic rule: %s", clean)
		}
		if got.Error["message"] != "public" || got.Output[0]["text"] != message {
			t.Fatalf("diagnostic rule changed protected content: %s", clean)
		}
	}
}

func TestSSE(t *testing.T) {
	s := New([]string{"hidden"})
	raw := "event: error\r\nid: 7\r\ndata: {\"error\":\r\ndata: {\"message\":\"hidden\"}}\r\n\r\n"
	got := string(s.SSE([]byte(raw), "public"))
	if !strings.Contains(got, "event: error\r\nid: 7\r\n") || !strings.Contains(got, `"message":"public"`) || !strings.HasSuffix(got, "\r\n\r\n") {
		t.Fatal(got)
	}
	normal := "event: response.output_text.delta\ndata: {\"delta\":\"hidden\"}\n\n"
	if string(s.SSE([]byte(normal), "public")) != normal {
		t.Fatal("changed delta")
	}
}
