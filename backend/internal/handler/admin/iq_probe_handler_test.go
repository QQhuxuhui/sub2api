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
