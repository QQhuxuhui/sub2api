package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/tidwall/sjson"
)

var codexSelectedPageName = regexp.MustCompile(`^codex_apps_selected_page_part_([1-9][0-9]*)_of_([1-9][0-9]*)$`)

// Desktop injects page snapshots as standalone outputs, without a model tool call.
// Keep them as untrusted user text, including on history replay and mixed bootstrap turns.
func normalizeCodexPageContext(body []byte) ([]byte, bool) {
	if !mayContainCodexPageContextCandidate(body) || !hasUniqueJSONMembers(body) {
		return body, false
	}
	var request map[string]json.RawMessage
	if json.Unmarshal(body, &request) != nil {
		return body, false
	}
	var input []json.RawMessage
	if json.Unmarshal(request["input"], &input) != nil {
		return body, false
	}
	changed := false
	result := body
	for i, raw := range input {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil {
			continue
		}
		readString := func(key string) string {
			var value string
			_ = json.Unmarshal(item[key], &value)
			return value
		}
		if readString("type") != "function_call_output" || readString("namespace") != "agent" {
			continue
		}
		if callID, exists := item["call_id"]; exists {
			var value string
			if bytes.Equal(bytes.TrimSpace(callID), []byte("null")) || json.Unmarshal(callID, &value) != nil || strings.TrimSpace(value) != "" {
				continue
			}
		}
		name, output := readString("name"), readString("output")
		if !validCodexPageContext(name, output) {
			continue
		}
		message := map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]string{{
				"type": "input_text",
				"text": "<external_" + name + ">\n" + output + "\n</external_" + name + ">",
			}},
		}
		encoded, err := marshalCodexPageContextJSON(message)
		if err != nil {
			return body, false
		}
		// Replace only this item; the rest of the body keeps its exact bytes,
		// so untouched history is not re-encoded or grown by HTML escaping.
		// Members are unique (checked above), so the path is unambiguous.
		updated, err := sjson.SetRawBytes(result, "input."+strconv.Itoa(i), encoded)
		if err != nil {
			return body, false
		}
		result = updated
		changed = true
	}
	if !changed {
		return body, false
	}
	return result, true
}

// marshalCodexPageContextJSON encodes without HTML escaping, so the page text
// keeps its <, > and & as written instead of growing to \u003c and the like.
func marshalCodexPageContextJSON(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}

func validCodexPageContext(name, output string) bool {
	if name == "codex_apps_open_page" {
		return validCodexPageSnapshot(output)
	}
	parts := codexSelectedPageName.FindStringSubmatch(name)
	if len(parts) != 3 {
		return false
	}
	part, err := strconv.Atoi(parts[1])
	if err != nil {
		return false
	}
	total, err := strconv.Atoi(parts[2])
	if err != nil || part > total {
		return false
	}
	header, snapshot, ok := strings.Cut(output, "\n")
	if !ok || strings.TrimSuffix(header, "\r") != fmt.Sprintf("Companion Page snapshot part %d of %d:", part, total) || strings.TrimSpace(snapshot) == "" {
		return false
	}
	// A multi-part snapshot may split inside a JSON string; preserve each chunk verbatim.
	return total > 1 || validCodexPageSnapshot(snapshot)
}

func validCodexPageSnapshot(text string) bool {
	// Unmarshal first: it enforces a nesting limit, while the duplicate-member
	// scan recurses without one, so a deep snapshot must be rejected before it.
	var snapshot map[string]json.RawMessage
	if json.Unmarshal([]byte(text), &snapshot) != nil {
		return false
	}
	if !hasUniqueJSONMembers([]byte(text)) {
		return false
	}
	id, exists := snapshot["page_id"]
	if !exists {
		return false
	}
	if bytes.Equal(bytes.TrimSpace(id), []byte("null")) {
		return true
	}
	var value string
	return json.Unmarshal(id, &value) == nil && strings.TrimSpace(value) != ""
}

// mayContainCodexPageContextCandidate is a cheap pre-check that lets ordinary
// requests skip the full decode. It only decides whether to decode: an escape
// that may spell the type or tool names defers to the full path, which still
// applies every check above.
func mayContainCodexPageContextCandidate(body []byte) bool {
	if codexJSONMayEscapePlainASCII(body) {
		return true
	}
	if !bytes.Contains(body, []byte("function_call_output")) {
		return false
	}
	return bytes.Contains(body, []byte("codex_apps_open_page")) ||
		bytes.Contains(body, []byte("codex_apps_selected_page_part_"))
}

// codexJSONMayEscapePlainASCII reports whether body has a \u escape that spells
// a printable ASCII character no encoder needs to escape; only such an escape
// can hide the tool names from a literal search. Escapes real clients emit
// (non-ASCII runes such as \u4e2d, control characters, quote, backslash,
// apostrophe and the HTML-safe <, >, &) are ignored, so ordinary escaped text
// keeps the fast path. Escapes are consumed pairwise, so an escaped backslash
// followed by a literal u is plain text. A malformed or truncated escape
// counts as true. Mirrors service.JSONMayEscapePlainASCII in upstream
// (json_escape_probe.go); switch to it once that file is merged.
func codexJSONMayEscapePlainASCII(body []byte) bool {
	for i := 0; ; {
		j := bytes.IndexByte(body[i:], '\\')
		if j < 0 {
			return false
		}
		next := i + j + 1
		if next >= len(body) {
			return true
		}
		if body[next] != 'u' {
			i = next + 1
			continue
		}
		if next+5 > len(body) {
			return true
		}
		r, err := strconv.ParseUint(string(body[next+1:next+5]), 16, 32)
		if err != nil || codexJSONEscapeHidesPlainASCII(rune(r)) {
			return true
		}
		i = next + 5
	}
}

func codexJSONEscapeHidesPlainASCII(r rune) bool {
	if r < 0x20 || r >= 0x7f {
		return false
	}
	switch r {
	case '"', '\\', '<', '>', '&', '\'':
		return false
	}
	return true
}
