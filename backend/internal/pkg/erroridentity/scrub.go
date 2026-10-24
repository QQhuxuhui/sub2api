// Package erroridentity rewrites only client-visible error fields. Scrubbers are
// immutable and may be shared by concurrent requests. Audit inputs stay intact.
package erroridentity

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"
)

const MaxPayload = 8 << 20
const maxDepth = 4

type Scrubber struct{ models []string }

func New(models []string) *Scrubber {
	seen := map[string]bool{}
	s := &Scrubber{}
	for _, m := range models {
		m = strings.TrimSpace(m)
		key := strings.ToLower(m)
		if m != "" && !seen[key] {
			seen[key] = true
			s.models = append(s.models, m)
		}
	}
	sort.Slice(s.models, func(i, j int) bool {
		if len(s.models[i]) == len(s.models[j]) {
			return s.models[i] < s.models[j]
		}
		return len(s.models[i]) > len(s.models[j])
	})
	return s
}

func word(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-'
}
func (s *Scrubber) modelsText(text, model string) string {
	if s == nil || model == "" || len(s.models) == 0 {
		return text
	}
	var out strings.Builder
	start := 0
	for i := 0; i < len(text); {
		matched := 0
		if i == 0 || !word(text[i-1]) && text[i-1] != '.' {
			for _, m := range s.models {
				end := i + len(m)
				if end > len(text) || !strings.EqualFold(text[i:end], m) {
					continue
				}
				if end < len(text) && (word(text[end]) || text[end] == '.' && end+1 < len(text) && text[end+1] >= '0' && text[end+1] <= '9') {
					continue
				}
				// Consume the longest match even when it is the client's own model.
				matched = len(m)
				if !strings.EqualFold(m, model) {
					out.WriteString(text[start:i])
					out.WriteString(model)
					start = end
				}
				break
			}
		}
		if matched > 0 {
			i += matched
		} else {
			i++
		}
	}
	if start == 0 {
		return text
	}
	out.WriteString(text[start:])
	return out.String()
}

var sharing = regexp.MustCompile(`(?i)The ChatGPT user has reached their Subscription Sharing usage limit[^.\n]*(?:\.|$)`)
var unsupported = regexp.MustCompile(`(?i)The '([^']+)' model is not supported when using Codex with a ChatGPT account\.`)
var azureRate = regexp.MustCompile(`(?i)Your requests to [^\n]+? for [^\n]+? in [A-Za-z0-9_-]+ have exceeded token rate limit\.?`)
var azureSupport = regexp.MustCompile(`(?i)contact us through an Azure support request at:\s*https?://[^\s"<>]+`)
var azurePolicy = regexp.MustCompile(`(?i)Azure OpenAI['’]s\s+(content management policy)`)
var organization = regexp.MustCompile(`(?i) in organization org-[A-Za-z0-9_-]+`)
var internalHeader = regexp.MustCompile(`(?i)X-OpenAI-Internal-[A-Za-z0-9_-]+ requires`)
var channelTag = regexp.MustCompile(`(?i) \[codex/codex\](\s*(?:\(trace:[^\r\n]*\))?\s*)$`)
var codex2api = regexp.MustCompile(`(?i)codex2api`)
var invalidSSE = regexp.MustCompile(`invalid SSE data JSON \(len=[0-9]+\):\s*"`)

