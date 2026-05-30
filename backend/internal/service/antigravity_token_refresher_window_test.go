//go:build unit

package service

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// 回归：401 强制刷新传入的极大窗口必须被 NeedsRefresh 尊重，
// 否则 ForceRefreshAccessToken 在"本地未过期"时不会真正刷新，401 原地重试失效。
func TestAntigravityTokenRefresher_NeedsRefresh_HonorsForceWindow(t *testing.T) {
	r := &AntigravityTokenRefresher{}
	// token 还有 30 分钟才过期（>15min 固定窗口）
	expiresAt := strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(), 10)
	account := &Account{
		Platform:    PlatformAntigravity,
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"expires_at": expiresAt},
	}

	// 正常调度窗口(15min)：30min > 15min → 不刷新（保持既有行为）
	require.False(t, r.NeedsRefresh(account, antigravityRefreshWindow),
		"15min 窗口下，30min 才过期的 token 不应触发刷新")

	// 强制刷新窗口(百年)：必须恒触发，即便本地远未过期
	require.True(t, r.NeedsRefresh(account, antigravityForceRefreshWindow),
		"强制刷新的极大窗口必须被尊重，使未过期 token 也触发刷新")
}

// 不可刷新的账号（非 antigravity oauth）始终返回 false，且不受窗口影响。
func TestAntigravityTokenRefresher_NeedsRefresh_CannotRefresh(t *testing.T) {
	r := &AntigravityTokenRefresher{}
	expiresAt := strconv.FormatInt(time.Now().Add(30*time.Minute).Unix(), 10)
	account := &Account{
		Platform:    PlatformAnthropic, // 非 antigravity
		Type:        AccountTypeOAuth,
		Credentials: map[string]any{"expires_at": expiresAt},
	}
	require.False(t, r.NeedsRefresh(account, antigravityForceRefreshWindow))
}
