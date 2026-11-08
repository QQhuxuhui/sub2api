<template>
  <div class="space-y-6">
    <!-- Recent routing -->
    <div class="card p-5">
      <div class="flex items-center justify-between">
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.events.title') }}</h2>
        <button type="button" class="btn btn-secondary text-sm" :disabled="eventsLoading" @click="loadEvents">{{ t('intentRouter.events.refresh') }}</button>
      </div>
      <p v-if="events.length === 0" class="mt-3 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.events.empty') }}</p>
      <div v-else class="mt-3 overflow-x-auto">
        <table class="min-w-full text-left text-sm">
          <thead class="text-xs text-gray-500 dark:text-gray-400">
            <tr>
              <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.time') }}</th>
              <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.group') }}</th>
              <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.kind') }}</th>
              <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.intent') }}</th>
              <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.account') }}</th>
              <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.latency') }}</th>
              <th class="py-2 font-medium">{{ t('intentRouter.events.detail') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="(event, i) in events" :key="i" class="text-gray-700 dark:text-gray-300">
              <td class="whitespace-nowrap py-2 pr-4 tabular-nums">{{ formatTime(event.time) }}</td>
              <td class="whitespace-nowrap py-2 pr-4">{{ groupName(event.group_id) }}</td>
              <td class="whitespace-nowrap py-2 pr-4">
                <span :class="['rounded-full px-2 py-0.5 text-xs', eventClass(event.kind)]">{{ t(`intentRouter.events.kinds.${event.kind}`) }}</span>
              </td>
              <td class="whitespace-nowrap py-2 pr-4">{{ event.kind === 'classified' && !event.intent ? t('intentRouter.events.noIntent') : event.intent || '—' }}</td>
              <td class="whitespace-nowrap py-2 pr-4">{{ event.account_id ? accountLabel(event.account_id) : '—' }}</td>
              <td class="whitespace-nowrap py-2 pr-4 tabular-nums">{{ event.latency_ms ? `${event.latency_ms} ms` : '—' }}</td>
              <td class="max-w-md break-words py-2 text-xs text-gray-500 dark:text-gray-400">{{ event.kind === 'keyword' ? t('intentRouter.events.keywordDetail', { keyword: event.detail }) : event.detail || '' }}</td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>


    <!-- Keyword hit review -->
    <div class="card p-5" data-test="intent-hits">
      <div class="flex flex-wrap items-center justify-between gap-3">
        <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.hits.title') }}</h2>
        <div class="flex flex-wrap items-center gap-2">
          <select v-model.number="hitGroupId" class="input w-auto text-sm" @change="reloadHits">
            <option :value="0">{{ t('intentRouter.hits.allGroups') }}</option>
            <option v-for="id in routedGroupIds" :key="id" :value="id">{{ groupName(id) }}</option>
          </select>
          <select v-model.number="hitHours" class="input w-auto text-sm" @change="reloadHits">
            <option v-for="h in HIT_HOURS" :key="h" :value="h">{{ t(`intentRouter.hits.hours.${h}`) }}</option>
          </select>
          <button type="button" class="btn btn-secondary text-sm" :disabled="hitsLoading" @click="reloadHits">{{ t('intentRouter.hits.refresh') }}</button>
        </div>
      </div>
      <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.hits.help') }}</p>
      <p v-if="hitStats.length === 0" class="mt-3 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.hits.empty') }}</p>
      <div v-else class="mt-3 overflow-x-auto">
        <table class="min-w-full text-left text-sm">
          <thead class="text-xs text-gray-500 dark:text-gray-400">
            <tr>
              <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.keyword') }}</th>
              <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.hitCount') }}</th>
              <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.users') }}</th>
              <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.apiKeys') }}</th>
              <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.avgChars') }}</th>
              <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.avgInputTokens') }}</th>
              <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.cost') }}</th>
              <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.lastHit') }}</th>
              <th class="py-2 font-medium"></th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-for="stat in hitStats" :key="stat.keyword" :class="['text-gray-700 dark:text-gray-300', stat.keyword === hitKeyword ? 'bg-primary-50 dark:bg-dark-700' : '']">
              <td class="max-w-xs break-words py-2 pr-4">
                <span class="font-mono text-xs">{{ showKeyword(stat.keyword) }}</span>
                <span v-if="stat.avg_input_tokens > SUSPECT_INPUT_TOKENS" class="ml-2 whitespace-nowrap rounded-full bg-amber-100 px-2 py-0.5 text-xs text-amber-700 dark:bg-amber-900/30 dark:text-amber-300" :title="t('intentRouter.hits.suspectHelp', { n: SUSPECT_INPUT_TOKENS })">{{ t('intentRouter.hits.suspect') }}</span>
              </td>
              <td class="py-2 pr-4 text-right tabular-nums">{{ stat.hits }}</td>
              <td class="py-2 pr-4 text-right tabular-nums">{{ stat.users }}</td>
              <td class="py-2 pr-4 text-right tabular-nums">{{ stat.api_keys }}</td>
              <td class="py-2 pr-4 text-right tabular-nums">{{ stat.avg_chars }}</td>
              <td class="py-2 pr-4 text-right tabular-nums">{{ stat.avg_input_tokens.toLocaleString() }}</td>
              <td class="py-2 pr-4 text-right tabular-nums">${{ stat.total_cost.toFixed(2) }}</td>
              <td class="whitespace-nowrap py-2 pr-4 tabular-nums">{{ formatTime(stat.last_hit) }}</td>
              <td class="whitespace-nowrap py-2"><button type="button" class="text-xs text-primary-600 hover:underline dark:text-primary-400" @click="filterHits(stat.keyword)">{{ t('intentRouter.hits.view') }}</button></td>
            </tr>
          </tbody>
        </table>
      </div>

      <div v-if="hitStats.length > 0" class="mt-5">
        <div class="flex flex-wrap items-center justify-between gap-2">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ hitKeyword ? t('intentRouter.hits.detailFiltered', { keyword: showKeyword(hitKeyword) }) : t('intentRouter.hits.detailTitle') }}
            <button v-if="hitKeyword" type="button" class="ml-2 text-xs font-normal text-primary-600 hover:underline dark:text-primary-400" @click="filterHits('')">{{ t('intentRouter.hits.clearFilter') }}</button>
          </h3>
          <div class="flex items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
            <span>{{ t('intentRouter.hits.total', { n: hitTotal }) }}</span>
            <button type="button" class="btn btn-secondary px-2 py-1 text-xs" :disabled="hitsLoading || hitPage <= 1" @click="loadHitPage(hitPage - 1)">{{ t('intentRouter.hits.prev') }}</button>
            <span class="tabular-nums">{{ hitPage }} / {{ hitPages }}</span>
            <button type="button" class="btn btn-secondary px-2 py-1 text-xs" :disabled="hitsLoading || hitPage >= hitPages" @click="loadHitPage(hitPage + 1)">{{ t('intentRouter.hits.next') }}</button>
          </div>
        </div>
        <div class="mt-2 overflow-x-auto">
          <table class="min-w-full text-left text-sm">
            <thead class="text-xs text-gray-500 dark:text-gray-400">
              <tr>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.time') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.group') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.user') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.keyword') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.excerpt') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.account') }}</th>
                <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.inputTokens') }}</th>
                <th class="py-2 text-right font-medium">{{ t('intentRouter.hits.cost') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="hit in hits" :key="hit.id" class="align-top text-gray-700 dark:text-gray-300">
                <td class="whitespace-nowrap py-2 pr-4 tabular-nums">{{ formatTime(hit.time) }}</td>
                <td class="whitespace-nowrap py-2 pr-4">{{ groupName(hit.group_id) }}</td>
                <td class="whitespace-nowrap py-2 pr-4 tabular-nums">#{{ hit.user_id }}</td>
                <td class="max-w-[10rem] break-words py-2 pr-4 font-mono text-xs">{{ showKeyword(hit.keyword) }}</td>
                <td class="min-w-[20rem] max-w-xl py-2 pr-4 text-xs">
                  <div class="whitespace-pre-wrap break-words"><template v-for="(part, i) in highlight(hit.excerpt, hit.keyword)" :key="i"><mark v-if="part.match" class="rounded bg-yellow-200 px-0.5 text-gray-900 dark:bg-yellow-600/60 dark:text-white">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></div>
                  <div class="mt-1 text-gray-400 dark:text-gray-500">
                    {{ hit.user_turn === 0 ? t('intentRouter.hits.turnLatest') : t('intentRouter.hits.turnPrevious') }} · {{ t('intentRouter.hits.chars', { n: hit.message_chars }) }}<template v-if="hit.model"> · {{ hit.model }}</template>
                  </div>
                </td>
                <td class="whitespace-nowrap py-2 pr-4">{{ hit.account_id ? accountLabel(hit.account_id) : t('intentRouter.hits.noUsage') }}</td>
                <td class="py-2 pr-4 text-right tabular-nums">{{ hit.input_tokens != null ? hit.input_tokens.toLocaleString() : '—' }}</td>
                <td class="py-2 text-right tabular-nums">{{ hit.total_cost != null ? `$${hit.total_cost.toFixed(4)}` : '—' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { intentRoutersAPI } from '@/api/admin/intentRouters'
import type { IntentRouteEvent, IntentRouteHit, IntentRouteKeywordStat } from '@/api/admin/intentRouters'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'

const props = defineProps<{
  routedGroupIds: number[]
  groupName: (id: number) => string
  accountLabel: (id: number) => string
}>()

const HIT_HOURS = [1, 24, 168, 720] as const
const HIT_PAGE_SIZE = 50
/** Detection prompts are a few hundred tokens; far above that is a long conversation. */
const SUSPECT_INPUT_TOKENS = 20000

const { t } = useI18n()
const appStore = useAppStore()
const groupName = (id: number) => props.groupName(id)
const accountLabel = (id: number) => props.accountLabel(id)
const routedGroupIds = computed(() => props.routedGroupIds)

const events = ref<IntentRouteEvent[]>([])
const eventsLoading = ref(false)
const hitsLoading = ref(false)
const hitGroupId = ref(0)
const hitHours = ref<number>(24)
const hitKeyword = ref('')
const hitStats = ref<IntentRouteKeywordStat[]>([])
const hits = ref<IntentRouteHit[]>([])
const hitTotal = ref(0)
const hitPage = ref(1)
const hitPages = computed(() => Math.max(1, Math.ceil(hitTotal.value / HIT_PAGE_SIZE)))

function errorText(err: unknown) {
  return extractI18nErrorMessage(err, t, 'intentRouter.errors', t('common.error'))
}
function formatTime(value: string) {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString()
}
function eventClass(kind: IntentRouteEvent['kind']) {
  if (kind === 'error') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (kind === 'skipped') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  if (kind === 'keyword') return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  if (kind === 'jev') return 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-300'
  if (kind === 'routed') return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}

async function loadEvents() {
  eventsLoading.value = true
  try {
    events.value = (await intentRoutersAPI.events(100)).data ?? []
  } catch {
    // The log is a convenience; the page works without it.
  } finally {
    eventsLoading.value = false
  }
}

/** Keywords may hold line breaks; show them as \\n so the row stays on one line. */
function showKeyword(keyword: string) { return keyword.replace(/\r/g, '\\r').replace(/\n/g, '\\n') }

/** Splits the excerpt around case-insensitive occurrences of the keyword. */
function highlight(text: string, keyword: string) {
  const needle = keyword.trim().toLowerCase()
  const parts: { text: string; match: boolean }[] = []
  if (!needle) return [{ text, match: false }]
  const lower = text.toLowerCase()
  // toLowerCase can change the length of a few characters; fall back to plain text then.
  if (lower.length !== text.length) return [{ text, match: false }]
  let from = 0
  for (let at = lower.indexOf(needle); at >= 0; at = lower.indexOf(needle, from)) {
    if (at > from) parts.push({ text: text.slice(from, at), match: false })
    parts.push({ text: text.slice(at, at + needle.length), match: true })
    from = at + needle.length
  }
  if (from < text.length) parts.push({ text: text.slice(from), match: false })
  return parts
}

function hitQuery() {
  return { group_id: hitGroupId.value || undefined, hours: hitHours.value, keyword: hitKeyword.value || undefined }
}

// A generation binds filters, statistics and details; stale requests never publish.
let hitRequestVersion = 0
let hitsDisposed = false
onBeforeUnmount(() => {
  hitsDisposed = true
  hitRequestVersion++
})

function isCurrentHitRequest(version: number) {
  return !hitsDisposed && version === hitRequestVersion
}

function clearHitPage() {
  hits.value = []
  hitTotal.value = 0
  hitPage.value = 1
}

async function loadHitPage(page: number) {
  if (hitsDisposed) return
  const version = ++hitRequestVersion
  const query = { ...hitQuery(), page, page_size: HIT_PAGE_SIZE }
  hitsLoading.value = true
  clearHitPage()
  try {
    const res = (await intentRoutersAPI.hits(query)).data
    if (!isCurrentHitRequest(version)) return
    hits.value = res?.items ?? []
    hitTotal.value = res?.total ?? 0
    hitPage.value = page
  } catch (err) {
    if (isCurrentHitRequest(version)) appStore.showError(errorText(err))
  } finally {
    if (isCurrentHitRequest(version)) hitsLoading.value = false
  }
}

async function reloadHits() {
  if (hitsDisposed) return
  const version = ++hitRequestVersion
  const { keyword: selectedKeyword, ...query } = hitQuery()
  hitsLoading.value = true
  hitStats.value = []
  clearHitPage()
  try {
    const stats = (await intentRoutersAPI.hitStats(query)).data ?? []
    if (!isCurrentHitRequest(version)) return
    const keyword = selectedKeyword && stats.some((s) => s.keyword === selectedKeyword) ? selectedKeyword : undefined
    const res = (await intentRoutersAPI.hits({ ...query, keyword, page: 1, page_size: HIT_PAGE_SIZE })).data
    if (!isCurrentHitRequest(version)) return
    // Publish both responses together using the same captured filter snapshot.
    hitStats.value = stats
    hitKeyword.value = keyword ?? ''
    hits.value = res?.items ?? []
    hitTotal.value = res?.total ?? 0
    hitPage.value = 1
  } catch (err) {
    if (isCurrentHitRequest(version)) appStore.showError(errorText(err))
  } finally {
    if (isCurrentHitRequest(version)) hitsLoading.value = false
  }
}

function filterHits(keyword: string) {
  hitKeyword.value = keyword
  void loadHitPage(1)
}

onMounted(() => {
  void loadEvents()
  void reloadHits()
})
</script>
