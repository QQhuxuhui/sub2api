import apiClient from '@/api/client'
import type { TeamQuery, TeamRowsResponse, TeamSummary } from './types'

// timezone 由 client.ts 的拦截器无条件附加到每个 GET 上，这里不重复传。
// 区间边界按**用户时区**解析、日柱按**服务器时区**分桶 —— 这个不对称是照抄
// /usage 的既有语义，前端不要试图纠正它，否则两个页面的数会对不上。

// ⚠️ client.ts 的响应拦截器**已经拆掉 { code, message, data } 这层信封**，
// 直接把 data 段作为 response.data 返回。所以这里只能解一层：
//   const { data } = await apiClient.get<T>(...)   → data 就是载荷本身
// 写成 apiClient.get<{ data: T }>(...) 再 return data.data 会多拆一层拿到 undefined，
// 页面表现是「加载失败」而不是类型错误（TS 只看泛型、不校验运行时形状）。
export async function getTeamSummary(params: TeamQuery, signal?: AbortSignal): Promise<TeamSummary> {
  const { data } = await apiClient.get<TeamSummary>('/user/team/summary', { params, signal })
  return data
}

export async function listTeamRows(params: TeamQuery, signal?: AbortSignal): Promise<TeamRowsResponse> {
  const { data } = await apiClient.get<TeamRowsResponse>('/user/team/rows', { params, signal })
  return data
}
