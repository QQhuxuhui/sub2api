<template>
  <!-- Custom Home Content: Full Page Mode -->
  <div v-if="homeContent" class="min-h-screen">
    <!-- iframe mode -->
    <iframe
      v-if="isHomeContentUrl"
      :src="homeContent.trim()"
      class="h-screen w-full border-0"
      allowfullscreen
    ></iframe>
    <!-- HTML mode - SECURITY: homeContent is admin-only setting, XSS risk is acceptable -->
    <div v-else v-html="homeContent"></div>
  </div>

  <!-- Default Home Page -->
  <div
    v-else
    class="relative flex min-h-screen flex-col overflow-x-clip bg-[linear-gradient(180deg,#f0fdfa_0%,#ffffff_46%,#f0f9ff_100%)] dark:bg-[linear-gradient(180deg,#0a1e1c_0%,#020617_46%,#071522_100%)]"
  >
    <!-- Background Decorations -->
    <div class="pointer-events-none absolute inset-0 overflow-hidden">
      <div
        class="absolute -left-32 -top-32 h-[28rem] w-[28rem] rounded-full bg-primary-300/25 blur-3xl dark:bg-primary-500/10"
      ></div>
      <div
        class="absolute -right-40 top-1/4 h-[26rem] w-[26rem] rounded-full bg-cyan-200/30 blur-3xl dark:bg-cyan-500/10"
      ></div>
      <div
        class="absolute bottom-0 left-1/3 h-96 w-96 rounded-full bg-sky-200/25 blur-3xl dark:bg-sky-500/5"
      ></div>
      <div
        class="absolute inset-x-0 top-0 h-[44rem] bg-[linear-gradient(rgba(20,184,166,0.05)_1px,transparent_1px),linear-gradient(90deg,rgba(20,184,166,0.05)_1px,transparent_1px)] bg-[size:56px_56px] [mask-image:radial-gradient(ellipse_75%_65%_at_50%_0%,#000,transparent)] dark:bg-[linear-gradient(rgba(45,212,191,0.06)_1px,transparent_1px),linear-gradient(90deg,rgba(45,212,191,0.06)_1px,transparent_1px)]"
      ></div>
    </div>

    <!-- Header -->
    <header class="sticky top-0 z-30 px-4 pt-4 sm:px-6">
      <nav
        class="mx-auto flex h-16 max-w-6xl items-center justify-between rounded-2xl border border-white/70 bg-white/70 px-4 shadow-sm backdrop-blur-xl dark:border-dark-700/60 dark:bg-dark-900/70 sm:px-5"
      >
        <!-- Logo + Site Name -->
        <div class="flex min-w-0 items-center gap-3">
          <div class="h-9 w-9 shrink-0 overflow-hidden rounded-xl shadow-sm">
            <img :src="siteLogo || '/logo.png'" alt="Logo" class="h-full w-full object-contain" />
          </div>
          <span class="truncate text-base font-semibold text-gray-900 dark:text-white">
            {{ siteName }}
          </span>
        </div>

        <!-- Nav Actions -->
        <div class="flex items-center gap-1.5 sm:gap-2">
          <router-link
            to="/key-usage"
            class="hidden rounded-full px-3 py-1.5 text-sm text-gray-600 transition-colors hover:bg-gray-100 hover:text-gray-900 dark:text-dark-300 dark:hover:bg-dark-800 dark:hover:text-white md:inline-flex"
          >
            {{ t('home.nav.keyUsage') }}
          </router-link>
          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="hidden rounded-full px-3 py-1.5 text-sm text-gray-600 transition-colors hover:bg-gray-100 hover:text-gray-900 dark:text-dark-300 dark:hover:bg-dark-800 dark:hover:text-white md:inline-flex"
          >
            {{ t('home.docs') }}
          </a>

          <LocaleSwitcher />

          <!-- Theme Toggle -->
          <button
            @click="toggleTheme"
            class="rounded-full p-2 text-gray-500 transition-colors hover:bg-gray-100 hover:text-gray-700 dark:text-dark-400 dark:hover:bg-dark-800 dark:hover:text-white"
            :title="isDark ? t('home.switchToLight') : t('home.switchToDark')"
          >
            <Icon v-if="isDark" name="sun" size="md" />
            <Icon v-else name="moon" size="md" />
          </button>

          <!-- Login / Dashboard Button -->
          <router-link
            v-if="isAuthenticated"
            :to="dashboardPath"
            class="inline-flex items-center gap-1.5 rounded-full bg-gray-900 py-1.5 pl-1.5 pr-3 transition-colors hover:bg-gray-800 dark:bg-white dark:hover:bg-gray-100"
          >
            <span
              class="flex h-5 w-5 items-center justify-center rounded-full bg-gradient-to-br from-primary-400 to-primary-600 text-[10px] font-semibold text-white"
            >
              {{ userInitial }}
            </span>
            <span class="text-xs font-medium text-white dark:text-gray-900">{{
              t('home.dashboard')
            }}</span>
          </router-link>
          <router-link
            v-else
            to="/login"
            class="inline-flex items-center rounded-full bg-gray-900 px-4 py-1.5 text-xs font-medium text-white transition-colors hover:bg-gray-800 dark:bg-white dark:text-gray-900 dark:hover:bg-gray-100"
          >
            {{ t('home.login') }}
          </router-link>
        </div>
      </nav>
    </header>

    <!-- Main Content -->
    <main class="relative z-10 flex-1 px-6">
      <div class="mx-auto max-w-6xl">
        <!-- Hero -->
        <section class="grid items-center gap-12 pb-8 pt-14 lg:grid-cols-[1.05fr_0.95fr] lg:pt-20">
          <!-- Left: Copy -->
          <div class="hero-enter text-center lg:text-left">
            <a
              :href="githubUrl"
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex items-center gap-2 rounded-full border border-primary-200/70 bg-white/70 px-4 py-1.5 text-xs font-medium text-primary-700 shadow-sm backdrop-blur-sm transition-colors hover:border-primary-300 hover:text-primary-800 dark:border-primary-500/30 dark:bg-primary-500/10 dark:text-primary-300 dark:hover:border-primary-500/50"
            >
              {{ t('home.badge') }}
              <Icon name="externalLink" size="xs" />
            </a>

            <h1
              class="mt-6 text-4xl font-bold tracking-tight text-gray-900 dark:text-white sm:text-5xl lg:text-6xl"
            >
              <span class="hero-gradient-text">{{ siteName }}</span>
            </h1>

            <p class="mt-4 text-xl font-medium text-gray-800 dark:text-gray-100 sm:text-2xl">
              {{ siteSubtitle }}
            </p>
            <p
              class="mx-auto mt-3 max-w-xl text-base leading-relaxed text-gray-600 dark:text-dark-300 lg:mx-0"
            >
              {{ t('home.heroDescription') }}
            </p>

            <div class="mt-8 flex flex-wrap items-center justify-center gap-3 lg:justify-start">
              <router-link
                :to="ctaTarget"
                class="inline-flex items-center gap-2 rounded-full bg-gradient-to-r from-primary-700 to-cyan-700 px-7 py-3 text-sm font-semibold text-white shadow-lg shadow-primary-500/30 transition-all hover:shadow-xl hover:shadow-primary-500/40 hover:brightness-110 active:scale-[0.98]"
              >
                {{ isAuthenticated ? t('home.goToDashboard') : t('home.getStarted') }}
                <Icon name="arrowRight" size="sm" :stroke-width="2" />
              </router-link>
              <a
                v-if="docUrl"
                :href="docUrl"
                target="_blank"
                rel="noopener noreferrer"
                class="inline-flex items-center gap-2 rounded-full border border-gray-300/80 bg-white/60 px-6 py-3 text-sm font-medium text-gray-700 backdrop-blur-sm transition-colors hover:border-gray-400 hover:bg-white dark:border-dark-600 dark:bg-dark-800/60 dark:text-gray-200 dark:hover:border-dark-500 dark:hover:bg-dark-800"
              >
                <Icon name="book" size="sm" />
                {{ t('home.viewDocs') }}
              </a>
            </div>
          </div>

          <!-- Right: Terminal -->
          <div class="relative mx-auto w-full max-w-md lg:max-w-none">
            <div
              class="pointer-events-none absolute -inset-8 rounded-[2.5rem] bg-gradient-to-br from-primary-400/20 via-cyan-400/10 to-sky-400/20 blur-2xl dark:from-primary-500/15 dark:via-cyan-500/10 dark:to-sky-500/15"
            ></div>
            <div class="terminal-window relative">
              <div class="terminal-header">
                <div class="terminal-buttons">
                  <span class="dot-close"></span>
                  <span class="dot-min"></span>
                  <span class="dot-max"></span>
                </div>
                <span class="terminal-title">app.py</span>
              </div>
              <div class="terminal-body">
                <div class="tline t1">
                  <span class="tk-prompt">$</span>
                  <span class="tk-cmd">vim</span>
                  <span class="tk-plain">app.py</span>
                </div>
                <div class="tline t2 diff-del">- base_url = "https://api.openai.com/v1"</div>
                <div class="tline t3 diff-add">+ base_url = "{{ baseOrigin }}/v1"</div>
                <div class="tline t4 mt-3">
                  <span class="tk-prompt">$</span>
                  <span class="tk-cmd">python</span>
                  <span class="tk-plain">app.py</span>
                </div>
                <div class="tline t5">
                  <span class="tk-ok">200 OK</span>
                  <span class="tk-dim">claude-sonnet-4-6 · stream</span>
                </div>
                <div class="tline t6 tk-dim">data: {"content": "Hello!"}</div>
                <div class="tline t7">
                  <span class="tk-prompt">$</span>
                  <span class="caret"></span>
                </div>
              </div>
            </div>
          </div>
        </section>

        <!-- Supported Providers -->
        <section data-reveal class="pb-4 pt-12">
          <p class="text-center text-sm font-medium text-gray-500 dark:text-dark-400">
            {{ t('home.providers.title') }}
          </p>
          <div class="mt-6 flex flex-wrap items-center justify-center gap-3">
            <div
              v-for="p in providerChips"
              :key="p.name"
              class="flex items-center gap-2.5 rounded-full border border-gray-200/80 bg-white/70 px-5 py-2.5 text-gray-800 backdrop-blur-sm transition-all hover:-translate-y-0.5 hover:border-primary-300/70 hover:shadow-md hover:shadow-primary-500/10 dark:border-dark-700/70 dark:bg-dark-800/70 dark:text-gray-100 dark:hover:border-primary-500/40"
            >
              <svg
                class="h-5 w-5"
                viewBox="0 0 24 24"
                fill="currentColor"
                fill-rule="evenodd"
                aria-hidden="true"
              >
                <path v-for="(d, i) in p.paths" :key="i" :d="d" />
              </svg>
              <span class="text-sm font-medium">{{ p.name }}</span>
            </div>
            <div
              class="flex items-center gap-2 rounded-full border border-dashed border-gray-300 px-5 py-2.5 text-gray-400 dark:border-dark-600 dark:text-dark-400"
            >
              <Icon name="plus" size="sm" />
              <span class="text-sm">{{ t('home.providers.more') }} · {{ t('home.providers.soon') }}</span>
            </div>
          </div>
        </section>

        <!-- Pain Points -->
        <section data-reveal class="pt-24 lg:pt-32">
          <div class="grid gap-10 lg:grid-cols-[0.85fr_1.15fr] lg:gap-16">
            <div class="self-start text-center lg:sticky lg:top-28 lg:text-left">
              <h2
                class="text-3xl font-bold tracking-tight text-gray-900 dark:text-white sm:text-4xl"
              >
                {{ t('home.painPoints.title') }}
              </h2>
              <div
                class="mx-auto mt-5 h-1 w-14 rounded-full bg-gradient-to-r from-primary-500 to-cyan-500 lg:mx-0"
              ></div>
            </div>
            <div class="grid gap-4 sm:grid-cols-2">
              <div
                v-for="item in painItems"
                :key="item.key"
                class="rounded-2xl border border-gray-200/70 bg-white/70 p-6 backdrop-blur-sm transition-all hover:-translate-y-0.5 hover:shadow-lg hover:shadow-gray-900/5 dark:border-dark-700/60 dark:bg-dark-800/60 dark:hover:shadow-black/20"
              >
                <div
                  class="flex h-10 w-10 items-center justify-center rounded-xl bg-gray-100 text-gray-500 dark:bg-dark-700 dark:text-dark-300"
                >
                  <Icon :name="item.icon" size="md" />
                </div>
                <h3 class="mt-4 font-semibold text-gray-900 dark:text-white">
                  {{ t(`home.painPoints.items.${item.key}.title`) }}
                </h3>
                <p class="mt-1.5 text-sm leading-relaxed text-gray-600 dark:text-dark-400">
                  {{ t(`home.painPoints.items.${item.key}.desc`) }}
                </p>
              </div>
            </div>
          </div>
        </section>

        <!-- Solutions Bento -->
        <section data-reveal class="pt-24 lg:pt-32">
          <h2
            class="text-center text-3xl font-bold tracking-tight text-gray-900 dark:text-white sm:text-4xl"
          >
            {{ t('home.solutions.title') }}
          </h2>
          <div class="mt-12 grid gap-4 lg:grid-cols-5">
            <!-- Big card: unified gateway with routing visual -->
            <div
              class="relative overflow-hidden rounded-3xl border border-primary-200/60 bg-gradient-to-br from-primary-500/10 via-white/70 to-cyan-500/10 p-8 backdrop-blur-sm dark:border-primary-500/20 dark:from-primary-500/10 dark:via-dark-800/70 dark:to-cyan-500/10 lg:col-span-3"
            >
              <div
                class="flex h-12 w-12 items-center justify-center rounded-2xl bg-gradient-to-br from-primary-500 to-cyan-600 text-white shadow-lg shadow-primary-500/30"
              >
                <Icon name="key" size="lg" />
              </div>
              <h3 class="mt-5 text-xl font-semibold text-gray-900 dark:text-white">
                {{ t('home.features.unifiedGateway') }}
              </h3>
              <p class="mt-2 max-w-md text-sm leading-relaxed text-gray-600 dark:text-dark-300">
                {{ t('home.features.unifiedGatewayDesc') }}
              </p>

              <!-- Routing visual -->
              <div class="mt-8 flex flex-wrap items-center gap-3">
                <div class="flex -space-x-2">
                  <div
                    v-for="p in providerChips"
                    :key="p.name"
                    class="flex h-10 w-10 items-center justify-center rounded-full border border-gray-200 bg-white text-gray-700 shadow-sm dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200"
                  >
                    <svg
                      class="h-5 w-5"
                      viewBox="0 0 24 24"
                      fill="currentColor"
                      fill-rule="evenodd"
                      aria-hidden="true"
                    >
                      <path v-for="(d, i) in p.paths" :key="i" :d="d" />
                    </svg>
                  </div>
                </div>
                <div
                  class="h-px min-w-8 flex-1 bg-gradient-to-r from-gray-300 to-primary-400 dark:from-dark-600 dark:to-primary-500"
                ></div>
                <div
                  class="flex items-center gap-2 whitespace-nowrap rounded-full bg-gray-900 px-4 py-2 font-mono text-xs text-primary-300 shadow-lg dark:bg-black/60"
                >
                  POST /v1/messages
                </div>
              </div>
            </div>

            <!-- Right column: two small cards -->
            <div class="grid gap-4 lg:col-span-2">
              <div
                class="rounded-3xl border border-gray-200/70 bg-white/70 p-7 backdrop-blur-sm dark:border-dark-700/60 dark:bg-dark-800/60"
              >
                <div
                  class="flex h-11 w-11 items-center justify-center rounded-2xl bg-gray-900 text-primary-400 dark:bg-dark-700"
                >
                  <Icon name="server" size="md" />
                </div>
                <h3 class="mt-4 font-semibold text-gray-900 dark:text-white">
                  {{ t('home.features.multiAccount') }}
                </h3>
                <p class="mt-1.5 text-sm leading-relaxed text-gray-600 dark:text-dark-400">
                  {{ t('home.features.multiAccountDesc') }}
                </p>
              </div>
              <div
                class="rounded-3xl border border-gray-200/70 bg-white/70 p-7 backdrop-blur-sm dark:border-dark-700/60 dark:bg-dark-800/60"
              >
                <div
                  class="flex h-11 w-11 items-center justify-center rounded-2xl bg-gray-900 text-cyan-400 dark:bg-dark-700"
                >
                  <Icon name="calculator" size="md" />
                </div>
                <h3 class="mt-4 font-semibold text-gray-900 dark:text-white">
                  {{ t('home.features.balanceQuota') }}
                </h3>
                <p class="mt-1.5 text-sm leading-relaxed text-gray-600 dark:text-dark-400">
                  {{ t('home.features.balanceQuotaDesc') }}
                </p>
              </div>
            </div>
          </div>
        </section>

        <!-- Comparison -->
        <section data-reveal class="pt-24 lg:pt-32">
          <h2
            class="text-center text-3xl font-bold tracking-tight text-gray-900 dark:text-white sm:text-4xl"
          >
            {{ t('home.comparison.title') }}
          </h2>
          <div
            class="mx-auto mt-12 max-w-4xl overflow-hidden rounded-3xl border border-gray-200/70 bg-white/80 shadow-sm backdrop-blur-sm dark:border-dark-700/60 dark:bg-dark-800/70"
          >
            <div class="overflow-x-auto">
              <table class="w-full min-w-[36rem] text-sm">
                <thead>
                  <tr class="border-b border-gray-200/80 dark:border-dark-700/80">
                    <th
                      class="px-6 py-4 text-left font-medium text-gray-500 dark:text-dark-400"
                    >
                      {{ t('home.comparison.headers.feature') }}
                    </th>
                    <th
                      class="px-6 py-4 text-left font-medium text-gray-500 dark:text-dark-400"
                    >
                      {{ t('home.comparison.headers.official') }}
                    </th>
                    <th
                      class="bg-primary-500/[0.06] px-6 py-4 text-left font-semibold text-primary-700 dark:bg-primary-500/10 dark:text-primary-300"
                    >
                      {{ t('home.comparison.headers.us') }}
                    </th>
                  </tr>
                </thead>
                <tbody class="divide-y divide-gray-100 dark:divide-dark-700/60">
                  <tr v-for="row in comparisonRows" :key="row">
                    <td class="px-6 py-4 font-medium text-gray-900 dark:text-white">
                      {{ t(`home.comparison.items.${row}.feature`) }}
                    </td>
                    <td class="px-6 py-4 text-gray-500 dark:text-dark-400">
                      {{ t(`home.comparison.items.${row}.official`) }}
                    </td>
                    <td
                      class="bg-primary-500/[0.06] px-6 py-4 text-gray-900 dark:bg-primary-500/10 dark:text-white"
                    >
                      <span class="flex items-start gap-2">
                        <Icon
                          name="checkCircle"
                          size="sm"
                          class="mt-0.5 shrink-0 text-primary-500"
                        />
                        {{ t(`home.comparison.items.${row}.us`) }}
                      </span>
                    </td>
                  </tr>
                </tbody>
              </table>
            </div>
          </div>
        </section>

        <!-- Steps -->
        <section data-reveal class="pt-24 lg:pt-32">
          <h2
            class="text-center text-3xl font-bold tracking-tight text-gray-900 dark:text-white sm:text-4xl"
          >
            {{ t('home.solutions.subtitle') }}
          </h2>
          <div class="relative mx-auto mt-14 max-w-4xl">
            <div
              class="absolute left-[16%] right-[16%] top-6 hidden h-px bg-gradient-to-r from-primary-300 via-cyan-300 to-sky-300 dark:from-primary-500/40 dark:via-cyan-500/40 dark:to-sky-500/40 sm:block"
            ></div>
            <div class="grid gap-10 sm:grid-cols-3 sm:gap-6">
              <div
                v-for="(step, idx) in stepItems"
                :key="step.key"
                class="relative flex flex-col items-center text-center"
              >
                <div
                  class="z-10 flex h-12 w-12 items-center justify-center rounded-2xl bg-gradient-to-br from-primary-600 to-cyan-700 text-lg font-bold text-white shadow-lg shadow-primary-500/30"
                >
                  {{ idx + 1 }}
                </div>
                <h3 class="mt-5 font-semibold text-gray-900 dark:text-white">
                  {{ t(`home.steps.${step.key}.title`) }}
                </h3>
                <p class="mt-1.5 max-w-[16rem] text-sm leading-relaxed text-gray-600 dark:text-dark-400">
                  {{ t(`home.steps.${step.key}.desc`) }}
                </p>
              </div>
            </div>
          </div>
        </section>

        <!-- CTA -->
        <section data-reveal class="pb-6 pt-24 lg:pt-28">
          <div
            class="relative overflow-hidden rounded-3xl bg-gradient-to-br from-primary-700 via-teal-700 to-cyan-800 px-6 py-14 text-center shadow-xl shadow-primary-500/20 sm:px-12"
          >
            <div
              class="pointer-events-none absolute -left-20 -top-24 h-64 w-64 rounded-full bg-white/10 blur-3xl"
            ></div>
            <div
              class="pointer-events-none absolute -bottom-24 -right-16 h-64 w-64 rounded-full bg-cyan-300/20 blur-3xl"
            ></div>
            <h2 class="relative text-3xl font-bold tracking-tight text-white sm:text-4xl">
              {{ t('home.cta.title') }}
            </h2>
            <p class="relative mx-auto mt-3 max-w-xl text-primary-50/95">
              {{ t('home.cta.description') }}
            </p>
            <router-link
              :to="ctaTarget"
              class="relative mt-8 inline-flex items-center gap-2 rounded-full bg-white px-8 py-3 text-sm font-semibold text-primary-700 shadow-lg transition-all hover:bg-primary-50 hover:shadow-xl active:scale-[0.98]"
            >
              {{ ctaLabel }}
              <Icon name="arrowRight" size="sm" :stroke-width="2" />
            </router-link>
          </div>
        </section>
      </div>
    </main>

    <!-- Footer -->
    <footer class="relative z-10 mt-16 border-t border-gray-200/60 px-6 py-8 dark:border-dark-800/60">
      <div
        class="mx-auto flex max-w-6xl flex-col items-center justify-between gap-4 text-center sm:flex-row sm:text-left"
      >
        <p class="text-sm text-gray-500 dark:text-dark-400">
          &copy; {{ currentYear }} {{ siteName }}. {{ t('home.footer.allRightsReserved') }}
        </p>
        <div class="flex items-center gap-5">
          <router-link
            to="/key-usage"
            class="text-sm text-gray-500 transition-colors hover:text-gray-700 dark:text-dark-400 dark:hover:text-white"
          >
            {{ t('home.nav.keyUsage') }}
          </router-link>
          <a
            v-if="docUrl"
            :href="docUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="text-sm text-gray-500 transition-colors hover:text-gray-700 dark:text-dark-400 dark:hover:text-white"
          >
            {{ t('home.docs') }}
          </a>
          <a
            :href="githubUrl"
            target="_blank"
            rel="noopener noreferrer"
            class="text-sm text-gray-500 transition-colors hover:text-gray-700 dark:text-dark-400 dark:hover:text-white"
          >
            GitHub
          </a>
        </div>
      </div>
    </footer>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, onMounted, onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAuthStore, useAppStore } from '@/stores'
