<template>
  <AppLayout>
    <div class="space-y-6">
      <div>
        <h1 class="text-2xl font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.title') }}</h1>
        <p class="mt-1 max-w-4xl text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.description') }}</p>
        <p class="mt-2 max-w-4xl rounded-lg bg-gray-50 p-3 text-sm leading-6 text-gray-600 dark:bg-dark-800 dark:text-gray-300">
          {{ t('intentRouter.howItWorks') }}
        </p>
      </div>

      <div v-if="loading" class="flex items-center justify-center py-16">
        <div class="h-8 w-8 animate-spin rounded-full border-b-2 border-primary-600"></div>
      </div>

      <div v-else class="grid gap-6 lg:grid-cols-[18rem_minmax(0,1fr)]">
        <!-- Groups -->
        <aside class="card h-fit p-3">
          <p class="px-2 pb-2 text-sm font-medium text-gray-700 dark:text-gray-300">{{ t('intentRouter.groups') }}</p>
          <input v-model.trim="groupSearch" type="search" class="input mb-2" :placeholder="t('intentRouter.searchGroups')" />
          <p v-if="filteredGroups.length === 0" class="px-2 py-4 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.noGroups') }}</p>
          <ul class="max-h-[32rem] space-y-1 overflow-y-auto">
            <li v-for="group in filteredGroups" :key="group.id">
              <button
                type="button"
                :data-test="`intent-group-${group.id}`"
                :class="[
                  'flex w-full items-center justify-between gap-2 rounded-lg px-2 py-2 text-left text-sm transition-colors',
                  selectedGroupId === group.id
                    ? 'bg-primary-50 text-primary-700 dark:bg-primary-900/30 dark:text-primary-200'
                    : 'text-gray-700 hover:bg-gray-50 dark:text-gray-300 dark:hover:bg-dark-700',
                ]"
                @click="selectGroup(group.id)"
              >
                <span class="min-w-0">
                  <span class="block truncate font-medium">{{ group.name }}</span>
                  <span class="block text-xs text-gray-500 dark:text-gray-400">{{ group.platform }}</span>
                </span>
                <span :class="['flex-shrink-0 rounded-full px-2 py-0.5 text-xs', statusClass(group.id)]">
                  {{ t(`intentRouter.status.${routerStatus(group.id)}`) }}
                </span>
              </button>
            </li>
          </ul>
          <button type="button" class="btn btn-secondary mt-3 w-full text-sm" :disabled="busy" @click="clearCache(0)">
            {{ t('intentRouter.actions.clearCacheAll') }}
          </button>
        </aside>

        <!-- Editor -->
        <section v-if="selectedGroup" class="min-w-0 space-y-6">
          <div class="card space-y-5 p-5">
            <label class="flex items-start gap-3">
              <input v-model="form.enabled" data-test="intent-enabled" type="checkbox" class="mt-1 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
              <span>
                <span class="block text-sm font-medium text-gray-900 dark:text-white">{{ t('intentRouter.enabled') }} — {{ selectedGroup.name }}</span>
                <span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.enabledHint') }}</span>
              </span>
            </label>
            <div>
              <label class="input-label">{{ t('intentRouter.excludedModels') }}</label>
              <textarea v-model="form.excludedModelsText" data-test="intent-excluded-models" rows="2" class="input font-mono text-sm" :placeholder="t('intentRouter.excludedModelsPlaceholder')"></textarea>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.excludedModelsHint') }}</p>
            </div>
            <div>
              <label class="input-label">{{ t('intentRouter.keywordMaxChars') }}</label>
              <input v-model.number="form.keyword_max_chars" data-test="intent-keyword-max-chars" type="number" min="0" max="1000000" step="1" class="input w-40" />
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.keywordMaxCharsHint') }}</p>
            </div>
            <div>
              <label class="input-label">{{ t('intentRouter.unavailableStatus') }}</label>
              <select v-model.number="form.unavailable_status" data-test="intent-unavailable-status" class="input w-40">
                <option :value="503">503</option>
                <option :value="502">502</option>
                <option :value="429">429</option>
              </select>
              <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.unavailableStatusHint') }}</p>
            </div>

          </div>

          <!-- Jev probe detection -->
          <div class="card space-y-4 p-5" data-test="intent-jev">
            <label class="flex items-start gap-3">
              <input v-model="form.jev.enabled" data-test="intent-jev-enabled" type="checkbox" class="mt-1 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
              <span>
                <span class="block text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.jev.title') }}</span>
                <span class="mt-1 block text-sm leading-6 text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.intro') }}</span>
              </span>
            </label>
            <div v-if="jevStatus" data-test="intent-jev-status" class="space-y-3 rounded-lg bg-gray-50 p-3 text-sm dark:bg-dark-800">
              <div class="flex flex-wrap items-center gap-2">
                <span :class="['rounded-full px-2 py-0.5 text-xs', jevStateClass(jevStatus.state)]">{{ t(`intentRouter.jev.status.${jevStatus.state}`) }}</span>
                <span v-if="jevStatus.state === 'normal' && jevStatus.last_success_at" class="text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.status.lastSuccess', { time: formatTime(jevStatus.last_success_at) }) }}</span>
                <span v-if="jevStatus.state === 'paused'" class="text-amber-700 dark:text-amber-300">{{ t('intentRouter.jev.status.pausedUntil', { time: formatTime(jevStatus.paused_until ?? ''), reason: jevStatus.reason ?? '' }) }}</span>
                <button type="button" class="ml-auto text-xs text-primary-600 hover:underline dark:text-primary-400" @click="selectedGroupId && loadJevStatus(selectedGroupId)">{{ t('intentRouter.jev.status.refresh') }}</button>
              </div>
              <p v-if="jevStatus.state === 'auto_disabled'" data-test="intent-jev-auto-disabled" class="text-red-700 dark:text-red-300">
                {{ t('intentRouter.jev.status.autoDisabledAt', { time: jevStatus.auto_disabled_at ? formatTime(jevStatus.auto_disabled_at) : '—', status: jevStatus.auto_disabled_status ?? '—' }) }}
                {{ jevAutoDisabledHint(jevStatus.auto_disabled_status) }}
                <span class="block text-xs opacity-80">{{ jevStatus.auto_disabled_detail || jevStatus.reason }}</span>
              </p>
              <p v-if="jevStatus.balance" data-test="intent-jev-balance-estimate" :class="jevStatus.balance.low ? 'font-medium text-red-700 dark:text-red-300' : 'text-gray-700 dark:text-gray-300'">
                {{ t('intentRouter.jev.status.balance', { estimated: formatUSD(jevStatus.balance.estimated_usd), set: formatUSD(jevStatus.balance.set_usd), at: formatTime(jevStatus.balance.set_at), spent: formatUSD(jevStatus.balance.spent_usd) }) }}
                <span v-if="jevStatus.balance.low"> · {{ t('intentRouter.jev.status.lowBalance') }}</span>
              </p>
              <p v-else class="text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.status.noBalance') }}</p>
              <div class="overflow-x-auto">
                <table class="min-w-full text-xs">
                  <thead class="text-left text-gray-500 dark:text-gray-400">
                    <tr>
                      <th class="py-1 pr-4">{{ t('intentRouter.jev.status.day') }}</th>
                      <th class="py-1 pr-4">{{ t('intentRouter.jev.status.calls') }}</th>
                      <th class="py-1 pr-4">{{ t('intentRouter.jev.status.skipped') }}</th>
                      <th class="py-1 pr-4">{{ t('intentRouter.jev.status.probes') }}</th>
                      <th class="py-1 pr-4">{{ t('intentRouter.jev.status.health') }}</th>
                      <th class="py-1 pr-4">{{ t('intentRouter.jev.status.errors') }}</th>
                      <th class="py-1 pr-4">{{ t('intentRouter.jev.status.tokens') }}</th>
                      <th class="py-1">{{ t('intentRouter.jev.status.cost') }}</th>
                    </tr>
                  </thead>
                  <tbody class="text-gray-700 dark:text-gray-300">
                    <tr v-for="day in jevStatus.days" :key="day.date" :data-test="`intent-jev-day-${day.date}`">
                      <td class="py-1 pr-4">{{ formatDay(day.date) }}</td>
                      <td class="py-1 pr-4">{{ day.calls }}</td>
                      <td class="py-1 pr-4">{{ day.skipped }}</td>
                      <td class="py-1 pr-4">{{ day.probes }}</td>
                      <td class="py-1 pr-4">{{ day.health ?? 0 }}</td>
                      <td class="py-1 pr-4">{{ day.errors }}</td>
                      <td class="py-1 pr-4">{{ day.tokens }}</td>
                      <td class="py-1">{{ formatUSD(day.cost_usd) }} / {{ formatCNY(day.cost_usd) }}</td>
                    </tr>
                  </tbody>
                </table>
              </div>
            </div>
            <div class="grid gap-4 md:grid-cols-2">
              <div>
                <label class="input-label">{{ t('intentRouter.jev.rule') }}</label>
                <select v-model="form.jev.rule" data-test="intent-jev-rule" class="input">
                  <option value="">{{ t('intentRouter.jev.rulePlaceholder') }}</option>
                  <option v-for="name in ruleNames" :key="name" :value="name">{{ name }}</option>
                </select>
                <p v-if="jevRuleAccounts.length" data-test="intent-jev-rule-accounts" class="mt-1 text-xs text-gray-700 dark:text-gray-300">{{ t('intentRouter.jev.ruleAccounts', { accounts: jevRuleAccounts.map(accountLabel).join('、') }) }}</p>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.ruleHint') }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('intentRouter.jev.apiKey') }}</label>
                <input
                  v-model.trim="form.jev_api_key"
                  data-test="intent-jev-api-key"
                  type="password"
                  autocomplete="new-password"
                  class="input"
                  :placeholder="jevKeyConfigured ? t('intentRouter.classifier.apiKeyKeep') : t('intentRouter.jev.apiKeyPlaceholder')"
                />
              </div>
              <div>
                <label class="input-label">{{ t('intentRouter.jev.threshold') }}</label>
                <input v-model.number="form.jev.threshold" data-test="intent-jev-threshold" type="number" min="0.05" max="1" step="0.05" class="input" />
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.thresholdHint') }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('intentRouter.jev.maxTurns') }}</label>
                <input v-model.number="form.jev.max_turns" type="number" min="1" max="50" class="input" />
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.maxTurnsHint') }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('intentRouter.jev.timeout') }}</label>
                <input v-model.number="form.jev.timeout_ms" type="number" min="200" max="5000" step="100" class="input" />
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.timeoutHint') }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('intentRouter.jev.model') }}</label>
                <input v-model.trim="form.jev.model" type="text" class="input" placeholder="jev-latest" />
              </div>
            </div>
            <div class="space-y-3 rounded-lg border border-gray-200 p-3 dark:border-dark-600" data-test="intent-jev-health">
              <label class="flex items-start gap-3">
                <input v-model="form.jev.health_enabled" data-test="intent-jev-health-enabled" type="checkbox" class="mt-1 h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
                <span>
                  <span class="block text-sm font-medium text-gray-900 dark:text-white">{{ t('intentRouter.jev.health.enabled') }}</span>
                  <span class="block text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.health.enabledHint') }}</span>
                </span>
              </label>
              <div v-if="form.jev.health_enabled" class="grid gap-4 md:grid-cols-2">
                <div>
                  <label class="input-label">{{ t('intentRouter.jev.health.threshold') }}</label>
                  <input v-model.number="form.jev.health_threshold" data-test="intent-jev-health-threshold" type="number" min="0.01" max="1" step="0.05" class="input" />
                  <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.health.thresholdHint') }}</p>
                </div>
                <div>
                  <label class="input-label">{{ t('intentRouter.jev.health.rule') }}</label>
                  <select v-model="form.jev.health_rule" data-test="intent-jev-health-rule" class="input">
                    <option value="">{{ t('intentRouter.jev.health.ruleNone') }}</option>
                    <option v-for="name in ruleNames" :key="name" :value="name">{{ name }}</option>
                  </select>
                  <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.health.ruleHint') }}</p>
                </div>
              </div>
            </div>
            <div class="grid gap-4 md:grid-cols-3">
              <div>
                <label class="input-label">{{ t('intentRouter.jev.samplePercent') }}</label>
                <input v-model.number="form.jev.sample_percent" data-test="intent-jev-sample" type="number" min="1" max="100" class="input" />
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.samplePercentHint') }}</p>
              </div>
              <div>
                <label class="input-label">{{ t('intentRouter.jev.setBalance') }}</label>
                <input v-model="form.jevBalanceText" data-test="intent-jev-balance" type="number" min="0" step="0.01" class="input" :placeholder="t('intentRouter.jev.setBalancePlaceholder')" />
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.setBalanceHint') }} <a href="https://console.typesafe.ai" target="_blank" rel="noopener noreferrer" class="text-primary-600 hover:underline dark:text-primary-400">console.typesafe.ai</a></p>
              </div>
              <div>
                <label class="input-label">{{ t('intentRouter.jev.lowBalance') }}</label>
                <input v-model.number="form.jev.low_balance_usd" data-test="intent-jev-low-balance" type="number" min="0" step="0.5" class="input" />
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.lowBalanceHint') }}</p>
              </div>
            </div>
            <div>
              <label class="input-label">{{ t('intentRouter.jev.scope') }}</label>
              <div class="flex flex-wrap gap-4 text-sm text-gray-700 dark:text-gray-300">
                <label class="flex items-center gap-2"><input v-model="form.jev.scope" data-test="intent-jev-scope-all" type="radio" value="all" />{{ t('intentRouter.jev.scopeAll') }}</label>
                <label class="flex items-center gap-2"><input v-model="form.jev.scope" data-test="intent-jev-scope-listed" type="radio" value="listed" />{{ t('intentRouter.jev.scopeListed') }}</label>
              </div>
              <div v-if="form.jev.scope === 'listed'" class="mt-3 grid gap-4 md:grid-cols-2">
                <div>
                  <label class="input-label">{{ t('intentRouter.jev.scopeUsers') }}</label>
                  <div class="relative">
                    <input v-model="jevUserQuery" data-test="intent-jev-user-search" type="text" class="input" :placeholder="t('intentRouter.jev.scopeUserSearch')" @input="searchJevUsers" />
                    <div v-if="jevUserResults.length" class="absolute z-10 mt-1 max-h-60 w-full overflow-auto rounded-lg border border-gray-200 bg-white shadow-lg dark:border-dark-600 dark:bg-dark-800">
                      <button
                        v-for="u in jevUserResults"
                        :key="u.id"
                        type="button"
                        :data-test="`intent-jev-user-option-${u.id}`"
                        class="block w-full px-3 py-2 text-left text-sm hover:bg-gray-50 dark:hover:bg-dark-700"
                        @click="addJevUser(u)"
                      >#{{ u.id }} {{ u.email }}</button>
                    </div>
                  </div>
                  <div class="mt-2 flex flex-wrap gap-2">
                    <span v-for="u in form.jevUsers" :key="u.id" :data-test="`intent-jev-user-${u.id}`" class="inline-flex items-center gap-1 rounded-full bg-gray-100 px-2 py-0.5 text-xs dark:bg-dark-700">
                      #{{ u.id }} {{ u.email || t('intentRouter.jev.scopeDeleted') }}
                      <button type="button" class="text-gray-500 hover:text-red-600" :aria-label="t('intentRouter.jev.scopeRemove')" @click="removeJevUser(u.id)">×</button>
                    </span>
                  </div>
                </div>
                <div>
                  <label class="input-label">{{ t('intentRouter.jev.scopeKeys') }}</label>
                  <div class="relative">
                    <input
                      v-model="jevKeyQuery"
                      data-test="intent-jev-key-search"
                      type="text"
                      class="input"
                      :disabled="!form.jevUsers.length"
                      :placeholder="form.jevUsers.length ? t('intentRouter.jev.scopeKeyPlaceholder') : t('intentRouter.jev.scopeKeyChooseUserFirst')"
                      @focus="openJevKeyPicker"
                      @input="searchJevKeys"
                    />
                    <div v-if="jevKeyPickerOpen && form.jevUsers.length" data-test="intent-jev-key-results" class="absolute z-10 mt-1 max-h-72 w-full overflow-auto rounded-lg border border-gray-200 bg-white shadow-lg dark:border-dark-600 dark:bg-dark-800">
                      <template v-for="u in form.jevUsers" :key="u.id">
                        <div class="bg-gray-50 px-3 py-1 text-xs text-gray-500 dark:bg-dark-700 dark:text-gray-400">#{{ u.id }} {{ u.email }}</div>
                        <button
                          v-for="k in jevKeyChoices(u.id)"
                          :key="k.id"
                          type="button"
                          :data-test="`intent-jev-key-option-${k.id}`"
                          class="block w-full px-3 py-1.5 text-left text-sm hover:bg-gray-50 dark:hover:bg-dark-700"
                          @click="addJevKey(k)"
                        >{{ k.name }}（#{{ k.id }}）</button>
                        <p v-if="!jevKeyChoices(u.id).length" class="px-3 py-1.5 text-xs text-gray-400">{{ t('intentRouter.jev.scopeKeyNone') }}</p>
                        <p v-else-if="(jevUserKeys[u.id] ?? []).length >= JEV_KEY_PAGE" class="px-3 py-1 text-xs text-gray-400">{{ t('intentRouter.jev.scopeKeyMore', { n: JEV_KEY_PAGE }) }}</p>
                      </template>
                      <button type="button" class="block w-full border-t border-gray-100 px-3 py-1.5 text-right text-xs text-gray-500 hover:text-gray-700 dark:border-dark-600" @click="jevKeyPickerOpen = false">{{ t('intentRouter.jev.scopeKeyClose') }}</button>
                    </div>
                  </div>
                  <div class="mt-2 flex flex-wrap gap-2">
                    <span v-for="k in form.jevKeys" :key="k.id" :data-test="`intent-jev-key-${k.id}`" class="inline-flex items-center gap-1 rounded-full bg-gray-100 px-2 py-0.5 text-xs dark:bg-dark-700">
                      {{ k.name || t('intentRouter.jev.scopeDeleted') }}（#{{ k.id }} · {{ jevUserEmail(k.user_id) }}）
                      <button type="button" class="text-gray-500 hover:text-red-600" :aria-label="t('intentRouter.jev.scopeRemove')" @click="removeJevKey(k.id)">×</button>
                    </span>
                  </div>
                  <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.jev.scopeKeysHint') }}</p>
                </div>
              </div>
            </div>
          </div>

          <!-- Answering channel-monitor challenges: the group's own setting, saved on its own -->
          <MonitorAnswerCard :group-id="selectedGroup.id" />

          <!-- Rules -->
          <div class="card space-y-4 p-5">
            <div class="flex flex-wrap items-start justify-between gap-3">
              <div>
                <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.rules.title') }}</h2>
                <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.rules.intro') }}</p>
              </div>
              <button type="button" data-test="intent-add-rule" class="btn btn-secondary text-sm" @click="addRule">{{ t('intentRouter.rules.add') }}</button>
            </div>
            <p v-if="form.rules.length === 0" class="rounded-lg border border-dashed border-gray-300 p-4 text-sm text-gray-500 dark:border-dark-600 dark:text-gray-400">
              {{ t('intentRouter.rules.empty') }}
            </p>

            <div v-for="(rule, index) in form.rules" :key="rule.uid" :data-test="`intent-rule-${index}`" class="space-y-3 rounded-xl border border-gray-200 p-4 dark:border-dark-600">
              <div class="grid gap-3 md:grid-cols-[14rem_minmax(0,1fr)]">
                <div>
                  <label class="input-label">{{ t('intentRouter.rules.name') }}</label>
                  <input v-model.trim="rule.name" data-test="intent-rule-name" type="text" maxlength="40" class="input" :placeholder="t('intentRouter.rules.namePlaceholder')" />
                </div>
              </div>

              <div>
                <label class="input-label">{{ t('intentRouter.rules.keywords') }}</label>
                <textarea v-model="rule.keywordsText" data-test="intent-rule-keywords" rows="2" class="input" :placeholder="t('intentRouter.rules.keywordsPlaceholder')"></textarea>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.rules.keywordsHint') }}</p>
              </div>

              <div>
                <label class="input-label">{{ t('intentRouter.rules.accounts') }}</label>
                <div class="flex flex-wrap gap-2">
                  <span
                    v-for="id in rule.account_ids"
                    :key="id"
                    :class="[
                      'inline-flex items-center gap-1 rounded-full px-2.5 py-1 text-xs',
                      accountProblem(id)
                        ? 'bg-red-50 text-red-700 dark:bg-red-900/30 dark:text-red-300'
                        : 'bg-gray-100 text-gray-700 dark:bg-dark-700 dark:text-gray-200',
                    ]"
                  >
                    {{ accountLabel(id) }}
                    <span v-if="accountProblem(id)">· {{ accountProblem(id) }}</span>
                    <button type="button" class="ml-0.5 text-gray-400 hover:text-red-500" :aria-label="t('common.delete')" @click="removeAccount(rule, id)">×</button>
                  </span>
                </div>
                <input
                  v-model.trim="rule.search"
                  data-test="intent-account-search"
                  type="search"
                  class="input mt-2"
                  :placeholder="t('intentRouter.rules.searchAccounts')"
                />
                <ul v-if="rule.search" class="mt-1 max-h-48 overflow-y-auto rounded-lg border border-gray-200 dark:border-dark-600">
                  <li v-if="accountMatches(rule).length === 0" class="px-3 py-2 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.rules.noAccounts') }}</li>
                  <li v-for="account in accountMatches(rule)" :key="account.id">
                    <button
                      type="button"
                      :data-test="`intent-account-option-${account.id}`"
                      class="flex w-full items-center justify-between gap-3 px-3 py-2 text-left text-sm hover:bg-gray-50 dark:hover:bg-dark-700"
                      @click="addAccount(rule, account.id)"
                    >
                      <span class="min-w-0 truncate text-gray-900 dark:text-white">#{{ account.id }} {{ account.name }}</span>
                      <span class="flex-shrink-0 text-xs text-gray-500 dark:text-gray-400">{{ account.platform }}</span>
                    </button>
                  </li>
                </ul>
                <p class="mt-1 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.rules.accountsHint') }}</p>
              </div>

              <div class="flex items-center justify-between">
                <label class="flex items-center gap-2 text-sm text-gray-700 dark:text-gray-300">
                  <input v-model="rule.enabled" type="checkbox" class="h-4 w-4 rounded border-gray-300 text-primary-600 focus:ring-primary-500" />
                  {{ t('intentRouter.rules.ruleEnabled') }}
                </label>
                <button type="button" class="text-sm text-red-600 hover:underline dark:text-red-400" @click="form.rules.splice(index, 1)">{{ t('intentRouter.rules.remove') }}</button>
              </div>
            </div>
          </div>

          <div class="flex flex-wrap items-center gap-3">
            <button type="button" data-test="intent-save" class="btn btn-primary" :disabled="busy" @click="save">
              {{ saving ? t('intentRouter.actions.saving') : t('intentRouter.actions.save') }}
            </button>
            <button v-if="savedRouter" type="button" class="btn btn-secondary" :disabled="busy" @click="clearCache(selectedGroup.id)">{{ t('intentRouter.actions.clearCache') }}</button>
            <button v-if="savedRouter" type="button" class="ml-auto text-sm text-red-600 hover:underline dark:text-red-400" :disabled="busy" @click="removeRouter">
              {{ t('intentRouter.actions.delete') }}
            </button>
          </div>

          <!-- Try it -->
          <div v-if="savedRouter" class="card space-y-3 p-5">
            <div>
              <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.test.title') }}</h2>
              <p class="mt-1 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.test.intro') }}</p>
            </div>
            <textarea v-model.trim="testText" data-test="intent-test-text" rows="3" class="input" :placeholder="t('intentRouter.test.placeholder')"></textarea>
            <p v-if="dirty" class="text-sm text-amber-700 dark:text-amber-300">{{ t('intentRouter.test.saveFirst') }}</p>
            <button type="button" data-test="intent-test-run" class="btn btn-secondary" :disabled="testing || dirty || !testText" @click="runTest">
              {{ testing ? t('intentRouter.test.running') : t('intentRouter.test.run') }}
            </button>
            <div v-if="testResult" data-test="intent-test-result" class="rounded-lg bg-gray-50 p-3 text-sm leading-6 dark:bg-dark-800">
              <p class="font-medium text-gray-900 dark:text-white">{{ testSummary }}</p>
              <p v-if="testResult.matched_by === 'keyword'" class="text-gray-500 dark:text-gray-400">
                {{ t('intentRouter.test.byKeyword', { keyword: testResult.keyword }) }}
              </p>
              <p v-else-if="testResult.matched_by === 'jev'" class="text-gray-500 dark:text-gray-400">
                {{ t('intentRouter.test.byJev', { p: (testResult.probe_probability ?? 0).toFixed(2) }) }}<template v-if="testResult.health_probability !== undefined"> · {{ t(testResult.health_check ? 'intentRouter.test.byJevHealthHit' : 'intentRouter.test.byJevHealth', { p: testResult.health_probability.toFixed(2) }) }}</template> · {{ t('intentRouter.test.latency', { ms: testResult.latency_ms }) }}
              </p>
              <p v-else-if="testResult.matched_by === 'classifier'" class="text-gray-500 dark:text-gray-400">
                {{ t('intentRouter.test.answer', { answer: testResult.answer || '—' }) }} · {{ t('intentRouter.test.latency', { ms: testResult.latency_ms }) }}
              </p>
            </div>
            <p v-if="testError" data-test="intent-test-error" class="rounded-lg bg-red-50 p-3 text-sm text-red-700 dark:bg-red-900/20 dark:text-red-300">{{ testError }}</p>
          </div>
        </section>
        <section v-else class="card p-8 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.selectGroup') }}</section>
      </div>

      <!-- Recent routing -->
      <div v-if="!loading" class="card p-5">
        <div class="flex items-center justify-between">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.events.title') }}</h2>
          <button type="button" class="btn btn-secondary text-sm" :disabled="eventsLoading" @click="loadEvents">{{ t('intentRouter.events.refresh') }}</button>
        </div>
        <p v-if="events.length === 0" class="mt-3 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.events.empty') }}</p>
        <div v-else class="mt-3 overflow-x-auto">
          <table class="min-w-full text-left text-sm">
            <thead class="text-xs text-gray-500 dark:text-gray-400">
              <tr>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.time') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.group') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.kind') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.intent') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.account') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.events.latency') }}</th>
                <th class="py-2 font-medium">{{ t('intentRouter.events.detail') }}</th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="(event, i) in events" :key="i" class="text-gray-700 dark:text-gray-300">
                <td class="whitespace-nowrap py-2 pr-4 tabular-nums">{{ formatTime(event.time) }}</td>
                <td class="whitespace-nowrap py-2 pr-4">{{ groupName(event.group_id) }}</td>
                <td class="whitespace-nowrap py-2 pr-4">
                  <span :class="['rounded-full px-2 py-0.5 text-xs', eventClass(event.kind)]">{{ t(`intentRouter.events.kinds.${event.kind}`) }}</span>
                </td>
                <td class="whitespace-nowrap py-2 pr-4">{{ event.kind === 'classified' && !event.intent ? t('intentRouter.events.noIntent') : event.intent || '—' }}</td>
                <td class="whitespace-nowrap py-2 pr-4">{{ event.account_id ? accountLabel(event.account_id) : '—' }}</td>
                <td class="whitespace-nowrap py-2 pr-4 tabular-nums">{{ event.latency_ms ? `${event.latency_ms} ms` : '—' }}</td>
                <td class="max-w-md break-words py-2 text-xs text-gray-500 dark:text-gray-400">{{ event.kind === 'keyword' ? t('intentRouter.events.keywordDetail', { keyword: event.detail }) : event.detail || '' }}</td>
              </tr>
            </tbody>
          </table>
        </div>
      </div>

      <!-- Keyword hit review -->
      <div v-if="!loading" class="card p-5" data-test="intent-hits">
        <div class="flex flex-wrap items-center justify-between gap-3">
          <h2 class="text-base font-semibold text-gray-900 dark:text-white">{{ t('intentRouter.hits.title') }}</h2>
          <div class="flex flex-wrap items-center gap-2">
            <select v-model.number="hitGroupId" class="input w-auto text-sm" @change="reloadHits">
              <option :value="0">{{ t('intentRouter.hits.allGroups') }}</option>
              <option v-for="id in routedGroupIds" :key="id" :value="id">{{ groupName(id) }}</option>
            </select>
            <select v-model.number="hitHours" class="input w-auto text-sm" @change="reloadHits">
              <option v-for="h in HIT_HOURS" :key="h" :value="h">{{ t(`intentRouter.hits.hours.${h}`) }}</option>
            </select>
            <button type="button" class="btn btn-secondary text-sm" :disabled="hitsLoading" @click="reloadHits">{{ t('intentRouter.hits.refresh') }}</button>
          </div>
        </div>
        <p class="mt-2 text-xs text-gray-500 dark:text-gray-400">{{ t('intentRouter.hits.help') }}</p>
        <p v-if="hitStats.length === 0" class="mt-3 text-sm text-gray-500 dark:text-gray-400">{{ t('intentRouter.hits.empty') }}</p>
        <div v-else class="mt-3 overflow-x-auto">
          <table class="min-w-full text-left text-sm">
            <thead class="text-xs text-gray-500 dark:text-gray-400">
              <tr>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.keyword') }}</th>
                <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.hitCount') }}</th>
                <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.users') }}</th>
                <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.apiKeys') }}</th>
                <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.avgChars') }}</th>
                <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.avgInputTokens') }}</th>
                <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.cost') }}</th>
                <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.lastHit') }}</th>
                <th class="py-2 font-medium"></th>
              </tr>
            </thead>
            <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
              <tr v-for="stat in hitStats" :key="stat.keyword" :class="['text-gray-700 dark:text-gray-300', stat.keyword === hitKeyword ? 'bg-primary-50 dark:bg-dark-700' : '']">
                <td class="max-w-xs break-words py-2 pr-4">
                  <span class="font-mono text-xs">{{ showKeyword(stat.keyword) }}</span>
                  <span v-if="stat.avg_input_tokens > SUSPECT_INPUT_TOKENS" class="ml-2 whitespace-nowrap rounded-full bg-amber-100 px-2 py-0.5 text-xs text-amber-700 dark:bg-amber-900/30 dark:text-amber-300" :title="t('intentRouter.hits.suspectHelp', { n: SUSPECT_INPUT_TOKENS })">{{ t('intentRouter.hits.suspect') }}</span>
                </td>
                <td class="py-2 pr-4 text-right tabular-nums">{{ stat.hits }}</td>
                <td class="py-2 pr-4 text-right tabular-nums">{{ stat.users }}</td>
                <td class="py-2 pr-4 text-right tabular-nums">{{ stat.api_keys }}</td>
                <td class="py-2 pr-4 text-right tabular-nums">{{ stat.avg_chars }}</td>
                <td class="py-2 pr-4 text-right tabular-nums">{{ stat.avg_input_tokens.toLocaleString() }}</td>
                <td class="py-2 pr-4 text-right tabular-nums">${{ stat.total_cost.toFixed(2) }}</td>
                <td class="whitespace-nowrap py-2 pr-4 tabular-nums">{{ formatTime(stat.last_hit) }}</td>
                <td class="whitespace-nowrap py-2"><button type="button" class="text-xs text-primary-600 hover:underline dark:text-primary-400" @click="filterHits(stat.keyword)">{{ t('intentRouter.hits.view') }}</button></td>
              </tr>
            </tbody>
          </table>
        </div>

        <div v-if="hitStats.length > 0" class="mt-5">
          <div class="flex flex-wrap items-center justify-between gap-2">
            <h3 class="text-sm font-semibold text-gray-900 dark:text-white">
              {{ hitKeyword ? t('intentRouter.hits.detailFiltered', { keyword: showKeyword(hitKeyword) }) : t('intentRouter.hits.detailTitle') }}
              <button v-if="hitKeyword" type="button" class="ml-2 text-xs font-normal text-primary-600 hover:underline dark:text-primary-400" @click="filterHits('')">{{ t('intentRouter.hits.clearFilter') }}</button>
            </h3>
            <div class="flex items-center gap-2 text-xs text-gray-500 dark:text-gray-400">
              <span>{{ t('intentRouter.hits.total', { n: hitTotal }) }}</span>
              <button type="button" class="btn btn-secondary px-2 py-1 text-xs" :disabled="hitsLoading || hitPage <= 1" @click="loadHitPage(hitPage - 1)">{{ t('intentRouter.hits.prev') }}</button>
              <span class="tabular-nums">{{ hitPage }} / {{ hitPages }}</span>
              <button type="button" class="btn btn-secondary px-2 py-1 text-xs" :disabled="hitsLoading || hitPage >= hitPages" @click="loadHitPage(hitPage + 1)">{{ t('intentRouter.hits.next') }}</button>
            </div>
          </div>
          <div class="mt-2 overflow-x-auto">
            <table class="min-w-full text-left text-sm">
              <thead class="text-xs text-gray-500 dark:text-gray-400">
                <tr>
                  <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.time') }}</th>
                  <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.group') }}</th>
                  <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.user') }}</th>
                  <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.keyword') }}</th>
                  <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.excerpt') }}</th>
                  <th class="py-2 pr-4 font-medium">{{ t('intentRouter.hits.account') }}</th>
                  <th class="py-2 pr-4 text-right font-medium">{{ t('intentRouter.hits.inputTokens') }}</th>
                  <th class="py-2 text-right font-medium">{{ t('intentRouter.hits.cost') }}</th>
                </tr>
              </thead>
              <tbody class="divide-y divide-gray-100 dark:divide-dark-700">
                <tr v-for="hit in hits" :key="hit.id" class="align-top text-gray-700 dark:text-gray-300">
                  <td class="whitespace-nowrap py-2 pr-4 tabular-nums">{{ formatTime(hit.time) }}</td>
                  <td class="whitespace-nowrap py-2 pr-4">{{ groupName(hit.group_id) }}</td>
                  <td class="whitespace-nowrap py-2 pr-4 tabular-nums">#{{ hit.user_id }}</td>
                  <td class="max-w-[10rem] break-words py-2 pr-4 font-mono text-xs">{{ showKeyword(hit.keyword) }}</td>
                  <td class="min-w-[20rem] max-w-xl py-2 pr-4 text-xs">
                    <div class="whitespace-pre-wrap break-words"><template v-for="(part, i) in highlight(hit.excerpt, hit.keyword)" :key="i"><mark v-if="part.match" class="rounded bg-yellow-200 px-0.5 text-gray-900 dark:bg-yellow-600/60 dark:text-white">{{ part.text }}</mark><template v-else>{{ part.text }}</template></template></div>
                    <div class="mt-1 text-gray-400 dark:text-gray-500">
                      {{ hit.user_turn === 0 ? t('intentRouter.hits.turnLatest') : t('intentRouter.hits.turnPrevious') }} · {{ t('intentRouter.hits.chars', { n: hit.message_chars }) }}<template v-if="hit.model"> · {{ hit.model }}</template>
                    </div>
                  </td>
                  <td class="whitespace-nowrap py-2 pr-4">{{ hit.account_id ? accountLabel(hit.account_id) : t('intentRouter.hits.noUsage') }}</td>
                  <td class="py-2 pr-4 text-right tabular-nums">{{ hit.input_tokens != null ? hit.input_tokens.toLocaleString() : '—' }}</td>
                  <td class="py-2 text-right tabular-nums">{{ hit.total_cost != null ? `$${hit.total_cost.toFixed(4)}` : '—' }}</td>
                </tr>
              </tbody>
            </table>
          </div>
        </div>
      </div>
    </div>
  </AppLayout>
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, reactive, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import AppLayout from '@/components/layout/AppLayout.vue'
import MonitorAnswerCard from '@/components/admin/intentRouter/MonitorAnswerCard.vue'
import { useAppStore } from '@/stores/app'
import { adminAPI } from '@/api/admin'
import { intentRoutersAPI } from '@/api/admin/intentRouters'
import type { IntentClassifyTestResult, IntentJevConfig, IntentJevStatus, IntentRouteEvent, IntentRouteHit, IntentRouteKeywordStat, IntentRouter, IntentRouterInput } from '@/api/admin/intentRouters'
import { extractI18nErrorMessage } from '@/utils/apiError'

