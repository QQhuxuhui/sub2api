<template>
  <BaseDialog :show="show" :title="t('admin.openaiCache.simModal.title', { name: account?.name ?? '' })" width="normal" @close="emit('close')">
    <div class="space-y-4">
      <div class="flex items-center justify-between gap-4">
        <div>
          <label class="input-label mb-0">{{ t('admin.openaiCache.simModal.enabled') }}</label>
          <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.accounts.openai.cacheSimulationDesc') }}</p>
        </div>
        <button
          type="button"
          data-testid="openai-cache-sim-modal-toggle"
          role="switch"
          :aria-checked="form.enabled"
          @click="form.enabled = !form.enabled"
          :class="[
            'relative inline-flex h-6 w-11 flex-shrink-0 cursor-pointer rounded-full border-2 border-transparent transition-colors duration-200 ease-in-out focus:outline-none focus:ring-2 focus:ring-primary-500 focus:ring-offset-2',
            form.enabled ? 'bg-primary-600' : 'bg-gray-200 dark:bg-dark-600'
          ]"
        >
          <span
            :class="[
              'pointer-events-none inline-block h-5 w-5 transform rounded-full bg-white shadow ring-0 transition duration-200 ease-in-out',
              form.enabled ? 'translate-x-5' : 'translate-x-0'
            ]"
          />
        </button>
      </div>
      <div class="grid grid-cols-2 gap-3 md:grid-cols-4">
        <div>
          <label class="input-label text-xs">{{ t('admin.accounts.openai.cacheSimulationLo') }}</label>
          <input v-model.number="form.lo" type="number" min="0" max="100" step="0.5" class="input" data-testid="openai-cache-sim-modal-lo" />
        </div>
        <div>
          <label class="input-label text-xs">{{ t('admin.accounts.openai.cacheSimulationHi') }}</label>
          <input v-model.number="form.hi" type="number" min="0" max="100" step="0.5" class="input" data-testid="openai-cache-sim-modal-hi" />
        </div>
        <div>
          <label class="input-label text-xs">{{ t('admin.accounts.openai.cacheSimulationHalfLife') }}</label>
          <input v-model.number="form.half_life_minutes" type="number" min="1" max="1440" step="1" class="input" data-testid="openai-cache-sim-modal-half-life" />
        </div>
        <div>
          <label class="input-label text-xs">{{ t('admin.accounts.openai.cacheSimulationCap') }}</label>
          <input v-model.number="form.cap" type="number" min="50" max="100" step="1" class="input" data-testid="openai-cache-sim-modal-cap" />
        </div>
      </div>
    </div>
    <template #footer>
      <div class="flex justify-end space-x-3">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="emit('close')">
          {{ t('admin.openaiCache.simModal.cancel') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="saving" data-testid="openai-cache-sim-modal-save" @click="handleSave">
          {{ t('admin.openaiCache.simModal.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores/app'
import { openaiCacheAPI } from '@/api/admin/openaiCache'
import type { OpenAICacheAccountRow, OpenAICacheSimConfigView } from '@/api/admin/openaiCache'

// 「上游缓存」页里的快捷配置弹窗：字段与账号编辑弹窗的缓存模拟区块一致，
// 但走专用接口只合并 openai_cache_simulation 这一项，不会动账号其它配置。
const props = defineProps<{
  show: boolean
  account: OpenAICacheAccountRow | null
}>()

const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved', accountId: number, config: OpenAICacheSimConfigView): void
}>()

const { t } = useI18n()
const appStore = useAppStore()

const saving = ref(false)
const form = reactive({ enabled: false, lo: 82, hi: 88, half_life_minutes: 60, cap: 95 })

watch(
  () => [props.show, props.account] as const,
  ([show, account]) => {
    if (!show || !account) return
    const sim = account.simulation
    // 这个弹窗是从「开启」按钮点进来的：关着的账号默认把开关打开省一次点击，
    // 已开启的账号本来就是开的；要关闭就在弹窗里拨回去再保存。
    form.enabled = true
    form.lo = sim.lo
    form.hi = sim.hi
    form.half_life_minutes = sim.half_life_minutes
    form.cap = sim.cap
  },
  { immediate: true }
)

const handleSave = async () => {
  if (!props.account) return
  if (form.enabled && form.lo > form.hi) {
    appStore.showError(t('admin.accounts.openai.cacheSimulationRangeInvalid'))
    return
  }
  saving.value = true
  try {
    const config = await openaiCacheAPI.updateSimulation(props.account.account_id, {
      enabled: form.enabled,
      lo: form.lo,
      hi: form.hi,
      half_life_minutes: form.half_life_minutes,
      cap: form.cap
    })
    appStore.showSuccess(t('admin.openaiCache.simModal.saved'))
    emit('saved', props.account.account_id, config)
  } catch (error) {
    console.error('Failed to update openai cache simulation:', error)
    appStore.showError(t('admin.openaiCache.simModal.saveFailed'))
  } finally {
    saving.value = false
  }
}
</script>