import LocaleSwitcher from '@/components/common/LocaleSwitcher.vue'
import Icon from '@/components/icons/Icon.vue'
import { sanitizeUrl } from '@/utils/url'
import { BRAND_PATHS } from '@/constants/brandIcons'

const { t } = useI18n()

const authStore = useAuthStore()
const appStore = useAppStore()

// Site settings - directly from appStore (already initialized from injected config)
const siteName = computed(() => appStore.cachedPublicSettings?.site_name || appStore.siteName || 'Sub2API')
const siteLogo = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.site_logo || appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const siteSubtitle = computed(() => appStore.cachedPublicSettings?.site_subtitle || t('home.heroSubtitle'))
const docUrl = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.doc_url || appStore.docUrl || ''))
const homeContent = computed(() => appStore.cachedPublicSettings?.home_content || '')

// Check if homeContent is a URL (for iframe display)
const isHomeContentUrl = computed(() => {
  const content = homeContent.value.trim()
  return content.startsWith('http://') || content.startsWith('https://')
})

// Theme
const isDark = ref(document.documentElement.classList.contains('dark'))

// GitHub URL
const githubUrl = 'https://github.com/Wei-Shaw/sub2api'

// Current deployment origin, shown in the hero terminal migration snippet
const baseOrigin = window.location.origin

