//go:build unit

package service

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// hotPathBenchBody 模拟线上大请求：工具定义、系统提示、带图片的首条用户消息和工具调用历史。
func hotPathBenchBody(size int) []byte {
	tools := make([]map[string]any, 0, 40)
	for i := 0; i < 40; i++ {
		tools = append(tools, map[string]any{
			"type": "function", "name": fmt.Sprintf("tool_%d", i), "description": strings.Repeat("describe ", 40),
			"parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}},
		})
	}
	input := []any{
		map[string]any{"role": "developer", "content": strings.Repeat("instructions ", 400)},
		map[string]any{"role": "user", "content": []any{
			map[string]any{"type": "input_text", "text": "first question"},
			map[string]any{"type": "input_image", "image_url": "data:image/png;base64," + strings.Repeat("QUJD", size/16)},
		}},
	}
	used := size / 4
	for i := 0; used < size; i++ {
		output := strings.Repeat("line of tool output with \"quotes\"\n", 300)
		input = append(input,
			map[string]any{"type": "function_call", "call_id": fmt.Sprintf("call_%d", i), "name": "tool_1", "arguments": `{"path":"a"}`},
			map[string]any{"type": "function_call_output", "call_id": fmt.Sprintf("call_%d", i), "output": output},
		)
		used += len(output) + 120
	}
	body, err := json.Marshal(map[string]any{"model": "gpt-6-astra", "stream": true, "store": false, "instructions": "be helpful", "tools": tools, "input": input})
	if err != nil {
		panic(err)
	}
	return body
}

// 基线：一个请求里这些函数的实际调用次数（handler + Forward 各一次种子，HasCompaction 三处）。
func hotPathBaseline(body []byte) {
	for i := 0; i < 2; i++ {
		_ = referenceSeed(body)
	}
	for i := 0; i < 3; i++ {
		_ = referenceHasCompactionTrigger(body)
	}
	_, _, _ = referenceNormalizeCompactionTrigger(body)
	_ = referenceMayContainEmptyBase64InputImage(body)
	_, _, _ = sanitizeOpenAIResponsesToolSchemasForPlatform(body, PlatformOpenAI)
}

func hotPathOptimized(c *gin.Context, svc *OpenAIGatewayService, body []byte) {
	EnableOpenAISessionSeedMemo(c)
	_ = svc.GenerateSessionHash(c, body)
	memo := takeOpenAISessionSeedMemo(c)
	_ = svc.generateSessionHashWithMemo(c, body, memo)
	for i := 0; i < 3; i++ {
		_ = HasCompactionTriggerInInput(body)
	}
	_, _, _ = NormalizeCompactionTriggerInputOrder(body)
	_ = openAIRequestBodyMayContainEmptyBase64InputImage(body)
	_, _, _ = sanitizeOpenAIResponsesToolSchemasForPlatform(body, PlatformOpenAI)
}

func BenchmarkOpenAIResponsesHotPath(b *testing.B) {
	gin.SetMode(gin.TestMode)
	for _, size := range []int{1 << 20, 4 << 20} {
		body := hotPathBenchBody(size)
		b.Run(fmt.Sprintf("baseline_%dMB", size>>20), func(b *testing.B) {
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				hotPathBaseline(body)
			}
		})
		b.Run(fmt.Sprintf("optimized_%dMB", size>>20), func(b *testing.B) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/responses", nil)
			svc := &OpenAIGatewayService{}
			b.SetBytes(int64(len(body)))
			b.ReportAllocs()
			for b.Loop() {
				hotPathOptimized(c, svc, body)
			}
		})
	}
}
