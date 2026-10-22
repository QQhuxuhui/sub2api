<template>
  <AppLayout>
    <div class="space-y-6">
      <!-- 范围与阈值 -->
      <div class="card p-4">
        <div class="flex flex-wrap items-center gap-4">
          <div class="flex items-center gap-2">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.openaiCache.timeRange') }}:</span>
            <DateRangePicker
              v-model:start-date="startDate"
              v-model:end-date="endDate"
              @change="reload"
            />
          </div>
          <div class="flex items-center gap-2" :title="t('admin.openaiCache.thresholdHint')">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.openaiCache.threshold') }}:</span>
            <input
              v-model.number="threshold"
              type="number"
              min="0"
              max="100"
              step="0.5"
              class="input w-24"
              data-testid="openai-cache-threshold"
            />
            <span class="text-sm text-gray-500">%</span>
          </div>
          <div class="flex items-center gap-2" :title="t('admin.openaiCache.scopeHint')">
            <div class="w-36">
              <Select v-model="scope" :options="scopeOptions" />
            </div>
          </div>
          <div class="flex items-center gap-2" :title="t('admin.openaiCache.granularityMinuteHint')">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.openaiCache.granularity') }}:</span>
            <div class="w-32">
              <Select v-model="granularity" :options="granularityOptions" @change="loadDaily" />
            </div>
          </div>
          <div class="ml-auto flex items-center gap-2">
            <span class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('admin.openaiCache.table.sortBy') }}:</span>
            <div class="w-44">
              <Select v-model="sortKey" :options="sortOptions" />
            </div>
          </div>
        </div>
      </div>

      <!-- 汇总卡片 -->
      <div class="grid grid-cols-2 gap-4 md:grid-cols-3 xl:grid-cols-6">
        <div class="card p-4">
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.openaiCache.summary.accounts') }}</p>
          <p class="mt-1 text-2xl font-semibold text-gray-900 dark:text-white">{{ summary?.accounts ?? 0 }}</p>
          <p class="mt-1 text-xs text-gray-500">{{ t('admin.openaiCache.summary.requests') }}: {{ (summaryBucket?.requests ?? 0).toLocaleString() }}</p>
        </div>
        <div class="card p-4">
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.openaiCache.summary.rawRate') }}</p>
          <p class="mt-1 text-2xl font-semibold" :class="rateClass(summaryBucket?.raw_rate ?? null, threshold)">{{ fmtPct(summaryBucket?.raw_rate) }}</p>
        </div>
        <div class="card p-4">
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.openaiCache.summary.displayRate') }}</p>
          <p class="mt-1 text-2xl font-semibold" :class="rateClass(summaryBucket?.display_rate ?? null, threshold)">{{ fmtPct(summaryBucket?.display_rate) }}</p>
        </div>
        <div class="card p-4">
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.openaiCache.summary.expectedCost') }}</p>
          <p class="mt-1 text-2xl font-semibold text-gray-900 dark:text-white">{{ fmtMoney(summary?.expected_cost) }}</p>
        </div>
        <div class="card p-4">
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.openaiCache.summary.billedCost') }}</p>
          <p class="mt-1 text-2xl font-semibold text-gray-900 dark:text-white">{{ fmtMoney(summary?.billed_cost) }}</p>
        </div>
        <div class="card p-4" :title="t('admin.openaiCache.summary.costDeltaHint')">
          <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('admin.openaiCache.summary.costDelta') }}</p>
          <p class="mt-1 text-2xl font-semibold text-amber-600 dark:text-amber-400">{{ fmtMoney(summary?.cost_delta) }}</p>
        </div>
      </div>

      <!-- 按天走势 -->
      <div class="card p-4">
        <div class="mb-3 flex items-center justify-between gap-4">
          <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
            {{ selectedAccount ? t('admin.openaiCache.chart.titleFor', { name: selectedAccount.name }) : t('admin.openaiCache.chart.title') }}
          </h3>
          <button v-if="selectedAccount" type="button" class="btn btn-secondary btn-sm" @click="selectAccount(null)">
            {{ t('admin.openaiCache.chart.backToAll') }}
          </button>
        </div>
        <div v-if="dailyLoading" class="flex h-64 items-center justify-center"><LoadingSpinner /></div>
        <div v-else-if="daily.length === 0" class="flex h-64 items-center justify-center text-sm text-gray-500">{{ t('admin.openaiCache.chart.empty') }}</div>
        <div v-else class="h-72">
          <Line :data="chartData" :options="chartOptions" />
        </div>
        <p v-if="daily.length > 300" class="mt-3 text-xs text-gray-500">{{ t('admin.openaiCache.chart.tableHidden') }}</p>
        <div v-if="daily.length && daily.length <= 300" class="mt-4 overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700">
            <thead class="bg-gray-50 text-xs uppercase text-gray-500 dark:bg-dark-800 dark:text-dark-400">
              <tr>
                <th class="px-3 py-2 text-left">{{ t('usage.time') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.requests') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.rawRate') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.displayRate') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.expectedCost') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.billedCost') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.costDelta') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="d in daily" :key="d.date">
                <td class="px-3 py-1.5 text-gray-700 dark:text-gray-300">{{ d.date }}</td>
                <td class="px-3 py-1.5 text-right text-gray-700 dark:text-gray-300">{{ bucketOf(d).requests.toLocaleString() }}</td>
                <td class="px-3 py-1.5 text-right" :class="rateClass(bucketOf(d).raw_rate, threshold)">{{ fmtPct(bucketOf(d).raw_rate) }}</td>
                <td class="px-3 py-1.5 text-right" :class="rateClass(bucketOf(d).display_rate, threshold)">{{ fmtPct(bucketOf(d).display_rate) }}</td>
                <td class="px-3 py-1.5 text-right text-gray-700 dark:text-gray-300">{{ fmtMoney(d.expected_cost) }}</td>
                <td class="px-3 py-1.5 text-right text-gray-700 dark:text-gray-300">{{ fmtMoney(d.billed_cost) }}</td>
                <td class="px-3 py-1.5 text-right text-amber-600 dark:text-amber-400">{{ fmtMoney(d.cost_delta) }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- 账号表 -->
      <div class="card overflow-hidden">
        <div v-if="loading" class="flex h-48 items-center justify-center"><LoadingSpinner /></div>
        <div v-else-if="sortedAccounts.length === 0" class="flex h-48 items-center justify-center text-sm text-gray-500">{{ t('admin.openaiCache.table.empty') }}</div>
        <div v-else class="overflow-x-auto">
          <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700" data-testid="openai-cache-table">
            <thead class="bg-gray-50 text-xs uppercase text-gray-500 dark:bg-dark-800 dark:text-dark-400">
              <tr>
                <th class="px-3 py-2 text-left">{{ t('admin.openaiCache.table.account') }}</th>
                <th class="px-3 py-2 text-left">{{ t('admin.openaiCache.table.accountStatus') }}</th>
                <th class="px-3 py-2 text-left">{{ t('admin.openaiCache.table.type') }}</th>
                <th class="px-3 py-2 text-left">{{ t('admin.openaiCache.table.simulation') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.requests') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.rawRate') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.displayRate') }}</th>
                <th class="px-3 py-2 text-right" :title="t('admin.openaiCache.table.windowHint')">{{ t('admin.openaiCache.table.window') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.zeroCache') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.expectedCost') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.billedCost') }}</th>
                <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.costDelta') }}</th>
                <th class="px-3 py-2 text-center">{{ t('admin.openaiCache.table.status') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr
                v-for="row in sortedAccounts"
                :key="row.account_id"
                class="cursor-pointer hover:bg-gray-50 dark:hover:bg-dark-800"
                :class="selectedAccount?.account_id === row.account_id && 'bg-primary-50 dark:bg-primary-900/20'"
                @click="selectAccount(row)"
              >
                <td class="px-3 py-2">
                  <div class="font-medium text-gray-900 dark:text-white">{{ row.name }}</div>
                  <div class="text-xs text-gray-500">#{{ row.account_id }}<span v-if="row.is_shadow"> · {{ t('admin.openaiCache.table.shadow') }}</span></div>
                </td>
                <td class="px-3 py-2" @click.stop>
                  <div class="flex items-center gap-1.5">
                    <AccountStatusIndicator :account="asAccount(row)" />
                    <span v-if="!row.schedulable" class="inline-flex rounded bg-gray-100 px-1.5 py-0.5 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-300">{{ t('admin.accounts.schedulableDisabled') }}</span>
                  </div>
                </td>
                <td class="px-3 py-2 text-gray-700 dark:text-gray-300">{{ row.type }}</td>
                <td class="px-3 py-2">
                  <span v-if="row.simulation.enabled" class="inline-flex rounded bg-primary-100 px-1.5 py-0.5 text-xs font-medium text-primary-700 dark:bg-primary-900/30 dark:text-primary-300">
                    {{ t('admin.openaiCache.table.simOn', { lo: row.simulation.lo, hi: row.simulation.hi }) }}
                  </span>
                  <span v-else class="text-xs text-gray-400">{{ t('admin.openaiCache.table.simOff') }}</span>
                </td>
                <td class="px-3 py-2 text-right text-gray-700 dark:text-gray-300">{{ bucketOf(row).requests.toLocaleString() }}</td>
                <td class="px-3 py-2 text-right font-medium" :class="rateClass(bucketOf(row).raw_rate, thresholdFor(row))">{{ fmtPct(bucketOf(row).raw_rate) }}</td>
                <td class="px-3 py-2 text-right font-medium" :class="rateClass(bucketOf(row).display_rate, thresholdFor(row))">{{ fmtPct(bucketOf(row).display_rate) }}</td>
                <td class="px-3 py-2 text-right text-gray-700 dark:text-gray-300">
                  <span v-if="row.window">{{ fmtPct(row.window.display_rate) }}</span>
                  <span v-else class="text-gray-400">—</span>
                </td>
                <td class="px-3 py-2 text-right" :class="row.zero_cache_pct >= 15 ? 'text-amber-600 dark:text-amber-400' : 'text-gray-700 dark:text-gray-300'" :title="row.zero_cache_pct >= 15 ? t('admin.openaiCache.table.zeroCacheWarn') : ''">
                  {{ row.requests > 0 ? row.zero_cache_pct.toFixed(1) + '%' : '—' }}
                </td>
                <td class="px-3 py-2 text-right text-gray-700 dark:text-gray-300">{{ fmtMoney(row.expected_cost) }}</td>
                <td class="px-3 py-2 text-right text-gray-700 dark:text-gray-300">{{ fmtMoney(row.billed_cost) }}</td>
                <td class="px-3 py-2 text-right text-amber-600 dark:text-amber-400">{{ fmtMoney(row.cost_delta) }}</td>
                <td class="px-3 py-2 text-center">
                  <span v-if="bucketOf(row).display_rate == null" class="text-xs text-gray-400">{{ t('admin.openaiCache.table.noData') }}</span>
                  <span v-else-if="(bucketOf(row).display_rate ?? 0) >= thresholdFor(row)" class="inline-flex rounded bg-emerald-100 px-1.5 py-0.5 text-xs font-medium text-emerald-700 dark:bg-emerald-900/30 dark:text-emerald-300">{{ t('admin.openaiCache.table.ok') }}</span>
                  <span v-else class="inline-flex rounded bg-rose-100 px-1.5 py-0.5 text-xs font-medium text-rose-700 dark:bg-rose-900/30 dark:text-rose-300">{{ t('admin.openaiCache.table.below') }}</span>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import {
  Chart as ChartJS,
  CategoryScale,
  LinearScale,
  PointElement,
  LineElement,
  Title,
  Tooltip,
  Legend,
  Filler
} from 'chart.js'
import { Line } from 'vue-chartjs'
import AppLayout from '@/components/layout/AppLayout.vue'
import DateRangePicker from '@/components/common/DateRangePicker.vue'
import Select from '@/components/common/Select.vue'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import AccountStatusIndicator from '@/components/account/AccountStatusIndicator.vue'
import { useAppStore } from '@/stores/app'
import { openaiCacheAPI } from '@/api/admin/openaiCache'
import type {
  OpenAICacheAccountRow,
  OpenAICacheDailyRow,
  OpenAICacheGranularity,
  OpenAICacheRateView,
  OpenAICacheSummary
} from '@/api/admin/openaiCache'
import type { Account } from '@/types'

ChartJS.register(CategoryScale, LinearScale, PointElement, LineElement, Title, Tooltip, Legend, Filler)

const { t } = useI18n()
const appStore = useAppStore()

const formatLD = (d: Date) => {
  const year = d.getFullYear()
  const month = String(d.getMonth() + 1).padStart(2, '0')
  const day = String(d.getDate()).padStart(2, '0')
  return `${year}-${month}-${day}`
}
// 默认只看今天：usage_logs 一天百万行级，范围越大越慢；历史天数按需再拉。
const today = formatLD(new Date())
const startDate = ref(today)
const endDate = ref(today)

const THRESHOLD_KEY = 'openai-cache-threshold'
const readThreshold = (): number => {
  try {
    const v = Number(localStorage.getItem(THRESHOLD_KEY))
    return Number.isFinite(v) && v > 0 ? v : 80
  } catch {
    return 80
  }
}
const threshold = ref<number>(readThreshold())
watch(threshold, (v) => {
  try { localStorage.setItem(THRESHOLD_KEY, String(v)) } catch { /* ignore */ }
})

type Scope = 'all' | 'text'
const scope = ref<Scope>('all')
const scopeOptions = computed(() => [
  { value: 'all', label: t('admin.openaiCache.scopeAll') },
  { value: 'text', label: t('admin.openaiCache.scopeText') }
])

const granularity = ref<OpenAICacheGranularity>('hour')
const rangeDays = computed(() => {
  const s = new Date(`${startDate.value}T00:00:00`).getTime()
  const e = new Date(`${endDate.value}T00:00:00`).getTime()
  return Math.round((e - s) / 86400000) + 1
})
const granularityOptions = computed(() => {
  const opts = [
    { value: 'day', label: t('admin.openaiCache.granularityDay') },
    { value: 'hour', label: t('admin.openaiCache.granularityHour') },
    { value: '5min', label: t('admin.openaiCache.granularity5Min') }
  ]
  // 分钟级只在范围不超过 2 天时可选（后端超过 2 天会自动降到 5 分钟）。
  if (rangeDays.value <= 2) opts.push({ value: 'minute', label: t('admin.openaiCache.granularityMinute') })
  return opts
})
watch(rangeDays, (d) => {
  if (d > 2 && granularity.value === 'minute') granularity.value = '5min'
})

// 复用账号页的状态指示组件：把本页的行映射成它需要的 Account 字段。
const asAccount = (row: OpenAICacheAccountRow): Account =>
  ({
    id: row.account_id,
    name: row.name,
    platform: 'openai',
    type: row.type,
    status: row.status,
    schedulable: row.schedulable,
    error_message: row.error_message || '',
    rate_limit_reset_at: row.rate_limit_reset_at ?? null,
    overload_until: row.overload_until ?? null,
    temp_unschedulable_until: row.temp_unschedulable_until ?? null,
    temp_unschedulable_reason: row.temp_unschedulable_reason || '',
    expires_at: row.expires_at ?? null,
    last_used_at: row.last_used_at ?? null,
    extra: {}
  }) as unknown as Account

type SortKey = 'delta' | 'rawRate' | 'requests'
const sortKey = ref<SortKey>('delta')
const sortOptions = computed(() => [
  { value: 'delta', label: t('admin.openaiCache.table.sortDelta') },
  { value: 'rawRate', label: t('admin.openaiCache.table.sortRawRate') },
  { value: 'requests', label: t('admin.openaiCache.table.sortRequests') }
])

const loading = ref(false)
const dailyLoading = ref(false)
const summary = ref<OpenAICacheSummary | null>(null)
const accounts = ref<OpenAICacheAccountRow[]>([])
const daily = ref<OpenAICacheDailyRow[]>([])
const selectedAccount = ref<OpenAICacheAccountRow | null>(null)

const bucketOf = (x: { all: OpenAICacheRateView; text: OpenAICacheRateView }): OpenAICacheRateView =>
  scope.value === 'text' ? x.text : x.all
const summaryBucket = computed(() => (summary.value ? bucketOf(summary.value) : null))

const thresholdFor = (row: OpenAICacheAccountRow): number =>
  row.simulation.enabled ? row.simulation.lo : threshold.value

const sortedAccounts = computed(() => {
  const list = [...accounts.value]
  switch (sortKey.value) {
    case 'rawRate':
      list.sort((a, b) => (bucketOf(a).raw_rate ?? 101) - (bucketOf(b).raw_rate ?? 101))
      break
    case 'requests':
      list.sort((a, b) => bucketOf(b).requests - bucketOf(a).requests)
      break
    default:
      list.sort((a, b) => b.cost_delta - a.cost_delta || b.requests - a.requests)
  }
  return list
})

const fmtPct = (v: number | null | undefined): string => (v == null ? '—' : `${v.toFixed(1)}%`)
const fmtMoney = (v: number | null | undefined): string => `$${(v ?? 0).toFixed(2)}`
const rateClass = (v: number | null, line: number): string => {
  if (v == null) return 'text-gray-400'
  return v >= line ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600 dark:text-rose-400'
}

const chartData = computed(() => ({
  labels: daily.value.map((d) => d.date),
  datasets: [
    {
      label: t('admin.openaiCache.chart.rawRate'),
      data: daily.value.map((d) => bucketOf(d).raw_rate),
      borderColor: 'rgb(244, 63, 94)',
      backgroundColor: 'rgba(244, 63, 94, 0.1)',
      yAxisID: 'y',
      tension: 0.3
    },
    {
      label: t('admin.openaiCache.chart.displayRate'),
      data: daily.value.map((d) => bucketOf(d).display_rate),
      borderColor: 'rgb(16, 185, 129)',
      backgroundColor: 'rgba(16, 185, 129, 0.1)',
      yAxisID: 'y',
      tension: 0.3
    },
    {
      label: t('admin.openaiCache.chart.costDelta'),
      data: daily.value.map((d) => d.cost_delta),
      borderColor: 'rgb(245, 158, 11)',
      backgroundColor: 'rgba(245, 158, 11, 0.15)',
      yAxisID: 'y1',
      fill: true,
      tension: 0.3
    }
  ]
}))
const chartOptions = computed(() => ({
  responsive: true,
  maintainAspectRatio: false,
  interaction: { mode: 'index' as const, intersect: false },
  scales: {
    y: { type: 'linear' as const, position: 'left' as const, min: 0, max: 100, ticks: { callback: (v: string | number) => `${v}%` } },
    y1: { type: 'linear' as const, position: 'right' as const, min: 0, grid: { drawOnChartArea: false }, ticks: { callback: (v: string | number) => `$${v}` } }
  }
}))

const loadAccounts = async () => {
  loading.value = true
  try {
    const res = await openaiCacheAPI.listAccounts({ start_date: startDate.value, end_date: endDate.value })
    summary.value = res.summary
    accounts.value = res.accounts || []
    if (selectedAccount.value) {
      selectedAccount.value = accounts.value.find((a) => a.account_id === selectedAccount.value?.account_id) || null
    }
  } catch (error) {
    console.error('Failed to load openai cache accounts:', error)
    appStore.showError(t('admin.openaiCache.loadFailed'))
  } finally {
    loading.value = false
  }
}

const loadDaily = async () => {
  dailyLoading.value = true
  try {
    const res = await openaiCacheAPI.listDaily({
      start_date: startDate.value,
      end_date: endDate.value,
      account_id: selectedAccount.value?.account_id,
      granularity: granularity.value
    })
    daily.value = res.days
    if (res.granularity !== granularity.value) granularity.value = res.granularity
  } catch (error) {
    console.error('Failed to load openai cache daily:', error)
    appStore.showError(t('admin.openaiCache.loadFailed'))
  } finally {
    dailyLoading.value = false
  }
}

const reload = async () => {
  await Promise.all([loadAccounts(), loadDaily()])
}

const selectAccount = (row: OpenAICacheAccountRow | null) => {
  selectedAccount.value = row && selectedAccount.value?.account_id === row.account_id ? null : row
  void loadDaily()
}

onMounted(reload)
</script>
