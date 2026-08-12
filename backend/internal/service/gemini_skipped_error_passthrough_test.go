package service

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/model"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ErrorPolicySkipped 分支（池模式账号、或开了自定义错误码但没列上这个码）原来
// 先把上游状态码换成 500 再往下走，错误透传规则引擎要么根本没被调用（原生链路），
// 要么只看得到 500（compat 链路）。结果：上游一个可操作的 400——内容审核拦截
// 「Your request was rejected by the upstream safety system」——到客户端变成 500，
// 客户端按服务端故障重试，同一个必然失败的提示词被反复打上来。
//
// 这里的用例把「未命中规则仍然 500」和「命中规则按规则出」两条同时钉住。

func geminiSafetyBlockBody() []byte {
	return []byte(`{"error":{"code":400,"message":"Your request was rejected by the upstream safety system. Please modify your prompt or input images and try again.","status":"INVALID_ARGUMENT"}}`)
}

// geminiKeywordPassthroughRule 复刻线上那条规则：不限状态码、不限平台，靠关键词命中，
// 状态码与消息都原样透传。
func geminiKeywordPassthroughRule(keyword string) *model.ErrorPassthroughRule {
	return &model.ErrorPassthroughRule{
		ID:              1,
		Name:            "内容审核拦截透传",
		Enabled:         true,
		Priority:        0,
		Keywords:        []string{keyword},
		MatchMode:       model.MatchModeAny,
		PassthroughCode: true,
		PassthroughBody: true,
	}
}

func bindGeminiSkippedRules(c *gin.Context, rules ...*model.ErrorPassthroughRule) {
	svc := &ErrorPassthroughService{}
	svc.setLocalCache(rules)
	BindErrorPassthroughService(c, svc)
}

func geminiSkippedAccount() *Account {
	return &Account{ID: 42, Name: "adobe2api-pool", Platform: PlatformGemini, Type: AccountTypeAPIKey}
}

func geminiSkippedUpstreamResp(status int, contentType string) *http.Response {
	h := http.Header{}
	if contentType != "" {
		h.Set("Content-Type", contentType)
	}
	return &http.Response{StatusCode: status, Header: h}
}

func newGeminiSkippedContext() (*httptest.ResponseRecorder, *gin.Context) {
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	return rec, c
}

// --- 原生 Gemini 链路 ---

func TestGeminiNativeSkipped_NoRuleKeepsThe500Convention(t *testing.T) {
	rec, c := newGeminiSkippedContext()

	svc := &GeminiMessagesCompatService{}
	err := svc.writeGeminiNativeSkippedError(c, geminiSkippedAccount(), geminiSkippedUpstreamResp(http.StatusBadRequest, "application/json"), "req-native", geminiSafetyBlockBody())

	require.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "未命中规则时必须保持既有的 500 约定")
	assert.JSONEq(t, string(geminiSafetyBlockBody()), rec.Body.String(), "未命中规则时上游响应体原样透传")
	assert.True(t, IsResponseCommitted(c))
}

func TestGeminiNativeSkipped_PassthroughRuleRestoresTheUpstreamStatus(t *testing.T) {
	rec, c := newGeminiSkippedContext()
	bindGeminiSkippedRules(c, geminiKeywordPassthroughRule("upstream safety system"))

	svc := &GeminiMessagesCompatService{}
	err := svc.writeGeminiNativeSkippedError(c, geminiSkippedAccount(), geminiSkippedUpstreamResp(http.StatusBadRequest, "application/json"), "req-native", geminiSafetyBlockBody())

	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code, "这就是客户诉求：审核拦截要返回 400 而不是 500")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField, ok := payload["error"].(map[string]any)
	require.True(t, ok, "原生链路必须保持 Google 错误结构")
	assert.Equal(t, float64(http.StatusBadRequest), errField["code"])
	assert.Equal(t, "INVALID_ARGUMENT", errField["status"])
	assert.Contains(t, errField["message"], "upstream safety system")
}

func TestGeminiNativeSkipped_RuleMatchesOnTheRealUpstreamCodeNot500(t *testing.T) {
	// 规则只挂 400。如果实现仍然拿 500 去匹配，这条就命中不了。
	rec, c := newGeminiSkippedContext()
	teapot := http.StatusTeapot
	custom := "内容被拦截"
	bindGeminiSkippedRules(c, &model.ErrorPassthroughRule{
		ID: 2, Name: "只挂 400", Enabled: true,
		ErrorCodes:    []int{http.StatusBadRequest},
		MatchMode:     model.MatchModeAll,
		ResponseCode:  &teapot,
		CustomMessage: &custom,
	})

	svc := &GeminiMessagesCompatService{}
	err := svc.writeGeminiNativeSkippedError(c, geminiSkippedAccount(), geminiSkippedUpstreamResp(http.StatusBadRequest, "application/json"), "req-native", geminiSafetyBlockBody())

	require.Error(t, err)
	assert.Equal(t, http.StatusTeapot, rec.Code)

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	errField := payload["error"].(map[string]any)
	assert.Equal(t, custom, errField["message"], "custom_message 必须能覆盖上游文案")
}

