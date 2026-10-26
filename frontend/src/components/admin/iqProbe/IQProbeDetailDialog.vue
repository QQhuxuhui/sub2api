<template>
  <BaseDialog :show="show" :title="monitor ? t('iqProbe.detail.title', { name: monitor.account_name, model: monitor.model }) : ''" width="full" @close="emit('close')">
    <div v-if="loading" class="flex justify-center py-12">
      <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
    </div>
    <div v-else-if="monitor" class="space-y-5">
      <div class="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm text-gray-600 dark:text-gray-300">
        <span>{{ t('iqProbe.columns.set') }}：{{ monitor.set_name }}</span>
        <span>{{ t('iqProbe.columns.interval') }}：{{ t('iqProbe.minutes', { n: monitor.interval_minutes }) }}</span>
        <span>{{ t('iqProbe.monitorDialog.effort') }}：{{ monitor.reasoning_effort || t('iqProbe.monitorDialog.effortDefault') }}</span>
        <span v-if="monitor.alert_enabled">{{ t('iqProbe.monitorDialog.threshold') }}：{{ Math.round(monitor.alert_threshold * 100) }}%</span>
      </div>

      <div class="card p-4">
        <p class="mb-2 text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('iqProbe.detail.trend', { n: trendPoints.length }) }}</p>
        <p v-if="trendPoints.length === 0" class="text-sm text-gray-500">{{ t('iqProbe.detail.noRuns') }}</p>
        <IQProbeSparkline v-else :points="trendPoints" :threshold="monitor.alert_enabled ? monitor.alert_threshold : undefined" :width="900" :height="80" class="w-full" />
      </div>

      <div class="card overflow-x-auto">
        <table class="min-w-full text-sm">
          <thead class="bg-gray-50 text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">
            <tr>
              <th class="sticky left-0 z-10 bg-gray-50 px-3 py-2 text-left dark:bg-dark-800">{{ t('iqProbe.detail.round') }}</th>
              <th class="px-3 py-2 text-right">{{ t('iqProbe.columns.score') }}</th>
              <th v-for="q in questionColumns" :key="q.id" class="max-w-[8rem] px-2 py-2 text-center font-normal" :title="q.title">
                <span class="block truncate">{{ q.title || `#${q.id}` }}</span>
              </th>
              <th class="px-3 py-2 text-right">{{ t('iqProbe.detail.cost') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-if="runs.length === 0">
              <td :colspan="questionColumns.length + 3" class="px-3 py-6 text-center text-gray-500">{{ t('iqProbe.detail.noRuns') }}</td>
            </tr>
            <tr v-for="run in runs" :key="run.id">
              <td class="sticky left-0 z-10 whitespace-nowrap bg-white px-3 py-2 text-gray-700 dark:bg-dark-900 dark:text-gray-300">
                {{ formatDateTime(run.started_at) }}
                <span v-if="run.trigger === 'manual'" class="ml-1 text-xs text-gray-400">{{ t('iqProbe.detail.manual') }}</span>
              </td>
              <td class="whitespace-nowrap px-3 py-2 text-right">
                <IQProbeScoreBadge :status="run.status" :score="run.score" :message="run.message" />
              </td>
              <td v-for="q in questionColumns" :key="q.id" class="px-2 py-2 text-center">
                <button
                  v-if="cell(run.id, q.id)"
                  type="button"
                  :class="['inline-flex h-6 w-6 items-center justify-center rounded text-xs font-semibold', verdictClass(cell(run.id, q.id)!.verdict)]"
                  :title="cellTitle(cell(run.id, q.id)!)"
                  @click="openAnswer(cell(run.id, q.id)!.id)"
                >
                  {{ verdictSymbol(cell(run.id, q.id)!.verdict) }}
                </button>
                <span v-else class="text-gray-300 dark:text-dark-600">·</span>
              </td>
              <td class="whitespace-nowrap px-3 py-2 text-right text-xs text-gray-500">${{ run.cost_usd.toFixed(4) }}</td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('iqProbe.detail.legend') }}</p>

      <!-- Answer -->
      <div v-if="answer" class="card space-y-3 p-4" data-test="iq-answer-panel">
        <div class="flex items-start justify-between gap-4">
          <div>
            <p class="font-medium text-gray-900 dark:text-white">{{ answer.question_title || `#${answer.question_id}` }}</p>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">
              {{ t(`iqProbe.verdict.${answer.verdict}`) }} · {{ t('iqProbe.detail.expected') }} {{ answer.expected_answer }} ·
              {{ t('iqProbe.detail.extracted') }} {{ answer.extracted_answer || '—' }} · {{ t(`iqProbe.judge.${answer.judge_type}`) }}
            </p>
          </div>
          <button type="button" class="text-sm text-gray-500 hover:text-gray-700" @click="answer = null">{{ t('common.close') }}</button>
        </div>
        <div class="grid gap-2 text-xs text-gray-600 dark:text-gray-300 md:grid-cols-4">
          <span>{{ t('iqProbe.detail.upstreamModel') }}：{{ answer.upstream_model || '—' }}</span>
          <span :class="modelMismatch(answer) ? 'font-semibold text-amber-600 dark:text-amber-400' : ''">
            {{ t('iqProbe.detail.responseModel') }}：{{ answer.response_model || '—' }}
          </span>
          <span>{{ t('iqProbe.detail.latency') }}：{{ answer.latency_ms }}ms（{{ t('iqProbe.detail.firstToken') }} {{ answer.first_token_ms }}ms）</span>
          <span>
            {{ t('iqProbe.detail.tokens') }}：{{ answer.input_tokens }} / {{ answer.output_tokens }}
            <template v-if="answer.reasoning_tokens">（{{ t('iqProbe.detail.reasoning') }} {{ answer.reasoning_tokens }}）</template>
          </span>
        </div>
        <p v-if="answer.error_message" class="whitespace-pre-wrap rounded bg-red-50 p-2 text-xs text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ answer.error_message }}</p>
        <pre class="max-h-96 overflow-auto whitespace-pre-wrap rounded bg-gray-50 p-3 text-xs text-gray-800 dark:bg-dark-800 dark:text-gray-200">{{ answer.response_text || t('iqProbe.detail.emptyReply') }}</pre>
      </div>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import IQProbeSparkline from './IQProbeSparkline.vue'
