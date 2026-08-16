//go:build unit

package teamops

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// gin.SetMode 写的是包级全局变量，而本文件的用例全都 t.Parallel()。
// 放在每个用例都会走到的构造函数里，就是 N 个 goroutine 并发写同一个全局 ——
// `go test -race` 会实打实地报 DATA RACE（不是理论风险，本分支就是这么被抓到的）。
//
// 用 init() 而不是 TestMain：本包已经有一个 TestMain（repo_integration_test.go，
// 带 //go:build integration），而同一次构建里两个 TestMain 不能共存。
// init() 在任何 tag 组合下都不冲突，且保证只执行一次、早于所有用例。
func init() {
	gin.SetMode(gin.TestMode)
}

// newTestContext 造一个已认证的 gin 上下文。userID 为 0 时不写入认证主体，
// 用来覆盖「认证中间件缺位」这条分支。
func newTestContext(target string, userID int64) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, target, nil)
	if userID != 0 {
		// 键必须与认证中间件写入时用的一致；仓库里没有 "user_id" 这个键。
		c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: userID})
	}
	return c, w
}

// decodeData 取出统一响应信封里的 data 段。
func decodeData(t *testing.T, body []byte) map[string]any {
	t.Helper()
	var envelope struct {
		Code int             `json:"code"`
		Data json.RawMessage `json:"data"`
	}
	require.NoError(t, json.Unmarshal(body, &envelope))
	require.Equal(t, 0, envelope.Code)
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(envelope.Data, &out))
	return out
}

func TestGetSummary_RequiresAuthenticatedSubject(t *testing.T) {
	t.Parallel()
	// 仓储传 nil 是安全的：未认证在触达服务层之前就返回了。
	h := NewHandler(NewService(nil, 90, true))
	c, w := newTestContext("/", 0)

	h.GetSummary(c)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestListRows_RequiresAuthenticatedSubject(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewService(nil, 90, true))
	c, w := newTestContext("/", 0)

	h.ListRows(c)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

// 认证主体存在但 UserID 非正：这是数据坏了，不能当成合法用户去查库——
// user_id = 0 的查询会返回一个空看板而不是报错，用户看到的是「你没有任何消耗」。
func TestGetSummary_RejectsNonPositiveUserID(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{}
	h := NewHandler(newTestService(repo, 90, fixedNow))
	c, w := newTestContext("/", 0)
	c.Set(string(middleware.ContextKeyUser), middleware.AuthSubject{UserID: 0})

	h.GetSummary(c)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Zero(t, repo.gotUserID)
}

func TestGetSummary_RejectsTooLongRange(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewService(nil, 90, true))
	c, w := newTestContext("/?start_date=2024-01-01&end_date=2026-08-15&timezone=UTC", 1)

	h.GetSummary(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	// 文案要写出上限天数，否则用户不知道该缩到多短。
	require.Contains(t, w.Body.String(), "92")
}

// 起止填反和格式填错是两种不同的用户错误、两条不同的失败路径，却都产出 400。
// 只断言状态码的话，把两条分支的文案对调也全绿——而用户会看到「日期格式不正确」，
// 拿着格式完全正确的日期反复改格式。所以必须断言文案落在了对的那条分支上。
func TestGetSummary_RejectsInvertedRangeWithItsOwnMessage(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewService(nil, 90, true))
	c, w := newTestContext("/?start_date=2026-08-15&end_date=2026-08-01&timezone=UTC", 1)

	h.GetSummary(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "开始日期不能晚于结束日期")
}

// 日期格式不对是用户的输入问题，必须是 400。回 500 的话前端会渲染
// 「服务器错误，请稍后重试」，用户会一直重试同一个错日期。
func TestGetSummary_RejectsMalformedDateWithItsOwnMessage(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewService(nil, 90, true))
	c, w := newTestContext("/?start_date=08%2F01%2F2026&end_date=2026-08-15&timezone=UTC", 1)

	h.GetSummary(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "YYYY-MM-DD")
}

// end_date 填错走的是解析层另一条 return，与 start_date 那条互不覆盖。
// 那条分支若吞掉错误，零值终点会被判成倒挂 —— 状态码还是 400，但文案变成
// 「开始日期不能晚于结束日期」，用户对着一个格式填错的结束日期怎么改都改不出来。
// 所以这里断的是文案落在了「格式」那条分支上，不是只断 400。
func TestGetSummary_RejectsMalformedEndDateWithItsOwnMessage(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewService(nil, 90, true))
	c, w := newTestContext("/?start_date=2026-08-01&end_date=2026-8-15&timezone=UTC", 1)

	h.GetSummary(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "YYYY-MM-DD")
	require.NotContains(t, w.Body.String(), "开始日期不能晚于结束日期")
}

func TestListRows_RejectsMalformedEndDateWithItsOwnMessage(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewService(nil, 90, true))
	c, w := newTestContext("/?start_date=2026-08-01&end_date=2026-8-15&timezone=UTC", 1)

	h.ListRows(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "YYYY-MM-DD")
	require.NotContains(t, w.Body.String(), "开始日期不能晚于结束日期")
}

