import { onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useAuthStore } from '@/stores/auth'
import { isChannelMonitorV2Mode } from '@/utils/featureFlags'
import { getPlazaHealth } from './api'
import type { PlazaHealthResponse } from './types'

const REFRESH_MS = 60_000

/**
 * 广场健康数据：仅登录且监控为 V2 时获取；页面可见时每 60 秒刷新。
 * 任何失败都静默降级为「无健康数据」，价格照常展示。
 */
export function usePlazaHealth() {
  const authStore = useAuthStore()
  const health = ref<PlazaHealthResponse | null>(null)
  let controller: AbortController | null = null
  let timer: ReturnType<typeof setInterval> | null = null

  function enabled(): boolean {
    return authStore.isAuthenticated && isChannelMonitorV2Mode()
  }

  async function refresh(): Promise<void> {
    controller?.abort()
    if (!enabled()) {
      health.value = null
      return
    }
    const current = new AbortController()
    controller = current
    try {
      const data = await getPlazaHealth(current.signal)
      if (controller === current) health.value = data?.available ? data : null
    } catch {
      if (controller === current) health.value = null
    }
  }

  function tick(): void {
    if (typeof document !== 'undefined' && document.visibilityState === 'hidden') return
    void refresh()
  }

  onMounted(() => {
    void refresh()
    timer = setInterval(tick, REFRESH_MS)
  })

  watch(
    () => authStore.isAuthenticated,
    () => {
      health.value = null
      void refresh()
    }
  )

  onBeforeUnmount(() => {
    controller?.abort()
    controller = null
    if (timer) clearInterval(timer)
  })

  return { health, refresh }
}
