<template>
  <BaseDialog :show="show" :title="t('admin.openaiCacheUser.modal.title', { name: userLabel })" width="normal" @close="emit('close')">
    <div class="space-y-4">
      <div class="flex gap-2" role="radiogroup">
        <button
          v-for="m in modes"
          :key="m"
          type="button"
          role="radio"
          :aria-checked="mode === m"
          :data-testid="`openai-cache-user-mode-${m}`"
          :class="['btn btn-sm', mode === m ? 'btn-primary' : 'btn-secondary']"
          @click="mode = m"
        >
          {{ t(`admin.openaiCacheUser.modal.mode.${m}`) }}
        </button>
      </div>
      <div v-if="mode === 'on'" class="grid grid-cols-2 gap-3 md:grid-cols-4">
        <div>
          <label class="input-label text-xs">{{ t('admin.accounts.openai.cacheSimulationLo') }}</label>
          <input v-model.number="form.lo" type="number" min="0" max="100" step="0.5" class="input" data-testid="openai-cache-user-lo" />
        </div>
        <div>
          <label class="input-label text-xs">{{ t('admin.accounts.openai.cacheSimulationHi') }}</label>
          <input v-model.number="form.hi" type="number" min="0" max="100" step="0.5" class="input" data-testid="openai-cache-user-hi" />
        </div>
        <div>
          <label class="input-label text-xs">{{ t('admin.accounts.openai.cacheSimulationHalfLife') }}</label>
          <input v-model.number="form.half_life_minutes" type="number" min="1" max="1440" step="1" class="input" />
        </div>
        <div>
          <label class="input-label text-xs">{{ t('admin.accounts.openai.cacheSimulationCap') }}</label>
          <input v-model.number="form.cap" type="number" min="50" max="100" step="1" class="input" />
        </div>
      </div>
      <p class="text-xs text-gray-500 dark:text-gray-400">{{ t(`admin.openaiCacheUser.modal.hint.${mode}`) }}</p>
    </div>
    <template #footer>
      <div class="flex justify-end space-x-3">
        <button type="button" class="btn btn-secondary" :disabled="saving" @click="emit('close')">
          {{ t('admin.openaiCache.simModal.cancel') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="saving" data-testid="openai-cache-user-save" @click="save">
          {{ t('admin.openaiCache.simModal.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import { useAppStore } from '@/stores/app'
import { openaiCacheAPI } from '@/api/admin/openaiCache'
import type { OpenAICacheSimConfigView, OpenAICacheUserRow } from '@/api/admin/openaiCache'

// 用户单独配置：继承（删除单独配置）/ 单独开启 / 明确关闭。单独配置优先于全局。
type Mode = 'inherit' | 'on' | 'off'
const modes: Mode[] = ['inherit', 'on', 'off']

const props = defineProps<{
  show: boolean
  user: OpenAICacheUserRow | null
  global: OpenAICacheSimConfigView | null
}>()
const emit = defineEmits<{
  (e: 'close'): void
  (e: 'saved'): void
}>()

const { t } = useI18n()
const appStore = useAppStore()
const saving = ref(false)
const mode = ref<Mode>('inherit')
const form = reactive({ lo: 82, hi: 88, half_life_minutes: 60, cap: 95 })

const userLabel = computed(() => props.user?.email || props.user?.username || `#${props.user?.user_id ?? ''}`)

watch(
  () => [props.show, props.user] as const,
  ([show, user]) => {
    if (!show || !user) return
    const base = user.override ?? user.config ?? props.global
    if (base) {
      form.lo = base.lo
      form.hi = base.hi
      form.half_life_minutes = base.half_life_minutes
      form.cap = base.cap
    }
    mode.value = user.override ? (user.override.enabled ? 'on' : 'off') : 'inherit'
  },
  { immediate: true }
)

const save = async () => {
  if (!props.user) return
  if (mode.value === 'on' && form.lo > form.hi) {
    appStore.showError(t('admin.accounts.openai.cacheSimulationRangeInvalid'))
    return
  }
  saving.value = true
  try {
    if (mode.value === 'inherit') {
      await openaiCacheAPI.updateUserSimulation(props.user.user_id, { inherit: true })
    } else {
      await openaiCacheAPI.updateUserSimulation(props.user.user_id, { enabled: mode.value === 'on', ...form })
    }
    appStore.showSuccess(t('admin.openaiCache.simModal.saved'))
    emit('saved')
  } catch (error) {
    console.error('Failed to update user cache simulation:', error)
    appStore.showError(t('admin.openaiCache.simModal.saveFailed'))
  } finally {
    saving.value = false
  }
}
</script>
