<template>
  <BaseDialog
    :show="show"
    :title="t('admin.users.emptyResponseBilling.title')"
    width="wide"
    @close="$emit('close')"
  >
    <div v-if="user" class="space-y-4">
      <p class="text-sm text-gray-600 dark:text-gray-400">
        {{ t('admin.users.emptyResponseBilling.subtitle', { email: user.email }) }}
      </p>
      <div v-if="loading" class="py-10 text-center text-gray-500">{{ t('common.loading') }}</div>
      <div v-else>
        <div v-if="rules.length === 0" class="rounded-xl border border-dashed border-gray-300 px-4 py-8 text-center text-sm text-gray-500 dark:border-dark-600">
          {{ t('admin.users.emptyResponseBilling.empty') }}
        </div>
        <div v-else class="overflow-x-auto">
          <table class="min-w-full text-sm">
            <thead>
              <tr class="border-b border-gray-200 text-gray-700 dark:border-dark-700 dark:text-gray-300">
                <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.emptyResponseBilling.columns.group') }}</th>
                <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.emptyResponseBilling.columns.model') }}</th>
                <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.emptyResponseBilling.columns.enabled') }}</th>
                <th class="px-3 py-2 text-left font-medium">{{ t('admin.users.emptyResponseBilling.columns.note') }}</th>
                <th class="px-3 py-2"></th>
              </tr>
            </thead>
            <tbody>
              <tr v-for="(row, idx) in rules" :key="idx" class="border-b border-gray-100 dark:border-dark-800">
                <td class="px-3 py-2">
                  <select v-model="row.group_id" class="input w-44">
                    <option :value="null">{{ t('admin.users.emptyResponseBilling.allGroups') }}</option>
                    <option v-for="g in allGroups" :key="g.id" :value="g.id">{{ g.name }}</option>
                  </select>
                </td>
                <td class="px-3 py-2">
                  <input
                    v-model="row.model"
                    type="text"
                    class="input w-56 font-mono"
                    maxlength="200"
                    :placeholder="t('admin.users.emptyResponseBilling.modelPlaceholder')"
                  />
                </td>
                <td class="px-3 py-2">
                  <input v-model="row.enabled" type="checkbox" class="h-4 w-4 rounded border-gray-300" />
                </td>
                <td class="px-3 py-2">
                  <input
                    v-model="row.note"
                    type="text"
                    class="input w-40"
                    :placeholder="t('admin.users.emptyResponseBilling.notePlaceholder')"
                  />
                </td>
                <td class="px-3 py-2 text-right">
                  <button
                    type="button"
                    class="text-xs text-red-500 hover:text-red-600"
                    @click="rules.splice(idx, 1)"
                  >{{ t('admin.users.emptyResponseBilling.remove') }}</button>
                </td>
              </tr>
            </tbody>
          </table>
        </div>
        <div class="mt-3">
          <button type="button" class="btn btn-secondary text-sm" @click="addRule">
            + {{ t('admin.users.emptyResponseBilling.addRule') }}
          </button>
        </div>
        <p class="mt-3 text-xs text-gray-500">{{ t('admin.users.emptyResponseBilling.hint') }}</p>
      </div>
    </div>
    <template #footer>
      <div class="flex justify-end gap-3">
        <button type="button" class="btn btn-secondary" @click="$emit('close')">
          {{ t('admin.users.emptyResponseBilling.cancel') }}
        </button>
        <button type="button" class="btn btn-primary" :disabled="submitting || loading" @click="onSave">
          {{ submitting ? t('admin.users.emptyResponseBilling.saving') : t('admin.users.emptyResponseBilling.save') }}
        </button>
      </div>
    </template>
  </BaseDialog>
</template>

<script setup lang="ts">
import { ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import type { EmptyResponseBillingRuleInput } from '@/api/admin/users'
import type { AdminUser, AdminGroup } from '@/types'
import BaseDialog from '@/components/common/BaseDialog.vue'

const props = defineProps<{ show: boolean; user: AdminUser | null; allGroups: AdminGroup[] }>()
const emit = defineEmits(['close', 'success'])

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(false)
const submitting = ref(false)
const rules = ref<EmptyResponseBillingRuleInput[]>([])

function addRule() {
  rules.value.push({ group_id: null, model: '', enabled: true, note: '' })
}

async function load() {
  if (!props.user) return
  loading.value = true
  try {
    const data = await adminAPI.users.getEmptyResponseBillingRules(props.user.id)
    rules.value = (data.rules || []).map((r) => ({
      group_id: r.group_id,
      model: r.model,
      enabled: r.enabled,
      note: r.note || '',
    }))
  } catch {
    appStore.showError(t('admin.users.emptyResponseBilling.loadFailed'))
    rules.value = []
  } finally {
    loading.value = false
  }
}

watch(
  () => props.show,
  (s) => { if (s && props.user) load() },
)

async function onSave() {
  if (!props.user) return
  // 与后端唯一索引同口径的前置查重：(group_id 折叠 null, 模型名小写)。
  // 在这里拦下能给出可读提示，而不是等后端 400。
  const seen = new Set<string>()
  for (const r of rules.value) {
    const key = `${r.group_id ?? 0}|${r.model.trim().toLowerCase()}`
    if (seen.has(key)) {
      appStore.showError(t('admin.users.emptyResponseBilling.duplicate'))
      return
    }
    seen.add(key)
  }
  submitting.value = true
  try {
    const payload = rules.value.map((r) => ({
      group_id: r.group_id,
      model: r.model.trim(),
      enabled: r.enabled,
      note: r.note.trim(),
    }))
    await adminAPI.users.updateEmptyResponseBillingRules(props.user.id, payload)
    appStore.showSuccess(t('admin.users.emptyResponseBilling.updateSuccess'))
    emit('success')
    emit('close')
  } catch (e: any) {
    appStore.showError(e?.response?.data?.message || t('admin.users.emptyResponseBilling.updateFailed'))
  } finally {
    submitting.value = false
  }
}
</script>
