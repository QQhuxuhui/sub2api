<template>
  <div class="grid gap-4 md:grid-cols-2">
    <div>
      <label class="input-label">{{ t('intentRouter.jev.scopeUsers') }}</label>
      <div class="relative">
        <input v-model="userQuery" :data-test="`${testPrefix}-user-search`" type="text" class="input" :placeholder="t('intentRouter.jev.scopeUserSearch')" @input="searchUsers" />
        <div v-if="userResults.length" class="absolute z-10 mt-1 max-h-60 w-full overflow-auto rounded-lg border border-gray-200 bg-white shadow-lg dark:border-dark-600 dark:bg-dark-800">
          <button
            v-for="u in userResults"
            :key="u.id"
            type="button"
            :data-test="`${testPrefix}-user-option-${u.id}`"
            class="block w-full px-3 py-2 text-left text-sm hover:bg-gray-50 dark:hover:bg-dark-700"
            @click="addUser(u)"
          >#{{ u.id }} {{ u.email }}</button>
        </div>
      </div>
      <div class="mt-2 flex flex-wrap gap-2">
        <span v-for="u in users" :key="u.id" :data-test="`${testPrefix}-user-${u.id}`" class="inline-flex items-center gap-1 rounded-full bg-gray-100 px-2 py-0.5 text-xs dark:bg-dark-700">
          #{{ u.id }} {{ u.email || t('intentRouter.jev.scopeDeleted') }}
          <button type="button" class="text-gray-500 hover:text-red-600" :aria-label="t('intentRouter.jev.scopeRemove')" @click="removeUser(u.id)">×</button>
        </span>
      </div>
    </div>
    <div>
      <label class="input-label">{{ t('intentRouter.jev.scopeKeys') }}</label>
      <div class="relative">
        <input
          v-model="keyQuery"
          :data-test="`${testPrefix}-key-search`"
          type="text"
          class="input"
          :disabled="!users.length"
          :placeholder="users.length ? t('intentRouter.jev.scopeKeyPlaceholder') : t('intentRouter.jev.scopeKeyChooseUserFirst')"
          @focus="openKeyPicker"
          @input="searchKeys"
        />
        <div v-if="keyPickerOpen && users.length" :data-test="`${testPrefix}-key-results`" class="absolute z-10 mt-1 max-h-72 w-full overflow-auto rounded-lg border border-gray-200 bg-white shadow-lg dark:border-dark-600 dark:bg-dark-800">
          <template v-for="u in users" :key="u.id">
            <div class="bg-gray-50 px-3 py-1 text-xs text-gray-500 dark:bg-dark-700 dark:text-gray-400">#{{ u.id }} {{ u.email }}</div>
            <button
              v-for="k in keyChoices(u.id)"
              :key="k.id"
              type="button"
              :data-test="`${testPrefix}-key-option-${k.id}`"
              class="block w-full px-3 py-1.5 text-left text-sm hover:bg-gray-50 dark:hover:bg-dark-700"
              @click="addKey(k)"
            >{{ k.name }}（#{{ k.id }}）</button>
            <p v-if="!keyChoices(u.id).length" class="px-3 py-1.5 text-xs text-gray-400">{{ t('intentRouter.jev.scopeKeyNone') }}</p>
            <p v-else-if="(userKeys[u.id] ?? []).length >= KEY_PAGE" class="px-3 py-1 text-xs text-gray-400">{{ t('intentRouter.jev.scopeKeyMore', { n: KEY_PAGE }) }}</p>
          </template>
          <button type="button" class="block w-full border-t border-gray-100 px-3 py-1.5 text-right text-xs text-gray-500 hover:text-gray-700 dark:border-dark-600" @click="keyPickerOpen = false">{{ t('intentRouter.jev.scopeKeyClose') }}</button>
        </div>
      </div>
      <div class="mt-2 flex flex-wrap gap-2">
        <span v-for="k in keys" :key="k.id" :data-test="`${testPrefix}-key-${k.id}`" class="inline-flex items-center gap-1 rounded-full bg-gray-100 px-2 py-0.5 text-xs dark:bg-dark-700">
          {{ k.name || t('intentRouter.jev.scopeDeleted') }}（#{{ k.id }} · {{ userEmail(k.user_id) }}）
          <button type="button" class="text-gray-500 hover:text-red-600" :aria-label="t('intentRouter.jev.scopeRemove')" @click="removeKey(k.id)">×</button>
        </span>
      </div>
      <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.scopeKeysHint') }}</p>
    </div>
  </div>
