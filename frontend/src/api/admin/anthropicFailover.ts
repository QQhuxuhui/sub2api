/**
 * Admin Anthropic failover API: the status + keyword rule table, group
 * switches and switch statistics for the /v1/messages paths.
 */

import { apiClient } from '../client'

export type AnthropicFailoverAction = 'failover' | 'return'

export interface AnthropicFailoverRule {
  id: string
  name: string
  enabled: boolean
  /** 0 matches any eligible 4xx. */
  status_code: number
  keywords: string[]
  action: AnthropicFailoverAction
}

export interface AnthropicFailoverConfig {
  enabled_group_ids: number[]
  max_rule_switches: number
  rules: AnthropicFailoverRule[]
}

export interface AnthropicFailoverRuleStats {
  rule_id: string
  hits: number
  recovered: number
}

export interface AnthropicFailoverStats {
  rules: AnthropicFailoverRuleStats[]
  stream_hits: number
  stream_recovered: number
  window_days: number
}

export interface AnthropicFailoverEvent {
  time: string
  group_id: number
  user_id: number
  api_key_id: number
  /** A rule id, or `stream_before_first_event`. */
  reason: string
  status_code: number
  switches: number
  accounts: number[] | null
  recovered: boolean
  canceled: boolean
}

export interface AnthropicFailoverOverview {
  config: AnthropicFailoverConfig
  stats: AnthropicFailoverStats
  recent: AnthropicFailoverEvent[]
}

export const ANTHROPIC_FAILOVER_STREAM_REASON = 'stream_before_first_event'

export async function getOverview(): Promise<AnthropicFailoverOverview> {
  const { data } = await apiClient.get<AnthropicFailoverOverview>('/admin/anthropic-failover')
  return data
}

export async function updateConfig(config: AnthropicFailoverConfig): Promise<AnthropicFailoverConfig> {
  const { data } = await apiClient.put<AnthropicFailoverConfig>('/admin/anthropic-failover', config)
  return data
}

export const anthropicFailoverAPI = { getOverview, updateConfig }

export default anthropicFailoverAPI
