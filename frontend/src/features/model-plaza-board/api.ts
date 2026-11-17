import { apiClient } from '@/api/client'
import type { PlazaHealthResponse } from './types'

/** 获取当前用户可用分组内各线路的健康摘要。需登录。 */
export async function getPlazaHealth(signal?: AbortSignal): Promise<PlazaHealthResponse> {
  const { data } = await apiClient.get<PlazaHealthResponse>('/model-plaza/health', { signal })
  return data
}
