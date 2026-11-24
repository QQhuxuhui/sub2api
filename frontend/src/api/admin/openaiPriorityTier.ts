/**
 * OpenAI 分组优先级严格分档开关（gptniubi 二开）
 */
import { apiClient } from '../client'

export interface OpenAIPriorityTierSettings {
  group_ids: number[]
}

export async function get(): Promise<OpenAIPriorityTierSettings> {
  const { data } = await apiClient.get<OpenAIPriorityTierSettings>('/admin/settings/openai-priority-tier')
  return data
}

export async function update(groupIds: number[]): Promise<OpenAIPriorityTierSettings> {
  const { data } = await apiClient.put<OpenAIPriorityTierSettings>('/admin/settings/openai-priority-tier', {
    group_ids: groupIds,
  })
  return data
}
