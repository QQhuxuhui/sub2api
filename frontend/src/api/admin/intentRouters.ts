import { apiClient } from '../client'

export interface IntentRule {
  name: string
  description: string
  /** Matched against the two latest user messages on every turn, before the classifier. */
  keywords?: string[]
  account_ids: number[]
  /** Replaces account_ids: matched requests are scheduled by the group of this platform API key (never billed to it). */
  api_key_id?: number
  enabled: boolean
}

/** An API key a rule may target; problem is empty when the key resolves to an active group. */
export interface IntentRuleKeyLabel {
  id: number
  name: string
  user_id: number
  user_email: string
  group_id: number
  group_name: string
  group_platform: string
  problem: '' | 'key_not_found' | 'key_disabled' | 'key_expired' | 'key_no_group' | 'group_not_found' | 'group_inactive'
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
  /** all: every request of the group; listed: only the listed users. */
  scope: 'all' | 'listed'
  user_ids: number[]
  /** Narrows a listed user to some of their keys; a user with none listed is covered with all keys. user_id is filled by the server. */
  api_keys: { id: number; user_id?: number }[]
  /** 1-100: share of eligible requests actually sent to Jev. */
  sample_percent: number
  /** Also ask Jev whether the request is a liveness/health check (sub2api channel monitor, "hi", "reply OK"). */
  health_enabled: boolean
  /** Health probability (0-1) at or above which a request counts as a health check. */
  health_threshold: number
  /** Rule health checks go to; "" leaves them to the group's ordinary scheduling. */
  health_rule: string
  /** Also ask Jev, on the same call, whether answering needs current real-world information. */
  timely_enabled: boolean
  /** Time-sensitivity probability (0-1) at or above which a request is routed to timely_rule. */
  timely_threshold: number
  /** Rule time-sensitive requests go to; required while timely_enabled is on. */
  timely_rule: string
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
  health?: number
  errors: number
  tokens: number
  cost_usd: number
  /** Calls that carried a usable time-sensitivity answer. */
  timely_answered?: number
  /** At or above the time-sensitivity threshold (also when a health check or probe took the request). */
  timely_hits?: number
  /** Actually routed to the time-sensitivity rule. */
  timely_routed?: number
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
  /** Keywords only count in a user message of at most this many characters; 0 = no limit. */
  keyword_max_chars: number
  /** HTTP status sent when a matched rule's accounts are all unavailable: 429, 502 or 503. */
  unavailable_status: number
  rules: IntentRule[]
  /** Requests for these models skip intent routing entirely; a trailing * matches a prefix. */
  excluded_models: string[]
  /** Write-only like the classifier key. */
  jev_api_key_configured: boolean
  jev: IntentJevConfig
  /** Names of the users and keys in jev; deleted ones are missing. */
  jev_scope?: { users: { id: number; email: string; username: string }[]; api_keys: { id: number; name: string; user_id: number }[] }
  /** The API keys of the rules that target one; broken ones carry a problem. */
  rule_keys?: IntentRuleKeyLabel[]
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
  keyword_max_chars: number
  unavailable_status: number
  rules: IntentRule[]
  excluded_models: string[]
  /** Leave empty to keep the stored key. */
  jev_api_key: string
  jev: IntentJevConfig
  /** The balance just read off the TypeSafe console; the estimate restarts from it. */
  jev_set_balance_usd?: number
}

export interface IntentClassifyTestResult {
  /** keyword: decided by a keyword, the classifier was not asked; '' when neither applied. */
  matched_by: 'keyword' | 'manual' | 'jev' | 'classifier' | ''
  keyword?: string
  answer: string
  intent: string
  understood: boolean
  account_ids: number[]
  /** Set when the matched rule targets an API key; target_group_id is absent when the key does not resolve. */
  api_key_id?: number
  target_group_id?: number
  latency_ms: number
  /** Jev's answer when Jev was asked. */
  probe_probability?: number
  /** Jev's health-check answer, when health checks are on. */
  health_probability?: number
  health_check?: boolean
  /** Jev's time-sensitivity answer, when that question is on; timely: routed for it. */
  timely_probability?: number
  timely?: boolean
  /** Jev's latency when the classifier decided after it. */
  jev_latency_ms?: number
  /** Admin judgement of the text's template, when there is one. */
  manual_label?: IntentTemplateDecision
  /** Judged "probe" but the group's Jev target rule is missing: testing went on. */
  manual_target_missing?: boolean
  template_key?: string
}

export type IntentTemplateDecision = 'probe' | 'health' | 'normal'
export type IntentTemplateStatus = 'pending' | IntentTemplateDecision

/** A message template queued for, or given, an admin judgement (applies to every group). */
export interface IntentRouteTemplate {
  key: string
  template: string
  sample: string
  text_chars: number
  status: IntentTemplateStatus
  first_seen_at: string
  last_seen_at: string
  seen_count: number
  /** a: called a health check yet the probe score is high; b: judged two ways; c: probe score just under the line. */
  reason_a: number
  reason_b: number
  reason_c: number
  verdict_route: number
  verdict_health: number
  verdict_normal: number
  probe_min: number | null
  probe_max: number | null
  health_min: number | null
  health_max: number | null
  user_ids: number[]
  group_ids: number[]
  decided_at: string | null
  decided_by: number | null
  revision: number
}

