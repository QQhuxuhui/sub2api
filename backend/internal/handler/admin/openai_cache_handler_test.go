package admin

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/pkg/timezone"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

func TestOpenAICacheTimeRangePreservesTimezoneAndCalendarDay(t *testing.T) {
	for _, tt := range []struct {
		name string
		tz   string
		date string
	}{
		{"shanghai", "Asia/Shanghai", "2026-03-08"},
		{"dst_start", "America/New_York", "2026-03-08"},
		{"dst_end", "America/New_York", "2026-11-01"},
		{"default", "", "2026-03-08"},
		{"invalid", "Invalid/Timezone", "2026-03-08"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			q := url.Values{"start_date": {tt.date}, "end_date": {tt.date}, "timezone": {tt.tz}}
			c.Request = httptest.NewRequest("GET", "/?"+q.Encode(), nil)
			start, end, ok := (&OpenAICacheHandler{}).timeRange(c)
			require.True(t, ok)
			loc := timezone.Location()
			if userLoc, err := time.LoadLocation(tt.tz); tt.tz != "" && err == nil {
				loc = userLoc
			}
			expected, err := time.ParseInLocation("2006-01-02", tt.date, loc)
			require.NoError(t, err)
			require.Equal(t, loc.String(), start.Location().String())
			require.True(t, start.Equal(expected))
			require.True(t, end.Equal(expected.AddDate(0, 0, 1)), "end must be midnight after the selected calendar date: got %v", end)
		})
	}
}

// openAICacheSimAdminService 记录 UpdateAccountExtra 收到的合并内容，并让第二次 GetAccount 返回更新后的账号。
type openAICacheSimAdminService struct {
	*stubAdminService
	account   *service.Account
	lastExtra map[string]any
}

func (s *openAICacheSimAdminService) GetAccount(ctx context.Context, id int64) (*service.Account, error) {
	return s.account, nil
}

func (s *openAICacheSimAdminService) UpdateAccountExtra(ctx context.Context, id int64, updates map[string]any) error {
	s.lastExtra = updates
	if s.account.Extra == nil {
		s.account.Extra = map[string]any{}
	}
	for k, v := range updates {
		s.account.Extra[k] = v
	}
	return nil
}

func newOpenAICacheSimRouter(account *service.Account) (*gin.Engine, *openAICacheSimAdminService) {
	gin.SetMode(gin.TestMode)
	svc := &openAICacheSimAdminService{stubAdminService: newStubAdminService(), account: account}
	h := NewOpenAICacheHandler(nil, svc)
	r := gin.New()
	r.PUT("/admin/openai-cache/accounts/:id/simulation", h.UpdateSimulation)
	return r, svc
}

func putSimulation(t *testing.T, r *gin.Engine, id string, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/admin/openai-cache/accounts/"+id+"/simulation", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

func TestOpenAICacheUpdateSimulation_MergesOnlySimulationKey(t *testing.T) {
	account := &service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey,
		Extra: map[string]any{"base_rpm": float64(30), "openai_strip_cache_creation": true}}
	r, svc := newOpenAICacheSimRouter(account)

	w := putSimulation(t, r, "7", `{"enabled":true,"lo":80,"hi":88,"half_life_minutes":60,"cap":90}`)
	require.Equal(t, 200, w.Code, w.Body.String())

	require.Len(t, svc.lastExtra, 1, "only the simulation key must be merged")
	sim, ok := svc.lastExtra["openai_cache_simulation"].(map[string]any)
	require.True(t, ok)
	require.Equal(t, true, sim["enabled"])
	require.Equal(t, float64(80), sim["lo"])
	require.Equal(t, float64(88), sim["hi"])
	// 其它 Extra 键不受影响
	require.Equal(t, float64(30), account.Extra["base_rpm"])

	var resp struct {
		Data struct {
			Enabled bool    `json:"enabled"`
			Lo      float64 `json:"lo"`
			Hi      float64 `json:"hi"`
			Cap     float64 `json:"cap"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.True(t, resp.Data.Enabled)
	require.Equal(t, float64(80), resp.Data.Lo)
	require.Equal(t, float64(90), resp.Data.Cap)
}

func TestOpenAICacheUpdateSimulation_Rejects(t *testing.T) {
	t.Run("lo above hi", func(t *testing.T) {
		r, svc := newOpenAICacheSimRouter(&service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeAPIKey})
		w := putSimulation(t, r, "7", `{"enabled":true,"lo":90,"hi":80,"half_life_minutes":60,"cap":95}`)
		require.Equal(t, 400, w.Code, w.Body.String())
		require.Nil(t, svc.lastExtra)
	})
	t.Run("non openai platform", func(t *testing.T) {
		r, svc := newOpenAICacheSimRouter(&service.Account{ID: 7, Platform: service.PlatformAnthropic, Type: service.AccountTypeAPIKey})
		w := putSimulation(t, r, "7", `{"enabled":true,"lo":80,"hi":88,"half_life_minutes":60,"cap":95}`)
		require.Equal(t, 400, w.Code)
		require.Nil(t, svc.lastExtra)
	})
	t.Run("shadow account", func(t *testing.T) {
		parent := int64(1)
		r, svc := newOpenAICacheSimRouter(&service.Account{ID: 7, Platform: service.PlatformOpenAI, Type: service.AccountTypeOAuth, ParentAccountID: &parent})
		w := putSimulation(t, r, "7", `{"enabled":true,"lo":80,"hi":88,"half_life_minutes":60,"cap":95}`)
		require.Equal(t, 400, w.Code)
		require.Contains(t, w.Body.String(), "OPENAI_CACHE_SIMULATION_SHADOW")
		require.Nil(t, svc.lastExtra)
	})
}
