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

export interface OpenAICacheDailyRow {
  date: string
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

export async function listDaily(params: OpenAICacheRangeParams & { account_id?: number }): Promise<OpenAICacheDailyRow[]> {
  const { data } = await apiClient.get<{ days: OpenAICacheDailyRow[] }>('/admin/openai-cache/daily', { params })
  return data.days || []
}

export const openaiCacheAPI = { listAccounts, listDaily }
export default openaiCacheAPI