func TestGeminiNativeSkipped_SkipMonitoringIsHonoured(t *testing.T) {
	_, c := newGeminiSkippedContext()
	rule := geminiKeywordPassthroughRule("upstream safety system")
	rule.SkipMonitoring = true
	bindGeminiSkippedRules(c, rule)

	svc := &GeminiMessagesCompatService{}
	_ = svc.writeGeminiNativeSkippedError(c, geminiSkippedAccount(), geminiSkippedUpstreamResp(http.StatusBadRequest, "application/json"), "req-native", geminiSafetyBlockBody())

	// 审核拦截是客户自己的提示词问题，不该计进上游故障监控。
	v, exists := c.Get(OpsSkipPassthroughKey)
	require.True(t, exists)
	assert.Equal(t, true, v)
}

func TestGeminiNativeSkipped_UnrelatedErrorsStillReport500(t *testing.T) {
	rec, c := newGeminiSkippedContext()
	bindGeminiSkippedRules(c, geminiKeywordPassthroughRule("upstream safety system"))

	svc := &GeminiMessagesCompatService{}
	body := []byte(`{"error":{"code":400,"message":"invalid thoughtSignature"}}`)
	err := svc.writeGeminiNativeSkippedError(c, geminiSkippedAccount(), geminiSkippedUpstreamResp(http.StatusBadRequest, "application/json"), "req-native", body)

	require.Error(t, err)
	assert.Equal(t, http.StatusInternalServerError, rec.Code, "没配规则的错误不能被顺带改掉")
}

func TestGeminiNativeSkipped_EmptyContentTypeFallsBackToJSON(t *testing.T) {
	rec, c := newGeminiSkippedContext()

	svc := &GeminiMessagesCompatService{}
	_ = svc.writeGeminiNativeSkippedError(c, geminiSkippedAccount(), geminiSkippedUpstreamResp(http.StatusBadRequest, ""), "req-native", geminiSafetyBlockBody())

	assert.Contains(t, rec.Header().Get("Content-Type"), "application/json")
}

// --- compat（Claude 结构）链路 ---

func TestGeminiCompatSkipped_NoRuleKeepsTheBodyDerivedStatus(t *testing.T) {
	rec, c := newGeminiSkippedContext()

	svc := &GeminiMessagesCompatService{}
	account := &Account{ID: 7, Platform: PlatformGemini, Type: AccountTypeAPIKey}
	err := svc.writeGeminiSkippedError(c, account, geminiSkippedUpstreamResp(http.StatusBadRequest, "application/json"), "req-1", geminiSafetyBlockBody())

	require.Error(t, err)
	// 既有行为：兜底映射优先采信响应体里的 error.code，带 code 的 body 本来就出 400。
	// 这次改动不许动它。
	assert.Equal(t, http.StatusBadRequest, rec.Code)
}

func TestGeminiCompatSkipped_NoRuleNoBodyCodeStillFallsBackTo502(t *testing.T) {
	rec, c := newGeminiSkippedContext()

	svc := &GeminiMessagesCompatService{}
	account := &Account{ID: 9, Platform: PlatformGemini, Type: AccountTypeAPIKey}
	// 响应体里没有 error.code → 只能按「一律 500」的约定映射成 502。
	body := []byte(`{"error":{"message":"something went wrong"}}`)
	err := svc.writeGeminiSkippedError(c, account, geminiSkippedUpstreamResp(http.StatusBadRequest, "application/json"), "req-3", body)

	require.Error(t, err)
	assert.Equal(t, http.StatusBadGateway, rec.Code)
}

func TestGeminiCompatSkipped_RuleSeesTheRealUpstreamCode(t *testing.T) {
	rec, c := newGeminiSkippedContext()
	bindGeminiSkippedRules(c, geminiKeywordPassthroughRule("upstream safety system"))

	svc := &GeminiMessagesCompatService{}
	account := &Account{ID: 8, Platform: PlatformGemini, Type: AccountTypeAPIKey}
	err := svc.writeGeminiSkippedError(c, account, geminiSkippedUpstreamResp(http.StatusBadRequest, "application/json"), "req-2", geminiSafetyBlockBody())

	require.Error(t, err)
	assert.Equal(t, http.StatusBadRequest, rec.Code,
		"passthrough_code 透出来的必须是上游真实的 400，而不是这条分支自己换上的 500")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &payload))
	assert.Equal(t, "error", payload["type"], "compat 链路保持 Claude 错误结构")
	errField := payload["error"].(map[string]any)
	assert.Equal(t, "upstream_error", errField["type"])
	assert.Contains(t, errField["message"], "upstream safety system")
	assert.True(t, IsResponseCommitted(c))
}