// Auth state
const isAuthenticated = computed(() => authStore.isAuthenticated)
const isAdmin = computed(() => authStore.isAdmin)
const dashboardPath = computed(() => isAdmin.value ? '/admin/dashboard' : '/dashboard')
const userInitial = computed(() => {
  const user = authStore.user
  if (!user || !user.email) return ''
  return user.email.charAt(0).toUpperCase()
})

// Registration availability drives the bottom CTA target
const registrationEnabled = computed(
  () => appStore.cachedPublicSettings?.registration_enabled !== false
)
const ctaTarget = computed(() => {
  if (isAuthenticated.value) return dashboardPath.value
  return registrationEnabled.value ? '/register' : '/login'
})
const ctaLabel = computed(() => {
  if (isAuthenticated.value) return t('home.goToDashboard')
  return registrationEnabled.value ? t('home.cta.button') : t('home.getStarted')
})

const providerChips = computed(() => [
  { name: t('home.providers.claude'), paths: BRAND_PATHS.anthropic },
  { name: t('home.providers.gpt'), paths: BRAND_PATHS.openai },
  { name: t('home.providers.gemini'), paths: BRAND_PATHS.gemini },
  { name: t('home.providers.grok'), paths: BRAND_PATHS.xai },
])

// Section data
const painItems = [
  { key: 'expensive', icon: 'creditCard' },
  { key: 'complex', icon: 'key' },
  { key: 'unstable', icon: 'exclamationTriangle' },
  { key: 'noControl', icon: 'chartBar' },
] as const

