<template>
  <BaseDialog :show="show" :title="run ? t('iqProbe.runDialog.title', { name: run.account_name || `#${run.account_id}`, model: run.model }) : ''" width="extra-wide" @close="emit('close')">
    <div v-if="run" class="space-y-4">
      <div class="flex flex-wrap items-center gap-x-6 gap-y-2 text-sm text-gray-600 dark:text-gray-300">
        <IQProbeScoreBadge :status="run.status" :score="run.score" />
        <span>{{ formatDateTime(run.started_at) }}<template v-if="run.trigger === 'manual'">（{{ t('iqProbe.detail.manual') }}）</template></span>
        <span>{{ t('iqProbe.columns.set') }}：{{ run.set_name }}</span>
        <span>{{ t('iqProbe.runs.breakdown') }}：{{ t('iqProbe.runs.breakdownValue', { c: run.correct_count, w: run.wrong_count, f: run.format_error_count, e: run.error_count }) }}</span>
        <span>{{ t('iqProbe.detail.tokens') }}：{{ run.input_tokens }} / {{ run.output_tokens }}</span>
        <span>{{ t('iqProbe.detail.cost') }}：${{ run.cost_usd.toFixed(4) }}</span>
      </div>
      <p v-if="run.message" class="rounded bg-amber-50 p-2 text-sm text-amber-800 dark:bg-amber-900/20 dark:text-amber-300">{{ run.message }}</p>

      <div v-if="loading" class="flex justify-center py-10">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      </div>
      <p v-else-if="answers.length === 0" class="py-6 text-center text-sm text-gray-500" data-test="iq-run-no-answers">
        {{ t('iqProbe.runDialog.noAnswers') }}
      </p>
      <ul v-else class="space-y-3">
        <li v-for="(a, i) in answers" :key="a.id" class="rounded-lg border border-gray-200 p-3 dark:border-dark-600" :data-test="`iq-run-answer-${a.id}`">
          <div class="flex flex-wrap items-start justify-between gap-2">
            <p class="font-medium text-gray-900 dark:text-white">{{ i + 1 }}. {{ a.question_title || `#${a.question_id}` }}</p>
            <span :class="['rounded-full px-2 py-0.5 text-xs font-medium', verdictClass(a.verdict)]">{{ t(`iqProbe.verdict.${a.verdict}`) }}</span>
          </div>
          <div class="mt-2 grid gap-x-4 gap-y-1 text-xs text-gray-600 dark:text-gray-300 md:grid-cols-2">
            <span>{{ t('iqProbe.detail.expected') }}：<code class="font-mono">{{ a.expected_answer }}</code>（{{ t(`iqProbe.judge.${a.judge_type}`) }}）</span>
            <span>{{ t('iqProbe.detail.extracted') }}：<code class="font-mono">{{ a.extracted_answer || '—' }}</code></span>
            <span>
              {{ t('iqProbe.detail.upstreamModel') }}：{{ a.upstream_model || '—' }} ·
              <span :class="modelMismatch(a) ? 'font-semibold text-amber-600 dark:text-amber-400' : ''">{{ t('iqProbe.detail.responseModel') }}：{{ a.response_model || '—' }}</span>
            </span>
            <span>
              {{ t('iqProbe.detail.latency') }} {{ a.latency_ms }}ms（{{ t('iqProbe.detail.firstToken') }} {{ a.first_token_ms }}ms）·
              {{ t('iqProbe.detail.tokens') }} {{ a.input_tokens }} / {{ a.output_tokens }}<template v-if="a.reasoning_tokens">（{{ t('iqProbe.detail.reasoning') }} {{ a.reasoning_tokens }}）</template>
            </span>
          </div>
          <p v-if="a.error_message" class="mt-2 whitespace-pre-wrap break-all rounded bg-red-50 p-2 text-xs text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ a.error_message }}</p>
          <details class="mt-2" :open="a.verdict !== 'correct'">
            <summary class="cursor-pointer text-xs text-primary-600 dark:text-primary-400">{{ t('iqProbe.runDialog.reply') }}</summary>
            <pre class="mt-1 max-h-80 overflow-auto whitespace-pre-wrap break-words rounded bg-gray-50 p-3 text-xs text-gray-800 dark:bg-dark-800 dark:text-gray-200">{{ a.response_text || t('iqProbe.detail.emptyReply') }}</pre>
          </details>
        </li>
      </ul>
    </div>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import IQProbeScoreBadge from './IQProbeScoreBadge.vue'
import { iqProbeAPI } from '@/api/admin/iqProbe'
import type { IQProbeAnswer, IQProbeRun, IQProbeVerdict } from '@/api/admin/iqProbe'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'
import { formatDateTime } from '@/utils/format'

const props = defineProps<{ show: boolean; run: IQProbeRun | null }>()
const emit = defineEmits<{ (e: 'close'): void }>()

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(false)
const answers = ref<IQProbeAnswer[]>([])
let requestSeq = 0

watch(
  () => [props.show, props.run?.id] as const,
  ([show, id]) => {
    if (show && id) void load(id)
  }
)

async function load(runId: number) {
  const seq = ++requestSeq
  loading.value = true
  answers.value = []
  try {
    const data = (await iqProbeAPI.listRunAnswers(runId)).data ?? []
    if (seq === requestSeq) answers.value = data
  } catch (err) {
    if (seq === requestSeq) appStore.showError(extractI18nErrorMessage(err, t, 'iqProbe.errors', t('common.error')))
  } finally {
    if (seq === requestSeq) loading.value = false
  }
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
      return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-400'
  }
}

function modelMismatch(a: IQProbeAnswer) {
  return !!a.response_model && !!a.upstream_model && !a.response_model.toLowerCase().startsWith(a.upstream_model.toLowerCase())
}
</script>
