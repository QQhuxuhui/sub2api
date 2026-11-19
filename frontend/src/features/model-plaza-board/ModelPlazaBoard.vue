<template>
  <div class="space-y-5">
    <!-- 页头(独立形态下展示标题;后台形态 AppHeader 已有页面标题) -->
    <div v-if="!embedded">
      <h1 class="text-2xl font-bold tracking-tight text-gray-900 dark:text-white sm:text-3xl">{{ t('modelPlaza.title') }}</h1>
      <p class="mt-1.5 text-sm text-gray-500 dark:text-dark-400">{{ t('modelPlaza.description') }}</p>
    </div>

    <!-- 全局价格说明(管理员配置,Markdown) -->
    <div
      v-if="descriptionHtml"
      class="plaza-description rounded-2xl border border-gray-100 bg-white px-5 py-4 text-sm shadow-card dark:border-dark-700/50 dark:bg-dark-800/50"
      v-html="descriptionHtml"
    ></div>

    <p v-if="!isAuthenticated" class="flex items-center gap-1.5 text-xs text-gray-400 dark:text-dark-500">
      <Icon name="infoCircle" size="xs" class="h-3.5 w-3.5" />
      {{ t('modelPlaza.anonymousHint') }}
    </p>

    <div v-if="loading" class="flex min-h-[240px] items-center justify-center">
      <div class="h-8 w-8 animate-spin rounded-full border-2 border-primary-600/25 border-t-primary-600 dark:border-primary-400/25 dark:border-t-primary-400"></div>
    </div>
    <div
      v-else-if="error"
      class="rounded-2xl border border-red-200 bg-red-50 px-5 py-8 text-center text-sm text-red-600 dark:border-red-500/30 dark:bg-red-500/10 dark:text-red-300"
    >
      {{ t('modelPlaza.loadFailed') }}
    </div>
    <div
      v-else-if="!hasAnyModel"
      class="rounded-2xl border border-dashed border-gray-300 px-5 py-12 text-center text-sm text-gray-500 dark:border-dark-600 dark:text-dark-400"
    >
      {{ t('modelPlaza.empty') }}
    </div>

    <template v-else>
      <!-- 搜索 + 视图切换 -->
      <div class="flex flex-wrap items-center gap-3">
        <label
          class="flex h-10 min-w-0 flex-1 basis-64 items-center gap-2 rounded-lg border border-gray-200 bg-white px-3 focus-within:border-primary-500 dark:border-dark-700 dark:bg-dark-800"
          for="plaza-board-search"
        >
          <Icon name="search" size="sm" class="h-4 w-4 flex-none text-gray-400" />
          <input
            id="plaza-board-search"
            v-model="filter.query"
            type="search"
            autocomplete="off"
            class="h-full min-w-0 flex-1 border-0 bg-transparent p-0 text-sm text-gray-900 placeholder:text-gray-400 focus:outline-none focus:ring-0 dark:text-white"
            :placeholder="t('plazaBoard.searchPlaceholder')"
          />
        </label>
        <div class="inline-flex rounded-lg border border-gray-200 bg-gray-100 p-0.5 dark:border-dark-700 dark:bg-dark-800" role="group" :aria-label="t('plazaBoard.view.label')">
          <button
            v-for="option in VIEWS"
            :key="option"
            type="button"
            :class="segClass(view === option)"
            :aria-pressed="view === option"
            :data-testid="`plaza-view-${option}`"
            @click="setView(option)"
          >
            {{ t(`plazaBoard.view.${option}`) }}
          </button>
        </div>
      </div>

      <!-- 状态总览(同时是状态筛选);没有健康数据时不显示 -->
      <div v-if="showHealth" class="grid grid-cols-2 gap-2.5 lg:grid-cols-4" role="group" :aria-label="t('plazaBoard.summary.label')">
        <button
          v-for="item in stateSummary"
          :key="item.key"
          type="button"
          :class="[
            'flex min-w-0 items-center gap-3 rounded-xl border bg-white px-3.5 py-3 text-left dark:bg-dark-800',
            filter.state === item.key
              ? 'border-primary-500 ring-1 ring-primary-500'
              : 'border-gray-200 hover:border-gray-400 dark:border-dark-700 dark:hover:border-dark-500'
          ]"
          :aria-pressed="filter.state === item.key"
          :data-testid="`plaza-state-${item.key}`"
          @click="filter.state = item.key"
        >
          <span v-if="item.key !== 'all'" :class="['h-2.5 w-2.5 flex-none', item.shape]" aria-hidden="true"></span>
          <span class="min-w-0">
            <b class="block text-xl font-bold leading-none tabular-nums text-gray-900 dark:text-white">{{ item.count }}</b>
            <span class="mt-1 block text-xs text-gray-500 dark:text-dark-400">{{ item.label }}</span>
          </span>
        </button>
      </div>

      <div class="grid items-start gap-4 lg:grid-cols-[288px_minmax(0,1fr)] lg:gap-6">
        <BoardGroupRail v-model="filter.group" :items="groupItems" :total="totalModels" :show-health="healthAvailable" />

        <main class="min-w-0">
          <div class="mb-3 flex flex-wrap items-center gap-2">
            <div class="flex min-w-0 flex-1 flex-wrap gap-1.5" role="group" :aria-label="t('plazaBoard.filters.vendor')">
              <button
                v-for="vendor in vendorOptions"
                :key="vendor.key"
                type="button"
                :class="chipClass(filter.vendor === vendor.key)"
                :aria-pressed="filter.vendor === vendor.key"
                :data-testid="`plaza-vendor-${vendor.key}`"
                @click="filter.vendor = vendor.key"
              >
                {{ vendor.label }}
                <span class="ml-1 text-xs font-normal tabular-nums text-gray-400 dark:text-dark-500">{{ vendor.count }}</span>
              </button>
            </div>
            <div class="inline-flex flex-none rounded-lg border border-gray-200 bg-gray-100 p-0.5 dark:border-dark-700 dark:bg-dark-800" role="group" :aria-label="t('plazaBoard.filters.billing')">
              <button
                v-for="option in BILLINGS"
                :key="option"
                type="button"
                :class="segClass(filter.billing === option)"
                :aria-pressed="filter.billing === option"
                :data-testid="`plaza-billing-${option}`"
                @click="filter.billing = option"
              >
                {{ t(`plazaBoard.filters.${option}`) }}
              </button>
            </div>
          </div>
          <div class="mb-3 flex flex-wrap items-center gap-3 text-[13px] text-gray-500 dark:text-dark-400">
            <span class="tabular-nums">{{ t('plazaBoard.count', { models: visibleModels.length, lines: visibleLineCount }) }}</span>
            <span class="flex-1"></span>
            <label for="plaza-board-sort">{{ t('plazaBoard.sort.label') }}</label>
            <select
              id="plaza-board-sort"
              v-model="filter.sort"
              class="rounded-lg border border-gray-200 bg-white py-1.5 pl-2.5 pr-8 text-[13px] text-gray-900 focus:border-primary-500 focus:outline-none focus:ring-0 dark:border-dark-700 dark:bg-dark-800 dark:text-white"
            >
              <option v-for="option in sortOptions" :key="option" :value="option">{{ t(`plazaBoard.sort.${option}`) }}</option>
            </select>
          </div>

          <div
            v-if="visibleModels.length === 0"
            class="rounded-xl border border-dashed border-gray-300 px-5 py-12 text-center text-sm text-gray-500 dark:border-dark-600 dark:text-dark-400"
          >
            {{ t('plazaBoard.emptyFiltered') }}
          </div>
          <BoardModelList
            v-else-if="view === 'list'"
            :models="visibleModels"
            :show-health="showHealth"
            @open="openModel"
          />
          <div v-else class="grid gap-3.5 [grid-template-columns:repeat(auto-fill,minmax(min(100%,290px),1fr))]">
            <BoardModelCard
              v-for="model in visibleModels"
              :key="model.name"
              :model="model"
              :show-health="showHealth"
                @open="openModel"
            />
          </div>
        </main>
      </div>
    </template>

    <BoardLineDrawer :model="openedModel" :show-health="showHealth" @close="openedName = null" />
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useRoute, useRouter } from 'vue-router'
import { marked } from 'marked'
import DOMPurify from 'dompurify'
import Icon from '@/components/icons/Icon.vue'
import type { ModelPlazaResponse } from '@/api/modelPlaza'
import { useAuthStore } from '@/stores/auth'
import BoardGroupRail, { type GroupRailItem } from './BoardGroupRail.vue'
import BoardLineDrawer from './BoardLineDrawer.vue'
import BoardModelCard from './BoardModelCard.vue'
import BoardModelList from './BoardModelList.vue'
import {
  OTHER_VENDOR_KEY,
  buildBoardModels,
  effectiveRate,
  filterAndSortModels,
  formatRate,
  groupStates,
  hasHealthData,
  hasRank,
  scopeGroups,
  type BoardFilter,
  type BoardSort
} from './plazaBoard'
import { usePlazaHealth } from './usePlazaHealth'