func TestListRows_RejectsInvertedRangeWithItsOwnMessage(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewService(nil, 90, true))
	c, w := newTestContext("/?start_date=2026-08-15&end_date=2026-08-01&timezone=UTC", 1)

	h.ListRows(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "开始日期不能晚于结束日期")
}

func TestListRows_RejectsMalformedDateWithItsOwnMessage(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewService(nil, 90, true))
	c, w := newTestContext("/?start_date=08%2F01%2F2026&end_date=2026-08-15&timezone=UTC", 1)

	h.ListRows(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "YYYY-MM-DD")
}

// 查询参数名一旦读错（startDate / from / begin_date 之类），用户选的周期会被静默忽略，
// 页面照常渲染默认区间的数字——没有任何报错。
func TestGetSummary_ReadsPeriodFromQueryParameters(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{}
	h := NewHandler(newTestService(repo, 90, fixedNow))
	c, w := newTestContext("/?start_date=2026-07-01&end_date=2026-07-31&timezone=UTC", 7)

	h.GetSummary(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int64(7), repo.gotUserID)
	require.Equal(t, "2026-07-01", repo.gotCur.StartDate)
	require.Equal(t, "2026-07-31", repo.gotCur.EndDate)
	require.Equal(t, "2026-06-01", repo.gotPrev.StartDate)
}

// 与 GetSummary 同形的一条。ListRows 侧不能沿用「本月 1 日 – 今天」这种区间：
// 那恰好就是参数缺失时的默认区间，读错参数名和读对参数名产出逐字相同的响应，
// 测试全绿而用户在表格上选的结束日被静默忽略——行数据按"今天"算、概览卡按用户
// 选的算，两个数字对不上且没有任何报错。所以这里必须用非默认区间。
func TestListRows_ReadsPeriodFromQueryParameters(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{}
	h := NewHandler(newTestService(repo, 90, fixedNow))
	c, w := newTestContext("/?start_date=2026-07-02&end_date=2026-07-20&timezone=UTC", 7)

	h.ListRows(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int64(7), repo.gotQuery.UserID)
	require.Equal(t, "2026-07-02", repo.gotQuery.Cur.StartDate)
	require.Equal(t, "2026-07-20", repo.gotQuery.Cur.EndDate)
	// 19 天的自定义区间走等长紧邻：06-13 – 07-01。
	require.Equal(t, "2026-06-13", repo.gotQuery.Prev.StartDate)
	require.Equal(t, "2026-07-01", repo.gotQuery.Prev.EndDate)
}

// 时区参数决定区间边界切在哪个零点上，而 /user/team 与 /usage 的口径必须一致
// （设计文档 §10.3 的验收线就建立在这上面）。读错参数名会静默回落到服务器时区，
// 金额差一点点、没有任何报错，本地开发（服务器与浏览器同为 UTC）完全复现不出来。
//
// 断言写成「两个不同时区必须给出相差 9 小时的边界」而不是「必须等于某个具体时区」：
// 后者在服务器时区恰好就是被测时区的机器上会漏判，前者在任何机器上都成立
// （读不到参数时两次都回落到同一个服务器时区，边界会相等）。
func TestGetSummary_ReadsTimezoneFromQueryParameters(t *testing.T) {
	t.Parallel()
	utcRepo := &fakeRepo{}
	c, w := newTestContext("/?start_date=2026-08-01&end_date=2026-08-15&timezone=UTC", 1)
	NewHandler(newTestService(utcRepo, 90, fixedNow)).GetSummary(c)
	require.Equal(t, http.StatusOK, w.Code)

	tokyoRepo := &fakeRepo{}
	c, w = newTestContext("/?start_date=2026-08-01&end_date=2026-08-15&timezone=Asia%2FTokyo", 1)
	NewHandler(newTestService(tokyoRepo, 90, fixedNow)).GetSummary(c)
	require.Equal(t, http.StatusOK, w.Code)

	require.Equal(t, 9*time.Hour, utcRepo.gotCur.Start.Sub(tokyoRepo.gotCur.Start),
		"两次边界相等说明 timezone 参数没被读到")
	require.Equal(t, "Asia/Tokyo", tokyoRepo.gotCur.Timezone)
}

func TestListRows_ReadsTimezoneFromQueryParameters(t *testing.T) {
	t.Parallel()
	utcRepo := &fakeRepo{}
	c, w := newTestContext("/?start_date=2026-08-01&end_date=2026-08-15&timezone=UTC", 1)
	NewHandler(newTestService(utcRepo, 90, fixedNow)).ListRows(c)
	require.Equal(t, http.StatusOK, w.Code)

	tokyoRepo := &fakeRepo{}
	c, w = newTestContext("/?start_date=2026-08-01&end_date=2026-08-15&timezone=Asia%2FTokyo", 1)
	NewHandler(newTestService(tokyoRepo, 90, fixedNow)).ListRows(c)
	require.Equal(t, http.StatusOK, w.Code)

	require.Equal(t, 9*time.Hour, utcRepo.gotQuery.Cur.Start.Sub(tokyoRepo.gotQuery.Cur.Start),
		"两次边界相等说明 timezone 参数没被读到")
	require.Equal(t, "Asia/Tokyo", tokyoRepo.gotQuery.Cur.Timezone)
}