// Text accepts an already decoded error message, never arbitrary output text.
func (s *Scrubber) Text(text, model string) string { return s.text(text, model, 0) }
func (s *Scrubber) text(text, model string, depth int) string {
	if len(text) > MaxPayload {
		RecordSizeLimit()
		return text
	}
	if depth >= maxDepth {
		coverage.depthLimit.Add(1)
		return text
	}
	if model == "" {
		coverage.missingModel.Add(1)
	}
	// Known CPA wrappers end in one outer ')'; preserve punctuation inside X.
	const prefix = "auth_unavailable: no auth available ("
	for i := 0; i < maxDepth; i++ {
		at := strings.Index(text, prefix)
		if at < 0 {
			break
		}
		tail := text[at+len(prefix):]
		if !strings.HasSuffix(strings.TrimSpace(tail), ")") {
			break
		}
		marker := strings.Index(tail, "; last upstream error: ")
		if marker < 0 {
			text = recordRewrite(8, text, text[:at]+"Service temporarily unavailable")
			break
		}
		text = recordRewrite(8, text, text[:at]+strings.TrimSuffix(strings.TrimSpace(tail[marker+len("; last upstream error: "):]), ")"))
	}
	// Remove the entire preview, including truncated/escaped Go %q strings.
	if loc := invalidSSE.FindStringIndex(text); loc != nil {
		colon := strings.Index(text[loc[0]:loc[1]], ":") + loc[0]
		text = recordRewrite(12, text, text[:colon])
	}
	// Embedded upstream error JSON is only recognized as a complete object.
	// Keep the JSON suffix out of all subsequent text rules: only its allowed
	// fields may change, and inserted model names must not be scanned again.
	var embedded string
	if at := strings.IndexByte(text, '{'); at >= 0 {
		tail := text[at:]
		if json.Valid([]byte(tail)) {
			embedded = string(s.json([]byte(tail), model, false, depth+1))
			text = text[:at]
		}
	}
	text = recordRewrite(1, text, sharing.ReplaceAllString(text, "Usage limit reached, please retry later"))
	text = recordRewrite(2, text, unsupported.ReplaceAllStringFunc(text, func(v string) string {
		m := model
		if m == "" {
			m = unsupported.FindStringSubmatch(v)[1]
		}
		return "The '" + m + "' model is not supported."
	}))
	text = recordRewrite(3, text, azureRate.ReplaceAllString(text, "Rate limit exceeded, please retry later"))
	text = recordRewrite(5, text, azureSupport.ReplaceAllString(text, "contact support"))
	// Only remove the vendor phrase in the known content-policy wording.
	if strings.Contains(strings.ToLower(text), "content management policy") {
		text = recordRewrite(6, text, azurePolicy.ReplaceAllString(text, "$1"))
	}
	text = recordRewrite(7, text, organization.ReplaceAllString(text, ""))
	text = recordRewrite(9, text, internalHeader.ReplaceAllString(text, "This request requires"))
	text = recordRewrite(10, text, channelTag.ReplaceAllString(text, "$1"))
	indices := codex2api.FindAllStringIndex(text, -1)
	for i := len(indices) - 1; i >= 0; i-- {
		a, b := indices[i][0], indices[i][1]
		isWord := func(c byte) bool { return word(c) && c != '-' }
		if (a == 0 || !isWord(text[a-1])) && (b == len(text) || !isWord(text[b])) {
			text = recordRewrite(11, text, text[:a]+"upstream"+text[b:])
		}
	}
	return recordRewrite(0, text, s.modelsText(text, model)) + embedded
}

// JSON preserves the original bytes when no allowed field changes. errorContext
// enables root message/string-error fields for HTTP error bodies and error events.
func (s *Scrubber) JSON(data []byte, model string, errorContext bool) []byte {
	return s.json(data, model, errorContext, 0)
}
func (s *Scrubber) json(data []byte, model string, errorContext bool, depth int) []byte {
	if len(data) > MaxPayload {
		RecordSizeLimit()
		return data
	}
	if depth >= maxDepth {
		coverage.depthLimit.Add(1)
		return data
	}
	if !errorContext && !bytes.Contains(data, []byte("error")) && !bytes.Contains(data, []byte("failed")) && !bytes.Contains(data, []byte(`\u`)) {
		return data
	}
	var obj map[string]json.RawMessage
	if json.Unmarshal(data, &obj) != nil || obj == nil {
		coverage.invalidJSON.Add(1)
		return data
	}
	getString := func(raw json.RawMessage) string { var v string; _ = json.Unmarshal(raw, &v); return v }
	kind := getString(obj["type"])
	knownError := kind == "error" || kind == "response.failed" || getString(obj["status"]) == "failed"
	changed := false
	setText := func(m map[string]json.RawMessage, key string) {
		var v string
		if json.Unmarshal(m[key], &v) == nil {
			if next := s.text(v, model, depth); next != v {
				m[key], _ = json.Marshal(next)
				changed = true
			}
		}
	}
	stripHeaders := func(m map[string]json.RawMessage) {
		var h map[string]json.RawMessage
		if json.Unmarshal(m["headers"], &h) != nil || h == nil {
			return
		}
		dirty := false
		for k := range h {
			if strings.HasPrefix(strings.ToLower(k), "x-ms-") {
				delete(h, k)
				dirty = true
			}
		}
		if dirty {
			ruleHits[4].Add(1)
			changed = true
			if len(h) == 0 {
				delete(m, "headers")
			} else {
				m["headers"], _ = json.Marshal(h)
			}
		}
	}
	if raw, ok := obj["error"]; ok && !bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		var e map[string]json.RawMessage
		if json.Unmarshal(raw, &e) == nil && e != nil {
			knownError = true
			setText(e, "message")
			stripHeaders(e)
			next, _ := json.Marshal(e)
			if changed {
				obj["error"] = next
			}
		} else if errorContext || knownError {
			setText(obj, "error")
		}
	}
	if raw, ok := obj["response"]; ok {
		next := s.json(raw, model, knownError, depth+1)
		if !bytes.Equal(raw, next) {
			obj["response"] = next
			changed = true
		}
	}
	if knownError || errorContext {
		setText(obj, "message")
		setText(obj, "detail")
		stripHeaders(obj)
		if model != "" {
			if v := getString(obj["model"]); v != "" && v != model {
				obj["model"], _ = json.Marshal(model)
				changed = true
			}
		}
	}
	if errorContext && !knownError && len(obj["message"]) == 0 && len(obj["detail"]) == 0 && len(obj["error"]) == 0 {
		coverage.unknownShape.Add(1)
	}
	if !changed {
		return data
	}
	next, err := json.Marshal(obj)
	if err != nil {
		return data
	}
	return next
}

