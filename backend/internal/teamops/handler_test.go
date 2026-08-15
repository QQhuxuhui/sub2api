//go:build unit

package teamops

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

// newTestContext 造一个已认证的 gin 上下文。userID 为 0 时不写入认证主体，
// 用来覆盖「认证中间件缺位」这条分支。
func newTestContext(target string, userID int64) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
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
	h := NewHandler(NewService(nil, 90))
	c, w := newTestContext("/", 0)

	h.GetSummary(c)

	require.Equal(t, http.StatusUnauthorized, w.Code)
}

func TestListRows_RequiresAuthenticatedSubject(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewService(nil, 90))
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
	h := NewHandler(NewService(nil, 90))
	c, w := newTestContext("/?start_date=2024-01-01&end_date=2026-08-15&timezone=UTC", 1)

	h.GetSummary(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	// 文案要写出上限天数，否则用户不知道该缩到多短。
	require.Contains(t, w.Body.String(), "92")
}

func TestGetSummary_RejectsInvertedRange(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewService(nil, 90))
	c, w := newTestContext("/?start_date=2026-08-15&end_date=2026-08-01&timezone=UTC", 1)

	h.GetSummary(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

// 日期格式不对是用户的输入问题，必须是 400。回 500 的话前端会渲染
// 「服务器错误，请稍后重试」，用户会一直重试同一个错日期。
func TestGetSummary_RejectsMalformedDateAsBadRequest(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewService(nil, 90))
	c, w := newTestContext("/?start_date=08%2F01%2F2026&end_date=2026-08-15&timezone=UTC", 1)

	h.GetSummary(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
}

func TestListRows_RejectsMalformedDateAsBadRequest(t *testing.T) {
	t.Parallel()
	h := NewHandler(NewService(nil, 90))
	c, w := newTestContext("/?start_date=08%2F01%2F2026&end_date=2026-08-15&timezone=UTC", 1)

	h.ListRows(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
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
