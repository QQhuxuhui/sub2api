<script setup lang="ts">
import { ref, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { opsAPI } from '@/api/admin/ops'
import type { NotifyChannel, NotifyChannelSettings } from '@/api/admin/ops'
import BaseDialog from '@/components/common/BaseDialog.vue'
import Select from '@/components/common/Select.vue'

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const config = ref<NotifyChannelSettings | null>(null)

const showEditor = ref(false)
const saving = ref(false)
const draft = ref<NotifyChannelSettings | null>(null)
const statusCodesInput = ref('')
const testingId = ref('')

const typeOptions = [
  { value: 'feishu', label: t('admin.ops.notifyChannels.typeFeishu') },
  { value: 'webhook', label: t('admin.ops.notifyChannels.typeWebhook') }
]

const severityOptions = [
  { value: '', label: t('admin.ops.notifyChannels.minSeverityAll') },
  { value: 'critical', label: t('common.critical') },
  { value: 'warning', label: t('common.warning') },
  { value: 'info', label: t('common.info') }
]

async function loadConfig() {
  loading.value = true
  try {
    config.value = await opsAPI.getNotifyChannelConfig()
  } catch (err: any) {
    console.error('[OpsNotifyChannelsCard] Failed to load config', err)
    appStore.showError(err?.response?.data?.message || t('admin.ops.notifyChannels.loadFailed'))
  } finally {
    loading.value = false
  }
}

function openEditor() {
  if (!config.value) return
  draft.value = JSON.parse(JSON.stringify(config.value)) as NotifyChannelSettings
  statusCodesInput.value = (draft.value.critical_error.status_codes || []).join(', ')
  showEditor.value = true
}

function addChannel() {
  if (!draft.value) return
  draft.value.channels.push({
    id: '',
    name: '',
    type: 'feishu',
    enabled: true,
    webhook_url: '',
    secret: '',
    secret_configured: false,
    min_severity: '',
    rate_limit_per_hour: 30,
    notify_resolved: true,
    timeout_seconds: 5
  })
}

function removeChannel(index: number) {
  if (!draft.value) return
  draft.value.channels.splice(index, 1)
}

function parseStatusCodes(): number[] {
  return statusCodesInput.value
    .split(/[,，\s]+/)
    .map((s) => parseInt(s.trim(), 10))
    .filter((n) => Number.isFinite(n) && n >= 100 && n <= 599)
}

async function saveConfig() {
  if (!draft.value) return
  draft.value.critical_error.status_codes = parseStatusCodes()
  saving.value = true
  try {
    config.value = await opsAPI.updateNotifyChannelConfig(draft.value)
    showEditor.value = false
    appStore.showSuccess(t('admin.ops.notifyChannels.saveSuccess'))
  } catch (err: any) {
    console.error('[OpsNotifyChannelsCard] Failed to save config', err)
    appStore.showError(err?.response?.data?.message || t('admin.ops.notifyChannels.saveFailed'))
  } finally {
    saving.value = false
  }
}

async function testChannel(ch: NotifyChannel) {
  testingId.value = ch.id || ch.name
  try {
    await opsAPI.testNotifyChannel(ch.id ? { channel_id: ch.id } : { channel: ch })
    appStore.showSuccess(t('admin.ops.notifyChannels.testSuccess'))
  } catch (err: any) {
    console.error('[OpsNotifyChannelsCard] Test send failed', err)
    appStore.showError(err?.response?.data?.message || t('admin.ops.notifyChannels.testFailed'))
  } finally {
    testingId.value = ''
  }
}

onMounted(() => {
  loadConfig()
})
</script>

<template>
  <div class="rounded-3xl bg-white p-6 shadow-sm ring-1 ring-gray-900/5 dark:bg-dark-800 dark:ring-dark-700">
    <div class="mb-4 flex items-start justify-between gap-4">
      <div>
        <h3 class="text-sm font-bold text-gray-900 dark:text-white">{{ t('admin.ops.notifyChannels.title') }}</h3>
        <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.notifyChannels.description') }}</p>
      </div>
      <button class="btn btn-sm btn-secondary" :disabled="!config" @click="openEditor">{{ t('common.edit') }}</button>
    </div>

    <div v-if="loading" class="py-4 text-sm text-gray-500 dark:text-gray-400">{{ t('common.loading') }}</div>
    <template v-else-if="config">
      <div v-if="config.channels.length === 0" class="py-2 text-sm text-gray-500 dark:text-gray-400">
        {{ t('admin.ops.notifyChannels.noChannels') }}
      </div>
      <ul v-else class="divide-y divide-gray-100 dark:divide-dark-700">
        <li v-for="ch in config.channels" :key="ch.id" class="flex items-center justify-between gap-3 py-2">
          <div class="min-w-0">
            <div class="flex items-center gap-2">
              <span class="truncate text-sm font-medium text-gray-900 dark:text-white">{{ ch.name }}</span>
              <span class="rounded-full bg-gray-100 px-2 py-0.5 text-xs text-gray-600 dark:bg-dark-700 dark:text-gray-300">
                {{ ch.type === 'feishu' ? t('admin.ops.notifyChannels.typeFeishu') : t('admin.ops.notifyChannels.typeWebhook') }}
              </span>
              <span
                class="rounded-full px-2 py-0.5 text-xs"
                :class="ch.enabled ? 'bg-green-100 text-green-700 dark:bg-green-900/40 dark:text-green-300' : 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'"
              >
                {{ ch.enabled ? t('common.enabled') : t('common.disabled') }}
              </span>
            </div>
            <div class="mt-0.5 truncate text-xs text-gray-500 dark:text-gray-400">{{ ch.webhook_url }}</div>
          </div>
          <button class="btn btn-sm btn-secondary shrink-0" :disabled="testingId !== ''" @click="testChannel(ch)">
            {{ testingId === (ch.id || ch.name) ? t('admin.ops.notifyChannels.testing') : t('admin.ops.notifyChannels.testSend') }}
          </button>
        </li>
      </ul>
      <div class="mt-3 border-t border-gray-100 pt-3 text-xs text-gray-500 dark:border-dark-700 dark:text-gray-400">
        {{ t('admin.ops.notifyChannels.criticalErrorTitle') }}:
        <span :class="config.critical_error.enabled ? 'text-green-600 dark:text-green-400' : ''">
          {{ config.critical_error.enabled ? t('common.enabled') : t('common.disabled') }}
        </span>
        <template v-if="config.critical_error.enabled">
          · {{ t('admin.ops.notifyChannels.statusCodes') }}: {{ config.critical_error.status_codes.join(', ') }}
          · {{ t('admin.ops.notifyChannels.cooldownMinutes') }}: {{ config.critical_error.cooldown_minutes }}
        </template>
      </div>
    </template>

    <BaseDialog :show="showEditor" :title="t('admin.ops.notifyChannels.title')" width="extra-wide" @close="showEditor = false">
      <div v-if="draft" class="space-y-5">
        <div class="flex items-center justify-between">
          <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.ops.notifyChannels.channelsTitle') }}</h4>
          <button class="btn btn-sm btn-secondary" @click="addChannel">{{ t('admin.ops.notifyChannels.addChannel') }}</button>
        </div>

        <div
          v-for="(ch, i) in draft.channels"
          :key="i"
          class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-600"
        >
          <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
            <div>
              <div class="mb-1 text-xs font-medium text-gray-600 dark:text-gray-300">{{ t('admin.ops.notifyChannels.name') }}</div>
              <input v-model="ch.name" type="text" class="input" />
            </div>
            <div>
              <div class="mb-1 text-xs font-medium text-gray-600 dark:text-gray-300">{{ t('admin.ops.notifyChannels.type') }}</div>
              <Select v-model="ch.type" :options="typeOptions" />
            </div>
            <div class="md:col-span-2">
              <div class="mb-1 text-xs font-medium text-gray-600 dark:text-gray-300">{{ t('admin.ops.notifyChannels.webhookUrl') }}</div>
              <input
                v-model="ch.webhook_url"
                type="text"
                class="input"
                :placeholder="ch.type === 'feishu' ? 'https://open.feishu.cn/open-apis/bot/v2/hook/...' : 'https://...'"
              />
            </div>
            <div>
              <div class="mb-1 text-xs font-medium text-gray-600 dark:text-gray-300">{{ t('admin.ops.notifyChannels.secret') }}</div>
              <input
                v-model="ch.secret"
                type="password"
                class="input"
                :placeholder="ch.secret_configured ? t('admin.ops.notifyChannels.secretPlaceholderConfigured') : t('admin.ops.notifyChannels.secretPlaceholder')"
              />
            </div>
            <div>
              <div class="mb-1 text-xs font-medium text-gray-600 dark:text-gray-300">{{ t('admin.ops.notifyChannels.minSeverity') }}</div>
              <Select v-model="ch.min_severity" :options="severityOptions" />
            </div>
            <div>
              <div class="mb-1 text-xs font-medium text-gray-600 dark:text-gray-300">{{ t('admin.ops.notifyChannels.rateLimitPerHour') }}</div>
              <input v-model.number="ch.rate_limit_per_hour" type="number" min="0" max="100000" class="input" />
            </div>
            <div>
              <div class="mb-1 text-xs font-medium text-gray-600 dark:text-gray-300">{{ t('admin.ops.notifyChannels.timeoutSeconds') }}</div>
              <input v-model.number="ch.timeout_seconds" type="number" min="1" max="30" class="input" />
            </div>
          </div>
          <div class="flex items-center justify-between">
            <div class="flex items-center gap-4">
              <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                <input v-model="ch.enabled" type="checkbox" class="h-4 w-4 rounded border-gray-300" />
                <span>{{ t('common.enabled') }}</span>
              </label>
              <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                <input v-model="ch.notify_resolved" type="checkbox" class="h-4 w-4 rounded border-gray-300" />
                <span>{{ t('admin.ops.notifyChannels.notifyResolved') }}</span>
              </label>
            </div>
            <button class="btn btn-sm btn-danger" @click="removeChannel(i)">{{ t('common.delete') }}</button>
          </div>
        </div>

        <div class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-600">
          <div>
            <h4 class="text-sm font-semibold text-gray-900 dark:text-white">{{ t('admin.ops.notifyChannels.criticalErrorTitle') }}</h4>
            <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.notifyChannels.criticalErrorDescription') }}</p>
          </div>
          <label class="inline-flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
            <input v-model="draft.critical_error.enabled" type="checkbox" class="h-4 w-4 rounded border-gray-300" />
            <span>{{ t('common.enabled') }}</span>
          </label>
          <div class="grid grid-cols-1 gap-3 md:grid-cols-2">
            <div>
              <div class="mb-1 text-xs font-medium text-gray-600 dark:text-gray-300">{{ t('admin.ops.notifyChannels.statusCodes') }}</div>
              <input v-model="statusCodesInput" type="text" class="input" placeholder="401, 403, 529" />
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('admin.ops.notifyChannels.statusCodesHint') }}</p>
            </div>
            <div>
              <div class="mb-1 text-xs font-medium text-gray-600 dark:text-gray-300">{{ t('admin.ops.notifyChannels.cooldownMinutes') }}</div>
              <input v-model.number="draft.critical_error.cooldown_minutes" type="number" min="0" max="1440" class="input" />
            </div>
          </div>
        </div>
      </div>

      <template #footer>
        <div class="flex justify-end gap-2">
          <button class="btn btn-secondary" @click="showEditor = false">{{ t('common.cancel') }}</button>
          <button class="btn btn-primary" :disabled="saving" @click="saveConfig">
            {{ saving ? t('common.saving') : t('common.save') }}
          </button>
        </div>
      </template>
    </BaseDialog>
  </div>
</template>
