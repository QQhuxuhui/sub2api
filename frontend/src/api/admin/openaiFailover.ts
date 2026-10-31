/**
 * Admin OpenAI failover API: the status + keyword rule table, group switches,
 * hit statistics and the "no encrypted arguments" account mark.
 */

import { apiClient } from '../client'

export type OpenAIFailoverAction = 'failover' | 'return'

export interface OpenAIFailoverRule {
  id: string
  name: string
  enabled: boolean
  status_code: number
  keywords: string[]
  action: OpenAIFailoverAction
}

export interface OpenAIFailoverConfig {
  enabled_group_ids: number[]
  slot_wait_ms: number
  max_rule_switches: number
  max_slot_switches: number
  rules: OpenAIFailoverRule[]
}

export interface OpenAIFailoverRuleStats {
  rule_id: string
  hits: number
  recovered: number
}

export interface OpenAIFailoverStats {
  rules: OpenAIFailoverRuleStats[]
  slot_switched: number
  slot_recovered: number
  rewritten_requests: number
  rewritten_items: number
}

export interface OpenAIFailoverEvent {
  time: string
  group_id: number
  user_id: number
  api_key_id: number
  rule_id: string
  status_code: number
  rule_switches: number
  slot_switches: number
  rewrites: number
  accounts: number[] | null
  recovered: boolean
}

export interface OpenAIFailoverAccount {
  id: number
  name: string
  type: string
  status: string
  schedulable: boolean
  no_encrypted_args: boolean
  can_check: boolean
  default_check_model: string
}

export interface OpenAIFailoverOverview {
  config: OpenAIFailoverConfig
  stats: OpenAIFailoverStats
  recent: OpenAIFailoverEvent[]
  accounts: OpenAIFailoverAccount[]
}

export interface OpenAIEncryptedArgsCheckResult {
  model: string
  verdict: 'supported' | 'unsupported' | 'mixed' | 'unknown'
  encrypted: number
  plaintext: number
  failed: number
  last_error?: string
}

export async function getOverview(): Promise<OpenAIFailoverOverview> {
  const { data } = await apiClient.get<OpenAIFailoverOverview>('/admin/openai-failover')
  return data
}

export async function updateConfig(config: OpenAIFailoverConfig): Promise<OpenAIFailoverConfig> {
  const { data } = await apiClient.put<OpenAIFailoverConfig>('/admin/openai-failover', config)
  return data
}

export async function updateEncryptedArgs(accountId: number, noEncryptedArgs: boolean): Promise<void> {
  await apiClient.put(`/admin/openai-failover/accounts/${accountId}/encrypted-args`, {
    no_encrypted_args: noEncryptedArgs
  })
}

/** Calls the upstream three times; never changes the mark by itself. */
export async function checkEncryptedArgs(accountId: number, model: string): Promise<OpenAIEncryptedArgsCheckResult> {
  const { data } = await apiClient.post<OpenAIEncryptedArgsCheckResult>(
    `/admin/openai-failover/accounts/${accountId}/encrypted-args/check`,
    { model },
    { timeout: 300000 }
  )
  return data
}

export const openaiFailoverAPI = { getOverview, updateConfig, updateEncryptedArgs, checkEncryptedArgs }
export default openaiFailoverAPI
