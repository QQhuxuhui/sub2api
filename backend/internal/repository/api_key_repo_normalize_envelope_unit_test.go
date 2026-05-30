package repository

import (
	"context"
	"testing"

	"github.com/Wei-Shaw/sub2api/internal/service"
	"github.com/stretchr/testify/require"
)

// 回归守卫：GetByKeyForAuth 的 group 列投影必须包含 normalize_anthropic_envelope，
// 否则网关认证路径读到的开关恒为 false，分组级信封规范化在生产中完全失活。
func TestAPIKeyRepository_GetByKeyForAuth_PreservesNormalizeEnvelope_SQLite(t *testing.T) {
	repo, client := newAPIKeyRepoSQLite(t)
	ctx := context.Background()
	user := mustCreateAPIKeyRepoUser(t, ctx, client, "getbykey-auth-normenv-unit@test.com")

	group, err := client.Group.Create().
		SetName("g-auth-normenv-unit").
		SetPlatform(service.PlatformAnthropic).
		SetStatus(service.StatusActive).
		SetSubscriptionType(service.SubscriptionTypeStandard).
		SetRateMultiplier(1).
		SetNormalizeAnthropicEnvelope(true).
		Save(ctx)
	require.NoError(t, err)

	key := &service.APIKey{
		UserID:  user.ID,
		Key:     "sk-getbykey-auth-normenv-unit",
		Name:    "NormEnv Key Unit",
		GroupID: &group.ID,
		Status:  service.StatusActive,
	}
	require.NoError(t, repo.Create(ctx, key))

	got, err := repo.GetByKeyForAuth(ctx, key.Key)
	require.NoError(t, err)
	require.NotNil(t, got.Group)
	require.True(t, got.Group.NormalizeAnthropicEnvelope,
		"normalize_anthropic_envelope 应经认证投影透传；若为 false 多半是 GetByKeyForAuth 的 Select 漏列")
}
