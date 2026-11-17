import { beforeEach, describe, expect, it, vi } from 'vitest'
import type { RouteLocationNormalized } from 'vue-router'
import { plazaMonitorRedirect } from '../monitorRedirect'

const state = vi.hoisted(() => ({
  loaded: true,
  fetchPublicSettings: vi.fn(),
  isAdmin: false,
  v2: true,
  plaza: true
}))

vi.mock('@/stores/app', () => ({
  useAppStore: () => ({
    get publicSettingsLoaded() {
      return state.loaded
    },
    fetchPublicSettings: state.fetchPublicSettings
  })
}))
vi.mock('@/stores/auth', () => ({
  useAuthStore: () => ({
    get isAdmin() {
      return state.isAdmin
    }
  })
}))
vi.mock('@/utils/featureFlags', () => ({
  FeatureFlags: { modelPlaza: { key: 'model_plaza_enabled', mode: 'opt-in' } },
  isChannelMonitorV2Mode: () => state.v2,
  isFeatureFlagEnabled: () => state.plaza
}))

const to = { path: '/monitor', query: { from: 'nav' } } as unknown as RouteLocationNormalized
const run = () => (plazaMonitorRedirect as unknown as (to: RouteLocationNormalized) => Promise<unknown>).call(undefined, to)

beforeEach(() => {
  state.loaded = true
  state.isAdmin = false
  state.v2 = true
  state.plaza = true
  state.fetchPublicSettings.mockReset()
  state.fetchPublicSettings.mockResolvedValue(undefined)
})

describe('plazaMonitorRedirect', () => {
  it('V2 + 广场开启 + 普通用户：进广场列表视图并保留原有参数', async () => {
    expect(await run()).toEqual({ path: '/model-plaza', query: { from: 'nav', embedded: '1', view: 'list' } })
  })

  it('管理员、V1 模式、广场未开启时放行到原页面', async () => {
    state.isAdmin = true
    expect(await run()).toBe(true)
    state.isAdmin = false
    state.v2 = false
    expect(await run()).toBe(true)
    state.v2 = true
    state.plaza = false
    expect(await run()).toBe(true)
  })

  it('公共设置未加载时先加载再判定；加载失败则放行', async () => {
    state.loaded = false
    expect(await run()).toEqual({ path: '/model-plaza', query: { from: 'nav', embedded: '1', view: 'list' } })
    expect(state.fetchPublicSettings).toHaveBeenCalledTimes(1)

    state.fetchPublicSettings.mockRejectedValue(new Error('offline'))
    expect(await run()).toBe(true)
  })
})
