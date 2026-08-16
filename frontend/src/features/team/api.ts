import apiClient from '@/api/client'
import type { TeamQuery, TeamRowsResponse, TeamSummary } from './types'

// timezone 由 client.ts 的拦截器无条件附加到每个 GET 上，这里不重复传。
// 区间边界按**用户时区**解析、日柱按**服务器时区**分桶 —— 这个不对称是照抄
// /usage 的既有语义，前端不要试图纠正它，否则两个页面的数会对不上。

export async function getTeamSummary(params: TeamQuery, signal?: AbortSignal): Promise<TeamSummary> {
  const { data } = await apiClient.get<{ data: TeamSummary }>('/user/team/summary', { params, signal })
  return data.data
}

export async function listTeamRows(params: TeamQuery, signal?: AbortSignal): Promise<TeamRowsResponse> {
  const { data } = await apiClient.get<{ data: TeamRowsResponse }>('/user/team/rows', { params, signal })
  return data.data
}