const props = defineProps<{
  response: ModelPlazaResponse | null
  loading: boolean
  error?: boolean
  /** 后台内嵌形态(AppLayout 内):隐藏页头。 */
  embedded?: boolean
}>()

type ViewMode = 'card' | 'list'
const VIEWS: ViewMode[] = ['card', 'list']
const BILLINGS: Array<BoardFilter['billing']> = ['all', 'token', 'request']

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const authStore = useAuthStore()
const isAuthenticated = computed(() => authStore.isAuthenticated)
const { health } = usePlazaHealth()

const descriptionHtml = computed(() => {
  const md = props.response?.description?.trim()
  if (!md) return ''
  return DOMPurify.sanitize(marked.parse(md) as string)
})

const filter = reactive<BoardFilter>({ query: '', group: 'all', vendor: 'all', billing: 'all', state: 'all', sort: 'name' })

/** 广场是否有任何模型（不受分组筛选影响，决定是否进入整体空态）。 */
const hasAnyModel = computed(() => (props.response?.groups ?? []).some((g) => (g.models ?? []).length > 0))

/** 有模型的分组；零模型分组不进分组栏。 */
const listedGroups = computed(() => (props.response?.groups ?? []).filter((g) => (g.models ?? []).length > 0))
const totalModels = computed(() => new Set(listedGroups.value.flatMap((g) => g.models.map((m) => m.name))).size)
const healthAvailable = computed(() => health.value?.available === true)
const groupItems = computed<GroupRailItem[]>(() => {
  const states = groupStates(listedGroups.value, health.value)
  return listedGroups.value.map((group) => ({
    group,
    count: new Set(group.models.map((m) => m.name)).size,
    rate: formatRate(effectiveRate(group)),
    state: states.get(group.id) ?? 'unknown'
  }))
})
// 选中的分组在刷新后不存在时回到全部。
watch(groupItems, (items) => {
  if (filter.group !== 'all' && !items.some((item) => item.group.id === filter.group)) filter.group = 'all'
})

