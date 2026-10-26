<template>
  <BaseDialog :show="show" :title="editing ? t('iqProbe.monitorDialog.editTitle') : t('iqProbe.monitorDialog.createTitle')" width="wide" @close="emit('close')">
    <div class="space-y-5">
      <!-- Target -->
      <div v-if="editing" class="rounded-lg bg-gray-50 p-3 text-sm text-gray-700 dark:bg-dark-800 dark:text-gray-300">
        {{ t('iqProbe.monitorDialog.account') }}：{{ editing.account_name }} (#{{ editing.account_id }}, {{ editing.account_platform }})
      </div>
      <template v-else>
        <div>
          <label class="input-label">{{ t('iqProbe.monitorDialog.group') }}</label>
          <select v-model="groupId" data-test="iq-monitor-group" class="input" @change="loadGroupAccounts">
            <option :value="null" disabled>{{ t('iqProbe.monitorDialog.pickGroup') }}</option>
            <option v-for="g in groups" :key="g.id" :value="g.id">{{ g.name }} ({{ g.platform }})</option>
          </select>
        </div>
        <div v-if="groupId !== null">
          <div class="mb-1 flex items-center justify-between">
            <label class="input-label mb-0">{{ t('iqProbe.monitorDialog.accounts', { n: accountIds.length }) }}</label>
            <button type="button" class="text-xs text-primary-600 hover:underline dark:text-primary-400" @click="toggleAll">
              {{ allChecked ? t('iqProbe.monitorDialog.selectNone') : t('iqProbe.monitorDialog.selectAll') }}
            </button>
          </div>
          <div v-if="accountsLoading" class="py-3 text-sm text-gray-500">{{ t('common.loading') }}</div>
          <p v-else-if="groupAccounts.length === 0" class="py-3 text-sm text-gray-500 dark:text-gray-400">{{ t('iqProbe.monitorDialog.noAccounts') }}</p>
          <div v-else class="max-h-56 space-y-1 overflow-y-auto rounded-lg border border-gray-200 p-2 dark:border-dark-600">
            <label v-for="a in groupAccounts" :key="a.id" class="flex items-center gap-2 rounded px-2 py-1 text-sm hover:bg-gray-50 dark:hover:bg-dark-700">
              <input v-model="accountIds" type="checkbox" :value="a.id" :data-test="`iq-monitor-account-${a.id}`" class="h-4 w-4 rounded border-gray-300 text-primary-600" @change="onAccountsChanged" />
              <span class="truncate text-gray-800 dark:text-gray-200">{{ a.name }}</span>
              <span class="text-xs text-gray-500">#{{ a.id }} · {{ a.platform }} · {{ a.type }}</span>
              <span v-if="a.status !== 'active'" class="rounded bg-red-50 px-1.5 text-xs text-red-600 dark:bg-red-900/30 dark:text-red-300">{{ a.status }}</span>
            </label>
          </div>
        </div>
      </template>

      <div class="grid gap-4 md:grid-cols-2">
        <div>
          <label class="input-label">{{ t('iqProbe.monitorDialog.model') }}</label>
          <input v-model.trim="form.model" data-test="iq-monitor-model" class="input" list="iq-probe-model-options" :placeholder="t('iqProbe.monitorDialog.modelPlaceholder')" />
          <datalist id="iq-probe-model-options">
            <option v-for="m in modelOptions" :key="m" :value="m" />
          </datalist>
          <p class="input-hint">{{ t('iqProbe.monitorDialog.modelHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('iqProbe.monitorDialog.set') }}</label>
          <select v-model="form.set_id" data-test="iq-monitor-set" class="input">
            <option :value="0" disabled>{{ t('iqProbe.monitorDialog.pickSet') }}</option>
            <option v-for="s in sets" :key="s.id" :value="s.id">{{ s.name }}（{{ t('iqProbe.sets.enabledCount', { n: s.enabled_count }) }}）</option>
          </select>
        </div>
        <div>
          <label class="input-label">{{ t('iqProbe.monitorDialog.interval') }}</label>
          <input v-model.number="form.interval_minutes" type="number" min="10" class="input" />
          <p class="input-hint">{{ t('iqProbe.monitorDialog.intervalHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('iqProbe.monitorDialog.effort') }}</label>
          <select v-model="form.reasoning_effort" class="input">
            <option value="">{{ t('iqProbe.monitorDialog.effortDefault') }}</option>
            <option v-for="e in efforts" :key="e" :value="e">{{ e }}</option>
          </select>
          <p class="input-hint">{{ t('iqProbe.monitorDialog.effortHint') }}</p>
        </div>
        <div>
          <label class="input-label">{{ t('iqProbe.monitorDialog.maxTokens') }}</label>
          <input v-model.number="form.max_tokens" type="number" min="0" class="input" />
          <p class="input-hint">{{ t('iqProbe.monitorDialog.maxTokensHint') }}</p>
        </div>
        <label class="flex items-center gap-3 self-center">
          <Toggle v-model="form.enabled" />
          <span class="text-sm text-gray-700 dark:text-gray-300">{{ t('iqProbe.monitorDialog.enabled') }}</span>
        </label>
      </div>

      <div class="space-y-3 rounded-lg border border-gray-200 p-4 dark:border-dark-600">
        <label class="flex items-center gap-3">
          <Toggle v-model="form.alert_enabled" />
          <span class="text-sm font-medium text-gray-800 dark:text-gray-200">{{ t('iqProbe.monitorDialog.alert') }}</span>
        </label>
        <p class="text-xs text-gray-500 dark:text-gray-400">{{ t('iqProbe.monitorDialog.alertHint') }}</p>
        <div v-if="form.alert_enabled" class="grid gap-4 md:grid-cols-3">
          <div>
            <label class="input-label">{{ t('iqProbe.monitorDialog.threshold') }}</label>
            <input v-model.number="thresholdPercent" type="number" min="1" max="100" class="input" />
          </div>
          <div>
            <label class="input-label">{{ t('iqProbe.monitorDialog.lowRounds') }}</label>
            <input v-model.number="form.alert_low_rounds" type="number" min="1" class="input" />
          </div>
          <div>
            <label class="input-label">{{ t('iqProbe.monitorDialog.invalidRounds') }}</label>
            <input v-model.number="form.alert_invalid_rounds" type="number" min="1" class="input" />
          </div>
        </div>
      </div>
    </div>

    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="emit('close')">{{ t('common.cancel') }}</button>
        <button type="button" data-test="iq-monitor-save" class="btn btn-primary" :disabled="saving || !canSave" @click="save">
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
import { adminAPI } from '@/api/admin'
import { iqProbeAPI } from '@/api/admin/iqProbe'
import type { IQProbeMonitor, IQProbeMonitorInput, IQProbeQuestionSet } from '@/api/admin/iqProbe'
import { useAppStore } from '@/stores/app'
import { extractI18nErrorMessage } from '@/utils/apiError'

interface GroupOption {
  id: number
  name: string
  platform: string
}
interface AccountOption {
  id: number
  name: string
  platform: string
  type: string
  status: string
}

const props = defineProps<{
  show: boolean
  /** null = create */
  editing: IQProbeMonitor | null
  groups: GroupOption[]
  sets: IQProbeQuestionSet[]
}>()
const emit = defineEmits<{ (e: 'close'): void; (e: 'saved'): void }>()

const { t } = useI18n()
const appStore = useAppStore()
const efforts = ['minimal', 'low', 'medium', 'high', 'xhigh']

const groupId = ref<number | null>(null)
const groupAccounts = ref<AccountOption[]>([])
const accountsLoading = ref(false)
const accountIds = ref<number[]>([])
const modelOptions = ref<string[]>([])
const saving = ref(false)
// A late reply for an earlier group / account must not overwrite the current one.
let accountsSeq = 0
let modelsSeq = 0

const blank = (): IQProbeMonitorInput => ({
  group_id: null,
  model: '',
  set_id: 0,
  interval_minutes: 60,
  reasoning_effort: '',
  max_tokens: 0,
  enabled: true,
  alert_enabled: false,
  alert_threshold: 0.7,
  alert_low_rounds: 2,
  alert_invalid_rounds: 3,
})
const form = reactive<IQProbeMonitorInput>(blank())

const thresholdPercent = computed({
  get: () => Math.round(form.alert_threshold * 100),
  set: (v: number) => {
    form.alert_threshold = Math.min(100, Math.max(1, Number(v) || 70)) / 100
  },
})

const allChecked = computed(() => groupAccounts.value.length > 0 && accountIds.value.length === groupAccounts.value.length)
const canSave = computed(() => form.model !== '' && form.set_id > 0 && (props.editing !== null || accountIds.value.length > 0))

watch(
  () => props.show,
  (open) => {
    if (!open) return
    accountsSeq++
    modelsSeq++
    accountsLoading.value = false
    Object.assign(form, blank())
    groupId.value = null
    groupAccounts.value = []
    accountIds.value = []
    modelOptions.value = []
    const m = props.editing
    if (m) {
      Object.assign(form, {
        group_id: m.group_id,
        model: m.model,
        set_id: m.set_id,
        interval_minutes: m.interval_minutes,
        reasoning_effort: m.reasoning_effort,
        max_tokens: m.max_tokens,
        enabled: m.enabled,
        alert_enabled: m.alert_enabled,
        alert_threshold: m.alert_threshold,
        alert_low_rounds: m.alert_low_rounds,
        alert_invalid_rounds: m.alert_invalid_rounds,
      })
      void loadModels(m.account_id)
    } else if (props.sets.length === 1) {
      form.set_id = props.sets[0].id
    }
  }
)

async function loadGroupAccounts() {
  const seq = ++accountsSeq
  modelsSeq++
  accountIds.value = []
  groupAccounts.value = []
  modelOptions.value = []
  if (groupId.value === null) {
    accountsLoading.value = false
    return
  }
  accountsLoading.value = true
  try {
    const res = await adminAPI.accounts.list(1, 500, { group: String(groupId.value), lite: '1' })
    if (seq !== accountsSeq) return
    groupAccounts.value = (res.items ?? []).map((a) => ({ id: a.id, name: a.name, platform: a.platform, type: a.type, status: a.status }))
  } catch (err) {
    if (seq === accountsSeq) appStore.showError(extractI18nErrorMessage(err, t, 'iqProbe.errors', t('common.error')))
  } finally {
    if (seq === accountsSeq) accountsLoading.value = false
  }
}

function toggleAll() {
  accountIds.value = allChecked.value ? [] : groupAccounts.value.map((a) => a.id)
  onAccountsChanged()
}

function onAccountsChanged() {
  const first = accountIds.value[0]
  if (first !== undefined) void loadModels(first)
  else {
    modelsSeq++
    modelOptions.value = []
  }
}

async function loadModels(accountId: number) {
  const seq = ++modelsSeq
  try {
    const models = await adminAPI.accounts.getAvailableModels(accountId)
    if (seq === modelsSeq) modelOptions.value = models.map((m) => m.id)
  } catch {
    if (seq === modelsSeq) modelOptions.value = []
  }
}

async function save() {
  saving.value = true
  try {
    if (props.editing) {
      await iqProbeAPI.updateMonitor(props.editing.id, { ...form })
      appStore.showSuccess(t('iqProbe.actions.saved'))
    } else {
      const res = (await iqProbeAPI.createMonitors({ ...form, group_id: groupId.value, account_ids: accountIds.value })).data
      const skipped = res.skipped_account_ids?.length ?? 0
      appStore.showSuccess(t('iqProbe.monitorDialog.created', { created: res.created?.length ?? 0, skipped }))
    }
    emit('saved')
  } catch (err) {
    appStore.showError(extractI18nErrorMessage(err, t, 'iqProbe.errors', t('common.error')))
  } finally {
    saving.value = false
  }
}
</script>
