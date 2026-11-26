<template>
  <div class="card p-4" data-testid="openai-cache-global-card">
    <div class="flex flex-wrap items-center gap-4">
      <div class="flex items-center gap-3" :title="t('admin.openaiCacheUser.global.hint')">
        <span class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.openaiCacheUser.global.title') }}</span>
        <button
          type="button"
          role="switch"
          data-testid="openai-cache-global-toggle"
          :aria-checked="form.enabled"
          :class="[
            'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
            form.enabled ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
          ]"
          @click="form.enabled = !form.enabled"
        >
          <span
            :class="[
              'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
              form.enabled ? 'translate-x-5' : 'translate-x-0'
            ]"
          />
        </button>
      </div>
      <label class="flex items-center gap-1 text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.openai.cacheSimulationLo') }}
        <input v-model.number="form.lo" type="number" min="0" max="100" step="0.5" class="input w-20" data-testid="openai-cache-global-lo" />
      </label>
      <label class="flex items-center gap-1 text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.openai.cacheSimulationHi') }}
        <input v-model.number="form.hi" type="number" min="0" max="100" step="0.5" class="input w-20" data-testid="openai-cache-global-hi" />
      </label>
      <label class="flex items-center gap-1 text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.openai.cacheSimulationCap') }}
        <input v-model.number="form.cap" type="number" min="50" max="100" step="1" class="input w-20" data-testid="openai-cache-global-cap" />
      </label>
      <label class="flex items-center gap-1 text-xs text-gray-500 dark:text-gray-400">
        {{ t('admin.accounts.openai.cacheSimulationHalfLife') }}
        <input v-model.number="form.half_life_minutes" type="number" min="1" max="1440" step="1" class="input w-20" data-testid="openai-cache-global-half-life" />
      </label>
      <button
        v-if="overrides > 0"
        type="button"
        class="badge badge-primary cursor-pointer"
        data-testid="openai-cache-global-overrides"
        :title="t('admin.openaiCacheUser.global.overridesHint')"
        @click="emit('show-overrides')"
      >
        {{ t('admin.openaiCacheUser.global.overrides', { n: overrides }) }}
      </button>
      <button
        type="button"
        class="btn btn-primary btn-sm ml-auto"
        :disabled="saving || loading"
        data-testid="openai-cache-global-save"
        @click="save"
      >
        {{ t('admin.openaiCache.simModal.save') }}
      </button>
    </div>
  </div>
</template>

<script setup lang="ts">
import { onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { openaiCacheAPI } from '@/api/admin/openaiCache'

// 全局「按用户缓存模拟」：开启后对没有单独配置的用户生效（用户单独配置优先）。
const emit = defineEmits<{
  (e: 'saved'): void
  (e: 'show-overrides'): void
}>()

const { t } = useI18n()
const appStore = useAppStore()
const loading = ref(false)
const saving = ref(false)
const overrides = ref(0)
const form = reactive({ enabled: false, lo: 82, hi: 88, half_life_minutes: 60, cap: 95 })

const load = async () => {
  loading.value = true
  try {
    const res = await openaiCacheAPI.getGlobalSimulation()
    Object.assign(form, res.config)
    overrides.value = res.overrides
  } catch (error) {
    console.error('Failed to load global cache simulation:', error)
    appStore.showError(t('admin.openaiCache.loadFailed'))
  } finally {
    loading.value = false
  }
}

const save = async () => {
  if (form.lo > form.hi) {
    appStore.showError(t('admin.accounts.openai.cacheSimulationRangeInvalid'))
    return
  }
  saving.value = true
  try {
    const saved = await openaiCacheAPI.updateGlobalSimulation({ ...form })
    Object.assign(form, saved)
    appStore.showSuccess(t('admin.openaiCache.simModal.saved'))
    emit('saved')
  } catch (error) {
    console.error('Failed to save global cache simulation:', error)
    appStore.showError(t('admin.openaiCache.simModal.saveFailed'))
  } finally {
    saving.value = false
  }
}

defineExpose({ load })
onMounted(load)
</script>
