/**
 * Admin OpenAI failover API: the status + keyword rule table, group switches
 * and switch statistics for the OpenAI HTTP paths. Same shape as the
 * Anthropic table, without in-stream switches.
 */

import { apiClient } from '../client'
import type {
  AnthropicFailoverConfig,
  AnthropicFailoverEvent,
  AnthropicFailoverRuleStats
} from './anthropicFailover'

export interface OpenAIFailoverConfig extends AnthropicFailoverConfig {
  /**
   * Error codes of a pre-output in-stream response.failed that are retried,
   * for every group. An empty list turns the retry off; leaving the field out
   * of a save keeps the stored list.
   */
  stream_failed_retry_codes?: string[]
}

export interface OpenAIFailoverOverview {
  config: OpenAIFailoverConfig
  stats: { rules: AnthropicFailoverRuleStats[]; window_days: number }
  recent: AnthropicFailoverEvent[]
}

export async function getOverview(): Promise<OpenAIFailoverOverview> {
  const { data } = await apiClient.get<OpenAIFailoverOverview>('/admin/openai-failover')
  return data
}

export async function updateConfig(config: OpenAIFailoverConfig): Promise<OpenAIFailoverConfig> {
  const { data } = await apiClient.put<OpenAIFailoverConfig>('/admin/openai-failover', config)
  return data
}

export const openAIFailoverAPI = { getOverview, updateConfig }

export default openAIFailoverAPI
