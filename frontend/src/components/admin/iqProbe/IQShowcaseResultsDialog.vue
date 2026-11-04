<template>
  <BaseDialog :show="show" :title="item ? t('iqShowcase.admin.resultsTitle', { title: item.title }) : ''" width="extra-wide" @close="emit('close')">
    <div class="space-y-4">
      <div class="flex gap-2">
        <button
          v-for="f in filters"
          :key="f.key"
          type="button"
          :data-test="`iq-showcase-results-${f.key}`"
          :class="['rounded-lg px-3 py-1 text-sm', filter === f.key ? 'bg-primary-600 text-white' : 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300']"
          @click="setFilter(f.key)"
        >
          {{ f.label }}
        </button>
        <button type="button" class="btn btn-secondary ml-auto text-sm" @click="load">{{ t('common.refresh') }}</button>
      </div>

      <div v-if="loading" class="py-8 text-center text-sm text-gray-500">{{ t('common.loading') }}</div>
      <p v-else-if="results.length === 0" class="py-8 text-center text-sm text-gray-500">{{ t('iqShowcase.admin.noResults') }}</p>
      <div v-else class="max-h-[65vh] space-y-2 overflow-y-auto">
        <div
          v-for="r in results"
          :key="r.id"
          :data-test="`iq-showcase-result-${r.id}`"
          class="flex gap-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600"
        >
          <div class="flex h-24 w-24 flex-shrink-0 items-center justify-center rounded bg-white">
            <img v-if="r.success && r.svg" :src="iqShowcaseSVGDataURL(r.svg)" alt="" class="max-h-full max-w-full object-contain" />
            <span v-else class="text-xs text-red-500">{{ t('iqShowcase.admin.failed') }}</span>
          </div>
          <div class="min-w-0 flex-1 space-y-1 text-xs text-gray-600 dark:text-gray-300">
            <div class="flex flex-wrap gap-x-3">
              <span :class="r.success ? 'text-green-600' : 'text-red-600'">{{ r.success ? t('iqShowcase.admin.success') : t('iqShowcase.admin.failed') }}</span>
              <span>{{ formatDateTime(r.created_at) }}</span>
              <span>{{ r.model }}</span>
              <span>rev {{ r.config_rev }}</span>
              <span v-if="r.http_status">HTTP {{ r.http_status }}</span>
              <span>{{ (r.latency_ms / 1000).toFixed(1) }}s</span>
            </div>
            <pre v-if="r.error" class="whitespace-pre-wrap break-all rounded bg-red-50 p-2 text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ r.error }}</pre>
          </div>
        </div>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { iqShowcaseAdminAPI } from '@/api/admin/iqShowcase'
import type { IQShowcaseItem, IQShowcaseResult } from '@/api/admin/iqShowcase'
import { iqShowcaseSVGDataURL } from '@/api/iqShowcase'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'

type Filter = 'all' | 'success' | 'failed'

const props = defineProps<{ show: boolean; item: IQShowcaseItem | null }>()
const emit = defineEmits<{ close: [] }>()

const { t } = useI18n()
const appStore = useAppStore()
const results = ref<IQShowcaseResult[]>([])
const loading = ref(false)
const filter = ref<Filter>('all')

const filters = computed(() => [
  { key: 'all' as Filter, label: t('iqShowcase.admin.filterAll') },
  { key: 'success' as Filter, label: t('iqShowcase.admin.success') },
  { key: 'failed' as Filter, label: t('iqShowcase.admin.failed') },
])

async function load() {
  if (!props.item) return
  loading.value = true
  try {
    const params = filter.value === 'all' ? {} : { success: filter.value === 'success' }
    results.value = (await iqShowcaseAdminAPI.listResults(props.item.id, { ...params, limit: 50 })).data ?? []
  } catch (err) {
    appStore.showError(extractI18nErrorMessage(err, t, 'iqShowcase.errors', t('common.error')))
  } finally {
    loading.value = false
  }
}

function setFilter(f: Filter) {
  filter.value = f
  void load()
}

watch(
  () => props.show,
  (open) => {
    if (!open) return
    filter.value = 'all'
    results.value = []
    void load()
  },
  { immediate: true },
)
</script>
