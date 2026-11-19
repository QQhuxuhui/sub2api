<template>
  <div>
    <!-- 窄屏：一个下拉框 -->
    <select
      id="plaza-group-select"
      class="h-10 w-full rounded-lg border border-gray-200 bg-white px-2.5 text-sm text-gray-900 focus:border-primary-500 focus:outline-none focus:ring-0 dark:border-dark-700 dark:bg-dark-800 dark:text-white lg:hidden"
      :aria-label="t('plazaBoard.filters.group')"
      :value="String(modelValue)"
      @change="pick(($event.target as HTMLSelectElement).value)"
    >
      <option value="all">{{ t('plazaBoard.filters.allGroups') }}</option>
      <option v-for="item in items" :key="item.group.id" :value="String(item.group.id)">{{ item.group.name }} ×{{ item.rate }}</option>
    </select>

    <!-- 宽屏：按平台归类的分组栏。分组名原样显示，不拆分不改写。 -->
    <nav
      class="hidden max-h-[calc(100vh-2rem)] overflow-y-auto rounded-xl border border-gray-200 bg-white p-2 dark:border-dark-700 dark:bg-dark-800 lg:sticky lg:top-4 lg:block"
      :aria-label="t('plazaBoard.filters.group')"
    >
      <label
        v-if="items.length > SEARCH_MIN"
        class="mb-1.5 flex h-[34px] items-center gap-1.5 rounded-lg border border-gray-200 bg-gray-50 px-2.5 focus-within:border-primary-500 dark:border-dark-700 dark:bg-dark-900"
        for="plaza-group-find"
      >
        <Icon name="search" size="xs" class="h-3.5 w-3.5 flex-none text-gray-400" />
        <input
          id="plaza-group-find"
          v-model="query"
          type="search"
          autocomplete="off"
          class="h-full min-w-0 flex-1 border-0 bg-transparent p-0 text-[13px] text-gray-900 placeholder:text-gray-400 focus:outline-none focus:ring-0 dark:text-white"
          :placeholder="t('plazaBoard.filters.findGroup')"
        />
      </label>

      <button type="button" :class="itemClass(modelValue === 'all')" :aria-pressed="modelValue === 'all'" data-testid="plaza-group-all" @click="pick('all')">
        <span class="mt-1.5 h-2 w-2 flex-none rounded-full bg-transparent" aria-hidden="true"></span>
        <span class="min-w-0 flex-1">
          <span class="block text-[13.5px] leading-snug">{{ t('plazaBoard.filters.allGroups') }}</span>
          <span class="mt-0.5 block text-[11px] tabular-nums text-gray-400 dark:text-dark-500">{{ t('plazaBoard.filters.models', { count: total }) }}</span>
        </span>
      </button>

      <template v-for="family in families" :key="family.key">
        <h3 class="mx-2 mb-1 mt-2.5 flex items-center gap-1.5 text-[11px] font-semibold tracking-wider text-gray-400 after:h-px after:flex-1 after:bg-gray-200 dark:text-dark-500 dark:after:bg-dark-700">
          {{ family.label }}
        </h3>
        <button
          v-for="item in family.items"
          :key="item.group.id"
          type="button"
          :class="itemClass(modelValue === item.group.id)"
          :aria-pressed="modelValue === item.group.id"
          :data-testid="`plaza-group-${item.group.id}`"
          @click="pick(String(item.group.id))"
        >
          <span :class="['mt-1.5 h-2 w-2 flex-none rounded-full', DOT_CLASS[showHealth ? item.state : 'unknown']]" aria-hidden="true"></span>
          <span class="min-w-0 flex-1">
            <span class="block text-[13.5px] leading-snug [overflow-wrap:anywhere]" data-testid="plaza-rail-name">{{ item.group.name }}</span>
            <span class="mt-0.5 flex items-center gap-1.5 text-[11px] tabular-nums text-gray-400 dark:text-dark-500">
              <span
                :class="[
                  'rounded px-1 font-mono text-[11.5px] leading-[17px]',
                  modelValue === item.group.id
                    ? 'bg-white text-primary-700 dark:bg-dark-800 dark:text-primary-300'
                    : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-dark-300'
                ]"
              >×{{ item.rate }}</span>
              {{ t('plazaBoard.filters.models', { count: item.count }) }}
              <Icon v-if="item.group.is_exclusive" name="lock" size="xs" class="h-3 w-3 flex-none" :title="t('plazaBoard.filters.exclusive')" />
            </span>
          </span>
        </button>
      </template>
    </nav>
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Icon from '@/components/icons/Icon.vue'
import type { ModelPlazaGroup } from '@/api/modelPlaza'
import type { BoardFilter } from './plazaBoard'
import type { PlazaHealthState } from './types'

export interface GroupRailItem {
  group: ModelPlazaGroup
  /** 该分组下的模型数。 */
  count: number
  /** 生效倍率文本。 */
  rate: string
  state: PlazaHealthState
}

const props = defineProps<{
  modelValue: BoardFilter['group']
  items: GroupRailItem[]
  /** 全部分组合计的模型数（去重）。 */
  total: number
  showHealth: boolean
}>()
const emit = defineEmits<{ (e: 'update:modelValue', value: BoardFilter['group']): void }>()
const { t } = useI18n()

/** 分组多于这个数才显示「找分组」。 */
const SEARCH_MIN = 8
const FAMILY_LABELS: Record<string, string> = { anthropic: 'Claude', openai: 'OpenAI', gemini: 'Gemini' }
const FAMILY_ORDER = ['anthropic', 'openai', 'gemini', 'other']
const DOT_CLASS: Record<PlazaHealthState, string> = {
  healthy: 'bg-emerald-500',
  warning: 'bg-amber-500',
  critical: 'bg-red-500',
  unknown: 'bg-gray-300 dark:bg-dark-600'
}

const query = ref('')

/** 归类只决定排列位置，组内保持后端给出的顺序。 */
const families = computed(() => {
  const needle = query.value.trim().toLowerCase()
  const buckets = new Map<string, GroupRailItem[]>()
  for (const item of props.items) {
    if (needle && !item.group.name.toLowerCase().includes(needle)) continue
    const key = item.group.platform in FAMILY_LABELS ? item.group.platform : 'other'
    const list = buckets.get(key)
    if (list) list.push(item)
    else buckets.set(key, [item])
  }
  return FAMILY_ORDER.filter((key) => buckets.has(key)).map((key) => ({
    key,
    label: FAMILY_LABELS[key] ?? t('plazaBoard.filters.otherVendor'),
    items: buckets.get(key)!
  }))
})

function pick(value: string) {
  emit('update:modelValue', value === 'all' ? 'all' : Number(value))
}

function itemClass(active: boolean): string {
  return [
    'flex w-full items-start gap-2.5 rounded-lg px-2 py-1.5 text-left',
    'focus-visible:outline focus-visible:outline-2 focus-visible:outline-primary-500',
    active
      ? 'bg-primary-50 font-semibold text-primary-700 dark:bg-primary-900/30 dark:text-primary-300'
      : 'text-gray-800 hover:bg-gray-100 dark:text-gray-200 dark:hover:bg-dark-700'
  ].join(' ')
}
</script>