interface GroupOption { id: number; name: string; platform: string }
interface AccountOption { id: number; name: string; platform: string }
interface RuleForm { uid: number; name: string; description: string; keywordsText: string; originalKeywords: string[]; account_ids: number[]; enabled: boolean; search: string }
interface JevUserForm { id: number; email: string }
interface JevKeyForm { id: number; name: string; user_id: number }
type FormState = Omit<IntentRouterInput, 'rules' | 'jev_set_balance_usd' | 'excluded_models'> & { excludedModelsText: string; rules: RuleForm[]; jevUsers: JevUserForm[]; jevKeys: JevKeyForm[]; jevBalanceText: string }

const ACCOUNT_PAGE_SIZE = 1000
const ACCOUNT_MAX_PAGES = 10
const HIT_HOURS = [1, 24, 168, 720] as const
const HIT_PAGE_SIZE = 50
/** Detection prompts are a few hundred tokens; far above that is a long conversation. */
const SUSPECT_INPUT_TOKENS = 20000

const { t } = useI18n()
const appStore = useAppStore()

const loading = ref(true)
const saving = ref(false)
const working = ref(false)
const testing = ref(false)
const eventsLoading = ref(false)
const busy = computed(() => saving.value || working.value)

const groups = ref<GroupOption[]>([])
const accounts = ref<AccountOption[]>([])
const routers = ref<Record<number, IntentRouter>>({})
const events = ref<IntentRouteEvent[]>([])
const hitsLoading = ref(false)
const hitGroupId = ref(0)
const hitHours = ref<number>(24)
const hitKeyword = ref('')
const hitStats = ref<IntentRouteKeywordStat[]>([])
const hits = ref<IntentRouteHit[]>([])
const hitTotal = ref(0)
const hitPage = ref(1)
const hitPages = computed(() => Math.max(1, Math.ceil(hitTotal.value / HIT_PAGE_SIZE)))
const routedGroupIds = computed(() => Object.keys(routers.value).map(Number))
const groupSearch = ref('')
const selectedGroupId = ref<number | null>(null)
const testText = ref('')
const testResult = ref<IntentClassifyTestResult | null>(null)
const testError = ref('')
const jevStatus = ref<IntentJevStatus | null>(null)

