//go:build unit

package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/Wei-Shaw/sub2api/internal/config"
	middleware2 "github.com/Wei-Shaw/sub2api/internal/server/middleware"
	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"
)

type plazaViewUserRepo struct {
	service.UserRepository
	user *service.User
	err  error
}

func (r plazaViewUserRepo) GetByID(context.Context, int64) (*service.User, error) {
	return r.user, r.err
}

type plazaViewGroupRepo struct {
	service.GroupRepository
	groups []service.Group
}

func (r plazaViewGroupRepo) ListActive(context.Context) ([]service.Group, error) {
	return r.groups, nil
}

type plazaViewSubRepo struct {
	service.UserSubscriptionRepository
	groupIDs []int64
}

func (r plazaViewSubRepo) ListActiveByUserID(_ context.Context, userID int64) ([]service.UserSubscription, error) {
	subs := make([]service.UserSubscription, 0, len(r.groupIDs))
	for _, id := range r.groupIDs {
		subs = append(subs, service.UserSubscription{
			UserID: userID, GroupID: id, Status: service.SubscriptionStatusActive, ExpiresAt: time.Now().Add(time.Hour),
		})
	}
	return subs, nil
}

type plazaViewChannelRepo struct {
	service.ChannelRepository
	channels []service.Channel
}

func (r plazaViewChannelRepo) ListAll(context.Context) ([]service.Channel, error) {
	return r.channels, nil
}

type plazaViewHealthSource struct {
	calls  int
	filter service.ChannelMonitorV2Filter
}

func (s *plazaViewHealthSource) Matrix(_ context.Context, filter service.ChannelMonitorV2Filter, _ service.ChannelMonitorV2GroupBy, _ bool) (*service.ChannelMonitorV2Matrix, error) {
	s.calls++
	s.filter = filter
	return &service.ChannelMonitorV2Matrix{}, nil
}

// 分组：1 公开、2/3 专属、4 公开订阅型、5 专属订阅型。每个分组绑定一个有定价的渠道模型。
func plazaViewGroups() []service.Group {
	return []service.Group{
		{ID: 1, Name: "public", Platform: service.PlatformAnthropic, RateMultiplier: 1},
		{ID: 2, Name: "exclusive-granted", Platform: service.PlatformAnthropic, RateMultiplier: 2, IsExclusive: true},
		{ID: 3, Name: "exclusive-other", Platform: service.PlatformAnthropic, RateMultiplier: 3, IsExclusive: true},
		{ID: 4, Name: "sub-public", Platform: service.PlatformAnthropic, RateMultiplier: 4, SubscriptionType: service.SubscriptionTypeSubscription},
		{ID: 5, Name: "sub-exclusive", Platform: service.PlatformAnthropic, RateMultiplier: 5, IsExclusive: true, SubscriptionType: service.SubscriptionTypeSubscription},
	}
}

func newPlazaViewHandler(user *service.User, userErr error, subscribed []int64, health *plazaViewHealthSource) *ModelPlazaHandler {
	groups := plazaViewGroups()
	price := 3e-6
	channels := make([]service.Channel, 0, len(groups))
	for _, g := range groups {
		channels = append(channels, service.Channel{
			ID: g.ID, Name: g.Name, Status: service.StatusActive, GroupIDs: []int64{g.ID},
			ModelPricing: []service.ChannelModelPricing{{
				Platform: g.Platform, Models: []string{"claude-sonnet"}, BillingMode: service.BillingModeToken,
				InputPrice: &price, OutputPrice: &price,
			}},
		})
	}
	groupRepo := plazaViewGroupRepo{groups: groups}
	plaza := service.NewModelPlazaService(plazaViewChannelRepo{channels: channels}, groupRepo, nil, nil, nil)
	if health != nil {
		plaza.SetHealthSource(health)
	}
	settings := service.NewSettingService(&settingHandlerPublicRepoStub{values: map[string]string{
		service.SettingKeyModelPlazaEnabled:     "true",
		service.SettingKeyChannelMonitorEnabled: "true",
		service.SettingKeyChannelMonitorMode:    service.ChannelMonitorModeV2,
	}}, &config.Config{})
	var keys *service.APIKeyService
	if user != nil || userErr != nil {
		keys = service.NewAPIKeyService(nil, plazaViewUserRepo{user: user, err: userErr}, groupRepo,
			plazaViewSubRepo{groupIDs: subscribed}, nil, nil, &config.Config{})
	}
	return NewModelPlazaHandler(plaza, keys, settings)
}

