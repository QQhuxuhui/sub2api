<template>
  <div class="space-y-4">
    <div class="flex flex-wrap items-center gap-3">
      <select v-model="status" class="input w-40" @change="reload(1)">
        <option value="">{{ t('iqProbe.runs.allStatus') }}</option>
        <option v-for="s in statuses" :key="s" :value="s">{{ t(`iqProbe.status.${s}`) }}</option>
      </select>
      <select v-model="monitorId" class="input w-72" @change="reload(1)">
        <option :value="0">{{ t('iqProbe.runs.allMonitors') }}</option>
        <option v-for="m in monitors" :key="m.id" :value="m.id">{{ m.account_name }} / {{ m.model }} / {{ m.set_name }}</option>
      </select>
      <button type="button" class="btn btn-secondary text-sm" @click="reload(page)">{{ t('common.refresh') }}</button>
    </div>

    <div class="card overflow-x-auto">
      <table class="min-w-full text-sm">
        <thead class="bg-gray-50 text-left text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">
          <tr>
            <th class="px-3 py-2">{{ t('iqProbe.runs.time') }}</th>
            <th class="px-3 py-2">{{ t('iqProbe.columns.account') }}</th>
            <th class="px-3 py-2">{{ t('iqProbe.columns.model') }}</th>
            <th class="px-3 py-2">{{ t('iqProbe.columns.set') }}</th>
            <th class="px-3 py-2">{{ t('iqProbe.columns.score') }}</th>
            <th class="px-3 py-2">{{ t('iqProbe.runs.breakdown') }}</th>
            <th class="px-3 py-2 text-right">{{ t('iqProbe.detail.tokens') }}</th>
            <th class="px-3 py-2 text-right">{{ t('iqProbe.detail.cost') }}</th>
            <th class="px-3 py-2">{{ t('iqProbe.runs.message') }}</th>
          </tr>
        </thead>
        <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
          <tr v-if="!loading && runs.length === 0">
            <td colspan="9" class="px-3 py-6 text-center text-gray-500">{{ t('iqProbe.runs.empty') }}</td>
          </tr>
          <tr
            v-for="r in runs"
            :key="r.id"
            :data-test="`iq-run-row-${r.id}`"
            class="cursor-pointer hover:bg-gray-50 dark:hover:bg-dark-800"
            :title="t('iqProbe.runs.clickHint')"
            @click="openRun(r)"
          >
            <td class="whitespace-nowrap px-3 py-2">
              {{ formatDateTime(r.started_at) }}
              <span v-if="r.trigger === 'manual'" class="ml-1 text-xs text-gray-400">{{ t('iqProbe.detail.manual') }}</span>
            </td>
            <td class="px-3 py-2">{{ r.account_name || `#${r.account_id}` }}</td>
            <td class="px-3 py-2 font-mono text-xs">{{ r.model }}</td>
            <td class="px-3 py-2">{{ r.set_name }}</td>
            <td class="px-3 py-2"><IQProbeScoreBadge :status="r.status" :score="r.score" :message="r.message" /></td>
            <td class="whitespace-nowrap px-3 py-2 text-xs text-gray-600 dark:text-gray-300">
              {{ t('iqProbe.runs.breakdownValue', { c: r.correct_count, w: r.wrong_count, f: r.format_error_count, e: r.error_count }) }}
            </td>
            <td class="whitespace-nowrap px-3 py-2 text-right text-xs">{{ r.input_tokens }} / {{ r.output_tokens }}</td>
            <td class="whitespace-nowrap px-3 py-2 text-right text-xs">${{ r.cost_usd.toFixed(4) }}</td>
            <td class="max-w-xs truncate px-3 py-2 text-xs text-gray-500" :title="r.message">{{ r.message }}</td>
          </tr>
        </tbody>
      </table>
    </div>
    <Pagination v-if="total > pageSize" :page="page" :total="total" :page-size="pageSize" :show-page-size-selector="false" @update:page="reload" />
    <IQProbeRunDialog :show="selectedRun !== null" :run="selectedRun" @close="selectedRun = null" />
  </div>
</template>

<script setup lang="ts">
import { onMounted, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import Pagination from '@/components/common/Pagination.vue'
import IQProbeScoreBadge from './IQProbeScoreBadge.vue'
import IQProbeRunDialog from './IQProbeRunDialog.vue'
import { iqProbeAPI } from '@/api/admin/iqProbe'
import type { IQProbeMonitor, IQProbeRun, IQProbeRunStatus } from '@/api/admin/iqProbe'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'

defineProps<{ monitors: IQProbeMonitor[] }>()

const { t } = useI18n()
const appStore = useAppStore()
const statuses: IQProbeRunStatus[] = ['scored', 'invalid', 'skipped', 'running']
const pageSize = 50

const runs = ref<IQProbeRun[]>([])
const total = ref(0)
const page = ref(1)
const status = ref('')
const monitorId = ref(0)
const loading = ref(false)
const selectedRun = ref<IQProbeRun | null>(null)

function openRun(run: IQProbeRun) {
  selectedRun.value = run
}

async function reload(p: number) {
  page.value = p
  loading.value = true
  try {
    const res = (
      await iqProbeAPI.listRuns({
        page: p,
        page_size: pageSize,
        status: status.value || undefined,
        monitor_id: monitorId.value || undefined,
      })
    ).data
    runs.value = res.items ?? []
    total.value = res.total ?? 0
  } catch (err) {
    appStore.showError(extractI18nErrorMessage(err, t, 'iqProbe.errors', t('common.error')))
  } finally {
    loading.value = false
  }
}

onMounted(() => void reload(1))

defineExpose({ reload: () => reload(page.value) })
</script>