// HasMappedModel is used by offline checks; the replacement uses the exact same
// boundary and client-name exclusion rules as the production scrubber.
func (s *Scrubber) HasMappedModel(text, model string) bool { return s.modelsText(text, model) != text }

// Body accepts either one JSON response or a sequence of SSE events, as stored
// in user error details. Unknown/non-JSON data retains the existing behavior.
func (s *Scrubber) Body(data []byte, model string, errorContext bool) []byte {
	if json.Valid(data) {
		return s.JSON(data, model, errorContext)
	}
	if !bytes.Contains(data, []byte("data:")) {
		if len(bytes.TrimSpace(data)) > 0 {
			coverage.invalidJSON.Add(1)
		}
		return data
	}
	var out bytes.Buffer
	for len(data) > 0 {
		end := -1
		start := 0
		for start < len(data) {
			n := bytes.IndexByte(data[start:], '\n')
			if n < 0 {
				break
			}
			n += start + 1
			if len(bytes.TrimSuffix(data[start:n-1], []byte("\r"))) == 0 {
				end = n
				break
			}
			start = n
		}
		if end < 0 {
			out.Write(s.SSE(data, model))
			break
		}
		out.Write(s.SSE(data[:end], model))
		data = data[end:]
	}
	return out.Bytes()
}

// SSE processes complete events (including multi-line data), retaining event,
// id, retry, comments and line ending style. Unchanged events stay byte-identical.
func (s *Scrubber) SSE(frame []byte, model string) []byte {
	lines := bytes.SplitAfter(frame, []byte("\n"))
	var data [][]byte
	var event string
	for _, line := range lines {
		line = bytes.TrimRight(line, "\r\n")
		if bytes.HasPrefix(line, []byte("event:")) {
			event = strings.TrimSpace(string(line[6:]))
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			v := line[5:]
			if len(v) > 0 && v[0] == ' ' {
				v = v[1:]
			}
			data = append(data, v)
		}
	}
	if len(data) == 0 {
		return frame
	}
	original := bytes.Join(data, []byte("\n"))
	next := s.JSON(original, model, event == "error" || event == "response.failed")
	if bytes.Equal(original, next) {
		return frame
	}
	var out bytes.Buffer
	written := false
	for _, line := range lines {
		if bytes.HasPrefix(line, []byte("data:")) {
			if written {
				continue
			}
			written = true
			out.WriteString("data: ")
			out.Write(next)
			if bytes.HasSuffix(line, []byte("\r\n")) {
				out.WriteString("\r\n")
			} else if bytes.HasSuffix(line, []byte("\n")) {
				out.WriteByte('\n')
			}
		} else {
			out.Write(line)
		}
	}
	return out.Bytes()
}