let nextUid = 1
const blankForm = (): FormState => ({
  enabled: false,
  classifier_base_url: '',
  classifier_api_key: '',
  classifier_protocol: 'openai_chat',
  classifier_model: '',
  classifier_timeout_ms: 3000,
  cache_ttl_seconds: 7200,
  max_input_chars: 2000,
  keyword_max_chars: 0,
  unavailable_status: 503,
  rules: [],
  jev_api_key: '',
  jev: blankJev(),
  jevUsers: [],
  jevKeys: [],
  excludedModelsText: '',
  jevBalanceText: '',
})
function blankJev(): IntentJevConfig {
  return { enabled: false, rule: '', threshold: 0.5, max_turns: 3, timeout_ms: 1000, model: 'jev-latest', scope: 'all', user_ids: [], api_keys: [], sample_percent: 100, health_enabled: false, health_threshold: 0.7, health_rule: '', low_balance_usd: 1, balance_usd: 0 }
}
const form = reactive<FormState>(blankForm())
const baseline = ref('')

const accountsById = computed(() => new Map(accounts.value.map((a) => [a.id, a])))
const selectedGroup = computed(() => groups.value.find((g) => g.id === selectedGroupId.value) ?? null)
const savedRouter = computed(() => (selectedGroupId.value ? routers.value[selectedGroupId.value] ?? null : null))
const jevKeyConfigured = computed(() => savedRouter.value?.jev_api_key_configured === true)
const ruleNames = computed(() => form.rules.map((rule) => rule.name.trim()).filter(Boolean))
const jevRuleAccounts = computed(() => form.rules.find((rule) => rule.name.trim() === form.jev.rule)?.account_ids ?? [])
const filteredGroups = computed(() => {
  const q = groupSearch.value.toLowerCase()
  return q ? groups.value.filter((g) => g.name.toLowerCase().includes(q) || String(g.id) === q) : groups.value
})
const dirty = computed(() => snapshot() !== baseline.value)

