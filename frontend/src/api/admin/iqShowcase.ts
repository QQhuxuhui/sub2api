import { apiClient } from '../client'

export type IQShowcaseProtocol = 'openai_responses' | 'openai_chat' | 'anthropic_messages' | 'gemini'

export const IQ_SHOWCASE_PROTOCOLS: IQShowcaseProtocol[] = ['openai_responses', 'openai_chat', 'anthropic_messages', 'gemini']

export interface IQShowcaseItem {
  id: number
  title: string
  group_id: number
  api_key_id: number | null
  protocol: IQShowcaseProtocol
  model: string
  prompt: string
  reasoning_effort: string
  max_output_tokens: number
  interval_minutes: number
  max_cards: number
  enabled: boolean
  sort_order: number
  next_run_at: string
  lease_until: string | null
  config_rev: number
  created_at: string
  updated_at: string
  group_name: string
  group_platform: string
  api_key_name: string
  /** Only the last four characters; the key itself never reaches the browser. */
  api_key_masked: string
  last_result_at: string | null
  last_success: boolean | null
  success_cards: number
  recent_failures: number
}

export type IQShowcaseItemInput = Pick<
  IQShowcaseItem,
  | 'title'
  | 'group_id'
  | 'api_key_id'
  | 'protocol'
  | 'model'
  | 'prompt'
  | 'reasoning_effort'
  | 'max_output_tokens'
  | 'interval_minutes'
  | 'max_cards'
  | 'enabled'
  | 'sort_order'
>

export interface IQShowcaseResult {
  id: number
  item_id: number
  config_rev: number
  group_id: number
  model: string
  success: boolean
  svg?: string
  error?: string
  http_status: number
  latency_ms: number
  created_at: string
}

export interface IQShowcaseKeyOption {
  id: number
  name: string
  masked: string
  status: string
  user_email: string
}

export const iqShowcaseAdminAPI = {
  listItems() {
    return apiClient.get<IQShowcaseItem[]>('/admin/iq-showcase/items')
  },
  createItem(input: IQShowcaseItemInput) {
    return apiClient.post<IQShowcaseItem>('/admin/iq-showcase/items', input)
  },
  updateItem(id: number, input: IQShowcaseItemInput) {
    return apiClient.put<IQShowcaseItem>(`/admin/iq-showcase/items/${id}`, input)
  },
  /** Switches an item without validating the rest, so a broken one can be stopped. */
  setEnabled(id: number, enabled: boolean) {
    return apiClient.put<IQShowcaseItem>(`/admin/iq-showcase/items/${id}/enabled`, { enabled })
  },
  removeItem(id: number) {
    return apiClient.delete(`/admin/iq-showcase/items/${id}`)
  },
  runItem(id: number) {
    return apiClient.post<{ started: boolean }>(`/admin/iq-showcase/items/${id}/run`)
  },
  listResults(id: number, params: { success?: boolean; limit?: number } = {}) {
    return apiClient.get<IQShowcaseResult[]>(`/admin/iq-showcase/items/${id}/results`, { params })
  },
  keyOptions(groupId: number) {
    return apiClient.get<IQShowcaseKeyOption[]>('/admin/iq-showcase/key-options', { params: { group_id: groupId } })
  },
}

/** Default protocol for a group platform; the admin can still pick another. */
export function defaultIQShowcaseProtocol(platform: string): IQShowcaseProtocol {
  switch (platform) {
    case 'anthropic':
      return 'anthropic_messages'
    case 'gemini':
    case 'antigravity':
      return 'gemini'
    default:
      return 'openai_responses'
  }
}
