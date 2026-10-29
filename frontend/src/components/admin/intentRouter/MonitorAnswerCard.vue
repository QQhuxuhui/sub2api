<template>
  <div class="card space-y-4 p-5" data-test="monitor-answer">
    <div class="flex items-start justify-between gap-3">
      <label class="flex items-start gap-3">
        <input v-model="form.enabled" data-test="monitor-answer-enabled" type="checkbox" class="mt-1 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
        <span>
          <span class="block text-sm font-medium text-gray-900 dark:text-white">{{ t('intentRouter.monitorAnswer.title') }}</span>
          <span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.monitorAnswer.intro') }}</span>
        </span>
      </label>
      <button type="button" data-test="monitor-answer-save" class="btn btn-primary btn-sm shrink-0" :disabled="loading || saving || !dirty" @click="save">
        {{ saving ? t('common.saving') : t('common.save') }}
      </button>
    </div>

    <div>
      <label class="input-label">{{ t('intentRouter.jev.scope') }}</label>
      <div class="flex flex-wrap gap-4 text-sm text-gray-700 dark:text-gray-300">
        <label class="flex items-center gap-2"><input v-model="form.scope" data-test="monitor-answer-scope-all" type="radio" value="all" />{{ t('intentRouter.jev.scopeAll') }}</label>
        <label class="flex items-center gap-2"><input v-model="form.scope" data-test="monitor-answer-scope-listed" type="radio" value="listed" />{{ t('intentRouter.jev.scopeListed') }}</label>
      </div>
      <ScopePicker v-if="form.scope === 'listed'" v-model:users="form.users" v-model:keys="form.keys" test-prefix="monitor-answer" class="mt-3" />
    </div>

    <div v-if="status" data-test="monitor-answer-status" class="space-y-3 rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">
      <div class="flex items-center justify-between">
        <span class="text-gray-700 dark:text-gray-300">{{ t('intentRouter.monitorAnswer.today', { n: status.days[0]?.answers ?? 0 }) }}</span>
        <button type="button" class="text-xs text-primary-600 hover:underline dark:text-primary-400" @click="loadStatus">{{ t('intentRouter.jev.status.refresh') }}</button>
      </div>
      <div class="overflow-x-auto">
        <table class="min-w-full text-xs">
          <thead class="text-left text-gray-500 dark:text-gray-400">
            <tr>
              <th v-for="day in status.days" :key="day.date" class="py-1 pr-4">{{ formatDay(day.date) }}</th>
            </tr>
          </thead>
          <tbody class="text-gray-700 dark:text-gray-300">
            <tr>
              <td v-for="day in status.days" :key="day.date" :data-test="`monitor-answer-day-${day.date}`" class="py-1 pr-4">{{ day.answers }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <div v-if="status.recent.length">
        <p class="mb-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.monitorAnswer.recent', { n: status.recent.length }) }}</p>
        <div class="max-h-64 overflow-auto">
          <table class="min-w-full text-xs">
            <thead class="sticky top-0 bg-gray-50 text-left text-gray-500 dark:bg-dark-800 dark:text-gray-400">
              <tr>
                <th class="py-1 pr-4">{{ t('intentRouter.monitorAnswer.time') }}</th>
                <th class="py-1 pr-4">{{ t('intentRouter.monitorAnswer.user') }}</th>
                <th class="py-1 pr-4">{{ t('intentRouter.monitorAnswer.key') }}</th>
                <th class="py-1 pr-4">{{ t('intentRouter.monitorAnswer.model') }}</th>
                <th class="py-1 pr-4">{{ t('intentRouter.monitorAnswer.endpoint') }}</th>
                <th class="py-1">{{ t('intentRouter.monitorAnswer.qa') }}</th>
              </tr>
            </thead>
            <tbody class="text-gray-700 dark:text-gray-300">
              <tr v-for="(r, i) in status.recent" :key="i" data-test="monitor-answer-record">
                <td class="py-1 pr-4 whitespace-nowrap">{{ formatTime(r.time) }}</td>
                <td class="py-1 pr-4">#{{ r.user_id }}</td>
                <td class="py-1 pr-4">#{{ r.api_key_id }}</td>
                <td class="py-1 pr-4">{{ r.model }}</td>
                <td class="py-1 pr-4">{{ r.endpoint }}</td>
                <td class="py-1 whitespace-nowrap">{{ r.question }} = {{ r.answer }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { intentRoutersAPI } from '@/api/admin/intentRouters'
import type { MonitorProbeAnswerConfig, MonitorProbeAnswerStatus, MonitorProbeAnswerView } from '@/api/admin/intentRouters'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'
import ScopePicker from './ScopePicker.vue'
import type { ScopeKey, ScopeUser } from './ScopePicker.vue'

const props = defineProps<{ groupId: number }>()
const { t } = useI18n()
const appStore = useAppStore()

const form = reactive<{ enabled: boolean; scope: 'all' | 'listed'; users: ScopeUser[]; keys: ScopeKey[] }>({ enabled: false, scope: 'all', users: [], keys: [] })
const baseline = ref('')
// Until the current group's setting has arrived, the form holds nothing
// worth saving.
const loading = ref(true)
const saving = ref(false)
const status = ref<MonitorProbeAnswerStatus | null>(null)

function toConfig(): MonitorProbeAnswerConfig {
  return { enabled: form.enabled, scope: form.scope, user_ids: form.users.map((u) => u.id), api_keys: form.keys.map((k) => ({ id: k.id })) }
}
const dirty = computed(() => JSON.stringify(toConfig()) !== baseline.value)

function fill(view: MonitorProbeAnswerView) {
  const userLabels = new Map(view.scope_labels.users.map((u) => [u.id, u.email]))
  const keyLabels = new Map(view.scope_labels.api_keys.map((k) => [k.id, k.name]))
  form.enabled = view.config.enabled
  form.scope = view.config.scope
  form.users = view.config.user_ids.map((id) => ({ id, email: userLabels.get(id) ?? '' }))
  form.keys = view.config.api_keys.map((k) => ({ id: k.id, user_id: k.user_id ?? 0, name: keyLabels.get(k.id) ?? '' }))
  baseline.value = JSON.stringify(toConfig())
}

function reset() {
  form.enabled = false
  form.scope = 'all'
  form.users = []
  form.keys = []
  baseline.value = JSON.stringify(toConfig())
}

// Every load and save belongs to the group it was started for; switching
// groups starts a new generation and anything older is dropped.
let generation = 0
async function load() {
  const gen = ++generation
  const groupId = props.groupId
  loading.value = true
  reset()
  try {
    const view = (await intentRoutersAPI.monitorAnswer(groupId)).data
    if (gen !== generation) return
    fill(view)
    loading.value = false
  } catch (err) {
    if (gen === generation) appStore.showError(extractI18nErrorMessage(err, t, 'intentRouter.errors', t('common.error')))
    return
  }
  await loadStatus()
}
let statusSeq = 0
async function loadStatus() {
  const seq = ++statusSeq
  const groupId = props.groupId
  try {
    const result = (await intentRoutersAPI.monitorAnswerStatus(groupId)).data
    if (seq === statusSeq && groupId === props.groupId) status.value = result
  } catch {
    if (seq === statusSeq && groupId === props.groupId) status.value = null
  }
}

async function save() {
  if (saving.value || loading.value) return
  const gen = generation
  const groupId = props.groupId
  saving.value = true
  try {
    const view = (await intentRoutersAPI.saveMonitorAnswer(groupId, toConfig())).data
    appStore.showSuccess(t('intentRouter.monitorAnswer.saved'))
    // The admin has moved on to another group: its form is not this one's.
    if (gen === generation && view.group_id === props.groupId) fill(view)
  } catch (err) {
    appStore.showError(extractI18nErrorMessage(err, t, 'intentRouter.errors', t('common.error')))
  } finally {
    saving.value = false
  }
}

function formatDay(day: string) {
  return day.length === 8 ? `${day.slice(4, 6)}-${day.slice(6, 8)}` : day
}
function formatTime(time: string) {
  const d = new Date(time)
  return Number.isNaN(d.getTime()) ? time : d.toLocaleString()
}

watch(() => props.groupId, () => {
  status.value = null
  void load()
}, { immediate: true })
</script>
