<template>
  <AppLayout>
    <div class="mx-auto w-full max-w-7xl px-4 py-6 sm:px-6 lg:px-8">
      <!-- 概览条：金额永远是全量，不随搜索/分页变化 -->
      <section
        class="rounded-xl border border-gray-200 bg-white p-5 dark:border-dark-700 dark:bg-dark-800"
      >
        <div class="min-w-[13rem]">
          <div class="mb-1.5 text-xs font-medium text-gray-500 dark:text-dark-400">
            {{ t('team.period') }}
          </div>
          <DateRangePicker v-model:start-date="startDate" v-model:end-date="endDate" />
        </div>

        <!-- 6 格概览。窄屏两列、平板三列、宽屏一行铺开；金额永远是全量，不随搜索/分页变化 -->
        <div
          class="mt-5 grid grid-cols-2 gap-x-6 gap-y-5 sm:grid-cols-3 xl:grid-cols-6"
          data-testid="overview-grid"
        >
          <div>
            <div class="mb-1 text-xs font-medium text-gray-500 dark:text-dark-400">
              {{ t('team.totalCost') }}
            </div>
            <div class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">
              {{ formatTeamMoney(summary?.total_cost, { zeroAsDash: false }) }}
            </div>
            <!-- 环比跟在总额下面：它是同一个数的另一种读法，拆成独立一格会让
                 六格里出现两个「金额」量纲的大字，反而看不出主次 -->
            <div class="mt-0.5 text-xs tabular-nums" :class="deltaClass">
              {{ t('team.delta') }} {{ formatTeamDelta(summary?.delta_pct) }}
            </div>
            <div class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
              {{ t('team.rows', { n: summary?.row_count ?? 0 }) }} ·
              {{ t('team.keys', { n: summary?.key_count ?? 0 }) }}
            </div>
            <div
              v-if="summary && !summary.compare.comparable"
              class="mt-0.5 text-xs text-gray-500 dark:text-dark-400"
            >
              {{ t('team.compareUnavailable') }}
            </div>
          </div>

          <div>
            <div class="mb-1 text-xs font-medium text-gray-500 dark:text-dark-400">
              {{ t('team.statRequests') }}
            </div>
            <div class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">
              {{ formatTeamCount(summary?.requests) }}
            </div>
            <div class="mt-0.5 text-xs tabular-nums text-gray-500 dark:text-dark-400">
              {{ t('team.statPerDay', { n: formatTeamRate(requestsPerDayValue) }) }}
            </div>
          </div>

          <div>
            <div class="mb-1 text-xs font-medium text-gray-500 dark:text-dark-400">
              {{ t('team.statAvgCost') }}
            </div>
            <div class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">
              {{ formatTeamAvgCost(globalAvg) }}
            </div>
            <div class="mt-0.5 text-xs text-gray-500 dark:text-dark-400">
              {{ t('team.statAvgCostHint') }}
            </div>
          </div>

          <!-- 缓存节省是近似值，tooltip 里必须说清楚，否则会被当成精确账单差额 -->
          <div :title="t('team.statCacheApprox')">
            <div class="mb-1 text-xs font-medium text-gray-500 dark:text-dark-400">
              {{ t('team.statCacheSaved') }}
              <span class="text-gray-400 dark:text-dark-500">≈</span>
            </div>
            <div class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">
              {{ formatTeamSavedCost(summary?.cache_saved_cost) }}
            </div>
            <div class="mt-0.5 text-xs tabular-nums text-gray-500 dark:text-dark-400">
              {{ t('team.statCacheHit', { p: formatTeamPercent(summary?.cache_hit_rate ?? null) }) }}
            </div>
          </div>

          <div>
            <div class="mb-1 text-xs font-medium text-gray-500 dark:text-dark-400">
              {{ t('team.statActive') }}
            </div>
            <div class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">
              {{ formatTeamCount(summary?.active_row_count) }}
            </div>
            <div class="mt-0.5 text-xs tabular-nums text-gray-500 dark:text-dark-400">
              {{ t('team.activeFmt', {
                done: formatTeamCount(summary?.active_row_count),
                total: formatTeamCount(summary?.row_count),
              }) }}
            </div>
          </div>

          <div>
            <div class="mb-1 text-xs font-medium text-gray-500 dark:text-dark-400">
              {{ t('team.owned') }}
            </div>
            <div class="text-2xl font-semibold tabular-nums text-gray-900 dark:text-white">
              {{ formatTeamCount(summary?.owned_key_count) }}
            </div>
            <div class="mt-0.5 text-xs tabular-nums text-gray-500 dark:text-dark-400">
              {{ t('team.ownedFmt', { done: summary?.owned_key_count ?? 0, total: summary?.key_count ?? 0 }) }}
            </div>
            <div
              v-if="summary && summary.owned_key_count === 0"
              class="mt-0.5 text-xs text-gray-500 dark:text-dark-400"
            >
              {{ t('team.ownedNone') }}
            </div>
          </div>
        </div>

        <!-- 跨保留边界：cut_date 可能晚于本期终点（本期整段越界），
             此时绝不能写「可查数据自 X 起」——那会与本期区间自相矛盾 -->
        <div
          v-if="summary?.retention_warning"
          class="mt-4 flex flex-wrap items-center gap-3 rounded-lg bg-blue-50 px-3 py-2 text-xs text-blue-800 dark:bg-blue-900/20 dark:text-blue-200"
        >
          <span>{{ retentionText }}</span>
          <button class="font-medium underline underline-offset-2" @click="jumpToRecent">
            {{ t('team.gotoRecent') }}
          </button>
        </div>
      </section>

      <!-- 搜索 -->
      <div class="mt-5 flex flex-wrap items-center gap-3">
        <input
          v-model="searchInput"
          type="search"
          :placeholder="t('team.search')"
          class="w-full max-w-sm rounded-lg border border-gray-300 bg-white px-3 py-2 text-sm text-gray-900 placeholder-gray-400 focus:border-primary-500 focus:outline-none dark:border-dark-600 dark:bg-dark-800 dark:text-white"
        />
        <span
          v-if="searchQuery && summary"
          class="text-xs text-gray-500 dark:text-dark-400"
        >
          {{ t('team.searchFiltered', { shown: total, all: summary.row_count }) }}
        </span>
      </div>

      <!-- 表格 -->
      <section
        class="mt-3 overflow-hidden rounded-xl border border-gray-200 bg-white dark:border-dark-700 dark:bg-dark-800"
      >
        <div class="overflow-x-auto">
          <table class="min-w-full text-sm">
            <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-900/40 dark:text-dark-400">
              <tr>
                <th class="px-4 py-3 text-left font-medium">
                  <button class="hover:text-gray-800 dark:hover:text-dark-100" @click="toggleSort('name')">
                    {{ t('team.colName') }}{{ sortMark('name') }}
                  </button>
                </th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium">
                  <button class="hover:text-gray-800 dark:hover:text-dark-100" @click="toggleSort('cost')">
                    {{ t('team.colCost') }}{{ sortMark('cost') }}
                  </button>
                </th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium">
                  <button class="hover:text-gray-800 dark:hover:text-dark-100" @click="toggleSort('delta')">
                    {{ t('team.colDelta') }}{{ sortMark('delta') }}
                  </button>
                </th>
                <th class="whitespace-nowrap px-4 py-3 text-left font-medium">{{ t('team.colTrend') }}</th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium">{{ t('team.colRequests') }}</th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium">{{ t('team.colAvgCost') }}</th>
                <th class="whitespace-nowrap px-4 py-3 text-left font-medium">{{ t('team.colTopModel') }}</th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium">{{ t('team.colCacheHit') }}</th>
                <th class="whitespace-nowrap px-4 py-3 text-right font-medium">{{ t('team.colLastUsed') }}</th>
              </tr>
            </thead>

            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <!-- 加载骨架 -->
              <tr v-if="loading" v-for="n in 5" :key="'sk' + n">
                <td colspan="9" class="px-4 py-4">
                  <div class="h-4 w-full animate-pulse rounded bg-gray-100 dark:bg-dark-700"></div>
                </td>
              </tr>

              <tr v-else-if="error">
                <td colspan="9" class="px-4 py-10 text-center">
                  <div class="text-sm text-gray-600 dark:text-dark-300">{{ t('team.loadFailed') }}</div>
                  <button
                    class="mt-2 text-sm font-medium text-primary-600 hover:underline dark:text-primary-400"
                    @click="reload"
                  >
                    {{ t('team.retry') }}
                  </button>
                </td>
              </tr>

              <!-- 全新账号：一把令牌都没有 -->
              <tr v-else-if="isEmptyAccount">
                <td colspan="9" class="px-4 py-12 text-center">
                  <div class="text-sm font-medium text-gray-700 dark:text-dark-200">{{ t('team.empty') }}</div>
                  <div class="mt-1 text-xs text-gray-500 dark:text-dark-400">{{ t('team.emptyHint') }}</div>
                  <RouterLink
                    to="/keys"
                    class="mt-3 inline-block text-sm font-medium text-primary-600 hover:underline dark:text-primary-400"
                  >
                    {{ t('team.gotoKeys') }}
                  </RouterLink>
                </td>
              </tr>

              <!-- 搜索无结果：与「全新账号」区分开，别把它渲染成空账号 -->
              <tr v-else-if="rows.length === 0">
                <td colspan="9" class="px-4 py-12 text-center text-sm text-gray-500 dark:text-dark-400">
                  {{ searchQuery ? t('team.searchEmpty') : t('team.zeroCost') }}
                </td>
              </tr>

              <tr
                v-for="r in rows"
                v-else
                :key="r.group_key"
                class="hover:bg-gray-50 dark:hover:bg-dark-700/40"
              >
                <td class="px-4 py-3">
                  <div class="flex items-center gap-2">
                    <span class="font-medium text-gray-900 dark:text-white">{{ r.display_name }}</span>
                    <span
                      v-if="r.by_owner"
                      class="rounded bg-primary-50 px-1.5 py-0.5 text-[10px] font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300"
                    >{{ t('team.byOwner') }}</span>
                    <span
                      v-else-if="r.all_deleted"
                      class="rounded bg-gray-100 px-1.5 py-0.5 text-[10px] text-gray-500 dark:bg-dark-700 dark:text-dark-400"
                    >{{ t('team.deleted') }}</span>
                  </div>
                  <!-- 令牌信息并进归属列：加厚版把独立的「令牌」列让位给数据维度 -->
                  <div class="mt-0.5 text-gray-500 dark:text-dark-400">
                    <span v-if="r.masked_key" class="font-mono text-xs">{{ r.masked_key }}</span>
                    <span v-else-if="r.all_deleted" class="text-xs">{{ t('team.deletedKeys', { n: r.key_count_all }) }}</span>
                    <span v-else class="text-xs tabular-nums">{{ t('team.keys', { n: r.key_count }) }}</span>
                  </div>
                </td>

                <td class="whitespace-nowrap px-4 py-3 text-right font-medium tabular-nums text-gray-900 dark:text-white">
                  {{ formatTeamMoney(r.current_cost) }}
                  <!-- 占比条：top_row_cost 与行金额有 1 分口径差，百分比已 clamp -->
                  <div
                    v-if="showShareBar"
                    class="ml-auto mt-1.5 h-1 w-24 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700"
                  >
                    <div
                      class="h-full rounded-full bg-primary-500"
                      :style="{ width: shareBarPercent(r.current_cost, summary?.top_row_cost ?? 0) + '%' }"
                    ></div>
                  </div>
                </td>

                <td
                  class="whitespace-nowrap px-4 py-3 text-right tabular-nums"
                  :class="r.is_anomaly ? 'font-semibold text-red-600 dark:text-red-400' : 'text-gray-500 dark:text-dark-400'"
                >
                  <span v-if="r.is_anomaly">▲ </span>{{ formatTeamDelta(r.delta_pct) }}
                </td>

                <!-- 趋势：daily 为空数组时组件什么都不画，这一格就是空的 -->
                <td class="px-4 py-3" data-testid="trend-cell">
                  <TrendSparkline
                    :daily="r.daily"
                    :label="t('team.trendLabel', { name: r.display_name })"
                  />
                </td>

                <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-500 dark:text-dark-400">
                  {{ formatTeamCount(r.requests) }}
                </td>

                <!-- 平均单次显著高于全局（>1.5x）标黄：这是「在烧长上下文」的信号 -->
                <td
                  class="whitespace-nowrap px-4 py-3 text-right tabular-nums"
                  :class="isAvgCostHot(r.avg_cost, globalAvg)
                    ? 'font-semibold text-amber-600 dark:text-amber-400'
                    : 'text-gray-500 dark:text-dark-400'"
                  :title="avgHotTitle(r)"
                >
                  {{ formatTeamAvgCost(r.avg_cost) }}
                </td>

                <td class="px-4 py-3">
                  <template v-if="r.top_model">
                    <div class="max-w-[11rem] truncate text-gray-700 dark:text-dark-200" :title="r.top_model">
                      {{ r.top_model }}
                    </div>
                    <div v-if="r.top_model_share !== null && r.top_model_share !== undefined" class="mt-1 flex items-center gap-2">
                      <div class="h-1 w-16 overflow-hidden rounded-full bg-gray-100 dark:bg-dark-700">
                        <div
                          class="h-full rounded-full bg-primary-500"
                          :style="{ width: percentBarWidth(r.top_model_share) + '%' }"
                        ></div>
                      </div>
                      <span class="text-xs tabular-nums text-gray-500 dark:text-dark-400">
                        {{ formatTeamPercent(r.top_model_share) }}
                      </span>
                    </div>
                  </template>
                  <span v-else class="text-gray-400 dark:text-dark-500">—</span>
                </td>

                <!-- cache_hit_rate 为 null 是「没有输入 token」，不是 0% -->
                <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-500 dark:text-dark-400">
                  {{ formatTeamPercent(r.cache_hit_rate ?? null) }}
                </td>

                <td class="whitespace-nowrap px-4 py-3 text-right tabular-nums text-gray-500 dark:text-dark-400">
                  {{ formatTeamLastUsed(r.last_used_at) ?? t('team.neverUsed') }}
                </td>
              </tr>
            </tbody>
          </table>
        </div>

        <!-- 对账条：搜索/分页时金额仍是全量 -->
        <div
          v-if="summary && !isEmptyAccount"
          class="border-t border-gray-100 px-4 py-3 text-xs leading-relaxed text-gray-500 dark:border-dark-700 dark:text-dark-400"
        >
          <div class="text-gray-700 dark:text-dark-200">{{ reconciliationText }}</div>
          <div class="mt-1">{{ t('team.caliber') }}</div>
          <div>{{ t('team.retentionHint') }}</div>
        </div>

        <div v-if="pages > 1" class="border-t border-gray-100 px-4 py-3 dark:border-dark-700">
          <Pagination
            :total="total"
            :page="page"
            :page-size="pageSize"
            :show-page-size-selector="false"
            @update:page="onPageChange"
          />
        </div>
      </section>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { RouterLink } from 'vue-router'