// --- 诊断信息：规则改写状态码后，监控必须还能还原上游真相 ---

func opsEvents(t *testing.T, c *gin.Context) []*OpsUpstreamErrorEvent {
	t.Helper()
	v, ok := c.Get(OpsUpstreamErrorsKey)
	require.True(t, ok, "应当记录了 ops 上游错误事件")
	events, ok := v.([]*OpsUpstreamErrorEvent)
	require.True(t, ok)
	return events
}

// 规则把 400 改写成 418 后，响应体里再也看不到 400，只剩 ops 事件能还原它。
func teapotRule() *model.ErrorPassthroughRule {
	teapot := http.StatusTeapot
	return &model.ErrorPassthroughRule{
		ID: 3, Name: "改写状态码", Enabled: true,
		ErrorCodes:   []int{http.StatusBadRequest},
		MatchMode:    model.MatchModeAll,
		ResponseCode: &teapot,
	}
}

func TestGeminiNativeSkipped_RecordsTheRealUpstreamStatusEvenWhenRewritten(t *testing.T) {
	rec, c := newGeminiSkippedContext()
	bindGeminiSkippedRules(c, teapotRule())

	svc := &GeminiMessagesCompatService{}
	err := svc.writeGeminiNativeSkippedError(c, geminiSkippedAccount(),
		geminiSkippedUpstreamResp(http.StatusBadRequest, "application/json"), "req-native", geminiSafetyBlockBody())

	require.Error(t, err)
	require.Equal(t, http.StatusTeapot, rec.Code)

	status, ok := c.Get(OpsUpstreamStatusCodeKey)
	require.True(t, ok)
	assert.Equal(t, http.StatusBadRequest, status, "客户端拿到 418，监控必须仍记 400")

	events := opsEvents(t, c)
	require.Len(t, events, 1)
	assert.Equal(t, http.StatusBadRequest, events[0].UpstreamStatusCode)
	assert.Equal(t, int64(42), events[0].AccountID)
	assert.Equal(t, "adobe2api-pool", events[0].AccountName)
	assert.Equal(t, PlatformGemini, events[0].Platform)
	assert.Equal(t, "req-native", events[0].UpstreamRequestID)
	assert.Contains(t, events[0].Message, "upstream safety system")
}

func TestGeminiNativeSkipped_RecordsDiagnosticsOnTheUnmatched500Too(t *testing.T) {
	// 这条分支原来一个 ops 事件都不记，原生链路的这类错误在监控里完全不可见。
	_, c := newGeminiSkippedContext()

	svc := &GeminiMessagesCompatService{}
	_ = svc.writeGeminiNativeSkippedError(c, geminiSkippedAccount(),
		geminiSkippedUpstreamResp(http.StatusBadRequest, "application/json"), "req-native", geminiSafetyBlockBody())

	events := opsEvents(t, c)
	require.Len(t, events, 1)
	assert.Equal(t, http.StatusBadRequest, events[0].UpstreamStatusCode)
	assert.Equal(t, "req-native", events[0].UpstreamRequestID)
}

func TestGeminiCompatSkipped_PassthroughShortcutStillRecordsDiagnostics(t *testing.T) {
	rec, c := newGeminiSkippedContext()
	bindGeminiSkippedRules(c, teapotRule())

	svc := &GeminiMessagesCompatService{}
	err := svc.writeGeminiSkippedError(c, geminiSkippedAccount(),
		geminiSkippedUpstreamResp(http.StatusBadRequest, "application/json"), "req-compat", geminiSafetyBlockBody())

	require.Error(t, err)
	require.Equal(t, http.StatusTeapot, rec.Code)

	events := opsEvents(t, c)
	require.Len(t, events, 1, "命中规则走捷径时也只应记一条，不能重复")
	assert.Equal(t, http.StatusBadRequest, events[0].UpstreamStatusCode)
	assert.Equal(t, int64(42), events[0].AccountID)
	assert.Equal(t, "req-compat", events[0].UpstreamRequestID)
}

func TestGeminiCompatSkipped_UnmatchedDoesNotDoubleRecord(t *testing.T) {
	// 未命中时由 writeGeminiMappedError 自己记，这里不能再补一条。
	_, c := newGeminiSkippedContext()

	svc := &GeminiMessagesCompatService{}
	_ = svc.writeGeminiSkippedError(c, geminiSkippedAccount(),
		geminiSkippedUpstreamResp(http.StatusBadRequest, "application/json"), "req-compat", geminiSafetyBlockBody())

	assert.Len(t, opsEvents(t, c), 1)
}