function toInput(): IntentRouterInput {
  return {
    enabled: form.enabled,
    classifier_base_url: form.classifier_base_url,
    classifier_api_key: form.classifier_api_key,
    classifier_protocol: form.classifier_protocol,
    classifier_model: form.classifier_model,
    classifier_timeout_ms: Number(form.classifier_timeout_ms) || 0,
    cache_ttl_seconds: Number(form.cache_ttl_seconds) || 0,
    max_input_chars: Number(form.max_input_chars) || 0,
    keyword_max_chars: Math.max(0, Math.trunc(Number(form.keyword_max_chars) || 0)),
    unavailable_status: Number(form.unavailable_status) || 503,
    rules: form.rules.map(({ name, description, keywordsText, originalKeywords, account_ids, enabled }) => ({
      name, description, keywords: restoreOriginalControls(parseKeywords(keywordsText), originalKeywords), account_ids: [...account_ids], enabled,
    })),
    excluded_models: form.excludedModelsText.split(/[\n,，、]+/).map((m) => m.trim()).filter(Boolean),
    jev_api_key: form.jev_api_key,
    jev: {
      ...form.jev,
      threshold: Number(form.jev.threshold) || 0,
      max_turns: Number(form.jev.max_turns) || 0,
      timeout_ms: Number(form.jev.timeout_ms) || 0,
      user_ids: form.jevUsers.map((u) => u.id),
      api_keys: form.jevKeys.map((k) => ({ id: k.id })),
      sample_percent: Number(form.jev.sample_percent) || 0,
      health_threshold: Number(form.jev.health_threshold) || 0,
      low_balance_usd: Number(form.jev.low_balance_usd) || 0,
    },
    ...(String(form.jevBalanceText).trim() !== '' ? { jev_set_balance_usd: Number(form.jevBalanceText) } : {}),
  }
}
// Jev scope pickers: users are searched like on the usage page; keys come
// from the chosen users, so nobody has to look IDs up.
const jevUserQuery = ref('')
const jevUserResults = ref<{ id: number; email: string }[]>([])
const jevUserKeys = ref<Record<number, { id: number; name: string; user_id: number }[]>>({})
let jevUserSearchTimer: ReturnType<typeof setTimeout> | null = null
let jevUserSearchSeq = 0
function searchJevUsers() {
  if (jevUserSearchTimer) clearTimeout(jevUserSearchTimer)
  const query = jevUserQuery.value.trim()
  const seq = ++jevUserSearchSeq
  if (!query) {
    jevUserResults.value = []
    return
  }
  jevUserSearchTimer = setTimeout(async () => {
    try {
      const users = await adminAPI.usage.searchUsers(query)
      if (seq === jevUserSearchSeq) jevUserResults.value = users.filter((u) => !u.deleted && !form.jevUsers.some((x) => x.id === u.id))
    } catch {
      if (seq === jevUserSearchSeq) jevUserResults.value = []
    }
  }, 250)
}
function addJevUser(u: { id: number; email: string }) {
  if (!form.jevUsers.some((x) => x.id === u.id)) form.jevUsers.push({ id: u.id, email: u.email })
  jevUserQuery.value = ''
  jevUserResults.value = []
}
function removeJevUser(id: number) {
  form.jevUsers = form.jevUsers.filter((u) => u.id !== id)
  form.jevKeys = form.jevKeys.filter((k) => k.user_id !== id)
}
// The key search returns a user's 30 newest keys matching the name typed
// (all of them when nothing is typed); older keys are found by name.
const JEV_KEY_PAGE = 30
const jevKeyQuery = ref('')
const jevKeyPickerOpen = ref(false)
let jevKeySearchTimer: ReturnType<typeof setTimeout> | null = null
let jevKeySearchSeq = 0
function openJevKeyPicker() {
  jevKeyPickerOpen.value = true
  searchJevKeys()
}
function searchJevKeys() {
  if (jevKeySearchTimer) clearTimeout(jevKeySearchTimer)
  const seq = ++jevKeySearchSeq
  const query = jevKeyQuery.value.trim()
  jevKeySearchTimer = setTimeout(async () => {
    const users = form.jevUsers.map((u) => u.id)
    const results = await Promise.all(users.map(async (id) => {
      try {
        return [id, await adminAPI.usage.searchApiKeys(id, query || undefined)] as const
      } catch {
        return [id, []] as const
      }
    }))
    if (seq !== jevKeySearchSeq) return
    const next = { ...jevUserKeys.value }
    for (const [id, keys] of results) next[id] = [...keys]
    jevUserKeys.value = next
  }, 250)
}
function jevKeyChoices(userId: number) {
  return (jevUserKeys.value[userId] ?? []).filter((k) => !form.jevKeys.some((x) => x.id === k.id))
}
function addJevKey(key: { id: number; name: string; user_id: number }) {
  if (!form.jevKeys.some((k) => k.id === key.id)) form.jevKeys.push({ id: key.id, name: key.name, user_id: key.user_id })
}
function removeJevKey(id: number) {
  form.jevKeys = form.jevKeys.filter((k) => k.id !== id)
}
function jevUserEmail(userId: number) {
  return form.jevUsers.find((u) => u.id === userId)?.email || `#${userId}`
}
// Keywords are typed one per line or comma-separated (ASCII or Chinese commas).
function parseKeywords(text: string): string[] {
  const result: string[] = []
  let current = ''
  let quoted = false
  const finish = () => {
    const keyword = current.trim()
    if (keyword) result.push(keyword)
    current = ''
  }
  for (let i = 0; i < text.length; i++) {
    const char = text[i]
    if (quoted) {
      if (char === '"' && text[i + 1] === '"') {
        current += '"'
        i++
      } else if (char === '"') {
        quoted = false
      } else {
        current += char
      }
    } else if (char === '"' && current.trim() === '') {
      quoted = true
    } else if (/[\n,，、]/.test(char)) {
      finish()
    } else {
      current += char
    }
  }
  finish()
  return result
}
function formatKeywords(keywords: string[]): string {
  return keywords.map((keyword) => /[\r\n,，、"]/.test(keyword)
    ? `"${keyword.replace(/\r/g, '\\r').replace(/"/g, '""')}"`
    : keyword).join(', ')
}
function restoreOriginalControls(parsed: string[], originals: string[]): string[] {
  const controls = new Map(originals.filter((keyword) => keyword.includes('\r'))
    .map((keyword) => [keyword.replace(/\r/g, '\\r'), keyword]))
  return parsed.map((keyword) => controls.get(keyword) ?? keyword)
}
function snapshot() { return JSON.stringify(toInput()) }

function fillForm(router: IntentRouter | null) {
  Object.assign(form, blankForm())
  if (router) {
    form.enabled = router.enabled
    form.classifier_base_url = router.classifier_base_url
    form.classifier_protocol = router.classifier_protocol
    form.classifier_model = router.classifier_model
    form.classifier_timeout_ms = router.classifier_timeout_ms
    form.cache_ttl_seconds = router.cache_ttl_seconds
    form.max_input_chars = router.max_input_chars
    form.keyword_max_chars = router.keyword_max_chars ?? 0
    form.unavailable_status = router.unavailable_status || 503
    form.excludedModelsText = (router.excluded_models ?? []).join('\n')
    form.jev = { ...blankJev(), ...(router.jev ?? {}) }
    const userLabels = new Map((router.jev_scope?.users ?? []).map((u) => [u.id, u.email]))
    const keyLabels = new Map((router.jev_scope?.api_keys ?? []).map((k) => [k.id, k.name]))
    form.jevUsers = (router.jev?.user_ids ?? []).map((id) => ({ id, email: userLabels.get(id) ?? '' }))
    form.jevKeys = (router.jev?.api_keys ?? []).map((k) => ({ id: k.id, user_id: k.user_id ?? 0, name: keyLabels.get(k.id) ?? '' }))
    form.rules = (router.rules ?? []).map(({ keywords, ...rule }) => ({
      ...rule, keywordsText: formatKeywords(keywords ?? []), originalKeywords: [...(keywords ?? [])], account_ids: [...(rule.account_ids ?? [])], uid: nextUid++, search: '',
    }))
  }
  baseline.value = snapshot()
  testResult.value = null
  testError.value = ''
}

function selectGroup(id: number) {
  selectedGroupId.value = id
  fillForm(routers.value[id] ?? null)
  void loadJevStatus(id)
}

// Every status request and every save takes a new sequence number; only the
// answer to the latest request may touch the page. An answer that left the
// server before a save (say "switched off") must not undo that save.
let jevStatusSeq = 0
async function loadJevStatus(groupId: number) {
  const seq = ++jevStatusSeq
  if (!routers.value[groupId]) {
    jevStatus.value = null
    return
  }
  try {
    const status = (await intentRoutersAPI.jevStatus(groupId)).data
    if (seq !== jevStatusSeq || selectedGroupId.value !== groupId) return
    jevStatus.value = status
    // Switched off by the server while this page still shows it on: follow,
    // or the next save would quietly turn it back on.
    const router = routers.value[groupId]
    if (status.state === 'auto_disabled' && router?.jev?.enabled) {
      const wasClean = !dirty.value
      routers.value = { ...routers.value, [groupId]: { ...router, jev: { ...router.jev, enabled: false, auto_disabled_status: status.auto_disabled_status, auto_disabled_detail: status.auto_disabled_detail, auto_disabled_at: status.auto_disabled_at } } }
      if (wasClean) fillForm(routers.value[groupId])
      else form.jev.enabled = false
      jevStatus.value = status
    }
  } catch {
    if (seq === jevStatusSeq && selectedGroupId.value === groupId) jevStatus.value = null
  }
}
function formatUSD(value: number) { return `$${value.toFixed(value !== 0 && Math.abs(value) < 0.01 ? 4 : 2)}` }
function formatCNY(usd: number) { return `¥${(usd * (jevStatus.value?.cny_per_usd ?? 7)).toFixed(usd !== 0 && Math.abs(usd) < 0.01 ? 4 : 2)}` }
function formatDay(day: string) { return `${day.slice(4, 6)}-${day.slice(6, 8)}` }
function jevAutoDisabledHint(status?: number) {
  switch (status) {
    case 401: return t('intentRouter.jev.status.why401')
    case 402: return t('intentRouter.jev.status.why402')
    case 403: return t('intentRouter.jev.status.why403')
    case 451: return t('intentRouter.jev.status.why451')
    default: return ''
  }
}

function routerStatus(groupId: number): 'on' | 'off' | 'none' {
  const router = routers.value[groupId]
  if (!router) return 'none'
  return router.enabled ? 'on' : 'off'
}
function statusClass(groupId: number) {
  switch (routerStatus(groupId)) {
    case 'on': return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
    case 'off': return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
    default: return 'bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-gray-400'
  }
}

// Mirrors the server's rule: same platform, or Antigravity for Claude/Gemini groups.
function platformCompatible(groupPlatform: string, accountPlatform: string) {
  if (groupPlatform === 'anthropic' || groupPlatform === 'gemini') return accountPlatform === groupPlatform || accountPlatform === 'antigravity'
  if (groupPlatform === 'openai' || groupPlatform === 'antigravity') return accountPlatform === groupPlatform
  return true
}
function accountLabel(id: number) {
  const account = accountsById.value.get(id)
  return account ? `#${id} ${account.name}` : t('intentRouter.rules.missingAccount', { id })
}
function accountProblem(id: number) {
  const account = accountsById.value.get(id)
  if (!account || !selectedGroup.value) return ''
  return platformCompatible(selectedGroup.value.platform, account.platform) ? '' : t('intentRouter.rules.incompatible')
}
function accountMatches(rule: RuleForm) {
  const q = rule.search.toLowerCase()
  const groupPlatform = selectedGroup.value?.platform ?? ''
  return accounts.value
    .filter((a) => !rule.account_ids.includes(a.id) && platformCompatible(groupPlatform, a.platform))
    .filter((a) => a.name.toLowerCase().includes(q) || String(a.id) === q)
    .slice(0, 30)
}
function addAccount(rule: RuleForm, id: number) {
  if (!rule.account_ids.includes(id)) rule.account_ids.push(id)
  rule.search = ''
}
function removeAccount(rule: RuleForm, id: number) {
  rule.account_ids = rule.account_ids.filter((x) => x !== id)
}
function addRule() {
  form.rules.push({ uid: nextUid++, name: '', description: '', keywordsText: '', originalKeywords: [], account_ids: [], enabled: true, search: '' })
}

function errorText(err: unknown) {
  return extractI18nErrorMessage(err, t, 'intentRouter.errors', t('common.error'))
}

// Every request below is tied to the group it was started for. The admin may
// pick another group while it is in flight; its answer then still updates that
// group's stored state, but must never touch the form, which by now shows a
// different group — refilling it would let the next save overwrite the wrong one.
async function save() {
  const groupId = selectedGroupId.value
  if (!groupId || busy.value) return
  saving.value = true
  jevStatusSeq++
  try {
    const res = await intentRoutersAPI.save(groupId, toInput())
    routers.value = { ...routers.value, [groupId]: res.data }
    if (selectedGroupId.value === groupId) {
      fillForm(res.data)
      void loadJevStatus(groupId)
    }
    appStore.showSuccess(t('intentRouter.actions.saved'))
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    saving.value = false
  }
}

async function removeRouter() {
  const group = selectedGroup.value
  if (!group || busy.value || !window.confirm(t('intentRouter.actions.deleteConfirm', { group: group.name }))) return
  working.value = true
  try {
    await intentRoutersAPI.remove(group.id)
    const next = { ...routers.value }
    delete next[group.id]
    routers.value = next
    if (selectedGroupId.value === group.id) fillForm(null)
    appStore.showSuccess(t('intentRouter.actions.deleted'))
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    working.value = false
  }
}

async function clearCache(groupId: number) {
  if (busy.value) return
  working.value = true
  try {
    const res = await intentRoutersAPI.clearCache(groupId)
    appStore.showSuccess(t('intentRouter.actions.cleared', { count: res.data.removed }))
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    working.value = false
  }
}

const testSummary = computed(() => {
  const result = testResult.value
  if (!result) return ''
  if (!result.understood) return t('intentRouter.test.resultUnclear')
  if (!result.intent) return t('intentRouter.test.resultNone')
  return t('intentRouter.test.resultIntent', { intent: result.intent, accounts: result.account_ids.map(accountLabel).join('、') })
})

async function runTest() {
  const groupId = selectedGroupId.value
  if (!groupId || testing.value || dirty.value || !testText.value) return
  testing.value = true
  testResult.value = null
  testError.value = ''
  try {
    const result = (await intentRoutersAPI.test(groupId, testText.value)).data
    if (selectedGroupId.value === groupId) testResult.value = result
  } catch (err) {
    if (selectedGroupId.value === groupId) testError.value = errorText(err)
  } finally {
    testing.value = false
  }
}

function groupName(id: number) { return groups.value.find((g) => g.id === id)?.name ?? `#${id}` }
function formatTime(value: string) {
  const d = new Date(value)
  return Number.isNaN(d.getTime()) ? value : d.toLocaleString()
}
function jevStateClass(state: IntentJevStatus['state']) {
  if (state === 'normal') return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
  if (state === 'paused') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  if (state === 'auto_disabled') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}
function eventClass(kind: IntentRouteEvent['kind']) {
  if (kind === 'error') return 'bg-red-100 text-red-700 dark:bg-red-900/30 dark:text-red-300'
  if (kind === 'skipped') return 'bg-amber-100 text-amber-700 dark:bg-amber-900/30 dark:text-amber-300'
  if (kind === 'keyword') return 'bg-blue-100 text-blue-700 dark:bg-blue-900/30 dark:text-blue-300'
  if (kind === 'jev') return 'bg-purple-100 text-purple-700 dark:bg-purple-900/30 dark:text-purple-300'
  if (kind === 'routed') return 'bg-green-100 text-green-700 dark:bg-green-900/30 dark:text-green-300'
  return 'bg-gray-100 text-gray-600 dark:bg-dark-700 dark:text-gray-300'
}

async function loadEvents() {
  eventsLoading.value = true
  try {
    events.value = (await intentRoutersAPI.events(100)).data ?? []
  } catch {
    // The log is a convenience; the page works without it.
  } finally {
    eventsLoading.value = false
  }
}

/** Keywords may hold line breaks; show them as \n so the row stays on one line. */
function showKeyword(keyword: string) { return keyword.replace(/\r/g, '\\r').replace(/\n/g, '\\n') }

/** Splits the excerpt around case-insensitive occurrences of the keyword. */
function highlight(text: string, keyword: string) {
  const needle = keyword.trim().toLowerCase()
  const parts: { text: string; match: boolean }[] = []
  if (!needle) return [{ text, match: false }]
  const lower = text.toLowerCase()
  // toLowerCase can change the length of a few characters; fall back to plain text then.
  if (lower.length !== text.length) return [{ text, match: false }]
  let from = 0
  for (let at = lower.indexOf(needle); at >= 0; at = lower.indexOf(needle, from)) {
    if (at > from) parts.push({ text: text.slice(from, at), match: false })
    parts.push({ text: text.slice(at, at + needle.length), match: true })
    from = at + needle.length
  }
  if (from < text.length) parts.push({ text: text.slice(from), match: false })
  return parts
}

function hitQuery() {
  return { group_id: hitGroupId.value || undefined, hours: hitHours.value, keyword: hitKeyword.value || undefined }
}

// A generation binds filters, statistics and details; stale requests never publish.
let hitRequestVersion = 0
let hitsDisposed = false
onBeforeUnmount(() => {
  hitsDisposed = true
  hitRequestVersion++
})

function isCurrentHitRequest(version: number) {
  return !hitsDisposed && version === hitRequestVersion
}

function clearHitPage() {
  hits.value = []
  hitTotal.value = 0
  hitPage.value = 1
}

async function loadHitPage(page: number) {
  if (hitsDisposed) return
  const version = ++hitRequestVersion
  const query = { ...hitQuery(), page, page_size: HIT_PAGE_SIZE }
  hitsLoading.value = true
  clearHitPage()
  try {
    const res = (await intentRoutersAPI.hits(query)).data
    if (!isCurrentHitRequest(version)) return
    hits.value = res?.items ?? []
    hitTotal.value = res?.total ?? 0
    hitPage.value = page
  } catch (err) {
    if (isCurrentHitRequest(version)) appStore.showError(errorText(err))
  } finally {
    if (isCurrentHitRequest(version)) hitsLoading.value = false
  }
}

async function reloadHits() {
  if (hitsDisposed) return
  const version = ++hitRequestVersion
  const { keyword: selectedKeyword, ...query } = hitQuery()
  hitsLoading.value = true
  hitStats.value = []
  clearHitPage()
  try {
    const stats = (await intentRoutersAPI.hitStats(query)).data ?? []
    if (!isCurrentHitRequest(version)) return
    const keyword = selectedKeyword && stats.some((s) => s.keyword === selectedKeyword) ? selectedKeyword : undefined
    const res = (await intentRoutersAPI.hits({ ...query, keyword, page: 1, page_size: HIT_PAGE_SIZE })).data
    if (!isCurrentHitRequest(version)) return
    // Publish both responses together using the same captured filter snapshot.
    hitStats.value = stats
    hitKeyword.value = keyword ?? ''
    hits.value = res?.items ?? []
    hitTotal.value = res?.total ?? 0
    hitPage.value = 1
  } catch (err) {
    if (isCurrentHitRequest(version)) appStore.showError(errorText(err))
  } finally {
    if (isCurrentHitRequest(version)) hitsLoading.value = false
  }
}

function filterHits(keyword: string) {
  hitKeyword.value = keyword
  void loadHitPage(1)
}

async function loadAccounts() {
  const all: AccountOption[] = []
  for (let page = 1; page <= ACCOUNT_MAX_PAGES; page++) {
    const res = await adminAPI.accounts.list(page, ACCOUNT_PAGE_SIZE, { lite: '1' })
    const items = res.items ?? []
    all.push(...items.map((a) => ({ id: a.id, name: a.name, platform: a.platform })))
    if (items.length < ACCOUNT_PAGE_SIZE) break
  }
  accounts.value = all
}

onMounted(async () => {
  try {
    const [groupList, routerList] = await Promise.all([
      adminAPI.groups.getAllIncludingInactive(),
      intentRoutersAPI.list(),
      loadAccounts(),
    ])
    groups.value = groupList.map((g) => ({ id: g.id, name: g.name, platform: g.platform }))
    routers.value = Object.fromEntries((routerList.data ?? []).map((r) => [r.group_id, r]))
    const first = groups.value.find((g) => routers.value[g.id]) ?? groups.value[0]
    if (first) selectGroup(first.id)
  } catch (err) {
    appStore.showError(errorText(err))
  } finally {
    loading.value = false
  }
  void loadEvents()
  void reloadHits()
})
</script>
