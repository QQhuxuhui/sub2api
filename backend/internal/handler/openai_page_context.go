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

// codexUntrustedContextNames are the names Codex gives to context it injects
// as standalone function_call_output items (namespace "agent", no call_id).
// The selected-page entry is a prefix; the rest are exact names.
var codexUntrustedContextNames = []string{
	"codex_apps_open_page", "codex_apps_selected_page_part_",
	"browser_info", "terminal_info", "goal_objective", "untrusted_context", "untrusted_content", "ide_context", "proposed_plan",
}

// Codex injects external context (Desktop page snapshots, VS Code IDE context,
// terminal and browser info, approved plans, ...) as standalone outputs,
// without a model tool call. Keep them as untrusted user text, including on
// history replay and mixed bootstrap turns.
func normalizeCodexUntrustedContext(body []byte) ([]byte, bool) {
	if !mayContainCodexCallOutputCandidate(body, codexUntrustedContextNames...) || !hasUniqueJSONMembers(body) {
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
		name := readString("name")
		var output *string
		if json.Unmarshal(item["output"], &output) != nil || output == nil || !validCodexUntrustedContext(name, *output) {
			continue
		}
		text := "<external_" + name + ">" + *output + "</external_" + name + ">"
		if validCodexPageContext(name, *output) {
			text = "<external_" + name + ">\n" + *output + "\n</external_" + name + ">"
		}
		message := map[string]any{
			"type": "message",
			"role": "user",
			"content": []map[string]string{{
				"type": "input_text",
				"text": text,
			}},
		}
		encoded, err := marshalCodexPageContextJSON(message)
		if err != nil {
			return body, false
		}
		input[i] = encoded
		changed = true
	}
	if !changed {
		return body, false
	}
	// Rebuild the input array once from each item's original bytes (a
	// json.RawMessage keeps them verbatim) and swap it in with a single
	// replacement, so untouched history is neither re-encoded nor copied once
	// per matched item. Members are unique (checked above), so the path is
	// unambiguous.
	size := 2 + len(input)
	for _, raw := range input {
		size += len(raw)
	}
	encodedInput := make([]byte, 0, size)
	encodedInput = append(encodedInput, '[')
	for i, raw := range input {
		if i > 0 {
			encodedInput = append(encodedInput, ',')
		}
		encodedInput = append(encodedInput, raw...)
	}
	encodedInput = append(encodedInput, ']')
	result, err := sjson.SetRawBytes(body, "input", encodedInput)
	if err != nil {
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

func validCodexUntrustedContext(name, output string) bool {
	switch name {
	case "browser_info", "terminal_info", "goal_objective", "untrusted_context", "ide_context":
		return strings.TrimSpace(output) != ""
	case "untrusted_content":
		// Codex's fallback for an unnamed context source can carry any text, including an empty value.
		return true
	case "proposed_plan":
		// Codex splits an approved plan by byte length, so a chunk can be all whitespace.
		return output != ""
	default:
		return validCodexPageContext(name, output)
	}
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