import AppLayout from '@/components/layout/AppLayout.vue'
import Pagination from '@/components/common/Pagination.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import { getTeamSummary, listTeamRows } from '@/features/team/api'
import TrendSparkline from '@/features/team/TrendSparkline.vue'
import type { TeamRow, TeamSort, TeamSummary } from '@/features/team/types'
import {
  defaultTeamRange,
  formatTeamAvgCost,
  formatTeamCount,
  formatTeamDelta,
  formatTeamLastUsed,
  formatTeamMoney,
  formatTeamPercent,
  formatTeamRate,
  formatTeamSavedCost,
  globalAvgCost,
  isAvgCostHot,
  localDate,
  percentBarWidth,
  periodDayCount,
  requestsPerDay,
  shareBarPercent,
} from '@/features/team/format'

const { t } = useI18n()

const range = defaultTeamRange()
const startDate = ref(range.start_date)
const endDate = ref(range.end_date)

const summary = ref<TeamSummary | null>(null)
const rows = ref<TeamRow[]>([])
const total = ref(0)
const pages = ref(0)
const page = ref(1)
const pageSize = 20
const sort = ref<TeamSort>('cost')
const order = ref<'asc' | 'desc'>('desc')
const searchInput = ref('')
const searchQuery = ref('')
const loading = ref(true)
const error = ref(false)

