package handler

import (
	"strings"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/tidwall/gjson"
	"go.uber.org/zap"
)

const (
	functionCallOutputShapeMaxItems    = 64
	functionCallOutputShapeMaxValueLen = 64
)

// functionCallOutputInputShapeField describes the structure of a request's
// input when it is rejected for a tool output without call_id: item types,
// namespaces and names, and which item tripped the check. It never logs item
// content, and records the descriptive fields only when they are strings.
func functionCallOutputInputShapeField(body []byte) zap.Field {
	items := make([]map[string]any, 0)
	shape := map[string]any{"total": 0, "items": items}
	input := gjson.GetBytes(body, "input")
	if !input.IsArray() {
		return zap.Any("input_shape", shape)
	}
	total := 0
	var firstMissing map[string]any
	input.ForEach(func(_, item gjson.Result) bool {
		index := total
		total++
		if index < functionCallOutputShapeMaxItems {
			items = append(items, functionCallOutputItemShape(item))
		}
		if firstMissing == nil && item.IsObject() &&
			service.IsCodexToolCallOutputItemType(item.Get("type").String()) &&
			service.CodexToolCallOutputCallIDMissing(item) {
			firstMissing = functionCallOutputItemShape(item)
			firstMissing["index"] = index
		}
		return true
	})
	shape["total"] = total
	shape["items"] = items
	if firstMissing != nil {
		shape["first_missing"] = firstMissing
	}
	return zap.Any("input_shape", shape)
}

func functionCallOutputItemShape(item gjson.Result) map[string]any {
	shape := map[string]any{}
	if !item.IsObject() {
		return shape
	}
	for _, key := range []string{"type", "namespace", "name"} {
		value := item.Get(key)
		if value.Type != gjson.String {
			continue
		}
		text := value.Str
		// Clone so the queued log event holds only these bytes, not the
		// whole input copy the substring would otherwise keep alive.
		// Drop NUL first: ops_system_logs.extra is jsonb, which rejects
		// \u0000 and would fail the whole log batch.
		text = strings.ReplaceAll(text, "\x00", "")
		shape[key] = strings.Clone(functionCallOutputShapeTruncate(text))
	}
	shape["has_call_id"] = !service.CodexToolCallOutputCallIDMissing(item)
	return shape
}

// functionCallOutputShapeTruncate cuts text to at most
// functionCallOutputShapeMaxValueLen bytes without splitting a UTF-8 rune.
func functionCallOutputShapeTruncate(text string) string {
	if len(text) <= functionCallOutputShapeMaxValueLen {
		return text
	}
	cut := functionCallOutputShapeMaxValueLen
	for cut > 0 && (text[cut]&0xC0) == 0x80 {
		cut--
	}
	return text[:cut]
}