export interface IntentRouteTemplateQuery {
  status?: IntentTemplateStatus | 'all'
  reason?: '' | 'a' | 'b' | 'c'
  q?: string
  page?: number
  page_size?: number
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

/** Answering sub2api channel-monitor challenges by program; the group's own setting, independent of its router. */
export interface MonitorProbeAnswerConfig {
  enabled: boolean
  /** all: every request of the group; listed: only the listed users (narrowed to api_keys when any of theirs are listed). */
  scope: 'all' | 'listed'
  user_ids: number[]
  /** user_id is filled by the server. */
  api_keys: { id: number; user_id?: number }[]
}

export interface MonitorProbeAnswerView {
  group_id: number
  config: MonitorProbeAnswerConfig
  /** Names of the users and keys in config; deleted ones are missing. */
  scope_labels: { users: { id: number; email: string; username: string }[]; api_keys: { id: number; name: string; user_id: number }[] }
}

export interface MonitorProbeAnswerRecord {
  time: string
  group_id: number
  user_id: number
  api_key_id: number
  endpoint: string
  model: string
  /** e.g. "37 + 64" */
  question: string
  answer: string
}

export interface MonitorProbeAnswerStatus {
  /** Today first. */
  days: { date: string; answers: number }[]
  /** Newest first, at most 500. */
  recent: MonitorProbeAnswerRecord[]
}

/** Early first event on OpenAI streams; the group's own setting, independent of its router. */
export interface OpenAIStreamPreludeConfig {
  enabled: boolean
  /** The early event goes out at a random moment in [min, max] ms after arrival, unless real output or an error came first. */
  delay_min_ms: number
  delay_max_ms: number
  /** all: every request of the group; listed: only the listed users (narrowed to api_keys when any of theirs are listed). */
  scope: 'all' | 'listed'
  user_ids: number[]
  /** user_id is filled by the server. */
  api_keys: { id: number; user_id?: number }[]
}

export interface OpenAIStreamPreludeView {
  group_id: number
  config: OpenAIStreamPreludeConfig
  /** Names of the users and keys in config; deleted ones are missing. */
  scope_labels: { users: { id: number; email: string; username: string }[]; api_keys: { id: number; name: string; user_id: number }[] }
}

export interface OpenAIStreamPreludeRecord {
  time: string
  group_id: number
  user_id: number
  api_key_id: number
  endpoint: string
  model: string
  /** From the request's arrival to the early event. */
  prelude_ms: number
  /** From the request's arrival to the first real byte; missing when none came. */
  output_ms?: number
  heartbeats: number
  outcome: 'ok' | 'failed' | 'client_gone'
}

export interface OpenAIStreamPreludeStatus {
  /** Today first. */
  days: { date: string; sent: number; failed: number }[]
  /** Newest first, at most 500. */
  recent: OpenAIStreamPreludeRecord[]
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
  /** API keys a rule may target, by key name, ID or owner email; platform narrows to groups of one platform. */
  targetKeys(q: string, platform?: string) {
    return apiClient.get<IntentRuleKeyLabel[]>('/admin/intent-routers/target-keys', { params: { q, ...(platform ? { platform } : {}) } })
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
  monitorAnswer(groupId: number) {
    return apiClient.get<MonitorProbeAnswerView>(`/admin/intent-routers/${groupId}/monitor-answer`)
  },
  saveMonitorAnswer(groupId: number, config: MonitorProbeAnswerConfig) {
    return apiClient.put<MonitorProbeAnswerView>(`/admin/intent-routers/${groupId}/monitor-answer`, config)
  },
  monitorAnswerStatus(groupId: number) {
    return apiClient.get<MonitorProbeAnswerStatus>(`/admin/intent-routers/${groupId}/monitor-answer/status`)
  },
  streamPrelude(groupId: number) {
    return apiClient.get<OpenAIStreamPreludeView>(`/admin/intent-routers/${groupId}/stream-prelude`)
  },
  saveStreamPrelude(groupId: number, config: OpenAIStreamPreludeConfig) {
    return apiClient.put<OpenAIStreamPreludeView>(`/admin/intent-routers/${groupId}/stream-prelude`, config)
  },
  streamPreludeStatus(groupId: number) {
    return apiClient.get<OpenAIStreamPreludeStatus>(`/admin/intent-routers/${groupId}/stream-prelude/status`)
  },
  templates(query: IntentRouteTemplateQuery) {
    return apiClient.get<{ items: IntentRouteTemplate[]; total: number }>('/admin/intent-routers/templates', { params: query })
  },
  decideTemplate(key: string, status: IntentTemplateDecision, revision: number) {
    return apiClient.put<IntentRouteTemplate>(`/admin/intent-routers/templates/${key}/decision`, { status, revision })
  },
  revokeTemplate(key: string, revision: number) {
    return apiClient.delete<IntentRouteTemplate>(`/admin/intent-routers/templates/${key}/decision`, { params: { revision } })
  },
  hitStats(query: IntentRouteHitQuery) {
    return apiClient.get<IntentRouteKeywordStat[]>('/admin/intent-routers/hits/stats', { params: query })
  },
}

export default intentRoutersAPI