// summary 与 rows 共用一个 AbortController：周期/排序/分页/搜索一变就取消上一批。
// 否则快速切周期时慢的那个后到，页面会出现「周期文案写着本月、金额是上月的」——
// 只在网络慢时出现，本地开发绝对复现不出来。
let controller: AbortController | null = null
let searchTimer: ReturnType<typeof setTimeout> | null = null

// 「全新账号」必须靠 key_count 判定，不能靠 rows.length —— 搜索无结果时 rows 也是空，
// 那两种状态的文案完全不同（一个引导去建令牌，一个提示换个词搜）。
const isEmptyAccount = computed(
  () => !loading.value && !error.value && summary.value !== null && summary.value.key_count === 0 && summary.value.row_count === 0,
)

// 只有一行时不渲染占比条：满条会看起来像超限告警。
const showShareBar = computed(() => (summary.value?.row_count ?? 0) > 1 && (summary.value?.top_row_cost ?? 0) > 0)

// 全局平均单次 = 总消耗 / 总请求数。requests 为 0 时是 null 而不是 0 ——
// 拿 0 当基准会让每一行的「显著高于全局」都成立，整列标黄。
const globalAvg = computed(() => globalAvgCost(summary.value?.total_cost_raw, summary.value?.requests))

// 汇总请求数可能包含保留切点当天的残缺数据，而接口没有逐日请求数可供前端剔除。
// 只要出现保留期警告就不展示日均，避免把不完整的分子除以完整自然日数。
const requestsPerDayValue = computed(() => {
  const s = summary.value
  if (!s || s.retention_warning) return null
  return requestsPerDay(s.requests, periodDayCount(s.period?.start_date, s.period?.end_date))
})

