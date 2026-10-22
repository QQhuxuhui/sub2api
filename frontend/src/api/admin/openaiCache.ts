/**
 * Admin OpenAI cache API: upstream accounts' real/display cache rates and cache-simulation concessions.
 */

import { apiClient } from '../client'

export interface OpenAICacheTokenBucket {
  requests: number
  zero_cache_requests: number
  input_tokens: number
  cache_read_tokens: number
  cache_creation_tokens: number
  raw_input_tokens: number
  raw_cache_read_tokens: number
  billed_cost: number
  cost_delta: number
}

export interface OpenAICacheRateView extends OpenAICacheTokenBucket {
  raw_rate: number | null
  display_rate: number | null
}

export interface OpenAICacheSimConfigView {
  enabled: boolean
  lo: number
  hi: number
  half_life_minutes: number
  cap: number
}

export interface OpenAICacheWindowView {
  total_tokens: number
  cached_tokens: number
  display_rate: number | null
  updated_at?: string
}

export interface OpenAICacheAccountRow {
  account_id: number
  name: string
  type: string
  status: string
  is_shadow: boolean
  schedulable: boolean
  error_message?: string
  rate_limit_reset_at?: string | null
  overload_until?: string | null
  temp_unschedulable_until?: string | null
  temp_unschedulable_reason?: string
  expires_at?: string | null
  last_used_at?: string | null
  simulation: OpenAICacheSimConfigView
  all: OpenAICacheRateView
  text: OpenAICacheRateView
  requests: number
  zero_cache_requests: number
  zero_cache_pct: number
  billed_cost: number
  expected_cost: number
  cost_delta: number
  window?: OpenAICacheWindowView
}

export interface OpenAICacheSummary {
  accounts: number
  all: OpenAICacheRateView
  text: OpenAICacheRateView
  billed_cost: number
  expected_cost: number
  cost_delta: number
}

export interface OpenAICacheAccountsResult {
  summary: OpenAICacheSummary
  accounts: OpenAICacheAccountRow[]
}

export type OpenAICacheGranularity = 'day' | 'hour' | '5min' | 'minute'

export interface OpenAICacheDailyRow {
  date: string
  bucket_start: string
  all: OpenAICacheRateView
  text: OpenAICacheRateView
  billed_cost: number
  expected_cost: number
  cost_delta: number
}

export interface OpenAICacheRangeParams {
  start_date: string
  end_date: string
  timezone?: string
}

export async function listAccounts(params: OpenAICacheRangeParams): Promise<OpenAICacheAccountsResult> {
  const { data } = await apiClient.get<OpenAICacheAccountsResult>('/admin/openai-cache/accounts', { params })
  return data
}

export async function listDaily(
  params: OpenAICacheRangeParams & { account_id?: number; granularity?: OpenAICacheGranularity }
): Promise<{ days: OpenAICacheDailyRow[]; granularity: OpenAICacheGranularity }> {
  const { data } = await apiClient.get<{ days: OpenAICacheDailyRow[]; granularity: OpenAICacheGranularity }>(
    '/admin/openai-cache/daily',
    { params }
  )
  return { days: data.days || [], granularity: data.granularity || 'day' }
}

export interface OpenAICacheSimUpdateRequest {
  enabled: boolean
  lo: number
  hi: number
  half_life_minutes: number
  cap: number
}

/**
 * 页面内快捷修改某账号的缓存模拟配置。后端只对 extra.openai_cache_simulation 做 key 级合并，
 * 不会碰账号的其它 extra 键。
 */
export async function updateSimulation(
  accountId: number,
  payload: OpenAICacheSimUpdateRequest
): Promise<OpenAICacheSimConfigView> {
  const { data } = await apiClient.put<OpenAICacheSimConfigView>(
    `/admin/openai-cache/accounts/${accountId}/simulation`,
    payload
  )
  return data
}

export const openaiCacheAPI = { listAccounts, listDaily, updateSimulation }
export default openaiCacheAPI
