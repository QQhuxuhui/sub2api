<template>
  <div
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
        class="absolute inset-x-0 top-0 h-full bg-[linear-gradient(rgba(20,184,166,0.05)_1px,transparent_1px),linear-gradient(90deg,rgba(20,184,166,0.05)_1px,transparent_1px)] bg-[size:56px_56px] [mask-image:radial-gradient(ellipse_75%_65%_at_50%_0%,#000,transparent)] dark:bg-[linear-gradient(rgba(45,212,191,0.06)_1px,transparent_1px),linear-gradient(90deg,rgba(45,212,191,0.06)_1px,transparent_1px)]"
      ></div>
    </div>

    <main class="relative z-10 flex flex-1 items-center justify-center px-4 py-10 sm:px-6">
      <div
        class="w-full max-w-6xl rounded-[2rem] border border-white/80 bg-white/55 p-6 shadow-2xl shadow-primary-500/10 backdrop-blur-2xl dark:border-dark-700/50 dark:bg-dark-900/50 sm:p-10 lg:p-14"
      >
        <div class="grid items-center gap-10 lg:grid-cols-[1.05fr_0.95fr] lg:gap-16">
        <!-- Left: Brand Narrative (desktop only) -->
        <div class="hidden lg:block">
          <router-link
            to="/home"
            class="inline-flex items-center gap-2 rounded-full border border-primary-200/70 bg-white/70 px-4 py-1.5 text-xs font-medium text-primary-700 shadow-sm backdrop-blur-sm transition-colors hover:border-primary-300 hover:text-primary-800 dark:border-primary-500/30 dark:bg-primary-500/10 dark:text-primary-300 dark:hover:border-primary-500/50"
          >
            {{ t('home.badge') }}
          </router-link>

          <template v-if="settingsLoaded">
            <div class="mt-8 flex items-center gap-4">
              <div
                class="h-14 w-14 shrink-0 overflow-hidden rounded-2xl shadow-lg shadow-primary-500/20"
              >
                <img
                  :src="siteLogo || '/logo.png'"
                  alt="Logo"
                  class="h-full w-full object-contain"
                />
              </div>
              <div class="min-w-0">
                <h1 class="auth-gradient-text truncate text-3xl font-bold tracking-tight">
                  {{ siteName }}
                </h1>
                <p class="mt-1 text-sm text-gray-500 dark:text-dark-400">
                  {{ siteSubtitle }}
                </p>
              </div>
            </div>
          </template>

          <p class="mt-6 max-w-md text-base leading-relaxed text-gray-600 dark:text-dark-300">
            {{ t('home.heroDescription') }}
          </p>

          <div class="mt-10 flex items-center gap-4">
            <div class="flex">
              <span
                v-for="(paths, key) in BRAND_PATHS"
                :key="key"
                class="brand-dot flex h-10 w-10 items-center justify-center rounded-full border border-gray-200 bg-white text-gray-700 shadow-sm dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200"
              >
                <svg class="h-5 w-5" viewBox="0 0 24 24" fill="currentColor" fill-rule="evenodd" aria-hidden="true">
                  <path v-for="(d, i) in paths" :key="i" :d="d" />
                </svg>
              </span>
            </div>
            <span class="text-sm text-gray-500 dark:text-dark-400">
              {{ t('home.providers.title') }}
            </span>
          </div>
        </div>

        <!-- Right: Card Column -->
        <div class="mx-auto w-full max-w-md">
          <!-- Mobile Brand -->
          <div class="mb-8 text-center lg:hidden">
            <template v-if="settingsLoaded">
              <div
                class="inline-flex h-14 w-14 items-center justify-center overflow-hidden rounded-2xl shadow-lg shadow-primary-500/20"
              >
                <img
                  :src="siteLogo || '/logo.png'"
                  alt="Logo"
                  class="h-full w-full object-contain"
                />
              </div>
              <h1 class="auth-gradient-text mt-4 text-2xl font-bold tracking-tight">
                {{ siteName }}
              </h1>
              <p class="mt-1.5 text-sm text-gray-500 dark:text-dark-400">
                {{ siteSubtitle }}
              </p>
            </template>
          </div>

          <!-- Card -->
          <div
            class="rounded-3xl border border-gray-100 bg-white p-8 shadow-lg shadow-gray-900/5 dark:border-dark-700/60 dark:bg-dark-800/80 dark:shadow-black/20"
          >
            <slot />
          </div>

          <!-- Footer Links -->
          <div class="mt-6 text-center text-sm">
            <slot name="footer" />
          </div>

          <!-- Copyright -->
          <p class="mt-8 text-center text-xs text-gray-400 dark:text-dark-500">
            &copy; {{ currentYear }} {{ siteName }}. {{ t('home.footer.allRightsReserved') }}
          </p>
        </div>
        </div>
      </div>
    </main>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useAppStore } from '@/stores'
import { sanitizeUrl } from '@/utils/url'
import { BRAND_PATHS } from '@/constants/brandIcons'

const { t } = useI18n()

const appStore = useAppStore()

const siteName = computed(() => appStore.siteName || 'Sub2API')
const siteLogo = computed(() => sanitizeUrl(appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const siteSubtitle = computed(() => appStore.cachedPublicSettings?.site_subtitle || t('home.heroSubtitle'))
const settingsLoaded = computed(() => appStore.publicSettingsLoaded)

const currentYear = computed(() => new Date().getFullYear())

onMounted(() => {
  appStore.fetchPublicSettings()
})
</script>

<style scoped>
.auth-gradient-text {
  background: linear-gradient(100deg, #0d9488 0%, #06b6d4 55%, #0ea5e9 100%);
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
}

:global(.dark) .auth-gradient-text {
  background: linear-gradient(100deg, #2dd4bf 0%, #22d3ee 55%, #38bdf8 100%);
  -webkit-background-clip: text;
  background-clip: text;
}

.brand-dot + .brand-dot {
  margin-left: -8px;
}
</style>