/** 当前分组范围内的模型；选中分组后线路、价格、状态与各项统计都只来自该分组。 */
const allModels = computed(() => buildBoardModels(scopeGroups(props.response?.groups, filter.group), health.value))
const showHealth = computed(() => hasHealthData(allModels.value))
const rankAvailable = computed(() => hasRank(allModels.value))

const sortOptions = computed<BoardSort[]>(() => {
  const options: BoardSort[] = ['name', 'price']
  if (rankAvailable.value) options.unshift('rank')
  if (showHealth.value) options.push('stable', 'fast')
  return options
})

// 有热度名次时默认按「最多人用」；选中的排序或状态筛选失效时回落。
let sortTouched = false
watch(
  () => filter.sort,
  () => {
    sortTouched = true
  },
  { flush: 'sync' }
)
watch(
  sortOptions,
  (options) => {
    if (!options.includes(filter.sort)) {
      filter.sort = options[0]
      sortTouched = false
    } else if (!sortTouched && options[0] === 'rank' && filter.sort !== 'rank') {
      filter.sort = 'rank'
      sortTouched = false
    }
  },
  { immediate: true }
)
watch(showHealth, (available) => {
  if (!available) filter.state = 'all'
})

const view = computed<ViewMode>(() => (route.query.view === 'list' ? 'list' : 'card'))
function setView(next: ViewMode) {
  if (next === view.value) return
  const query = { ...route.query }
  if (next === 'list') query.view = 'list'
  else delete query.view
  void router.replace({ query })
}

