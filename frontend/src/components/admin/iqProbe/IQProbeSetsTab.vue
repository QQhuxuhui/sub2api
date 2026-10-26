<template>
  <div class="grid gap-6 lg:grid-cols-[18rem_minmax(0,1fr)]">
    <!-- Sets -->
    <aside class="card h-fit space-y-2 p-3">
      <div class="flex items-center justify-between px-2">
        <p class="text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('iqProbe.sets.title') }}</p>
        <button type="button" data-test="iq-set-add" class="text-sm text-primary-600 hover:underline dark:text-primary-400" @click="openSetDialog(null)">
          {{ t('iqProbe.sets.add') }}
        </button>
      </div>
      <p v-if="sets.length === 0" class="px-2 py-4 text-sm text-gray-500 dark:text-gray-400">{{ t('iqProbe.sets.empty') }}</p>
      <ul class="space-y-1">
        <li v-for="s in sets" :key="s.id">
          <button
            type="button"
            :class="[
              'w-full rounded-lg px-2 py-2 text-left text-sm transition-colors',
              selectedId === s.id
                ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-200'
                : 'text-gray-700 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-dark-700',
            ]"
            @click="select(s.id)"
          >
            <span class="block truncate font-medium">{{ s.name }}</span>
            <span class="block text-xs text-gray-500 dark:text-gray-400">
              {{ t('iqProbe.sets.counts', { questions: s.question_count, enabled: s.enabled_count, monitors: s.monitor_count }) }}
            </span>
          </button>
        </li>
      </ul>
      <label class="btn btn-secondary w-full cursor-pointer text-sm">
        {{ t('iqProbe.sets.import') }}
        <input type="file" accept="application/json,.json" class="hidden" data-test="iq-set-import" @change="importFile" />
      </label>
      <button type="button" data-test="iq-set-import-sample" class="btn btn-secondary w-full text-sm" @click="importData(sampleSet)">
        {{ t('iqProbe.sets.importSample') }}
      </button>
    </aside>

    <!-- Questions -->
    <section v-if="selectedSet" class="min-w-0 space-y-4">
      <div class="card flex flex-wrap items-start justify-between gap-3 p-4">
        <div class="min-w-0">
          <p class="font-medium text-gray-900 dark:text-white">{{ selectedSet.name }}</p>
          <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ selectedSet.description || t('iqProbe.sets.noDescription') }}</p>
        </div>
        <div class="flex flex-wrap gap-2">
          <button type="button" class="btn btn-secondary text-sm" @click="openSetDialog(selectedSet)">{{ t('common.edit') }}</button>
          <button type="button" class="btn btn-secondary text-sm" @click="exportSet">{{ t('iqProbe.sets.export') }}</button>
          <button type="button" class="btn btn-danger text-sm" :disabled="selectedSet.monitor_count > 0" :title="selectedSet.monitor_count > 0 ? t('iqProbe.sets.inUse') : ''" @click="removeSet">
            {{ t('common.delete') }}
          </button>
          <button type="button" data-test="iq-question-add" class="btn btn-primary text-sm" @click="openQuestionDialog(null)">{{ t('iqProbe.questions.add') }}</button>
        </div>
      </div>

      <div class="card overflow-x-auto">
        <table class="min-w-full text-sm">
          <thead class="bg-gray-50 text-left text-xs text-gray-500 dark:bg-dark-800 dark:text-gray-400">
            <tr>
              <th class="px-3 py-2">{{ t('iqProbe.questions.title') }}</th>
              <th class="px-3 py-2">{{ t('iqProbe.questions.expected') }}</th>
              <th class="px-3 py-2">{{ t('iqProbe.questions.judge') }}</th>
              <th class="px-3 py-2 text-right">{{ t('iqProbe.questions.weight') }}</th>
              <th class="px-3 py-2">{{ t('iqProbe.questions.enabled') }}</th>
              <th class="px-3 py-2 text-right">{{ t('common.actions') }}</th>
            </tr>
          </thead>
          <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
            <tr v-if="questions.length === 0">
              <td colspan="6" class="px-3 py-6 text-center text-gray-500">{{ t('iqProbe.questions.empty') }}</td>
            </tr>
            <tr v-for="q in questions" :key="q.id" :class="q.enabled ? '' : 'opacity-50'">
              <td class="max-w-md px-3 py-2">
                <p class="truncate font-medium text-gray-800 dark:text-gray-200">{{ q.title || q.prompt.slice(0, 40) }}</p>
                <p class="truncate text-xs text-gray-500 dark:text-gray-400">{{ q.prompt }}</p>
              </td>
              <td class="px-3 py-2 font-mono text-xs">{{ q.expected_answer }}</td>
              <td class="whitespace-nowrap px-3 py-2 text-xs">
                {{ t(`iqProbe.judge.${q.judge_type}`) }}<template v-if="q.judge_type === 'numeric' && q.numeric_tolerance"> ±{{ q.numeric_tolerance }}</template>
              </td>
              <td class="px-3 py-2 text-right">{{ q.weight }}</td>
              <td class="px-3 py-2"><Toggle :model-value="q.enabled" @update:model-value="(v: boolean) => toggleQuestion(q, v)" /></td>
              <td class="whitespace-nowrap px-3 py-2 text-right">
                <button type="button" class="text-primary-600 hover:underline dark:text-primary-400" @click="openQuestionDialog(q)">{{ t('common.edit') }}</button>
                <button type="button" class="ml-3 text-red-600 hover:underline dark:text-red-400" @click="removeQuestion(q)">{{ t('common.delete') }}</button>
              </td>
            </tr>
          </tbody>
        </table>
      </div>
      <p class="text-xs leading-5 text-gray-500 dark:text-gray-400">{{ t('iqProbe.questions.hint') }}</p>
    </section>

    <!-- Set dialog -->
    <BaseDialog :show="setDialog.show" :title="setDialog.id ? t('iqProbe.sets.editTitle') : t('iqProbe.sets.createTitle')" width="normal" @close="setDialog.show = false">
      <div class="space-y-4">
        <div>
          <label class="input-label">{{ t('iqProbe.sets.name') }}</label>
          <input v-model.trim="setDialog.name" data-test="iq-set-name" class="input" maxlength="100" />
        </div>
        <div>
          <label class="input-label">{{ t('iqProbe.sets.description') }}</label>
          <textarea v-model="setDialog.description" rows="3" class="input"></textarea>
        </div>
      </div>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" @click="setDialog.show = false">{{ t('common.cancel') }}</button>
          <button type="button" data-test="iq-set-save" class="btn btn-primary" :disabled="busy || !setDialog.name" @click="saveSet">{{ t('common.save') }}</button>
        </div>
      </template>
    </BaseDialog>

    <!-- Question dialog -->
    <BaseDialog :show="qDialog.show" :title="qDialog.id ? t('iqProbe.questions.editTitle') : t('iqProbe.questions.createTitle')" width="wide" @close="qDialog.show = false">
      <div class="space-y-4">
        <div>
          <label class="input-label">{{ t('iqProbe.questions.title') }}</label>
          <input v-model.trim="qDialog.form.title" class="input" maxlength="200" />
        </div>
        <div>
          <label class="input-label">{{ t('iqProbe.questions.prompt') }}</label>
          <textarea v-model="qDialog.form.prompt" data-test="iq-question-prompt" rows="6" class="input font-mono text-sm"></textarea>
          <p class="input-hint">{{ t('iqProbe.questions.promptHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('iqProbe.questions.system') }}</label>
          <textarea v-model="qDialog.form.system_prompt" rows="2" class="input font-mono text-sm"></textarea>
        </div>
        <div class="grid gap-4 md:grid-cols-4">
          <div class="md:col-span-2">
            <label class="input-label">{{ t('iqProbe.questions.expected') }}</label>
            <input v-model.trim="qDialog.form.expected_answer" data-test="iq-question-expected" class="input font-mono" />
            <p class="input-hint">{{ t('iqProbe.questions.expectedHint') }}</p>
          </div>
          <div>
            <label class="input-label">{{ t('iqProbe.questions.judge') }}</label>
            <select v-model="qDialog.form.judge_type" class="input">
              <option v-for="j in judgeTypes" :key="j" :value="j">{{ t(`iqProbe.judge.${j}`) }}</option>
            </select>
          </div>
          <div>
            <label class="input-label">{{ t('iqProbe.questions.weight') }}</label>
            <input v-model.number="qDialog.form.weight" type="number" min="0.1" step="0.5" class="input" />
          </div>
          <div v-if="qDialog.form.judge_type === 'numeric'">
            <label class="input-label">{{ t('iqProbe.questions.tolerance') }}</label>
            <input v-model.number="qDialog.form.numeric_tolerance" type="number" min="0" step="any" class="input" />
          </div>
          <div>
            <label class="input-label">{{ t('iqProbe.questions.sortOrder') }}</label>
            <input v-model.number="qDialog.form.sort_order" type="number" class="input" />
          </div>
          <label class="flex items-center gap-3 self-end pb-2">
            <Toggle v-model="qDialog.form.enabled" />
            <span class="text-sm text-gray-700 dark:text-gray-300">{{ t('iqProbe.questions.enabled') }}</span>
          </label>
        </div>
        <div>
          <label class="input-label">{{ t('iqProbe.questions.note') }}</label>
          <input v-model.trim="qDialog.form.note" class="input" :placeholder="t('iqProbe.questions.notePlaceholder')" />
        </div>
      </div>
      <template #footer>
        <div class="flex justify-end gap-3">
          <button type="button" class="btn btn-secondary" @click="qDialog.show = false">{{ t('common.cancel') }}</button>
          <button type="button" data-test="iq-question-save" class="btn btn-primary" :disabled="busy || !qDialog.form.prompt.trim() || !qDialog.form.expected_answer" @click="saveQuestion">
            {{ t('common.save') }}
          </button>
        </div>
      </template>
    </BaseDialog>
  </div>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import { iqProbeAPI } from '@/api/admin/iqProbe'
import type { IQProbeJudgeType, IQProbeQuestion, IQProbeQuestionInput, IQProbeQuestionSet, IQProbeSetExport } from '@/api/admin/iqProbe'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'
import sampleSetJSON from './sampleSet.json'

// Server-side validation is authoritative; the JSON literal just widens judge_type to string.
const sampleSet = sampleSetJSON as IQProbeSetExport

const props = defineProps<{ sets: IQProbeQuestionSet[] }>()
const emit = defineEmits<{ (e: 'changed'): void }>()

const { t } = useI18n()
const appStore = useAppStore()
const judgeTypes: IQProbeJudgeType[] = ['exact', 'choice', 'numeric']

const selectedId = ref<number | null>(null)
const questions = ref<IQProbeQuestion[]>([])
const busy = ref(false)
let questionsRequest = 0

const selectedSet = computed(() => props.sets.find((s) => s.id === selectedId.value) ?? null)

watch(
  () => props.sets,
  (sets) => {
    if (selectedId.value === null || !sets.some((s) => s.id === selectedId.value)) {
      selectedId.value = sets[0]?.id ?? null
      void loadQuestions()
    }
  },
  { immediate: true }
)

function errorText(err: unknown) {
  return extractI18nErrorMessage(err, t, 'iqProbe.errors', t('common.error'))
}

function select(id: number) {
  selectedId.value = id
  void loadQuestions()
}

async function loadQuestions() {
  const request = ++questionsRequest
  const setId = selectedId.value
  questions.value = []
  if (setId === null) return
  try {
    const response = await iqProbeAPI.listQuestions(setId)
    if (request === questionsRequest && selectedId.value === setId) {
      questions.value = response.data ?? []
    }
  } catch (err) {
    if (request === questionsRequest && selectedId.value === setId) {
      appStore.showError(errorText(err))
    }
  }
}

// ---- sets ----

const setDialog = reactive({ show: false, id: 0, name: '', description: '' })

function openSetDialog(set: IQProbeQuestionSet | null) {
  Object.assign(setDialog, { show: true, id: set?.id ?? 0, name: set?.name ?? '', description: set?.description ?? '' })
}

async function saveSet() {
  busy.value = true
  try {
    const input = { name: setDialog.name, description: setDialog.description }
    const saved = setDialog.id ? (await iqProbeAPI.updateSet(setDialog.id, input)).data : (await iqProbeAPI.createSet(input)).data
    selectedId.value = saved.id
    setDialog.show = false
    appStore.showSuccess(t('iqProbe.actions.saved'))
    emit('changed')
    void loadQuestions()
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    busy.value = false
  }
}

async function removeSet() {
  const set = selectedSet.value
  if (!set || !confirm(t('iqProbe.sets.confirmDelete', { name: set.name }))) return
  try {
    await iqProbeAPI.removeSet(set.id)
    selectedId.value = null
    appStore.showSuccess(t('iqProbe.actions.deleted'))
    emit('changed')
  } catch (err) {
    appStore.showError(errorText(err))
  }
}

async function exportSet() {
  const set = selectedSet.value
  if (!set) return
  try {
    const data = (await iqProbeAPI.exportSet(set.id)).data
    const blob = new Blob([JSON.stringify(data, null, 2)], { type: 'application/json' })
    const url = URL.createObjectURL(blob)
    const link = document.createElement('a')
    link.href = url
    link.download = `iq-probe-${set.name}.json`
    link.click()
    URL.revokeObjectURL(url)
  } catch (err) {
    appStore.showError(errorText(err))
  }
}

async function importFile(event: Event) {
  const input = event.target as HTMLInputElement
  const file = input.files?.[0]
  input.value = ''
  if (!file) return
  let data: IQProbeSetExport
  try {
    data = JSON.parse(await file.text())
  } catch {
    appStore.showError(t('iqProbe.sets.importInvalid'))
    return
  }
  await importData(data)
}

async function importData(data: IQProbeSetExport) {
  try {
    const set = (await iqProbeAPI.importSet(data)).data
    selectedId.value = set.id
    appStore.showSuccess(t('iqProbe.sets.imported', { name: set.name, n: set.question_count }))
    emit('changed')
    void loadQuestions()
  } catch (err) {
    appStore.showError(errorText(err))
  }
}

// ---- questions ----

const blankQuestion = (): IQProbeQuestionInput => ({
  title: '',
  prompt: '',
  system_prompt: '',
  expected_answer: '',
  judge_type: 'exact',
  numeric_tolerance: 0,
  weight: 1,
  enabled: true,
  note: '',
  sort_order: questions.value.length,
})
const qDialog = reactive<{ show: boolean; id: number; form: IQProbeQuestionInput }>({ show: false, id: 0, form: blankQuestion() })

function toInput(q: IQProbeQuestion): IQProbeQuestionInput {
  return {
    title: q.title,
    prompt: q.prompt,
    system_prompt: q.system_prompt,
    expected_answer: q.expected_answer,
    judge_type: q.judge_type,
    numeric_tolerance: q.numeric_tolerance,
    weight: q.weight,
    enabled: q.enabled,
    note: q.note,
    sort_order: q.sort_order,
  }
}

function openQuestionDialog(q: IQProbeQuestion | null) {
  qDialog.id = q?.id ?? 0
  qDialog.form = q ? toInput(q) : blankQuestion()
  qDialog.show = true
}

async function saveQuestion() {
  if (selectedId.value === null) return
  busy.value = true
  try {
    if (qDialog.id) await iqProbeAPI.updateQuestion(qDialog.id, qDialog.form)
    else await iqProbeAPI.createQuestion(selectedId.value, qDialog.form)
    qDialog.show = false
    appStore.showSuccess(t('iqProbe.actions.saved'))
    await loadQuestions()
    emit('changed')
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    busy.value = false
  }
}

async function toggleQuestion(q: IQProbeQuestion, enabled: boolean) {
  try {
    await iqProbeAPI.updateQuestion(q.id, { ...toInput(q), enabled })
    q.enabled = enabled
    emit('changed')
  } catch (err) {
    appStore.showError(errorText(err))
  }
}

async function removeQuestion(q: IQProbeQuestion) {
  if (!confirm(t('iqProbe.questions.confirmDelete', { name: q.title || q.prompt.slice(0, 30) }))) return
  try {
    await iqProbeAPI.removeQuestion(q.id)
    await loadQuestions()
    emit('changed')
  } catch (err) {
    appStore.showError(errorText(err))
  }
}
</script>
