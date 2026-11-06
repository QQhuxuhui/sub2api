<template>
  <div class="card p-5" data-test="template-review">
    <div class="flex flex-wrap items-center justify-between gap-3">
      <div class="flex flex-wrap items-center gap-2">
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.templates.title') }}</h2>
        <span class="whitespace-nowrap rounded-full bg-primary-50 px-2 py-0.5 text-xs text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">{{ t('intentRouter.templates.scopeBadge') }}</span>
      </div>
      <div class="flex flex-wrap items-center gap-2">
        <select v-model="status" data-test="template-status" class="input w-auto text-sm" @change="reload">
          <option v-for="s in STATUSES" :key="s" :value="s">{{ t(`intentRouter.templates.status.${s}`) }}</option>
        </select>
        <select v-model="reason" data-test="template-reason" class="input w-auto text-sm" @change="reload">
          <option value="">{{ t('intentRouter.templates.reason.all') }}</option>
          <option v-for="r in REASONS" :key="r" :value="r">{{ t(`intentRouter.templates.reason.${r}`) }}</option>
        </select>
        <input v-model="search" data-test="template-search" type="search" class="input w-48 text-sm" :placeholder="t('intentRouter.templates.search')" @keyup.enter="reload" />
        <button type="button" data-test="template-refresh" class="btn btn-secondary text-sm" :disabled="loading" @click="reload">{{ t('intentRouter.templates.refresh') }}</button>
      </div>
    </div>
    <p class="mt-2 text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('intentRouter.templates.help') }}</p>

    <p v-if="loadError" data-test="template-error" class="mt-3 text-sm text-red-600 dark:text-red-400">{{ loadError }}</p>
    <p v-else-if="!loading && items.length === 0" data-test="template-empty" class="mt-3 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.templates.empty') }}</p>
    <div v-else class="mt-3 overflow-x-auto" :class="{ 'opacity-60': loading }">
      <table class="min-w-full text-left text-sm">
        <thead class="text-xs text-gray-500 dark:text-gray-400">
          <tr>
            <th class="py-2 pr-4 font-medium">{{ t('intentRouter.templates.template') }}</th>
            <th class="py-2 pr-4 font-medium">{{ t('intentRouter.templates.reasons') }}</th>
            <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.templates.seen') }}</th>
            <th class="whitespace-nowrap py-2 pr-4 font-medium">{{ t('intentRouter.templates.verdicts') }}</th>
            <th class="whitespace-nowrap py-2 pr-4 font-medium">{{ t('intentRouter.templates.scores') }}</th>
            <th class="py-2 pr-4 font-medium">{{ t('intentRouter.templates.who') }}</th>
            <th class="py-2 pr-4 font-medium">{{ t('intentRouter.templates.lastSeen') }}</th>
            <th class="py-2 font-medium">{{ t('intentRouter.templates.actions') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
          <tr v-for="item in items" :key="item.key" :data-test="`template-row-${item.key.slice(0, 12)}`" class="align-top text-gray-700 dark:text-gray-300">
            <td class="min-w-[18rem] max-w-xl py-2 pr-4 text-xs">
              <div class="whitespace-pre-wrap break-words font-mono">{{ item.template }}</div>
              <div class="mt-1 text-gray-400 dark:text-gray-500">
                {{ t('intentRouter.templates.chars', { n: item.text_chars }) }}
                <template v-if="item.sample">
                  ·
                  <button type="button" class="text-primary-600 hover:underline dark:text-primary-400" :data-test="`template-sample-toggle-${item.key.slice(0, 12)}`" @click="toggle(item.key)">
                    {{ expanded[item.key] ? t('intentRouter.templates.hideSample') : t('intentRouter.templates.showSample') }}
                  </button>
                </template>
              </div>
              <div v-if="expanded[item.key]" :data-test="`template-sample-${item.key.slice(0, 12)}`" class="mt-1 whitespace-pre-wrap break-words rounded bg-gray-50 p-2 text-gray-600 dark:bg-dark-800 dark:text-gray-300">{{ item.sample }}</div>
            </td>
            <td class="whitespace-nowrap py-2 pr-4">
              <span v-for="r in REASONS" v-show="reasonCount(item, r) > 0" :key="r" :title="t(`intentRouter.templates.reason.${r}`)" class="mr-1 inline-block rounded-full bg-amber-100 px-2 py-0.5 text-xs tabular-nums text-amber-800 dark:bg-amber-900/30 dark:text-amber-300">
                {{ t(`intentRouter.templates.reasonShort.${r}`) }} {{ reasonCount(item, r) }}
              </span>
            </td>
            <td class="py-2 pr-4 text-right tabular-nums">{{ item.seen_count }}</td>
            <td class="whitespace-nowrap py-2 pr-4 tabular-nums">{{ item.verdict_route }} / {{ item.verdict_health }} / {{ item.verdict_normal }}</td>
            <td class="whitespace-nowrap py-2 pr-4 tabular-nums">{{ range(item.probe_min, item.probe_max) }} / {{ range(item.health_min, item.health_max) }}</td>
            <td class="py-2 pr-4 text-xs tabular-nums">
              <div>{{ ids(item.user_ids, '#') }}</div>
              <div class="text-gray-400 dark:text-gray-500">{{ groups(item.group_ids) }}</div>
            </td>
            <td class="whitespace-nowrap py-2 pr-4 text-xs tabular-nums">{{ formatTime(item.last_seen_at) }}</td>
            <td class="whitespace-nowrap py-2">
              <div v-if="item.status === 'pending'" class="flex gap-1">
                <button v-for="d in DECISIONS" :key="d" type="button" :data-test="`template-decide-${d}-${item.key.slice(0, 12)}`" :class="['btn px-2 py-1 text-xs', d === 'probe' ? 'btn-primary' : 'btn-secondary']" :disabled="busy[item.key]" @click="decide(item, d)">
                  {{ t(`intentRouter.templates.decide${cap(d)}`) }}
                </button>
              </div>
              <div v-else class="flex items-center gap-2">
                <span :class="['rounded-full px-2 py-0.5 text-xs', decisionClass(item.status)]">{{ t('intentRouter.templates.decidedAs', { label: t(`intentRouter.templates.status.${item.status}`) }) }}</span>
                <button type="button" :data-test="`template-revoke-${item.key.slice(0, 12)}`" class="text-xs text-primary-600 hover:underline disabled:opacity-50 dark:text-primary-400" :disabled="busy[item.key]" @click="revoke(item)">{{ t('intentRouter.templates.revoke') }}</button>
              </div>
            </td>
          </tr>
        </tbody>
      </table>
    </div>

    <div v-if="total > PAGE_SIZE" class="mt-3 flex items-center justify-end gap-2 text-xs text-gray-500 dark:text-gray-400">
      <span>{{ t('intentRouter.templates.total', { n: total }) }}</span>
      <button type="button" class="btn btn-secondary px-2 py-1 text-xs" :disabled="loading || page <= 1" @click="load(page - 1)">{{ t('intentRouter.templates.prev') }}</button>
      <span class="tabular-nums">{{ page }} / {{ pages }}</span>
      <button type="button" class="btn btn-secondary px-2 py-1 text-xs" :disabled="loading || page >= pages" @click="load(page + 1)">{{ t('intentRouter.templates.next') }}</button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { intentRoutersAPI } from '@/api/admin/intentRouters'
import type { IntentRouteTemplate, IntentTemplateDecision, IntentTemplateStatus } from '@/api/admin/intentRouters'
import { useAppStore } from '@/stores/app'
import { extractApiErrorCode, extractI18nErrorMessage } from '@/utils/apiError'

const props = defineProps<{ groupNames?: Record<number, string> }>()

const STATUSES: Array<IntentTemplateStatus | 'all'> = ['pending', 'probe', 'health', 'normal', 'all']
const REASONS = ['a', 'b', 'c'] as const
const DECISIONS: IntentTemplateDecision[] = ['probe', 'health', 'normal']
const PAGE_SIZE = 20

const { t } = useI18n()
const appStore = useAppStore()

const status = ref<IntentTemplateStatus | 'all'>('pending')
const reason = ref<'' | 'a' | 'b' | 'c'>('')
const search = ref('')
const items = ref<IntentRouteTemplate[]>([])
const total = ref(0)
const page = ref(1)
const loading = ref(false)
const loadError = ref('')
const expanded = reactive<Record<string, boolean>>({})
const busy = reactive<Record<string, boolean>>({})
let seq = 0

const pages = computed(() => Math.max(1, Math.ceil(total.value / PAGE_SIZE)))

function errorText(err: unknown, fallback: string) {
  return extractI18nErrorMessage(err, t, 'intentRouter.errors', fallback)
}

async function load(p = page.value) {
  const mine = ++seq
  loading.value = true
  try {
    const { data } = await intentRoutersAPI.templates({ status: status.value, reason: reason.value, q: search.value.trim(), page: p, page_size: PAGE_SIZE })
    if (mine !== seq) return
    items.value = data.items ?? []
    total.value = data.total ?? 0
    page.value = p
    loadError.value = ''
  } catch (err) {
    if (mine !== seq) return
    loadError.value = errorText(err, t('intentRouter.templates.loadFailed'))
  } finally {
    if (mine === seq) loading.value = false
  }
}

function reload() {
  void load(1)
}

async function change(item: IntentRouteTemplate, run: () => Promise<unknown>, okText: string) {
  busy[item.key] = true
  try {
    await run()
    appStore.showSuccess(okText)
  } catch (err) {
    if (extractApiErrorCode(err) === 'INTENT_TEMPLATE_CONFLICT') appStore.showError(t('intentRouter.templates.conflict'))
    else appStore.showError(errorText(err, t('common.error')))
  } finally {
    busy[item.key] = false
  }
  await load()
}

function decide(item: IntentRouteTemplate, decision: IntentTemplateDecision) {
  return change(item, () => intentRoutersAPI.decideTemplate(item.key, decision, item.revision), t('intentRouter.templates.saved'))
}

function revoke(item: IntentRouteTemplate) {
  return change(item, () => intentRoutersAPI.revokeTemplate(item.key, item.revision), t('intentRouter.templates.revoked'))
}

function toggle(key: string) {
  expanded[key] = !expanded[key]
}

function reasonCount(item: IntentRouteTemplate, r: 'a' | 'b' | 'c') {
  return r === 'a' ? item.reason_a : r === 'b' ? item.reason_b : item.reason_c
}

function range(lo: number | null, hi: number | null) {
  if (lo == null || hi == null) return '—'
  return lo.toFixed(2) === hi.toFixed(2) ? lo.toFixed(2) : `${lo.toFixed(2)}~${hi.toFixed(2)}`
}

function ids(list: number[], prefix: string) {
  return list.length ? list.map((id) => `${prefix}${id}`).join(' ') : '—'
}

function groups(list: number[]) {
  return list.length ? list.map((id) => props.groupNames?.[id] ?? `#${id}`).join('、') : '—'
}

function cap(s: string) {
  return s.charAt(0).toUpperCase() + s.slice(1)
}

function decisionClass(s: IntentTemplateStatus) {
  if (s === 'probe') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (s === 'health') return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
}

function formatTime(value: string) {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString()
}

onMounted(() => {
  void load(1)
})
</script>