</template>

<script setup lang="ts">
// Users and, optionally, some of their API keys — picked by searching, so
// nobody has to look IDs up. A user without listed keys means all their keys.
import { ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { adminAPI } from '@/api/admin'

export interface ScopeUser { id: number; email: string }
export interface ScopeKey { id: number; name: string; user_id: number }

const props = defineProps<{ users: ScopeUser[]; keys: ScopeKey[]; testPrefix: string }>()
const emit = defineEmits<{ 'update:users': [ScopeUser[]]; 'update:keys': [ScopeKey[]] }>()
const { t } = useI18n()

const userQuery = ref('')
const userResults = ref<ScopeUser[]>([])
const userKeys = ref<Record<number, ScopeKey[]>>({})
let userSearchTimer: ReturnType<typeof setTimeout> | null = null
let userSearchSeq = 0
function searchUsers() {
  if (userSearchTimer) clearTimeout(userSearchTimer)
  const query = userQuery.value.trim()
  const seq = ++userSearchSeq
  if (!query) {
    userResults.value = []
    return
  }
  userSearchTimer = setTimeout(async () => {
    try {
      const found = await adminAPI.usage.searchUsers(query)
      if (seq === userSearchSeq) userResults.value = found.filter((u) => !u.deleted && !props.users.some((x) => x.id === u.id))
    } catch {
      if (seq === userSearchSeq) userResults.value = []
    }
  }, 250)
}
function addUser(u: ScopeUser) {
  if (!props.users.some((x) => x.id === u.id)) emit('update:users', [...props.users, { id: u.id, email: u.email }])
  userQuery.value = ''
  userResults.value = []
}
function removeUser(id: number) {
  emit('update:users', props.users.filter((u) => u.id !== id))
  emit('update:keys', props.keys.filter((k) => k.user_id !== id))
}
// The key search returns a user's 30 newest keys matching the name typed
// (all of them when nothing is typed); older keys are found by name.
const KEY_PAGE = 30
const keyQuery = ref('')
const keyPickerOpen = ref(false)
let keySearchTimer: ReturnType<typeof setTimeout> | null = null
let keySearchSeq = 0
function openKeyPicker() {
  keyPickerOpen.value = true
  searchKeys()
}
function searchKeys() {
  if (keySearchTimer) clearTimeout(keySearchTimer)
  const seq = ++keySearchSeq
  const query = keyQuery.value.trim()
  keySearchTimer = setTimeout(async () => {
    const results = await Promise.all(props.users.map(async (u) => {
      try {
        return [u.id, await adminAPI.usage.searchApiKeys(u.id, query || undefined)] as const
      } catch {
        return [u.id, []] as const
      }
    }))
    if (seq !== keySearchSeq) return
    const next = { ...userKeys.value }
    for (const [id, found] of results) next[id] = [...found]
    userKeys.value = next
  }, 250)
}
function keyChoices(userId: number) {
  return (userKeys.value[userId] ?? []).filter((k) => !props.keys.some((x) => x.id === k.id))
}
function addKey(key: ScopeKey) {
  if (!props.keys.some((k) => k.id === key.id)) emit('update:keys', [...props.keys, { id: key.id, name: key.name, user_id: key.user_id }])
}
function removeKey(id: number) {
  emit('update:keys', props.keys.filter((k) => k.id !== id))
}
function userEmail(userId: number) {
  return props.users.find((u) => u.id === userId)?.email || `#${userId}`
}
</script>
