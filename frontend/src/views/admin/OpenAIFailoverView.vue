<template>
  <AppLayout>
    <div class="space-y-6">
      <div v-if="!form" class="card p-6 text-sm text-gray-500">{{ loading ? t('common.loading') : loadError }}</div>
      <template v-else>
        <!-- 分组开关与上限 -->
        <section class="card space-y-4 p-5">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.openaiFailover.groups.title') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.openaiFailover.groups.hint') }}</p>
          </div>
          <div class="flex flex-wrap gap-x-5 gap-y-2">
            <label v-for="group in groups" :key="group.id" class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
              <input v-model="form.enabled_group_ids" type="checkbox" :value="group.id" :data-test="`failover-group-${group.id}`" />
              <span>{{ group.name }}</span>
            </label>
            <span v-if="groups.length === 0" class="text-sm text-gray-500">{{ t('admin.openaiFailover.groups.empty') }}</span>
          </div>
          <div class="grid gap-4 sm:grid-cols-3">
            <div>
              <label class="input-label">{{ t('admin.openaiFailover.limits.slotWait') }}</label>
              <input v-model.number="form.slot_wait_ms" type="number" min="500" max="10000" step="100" class="input" />
              <p class="input-hint">{{ t('admin.openaiFailover.limits.slotWaitHint') }}</p>
            </div>
            <div>
              <label class="input-label">{{ t('admin.openaiFailover.limits.maxSlotSwitches') }}</label>
              <input v-model.number="form.max_slot_switches" type="number" min="0" max="10" class="input" />
            </div>
            <div>
              <label class="input-label">{{ t('admin.openaiFailover.limits.maxRuleSwitches') }}</label>
              <input v-model.number="form.max_rule_switches" type="number" min="1" max="10" class="input" />
            </div>
          </div>
        </section>

        <!-- 规则表 -->
        <section class="card space-y-4 p-5">
          <div class="flex flex-wrap items-start justify-between gap-3">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.openaiFailover.rules.title') }}</h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.openaiFailover.rules.hint') }}</p>
            </div>
            <button type="button" class="btn btn-secondary text-sm" data-test="failover-add-rule" @click="addRule">{{ t('admin.openaiFailover.rules.add') }}</button>
          </div>
          <div class="overflow-x-auto">
            <table class="w-full min-w-[860px] text-sm">
              <thead>
                <tr class="border-b border-gray-200 text-left text-xs text-gray-500 dark:border-dark-700">
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.rules.enabled') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.rules.name') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.rules.status') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.rules.keywords') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.rules.action') }}</th>
                  <th class="py-2 pr-3 text-right font-medium">{{ t('admin.openaiFailover.rules.hits') }}</th>
                  <th class="py-2 pr-3 text-right font-medium">{{ t('admin.openaiFailover.rules.recovery') }}</th>
                  <th class="py-2 font-medium"></th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(rule, index) in form.rules" :key="rule.key" class="border-b border-gray-100 align-top dark:border-dark-800" data-test="failover-rule">
                  <td class="py-2 pr-3"><input v-model="rule.enabled" type="checkbox" /></td>
                  <td class="py-2 pr-3"><input v-model="rule.name" class="input w-44" /></td>
                  <td class="py-2 pr-3">
                    <input v-model.number="rule.status_code" type="number" min="0" max="499" class="input w-24" :placeholder="t('admin.openaiFailover.rules.anyStatus')" />
                  </td>
                  <td class="py-2 pr-3">
                    <textarea v-model="rule.keywordsText" rows="2" class="input min-w-[260px] font-mono text-xs" :placeholder="t('admin.openaiFailover.rules.keywordsPlaceholder')"></textarea>
                  </td>
                  <td class="py-2 pr-3">
                    <select v-model="rule.action" class="input w-28">
                      <option value="failover">{{ t('admin.openaiFailover.rules.actionFailover') }}</option>
                      <option value="return">{{ t('admin.openaiFailover.rules.actionReturn') }}</option>
                    </select>
                  </td>
                  <td class="py-2 pr-3 text-right tabular-nums">{{ ruleStats(rule.id).hits.toLocaleString() }}</td>
                  <td class="py-2 pr-3 text-right tabular-nums">{{ recoveryRate(ruleStats(rule.id)) }}</td>
                  <td class="whitespace-nowrap py-2">
                    <button type="button" class="px-1 text-gray-500 hover:text-gray-900 disabled:opacity-30 dark:hover:text-white" :disabled="index === 0" :title="t('admin.openaiFailover.rules.moveUp')" @click="moveRule(index, -1)">↑</button>
                    <button type="button" class="px-1 text-gray-500 hover:text-gray-900 disabled:opacity-30 dark:hover:text-white" :disabled="index === form.rules.length - 1" :title="t('admin.openaiFailover.rules.moveDown')" @click="moveRule(index, 1)">↓</button>
                    <button type="button" class="px-1 text-red-500 hover:text-red-700" :title="t('common.delete')" @click="form.rules.splice(index, 1)">×</button>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
          <div class="flex items-center gap-3">
            <button type="button" class="btn btn-primary" data-test="failover-save" :disabled="saving" @click="save">{{ t('common.save') }}</button>
            <span class="text-xs text-gray-500">{{ t('admin.openaiFailover.statsWindow') }}</span>
          </div>
        </section>

        <!-- 统计 -->
        <div class="grid grid-cols-1 gap-4 sm:grid-cols-2">
          <div class="card p-4">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.openaiFailover.summary.slotSwitched') }}</p>
            <p class="mt-1 text-2xl font-semibold text-gray-900 dark:text-white">{{ stats.slot_switched.toLocaleString() }}</p>
            <p class="mt-1 text-xs text-gray-500">{{ t('admin.openaiFailover.summary.recovered') }}: {{ stats.slot_recovered.toLocaleString() }}</p>
          </div>
          <div class="card p-4">
            <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.openaiFailover.summary.rewritten') }}</p>
            <p class="mt-1 text-2xl font-semibold text-gray-900 dark:text-white">{{ stats.rewritten_requests.toLocaleString() }}</p>
            <p class="mt-1 text-xs text-gray-500">{{ t('admin.openaiFailover.summary.rewrittenItems') }}: {{ stats.rewritten_items.toLocaleString() }}</p>
          </div>
        </div>

        <!-- 不支持加密参数的账号 -->
        <section class="card space-y-4 p-5">
          <div>
            <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.openaiFailover.accounts.title') }}</h2>
            <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('admin.openaiFailover.accounts.hint') }}</p>
          </div>
          <input v-model.trim="accountSearch" type="search" class="input max-w-sm" :placeholder="t('admin.openaiFailover.accounts.search')" />
          <div class="max-h-[32rem] overflow-auto">
            <table class="w-full min-w-[760px] text-sm">
              <thead>
                <tr class="border-b border-gray-200 text-left text-xs text-gray-500 dark:border-dark-700">
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.accounts.name') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.accounts.mark') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.accounts.model') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.accounts.check') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="account in visibleAccounts" :key="account.id" class="border-b border-gray-100 dark:border-dark-800" data-test="failover-account">
                  <td class="py-2 pr-3">
                    <span class="text-gray-900 dark:text-white">{{ account.name }}</span>
                    <span class="ml-2 text-xs text-gray-500">#{{ account.id }}{{ account.schedulable ? '' : ` · ${t('admin.openaiFailover.accounts.paused')}` }}</span>
                  </td>
                  <td class="py-2 pr-3">
                    <input type="checkbox" :checked="account.no_encrypted_args" :disabled="busyAccounts.has(account.id)" :data-test="`failover-mark-${account.id}`" @change="toggleMark(account, $event)" />
                  </td>
                  <td class="py-2 pr-3">
                    <input v-if="account.can_check" v-model.trim="checkModels[account.id]" class="input w-40 font-mono text-xs" :placeholder="account.default_check_model" />
                  </td>
                  <td class="py-2 pr-3">
                    <div v-if="account.can_check" class="flex flex-wrap items-center gap-2">
                      <button type="button" class="btn btn-secondary text-xs" :disabled="busyAccounts.has(account.id)" @click="runCheck(account)">
                        {{ busyAccounts.has(account.id) ? t('admin.openaiFailover.accounts.checking') : t('admin.openaiFailover.accounts.runCheck') }}
                      </button>
                      <span v-if="checks[account.id]" class="text-xs" :class="verdictClass(checks[account.id].verdict)" :title="checks[account.id].last_error">
                        {{ t(`admin.openaiFailover.accounts.verdict.${checks[account.id].verdict}`) }}
                        ({{ t('admin.openaiFailover.accounts.counts', { ...checks[account.id] }) }})
                      </span>
                    </div>
                    <span v-else class="text-xs text-gray-500">{{ t('admin.openaiFailover.accounts.noCheck') }}</span>
                  </td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>

        <!-- 最近记录 -->
        <section class="card space-y-3 p-5">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('admin.openaiFailover.recent.title') }}</h2>
          <div class="max-h-96 overflow-auto">
            <table class="w-full min-w-[720px] text-sm">
              <thead>
                <tr class="border-b border-gray-200 text-left text-xs text-gray-500 dark:border-dark-700">
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.recent.time') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.recent.reason') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.recent.group') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.recent.apiKey') }}</th>
                  <th class="py-2 pr-3 font-medium">{{ t('admin.openaiFailover.recent.accounts') }}</th>
                  <th class="py-2 font-medium">{{ t('admin.openaiFailover.recent.result') }}</th>
                </tr>
              </thead>
              <tbody>
                <tr v-for="(event, index) in recent" :key="index" class="border-b border-gray-100 dark:border-dark-800">
                  <td class="whitespace-nowrap py-2 pr-3 text-gray-500">{{ new Date(event.time).toLocaleString() }}</td>
                  <td class="py-2 pr-3">{{ eventReason(event) }}</td>
                  <td class="py-2 pr-3">{{ event.group_id }}</td>
                  <td class="py-2 pr-3">{{ event.api_key_id }}</td>
                  <td class="py-2 pr-3 font-mono text-xs">{{ (event.accounts || []).join(' → ') }}</td>
                  <td class="py-2" :class="event.recovered ? 'text-emerald-600 dark:text-emerald-400' : 'text-red-600 dark:text-red-400'">
                    {{ event.recovered ? t('admin.openaiFailover.recent.recovered') : t('admin.openaiFailover.recent.failed') }}
                  </td>
                </tr>
                <tr v-if="recent.length === 0">
                  <td colspan="6" class="py-6 text-center text-gray-500">{{ t('admin.openaiFailover.recent.empty') }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </section>
      </template>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import { adminAPI } from '@/api/admin'
import type {
  OpenAIEncryptedArgsCheckResult,
  OpenAIFailoverAccount,
  OpenAIFailoverAction,
  OpenAIFailoverConfig,
  OpenAIFailoverEvent,
  OpenAIFailoverRuleStats,
  OpenAIFailoverStats
} from '@/api/admin/openaiFailover'
import { useAppStore } from '@/stores/app'

interface RuleRow {
  key: number
  id: string
  name: string
  enabled: boolean
  status_code: number | ''
  keywordsText: string
  action: OpenAIFailoverAction
}

interface FormState extends Omit<OpenAIFailoverConfig, 'rules'> {
  rules: RuleRow[]
}

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(true)
const loadError = ref('')
const saving = ref(false)
const form = ref<FormState | null>(null)
const groups = ref<{ id: number; name: string }[]>([])
const stats = ref<OpenAIFailoverStats>({ rules: [], slot_switched: 0, slot_recovered: 0, rewritten_requests: 0, rewritten_items: 0 })
const recent = ref<OpenAIFailoverEvent[]>([])
const accounts = ref<OpenAIFailoverAccount[]>([])
const accountSearch = ref('')
const checks = reactive<Record<number, OpenAIEncryptedArgsCheckResult>>({})
const checkModels = reactive<Record<number, string>>({})
const busyAccounts = reactive(new Set<number>())
let nextRuleKey = 1

const visibleAccounts = computed(() => {
  const search = accountSearch.value.toLowerCase()
  if (!search) return accounts.value
  return accounts.value.filter((account) => account.name.toLowerCase().includes(search) || String(account.id) === search)
})

function errorText(err: unknown): string {
  const failure = err as { message?: string; response?: { data?: { message?: string } } }
  return failure.response?.data?.message || failure.message || t('admin.openaiFailover.failed')
}

function toForm(config: OpenAIFailoverConfig): FormState {
  return {
    ...config,
    enabled_group_ids: config.enabled_group_ids || [],
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

function toConfig(state: FormState): OpenAIFailoverConfig {
  return {
    enabled_group_ids: state.enabled_group_ids,
    slot_wait_ms: state.slot_wait_ms,
    max_rule_switches: state.max_rule_switches,
    max_slot_switches: state.max_slot_switches,
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
    const [overview, openaiGroups] = await Promise.all([
      adminAPI.openaiFailover.getOverview(),
      adminAPI.groups.getAll('openai')
    ])
    form.value = toForm(overview.config)
    stats.value = overview.stats
    recent.value = overview.recent || []
    accounts.value = overview.accounts || []
    groups.value = openaiGroups.map((group) => ({ id: group.id, name: group.name }))
  } catch (err) {
    loadError.value = errorText(err)
    appStore.showError(loadError.value)
  } finally {
    loading.value = false
  }
}

async function save() {
  if (!form.value) return
  saving.value = true
  try {
    form.value = toForm(await adminAPI.openaiFailover.updateConfig(toConfig(form.value)))
    appStore.showSuccess(t('admin.openaiFailover.saved'))
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

function ruleStats(id: string): OpenAIFailoverRuleStats {
  return stats.value.rules.find((row) => row.rule_id === id) || { rule_id: id, hits: 0, recovered: 0 }
}

function recoveryRate(row: OpenAIFailoverRuleStats): string {
  return row.hits ? `${((row.recovered / row.hits) * 100).toFixed(1)}%` : '—'
}

function eventReason(event: OpenAIFailoverEvent): string {
  if (!event.rule_id) return t('admin.openaiFailover.recent.slot')
  const name = form.value?.rules.find((rule) => rule.id === event.rule_id)?.name
  return name || event.rule_id
}

function verdictClass(verdict: OpenAIEncryptedArgsCheckResult['verdict']): string {
  if (verdict === 'supported') return 'text-emerald-600 dark:text-emerald-400'
  if (verdict === 'unknown') return 'text-gray-500'
  return 'text-red-600 dark:text-red-400'
}

async function toggleMark(account: OpenAIFailoverAccount, event: Event) {
  const input = event.target as HTMLInputElement
  const marked = input.checked
  busyAccounts.add(account.id)
  try {
    await adminAPI.openaiFailover.updateEncryptedArgs(account.id, marked)
    account.no_encrypted_args = marked
    appStore.showSuccess(t('admin.openaiFailover.saved'))
  } catch (err) {
    input.checked = !marked
    appStore.showError(errorText(err))
  } finally {
    busyAccounts.delete(account.id)
  }
}

async function runCheck(account: OpenAIFailoverAccount) {
  busyAccounts.add(account.id)
  try {
    checks[account.id] = await adminAPI.openaiFailover.checkEncryptedArgs(account.id, checkModels[account.id] || '')
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    busyAccounts.delete(account.id)
  }
}

onMounted(load)
</script>
