<template>
  <section class="space-y-4">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('iqShowcase.admin.hint') }}</p>
      <div class="flex gap-2">
        <button type="button" class="btn btn-secondary text-sm" @click="load">{{ t('common.refresh') }}</button>
        <button type="button" class="btn btn-primary text-sm" data-test="iq-showcase-add" @click="openDialog(null)">{{ t('iqShowcase.admin.add') }}</button>
      </div>
    </div>

    <div class="overflow-x-auto rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-900">
      <table class="min-w-[960px] w-full text-sm">
        <thead class="bg-gray-50 text-left text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">
          <tr>
            <th class="px-4 py-2.5 font-semibold">{{ t('iqShowcase.admin.columns.item') }}</th>
            <th class="px-4 py-2.5 font-semibold">{{ t('iqShowcase.admin.columns.model') }}</th>
            <th class="px-4 py-2.5 font-semibold">{{ t('iqShowcase.admin.columns.key') }}</th>
            <th class="px-4 py-2.5 font-semibold">{{ t('iqShowcase.admin.columns.cards') }}</th>
            <th class="px-4 py-2.5 font-semibold">{{ t('iqShowcase.admin.columns.last') }}</th>
            <th class="px-4 py-2.5 font-semibold">{{ t('iqShowcase.admin.columns.interval') }}</th>
            <th class="px-4 py-2.5 font-semibold">{{ t('iqShowcase.admin.columns.enabled') }}</th>
            <th class="px-4 py-2.5"><span class="sr-only">{{ t('common.actions') }}</span></th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
          <tr v-if="!loading && items.length === 0">
            <td colspan="8" class="px-4 py-8 text-center text-gray-500">{{ t('iqShowcase.admin.empty') }}</td>
          </tr>
          <tr v-for="it in items" :key="it.id" :data-test="`iq-showcase-row-${it.id}`" class="hover:bg-gray-50 dark:hover:bg-dark-800">
            <td class="px-4 py-3">
              <div class="font-semibold text-gray-800 dark:text-gray-100">{{ it.title }}</div>
              <div class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ it.group_name || `#${it.group_id}` }}</div>
            </td>
            <td class="px-4 py-3">
              <div class="font-mono text-xs text-gray-800 dark:text-gray-100">{{ it.model }}</div>
              <div class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">{{ t(`iqShowcase.admin.protocols.${it.protocol}`) }}</div>
            </td>
            <td class="px-4 py-3 text-xs">
              <span v-if="it.api_key_id">{{ it.api_key_name || `#${it.api_key_id}` }} · {{ it.api_key_masked }}</span>
              <span v-else class="inline-flex items-center gap-1.5 text-red-600 dark:text-red-400" data-test="iq-showcase-key-missing">
                <span class="h-1.5 w-1.5 rounded-full bg-red-500" aria-hidden="true"></span>{{ t('iqShowcase.admin.keyMissing') }}
              </span>
            </td>
            <td class="whitespace-nowrap px-4 py-3 text-xs tabular-nums">
              {{ it.success_cards }} / {{ it.max_cards }}
              <div v-if="it.recent_failures" class="mt-0.5 text-red-600 dark:text-red-400">{{ t('iqShowcase.admin.recentFailures', { n: it.recent_failures }) }}</div>
            </td>
            <td class="whitespace-nowrap px-4 py-3 text-xs">
              <span :class="['inline-flex items-center gap-1.5', statusOf(it).text]" :data-test="`iq-showcase-status-${it.id}`" :data-status="statusOf(it).kind">
                <span :class="['h-1.5 w-1.5 rounded-full', statusOf(it).dot]" aria-hidden="true"></span>{{ statusOf(it).label }}
              </span>
            </td>
            <td class="whitespace-nowrap px-4 py-3 text-xs tabular-nums">{{ t('iqProbe.minutes', { n: it.interval_minutes }) }}</td>
            <td class="px-4 py-3"><Toggle :model-value="it.enabled" @update:model-value="(v: boolean) => toggle(it, v)" /></td>
            <td class="whitespace-nowrap px-4 py-3">
              <div class="flex justify-end gap-1">
                <button type="button" :data-test="`iq-showcase-run-${it.id}`" class="rounded-lg px-2 py-1 text-primary-700 hover:bg-primary-50 disabled:text-gray-400 disabled:hover:bg-transparent dark:text-primary-300 dark:hover:bg-primary-900/30" :disabled="isRunning(it)" @click="runNow(it)">
                  {{ t('iqShowcase.admin.runNow') }}
                </button>
                <button type="button" class="rounded-lg px-2 py-1 text-primary-700 hover:bg-primary-50 dark:text-primary-300 dark:hover:bg-primary-900/30" @click="openResults(it)">{{ t('iqShowcase.admin.results') }}</button>
                <button type="button" class="rounded-lg px-2 py-1 text-primary-700 hover:bg-primary-50 dark:text-primary-300 dark:hover:bg-primary-900/30" @click="openDialog(it)">{{ t('common.edit') }}</button>
                <button type="button" class="rounded-lg px-2 py-1 text-red-600 hover:bg-red-50 dark:text-red-400 dark:hover:bg-red-900/30" @click="remove(it)">{{ t('common.delete') }}</button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <IQShowcaseItemDialog :show="dialog.show" :editing="dialog.editing" :groups="groups" @close="dialog.show = false" @saved="onSaved" />
    <IQShowcaseResultsDialog :show="resultsDialog.show" :item="resultsDialog.item" @close="resultsDialog.show = false" />
  </section>
