<template>
  <div class="space-y-3">
    <h2 class="flex items-center gap-1 text-base font-semibold text-gray-900 dark:text-white">
      {{ t('intentRouter.test.title') }}
      <InfoTip :text="t('intentRouter.test.intro')" :label="t('intentRouter.test.title')" />
    </h2>
    <div class="flex flex-col gap-2 sm:flex-row sm:items-start">
      <textarea v-model.trim="text" data-test="intent-test-text" rows="2" class="input flex-1" :placeholder="t('intentRouter.test.placeholder')"></textarea>
      <button type="button" data-test="intent-test-run" class="btn btn-secondary sm:w-24" :disabled="testing || dirty || !text" :title="dirty ? t('intentRouter.test.saveFirst') : ''" @click="run">
        {{ testing ? t('intentRouter.test.running') : t('intentRouter.test.run') }}
      </button>
    </div>
    <p v-if="dirty" class="text-xs text-amber-700 dark:text-amber-300">{{ t('intentRouter.test.saveFirst') }}</p>
    <div v-if="result" data-test="intent-test-result" class="rounded-lg bg-gray-50 p-3 text-sm leading-6 dark:bg-dark-800">
      <p class="font-medium text-gray-900 dark:text-white">{{ summary }}</p>
      <p v-if="result.manual_target_missing" data-test="intent-test-manual-missing" class="text-amber-700 dark:text-amber-300">
        {{ t('intentRouter.test.byManualTargetMissing') }}
      </p>
      <p v-if="result.matched_by === 'keyword'" class="text-gray-500 dark:text-gray-400">
        {{ t('intentRouter.test.byKeyword', { keyword: result.keyword }) }}
      </p>
      <p v-else-if="result.matched_by === 'manual'" data-test="intent-test-manual" class="text-gray-500 dark:text-gray-400">
        {{ t('intentRouter.test.byManual', { label: t(`intentRouter.templates.status.${result.manual_label}`) }) }}
      </p>
      <p v-else-if="result.matched_by === 'jev'" class="text-gray-500 dark:text-gray-400">
        {{ t('intentRouter.test.byJev', { p: (result.probe_probability ?? 0).toFixed(2) }) }}<template v-if="result.health_probability !== undefined"> · {{ t(result.health_check ? 'intentRouter.test.byJevHealthHit' : 'intentRouter.test.byJevHealth', { p: result.health_probability.toFixed(2) }) }}</template><template v-if="result.timely_probability !== undefined"> · <span data-test="intent-test-timely">{{ t(result.timely ? 'intentRouter.test.byJevTimelyHit' : 'intentRouter.test.byJevTimely', { p: result.timely_probability.toFixed(2) }) }}</span></template> · {{ t('intentRouter.test.latency', { ms: result.latency_ms }) }}
      </p>
      <p v-else-if="result.matched_by === 'classifier'" class="text-gray-500 dark:text-gray-400">
        {{ t('intentRouter.test.answer', { answer: result.answer || '—' }) }} · {{ t('intentRouter.test.latency', { ms: result.latency_ms }) }}
      </p>
      <p v-if="result.matched_by === 'classifier' && result.probe_probability !== undefined" data-test="intent-test-jev-before" class="text-gray-500 dark:text-gray-400">
        {{ t('intentRouter.test.byJev', { p: result.probe_probability.toFixed(2) }) }}<template v-if="result.health_probability !== undefined"> · {{ t('intentRouter.test.byJevHealth', { p: result.health_probability.toFixed(2) }) }}</template><template v-if="result.timely_probability !== undefined"> · {{ t('intentRouter.test.byJevTimely', { p: result.timely_probability.toFixed(2) }) }}</template><template v-if="result.jev_latency_ms !== undefined"> · {{ t('intentRouter.test.latency', { ms: result.jev_latency_ms }) }}</template>
      </p>
    </div>
    <p v-if="error" data-test="intent-test-error" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ error }}</p>
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { intentRoutersAPI } from '@/api/admin/intentRouters'
import type { IntentClassifyTestResult } from '@/api/admin/intentRouters'
import { extractI18nErrorMessage } from '@/utils/apiError'
import InfoTip from './InfoTip.vue'
import { useIntentRouter } from './context'

const { t } = useI18n()
const { selectedGroup, dirty, formGeneration, accountLabel, keyLabel } = useIntentRouter()

const text = ref('')
const testing = ref(false)
const result = ref<IntentClassifyTestResult | null>(null)
const error = ref('')

// A result belongs to the form it was asked for.
watch(formGeneration, () => {
  result.value = null
  error.value = ''
})

const summary = computed(() => {
  const r = result.value
  if (!r) return ''
  if (!r.understood) return t('intentRouter.test.resultUnclear')
  if (!r.intent) return t('intentRouter.test.resultNone')
  const targets = r.api_key_id ? keyLabel(r.api_key_id) : r.account_ids.map(accountLabel).join('、')
  return t('intentRouter.test.resultIntent', { intent: r.intent, accounts: targets })
})

async function run() {
  const groupId = selectedGroup.value?.id
  if (!groupId || testing.value || dirty.value || !text.value) return
  testing.value = true
  result.value = null
  error.value = ''
  try {
    const answer = (await intentRoutersAPI.test(groupId, text.value)).data
    if (selectedGroup.value?.id === groupId) result.value = answer
  } catch (err) {
    if (selectedGroup.value?.id === groupId) error.value = extractI18nErrorMessage(err, t, 'intentRouter.errors', t('common.error'))
  } finally {
    testing.value = false
  }
}
</script>
