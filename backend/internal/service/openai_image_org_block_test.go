package service

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestIsImageOrgBlockedForSelection 验证组织级生图冷却的选号判定。
// 该判定被高级调度器(isAccountRequestCompatible)与兜底负载感知选号共用，
// 修复点：默认(未开高级调度)走兜底路径时，此前漏检该冷却，会空切到同 org 的其它账号继续撞 429。
func TestIsImageOrgBlockedForSelection(t *testing.T) {
	svc := &OpenAIGatewayService{}
	const blockedOrg = "org-blocked"
	svc.blockOrgImageScheduling(blockedOrg, time.Now().Add(10*time.Minute))

	newAcc := func(org string) *Account {
		creds := map[string]any{}
		if org != "" {
			creds["organization_id"] = org
		}
		return &Account{Platform: PlatformOpenAI, Type: AccountTypeOAuth, Credentials: creds}
	}

	blockedAcc := newAcc(blockedOrg)
	otherOrgAcc := newAcc("org-other")
	noOrgAcc := newAcc("")

	// 生图请求 + 所属 org 正处于冷却 → true（选号应跳过）
	require.True(t, svc.isImageOrgBlockedForSelection(blockedAcc, OpenAIImagesCapabilityBasic))
	// 生图请求 + 不同 org（未冷却）→ false
	require.False(t, svc.isImageOrgBlockedForSelection(otherOrgAcc, OpenAIImagesCapabilityBasic))
	// 生图请求 + 账号无 org → false
	require.False(t, svc.isImageOrgBlockedForSelection(noOrgAcc, OpenAIImagesCapabilityBasic))
	// 非生图请求（capability 为空）→ false，即便该 org 在冷却中（不应误伤非生图流量）
	require.False(t, svc.isImageOrgBlockedForSelection(blockedAcc, ""))
	// nil 账号 → false
	require.False(t, svc.isImageOrgBlockedForSelection(nil, OpenAIImagesCapabilityBasic))
}