</template>

<script setup lang="ts">
import { onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import IQShowcaseItemDialog from './IQShowcaseItemDialog.vue'
import IQShowcaseResultsDialog from './IQShowcaseResultsDialog.vue'
import { iqShowcaseAdminAPI } from '@/api/admin/iqShowcase'
import type { IQShowcaseItem } from '@/api/admin/iqShowcase'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { formatRelativeTime } from '@/utils/format'

defineProps<{ groups: Array<{ id: number; name: string; platform: string }> }>()

const { t } = useI18n()
const appStore = useAppStore()
const items = ref<IQShowcaseItem[]>([])
const loading = ref(false)
const dialog = reactive<{ show: boolean; editing: IQShowcaseItem | null }>({ show: false, editing: null })
const resultsDialog = reactive<{ show: boolean; item: IQShowcaseItem | null }>({ show: false, item: null })
let pollTimer: ReturnType<typeof setInterval> | null = null


function errorText(err: unknown) {
  return extractI18nErrorMessage(err, t, 'iqShowcase.errors', t('common.error'))
}

function isRunning(it: IQShowcaseItem) {
  return it.lease_until !== null && new Date(it.lease_until).getTime() > Date.now()
}

type ItemStatus = { kind: 'running' | 'ok' | 'failed' | 'none'; label: string; text: string; dot: string }

// Running wins over the last result; an item that never ran shows a dash.
function statusOf(it: IQShowcaseItem): ItemStatus {
  if (isRunning(it)) {
    return { kind: 'running', label: t('iqShowcase.admin.statusRunning'), text: 'text-primary-700 dark:text-primary-300', dot: 'bg-primary-500' }
  }
  if (it.last_result_at) {
    const time = formatRelativeTime(it.last_result_at)
    return it.last_success
      ? { kind: 'ok', label: t('iqShowcase.admin.statusOk', { time }), text: 'text-green-700 dark:text-green-400', dot: 'bg-green-500' }
      : { kind: 'failed', label: t('iqShowcase.admin.statusFailed', { time }), text: 'text-red-600 dark:text-red-400', dot: 'bg-red-500' }
  }
  return { kind: 'none', label: '—', text: 'text-gray-400', dot: 'bg-gray-300 dark:bg-dark-500' }
}

async function load() {
  loading.value = true
  try {
    items.value = (await iqShowcaseAdminAPI.listItems()).data ?? []
    if (items.value.some(isRunning)) startPolling()
    else stopPolling()
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    loading.value = false
  }
}

async function toggle(it: IQShowcaseItem, enabled: boolean) {
  try {
    Object.assign(it, (await iqShowcaseAdminAPI.setEnabled(it.id, enabled)).data)
  } catch (err) {
    appStore.showError(errorText(err))
  }
}

async function runNow(it: IQShowcaseItem) {
  try {
    await iqShowcaseAdminAPI.runItem(it.id)
    appStore.showSuccess(t('iqShowcase.admin.runStarted'))
    await load()
    startPolling()
  } catch (err) {
    appStore.showError(errorText(err))
  }
}

async function remove(it: IQShowcaseItem) {
  if (!confirm(t('iqShowcase.admin.confirmDelete', { title: it.title }))) return
  try {
    await iqShowcaseAdminAPI.removeItem(it.id)
    appStore.showSuccess(t('iqShowcase.admin.deleted'))
    await load()
  } catch (err) {
    appStore.showError(errorText(err))
  }
}

function openDialog(it: IQShowcaseItem | null) {
  dialog.editing = it
  dialog.show = true
}

function openResults(it: IQShowcaseItem) {
  resultsDialog.item = it
  resultsDialog.show = true
}

function onSaved() {
  dialog.show = false
  void load()
}

function startPolling() {
  if (pollTimer) return
  pollTimer = setInterval(() => void load(), 5000)
}

function stopPolling() {
  if (pollTimer) clearInterval(pollTimer)
  pollTimer = null
}

onMounted(load)
onBeforeUnmount(stopPolling)
</script>
