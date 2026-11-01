//go:build unit

package handler

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

func bootstrapBenchBody(withCallOutputs bool) []byte {
	input := []any{map[string]any{"role": "user", "content": strings.Repeat("hello ", 1000)}}
	for i := 0; i < 200; i++ {
		text := strings.Repeat("tool output line\n", 300)
		if withCallOutputs {
			input = append(input,
				map[string]any{"type": "function_call", "call_id": fmt.Sprintf("c%d", i), "name": "shell", "arguments": "{}"},
				map[string]any{"type": "function_call_output", "call_id": fmt.Sprintf("c%d", i), "output": text},
			)
		} else {
			input = append(input, map[string]any{"role": "assistant", "content": text})
		}
	}
	tools := []any{map[string]any{"namespace": "codex_app", "name": "create_thread"}, map[string]any{"namespace": "codex_app", "name": "automation_update"}}
	body, err := json.Marshal(map[string]any{"model": "m", "tools": tools, "input": input})
	if err != nil {
		panic(err)
	}
	return body
}

func BenchmarkCodexBootstrap(b *testing.B) {
	for name, body := range map[string][]byte{"plain": bootstrapBenchBody(false), "tool_history": bootstrapBenchBody(true)} {
		b.Run(name+"/baseline", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = normalizeCodexCallOutputBootstrap(body, isCodexAutomationCandidate, false)
				_, _ = normalizeCodexCallOutputBootstrap(body, isCodexDelegationCandidate, true)
			}
		})
		b.Run(name+"/optimized", func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_, _ = normalizeCodexAutomationBootstrap(body)
				_, _ = normalizeCodexDelegationBootstrap(body)
			}
		})
	}
}