import IQProbeScoreBadge from './IQProbeScoreBadge.vue'
import { iqProbeAPI } from '@/api/admin/iqProbe'
import type { IQProbeAnswer, IQProbeMonitor, IQProbeRun, IQProbeScorePoint, IQProbeVerdict } from '@/api/admin/iqProbe'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{ show: boolean; monitor: IQProbeMonitor | null }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(false)
const runs = ref<IQProbeRun[]>([])
const answers = ref<IQProbeAnswer[]>([])
const answer = ref<IQProbeAnswer | null>(null)

watch(
  () => [props.show, props.monitor?.id],
  () => {
    if (props.show && props.monitor) void load(props.monitor.id)
  }
)

async function load(id: number) {
  loading.value = true
  answer.value = null
  try {
    const res = (await iqProbeAPI.matrix(id, 30)).data
    runs.value = res.runs ?? []
    answers.value = res.answers ?? []
  } catch (err) {
    appStore.showError(extractI18nErrorMessage(err, t, 'iqProbe.errors', t('common.error')))
  } finally {
    loading.value = false
  }
}

// Columns follow the questions seen in these rounds, so deleted or edited
// questions still show for the rounds that asked them.
const questionColumns = computed(() => {
  const seen = new Map<number, { id: number; title: string }>()
  for (const a of answers.value) {
    if (!seen.has(a.question_id)) seen.set(a.question_id, { id: a.question_id, title: a.question_title })
  }
  return [...seen.values()].sort((a, b) => a.id - b.id)
})

const cells = computed(() => {
  const map = new Map<string, IQProbeAnswer>()
  for (const a of answers.value) map.set(`${a.run_id}:${a.question_id}`, a)
  return map
})
const cell = (runId: number, questionId: number) => cells.value.get(`${runId}:${questionId}`)

const trendPoints = computed<IQProbeScorePoint[]>(() =>
  runs.value
    .filter((r) => r.status === 'scored' || r.status === 'invalid')
    .map((r) => ({ at: r.started_at, score: r.score, status: r.status }))
    .reverse()
)

function verdictSymbol(v: IQProbeVerdict) {
  return { correct: '✓', wrong: '✗', format_error: '?', error: '!', unsupported: '–' }[v]
}

function verdictClass(v: IQProbeVerdict) {
  switch (v) {
    case 'correct':
      return 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300'
    case 'wrong':
      return 'bg-red-100 text-red-700 dark:bg-red-900/40 dark:text-red-300'
    case 'format_error':
      return 'bg-amber-100 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300'
    default:
      return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
  }
}

function cellTitle(a: IQProbeAnswer) {
  return `${t(`iqProbe.verdict.${a.verdict}`)} · ${a.extracted_answer || a.error_message || ''}`.slice(0, 200)
}

function modelMismatch(a: IQProbeAnswer) {
  return !!a.response_model && !!a.upstream_model && !a.response_model.toLowerCase().startsWith(a.upstream_model.toLowerCase())
}

async function openAnswer(id: number) {
  try {
    answer.value = (await iqProbeAPI.getAnswer(id)).data
  } catch (err) {
    appStore.showError(extractI18nErrorMessage(err, t, 'iqProbe.errors', t('common.error')))
  }
}
</script>
