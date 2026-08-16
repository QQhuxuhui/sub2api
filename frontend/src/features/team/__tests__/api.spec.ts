import { describe, expect, it, vi, beforeEach } from 'vitest'
import { getTeamSummary, listTeamRows } from '../api'
import apiClient from '@/api/client'

// client.ts 的响应拦截器已经拆掉 { code, message, data } 信封、直接返回 data 段。
// 在此之上再 return data.data 就多拆了一层，拿到 undefined ——
// TypeScript 抓不到（泛型只是标注，不校验运行时形状），页面上表现为「加载失败」。
// 本仓第一版就是这么写错的，所以这里用**拦截器之后的真实形状**钉住。
vi.mock('@/api/client', () => ({
  default: { get: vi.fn() },
}))

const mockGet = apiClient.get as unknown as ReturnType<typeof vi.fn>

// 拦截器之后 axios 给到调用方的形状：response.data 已经是载荷本身
const asAxios = <T>(payload: T) => Promise.resolve({ data: payload })

beforeEach(() => mockGet.mockReset())

describe('team api 解包层数', () => {
  it('getTeamSummary 返回载荷本身，不是再包一层的信封', async () => {
    mockGet.mockReturnValue(asAxios({ total_cost: 12.34, row_count: 3, key_count: 5 }))
    const s = await getTeamSummary({ start_date: '2026-08-01', end_date: '2026-08-16' })
    // 断具体字段值而不是 toBeDefined()：多拆一层时得到 undefined，
    // 而 undefined 上取属性会抛错，只断 defined 的话反而可能被 mock 形状蒙混过去
    expect(s.total_cost).toBe(12.34)
    expect(s.row_count).toBe(3)
  })

  it('listTeamRows 返回带 items 的分页对象', async () => {
    mockGet.mockReturnValue(asAxios({ items: [{ group_key: 'k:1', current_cost: 1 }], total: 1, page: 1, page_size: 20, pages: 1 }))
    const r = await listTeamRows({ start_date: '2026-08-01', end_date: '2026-08-16' })
    expect(Array.isArray(r.items)).toBe(true)
    expect(r.items[0].group_key).toBe('k:1')
    expect(r.total).toBe(1)
  })

  it('请求路径与既有面板接口同前缀（baseURL 已含 /api/v1）', async () => {
    mockGet.mockReturnValue(asAxios({}))
    await getTeamSummary({})
    expect(mockGet.mock.calls[0][0]).toBe('/user/team/summary')
    mockGet.mockReturnValue(asAxios({ items: [], total: 0, page: 1, page_size: 20, pages: 0 }))
    await listTeamRows({})
    expect(mockGet.mock.calls[1][0]).toBe('/user/team/rows')
  })

  it('AbortSignal 透传给 axios —— 快切周期时要靠它取消上一批', async () => {
    mockGet.mockReturnValue(asAxios({}))
    const ac = new AbortController()
    await getTeamSummary({}, ac.signal)
    expect(mockGet.mock.calls[0][1].signal).toBe(ac.signal)
  })
})
