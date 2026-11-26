<template>
  <div class="space-y-6" data-testid="openai-cache-user-panel">
    <!-- 选中用户的走势 -->
    <div v-if="selected" class="card p-4">
      <div class="mb-3 flex items-center justify-between gap-4">
        <h3 class="text-sm font-semibold text-gray-900 dark:text-white" :title="t('admin.openaiCacheUser.timezoneHint', { tz: timezoneName })">
          {{ t('admin.openaiCacheUser.trendFor', { name: label(selected) }) }}
        </h3>
        <button type="button" class="btn btn-secondary btn-sm" @click="selected = null">{{ t('admin.openaiCache.chart.backToAll') }}</button>
      </div>
      <div v-if="trendLoading" class="flex h-64 items-center justify-center"><LoadingSpinner /></div>
      <div v-else-if="trend.length === 0" class="flex h-64 items-center justify-center text-sm text-gray-500">{{ t('admin.openaiCache.chart.empty') }}</div>
      <div v-else class="h-72"><Line :data="chartData" :options="chartOptions" /></div>
    </div>

    <div class="card overflow-hidden">
      <div class="flex items-center gap-3 border-b border-gray-100 px-4 py-2 dark:border-dark-700">
        <label class="flex items-center gap-2 text-sm text-gray-600 dark:text-gray-300">
          <input v-model="onlyOverrides" type="checkbox" data-testid="openai-cache-user-only-overrides" />
          {{ t('admin.openaiCacheUser.onlyOverrides') }}
        </label>
        <span class="ml-auto text-xs text-gray-400" :title="t('admin.openaiCacheUser.timezoneHint', { tz: timezoneName })">{{ timezoneName }}</span>
      </div>
      <div v-if="loading" class="flex h-48 items-center justify-center"><LoadingSpinner /></div>
      <div v-else-if="rows.length === 0" class="flex h-48 items-center justify-center text-sm text-gray-500">{{ t('admin.openaiCache.table.empty') }}</div>
      <div v-else class="overflow-x-auto">
        <table class="min-w-full divide-y divide-gray-200 text-sm dark:divide-dark-700" data-testid="openai-cache-user-table">
          <thead class="bg-gray-50 text-xs uppercase text-gray-500 dark:bg-dark-800 dark:text-dark-400">
            <tr>
              <th class="px-3 py-2 text-left">{{ t('admin.openaiCacheUser.table.user') }}</th>
              <th class="px-3 py-2 text-left" :title="t('admin.openaiCacheUser.table.sourceHint')">{{ t('admin.openaiCacheUser.table.source') }}</th>
              <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.requests') }}</th>
              <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.rawRate') }}</th>
              <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.displayRate') }}</th>
              <th class="px-3 py-2 text-right" :title="t('admin.openaiCacheUser.table.reachableHint')">{{ t('admin.openaiCacheUser.table.reachable') }}</th>
              <th class="px-3 py-2 text-right" :title="t('admin.openaiCache.table.windowHint')">{{ t('admin.openaiCache.table.window') }}</th>
              <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.zeroCache') }}</th>
              <th class="px-3 py-2 text-right">{{ t('admin.openaiCache.table.costDelta') }}</th>
              <th class="px-3 py-2 text-center">{{ t('admin.openaiCacheUser.table.verdict') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr
              v-for="row in rows"
              :key="row.user_id"
              class="cursor-pointer hover:bg-gray-50 dark:hover:bg-dark-800"
              :class="selected?.user_id === row.user_id ? 'bg-primary-50 dark:bg-dark-800' : ''"
              :data-testid="`openai-cache-user-row-${row.user_id}`"
              @click="select(row)"
            >
              <td class="px-3 py-2">
                <div class="flex items-center gap-2">
                  <span class="font-medium text-gray-900 dark:text-white">{{ label(row) }}</span>
                  <button
                    type="button"
                    class="text-xs text-primary-600 hover:underline dark:text-primary-400"
                    :data-testid="`openai-cache-user-edit-${row.user_id}`"
                    :title="t('admin.openaiCacheUser.editHint')"
                    @click.stop="openModal(row)"
                  >
                    {{ t('admin.openaiCacheUser.edit') }}
                  </button>
                </div>
                <div class="text-xs text-gray-400">#{{ row.user_id }}</div>
              </td>
              <td class="px-3 py-2">
                <span :class="['badge', sourceBadge(row)]" :title="sourceTitle(row)">{{ sourceText(row) }}</span>
              </td>
              <td class="px-3 py-2 text-right tabular-nums">{{ row.requests.toLocaleString() }}</td>
              <td class="px-3 py-2 text-right tabular-nums">{{ fmtPct(row.all.raw_rate) }}</td>
              <td class="px-3 py-2 text-right tabular-nums" :class="rateClass(row.all.display_rate, row)">{{ fmtPct(row.all.display_rate) }}</td>
              <td class="px-3 py-2 text-right tabular-nums" :title="coverageTitle(row)">{{ fmtPct(row.reachable_rate) }}</td>
              <td class="px-3 py-2 text-right tabular-nums">{{ fmtPct(row.window?.display_rate) }}</td>
              <td class="px-3 py-2 text-right tabular-nums">{{ row.zero_cache_pct.toFixed(1) }}%</td>
              <td class="px-3 py-2 text-right tabular-nums text-amber-600 dark:text-amber-400">{{ fmtMoney(row.cost_delta) }}</td>
              <td class="px-3 py-2 text-center">
                <span v-if="row.verdict" :class="['badge', verdictBadge(row.verdict)]" :title="verdictTitle(row.verdict)">{{ t(`admin.openaiCacheUser.verdict.${row.verdict}`) }}</span>
                <span v-else class="text-xs text-gray-400">{{ emptyVerdict(row) }}</span>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
    </div>

    <OpenAICacheUserSimModal :show="modalOpen" :user="modalUser" :global="global" @close="modalOpen = false" @saved="onSaved" />
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { Line } from 'vue-chartjs'
import LoadingSpinner from '@/components/common/LoadingSpinner.vue'
import OpenAICacheUserSimModal from '@/components/admin/OpenAICacheUserSimModal.vue'
import { useAppStore } from '@/stores/app'
import { openaiCacheAPI } from '@/api/admin/openaiCache'
import type {
  OpenAICacheGranularity,
  OpenAICacheSimConfigView,
  OpenAICacheUserRow,
  OpenAICacheUserTrendRow,
  OpenAICacheVerdict
} from '@/api/admin/openaiCache'

// 「上游缓存」页的按用户视图（日期按 Asia/Shanghai）。Chart.js 组件由父页面注册。
const props = defineProps<{
  startDate: string
  endDate: string
  granularity: OpenAICacheGranularity
}>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(false)
const users = ref<OpenAICacheUserRow[]>([])
const global = ref<OpenAICacheSimConfigView | null>(null)
const timezoneName = ref('Asia/Shanghai')
const onlyOverrides = ref(false)
const selected = ref<OpenAICacheUserRow | null>(null)
const trend = ref<OpenAICacheUserTrendRow[]>([])
const trendLoading = ref(false)
const modalOpen = ref(false)
const modalUser = ref<OpenAICacheUserRow | null>(null)

const rows = computed(() => (onlyOverrides.value ? users.value.filter((u) => u.override) : users.value))

const label = (row: OpenAICacheUserRow) => row.email || row.username || `#${row.user_id}`
const fmtPct = (v: number | null | undefined): string => (v == null ? '—' : `${v.toFixed(1)}%`)
const fmtMoney = (v: number | null | undefined): string => `$${(v ?? 0).toFixed(2)}`

const rateClass = (v: number | null, row: OpenAICacheUserRow): string => {
  if (v == null) return 'text-gray-400'
  if (!row.active || !row.config) return 'text-gray-900 dark:text-white'
  return v >= row.config.lo ? 'text-emerald-600 dark:text-emerald-400' : 'text-rose-600 dark:text-rose-400'
}

const sourceText = (row: OpenAICacheUserRow) => {
  if (row.source === 'user') return row.active ? t('admin.openaiCacheUser.source.userOn') : t('admin.openaiCacheUser.source.userOff')
  return t(`admin.openaiCacheUser.source.${row.source}`)
}
const sourceBadge = (row: OpenAICacheUserRow) => {
  if (row.source === 'user') return row.active ? 'badge-primary' : 'badge-gray'
  return row.source === 'global' ? 'badge-success' : 'badge-gray'
}
const sourceTitle = (row: OpenAICacheUserRow) =>
  row.config ? `${row.config.lo}% ~ ${row.config.hi}%` : t('admin.openaiCacheUser.source.accountHint')

const coverageTitle = (row: OpenAICacheUserRow) =>
  t('admin.openaiCacheUser.table.coverage', { pct: fmtPct(row.coverage_rate) })

const verdictBadge = (v: OpenAICacheVerdict) =>
  v === 'ok' ? 'badge-success' : v === 'ceiling_short' ? 'badge-gray' : 'badge-warning'
const verdictTitle = (v: OpenAICacheVerdict) => (v ? t(`admin.openaiCacheUser.verdictHint.${v}`) : '')
const emptyVerdict = (row: OpenAICacheUserRow) => {
  if (row.source === 'user' && !row.active) return t('admin.openaiCacheUser.verdictEmpty.off')
  if (!row.active) return t('admin.openaiCacheUser.verdictEmpty.account')
  return t('admin.openaiCacheUser.verdictEmpty.noInput')
}

const load = async () => {
  loading.value = true
  try {
    const res = await openaiCacheAPI.listUsers({ start_date: props.startDate, end_date: props.endDate })
    users.value = res.users
    global.value = res.global
    timezoneName.value = res.timezone || 'Asia/Shanghai'
    if (selected.value) {
      selected.value = res.users.find((u) => u.user_id === selected.value?.user_id) ?? null
    }
  } catch (error) {
    console.error('Failed to load cache users:', error)
    appStore.showError(t('admin.openaiCache.loadFailed'))
  } finally {
    loading.value = false
  }
}

const loadTrend = async () => {
  if (!selected.value) return
  trendLoading.value = true
  try {
    const res = await openaiCacheAPI.listUserTrend({
      user_id: selected.value.user_id,
      start_date: props.startDate,
      end_date: props.endDate,
      granularity: props.granularity
    })
    trend.value = res.rows
  } catch (error) {
    console.error('Failed to load user cache trend:', error)
    appStore.showError(t('admin.openaiCache.loadFailed'))
  } finally {
    trendLoading.value = false
  }
}

const select = (row: OpenAICacheUserRow) => {
  selected.value = row
  void loadTrend()
}

const openModal = (row: OpenAICacheUserRow) => {
  modalUser.value = row
  modalOpen.value = true
}

const onSaved = async () => {
  modalOpen.value = false
  await load()
  emit('changed')
}

const chartData = computed(() => ({
  labels: trend.value.map((d) => d.date),
  datasets: [
    { label: t('admin.openaiCache.table.displayRate'), data: trend.value.map((d) => d.all.display_rate), borderColor: '#10b981', tension: 0.2, spanGaps: true },
    { label: t('admin.openaiCache.table.rawRate'), data: trend.value.map((d) => d.all.raw_rate), borderColor: '#6366f1', tension: 0.2, spanGaps: true },
    { label: t('admin.openaiCacheUser.table.reachable'), data: trend.value.map((d) => d.reachable_rate), borderColor: '#f59e0b', borderDash: [4, 4], tension: 0.2, spanGaps: true }
  ]
}))

const chartOptions = {
  responsive: true,
  maintainAspectRatio: false,
  interaction: { mode: 'index' as const, intersect: false },
  scales: { y: { min: 0, max: 100, ticks: { callback: (v: number | string) => `${v}%` } } }
}

watch(() => [props.startDate, props.endDate] as const, () => {
  void load()
  void loadTrend()
})
watch(() => props.granularity, () => void loadTrend())

defineExpose({ load, showOverrides: () => (onlyOverrides.value = true) })
onMounted(load)
</script>
