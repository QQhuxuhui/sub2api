package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// openAIStreamEventCarriesNoOutput reports events some relays send before the
// model has produced anything, to fake an early first token. Seen on group 4 on
// 2026-11-19 (168 failures that could no longer fail over in 90 minutes):
//
//   - response.audio.done carrying only type and sequence_number (accounts
//     286/225/142, right before "Upstream service temporarily unavailable");
//   - codex.rate_limits, codex.client.keepalive, proxy.upstream_error and
//     sub2api.gateway_ready status frames;
//   - a reasoning summary part whose text is a lone zero-width space;
//   - a reasoning item that carries only encrypted_content.
//
// Committing the stream on one of them turned the failure that followed into
// one that went straight to the client. Staged like the preamble instead, they
// reach the client in order with the first real output, or are dropped with the
// failed attempt. Only a JSON object payload qualifies, so [DONE] and plain text
// sent under one of these event names keep their old handling.
func openAIStreamEventCarriesNoOutput(data, eventType string) bool {
	// Filter by type first: every stream frame passes through here several
	// times, and large output frames must not pay for a JSON validation.
	switch eventType {
	case "response.audio.done",
		"codex.rate_limits", "codex.client.keepalive", "proxy.upstream_error", "sub2api.gateway_ready",
		"response.reasoning_summary_part.added", "response.reasoning_summary_part.done",
		"response.output_item.added":
	default:
		return false
	}
	trimmed := strings.TrimSpace(data)
	if !strings.HasPrefix(trimmed, "{") || !gjson.Valid(trimmed) {
		return false
	}
	switch eventType {
	case "response.audio.done",
		"codex.rate_limits", "codex.client.keepalive", "proxy.upstream_error", "sub2api.gateway_ready":
		return true
	case "response.reasoning_summary_part.added", "response.reasoning_summary_part.done":
		part := gjson.Get(trimmed, "part")
		if !part.IsObject() || part.Get("type").String() != "summary_text" {
			return false
		}
		text := part.Get("text")
		return text.Type == gjson.String && openAIStreamTextIsBlank(text.String()) &&
			part.Get("transcript").String() == ""
	case "response.output_item.added":
		item := gjson.Get(trimmed, "item")
		if !item.IsObject() || item.Get("type").String() != "reasoning" {
			return false
		}
		encrypted := item.Get("encrypted_content")
		if encrypted.Type != gjson.String || encrypted.String() == "" {
			return false
		}
		return openAIStreamNoopListIsEmpty(item.Get("summary")) &&
			openAIStreamNoopListIsEmpty(item.Get("content")) &&
			!openAIStreamItemHasVisibleOutput(item)
	}
	return false
}

// openAIStreamNoopListIsEmpty is true for a missing, null or empty-array field.
// Any element at all leaves the item to the existing judgement.
func openAIStreamNoopListIsEmpty(value gjson.Result) bool {
	if !value.Exists() || value.Type == gjson.Null {
		return true
	}
	return value.IsArray() && len(value.Array()) == 0
}