func callPlazaGet(h *ModelPlazaHandler, userID int64) (*httptest.ResponseRecorder, []int64) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/model-plaza", nil)
	if userID > 0 {
		c.Set(string(middleware2.ContextKeyUser), middleware2.AuthSubject{UserID: userID})
	}
	h.Get(c)
	var body struct {
		Data struct {
			Groups []struct {
				ID int64 `json:"id"`
			} `json:"groups"`
		} `json:"data"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	ids := make([]int64, 0, len(body.Data.Groups))
	for _, g := range body.Data.Groups {
		ids = append(ids, g.ID)
	}
	return rec, ids
}

func TestIntersectPlazaGroups(t *testing.T) {
	groups := []service.PlazaGroup{{ID: 3}, {ID: 1}, {ID: 2}}
	out := intersectPlazaGroups(groups, []service.Group{{ID: 2}, {ID: 3}, {ID: 9}})
	require.Equal(t, []int64{3, 2}, []int64{out[0].ID, out[1].ID}, "保持广场清单的顺序")
	require.Len(t, out, 2)
	require.Empty(t, intersectPlazaGroups(groups, nil))
}

func TestModelPlazaGet_GroupsFollowAccountPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name       string
		user       *service.User
		subscribed []int64
		want       []int64
	}{
		{"普通用户：公开 + 已授权专属", &service.User{ID: 7, Role: service.RoleUser, AllowedGroups: []int64{2}}, nil, []int64{1, 2}},
		{"普通用户无授权：只有公开", &service.User{ID: 7, Role: service.RoleUser}, nil, []int64{1}},
		{"管理员：全部标准分组，未订阅的订阅型不可见", &service.User{ID: 7, Role: service.RoleAdmin}, nil, []int64{1, 2, 3}},
		{"限制公开分组的普通用户：只有授权列表里的标准分组", &service.User{ID: 7, Role: service.RoleUser, AllowedGroups: []int64{3}, RestrictPublicGroups: true}, nil, []int64{3}},
		{"有效订阅但无显式授权：订阅型可见", &service.User{ID: 7, Role: service.RoleUser}, []int64{4, 5}, []int64{1, 4, 5}},
		{"只有显式授权但无有效订阅：订阅型不可见", &service.User{ID: 7, Role: service.RoleUser, AllowedGroups: []int64{4, 5}}, nil, []int64{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec, ids := callPlazaGet(newPlazaViewHandler(tc.user, nil, tc.subscribed, nil), 7)
			require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
			require.Equal(t, tc.want, ids)
			require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
			require.Contains(t, rec.Header().Values("Vary"), "Authorization")
		})
	}
}

func TestModelPlazaGet_AnonymousSeesOnlyNonExclusive(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec, ids := callPlazaGet(newPlazaViewHandler(&service.User{ID: 7, Role: service.RoleAdmin}, nil, nil, nil), 0)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, []int64{1, 4}, ids, "匿名沿用上游规则：非专属分组（订阅型照常展示）")
	require.Empty(t, rec.Header().Get("Cache-Control"))
	require.Contains(t, rec.Header().Values("Vary"), "Authorization")
}

func TestModelPlazaGet_PermissionLookupFailureShowsNothing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec, ids := callPlazaGet(newPlazaViewHandler(nil, errors.New("db down"), nil, nil), 7)
	require.NotEqual(t, http.StatusOK, rec.Code, "权限拿不到时不能降级成匿名视图")
	require.Empty(t, ids)
}

func TestModelPlazaGet_AuthedWithoutKeyServiceFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	rec, ids := callPlazaGet(newPlazaViewHandler(nil, nil, nil, nil), 7)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Empty(t, ids)
	require.Contains(t, rec.Body.String(), `"groups":[]`)
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
}

func TestModelPlazaHealth_ScopeIsExactlyTheAccountsGroups(t *testing.T) {
	gin.SetMode(gin.TestMode)
	src := &plazaViewHealthSource{}
	h := newPlazaViewHandler(&service.User{ID: 7, Role: service.RoleUser, AllowedGroups: []int64{2}}, nil, nil, src)

	rec := callPlazaHealth(h, 7)

	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	require.Equal(t, 1, src.calls)
	got := append([]int64(nil), src.filter.AllowedGroupIDs...)
	sort.Slice(got, func(i, j int) bool { return got[i] < got[j] })
	require.Equal(t, []int64{1, 2}, got, "公开分组 + 已授权专属分组，不含未授权专属分组")
	require.True(t, src.filter.RestrictGroups)
	require.Equal(t, "private, no-store", rec.Header().Get("Cache-Control"))
}
