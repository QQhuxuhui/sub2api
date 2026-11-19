import { apiClient } from '@/api/client'
import type { PlazaHealthResponse } from './types'

/** 健康接口的响应格式版本（小时采样）。不带版本的请求会被当作旧页面，只拿到 available=false。 */
const HEALTH_FORMAT_VERSION = 2

/** 获取当前用户可用分组内各线路的健康摘要。需登录。 */
export async function getPlazaHealth(signal?: AbortSignal): Promise<PlazaHealthResponse> {
  const { data } = await apiClient.get<PlazaHealthResponse>('/model-plaza/health', { signal, params: { v: HEALTH_FORMAT_VERSION } })
  return data
}
