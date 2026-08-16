//go:build unit

package teamops

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/require"
)

// /summary 与 /rows 的 JSON 键是前端**唯一**的契约，而全分支回扫实测：
// 把这 20+ 个键随便改名，全套后端测试照样全绿（6 条改名变异全部存活）。
// 也就是说改坏契约不会有任何东西报警，只会让前端页面上一片空白或 NaN。
//
// 断言用**完整键集合**（ElementsMatch）而不是「某几个键存在」：
// 后者在增键时不红（漏了没人知道）、删键时也不红（除非恰好断到那个键）。
// 只有全集比对才能同时钉住「改名」「删键」「悄悄加键」三种。
//
// 加键是正常演进 —— 这条红了不代表出错，代表**该同步前端了**：
// 更新下面的清单，并确认前端的 types.ts 也跟上。
func TestSummaryJSONKeysAreStable(t *testing.T) {
	t.Parallel()
	require.ElementsMatch(t, []string{
		"period", "compare",
		"total_cost", "total_cost_raw", "prev_cost",
		"delta_pct", "delta_abs",
		"row_count", "key_count", "deleted_key_count", "owned_key_count",
		"top_row_cost", "retention_warning", "conclusion",
	}, jsonKeys(t, SummaryDTO{}),
		"/summary 的 JSON 键集合变了。前端 features/team/types.ts 必须同步，"+
			"否则页面上对应字段会静默变成 undefined。")
}

func TestRowJSONKeysAreStable(t *testing.T) {
	t.Parallel()
	require.ElementsMatch(t, []string{
		"group_key", "display_name", "by_owner",
		"key_count", "key_count_all", "all_deleted",
		"masked_key", "last_used_at",
		"current_cost", "prev_cost", "delta_pct", "delta_abs",
		"requests", "prev_requests", "is_anomaly",
	}, jsonKeys(t, Row{}),
		"/rows 每一行的 JSON 键集合变了，前端表格列会静默失效。")
}

// 嵌套对象各自也钉一遍：前端对它们做了解构，改名同样静默失效。
func TestNestedJSONKeysAreStable(t *testing.T) {
	t.Parallel()
	require.ElementsMatch(t, []string{"start_date", "end_date", "timezone"}, jsonKeys(t, PeriodDTO{}))
	require.ElementsMatch(t, []string{"start_date", "end_date", "comparable", "reason"}, jsonKeys(t, CompareDTO{}))
	require.ElementsMatch(t, []string{"kind", "cut_date"}, jsonKeys(t, RetentionWarning{}))
	require.ElementsMatch(t, []string{"kind", "group_key", "display_name", "text", "extra_count"}, jsonKeys(t, Conclusion{}))
}

// jsonKeys 走真实的 json.Marshal，而不是反射读 tag：
// omitempty、内嵌结构体展开、自定义 MarshalJSON 都会影响最终键集合，
// 只有真序列化一遍才反映前端实际收到的东西。
func jsonKeys(t *testing.T, v any) []string {
	t.Helper()
	raw, err := json.Marshal(v)
	require.NoError(t, err)
	var m map[string]any
	require.NoError(t, json.Unmarshal(raw, &m))
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
