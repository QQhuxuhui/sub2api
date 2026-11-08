<template>
  <!-- Wide screens: the list beside the editor -->
  <aside class="card hidden h-fit p-3 lg:block">
    <input v-model.trim="search" type="search" class="input mb-2" :placeholder="t('intentRouter.searchGroups')" />
    <p v-if="filtered.length === 0" class="px-2 py-4 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.noGroups') }}</p>
    <ul class="max-h-[calc(100vh-14rem)] space-y-0.5 overflow-y-auto">
      <li v-for="group in filtered" :key="group.id">
        <button
          type="button"
          :data-test="`intent-group-${group.id}`"
          :class="[
            'flex w-full items-center gap-2.5 rounded-lg px-2 py-2 text-left text-sm transition-colors',
            selectedId === group.id
              ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-200'
              : 'text-gray-700 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-dark-700',
          ]"
          @click="emit('select', group.id)"
        >
          <StatusDot :tone="statusOf(group.id).tone" :label="statusOf(group.id).label" />
          <span class="min-w-0 flex-1 truncate font-medium">{{ group.name }}</span>
          <span class="flex-shrink-0 text-xs text-gray-400 dark:text-gray-500">{{ group.platform }}</span>
        </button>
      </li>
    </ul>
    <button type="button" class="mt-3 w-full rounded-lg px-2 py-1.5 text-xs text-gray-500 hover:bg-gray-50 hover:text-gray-700 dark:text-gray-400 dark:hover:bg-dark-700" :disabled="busy" @click="emit('clear-all')">
      {{ t('intentRouter.actions.clearCacheAll') }}
    </button>
  </aside>

  <!-- Narrow screens: one picker, so the rules start right below it -->
  <div ref="picker" class="relative lg:hidden">
    <button
      type="button"
      data-test="intent-group-picker"
      class="input flex w-full items-center gap-2.5 text-left"
      :aria-expanded="open"
      @click="open = !open"
    >
      <template v-if="selected">
        <StatusDot :tone="statusOf(selected.id).tone" :label="statusOf(selected.id).label" />
        <span class="min-w-0 flex-1 truncate font-medium text-gray-900 dark:text-white">{{ selected.name }}</span>
        <span class="text-xs text-gray-400">{{ selected.platform }}</span>
      </template>
      <span v-else class="flex-1 text-gray-500">{{ t('intentRouter.pickGroup') }}</span>
      <svg viewBox="0 0 20 20" fill="currentColor" class="h-4 w-4 text-gray-400" aria-hidden="true"><path fill-rule="evenodd" d="M5.23 7.21a.75.75 0 011.06.02L10 11.06l3.71-3.83a.75.75 0 111.08 1.04l-4.25 4.39a.75.75 0 01-1.08 0L5.21 8.27a.75.75 0 01.02-1.06z" clip-rule="evenodd" /></svg>
    </button>
    <div v-if="open" class="absolute z-30 mt-1 w-full rounded-lg border border-gray-200 bg-white p-2 shadow-lg dark:border-dark-600 dark:bg-dark-800">
      <input v-model.trim="search" type="search" class="input mb-2" :placeholder="t('intentRouter.searchGroups')" />
      <ul class="max-h-[60vh] overflow-y-auto">
        <li v-for="group in filtered" :key="group.id">
          <button
            type="button"
            :data-test="`intent-group-m-${group.id}`"
            :class="['flex w-full items-center gap-2.5 rounded-lg px-2 py-2.5 text-left text-sm', selectedId === group.id ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-200' : 'text-gray-700 dark:text-gray-300']"
            @click="choose(group.id)"
          >
            <StatusDot :tone="statusOf(group.id).tone" :label="statusOf(group.id).label" />
            <span class="min-w-0 flex-1 truncate">{{ group.name }}</span>
            <span class="text-xs text-gray-400">{{ group.platform }}</span>
          </button>
        </li>
      </ul>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import StatusDot from './StatusDot.vue'
import type { GroupOption, Tone } from './ruleUtils'

const props = defineProps<{
  groups: GroupOption[]
  selectedId: number | null
  statusOf: (groupId: number) => { tone: Tone; label: string }
  busy: boolean
}>()
const emit = defineEmits<{ select: [id: number]; 'clear-all': [] }>()
const { t } = useI18n()

const search = ref('')
const open = ref(false)
const picker = ref<HTMLElement | null>(null)
const filtered = computed(() => {
  const q = search.value.toLowerCase()
  return q ? props.groups.filter((g) => g.name.toLowerCase().includes(q) || String(g.id) === q) : props.groups
})
const selected = computed(() => props.groups.find((g) => g.id === props.selectedId) ?? null)

function choose(id: number) {
  open.value = false
  search.value = ''
  emit('select', id)
}
function onDocumentClick(event: MouseEvent) {
  if (open.value && picker.value && !picker.value.contains(event.target as Node)) open.value = false
}
onMounted(() => document.addEventListener('click', onDocumentClick))
onBeforeUnmount(() => document.removeEventListener('click', onDocumentClick))
</script>