function avgHotTitle(r: TeamRow): string {
  const g = globalAvg.value
  if (!g || !isAvgCostHot(r.avg_cost, g)) return ''
  return t('team.avgCostHot', { x: (r.avg_cost / g).toFixed(1) })
}

const deltaClass = computed(() => {
  const p = summary.value?.delta_pct
  if (p === null || p === undefined) return 'text-gray-400 dark:text-dark-500'
  return 'text-gray-900 dark:text-white'
})

const retentionText = computed(() => {
  const w = summary.value?.retention_warning
  if (!w) return ''
  // cut_date 晚于本期终点 = 本期整段越界，此时说「可查数据自 X 起」会与本期区间打架
  if (summary.value && w.cut_date > summary.value.period.end_date) return t('team.retentionOutOfRange')
  return t('team.retentionWarning')
})

const reconciliationText = computed(() => {
  const s = summary.value
  if (!s) return ''
  if (s.retention_warning) {
    return t('team.reconciliationDegraded', { total: formatTeamMoney(s.total_cost, { zeroAsDash: false }) })
  }
  const base = t('team.reconciliation', {
    total: formatTeamMoney(s.total_cost, { zeroAsDash: false }),
    rows: t('team.rows', { n: s.row_count }),
  })
  // N 取 deleted_key_count（全量），不是 key_count —— 后者只数存续令牌
  return s.deleted_key_count > 0
    ? base + t('team.reconciliationWithDeleted', { n: s.deleted_key_count })
    : base
})

