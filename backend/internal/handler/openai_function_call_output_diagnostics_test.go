package handler

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func encodeInputShape(t *testing.T, body string) (string, map[string]any) {
	t.Helper()
	enc := zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	buf, err := enc.EncodeEntry(zapcore.Entry{Message: "m"}, []zapcore.Field{functionCallOutputInputShapeField([]byte(body))})
	require.NoError(t, err)
	encoded := buf.String()
	var decoded map[string]any
	require.NoError(t, json.Unmarshal([]byte(encoded), &decoded))
	shape, ok := decoded["input_shape"].(map[string]any)
	require.True(t, ok)
	return encoded, shape
}

func shapeItems(t *testing.T, shape map[string]any) []any {
	t.Helper()
	items, ok := shape["items"].([]any)
	require.True(t, ok)
	return items
}

func shapeFirstMissing(t *testing.T, shape map[string]any) map[string]any {
	t.Helper()
	missing, ok := shape["first_missing"].(map[string]any)
	require.True(t, ok)
	return missing
}

func TestFunctionCallOutputInputShapeOmitsContent(t *testing.T) {
	body := `{"input":[` +
		`{"type":"message","role":"SECRET_ROLE","content":"SECRET_TEXT"},` +
		`{"type":"function_call_output","namespace":"agent","name":{"content":"SECRET_NAME"},"output":"SECRET_OUTPUT"},` +
		`{"type":"function_call_output","call_id":"call_1","name":"read_file","output":"SECRET_RESULT"}]}`
	encoded, shape := encodeInputShape(t, body)
	for _, secret := range []string{"SECRET_ROLE", "SECRET_TEXT", "SECRET_NAME", "SECRET_OUTPUT", "SECRET_RESULT", `"role"`, `"output"`, `"content"`} {
		require.NotContains(t, encoded, secret)
	}
	require.EqualValues(t, 3, shape["total"])
	items := shapeItems(t, shape)
	require.Len(t, items, 3)
	require.Equal(t, map[string]any{"type": "message", "has_call_id": false}, items[0])
	require.Equal(t, map[string]any{"type": "function_call_output", "namespace": "agent", "has_call_id": false}, items[1])
	require.Equal(t, map[string]any{"type": "function_call_output", "name": "read_file", "has_call_id": true}, items[2])
	require.Equal(t, map[string]any{"type": "function_call_output", "namespace": "agent", "has_call_id": false, "index": float64(1)}, shape["first_missing"])
}

func TestFunctionCallOutputInputShapeFindsTriggerBeyondCap(t *testing.T) {
	parts := make([]string, 0, 65)
	for i := 0; i < 64; i++ {
		parts = append(parts, fmt.Sprintf(`{"type":"message","role":"user","content":"m%d"}`, i))
	}
	parts = append(parts, `{"type":"function_call_output","namespace":"agent","name":"codex_apps_open_page","call_id":"  ","output":"x"}`)
	_, shape := encodeInputShape(t, `{"input":[`+strings.Join(parts, ",")+`]}`)
	require.EqualValues(t, 65, shape["total"])
	require.Len(t, shapeItems(t, shape), 64)
	missing := shapeFirstMissing(t, shape)
	require.EqualValues(t, 64, missing["index"])
	require.Equal(t, "codex_apps_open_page", missing["name"])
}

func TestFunctionCallOutputInputShapeUsesValidatorRules(t *testing.T) {
	body := `{"input":[{"type":"function_call_output","call_id":42,"output":"x"},{"type":"custom_tool_call_output","name":"apply_patch","output":"y"}]}`
	_, shape := encodeInputShape(t, body)
	missing := shapeFirstMissing(t, shape)
	require.EqualValues(t, 1, missing["index"])
	require.Equal(t, "custom_tool_call_output", missing["type"])
	first, ok := shapeItems(t, shape)[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, first["has_call_id"])
}

func TestFunctionCallOutputInputShapeTruncatesAndHandlesNonArray(t *testing.T) {
	long := strings.Repeat("名", 30) // 90 bytes
	_, shape := encodeInputShape(t, `{"input":[{"type":"function_call_output","name":"`+long+`"}]}`)
	first, ok := shapeItems(t, shape)[0].(map[string]any)
	require.True(t, ok)
	name, ok := first["name"].(string)
	require.True(t, ok)
	require.LessOrEqual(t, len(name), functionCallOutputShapeMaxValueLen)
	require.True(t, strings.HasPrefix(long, name))
	require.Equal(t, strings.Repeat("名", 21), name)

	for _, body := range []string{`{"input":"hello"}`, `{"model":"gpt-5"}`} {
		_, shape := encodeInputShape(t, body)
		require.EqualValues(t, 0, shape["total"])
		require.Empty(t, shape["items"])
		require.NotContains(t, shape, "first_missing")
	}
}

func TestFunctionCallOutputInputShapeDropsNUL(t *testing.T) {
	body := `{"input":[{"type":"function_call_output","namespace":"a\u0000gent","name":"read\u0000_file"}]}`
	encoded, shape := encodeInputShape(t, body)
	require.NotContains(t, encoded, `\u0000`)
	missing := shapeFirstMissing(t, shape)
	require.Equal(t, "agent", missing["namespace"])
	require.Equal(t, "read_file", missing["name"])
	item, ok := shapeItems(t, shape)[0].(map[string]any)
	require.True(t, ok)
	require.Equal(t, "agent", item["namespace"])
}
