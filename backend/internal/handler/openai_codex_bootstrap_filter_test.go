//go:build unit

package handler

import (
	"fmt"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func codexBootstrapFilterSamples() []string {
	automation := "Automation: nightly\nAutomation ID: nightly-1\nAutomation memory: $CODEX_HOME/automations/nightly-1/memory.md\nLast run: never\n\nDo the work."
	delegation := "<codex_delegation><source_thread_id>t1</source_thread_id><input>task</input></codex_delegation>"
	item := func(namespace, name, output string) string {
		return fmt.Sprintf(`{"type":"function_call_output","namespace":%q,"name":%q,"output":%q}`, namespace, name, output)
	}
	tools := `"tools":[{"namespace":"codex_app","name":"create_thread"},{"namespace":"codex_app","name":"automation_update"}]`
	return []string{
		``, `{`, `[]`, `null`, `{}`, `{"input":"x"}`, `{"input":[]}`,
		`{` + tools + `,"input":[{"role":"user","content":"hi"}]}`,
		`{` + tools + `,"input":[{"type":"function_call_output","call_id":"c1","output":"x"}]}`,
		`{"input":[` + item("codex_app", "automation_update", automation) + `]}`,
		`{"input":[` + item("codex_app", "automation_update", "not bootstrap") + `]}`,
		`{"input":[` + item("codex_tui", "automation_update", automation) + `]}`,
		`{"input":[` + item("codex_app", "create_thread", delegation) + `]}`,
		`{"input":[` + item("codex_tui", "send_message_to_thread", delegation) + `]}`,
		`{"input":[` + item("codex_app", "other", delegation) + `]}`,
		`{"input":[{"role":"user","content":"x"},` + item("codex_app", "automation_update", automation) + `]}`,
		`{"previous_response_id":"resp_1","input":[` + item("codex_app", "automation_update", automation) + `]}`,
		`{"input":[` + item("codex_app", "automation_update", automation) + `],"input":[]}`,
		`{"input":[{"type":"function_call_output","type":"x","namespace":"codex_app","name":"automation_update","output":"o"}]}`,
		`{"input":[{"type":"function_call_output","namespace":1,"name":"automation_update","output":"o"}]}`,
		strings.ReplaceAll(`{"input":[`+item("codex_app", "automation_update", automation)+`]}`, `"namespace"`, `"namespace"`),
		strings.ReplaceAll(`{"input":[`+item("codex_app", "automation_update", automation)+`]}`, `"function_call_output"`, `"function_call_output"`),
		strings.ReplaceAll(`{"input":[`+item("codex_app", "automation_update", automation)+`]}`, `"codex_app"`, `"codex_app"`),
	}
}

// 过滤后的入口必须与未过滤的 normalizeCodexCallOutputBootstrap 完全一致。
func assertCodexBootstrapFilterMatches(t *testing.T, body []byte) {
	t.Helper()
	wantBody, wantChanged := normalizeCodexCallOutputBootstrap(body, isCodexAutomationCandidate, false)
	gotBody, gotChanged := normalizeCodexAutomationBootstrap(body)
	require.Equal(t, wantChanged, gotChanged, "automation: %q", body)
	require.Equal(t, wantBody, gotBody, "automation: %q", body)

	wantBody, wantChanged = normalizeCodexCallOutputBootstrap(body, isCodexDelegationCandidate, true)
	gotBody, gotChanged = normalizeCodexDelegationBootstrap(body)
	require.Equal(t, wantChanged, gotChanged, "delegation: %q", body)
	require.Equal(t, wantBody, gotBody, "delegation: %q", body)
}

func TestCodexBootstrapFilters_MatchUnfiltered(t *testing.T) {
	for _, sample := range codexBootstrapFilterSamples() {
		assertCodexBootstrapFilterMatches(t, []byte(sample))
	}
}

func TestCodexBootstrapFilters_SampleRewrites(t *testing.T) {
	// 样例要真的触发改写，否则差分测不到通过过滤的分支。
	rewritten := 0
	for _, sample := range codexBootstrapFilterSamples() {
		if _, changed := normalizeCodexAutomationBootstrap([]byte(sample)); changed {
			rewritten++
		}
	}
	require.Greater(t, rewritten, 0)
}

func FuzzCodexBootstrapFiltersMatchUnfiltered(f *testing.F) {
	for _, sample := range codexBootstrapFilterSamples() {
		f.Add([]byte(sample))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		assertCodexBootstrapFilterMatches(t, body)
	})
}