function sortMark(k: TeamSort): string {
  if (sort.value !== k) return ''
  return order.value === 'desc' ? ' ↓' : ' ↑'
}

function toggleSort(k: TeamSort) {
  if (sort.value === k) {
    order.value = order.value === 'desc' ? 'asc' : 'desc'
  } else {
    sort.value = k
    order.value = 'desc'
  }
  page.value = 1
  void reload()
}

function onPageChange(p: number) {
  page.value = p
  void reload()
}

function jumpToRecent() {
  const now = new Date()
  startDate.value = localDate(new Date(now.getTime() - 29 * 86400_000))
  endDate.value = localDate(now)
}

async function reload() {
  controller?.abort()
  const ac = new AbortController()
  controller = ac
  loading.value = true
  error.value = false

  const base = { start_date: startDate.value, end_date: endDate.value }
  try {
    const [s, r] = await Promise.all([
      getTeamSummary(base, ac.signal),
      listTeamRows(
        { ...base, sort: sort.value, order: order.value, page: page.value, page_size: pageSize, q: searchQuery.value || undefined },
        ac.signal,
      ),
    ])
    if (ac.signal.aborted) return
    summary.value = s
    rows.value = r.items
    total.value = r.total
    pages.value = r.pages
  } catch (e) {
    if (ac.signal.aborted || (e as { code?: string })?.code === 'ERR_CANCELED') return
    error.value = true
  } finally {
    if (!ac.signal.aborted) loading.value = false
  }
}

watch([startDate, endDate], () => {
  page.value = 1
  void reload()
})

watch(searchInput, (v) => {
  if (searchTimer) clearTimeout(searchTimer)
  searchTimer = setTimeout(() => {
    searchQuery.value = v.trim()
    page.value = 1
    void reload()
  }, 300)
})

onMounted(() => void reload())
onBeforeUnmount(() => {
  controller?.abort()
  if (searchTimer) clearTimeout(searchTimer)
})
</script>
