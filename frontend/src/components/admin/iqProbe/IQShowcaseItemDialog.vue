<template>
  <BaseDialog :show="show" :title="editing ? t('iqShowcase.admin.editTitle') : t('iqShowcase.admin.createTitle')" width="wide" @close="emit('close')">
    <div class="grid gap-x-4 gap-y-3.5 md:grid-cols-2">
      <div class="md:col-span-2">
        <label class="input-label" for="iq-showcase-f-title">{{ t('iqShowcase.admin.fields.title') }}</label>
        <input id="iq-showcase-f-title" v-model.trim="form.title" data-test="iq-showcase-title" class="input" maxlength="100" :placeholder="t('iqShowcase.admin.fields.titlePlaceholder')" />
      </div>
      <div>
        <label class="input-label" for="iq-showcase-f-group">{{ t('iqShowcase.admin.fields.group') }}</label>
        <select id="iq-showcase-f-group" v-model="form.group_id" data-test="iq-showcase-group" class="input" @change="onGroupChanged">
          <option :value="0" disabled>{{ t('iqShowcase.admin.fields.pickGroup') }}</option>
          <option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }}</option>
        </select>
      </div>
      <div>
        <label class="input-label" for="iq-showcase-f-key">{{ t('iqShowcase.admin.fields.key') }}</label>
        <select id="iq-showcase-f-key" v-model="form.api_key_id" data-test="iq-showcase-key" class="input" :disabled="!form.group_id || keysLoading">
          <option :value="null" disabled>{{ keysLoading ? t('common.loading') : t('iqShowcase.admin.fields.pickKey') }}</option>
          <option v-for="k in keys" :key="k.id" :value="k.id">
            {{ k.name || '#' + k.id }} · {{ k.masked }}{{ k.status !== 'active' ? ' · ' + k.status : '' }}
          </option>
        </select>
        <p v-if="form.group_id && !keysLoading && keys.length === 0" class="input-hint" data-test="iq-showcase-no-keys">{{ t('iqShowcase.admin.fields.noKeys') }}</p>
      </div>
      <div>
        <label class="input-label" for="iq-showcase-f-protocol">{{ t('iqShowcase.admin.fields.protocol') }}</label>
        <select id="iq-showcase-f-protocol" v-model="form.protocol" data-test="iq-showcase-protocol" class="input">
          <option v-for="p in IQ_SHOWCASE_PROTOCOLS" :key="p" :value="p">{{ t(`iqShowcase.admin.protocols.${p}`) }}</option>
        </select>
      </div>
      <div>
        <label class="input-label" for="iq-showcase-f-model">{{ t('iqShowcase.admin.fields.model') }}</label>
        <input id="iq-showcase-f-model" v-model.trim="form.model" data-test="iq-showcase-model" class="input" maxlength="200" />
      </div>
      <div class="md:col-span-2">
        <label class="input-label" for="iq-showcase-f-prompt">{{ t('iqShowcase.admin.fields.prompt') }}</label>
        <textarea id="iq-showcase-f-prompt" v-model="form.prompt" data-test="iq-showcase-prompt" rows="4" class="input text-sm"></textarea>
        <p class="input-hint">{{ t('iqShowcase.admin.fields.promptHint') }}</p>
      </div>
      <div>
        <label class="input-label" for="iq-showcase-f-interval">{{ t('iqShowcase.admin.fields.interval') }}</label>
        <input id="iq-showcase-f-interval" v-model.number="form.interval_minutes" type="number" min="1" max="1440" class="input" />
      </div>
      <div>
        <label class="input-label" for="iq-showcase-f-cards">{{ t('iqShowcase.admin.fields.maxCards') }}</label>
        <input id="iq-showcase-f-cards" v-model.number="form.max_cards" type="number" min="1" max="100" class="input" />
      </div>

      <details class="group md:col-span-2 border-t border-dashed border-gray-200 pt-2 dark:border-dark-600" data-test="iq-showcase-advanced" :open="advancedOpen" @toggle="onAdvancedToggle">
        <summary class="inline-flex min-h-8 cursor-pointer list-none items-center gap-1.5 text-sm text-gray-600 dark:text-gray-300 [&::-webkit-details-marker]:hidden">
          <svg class="h-3 w-3 transition-transform group-open:rotate-90" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.4" stroke-linecap="round" aria-hidden="true"><path d="M9 6l6 6-6 6" /></svg>
          {{ t('iqShowcase.admin.advanced') }}
        </summary>
        <div class="grid gap-x-4 gap-y-3.5 pt-3 md:grid-cols-3">
          <div>
            <label class="input-label" for="iq-showcase-f-effort">{{ t('iqShowcase.admin.fields.effort') }}</label>
            <select id="iq-showcase-f-effort" v-model="form.reasoning_effort" data-test="iq-showcase-effort" class="input" :disabled="!effortApplies">
              <option value="">{{ t('iqShowcase.admin.fields.effortDefault') }}</option>
              <option v-for="e in efforts" :key="e" :value="e">{{ e }}</option>
            </select>
          </div>
          <div>
            <label class="input-label" for="iq-showcase-f-tokens">{{ t('iqShowcase.admin.fields.maxTokens') }}</label>
            <input id="iq-showcase-f-tokens" v-model.number="form.max_output_tokens" data-test="iq-showcase-max-tokens" type="number" min="0" max="200000" class="input" />
            <p class="input-hint" data-test="iq-showcase-max-tokens-hint">{{ form.protocol === 'anthropic_messages' ? t('iqShowcase.admin.fields.maxTokensHintAnthropic') : t('iqShowcase.admin.fields.maxTokensHint') }}</p>
          </div>
          <div>
            <label class="input-label" for="iq-showcase-f-sort">{{ t('iqShowcase.admin.fields.sortOrder') }}</label>
            <input id="iq-showcase-f-sort" v-model.number="form.sort_order" data-test="iq-showcase-sort" type="number" class="input" />
          </div>
        </div>
      </details>

      <p v-if="editing" class="rounded-lg bg-amber-50 px-3 py-2 text-xs text-amber-800 dark:bg-amber-900/20 dark:text-amber-300 md:col-span-2">
        {{ t('iqShowcase.admin.revHint') }}
      </p>
    </div>

    <template #footer>
      <div class="flex w-full flex-wrap items-center justify-between gap-3">
        <label class="flex items-center gap-2.5 text-sm text-gray-700 dark:text-gray-300">
          <Toggle v-model="form.enabled" data-test="iq-showcase-enabled" />
          {{ t('iqShowcase.admin.fields.enabled') }}
        </label>
        <div class="flex gap-3">
          <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
          <button type="button" class="btn btn-primary" data-test="iq-showcase-save" :disabled="saving || !canSave" @click="save">
            {{ saving ? t('common.saving') : t('common.save') }}
          </button>
        </div>
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
// The advanced section opens for an item that already uses one of its fields,
// so an edit never hides settings that are in effect.
const advancedOpen = ref(false)

function onAdvancedToggle(event: Event) {
  advancedOpen.value = (event.target as HTMLDetailsElement).open
}

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
    advancedOpen.value = !!e && (e.reasoning_effort !== '' || e.max_output_tokens !== 0 || e.sort_order !== 0)
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
