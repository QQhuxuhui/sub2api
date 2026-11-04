<template>
  <section class="space-y-4">
    <p class="max-w-4xl rounded-lg bg-gray-50 p-3 text-sm leading-6 text-gray-600 dark:bg-dark-800 dark:text-gray-300">
      {{ t('iqShowcase.admin.howItWorks') }}
    </p>
    <div class="flex flex-wrap items-center justify-between gap-3">
      <p class="text-sm text-gray-500 dark:text-gray-400">{{ t('iqShowcase.admin.summary', { n: items.length, enabled: enabledCount }) }}</p>
      <div class="flex gap-2">
        <button type="button" class="btn btn-secondary text-sm" @click="load">{{ t('common.refresh') }}</button>
        <button type="button" class="btn btn-primary text-sm" data-test="iq-showcase-add" @click="openDialog(null)">{{ t('iqShowcase.admin.add') }}</button>
      </div>
    </div>

    <div class="overflow-x-auto rounded-xl border border-gray-200 dark:border-dark-700">
      <table class="min-w-full text-sm">
        <thead class="bg-gray-50 text-left text-xs uppercase text-gray-500 dark:bg-dark-800 dark:text-gray-400">
          <tr>
            <th class="px-3 py-2">{{ t('iqShowcase.admin.columns.title') }}</th>
            <th class="px-3 py-2">{{ t('iqShowcase.admin.columns.group') }}</th>
            <th class="px-3 py-2">{{ t('iqShowcase.admin.columns.model') }}</th>
            <th class="px-3 py-2">{{ t('iqShowcase.admin.columns.key') }}</th>
            <th class="px-3 py-2">{{ t('iqShowcase.admin.columns.cards') }}</th>
            <th class="px-3 py-2">{{ t('iqShowcase.admin.columns.last') }}</th>
            <th class="px-3 py-2">{{ t('iqShowcase.admin.columns.interval') }}</th>
            <th class="px-3 py-2">{{ t('iqShowcase.admin.columns.enabled') }}</th>
            <th class="px-3 py-2 text-right">{{ t('common.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
          <tr v-if="!loading && items.length === 0">
            <td colspan="9" class="px-3 py-8 text-center text-gray-500">{{ t('iqShowcase.admin.empty') }}</td>
          </tr>
          <tr v-for="it in items" :key="it.id" :data-test="`iq-showcase-row-${it.id}`" class="hover:bg-gray-50 dark:hover:bg-dark-800">
            <td class="px-3 py-2 font-medium text-gray-800 dark:text-gray-100">{{ it.title }}</td>
            <td class="px-3 py-2 text-xs">{{ it.group_name || `#${it.group_id}` }}</td>
            <td class="px-3 py-2 font-mono text-xs">
              {{ it.model }}
              <span class="ml-1 text-gray-400">{{ t(`iqShowcase.admin.protocols.${it.protocol}`) }}</span>
            </td>
            <td class="px-3 py-2 text-xs">
              <span v-if="it.api_key_id">{{ it.api_key_name || `#${it.api_key_id}` }} · {{ it.api_key_masked }}</span>
              <span v-else class="text-red-500">{{ t('iqShowcase.admin.keyMissing') }}</span>
            </td>
            <td class="px-3 py-2 text-xs">
              {{ it.success_cards }} / {{ it.max_cards }}
              <span v-if="it.recent_failures" class="ml-1 text-red-500">{{ t('iqShowcase.admin.recentFailures', { n: it.recent_failures }) }}</span>
            </td>
            <td class="whitespace-nowrap px-3 py-2 text-xs">
              <template v-if="it.last_result_at">
                <span :class="it.last_success ? 'text-green-600' : 'text-red-600'">{{ it.last_success ? t('iqShowcase.admin.success') : t('iqShowcase.admin.failed') }}</span>
                <span class="ml-1 text-gray-500">{{ formatRelativeTime(it.last_result_at) }}</span>
              </template>
              <span v-else class="text-gray-400">—</span>
              <span v-if="isRunning(it)" class="ml-1 text-primary-600">{{ t('iqShowcase.admin.running') }}</span>
            </td>
            <td class="whitespace-nowrap px-3 py-2 text-xs">{{ t('iqProbe.minutes', { n: it.interval_minutes }) }}</td>
            <td class="px-3 py-2"><Toggle :model-value="it.enabled" @update:model-value="(v: boolean) => toggle(it, v)" /></td>
            <td class="whitespace-nowrap px-3 py-2 text-right">
              <button type="button" :data-test="`iq-showcase-run-${it.id}`" class="text-primary-600 hover:underline disabled:opacity-50 dark:text-primary-400" :disabled="isRunning(it)" @click="runNow(it)">
                {{ t('iqShowcase.admin.runNow') }}
              </button>
              <button type="button" class="ml-3 text-primary-600 hover:underline dark:text-primary-400" @click="openResults(it)">{{ t('iqShowcase.admin.results') }}</button>
              <button type="button" class="ml-3 text-primary-600 hover:underline dark:text-primary-400" @click="openDialog(it)">{{ t('common.edit') }}</button>
              <button type="button" class="ml-3 text-red-600 hover:underline dark:text-red-400" @click="remove(it)">{{ t('common.delete') }}</button>
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
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Toggle from '@/components/common/Toggle.vue'
import IQShowcaseItemDialog from './IQShowcaseItemDialog.vue'
import IQShowcaseResultsDialog from './IQShowcaseResultsDialog.vue'
import { iqShowcaseAdminAPI } from '@/api/admin/iqShowcase'
import type { IQShowcaseItem, IQShowcaseItemInput } from '@/api/admin/iqShowcase'
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

const enabledCount = computed(() => items.value.filter((i) => i.enabled).length)

function errorText(err: unknown) {
  return extractI18nErrorMessage(err, t, 'iqShowcase.errors', t('common.error'))
}

function isRunning(it: IQShowcaseItem) {
  return it.lease_until !== null && new Date(it.lease_until).getTime() > Date.now()
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

function input(it: IQShowcaseItem): IQShowcaseItemInput {
  return {
    title: it.title,
    group_id: it.group_id,
    api_key_id: it.api_key_id,
    protocol: it.protocol,
    model: it.model,
    prompt: it.prompt,
    reasoning_effort: it.reasoning_effort,
    max_output_tokens: it.max_output_tokens,
    interval_minutes: it.interval_minutes,
    max_cards: it.max_cards,
    enabled: it.enabled,
    sort_order: it.sort_order,
  }
}

async function toggle(it: IQShowcaseItem, enabled: boolean) {
  try {
    Object.assign(it, (await iqShowcaseAdminAPI.updateItem(it.id, { ...input(it), enabled })).data)
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
