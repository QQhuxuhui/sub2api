//go:build unit

package admin

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type iqProbeDuplicateRepo struct{ service.IQProbeRepository }

func (*iqProbeDuplicateRepo) GetSet(context.Context, int64) (*service.IQProbeQuestionSet, error) {
	return &service.IQProbeQuestionSet{ID: 1}, nil
}

func (*iqProbeDuplicateRepo) CreateMonitor(context.Context, *service.IQProbeMonitor) (*service.IQProbeMonitor, error) {
	return nil, service.ErrIQProbeMonitorDuplicate
}

type iqProbeAccountRepo struct{ service.AccountRepository }

func (*iqProbeAccountRepo) GetByID(context.Context, int64) (*service.Account, error) {
	return &service.Account{ID: 42}, nil
}

func TestIQProbeCreateMonitors_AllDuplicatesReturnEmptyArray(t *testing.T) {
	svc := service.NewIQProbeService(&iqProbeDuplicateRepo{}, &iqProbeAccountRepo{}, nil, nil, nil, nil)
	handler := NewIQProbeHandler(svc)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(`{"account_ids":[42],"model":"test-model","set_id":1,"alert_threshold":0.7}`))
	c.Request.Header.Set("Content-Type", "application/json")
	handler.CreateMonitors(c)
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data struct {
			Created json.RawMessage `json:"created"`
			Skipped []int64         `json:"skipped_account_ids"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.JSONEq(t, "[]", string(body.Data.Created))
	require.Equal(t, []int64{42}, body.Data.Skipped)
}

type iqProbeRunAnswersRepo struct {
	service.IQProbeRepository
	runIDs   []int64
	withText bool
}

func (r *iqProbeRunAnswersRepo) ListAnswersByRuns(_ context.Context, runIDs []int64, withText bool) ([]*service.IQProbeAnswer, error) {
	r.runIDs, r.withText = runIDs, withText
	return []*service.IQProbeAnswer{{ID: 1, RunID: 7, Verdict: service.IQProbeVerdictCorrect, ResponseText: "推理\n答案：42"}}, nil
}

func TestIQProbeListRunAnswers_IncludesReplies(t *testing.T) {
	repo := &iqProbeRunAnswersRepo{}
	handler := NewIQProbeHandler(service.NewIQProbeService(repo, nil, nil, nil, nil, nil))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: "7"}}
	handler.ListRunAnswers(c)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, []int64{7}, repo.runIDs)
	require.True(t, repo.withText, "run answers must carry the full reply")
	var body struct {
		Data []service.IQProbeAnswer `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.Len(t, body.Data, 1)
	require.Equal(t, "推理\n答案：42", body.Data[0].ResponseText)
}

type iqProbeRunHandlerRepo struct {
	service.IQProbeRepository
	filter  service.IQProbeRunFilter
	missing bool
}

func (r *iqProbeRunHandlerRepo) ListRuns(_ context.Context, f service.IQProbeRunFilter) ([]*service.IQProbeRun, int64, error) {
	r.filter = f
	if r.missing {
		return nil, 0, nil
	}
	return []*service.IQProbeRun{{ID: 77, MonitorID: 9, Status: service.IQProbeRunScored}}, 1, nil
}

func (*iqProbeRunHandlerRepo) GetMonitor(context.Context, int64) (*service.IQProbeMonitor, error) {
	return &service.IQProbeMonitor{ID: 9}, nil
}

func (*iqProbeRunHandlerRepo) CreateRun(context.Context, *service.IQProbeRun) (int64, error) {
	return 77, nil
}

func (*iqProbeRunHandlerRepo) HeartbeatRun(context.Context, int64) (bool, error) { return false, nil }
func (*iqProbeRunHandlerRepo) FinishRun(context.Context, *service.IQProbeRun, *service.IQProbeMonitorRunState) (bool, error) {
	return false, nil
}

func TestIQProbeRunMonitor_ReturnsRoundID(t *testing.T) {
	h := NewIQProbeHandler(service.NewIQProbeService(&iqProbeRunHandlerRepo{}, nil, nil, nil, nil, nil))
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	c.Params = gin.Params{{Key: "id", Value: "9"}}
	h.RunMonitor(c)
	require.Equal(t, http.StatusOK, w.Code)
	var body struct {
		Data struct {
			Started bool  `json:"started"`
			RunID   int64 `json:"run_id"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	require.True(t, body.Data.Started)
	require.Equal(t, int64(77), body.Data.RunID)
}

func TestIQProbeGetRun_ReturnsSpecificRoundOrNotFound(t *testing.T) {
	for _, missing := range []bool{false, true} {
		repo := &iqProbeRunHandlerRepo{missing: missing}
		h := NewIQProbeHandler(service.NewIQProbeService(repo, nil, nil, nil, nil, nil))
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
		c.Params = gin.Params{{Key: "id", Value: "77"}}
		h.GetRun(c)
		require.Equal(t, int64(77), repo.filter.RunID)
		if missing {
			require.Equal(t, http.StatusNotFound, w.Code)
		} else {
			require.Equal(t, http.StatusOK, w.Code)
			var body struct {
				Data service.IQProbeRun `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
			require.Equal(t, int64(77), body.Data.ID)
			require.Equal(t, service.IQProbeRunScored, body.Data.Status)
		}
	}
}
