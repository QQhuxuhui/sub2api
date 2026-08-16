package teamops

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/Wei-Shaw/sub2api/internal/pkg/response"
	"github.com/Wei-Shaw/sub2api/internal/server/middleware"

	"github.com/gin-gonic/gin"
)

// Handler 是团队消耗看板的 HTTP 入口。
type Handler struct {
	svc *Service
}

func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// currentUserID 取当前登录用户。UserID 非正视同未认证：那种上下文说明认证链路出了问题，
// 拿 0 去查库会返回一个空看板而不是报错，用户看到的是「你没有任何消耗」。
func currentUserID(c *gin.Context) (int64, bool) {
	subject, ok := middleware.GetAuthSubjectFromContext(c)
	if !ok || subject.UserID <= 0 {
		return 0, false
	}
	return subject.UserID, true
}

// writeRequestError 把用户输入引起的错误写成 400 并返回 true，其余返回 false 交给调用方。
// 分类只认哨兵、不认错误文本：文本是会被改写的，而分类错了的后果是用户填错日期却看到
// 「服务器错误」，然后拿着同一个错日期一直重试。
func writeRequestError(c *gin.Context, err error) bool {
	switch {
	case errors.Is(err, ErrRangeTooLong):
		response.Error(c, http.StatusBadRequest,
			fmt.Sprintf("所选时间范围超过 %d 天，请缩小范围", MaxRangeDays))
		return true
	case errors.Is(err, ErrRangeInverted):
		response.Error(c, http.StatusBadRequest, "开始日期不能晚于结束日期")
		return true
	case errors.Is(err, ErrInvalidDate):
		response.Error(c, http.StatusBadRequest, "日期格式不正确，应为 YYYY-MM-DD")
		return true
	}
	return false
}

// writeInternalError 记下原始错误并回一句与实现无关的话。仓储错误里带表名列名，
// 属于不该出现在响应体里的实现细节。
func writeInternalError(c *gin.Context, endpoint string, err error) {
	slog.Error("team usage console request failed", "endpoint", endpoint, "error", err)
	response.Error(c, http.StatusInternalServerError, "加载团队消耗数据失败")
}

// GetSummary 处理 GET /user/team/summary。
func (h *Handler) GetSummary(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	dto, err := h.svc.Summary(c.Request.Context(), userID,
		c.Query("start_date"), c.Query("end_date"), c.Query("timezone"))
	if err != nil {
		if writeRequestError(c, err) {
			return
		}
		writeInternalError(c, "summary", err)
		return
	}

	response.Success(c, dto)
}

// ListRows 处理 GET /user/team/rows。
//
// 分页夹取发生在仓储层且不回传（RowQuery 是值传递），所以 page=999999999 的响应会是
// page=999999999、total=30、items=[]。这与既有的 response.ParsePagination（对 page 不设上界）
// 一致，前端按 total 判断是否越界。
func (h *Handler) ListRows(c *gin.Context) {
	userID, ok := currentUserID(c)
	if !ok {
		response.Error(c, http.StatusUnauthorized, "未登录")
		return
	}

	page, pageSize := response.ParsePagination(c)
	rows, total, err := h.svc.Rows(c.Request.Context(), userID,
		c.Query("start_date"), c.Query("end_date"), c.Query("timezone"),
		c.Query("sort"), c.Query("order"), c.Query("q"), page, pageSize)
	if err != nil {
		if writeRequestError(c, err) {
			return
		}
		writeInternalError(c, "rows", err)
		return
	}

	if rows == nil {
		// 前端对 items 直接做 v-for / .length，null 会在渲染层炸掉而不是显示空表格。
		rows = []Row{}
	}
	response.Paginated(c, rows, total, page, pageSize)
}
