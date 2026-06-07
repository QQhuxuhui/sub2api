//go:build unit

package service

import (
	"testing"
)

// 管理员给自己创建/编辑密钥时，应能看到并绑定所有「标准分组」——包括专属(IsExclusive)
// 分组，不受 AllowedGroups 限制。
func TestCanUserBindGroupInternal_AdminBindsAllStandardGroups(t *testing.T) {
	s := &APIKeyService{}
	admin := &User{Role: RoleAdmin}

	exclusive := &Group{ID: 1, IsExclusive: true} // 不在 AllowedGroups 中
	if !s.canUserBindGroupInternal(admin, exclusive, nil) {
		t.Errorf("admin should be able to bind exclusive standard group")
	}

	public := &Group{ID: 2, IsExclusive: false}
	if !s.canUserBindGroupInternal(admin, public, nil) {
		t.Errorf("admin should be able to bind public standard group")
	}
}

// 订阅型分组：即使是管理员，无有效订阅也不应放行——与运行时中间件(api_key_auth)的
// 403 SUBSCRIPTION_NOT_FOUND 校验保持一致，避免创建出运行时不可用的“幽灵 key”。
func TestCanUserBindGroupInternal_SubscriptionGroupRequiresSubscription(t *testing.T) {
	s := &APIKeyService{}
	admin := &User{Role: RoleAdmin}
	subGroup := &Group{ID: 3, SubscriptionType: SubscriptionTypeSubscription}

	if s.canUserBindGroupInternal(admin, subGroup, map[int64]bool{}) {
		t.Errorf("admin without active subscription must NOT bind subscription group")
	}
	if !s.canUserBindGroupInternal(admin, subGroup, map[int64]bool{3: true}) {
		t.Errorf("admin with active subscription should bind subscription group")
	}
}

// 普通用户的过滤行为不应被改变（回归保护）。
func TestCanUserBindGroupInternal_NonAdminStillFiltered(t *testing.T) {
	s := &APIKeyService{}
	user := &User{Role: RoleUser} // 无 AllowedGroups、无订阅

	exclusive := &Group{ID: 1, IsExclusive: true}
	if s.canUserBindGroupInternal(user, exclusive, nil) {
		t.Errorf("non-admin must NOT bind exclusive group not in AllowedGroups")
	}

	public := &Group{ID: 2, IsExclusive: false}
	if !s.canUserBindGroupInternal(user, public, nil) {
		t.Errorf("non-admin should bind public group")
	}

	subGroup := &Group{ID: 3, SubscriptionType: SubscriptionTypeSubscription}
	if s.canUserBindGroupInternal(user, subGroup, map[int64]bool{}) {
		t.Errorf("non-admin without subscription must NOT bind subscription group")
	}
}
