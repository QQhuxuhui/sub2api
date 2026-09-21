import { apiClient } from '../client'

export interface IntentRule {
  name: string
  description: string
  account_ids: number[]
  enabled: boolean
}

export type IntentClassifierProtocol = 'openai_chat' | 'gemini'

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
}

export interface IntentClassifyTestResult {
  answer: string
  intent: string
  understood: boolean
  account_ids: number[]
  latency_ms: number
}

export interface IntentRouteEvent {
  time: string
  group_id: number
  kind: 'classified' | 'cached' | 'routed' | 'error' | 'skipped'
  intent?: string
  account_id?: number
  latency_ms?: number
  detail?: string
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
}

export default intentRoutersAPI
