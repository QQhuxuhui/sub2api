import { apiClient } from '../client'
import type { PaginatedResponse } from '@/types'

export type IQProbeJudgeType = 'exact' | 'choice' | 'numeric'
export type IQProbeVerdict = 'correct' | 'wrong' | 'format_error' | 'error' | 'unsupported'
export type IQProbeRunStatus = 'running' | 'scored' | 'invalid' | 'skipped'
export type IQProbeAlertState = 'ok' | 'low' | 'invalid'

export interface IQProbeQuestionSet {
  id: number
  name: string
  description: string
  question_count: number
  enabled_count: number
  monitor_count: number
  created_at: string
  updated_at: string
}

export interface IQProbeQuestion {
  id: number
  set_id: number
  title: string
  prompt: string
  system_prompt: string
  /** Several accepted answers are separated by "||". */
  expected_answer: string
  judge_type: IQProbeJudgeType
  numeric_tolerance: number
  weight: number
  enabled: boolean
  note: string
  sort_order: number
}

export type IQProbeQuestionInput = Omit<IQProbeQuestion, 'id' | 'set_id'>

export interface IQProbeScorePoint {
  at: string
  score: number | null
  status: IQProbeRunStatus
}

export interface IQProbeMonitor {
  id: number
  account_id: number
  group_id: number | null
  model: string
  set_id: number
  interval_minutes: number
  reasoning_effort: string
  max_tokens: number
  enabled: boolean
  alert_enabled: boolean
  /** 0..1 */
  alert_threshold: number
  alert_low_rounds: number
  alert_invalid_rounds: number
  alert_state: IQProbeAlertState
  consecutive_low: number
  consecutive_invalid: number
  last_run_at: string | null
  next_run_at: string | null
  last_status: IQProbeRunStatus | ''
  last_score: number | null
  account_name: string
  account_platform: string
  account_type: string
  account_status: string
  group_name: string
  set_name: string
  /** Last 24 hours of scored / invalid rounds. */
  trend?: IQProbeScorePoint[]
}

export interface IQProbeMonitorInput {
  group_id: number | null
  model: string
  set_id: number
  interval_minutes: number
  reasoning_effort: string
  max_tokens: number
  enabled: boolean
  alert_enabled: boolean
  alert_threshold: number
  alert_low_rounds: number
  alert_invalid_rounds: number
}

export interface IQProbeRun {
  id: number
  monitor_id: number
  account_id: number
  model: string
  set_id: number
  trigger: 'schedule' | 'manual'
  status: IQProbeRunStatus
  score: number | null
  total_count: number
  correct_count: number
  wrong_count: number
  format_error_count: number
  error_count: number
  input_tokens: number
  output_tokens: number
  cost_usd: number
  message: string
  started_at: string
  finished_at: string | null
  account_name: string
  set_name: string
}

export interface IQProbeAnswer {
  id: number
  run_id: number
  question_id: number
  question_title: string
  expected_answer: string
  judge_type: IQProbeJudgeType
  weight: number
  verdict: IQProbeVerdict
  extracted_answer: string
  /** Filled by getAnswer and listRunAnswers; the matrix listing omits it. */
  response_text?: string
  error_message: string
  upstream_model: string
  response_model: string
  input_tokens: number
  output_tokens: number
  reasoning_tokens: number
  latency_ms: number
  first_token_ms: number
  cost_usd: number
  created_at: string
}

export interface IQProbeMatrix {
  runs: IQProbeRun[]
  answers: IQProbeAnswer[]
}

export interface IQProbeSetExport {
  name: string
  description: string
  questions: Array<Partial<IQProbeQuestionInput> & { prompt: string; expected_answer: string }>
}

export interface IQProbeRunQuery {
  monitor_id?: number
  status?: string
  page?: number
  page_size?: number
}

export const iqProbeAPI = {
  listSets() {
    return apiClient.get<IQProbeQuestionSet[]>('/admin/iq-probe/sets')
  },
  createSet(input: { name: string; description: string }) {
    return apiClient.post<IQProbeQuestionSet>('/admin/iq-probe/sets', input)
  },
  updateSet(id: number, input: { name: string; description: string }) {
    return apiClient.put<IQProbeQuestionSet>(`/admin/iq-probe/sets/${id}`, input)
  },
  removeSet(id: number) {
    return apiClient.delete(`/admin/iq-probe/sets/${id}`)
  },
  exportSet(id: number) {
    return apiClient.get<IQProbeSetExport>(`/admin/iq-probe/sets/${id}/export`)
  },
  importSet(data: IQProbeSetExport) {
    return apiClient.post<IQProbeQuestionSet>('/admin/iq-probe/sets/import', data)
  },
  listQuestions(setId: number) {
    return apiClient.get<IQProbeQuestion[]>(`/admin/iq-probe/sets/${setId}/questions`)
  },
  createQuestion(setId: number, input: IQProbeQuestionInput) {
    return apiClient.post<IQProbeQuestion>(`/admin/iq-probe/sets/${setId}/questions`, input)
  },
  updateQuestion(id: number, input: IQProbeQuestionInput) {
    return apiClient.put<IQProbeQuestion>(`/admin/iq-probe/questions/${id}`, input)
  },
  removeQuestion(id: number) {
    return apiClient.delete(`/admin/iq-probe/questions/${id}`)
  },
  listMonitors() {
    return apiClient.get<IQProbeMonitor[]>('/admin/iq-probe/monitors')
  },
  /** One monitor per account; duplicates of account+model+set are skipped. */
  createMonitors(input: IQProbeMonitorInput & { account_ids: number[] }) {
    return apiClient.post<{ created: IQProbeMonitor[]; skipped_account_ids: number[] }>('/admin/iq-probe/monitors', input)
  },
  updateMonitor(id: number, input: IQProbeMonitorInput) {
    return apiClient.put<IQProbeMonitor>(`/admin/iq-probe/monitors/${id}`, input)
  },
  removeMonitor(id: number) {
    return apiClient.delete(`/admin/iq-probe/monitors/${id}`)
  },
  runMonitor(id: number) {
    return apiClient.post<{ started: boolean; run_id: number }>(`/admin/iq-probe/monitors/${id}/run`)
  },
  matrix(id: number, limit = 30) {
    return apiClient.get<IQProbeMatrix>(`/admin/iq-probe/monitors/${id}/matrix`, { params: { limit } })
  },
  listRuns(query: IQProbeRunQuery) {
    return apiClient.get<PaginatedResponse<IQProbeRun>>('/admin/iq-probe/runs', { params: query })
  },
  getRun(id: number) {
    return apiClient.get<IQProbeRun>(`/admin/iq-probe/runs/${id}`)
  },
  /** Every answer of one round, full replies included. */
  listRunAnswers(runId: number) {
    return apiClient.get<IQProbeAnswer[]>(`/admin/iq-probe/runs/${runId}/answers`)
  },
  getAnswer(id: number) {
    return apiClient.get<IQProbeAnswer>(`/admin/iq-probe/answers/${id}`)
  },
}

export default iqProbeAPI
