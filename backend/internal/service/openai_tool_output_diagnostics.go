package service

import (
	"strings"

	"github.com/tidwall/gjson"
)

// IsCodexToolCallOutputItemType reports whether an input item type is one of
// the tool outputs ValidateFunctionCallOutputContextBytes checks for call_id.
func IsCodexToolCallOutputItemType(typ string) bool {
	return isCodexToolCallOutputItemType(typ)
}

// CodexToolCallOutputCallIDMissing reports whether a tool output item lacks a
// call_id by the same test ValidateFunctionCallOutputContextBytes applies.
func CodexToolCallOutputCallIDMissing(item gjson.Result) bool {
	return strings.TrimSpace(item.Get("call_id").String()) == ""
}
