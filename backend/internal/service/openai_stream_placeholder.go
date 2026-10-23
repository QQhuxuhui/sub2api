package service

import (
	"strings"
	"unicode"

	"github.com/tidwall/gjson"
)

// openAIStreamPlaceholderMarker tags the filler events that relays built on
// sub2api emit right after accepting a request.
const openAIStreamPlaceholderMarker = "sub2api_placeholder"

// openAIStreamEventIsPlaceholder reports Responses stream events that carry
// no content yet: some relays answer at once with a whitespace-only message
// (" ") to fake an early first token and only then wait for the model. If such
// an event committed the stream, a failure the relay sends a few seconds later
// could no longer fail over to another account, so it is staged like the
// preamble instead.
//
// The marker covers the relays that tag their filler; the blank-text check
// covers any other filler made of whitespace or zero-width characters. Real
// output that merely starts with whitespace is only held until its next
// non-blank event, and a stream that ends blank is still committed by its
// terminal event.
func openAIStreamEventIsPlaceholder(data, eventType string) bool {
	eventType = strings.TrimSpace(eventType)
	if eventType == "" {
		eventType = strings.TrimSpace(gjson.Get(data, "type").String())
	}
	switch eventType {
	case "response.output_text.delta", "response.refusal.delta",
		"response.reasoning_summary_text.delta", "response.reasoning_text.delta":
		return gjson.Get(data, openAIStreamPlaceholderMarker).Bool() || openAIStreamTextIsBlank(gjson.Get(data, "delta").String())
	case "response.output_text.done", "response.reasoning_summary_text.done", "response.reasoning_text.done":
		return gjson.Get(data, openAIStreamPlaceholderMarker).Bool() || openAIStreamTextIsBlank(gjson.Get(data, "text").String())
	case "response.refusal.done":
		return gjson.Get(data, openAIStreamPlaceholderMarker).Bool() || openAIStreamTextIsBlank(gjson.Get(data, "refusal").String())
	case "response.content_part.added", "response.content_part.done":
		return gjson.Get(data, openAIStreamPlaceholderMarker).Bool() || openAIStreamPartIsBlank(gjson.Get(data, "part"))
	case "response.output_item.added", "response.output_item.done":
		if gjson.Get(data, openAIStreamPlaceholderMarker).Bool() {
			return true
		}
		item := gjson.Get(data, "item")
		if item.Get("type").String() != "message" {
			return false
		}
		for _, part := range item.Get("content").Array() {
			if !openAIStreamPartIsBlank(part) {
				return false
			}
		}
		return true
	}
	return false
}

func openAIStreamPartIsBlank(part gjson.Result) bool {
	switch part.Get("type").String() {
	case "output_text":
		return openAIStreamTextIsBlank(part.Get("text").String())
	case "refusal":
		return openAIStreamTextIsBlank(part.Get("refusal").String())
	}
	return false
}

// openAIStreamTextIsBlank treats zero-width characters as blank too, since
// they render as nothing just like whitespace.
func openAIStreamTextIsBlank(text string) bool {
	return strings.TrimFunc(text, func(r rune) bool {
		switch r {
		case '\u200b', '\u200c', '\u200d', '\u2060', '\ufeff':
			return true
		}
		return unicode.IsSpace(r)
	}) == ""
}
