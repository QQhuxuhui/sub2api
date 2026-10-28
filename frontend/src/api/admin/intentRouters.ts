import { apiClient } from '../client'

export interface IntentRule {
  name: string
  description: string
  /** Matched against the two latest user messages on every turn, before the classifier. */
  keywords?: string[]
  account_ids: number[]
  enabled: boolean
}

export type IntentClassifierProtocol = 'openai_chat' | 'gemini'

/** TypeSafe Jev judges keyword misses of short conversations; probes are routed like a keyword hit of `rule`. */
export interface IntentJevConfig {
  enabled: boolean
  /** Name of the rule whose accounts serve requests judged to be probes. */
  rule: string
  /** Probe probability (0-1) at or above which a request counts as a probe. */
  threshold: number
  /** Conversations with more items than this are never sent. */
  max_turns: number
  timeout_ms: number
  model: string
  /** all: every request of the group; listed: only the listed users and API keys. */
  scope: 'all' | 'listed'
  user_ids: number[]
  api_key_ids: number[]
  /** 1-100: share of eligible requests actually sent to Jev. */
  sample_percent: number
  /** Estimated balance below this is flagged; never switches Jev off. */
  low_balance_usd: number
  /** Server-owned: the balance last entered and when (set via jev_set_balance_usd). */
  balance_usd: number
  balance_set_at?: string
  /** Server-owned: set when Jev was switched off after an account-level error. */
  auto_disabled_status?: number
  auto_disabled_detail?: string
  auto_disabled_at?: string
}

export interface IntentJevDayStats {
  date: string
  calls: number
  skipped: number
  probes: number
  errors: number
  tokens: number
  cost_usd: number
}

export interface IntentJevStatus {
  state: 'off' | 'normal' | 'paused' | 'auto_disabled'
  last_success_at?: string
  paused_until?: string
  reason?: string
  auto_disabled_status?: number
  auto_disabled_detail?: string
  auto_disabled_at?: string
  /** Newest first. */
  days: IntentJevDayStats[]
  usd_per_mtok: number
  cny_per_usd: number
  balance?: {
    set_usd: number
    set_at: string
    spent_tokens: number
    spent_usd: number
    estimated_usd: number
    low_balance_usd: number
    low: boolean
  }
}

export interface IntentRouter {
  group_id: number
  enabled: boolean
  classifier_base_url: string
  /** The key itself is write-only; the server only says whether one is stored. */
  classifier_api_key_configured: boolean
  classifier_protocol: IntentClassifierProtocol
  classifier_model: string
  classifier_timeout_ms: number
  cache_ttl_seconds: number
  max_input_chars: number
  rules: IntentRule[]
  /** Write-only like the classifier key. */
  jev_api_key_configured: boolean
  jev: IntentJevConfig
  updated_at: string
}

export interface IntentRouterInput {
  enabled: boolean
  classifier_base_url: string
  /** Leave empty to keep the stored key. */
  classifier_api_key: string
  classifier_protocol: IntentClassifierProtocol
  classifier_model: string
  classifier_timeout_ms: number
  cache_ttl_seconds: number
  max_input_chars: number
  rules: IntentRule[]
  /** Leave empty to keep the stored key. */
  jev_api_key: string
  jev: IntentJevConfig
  /** The balance just read off the TypeSafe console; the estimate restarts from it. */
  jev_set_balance_usd?: number
}

export interface IntentClassifyTestResult {
  /** keyword: decided by a keyword, the classifier was not asked; '' when neither applied. */
  matched_by: 'keyword' | 'jev' | 'classifier' | ''
  keyword?: string
  answer: string
  intent: string
  understood: boolean
  account_ids: number[]
  latency_ms: number
  /** Jev's answer when Jev was asked. */
  probe_probability?: number
}

export interface IntentRouteEvent {
  time: string
  group_id: number
  kind: 'keyword' | 'jev' | 'classified' | 'cached' | 'routed' | 'error' | 'skipped'
  intent?: string
  account_id?: number
  latency_ms?: number
  detail?: string
}

/** One request a keyword sent to a rule's accounts, kept 30 days for review. */
export interface IntentRouteHit {
  id: number
  time: string
  group_id: number
  user_id: number
  api_key_id: number
  request_id: string
  endpoint: string
  model: string
  rule: string
  keyword: string
  /** 0: the latest user message matched; 1: the one before it. */
  user_turn: number
  message_chars: number
  /** The matched message around the keyword. */
  excerpt: string
  /** From the request's usage log; absent when none was written. */
  account_id?: number
  input_tokens?: number
  total_cost?: number
}

export interface IntentRouteKeywordStat {
  keyword: string
  hits: number
  users: number
  api_keys: number
  avg_chars: number
  /** Includes cache reads and cache creation. */
  avg_input_tokens: number
  total_cost: number
  last_hit: string
}

export interface IntentRouteHitQuery {
  group_id?: number
  user_id?: number
  keyword?: string
  hours?: number
  page?: number
  page_size?: number
}

export const intentRoutersAPI = {
  list() {
    return apiClient.get<IntentRouter[]>('/admin/intent-routers')
  },
  save(groupId: number, input: IntentRouterInput) {
    return apiClient.put<IntentRouter>(`/admin/intent-routers/${groupId}`, input)
  },
  remove(groupId: number) {
    return apiClient.delete(`/admin/intent-routers/${groupId}`)
  },
  test(groupId: number, text: string) {
    return apiClient.post<IntentClassifyTestResult>(`/admin/intent-routers/${groupId}/test`, { text })
  },
  /** groupId 0 forgets the conversations of every group. */
  clearCache(groupId: number) {
    return apiClient.post<{ removed: number }>(`/admin/intent-routers/${groupId}/clear-cache`)
  },
  events(limit = 100) {
    return apiClient.get<IntentRouteEvent[]>('/admin/intent-routers/events', { params: { limit } })
  },
  hits(query: IntentRouteHitQuery) {
    return apiClient.get<{ items: IntentRouteHit[]; total: number }>('/admin/intent-routers/hits', { params: query })
  },
  jevStatus(groupId: number) {
    return apiClient.get<IntentJevStatus>(`/admin/intent-routers/${groupId}/jev-status`)
  },
  hitStats(query: IntentRouteHitQuery) {
    return apiClient.get<IntentRouteKeywordStat[]>('/admin/intent-routers/hits/stats', { params: query })
  },
}

export default intentRoutersAPI
