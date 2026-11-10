<template>
  <div class="space-y-5">
    <div class="grid gap-5 md:grid-cols-2">
      <div class="md:col-span-2">
        <label class="input-label flex items-center gap-1">{{ t('intentRouter.excludedModels') }} <InfoTip :text="t('intentRouter.excludedModelsHint')" :label="t('intentRouter.excludedModels')" /></label>
        <textarea v-model="form.excludedModelsText" data-test="intent-excluded-models" rows="2" class="input font-mono text-sm" :placeholder="t('intentRouter.excludedModelsPlaceholder')"></textarea>
      </div>
      <div>
        <label class="input-label flex items-center gap-1">{{ t('intentRouter.keywordMaxChars') }} <InfoTip :text="t('intentRouter.keywordMaxCharsHint')" :label="t('intentRouter.keywordMaxChars')" /></label>
        <div class="flex items-center gap-2">
          <input v-model.number="form.keyword_max_chars" data-test="intent-keyword-max-chars" type="number" min="0" max="1000000" step="1" class="input w-40" />
          <span class="text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.zeroUnlimited') }}</span>
        </div>
      </div>
      <div>
        <label class="input-label flex items-center gap-1">{{ t('intentRouter.unavailableStatus') }} <InfoTip :text="t('intentRouter.unavailableStatusHint')" :label="t('intentRouter.unavailableStatus')" /></label>
        <select v-model.number="form.unavailable_status" data-test="intent-unavailable-status" class="input w-40">
          <option :value="503">503</option>
          <option :value="502">502</option>
          <option :value="429">429</option>
        </select>
      </div>
      <div class="md:col-span-2">
        <label class="flex items-center gap-3">
          <input v-model="form.cache_smooth_enabled" data-test="intent-cache-smooth" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
          <span class="input-label mb-0">{{ t('intentRouter.cacheSmooth') }}</span>
          <InfoTip :text="t('intentRouter.cacheSmoothHint')" :label="t('intentRouter.cacheSmooth')" />
        </label>
      </div>
    </div>

    <div v-if="savedRouter" class="flex flex-wrap items-center gap-3 border-t border-gray-100 pt-4 dark:border-dark-700">
      <button type="button" class="btn btn-secondary btn-sm" :disabled="busy" @click="emit('clear-cache')">{{ t('intentRouter.actions.clearCache') }}</button>
      <button type="button" data-test="intent-delete" class="btn btn-sm ml-auto border border-red-200 text-red-600 hover:bg-red-50 dark:border-red-900/50 dark:text-red-400 dark:hover:bg-red-900/20" :disabled="busy" @click="emit('delete')">
        {{ t('intentRouter.actions.delete') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { useI18n } from 'vue-i18n'
import InfoTip from './InfoTip.vue'
import { useIntentRouter } from './context'

defineProps<{ busy: boolean }>()
const emit = defineEmits<{ 'clear-cache': []; delete: [] }>()
const { t } = useI18n()
const { form, savedRouter } = useIntentRouter()
</script>
