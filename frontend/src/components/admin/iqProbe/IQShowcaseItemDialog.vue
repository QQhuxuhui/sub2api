<template>
  <BaseDialog :show="show" :title="editing ? t('iqShowcase.admin.editTitle') : t('iqShowcase.admin.createTitle')" width="wide" @close="emit('close')">
    <div class="space-y-5">
      <div class="grid gap-4 md:grid-cols-2">
        <div class="md:col-span-2">
          <label class="input-label">{{ t('iqShowcase.admin.fields.title') }}</label>
          <input v-model.trim="form.title" data-test="iq-showcase-title" class="input" maxlength="100" :placeholder="t('iqShowcase.admin.fields.titlePlaceholder')" />
          <p class="input-hint">{{ t('iqShowcase.admin.fields.titleHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('iqShowcase.admin.fields.group') }}</label>
          <select v-model="form.group_id" data-test="iq-showcase-group" class="input" @change="onGroupChanged">
            <option :value="0" disabled>{{ t('iqShowcase.admin.fields.pickGroup') }}</option>
            <option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }} ({{ g.platform }})</option>
          </select>
        </div>
        <div>
          <label class="input-label">{{ t('iqShowcase.admin.fields.key') }}</label>
          <select v-model="form.api_key_id" data-test="iq-showcase-key" class="input" :disabled="!form.group_id || keysLoading">
            <option :value="null" disabled>{{ keysLoading ? t('common.loading') : t('iqShowcase.admin.fields.pickKey') }}</option>
            <option v-for="k in keys" :key="k.id" :value="k.id">
              {{ k.name || '#' + k.id }} · {{ k.masked }}{{ k.user_email ? ' · ' + k.user_email : '' }}{{ k.status !== 'active' ? ' · ' + k.status : '' }}
            </option>
          </select>
          <p class="input-hint">{{ form.group_id && !keysLoading && keys.length === 0 ? t('iqShowcase.admin.fields.noKeys') : t('iqShowcase.admin.fields.keyHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('iqShowcase.admin.fields.protocol') }}</label>
          <select v-model="form.protocol" data-test="iq-showcase-protocol" class="input">
            <option v-for="p in IQ_SHOWCASE_PROTOCOLS" :key="p" :value="p">{{ t(`iqShowcase.admin.protocols.${p}`) }}</option>
          </select>
        </div>
        <div>
          <label class="input-label">{{ t('iqShowcase.admin.fields.model') }}</label>
          <input v-model.trim="form.model" data-test="iq-showcase-model" class="input" maxlength="200" />
          <p class="input-hint">{{ t('iqShowcase.admin.fields.modelHint') }}</p>
        </div>
        <div class="md:col-span-2">
          <label class="input-label">{{ t('iqShowcase.admin.fields.prompt') }}</label>
          <textarea v-model="form.prompt" data-test="iq-showcase-prompt" rows="6" class="input font-mono text-xs"></textarea>
          <p class="input-hint">{{ t('iqShowcase.admin.fields.promptHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('iqShowcase.admin.fields.interval') }}</label>
          <input v-model.number="form.interval_minutes" type="number" min="1" max="1440" class="input" />
          <p class="input-hint">{{ t('iqShowcase.admin.fields.intervalHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('iqShowcase.admin.fields.maxCards') }}</label>
          <input v-model.number="form.max_cards" type="number" min="1" max="100" class="input" />
          <p class="input-hint">{{ t('iqShowcase.admin.fields.maxCardsHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('iqShowcase.admin.fields.effort') }}</label>
          <select v-model="form.reasoning_effort" class="input" :disabled="!effortApplies">
            <option value="">{{ t('iqShowcase.admin.fields.effortDefault') }}</option>
            <option v-for="e in efforts" :key="e" :value="e">{{ e }}</option>
          </select>
          <p class="input-hint">{{ t('iqShowcase.admin.fields.effortHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('iqShowcase.admin.fields.maxTokens') }}</label>
          <input v-model.number="form.max_output_tokens" type="number" min="0" max="200000" class="input" />
          <p class="input-hint">{{ t('iqShowcase.admin.fields.maxTokensHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('iqShowcase.admin.fields.sortOrder') }}</label>
          <input v-model.number="form.sort_order" type="number" class="input" />
        </div>
        <div class="flex items-center gap-3 pt-6">
          <Toggle v-model="form.enabled" />
          <span class="text-sm text-gray-700 dark:text-gray-300">{{ t('iqShowcase.admin.fields.enabled') }}</span>
        </div>
      </div>
      <p v-if="editing" class="rounded-lg bg-amber-50 p-3 text-xs text-amber-700 dark:bg-amber-900/20 dark:text-amber-300">
        {{ t('iqShowcase.admin.revHint') }}
      </p>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button type="button" class="btn btn-primary" data-test="iq-showcase-save" :disabled="saving || !canSave" @click="save">
          {{ saving ? t('common.saving') : t('common.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { computed, reactive, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Toggle from '@/components/common/Toggle.vue'
import { IQ_SHOWCASE_PROTOCOLS, defaultIQShowcaseProtocol, iqShowcaseAdminAPI } from '@/api/admin/iqShowcase'
import type { IQShowcaseItem, IQShowcaseItemInput, IQShowcaseKeyOption } from '@/api/admin/iqShowcase'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'

const props = defineProps<{
  show: boolean
  editing: IQShowcaseItem | null
  groups: Array<{ id: number; name: string; platform: string }>
}>()
const emit = defineEmits<{ close: []; saved: [] }>()

const { t } = useI18n()
const appStore = useAppStore()
const efforts = ['none', 'minimal', 'low', 'medium', 'high', 'xhigh']

function blank(): IQShowcaseItemInput {
  return {
    title: '',
    group_id: 0,
    api_key_id: null,
    protocol: 'openai_responses',
    model: '',
    prompt: '',
    reasoning_effort: '',
    max_output_tokens: 0,
    interval_minutes: 60,
    max_cards: 12,
    enabled: true,
    sort_order: 0,
  }
}

const form = reactive<IQShowcaseItemInput>(blank())
const keys = ref<IQShowcaseKeyOption[]>([])
const keysLoading = ref(false)
const saving = ref(false)

const effortApplies = computed(() => form.protocol === 'openai_responses' || form.protocol === 'openai_chat')
const canSave = computed(() => form.title !== '' && form.group_id > 0 && form.api_key_id !== null && form.model !== '' && form.prompt.trim() !== '')

async function loadKeys(groupId: number) {
  keys.value = []
  if (!groupId) return
  keysLoading.value = true
  try {
    keys.value = (await iqShowcaseAdminAPI.keyOptions(groupId)).data ?? []
  } catch (err) {
    appStore.showError(extractI18nErrorMessage(err, t, 'iqShowcase.errors', t('common.error')))
  } finally {
    keysLoading.value = false
  }
}

function onGroupChanged() {
  form.api_key_id = null
  const group = props.groups.find((g) => g.id === form.group_id)
  if (group) form.protocol = defaultIQShowcaseProtocol(group.platform)
  void loadKeys(form.group_id)
}

watch(
  () => props.show,
  (open) => {
    if (!open) return
    Object.assign(form, blank())
    const e = props.editing
    if (e) {
      Object.assign(form, {
        title: e.title,
        group_id: e.group_id,
        api_key_id: e.api_key_id,
        protocol: e.protocol,
        model: e.model,
        prompt: e.prompt,
        reasoning_effort: e.reasoning_effort,
        max_output_tokens: e.max_output_tokens,
        interval_minutes: e.interval_minutes,
        max_cards: e.max_cards,
        enabled: e.enabled,
        sort_order: e.sort_order,
      })
    }
    void loadKeys(form.group_id)
  },
  { immediate: true },
)

async function save() {
  saving.value = true
  try {
    const input: IQShowcaseItemInput = { ...form, reasoning_effort: effortApplies.value ? form.reasoning_effort : '' }
    if (props.editing) await iqShowcaseAdminAPI.updateItem(props.editing.id, input)
    else await iqShowcaseAdminAPI.createItem(input)
    appStore.showSuccess(t('iqShowcase.admin.saved'))
    emit('saved')
  } catch (err) {
    appStore.showError(extractI18nErrorMessage(err, t, 'iqShowcase.errors', t('common.error')))
  } finally {
    saving.value = false
  }
}
</script>