/** 搜索范围内的模型（状态总览与厂商计数都基于它）。 */
const searchScoped = computed(() =>
  filterAndSortModels(allModels.value, { ...filter, vendor: 'all', billing: 'all', state: 'all' })
)

const stateSummary = computed(() => {
  const count = (state: string) => searchScoped.value.filter((m) => state === 'all' || m.state === state).length
  return [
    { key: 'all' as const, label: t('plazaBoard.summary.all'), count: count('all'), shape: '' },
    { key: 'healthy' as const, label: t('plazaBoard.state.healthy'), count: count('healthy'), shape: 'rounded-full bg-emerald-500' },
    { key: 'warning' as const, label: t('plazaBoard.state.warning'), count: count('warning'), shape: 'rotate-45 rounded-[1px] bg-amber-500' },
    { key: 'critical' as const, label: t('plazaBoard.state.critical'), count: count('critical'), shape: 'rounded-[1px] bg-red-500' }
  ]
})

const vendorOptions = computed(() => {
  const counts = new Map<string, { label: string; count: number }>()
  for (const model of allModels.value) {
    const entry = counts.get(model.vendor.key)
    if (entry) entry.count += 1
    else {
      counts.set(model.vendor.key, {
        label: model.vendor.key === OTHER_VENDOR_KEY ? t('plazaBoard.filters.otherVendor') : model.vendor.label,
        count: 1
      })
    }
  }
  const vendors = [...counts.entries()]
    .map(([key, value]) => ({ key, ...value }))
    .sort((a, b) => (a.key === OTHER_VENDOR_KEY ? 1 : 0) - (b.key === OTHER_VENDOR_KEY ? 1 : 0) || b.count - a.count)
  return [{ key: 'all', label: t('plazaBoard.filters.all'), count: allModels.value.length }, ...vendors]
})

const visibleModels = computed(() => filterAndSortModels(allModels.value, filter))
const visibleLineCount = computed(() => visibleModels.value.reduce((sum, m) => sum + m.lines.length, 0))

const openedName = ref<string | null>(null)
const openedModel = computed(() => allModels.value.find((m) => m.name === openedName.value) ?? null)
function openModel(name: string) {
  openedName.value = name
}

function segClass(active: boolean): string {
  return [
    'rounded-md px-3 py-1 text-sm focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500',
    active
      ? 'bg-white text-gray-900 shadow-sm dark:bg-dark-600 dark:text-white'
      : 'text-gray-500 hover:text-gray-900 dark:text-dark-400 dark:hover:text-white'
  ].join(' ')
}

function chipClass(active: boolean): string {
  return [
    'rounded-full border px-3 py-1 text-[13px] focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500',
    active
      ? 'border-primary-500 bg-primary-50 font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
      : 'border-gray-200 bg-white text-gray-600 hover:border-gray-400 dark:border-dark-700 dark:bg-dark-800 dark:text-dark-300 dark:hover:border-dark-500'
  ].join(' ')
}
</script>