const comparisonRows = ['pricing', 'models', 'management', 'stability', 'control'] as const

const stepItems = [{ key: 'register' }, { key: 'createKey' }, { key: 'replaceUrl' }] as const

// Current year for footer
const currentYear = computed(() => new Date().getFullYear())

// Toggle theme
function toggleTheme() {
  isDark.value = !isDark.value
  document.documentElement.classList.toggle('dark', isDark.value)
  localStorage.setItem('theme', isDark.value ? 'dark' : 'light')
}

// Initialize theme
function initTheme() {
  const savedTheme = localStorage.getItem('theme')
  if (
    savedTheme === 'dark' ||
    (!savedTheme && window.matchMedia('(prefers-color-scheme: dark)').matches)
  ) {
    isDark.value = true
    document.documentElement.classList.add('dark')
  }
}

// Scroll reveal (IntersectionObserver; falls back to static under reduced motion)
let revealObserver: IntersectionObserver | null = null

function initReveal() {
  const targets = document.querySelectorAll<HTMLElement>('[data-reveal]')
  const reduceMotion = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  if (reduceMotion || !('IntersectionObserver' in window)) {
    targets.forEach((el) => el.classList.add('is-revealed'))
    return
  }
  revealObserver = new IntersectionObserver(
    (entries) => {
      for (const entry of entries) {
        if (entry.isIntersecting) {
          entry.target.classList.add('is-revealed')
          revealObserver?.unobserve(entry.target)
        }
      }
    },
    { threshold: 0.12 }
  )
  targets.forEach((el) => revealObserver!.observe(el))
}

