import type { NavigationGuardWithThis, RouteLocationRaw } from 'vue-router'
import { useAppStore } from '@/stores/app'
import { useAuthStore } from '@/stores/auth'
import { FeatureFlags, isChannelMonitorV2Mode, isFeatureFlagEnabled } from '@/utils/featureFlags'

/**
 * `/monitor` 的进入守卫：监控为 V2 且模型广场开启时，普通用户改进广场的列表视图。
 * 其余情况（V1 模式、广场未开启、管理员）放行到原页面。
 */
export const plazaMonitorRedirect: NavigationGuardWithThis<undefined> = async (to) => {
  const appStore = useAppStore()
  const authStore = useAuthStore()
  if (!appStore.publicSettingsLoaded) {
    try {
      await appStore.fetchPublicSettings()
    } catch {
      return true
    }
  }
  if (authStore.isAdmin) return true
  if (!isChannelMonitorV2Mode() || !isFeatureFlagEnabled(FeatureFlags.modelPlaza)) return true
  const target: RouteLocationRaw = {
    path: '/model-plaza',
    query: { ...to.query, embedded: '1', view: 'list' }
  }
  return target
}