func TestGetSummary_ReturnsSummaryEnvelope(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{summary: Summary{TotalCost: 120.005, PrevCost: 100, RowCount: 3}}
	h := NewHandler(newTestService(repo, 90, fixedNow))
	c, w := newTestContext("/?start_date=2026-08-01&end_date=2026-08-15&timezone=UTC", 1)

	h.GetSummary(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := decodeData(t, w.Body.Bytes())
	require.InDelta(t, 120.01, data["total_cost"], 1e-9)
	require.InDelta(t, 120.005, data["total_cost_raw"], 1e-9)
	require.Equal(t, float64(3), data["row_count"])
	// 结论句阶段未实现：这个键必须在且为 null，前端据此渲染中性句。
	require.Contains(t, data, "conclusion")
	require.Nil(t, data["conclusion"])
}

// 仓储错误必须映射成 500，且不能把底层错误原文回给用户——
// SQL 报错里带表名与列名，属于不该外泄的实现细节。
func TestGetSummary_MapsRepoFailureToInternalErrorWithoutLeakingDetails(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{summaryErr: errors.New("pq: relation \"usage_logs\" does not exist")}
	h := NewHandler(newTestService(repo, 90, fixedNow))
	c, w := newTestContext("/?start_date=2026-08-01&end_date=2026-08-15&timezone=UTC", 1)

	h.GetSummary(c)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.NotContains(t, w.Body.String(), "usage_logs")
}

func TestListRows_MapsRepoFailureToInternalErrorWithoutLeakingDetails(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{rowsErr: errors.New("pq: relation \"usage_logs\" does not exist")}
	h := NewHandler(newTestService(repo, 90, fixedNow))
	c, w := newTestContext("/?start_date=2026-08-01&end_date=2026-08-15&timezone=UTC", 1)

	h.ListRows(c)

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.NotContains(t, w.Body.String(), "usage_logs")
}

func TestListRows_PassesSortSearchAndPaginationToRepo(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{}
	h := NewHandler(newTestService(repo, 90, fixedNow))
	c, w := newTestContext(
		"/?start_date=2026-08-01&end_date=2026-08-15&timezone=UTC&sort=delta&order=asc&q=%E7%8E%8B&page=3&page_size=50", 9)

	h.ListRows(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, int64(9), repo.gotQuery.UserID)
	require.Equal(t, "delta", repo.gotQuery.Sort)
	require.Equal(t, "asc", repo.gotQuery.Order)
	require.Equal(t, "王", repo.gotQuery.Q)
	require.Equal(t, 3, repo.gotQuery.Page)
	require.Equal(t, 50, repo.gotQuery.PageSize)
}

// 没有数据时 items 必须是 []，不能是 null：前端对 items 直接做 v-for /
// .length，null 会在渲染层炸掉，而不是显示一个空表格。
func TestListRows_ReturnsEmptyArrayInsteadOfNull(t *testing.T) {
	t.Parallel()
	h := NewHandler(newTestService(&fakeRepo{}, 90, fixedNow))
	c, w := newTestContext("/?start_date=2026-08-01&end_date=2026-08-15&timezone=UTC", 1)

	h.ListRows(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := decodeData(t, w.Body.Bytes())
	items, ok := data["items"].([]any)
	require.True(t, ok, "items 必须是数组，实际是 %T", data["items"])
	require.Empty(t, items)
}

func TestListRows_ReturnsPaginatedEnvelope(t *testing.T) {
	t.Parallel()
	repo := &fakeRepo{
		rows:  []Row{{GroupKey: "o:王磊", DisplayName: "王磊", CurrentCost: 120, PrevCost: 100}},
		total: 137,
	}
	h := NewHandler(newTestService(repo, 90, fixedNow))
	c, w := newTestContext("/?start_date=2026-08-01&end_date=2026-08-15&timezone=UTC&page=2&page_size=20", 1)

	h.ListRows(c)

	require.Equal(t, http.StatusOK, w.Code)
	data := decodeData(t, w.Body.Bytes())
	require.Equal(t, float64(137), data["total"])
	require.Equal(t, float64(2), data["page"])
	require.Equal(t, float64(20), data["page_size"])

	items := data["items"].([]any)
	require.Len(t, items, 1)
	row := items[0].(map[string]any)
	require.Equal(t, "o:王磊", row["group_key"])
	require.InDelta(t, 20.0, row["delta_pct"], 1e-9)
}
