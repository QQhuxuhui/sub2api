package service

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"
)

// openAIUpstreamCallIDMaxLength is the official Responses limit on call_id
// ("string_above_max_length ... maximum length 64").
const openAIUpstreamCallIDMaxLength = 64

var openAICallIDKey = []byte(`"call_id"`)

// ShortenOpenAIOverlongCallIDs replaces every input call_id longer than 64
// characters with "call_" + the first 32 hex digits of its SHA-256, and returns
// how many fields it changed. Some third-party upstreams mint longer call_ids;
// Codex replays them and every later turn on an official upstream fails with a
// 400 the client cannot recover from.
//
// The mapping only depends on the original value, so a call and its outputs
// still match, and a replayed history maps to the same bytes every turn. A value
// is only rewritten when its call is in this request's input: an output whose
// call lives upstream (previous_response_id) or that item_reference points at
// would lose its pair. A body without an overlong call_id is returned unchanged
// without being parsed.
func ShortenOpenAIOverlongCallIDs(body []byte) ([]byte, int) {
	if !openAIBodyMayHaveOverlongCallID(body) {
		return body, 0
	}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return body, 0
	}

	type occurrence struct {
		path  string
		value string
	}
	var occurrences []occurrence
	anchored := make(map[string]bool)
	referenced := make(map[string]bool)
	input.ForEach(func(index, item gjson.Result) bool {
		if !item.IsObject() {
			return true
		}
		itemType := item.Get("type").String()
		if itemType == "item_reference" {
			if id := item.Get("id"); id.Type == gjson.String {
				referenced[id.String()] = true
			}
			return true
		}
		callID := item.Get("call_id")
		if callID.Type != gjson.String || utf8.RuneCountInString(callID.String()) <= openAIUpstreamCallIDMaxLength {
			return true
		}
		value := callID.String()
		occurrences = append(occurrences, occurrence{
			path:  fmt.Sprintf("input.%d.call_id", index.Int()),
			value: value,
		})
		if !strings.HasSuffix(itemType, "_output") {
			anchored[value] = true
		}
		return true
	})

	changed := 0
	for _, o := range occurrences {
		if !anchored[o.value] || referenced[o.value] {
			continue
		}
		next, err := sjson.SetBytes(body, o.path, shortOpenAICallID(o.value))
		if err != nil {
			continue
		}
		body = next
		changed++
	}
	return body, changed
}

func shortOpenAICallID(value string) string {
	digest := sha256.Sum256([]byte(value))
	return "call_" + hex.EncodeToString(digest[:])[:32]
}

// openAIBodyMayHaveOverlongCallID scans for a "call_id" key whose raw string
// value is longer than 64 bytes. A raw length within 64 means the decoded value
// is too, so no overlong value is missed unless the key itself is escaped.
func openAIBodyMayHaveOverlongCallID(body []byte) bool {
	rest := body
	for {
		at := bytes.Index(rest, openAICallIDKey)
		if at < 0 {
			return false
		}
		rest = rest[at+len(openAICallIDKey):]
		i := skipJSONSpace(rest, 0)
		if i >= len(rest) || rest[i] != ':' {
			continue
		}
		i = skipJSONSpace(rest, i+1)
		if i >= len(rest) || rest[i] != '"' {
			continue
		}
		start := i + 1
		end := start
		for end < len(rest) && rest[end] != '"' {
			if rest[end] == '\\' {
				end++
			}
			end++
		}
		if end > len(rest) {
			end = len(rest)
		}
		if end-start > openAIUpstreamCallIDMaxLength {
			return true
		}
		if end >= len(rest) {
			return false
		}
		rest = rest[end+1:]
	}
}

func skipJSONSpace(b []byte, i int) int {
	for i < len(b) {
		switch b[i] {
		case ' ', '\t', '\n', '\r':
			i++
		default:
			return i
		}
	}
	return i
}
