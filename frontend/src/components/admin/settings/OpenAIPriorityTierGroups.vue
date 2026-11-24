<template>
  <div
    class="border-t border-gray-100 pt-5 dark:border-dark-700"
    data-testid="openai-priority-tier"
  >
    <div class="flex items-start justify-between gap-4">
      <div class="min-w-0">
        <label class="text-sm font-medium text-gray-700 dark:text-gray-300">
          {{ t('openaiPriorityTier.title') }}
        </label>
        <p class="mt-0.5 text-xs text-gray-500 dark:text-gray-400">
          {{ t('openaiPriorityTier.description') }}
        </p>
      </div>
      <span
        v-if="saving"
        class="mt-0.5 h-4 w-4 shrink-0 animate-spin rounded-full border-2 border-primary-500 border-t-transparent"
        role="status"
        :aria-label="t('openaiPriorityTier.title')"
      />
    </div>

    <div v-if="loading" class="mt-3 flex flex-wrap gap-2" aria-hidden="true">
      <span
        v-for="n in 3"
        :key="n"
        class="h-7 w-24 animate-pulse rounded-full bg-gray-100 dark:bg-dark-700"
      />
    </div>

    <div
      v-else-if="loadError"
      class="mt-3 flex items-center gap-2 text-xs text-red-600 dark:text-red-400"
      role="alert"
    >
      <span>{{ t('openaiPriorityTier.loadFailed') }}</span>
      <button type="button" class="font-medium underline" data-test="retry" @click="load">
        {{ t('openaiPriorityTier.retry') }}
      </button>
    </div>

    <p v-else-if="groups.length === 0" class="mt-3 text-xs text-gray-400">
      {{ t('openaiPriorityTier.empty') }}
    </p>

    <div v-else class="mt-3 flex flex-wrap gap-2">
      <button
        v-for="group in groups"
        :key="group.id"
        type="button"
        :data-test="`tier-group-${group.id}`"
        :aria-pressed="isSelected(group.id)"
        :disabled="saving"
        class="inline-flex items-center gap-1.5 rounded-full border px-3 py-1 text-xs font-medium transition-colors disabled:cursor-not-allowed disabled:opacity-60"
        :class="
          isSelected(group.id)
            ? 'border-primary-500 bg-primary-50 text-primary-700 dark:bg-primary-900/20 dark:text-primary-300'
            : 'border-gray-200 text-gray-600 hover:border-gray-300 dark:border-dark-600 dark:text-gray-300 dark:hover:border-dark-500'
        "
        @click="toggle(group.id)"
      >
        <span
          class="h-1.5 w-1.5 rounded-full"
          :class="isSelected(group.id) ? 'bg-primary-500' : 'bg-gray-300 dark:bg-dark-500'"
        />
        {{ group.name }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { getAll as getAllGroups } from '@/api/admin/groups'
import * as priorityTierAPI from '@/api/admin/openaiPriorityTier'
import { useAppStore } from '@/stores'

interface GroupOption {
  id: number
  name: string
}

const { t } = useI18n()
const appStore = useAppStore()

const groups = ref<GroupOption[]>([])
// 最后一次服务端确认的列表；保存失败时恢复到它。
const confirmed = ref<number[]>([])
const selected = ref<number[]>([])
const loading = ref(true)
const loadError = ref(false)
const saving = ref(false)

const isSelected = (id: number) => selected.value.includes(id)

async function load() {
  loading.value = true
  loadError.value = false
  try {
    const [groupList, settings] = await Promise.all([
      getAllGroups('openai'),
      priorityTierAPI.get(),
    ])
    groups.value = (groupList || []).map((g) => ({ id: g.id, name: g.name }))
    confirmed.value = [...(settings?.group_ids || [])]
    selected.value = [...confirmed.value]
  } catch {
    loadError.value = true
  } finally {
    loading.value = false
  }
}

async function toggle(id: number) {
  if (saving.value) return
  const next = isSelected(id)
    ? selected.value.filter((g) => g !== id)
    : [...selected.value, id]
  selected.value = next
  saving.value = true
  try {
    const saved = await priorityTierAPI.update(next)
    confirmed.value = [...(saved?.group_ids || [])]
    selected.value = [...confirmed.value]
    appStore.showSuccess(t('openaiPriorityTier.saved'))
  } catch {
    selected.value = [...confirmed.value]
    appStore.showError(t('openaiPriorityTier.saveFailed'))
  } finally {
    saving.value = false
  }
}

onMounted(load)
</script>
