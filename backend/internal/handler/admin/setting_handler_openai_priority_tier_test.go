//go:build unit

package admin

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/config"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type openAIPriorityTierRepoStub struct {
	settingHandlerRepoStub
}

func (s *openAIPriorityTierRepoStub) Set(_ context.Context, key, value string) error {
	if s.values == nil {
		s.values = map[string]string{}
	}
	s.values[key] = value
	return nil
}

func doOpenAIPriorityTierRequest(t *testing.T, h *SettingHandler, method, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(method, "/api/v1/admin/settings/openai-priority-tier", bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if method == http.MethodGet {
		h.GetOpenAIPriorityTierSettings(c)
	} else {
		h.UpdateOpenAIPriorityTierSettings(c)
	}
	return rec
}

func decodeOpenAIPriorityTierGroups(t *testing.T, rec *httptest.ResponseRecorder) []int64 {
	t.Helper()
	var envelope struct {
		Data struct {
			GroupIDs []int64 `json:"group_ids"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &envelope), rec.Body.String())
	return envelope.Data.GroupIDs
}

func TestOpenAIPriorityTierSettingsHandler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	repo := &openAIPriorityTierRepoStub{settingHandlerRepoStub{values: map[string]string{}}}
	h := NewSettingHandler(service.NewSettingService(repo, &config.Config{}), nil, nil, nil, nil, nil, nil)

	rec := doOpenAIPriorityTierRequest(t, h, http.MethodGet, "")
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, decodeOpenAIPriorityTierGroups(t, rec))

	rec = doOpenAIPriorityTierRequest(t, h, http.MethodPut, `{"group_ids":[7,4,4,0]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []int64{4, 7}, decodeOpenAIPriorityTierGroups(t, rec))
	require.Equal(t, "[4,7]", repo.values[service.SettingKeyOpenAIPriorityTierGroupIDs])

	rec = doOpenAIPriorityTierRequest(t, h, http.MethodGet, "")
	require.Equal(t, []int64{4, 7}, decodeOpenAIPriorityTierGroups(t, rec))

	rec = doOpenAIPriorityTierRequest(t, h, http.MethodPut, `{"group_ids":[]}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, decodeOpenAIPriorityTierGroups(t, rec))

	for _, bad := range []string{`{}`, `not-json`, `{"group_ids":"4"}`} {
		rec = doOpenAIPriorityTierRequest(t, h, http.MethodPut, bad)
		require.Equal(t, http.StatusBadRequest, rec.Code, bad)
	}
}
