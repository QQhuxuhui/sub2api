<template>
  <AppLayout>
    <div class="space-y-6">
      <div>
        <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('iqProbe.title') }}</h1>
        <p class="mt-1 max-w-4xl text-sm text-gray-500 dark:text-gray-400">{{ t('iqProbe.description') }}</p>
        <p class="mt-2 max-w-4xl rounded-lg bg-gray-50 p-3 text-sm leading-6 text-gray-600 dark:bg-dark-800 dark:text-gray-300">
          {{ t('iqProbe.howItWorks') }}
        </p>
      </div>

      <div class="flex gap-1 border-b border-gray-200 dark:border-dark-700">
        <button
          v-for="tab in tabs"
          :key="tab"
          type="button"
          :data-test="`iq-tab-${tab}`"
          :class="[
            '-mb-px border-b-2 px-4 py-2 text-sm font-medium',
            activeTab === tab
              ? 'border-primary-600 text-primary-700 dark:text-primary-300'
              : 'border-transparent text-gray-500 hover:text-gray-700 dark:text-gray-400 dark:hover:text-gray-200',
          ]"
          @click="activeTab = tab"
        >
          {{ t(`iqProbe.tabs.${tab}`) }}
        </button>
      </div>

      <div v-if="loading" class="flex items-center justify-center py-16">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      </div>

      <!-- Monitors -->
      <section v-else-if="activeTab === 'monitors'" class="space-y-4">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('iqProbe.monitors.summary', { n: monitors.length, enabled: enabledCount }) }}</p>
          <div class="flex gap-2">
            <button type="button" class="btn btn-secondary text-sm" @click="loadMonitors">{{ t('common.refresh') }}</button>
            <button
              type="button"
              data-test="iq-monitor-add"
              class="btn btn-primary text-sm"
              :disabled="sets.length === 0"
              :title="sets.length === 0 ? t('iqProbe.monitors.needSet') : ''"
              @click="openMonitorDialog(null)"
            >
              {{ t('iqProbe.monitors.add') }}
            </button>
          </div>
        </div>

        <div class="card overflow-x-auto">
          <table class="min-w-full text-sm">
            <thead class="bg-gray-50 text-left text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">
              <tr>
                <th class="px-3 py-2">{{ t('iqProbe.columns.account') }}</th>
                <th class="px-3 py-2">{{ t('iqProbe.columns.model') }}</th>
                <th class="px-3 py-2">{{ t('iqProbe.columns.set') }}</th>
                <th class="px-3 py-2">{{ t('iqProbe.columns.score') }}</th>
                <th class="px-3 py-2">{{ t('iqProbe.columns.trend') }}</th>
                <th class="px-3 py-2">{{ t('iqProbe.columns.lastRun') }}</th>
                <th class="px-3 py-2">{{ t('iqProbe.columns.interval') }}</th>
                <th class="px-3 py-2">{{ t('iqProbe.columns.alert') }}</th>
                <th class="px-3 py-2">{{ t('iqProbe.columns.enabled') }}</th>
                <th class="px-3 py-2 text-right">{{ t('common.actions') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-if="monitors.length === 0">
                <td colspan="10" class="px-3 py-8 text-center text-gray-500">{{ t('iqProbe.monitors.empty') }}</td>
              </tr>
              <tr v-for="m in monitors" :key="m.id" :data-test="`iq-monitor-row-${m.id}`" class="hover:bg-gray-50 dark:hover:bg-dark-800">
                <td class="px-3 py-2">
                  <button type="button" class="text-left" @click="openDetail(m)">
                    <span class="block font-medium text-primary-700 hover:underline dark:text-primary-300">{{ m.account_name || `#${m.account_id}` }}</span>
                    <span class="block text-xs text-gray-500 dark:text-gray-400">
                      #{{ m.account_id }} · {{ m.account_platform }}<template v-if="m.group_name"> · {{ m.group_name }}</template>
                      <span v-if="m.account_status && m.account_status !== 'active'" class="ml-1 text-red-500">{{ m.account_status }}</span>
                    </span>
                  </button>
                </td>
                <td class="px-3 py-2 font-mono text-xs">
                  {{ m.model }}
                  <span v-if="m.reasoning_effort" class="ml-1 rounded bg-gray-100 px-1 text-gray-500 dark:bg-dark-700">{{ m.reasoning_effort }}</span>
                </td>
                <td class="px-3 py-2">{{ m.set_name }}</td>
                <td class="px-3 py-2">
                  <IQProbeScoreBadge :status="running.has(m.id) ? 'running' : m.last_status" :score="m.last_score" :threshold="m.alert_enabled ? m.alert_threshold : undefined" />
                </td>
                <td class="px-3 py-2">
                  <IQProbeSparkline v-if="m.trend && m.trend.length" :points="m.trend" :threshold="m.alert_enabled ? m.alert_threshold : undefined" />
                  <span v-else class="text-xs text-gray-400">—</span>
                </td>
                <td class="whitespace-nowrap px-3 py-2 text-xs text-gray-600 dark:text-gray-300">
                  {{ m.last_run_at ? formatRelativeTime(m.last_run_at) : '—' }}
                </td>
                <td class="whitespace-nowrap px-3 py-2 text-xs">{{ t('iqProbe.minutes', { n: m.interval_minutes }) }}</td>
                <td class="whitespace-nowrap px-3 py-2 text-xs">
                  <span v-if="!m.alert_enabled" class="text-gray-400">{{ t('iqProbe.alertState.off') }}</span>
                  <span v-else :class="alertClass(m.alert_state)">{{ t(`iqProbe.alertState.${m.alert_state}`) }}</span>
                </td>
                <td class="px-3 py-2"><Toggle :model-value="m.enabled" @update:model-value="(v: boolean) => toggleMonitor(m, v)" /></td>
                <td class="whitespace-nowrap px-3 py-2 text-right">
                  <button type="button" :data-test="`iq-monitor-run-${m.id}`" class="text-primary-600 hover:underline disabled:opacity-50 dark:text-primary-400" :disabled="running.has(m.id)" @click="runNow(m)">
                    {{ t('iqProbe.monitors.runNow') }}
                  </button>
                  <button type="button" class="ml-3 text-primary-600 hover:underline dark:text-primary-400" @click="openMonitorDialog(m)">{{ t('common.edit') }}</button>
                  <button type="button" class="ml-3 text-red-600 hover:underline dark:text-red-400" @click="removeMonitor(m)">{{ t('common.delete') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </section>

      <IQProbeSetsTab v-else-if="activeTab === 'sets'" :sets="sets" @changed="loadSets" />
      <IQProbeRunsTab v-else :monitors="monitors" />
    </div>

    <IQProbeMonitorDialog :show="monitorDialog.show" :editing="monitorDialog.editing" :groups="groups" :sets="sets" @close="monitorDialog.show = false" @saved="onMonitorSaved" />
    <IQProbeDetailDialog :show="detail.show" :monitor="detail.monitor" @close="detail.show = false" />
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import Toggle from '@/components/common/Toggle.vue'
import IQProbeSparkline from '@/components/admin/iqProbe/IQProbeSparkline.vue'
import IQProbeScoreBadge from '@/components/admin/iqProbe/IQProbeScoreBadge.vue'
import IQProbeMonitorDialog from '@/components/admin/iqProbe/IQProbeMonitorDialog.vue'
import IQProbeDetailDialog from '@/components/admin/iqProbe/IQProbeDetailDialog.vue'
import IQProbeSetsTab from '@/components/admin/iqProbe/IQProbeSetsTab.vue'
import IQProbeRunsTab from '@/components/admin/iqProbe/IQProbeRunsTab.vue'
import { adminAPI } from '@/api/admin'
import { iqProbeAPI } from '@/api/admin/iqProbe'
import type { IQProbeAlertState, IQProbeMonitor, IQProbeMonitorInput, IQProbeQuestionSet } from '@/api/admin/iqProbe'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { formatRelativeTime } from '@/utils/format'

type Tab = 'monitors' | 'sets' | 'runs'
const tabs: Tab[] = ['monitors', 'sets', 'runs']

const { t } = useI18n()
const appStore = useAppStore()

const activeTab = ref<Tab>('monitors')
const loading = ref(true)
const monitors = ref<IQProbeMonitor[]>([])
const sets = ref<IQProbeQuestionSet[]>([])
const groups = ref<Array<{ id: number; name: string; platform: string }>>([])
/** Monitors with a manual round in flight; polled until their last_run_at moves. */
const running = ref(new Map<number, string | null>())
let pollTimer: ReturnType<typeof setInterval> | null = null

const monitorDialog = reactive<{ show: boolean; editing: IQProbeMonitor | null }>({ show: false, editing: null })
const detail = reactive<{ show: boolean; monitor: IQProbeMonitor | null }>({ show: false, monitor: null })

const enabledCount = computed(() => monitors.value.filter((m) => m.enabled).length)

function errorText(err: unknown) {
  return extractI18nErrorMessage(err, t, 'iqProbe.errors', t('common.error'))
}

function alertClass(state: IQProbeAlertState) {
  if (state === 'low') return 'font-medium text-red-600 dark:text-red-400'
  if (state === 'invalid') return 'font-medium text-amber-600 dark:text-amber-400'
  return 'text-green-600 dark:text-green-400'
}

async function loadMonitors() {
  try {
    monitors.value = (await iqProbeAPI.listMonitors()).data ?? []
    for (const [id, lastRun] of running.value) {
      const m = monitors.value.find((x) => x.id === id)
      if (!m || m.last_run_at !== lastRun) running.value.delete(id)
    }
    if (running.value.size === 0) stopPolling()
  } catch (err) {
    appStore.showError(errorText(err))
  }
}

async function loadSets() {
  try {
    sets.value = (await iqProbeAPI.listSets()).data ?? []
  } catch (err) {
    appStore.showError(errorText(err))
  }
}

function openMonitorDialog(m: IQProbeMonitor | null) {
  monitorDialog.editing = m
  monitorDialog.show = true
}

function onMonitorSaved() {
  monitorDialog.show = false
  void loadMonitors()
  void loadSets()
}

function openDetail(m: IQProbeMonitor) {
  detail.monitor = m
  detail.show = true
}

function monitorInput(m: IQProbeMonitor): IQProbeMonitorInput {
  return {
    group_id: m.group_id,
    model: m.model,
    set_id: m.set_id,
    interval_minutes: m.interval_minutes,
    reasoning_effort: m.reasoning_effort,
    max_tokens: m.max_tokens,
    enabled: m.enabled,
    alert_enabled: m.alert_enabled,
    alert_threshold: m.alert_threshold,
    alert_low_rounds: m.alert_low_rounds,
    alert_invalid_rounds: m.alert_invalid_rounds,
  }
}

async function toggleMonitor(m: IQProbeMonitor, enabled: boolean) {
  try {
    const updated = (await iqProbeAPI.updateMonitor(m.id, { ...monitorInput(m), enabled })).data
    Object.assign(m, updated)
  } catch (err) {
    appStore.showError(errorText(err))
  }
}

async function runNow(m: IQProbeMonitor) {
  try {
    await iqProbeAPI.runMonitor(m.id)
    running.value.set(m.id, m.last_run_at)
    appStore.showSuccess(t('iqProbe.monitors.runStarted'))
    startPolling()
  } catch (err) {
    appStore.showError(errorText(err))
  }
}

async function removeMonitor(m: IQProbeMonitor) {
  if (!confirm(t('iqProbe.monitors.confirmDelete', { name: `${m.account_name} / ${m.model}` }))) return
  try {
    await iqProbeAPI.removeMonitor(m.id)
    appStore.showSuccess(t('iqProbe.actions.deleted'))
    await Promise.all([loadMonitors(), loadSets()])
  } catch (err) {
    appStore.showError(errorText(err))
  }
}

function startPolling() {
  if (pollTimer) return
  pollTimer = setInterval(() => void loadMonitors(), 5000)
}

function stopPolling() {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = null
}

onMounted(async () => {
  try {
    const [groupList] = await Promise.all([adminAPI.groups.getAllIncludingInactive(), loadMonitors(), loadSets()])
    groups.value = groupList.map((g) => ({ id: g.id, name: g.name, platform: g.platform }))
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    loading.value = false
  }
})

onBeforeUnmount(stopPolling)
</script>
