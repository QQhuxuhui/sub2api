<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="!form" class="card p-6 text-sm text-gray-500">{{ loading ? t('common.loading') : loadError }}</div>
      <template v-else>
        <!-- 启用分组 + 上限 -->
        <section class="card space-y-4 p-5">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.anthropicFailover.groups.title') }}</h2>
            <label class="flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300" :title="t('admin.anthropicFailover.limits.maxRuleSwitchesHint')">
              <span>{{ t('admin.anthropicFailover.limits.maxRuleSwitches') }}</span>
              <input v-model.number="form.max_rule_switches" type="number" min="1" max="10" class="input w-20" data-test="failover-max-switches" />
            </label>
          </div>
          <div class="flex flex-wrap gap-2">
            <label
              v-for="group in groups"
              :key="group.id"
              class="flex cursor-pointer items-center gap-2 rounded-lg border px-3 py-1.5 text-sm transition-colors"
              :class="form.enabled_group_ids.includes(group.id)
                ? 'border-primary-500 bg-primary-50 text-primary-700 dark:border-primary-400 dark:bg-primary-900/20 dark:text-primary-300'
                : 'border-gray-200 text-gray-700 hover:border-gray-300 dark:border-dark-700 dark:text-gray-300'"
            >
              <input v-model="form.enabled_group_ids" type="checkbox" :value="group.id" :data-test="`failover-group-${group.id}`" />
              <span>{{ group.name }}</span>
            </label>
            <span v-if="groups.length === 0" class="text-sm text-gray-500">{{ t('admin.anthropicFailover.groups.empty') }}</span>
          </div>
        </section>

        <!-- 规则表 -->
        <section class="card space-y-4 p-5">
          <div class="flex flex-wrap items-center justify-between gap-3">
            <h2 class="text-base font-semibold text-gray-900 dark:text-white" :title="t('admin.anthropicFailover.rules.hint')">
              {{ t('admin.anthropicFailover.rules.title') }}
            </h2>
            <button type="button" class="btn btn-secondary text-sm" data-test="failover-add-rule" @click="addRule">
              <Icon name="plus" size="sm" class="mr-1" />{{ t('admin.anthropicFailover.rules.add') }}
            </button>
          </div>
          <div class="overflow-x-auto">
            <table class="w-full min-w-[820px] text-sm">
              <thead>
                <tr class="whitespace-nowrap border-b border-gray-200 text-left text-xs text-gray-500 dark:border-dark-700">
                  <th class="py-2 pr-3 font-medium">{{ t('admin.anthropicFailover.rules.enabled') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.anthropicFailover.rules.name') }}</th>
                  <th class="py-2 pr-3 font-medium" :title="t('admin.anthropicFailover.rules.statusHint')">{{ t('admin.anthropicFailover.rules.status') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.anthropicFailover.rules.keywords') }}</th>
                  <th class="py-2 pr-3 font-medium" :title="t('admin.anthropicFailover.rules.actionHint')">{{ t('admin.anthropicFailover.rules.action') }}</th>
                  <th class="py-2 pr-3 text-right font-medium" :title="statsWindowText">{{ t('admin.anthropicFailover.rules.hits') }}</th>
                  <th class="py-2 pr-3 text-right font-medium" :title="statsWindowText">{{ t('admin.anthropicFailover.rules.recovery') }}</th>
                  <th class="py-2 font-medium"></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(rule, index) in form.rules" :key="rule.key" class="border-b border-gray-100 align-top dark:border-dark-800" data-test="failover-rule">
                  <td class="py-2 pr-3"><input v-model="rule.enabled" type="checkbox" class="mt-2.5" /></td>
                  <td class="py-2 pr-3"><input v-model="rule.name" class="input w-40" /></td>
                  <td class="py-2 pr-3">
                    <input v-model.number="rule.status_code" type="number" min="400" max="499" class="input w-24" :placeholder="t('admin.anthropicFailover.rules.anyStatus')" />
                  </td>
                  <td class="py-2 pr-3">
                    <textarea v-model="rule.keywordsText" rows="2" class="input min-w-[240px] font-mono text-xs" :placeholder="t('admin.anthropicFailover.rules.keywordsPlaceholder')"></textarea>
                  </td>
                  <td class="py-2 pr-3">
                    <select v-model="rule.action" class="input w-28">
                      <option value="failover">{{ t('admin.anthropicFailover.rules.actionFailover') }}</option>
                      <option value="return">{{ t('admin.anthropicFailover.rules.actionReturn') }}</option>
                    </select>
                  </td>
                  <td class="py-2 pr-3 pt-4 text-right tabular-nums">{{ ruleStats(rule.id).hits.toLocaleString() }}</td>
                  <td class="py-2 pr-3 pt-4 text-right tabular-nums" :class="recoveryClass(ruleStats(rule.id))">{{ recoveryRate(ruleStats(rule.id)) }}</td>
                  <td class="whitespace-nowrap py-2 pt-3">
                    <button type="button" class="rounded p-1 text-gray-500 hover:text-gray-900 disabled:opacity-30 dark:hover:text-white" :disabled="index === 0" :title="t('admin.anthropicFailover.rules.moveUp')" :aria-label="t('admin.anthropicFailover.rules.moveUp')" @click="moveRule(index, -1)">
                      <Icon name="arrowUp" size="sm" />
                    </button>
                    <button type="button" class="rounded p-1 text-gray-500 hover:text-gray-900 disabled:opacity-30 dark:hover:text-white" :disabled="index === form.rules.length - 1" :title="t('admin.anthropicFailover.rules.moveDown')" :aria-label="t('admin.anthropicFailover.rules.moveDown')" @click="moveRule(index, 1)">
                      <Icon name="arrowDown" size="sm" />
                    </button>
                    <button type="button" class="rounded p-1 text-red-500 hover:text-red-700" :title="t('common.delete')" :aria-label="t('common.delete')" data-test="failover-delete-rule" @click="form.rules.splice(index, 1)">
                      <Icon name="trash" size="sm" />
                    </button>
                  </td>
                </tr>
                <tr v-if="form.rules.length === 0">
                  <td colspan="8" class="py-8 text-center text-gray-500">{{ t('admin.anthropicFailover.rules.empty') }}</td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="flex items-center gap-3">
            <button type="button" class="btn btn-primary" data-test="failover-save" :disabled="saving" @click="save">{{ t('common.save') }}</button>
            <button type="button" class="btn btn-secondary" :disabled="loading" :title="t('common.refresh')" :aria-label="t('common.refresh')" @click="load">
              <Icon name="refresh" size="sm" />
            </button>
          </div>
        </section>

        <!-- 流内报错换号统计 -->
        <section class="card flex flex-wrap items-center justify-between gap-4 p-5" :title="t('admin.anthropicFailover.stream.hint')" data-test="failover-stream-stats">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.anthropicFailover.stream.title') }}</h2>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ statsWindowText }}</p>
          </div>
          <dl class="flex gap-8 text-right">
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.anthropicFailover.rules.hits') }}</dt>
              <dd class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">{{ stats.stream_hits.toLocaleString() }}</dd>
            </div>
            <div>
              <dt class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.anthropicFailover.rules.recovery') }}</dt>
              <dd class="text-2xl font-semibold tabular-nums" :class="recoveryClass(streamStats) || 'text-gray-900 dark:text-white'">{{ recoveryRate(streamStats) }}</dd>
            </div>
          </dl>
        </section>

        <!-- 最近事件 -->
        <details class="card p-5" data-test="failover-recent">
          <summary class="cursor-pointer text-base font-semibold text-gray-900 dark:text-white">
            {{ t('admin.anthropicFailover.recent.title') }}
            <span class="ml-2 text-sm font-normal text-gray-500">{{ recent.length }}</span>
          </summary>
          <div class="mt-4 max-h-[28rem] overflow-auto">
            <table class="w-full min-w-[680px] text-sm">
              <thead>
                <tr class="whitespace-nowrap border-b border-gray-200 text-left text-xs text-gray-500 dark:border-dark-700">
                  <th class="py-2 pr-3 font-medium">{{ t('admin.anthropicFailover.recent.time') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.anthropicFailover.recent.reason') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.anthropicFailover.recent.status') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.anthropicFailover.recent.group') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.anthropicFailover.recent.accounts') }}</th>
                  <th class="py-2 font-medium">{{ t('admin.anthropicFailover.recent.result') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(event, index) in recent" :key="index" class="border-b border-gray-100 dark:border-dark-800">
                  <td class="whitespace-nowrap py-2 pr-3 text-gray-500">{{ new Date(event.time).toLocaleString() }}</td>
                  <td class="py-2 pr-3">{{ eventReason(event) }}</td>
                  <td class="py-2 pr-3 tabular-nums">{{ event.status_code || '—' }}</td>
                  <td class="py-2 pr-3">{{ groupName(event.group_id) }}</td>
                  <td class="py-2 pr-3 font-mono text-xs">{{ (event.accounts || []).join(' → ') }}</td>
                  <td class="py-2" :class="eventResultClass(event)">{{ eventResult(event) }}</td>
                </tr>
                <tr v-if="recent.length === 0">
                  <td colspan="6" class="py-6 text-center text-gray-500">{{ t('admin.anthropicFailover.recent.empty') }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </details>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Icon from '@/components/icons/Icon.vue'
import { adminAPI } from '@/api/admin'
import {
  ANTHROPIC_FAILOVER_STREAM_REASON,
  type AnthropicFailoverAction,
  type AnthropicFailoverConfig,
  type AnthropicFailoverEvent,
  type AnthropicFailoverRuleStats,
  type AnthropicFailoverStats
} from '@/api/admin/anthropicFailover'
import { useAppStore } from '@/stores/app'

interface RuleRow {
  key: number
  id: string
  name: string
  enabled: boolean
  status_code: number | ''
  keywordsText: string
  action: AnthropicFailoverAction
}

interface FormState extends Omit<AnthropicFailoverConfig, 'rules'> {
  rules: RuleRow[]
}

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(true)
const loadError = ref('')
const saving = ref(false)
const form = ref<FormState | null>(null)
const groups = ref<{ id: number; name: string }[]>([])
const stats = ref<AnthropicFailoverStats>({ rules: [], stream_hits: 0, stream_recovered: 0, window_days: 7 })
const recent = ref<AnthropicFailoverEvent[]>([])
let nextRuleKey = 1

const statsWindowText = computed(() => t('admin.anthropicFailover.statsWindow', { days: stats.value.window_days || 7 }))
const streamStats = computed<AnthropicFailoverRuleStats>(() => ({
  rule_id: ANTHROPIC_FAILOVER_STREAM_REASON,
  hits: stats.value.stream_hits,
  recovered: stats.value.stream_recovered
}))

function errorText(err: unknown): string {
  const failure = err as { message?: string; response?: { data?: { message?: string } } }
  return failure.response?.data?.message || failure.message || t('admin.anthropicFailover.failed')
}

function toForm(config: AnthropicFailoverConfig): FormState {
  return {
    enabled_group_ids: config.enabled_group_ids || [],
    max_rule_switches: config.max_rule_switches,
    rules: (config.rules || []).map((rule) => ({
      key: nextRuleKey++,
      id: rule.id,
      name: rule.name,
      enabled: rule.enabled,
      status_code: rule.status_code || '',
      keywordsText: (rule.keywords || []).join('\n'),
      action: rule.action
    }))
  }
}

function toConfig(state: FormState): AnthropicFailoverConfig {
  return {
    enabled_group_ids: state.enabled_group_ids,
    max_rule_switches: state.max_rule_switches,
    rules: state.rules.map((rule) => ({
      id: rule.id,
      name: rule.name,
      enabled: rule.enabled,
      status_code: Number(rule.status_code) || 0,
      keywords: rule.keywordsText.split('\n').map((keyword) => keyword.trim()).filter(Boolean),
      action: rule.action
    }))
  }
}

async function load() {
  loading.value = true
  try {
    const [overview, anthropicGroups] = await Promise.all([
      adminAPI.anthropicFailover.getOverview(),
      adminAPI.groups.getAll('anthropic')
    ])
    form.value = toForm(overview.config)
    stats.value = overview.stats
    recent.value = overview.recent || []
    groups.value = anthropicGroups.map((group) => ({ id: group.id, name: group.name }))
  } catch (err) {
    loadError.value = errorText(err)
    appStore.showError(loadError.value)
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!form.value) return
  const config = toConfig(form.value)
  if (config.rules.some((rule) => rule.keywords.length === 0)) {
    appStore.showError(t('admin.anthropicFailover.rules.keywordRequired'))
    return
  }
  saving.value = true
  try {
    form.value = toForm(await adminAPI.anthropicFailover.updateConfig(config))
    appStore.showSuccess(t('admin.anthropicFailover.saved'))
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    saving.value = false
  }
}

function addRule() {
  form.value?.rules.push({ key: nextRuleKey++, id: '', name: '', enabled: true, status_code: '', keywordsText: '', action: 'failover' })
}

function moveRule(index: number, delta: number) {
  const rules = form.value?.rules
  if (!rules) return
  const [rule] = rules.splice(index, 1)
  rules.splice(index + delta, 0, rule)
}

function ruleStats(id: string): AnthropicFailoverRuleStats {
  return stats.value.rules.find((row) => row.rule_id === id) || { rule_id: id, hits: 0, recovered: 0 }
}

function recoveryRate(row: AnthropicFailoverRuleStats): string {
  return row.hits ? `${((row.recovered / row.hits) * 100).toFixed(1)}%` : '—'
}

// A rule that keeps matching without ever recovering only adds upstream calls.
function recoveryClass(row: AnthropicFailoverRuleStats): string {
  if (row.hits < 20) return ''
  const rate = row.recovered / row.hits
  if (rate < 0.1) return 'text-red-600 dark:text-red-400'
  if (rate >= 0.5) return 'text-emerald-600 dark:text-emerald-400'
  return ''
}

function groupName(id: number): string {
  return groups.value.find((group) => group.id === id)?.name || String(id)
}

function eventReason(event: AnthropicFailoverEvent): string {
  if (event.reason === ANTHROPIC_FAILOVER_STREAM_REASON) return t('admin.anthropicFailover.stream.title')
  return form.value?.rules.find((rule) => rule.id === event.reason)?.name || event.reason
}

function eventResult(event: AnthropicFailoverEvent): string {
  if (event.canceled) return t('admin.anthropicFailover.recent.canceled')
  return event.recovered ? t('admin.anthropicFailover.recent.recovered') : t('admin.anthropicFailover.recent.failed')
}

function eventResultClass(event: AnthropicFailoverEvent): string {
  if (event.canceled) return 'text-gray-500'
  return event.recovered ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'
}

onMounted(load)
</script>
