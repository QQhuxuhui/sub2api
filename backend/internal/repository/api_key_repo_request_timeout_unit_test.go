package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 回归守卫：GetByKeyForAuth 的 user 列投影必须包含 request_timeout_seconds，
// 否则认证缓存快照里的用户级超时恒为 0（继承全局），-1 与自定义秒数在生产中全部失活。
func TestAPIKeyRepository_GetByKeyForAuth_PreservesRequestTimeoutSeconds_SQLite(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()

	u, err := client.User.Create().
		SetEmail("getbykey-auth-reqtimeout-unit@test.com").
		SetPasswordHash("test-password-hash").
		SetRole(service.RoleUser).
		SetStatus(service.StatusActive).
		SetRequestTimeoutSeconds(-1).
		Save(ctx)
	require.NoError(t, err)

	key := &service.APIKey{
		UserID: u.ID,
		Key:    "sk-getbykey-auth-reqtimeout-unit",
		Name:   "ReqTimeout Key Unit",
		Status: service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	got, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.NotNil(t, got.User)
	require.Equal(t, -1, got.User.RequestTimeoutSeconds,
		"request_timeout_seconds 应经认证投影透传；若为 0 多半是 GetByKeyForAuth 的 user Select 漏列")
}
