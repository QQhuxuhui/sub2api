package service

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/tidwall/gjson"
)

const (
	// iqShowcaseMaxAnswerBytes caps the text gathered from the stream.
	iqShowcaseMaxAnswerBytes = 1 << 20
	// iqShowcaseMaxStreamBytes caps everything read from the stream, deltas
	// carry JSON overhead around the text.
	iqShowcaseMaxStreamBytes = 16 << 20
	iqShowcaseMaxErrorBytes  = 2 << 10
	// Anthropic requires max_tokens; this is used when the item leaves it 0.
	iqShowcaseAnthropicDefaultMaxTokens = 16384
)

// BuildIQShowcaseRequest builds the request a showcase probe sends to the
// gateway. It looks exactly like a client request: JSON body, streaming, the
// key in the header the protocol's clients use, and no internal marker, so the
// gateway applies the same auth, intent routing, scheduling and billing as for
// real traffic. Content-Type matters: intent routing skips non-JSON bodies.
func BuildIQShowcaseRequest(ctx context.Context, baseURL, apiKey string, item *IQShowcaseItem) (*http.Request, error) {
	base := strings.TrimRight(baseURL, "/")
	var (
		path    string
		payload map[string]any
	)
	switch item.Protocol {
	case IQShowcaseProtocolOpenAIResponses:
		path = "/v1/responses"
		payload = map[string]any{
			"model": item.Model,
			"input": []map[string]any{{
				"role":    "user",
				"content": []map[string]any{{"type": "input_text", "text": item.Prompt}},
			}},
			"stream": true,
		}
		if item.ReasoningEffort != "" {
			payload["reasoning"] = map[string]any{"effort": item.ReasoningEffort}
		}
		if item.MaxOutputTokens > 0 {
			payload["max_output_tokens"] = item.MaxOutputTokens
		}
	case IQShowcaseProtocolOpenAIChat:
		path = "/v1/chat/completions"
		payload = map[string]any{
			"model":    item.Model,
			"messages": []map[string]any{{"role": "user", "content": item.Prompt}},
			"stream":   true,
		}
		if item.ReasoningEffort != "" {
			payload["reasoning_effort"] = item.ReasoningEffort
		}
		if item.MaxOutputTokens > 0 {
			payload["max_tokens"] = item.MaxOutputTokens
		}
	case IQShowcaseProtocolAnthropicMessages:
		path = "/v1/messages"
		maxTokens := item.MaxOutputTokens
		if maxTokens <= 0 {
			maxTokens = iqShowcaseAnthropicDefaultMaxTokens
		}
		payload = map[string]any{
			"model":      item.Model,
			"max_tokens": maxTokens,
			"messages":   []map[string]any{{"role": "user", "content": item.Prompt}},
			"stream":     true,
		}
	case IQShowcaseProtocolGemini:
		path = "/v1beta/models/" + url.PathEscape(item.Model) + ":streamGenerateContent?alt=sse"
		payload = map[string]any{
			"contents": []map[string]any{{"role": "user", "parts": []map[string]any{{"text": item.Prompt}}}},
		}
		if item.MaxOutputTokens > 0 {
			payload["generationConfig"] = map[string]any{"maxOutputTokens": item.MaxOutputTokens}
		}
	default:
		return nil, fmt.Errorf("unknown protocol %q", item.Protocol)
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	switch item.Protocol {
	case IQShowcaseProtocolGemini:
		req.Header.Set("x-goog-api-key", apiKey)
	case IQShowcaseProtocolAnthropicMessages:
		req.Header.Set("x-api-key", apiKey)
		req.Header.Set("anthropic-version", "2023-06-01")
	default:
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	return req, nil
}

// iqShowcaseStreamState gathers the answer text and decides whether the
// stream ended normally. Any failure signal wins over gathered text.
type iqShowcaseStreamState struct {
	protocol string
	text     strings.Builder
	done     bool  // a normal end was seen
	failure  error // first failure signal
}

func (st *iqShowcaseStreamState) fail(format string, a ...any) {
	if st.failure == nil {
		st.failure = fmt.Errorf(format, a...)
	}
}

func (st *iqShowcaseStreamState) appendText(s string) {
	if st.failure != nil || s == "" {
		return
	}
	if st.text.Len()+len(s) > iqShowcaseMaxAnswerBytes {
		st.fail("answer is larger than %d bytes", iqShowcaseMaxAnswerBytes)
		return
	}
	st.text.WriteString(s)
}

// handle processes one SSE data payload.
func (st *iqShowcaseStreamState) handle(data string) {
	data = strings.TrimSpace(data)
	if data == "" {
		return
	}
	if data == "[DONE]" {
		if st.protocol == IQShowcaseProtocolOpenAIChat {
			st.done = true
		}
		return
	}
	if !gjson.Valid(data) {
		st.fail("stream carried a non-JSON event: %s", truncateIQShowcaseError(data))
		return
	}
	ev := gjson.Parse(data)
	switch st.protocol {
	case IQShowcaseProtocolOpenAIResponses:
		st.handleResponses(ev, data)
	case IQShowcaseProtocolOpenAIChat:
		st.handleChat(ev, data)
	case IQShowcaseProtocolAnthropicMessages:
		st.handleAnthropic(ev, data)
	case IQShowcaseProtocolGemini:
		st.handleGemini(ev, data)
	}
}

func (st *iqShowcaseStreamState) handleResponses(ev gjson.Result, raw string) {
	switch ev.Get("type").String() {
	case "response.output_text.delta":
		st.appendText(ev.Get("delta").String())
	case "response.completed":
		st.done = true
		// Some upstreams send no deltas; take the final text then.
		if st.text.Len() == 0 {
			ev.Get("response.output").ForEach(func(_, item gjson.Result) bool {
				item.Get("content").ForEach(func(_, part gjson.Result) bool {
					if part.Get("type").String() == "output_text" {
						st.appendText(part.Get("text").String())
					}
					return true
				})
				return true
			})
		}
	case "response.failed", "response.incomplete", "error":
		st.fail("upstream reported %s", truncateIQShowcaseError(raw))
	}
}

func (st *iqShowcaseStreamState) handleChat(ev gjson.Result, raw string) {
	if ev.Get("error").Exists() {
		st.fail("upstream reported %s", truncateIQShowcaseError(raw))
		return
	}
	choice := ev.Get("choices.0")
	if !choice.Exists() {
		return
	}
	st.appendText(choice.Get("delta.content").String())
	reason := choice.Get("finish_reason")
	if reason.Exists() && reason.Type != gjson.Null && reason.String() != "" {
		if reason.String() == "stop" {
			// The gateway does not add [DONE] for upstreams that end with a
			// finish_reason and EOF, so stop counts as a normal end.
			st.done = true
		} else {
			st.fail("answer ended with finish_reason=%s", reason.String())
		}
	}
}

func (st *iqShowcaseStreamState) handleAnthropic(ev gjson.Result, raw string) {
	switch ev.Get("type").String() {
	case "content_block_delta":
		if ev.Get("delta.type").String() == "text_delta" {
			st.appendText(ev.Get("delta.text").String())
		}
	case "message_delta":
		switch reason := ev.Get("delta.stop_reason").String(); reason {
		case "", "end_turn", "stop_sequence":
		default:
			st.fail("answer ended with stop_reason=%s", reason)
		}
	case "message_stop":
		st.done = true
	case "error":
		st.fail("upstream reported %s", truncateIQShowcaseError(raw))
	}
}

func (st *iqShowcaseStreamState) handleGemini(ev gjson.Result, raw string) {
	if ev.Get("error").Exists() {
		st.fail("upstream reported %s", truncateIQShowcaseError(raw))
		return
	}
	if block := ev.Get("promptFeedback.blockReason").String(); block != "" {
		st.fail("prompt was blocked: %s", block)
		return
	}
	cand := ev.Get("candidates.0")
	if !cand.Exists() {
		return
	}
	cand.Get("content.parts").ForEach(func(_, part gjson.Result) bool {
		if !part.Get("thought").Bool() {
			st.appendText(part.Get("text").String())
		}
		return true
	})
	if reason := cand.Get("finishReason").String(); reason != "" {
		if reason == "STOP" {
			st.done = true
		} else {
			st.fail("answer ended with finishReason=%s", reason)
		}
	}
}

// readIQShowcaseStream reads an SSE body to the end and returns the answer
// text, or why the round failed.
func readIQShowcaseStream(protocol string, body io.Reader) (string, error) {
	st := &iqShowcaseStreamState{protocol: protocol}
	limited := &io.LimitedReader{R: body, N: iqShowcaseMaxStreamBytes + 1}
	reader := bufio.NewReaderSize(limited, 64<<10)
	var data strings.Builder
	flush := func() {
		if data.Len() > 0 {
			st.handle(data.String())
			data.Reset()
		}
	}
	for {
		line, err := reader.ReadString('\n')
		if len(line) > 0 {
			trimmed := strings.TrimRight(line, "\r\n")
			switch {
			case trimmed == "":
				flush()
			case strings.HasPrefix(trimmed, "data:"):
				if data.Len() > 0 {
					data.WriteByte('\n')
				}
				data.WriteString(strings.TrimPrefix(strings.TrimPrefix(trimmed, "data:"), " "))
			}
		}
		if st.failure != nil {
			return "", st.failure
		}
		if limited.N <= 0 {
			return "", fmt.Errorf("stream is larger than %d bytes", iqShowcaseMaxStreamBytes)
		}
		if err == io.EOF {
			flush()
			break
		}
		if err != nil {
			return "", fmt.Errorf("stream read failed: %v", err)
		}
	}
	if st.failure != nil {
		return "", st.failure
	}
	if !st.done {
		return "", errors.New("stream ended without a normal end (interrupted)")
	}
	return st.text.String(), nil
}

func truncateIQShowcaseError(s string) string {
	s = strings.TrimSpace(s)
	if len(s) <= iqShowcaseMaxErrorBytes {
		return s
	}
	cut := iqShowcaseMaxErrorBytes
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return s[:cut] + "…"
}