onMounted(() => {
  initTheme()

  // Check auth state
  authStore.checkAuth()

  // Ensure public settings are loaded (will use cache if already loaded from injected config)
  if (!appStore.publicSettingsLoaded) {
    appStore.fetchPublicSettings()
  }

  initReveal()
})

onUnmounted(() => {
  revealObserver?.disconnect()
  revealObserver = null
})
</script>

<style scoped>
/* ============ Hero ============ */
.hero-gradient-text {
  background: linear-gradient(100deg, #0d9488 0%, #06b6d4 55%, #0ea5e9 100%);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
}

:global(.dark) .hero-gradient-text {
  background: linear-gradient(100deg, #2dd4bf 0%, #22d3ee 55%, #38bdf8 100%);
  -webkit-background-clip: text;
  background-clip: text;
}

.hero-enter > * {
  opacity: 0;
  animation: rise-in 0.7s cubic-bezier(0.16, 1, 0.3, 1) forwards;
}

.hero-enter > *:nth-child(1) {
  animation-delay: 0.05s;
}
.hero-enter > *:nth-child(2) {
  animation-delay: 0.12s;
}
.hero-enter > *:nth-child(3) {
  animation-delay: 0.19s;
}
.hero-enter > *:nth-child(4) {
  animation-delay: 0.26s;
}
.hero-enter > *:nth-child(5) {
  animation-delay: 0.33s;
}

@keyframes rise-in {
  from {
    opacity: 0;
    transform: translateY(18px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

/* ============ Scroll Reveal ============ */
[data-reveal] {
  opacity: 0;
  transform: translateY(26px);
  transition:
    opacity 0.7s cubic-bezier(0.16, 1, 0.3, 1),
    transform 0.7s cubic-bezier(0.16, 1, 0.3, 1);
}

[data-reveal].is-revealed {
  opacity: 1;
  transform: none;
}

/* ============ Terminal ============ */
.terminal-window {
  background: linear-gradient(150deg, #10222b 0%, #0a1420 100%);
  border: 1px solid rgba(45, 212, 191, 0.16);
  border-radius: 20px;
  box-shadow:
    0 30px 60px -15px rgba(2, 44, 34, 0.35),
    inset 0 1px 0 rgba(255, 255, 255, 0.08);
  overflow: hidden;
}

.terminal-header {
  display: flex;
  align-items: center;
  padding: 13px 18px;
  background: rgba(255, 255, 255, 0.04);
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
}

.terminal-buttons {
  display: flex;
  gap: 8px;
}

.terminal-buttons span {
  width: 11px;
  height: 11px;
  border-radius: 50%;
}

.dot-close {
  background: #f87171;
}
.dot-min {
  background: #fbbf24;
}
.dot-max {
  background: #34d399;
}

.terminal-title {
  flex: 1;
  text-align: center;
  font-size: 12px;
  font-family: ui-monospace, 'Fira Code', monospace;
  color: #5eead4;
  opacity: 0.55;
  margin-right: 49px;
}

.terminal-body {
  padding: 22px 24px 24px;
  font-family: ui-monospace, 'Fira Code', monospace;
  font-size: 13px;
  line-height: 1.9;
  word-break: break-all;
}

@media (max-width: 480px) {
  .terminal-body {
    padding: 16px 16px 18px;
    font-size: 11.5px;
  }
}

.tline {
  display: flex;
  align-items: baseline;
  gap: 8px;
  flex-wrap: wrap;
  opacity: 0;
  animation: line-appear 0.5s ease forwards;
}

.t1 {
  animation-delay: 0.4s;
}
.t2 {
  animation-delay: 0.9s;
}
.t3 {
  animation-delay: 1.3s;
}
.t4 {
  animation-delay: 1.9s;
}
.t5 {
  animation-delay: 2.5s;
}
.t6 {
  animation-delay: 2.8s;
}
.t7 {
  animation-delay: 3.2s;
}

@keyframes line-appear {
  from {
    opacity: 0;
    transform: translateY(5px);
  }
  to {
    opacity: 1;
    transform: translateY(0);
  }
}

.tk-prompt {
  color: #34d399;
  font-weight: 700;
}
.tk-cmd {
  color: #38bdf8;
}
.tk-plain {
  color: #cbd5e1;
}
.tk-dim {
  color: #64748b;
}
.tk-ok {
  color: #34d399;
  background: rgba(52, 211, 153, 0.12);
  padding: 1px 8px;
  border-radius: 6px;
  font-weight: 600;
}

.diff-del {
  color: #fda4af;
  background: rgba(244, 63, 94, 0.09);
  border-radius: 6px;
  padding: 0 8px;
  text-decoration: line-through;
  text-decoration-color: rgba(253, 164, 175, 0.5);
}

.diff-add {
  color: #6ee7b7;
  background: rgba(16, 185, 129, 0.12);
  border-radius: 6px;
  padding: 0 8px;
}

.caret {
  display: inline-block;
  width: 8px;
  height: 15px;
  background: #34d399;
  transform: translateY(2px);
  animation: blink 1s step-end infinite;
}

@keyframes blink {
  0%,
  50% {
    opacity: 1;
  }
  51%,
  100% {
    opacity: 0;
  }
}

/* ============ Reduced Motion ============ */
@media (prefers-reduced-motion: reduce) {
  .hero-enter > *,
  .tline {
    animation: none;
    opacity: 1;
  }

  .caret {
    animation: none;
  }

  [data-reveal] {
    opacity: 1;
    transform: none;
    transition: none;
  }
}
</style>
