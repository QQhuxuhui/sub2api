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
        class="absolute inset-x-0 top-0 h-full bg-[linear-gradient(rgba(20,184,166,0.05)_1px,transparent_1px),linear-gradient(90deg,rgba(20,184,166,0.05)_1px,transparent_1px)] bg-[size:56px_56px] [mask-image:radial-gradient(ellipse_75%_65%_at_50%_0%,#000,transparent)] dark:bg-[linear-gradient(rgba(45,212,191,0.06)_1px,transparent_1px),linear-gradient(90deg,rgba(45,212,191,0.06)_1px,transparent_1px)]"
      ></div>
    </div>

    <main class="relative z-10 flex flex-1 items-center justify-center px-4 py-10 sm:px-6">
      <!-- 玻璃大面板 -->
      <div
        class="auth-panel w-full max-w-6xl rounded-[2rem] border border-white/80 bg-white/55 p-6 shadow-2xl shadow-primary-500/10 backdrop-blur-2xl dark:border-dark-700/50 dark:bg-dark-900/50 sm:p-10 lg:p-14"
      >
        <div class="grid items-center gap-10 lg:grid-cols-[1.05fr_0.95fr] lg:gap-14">
          <!-- 左栏：品牌叙事 -->
          <div class="hidden lg:block">
            <router-link to="/home" class="brand-row flex items-center gap-4">
              <template v-if="settingsLoaded">
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
                  <p class="mt-1 truncate text-sm text-gray-500 dark:text-dark-400">
                    {{ siteSubtitle }}
                  </p>
                </div>
              </template>
            </router-link>

            <!-- 终端心跳：循环重演一次登录叙事 -->
            <div class="auth-term mt-6" aria-hidden="true">
              <div class="term-bar">
                <span class="dot-r"></span>
                <span class="dot-y"></span>
                <span class="dot-g"></span>
              </div>
              <div class="term-body">
                <div class="tline tl-1">
                  <span class="tk-p">$</span>
                  <span class="tk-c">curl</span>
                  <span class="tk-t">{{ baseOrigin }}/v1/messages</span>
                </div>
                <div class="tline tl-2">
                  <span class="tk-ok">200 OK</span>
                  <span class="tk-d">claude-sonnet-4-6 · stream</span>
                </div>
                <div class="tline tl-3">
                  <span class="tk-d">data: {"content": "Hello!"}</span>
                </div>
                <div class="tline tl-4">
                  <span class="tk-p">$</span>
                  <span class="term-caret"></span>
                </div>
              </div>
            </div>

            <!-- AI 对话与多模态能力 -->
            <div class="auth-chat mt-5 flex max-w-md flex-col gap-2.5" aria-hidden="true">
              <div
                class="bub-user self-end rounded-2xl rounded-br-md border border-primary-200/70 bg-white/80 px-4 py-3 text-[13.5px] leading-relaxed text-gray-800 dark:border-primary-500/25 dark:bg-dark-800/70 dark:text-gray-100"
              >
                <span
                  class="mb-2 inline-flex items-center gap-1.5 rounded-lg border border-primary-200 bg-primary-50 px-2.5 py-1 font-mono text-[11.5px] text-primary-700 dark:border-primary-500/25 dark:bg-primary-500/10 dark:text-primary-300"
                >
                  <Icon name="document" size="xs" />
                  diagram.png
                </span>
                <p>{{ t('home.authPanel.chatQuestion') }}</p>
              </div>
              <div
                class="bub-ai flex items-start gap-2.5 self-start rounded-2xl rounded-bl-md border border-gray-200/80 bg-white/80 px-4 py-3 text-[13.5px] text-gray-700 dark:border-dark-700/70 dark:bg-dark-800/70 dark:text-gray-200"
              >
                <span
                  class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-gray-900 text-white dark:bg-dark-700"
                >
                  <svg
                    class="h-3.5 w-3.5"
                    viewBox="0 0 24 24"
                    fill="currentColor"
                    fill-rule="evenodd"
                    aria-hidden="true"
                  >
                    <path v-for="(d, i) in BRAND_PATHS.anthropic" :key="i" :d="d" />
                  </svg>
                </span>
                <span class="flex min-h-6 items-center">
                  <span class="ai-wipe">{{ t('home.authPanel.chatAnswer') }}</span>
                  <span class="stream-caret ml-1.5"></span>
                </span>
              </div>
              <div class="cap-row mt-1 flex flex-wrap gap-2">
                <span
                  v-for="cap in capabilities"
                  :key="cap.key"
                  class="cap inline-flex items-center gap-1.5 rounded-full border border-gray-200/80 bg-white/70 px-3 py-1.5 text-xs text-gray-700 dark:border-dark-700/70 dark:bg-dark-800/60 dark:text-gray-300"
                >
                  <Icon :name="cap.icon" size="xs" />
                  {{ t(`home.authPanel.capabilities.${cap.key}`) }}
                </span>
              </div>
            </div>

            <!-- 联系我们 -->
            <div
              v-if="telegramUrl || qqGroupUrl"
              class="contact-row mt-6 flex flex-wrap items-center gap-2.5"
            >
              <span class="text-[13px] text-gray-500 dark:text-dark-400">
                {{ t('home.authPanel.contact') }}
              </span>
              <a
                v-if="telegramUrl"
                :href="telegramUrl"
                target="_blank"
                rel="noopener noreferrer"
                class="contact-pill inline-flex items-center gap-2 rounded-full border border-gray-200/90 bg-white/75 px-4 py-2 text-[13px] font-medium text-gray-700 backdrop-blur-sm transition-all hover:-translate-y-0.5 hover:border-primary-300 hover:shadow-md hover:shadow-primary-500/10 dark:border-dark-700/80 dark:bg-dark-800/75 dark:text-gray-200 dark:hover:border-primary-500/40"
              >
                <svg class="h-4 w-4 text-[#26A5E4]" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
                  <path :d="TELEGRAM_PATH" />
                </svg>
                Telegram
              </a>
              <a
                v-if="qqGroupUrl"
                :href="qqGroupUrl"
                target="_blank"
                rel="noopener noreferrer"
                class="contact-pill inline-flex items-center gap-2 rounded-full border border-gray-200/90 bg-white/75 px-4 py-2 text-[13px] font-medium text-gray-700 backdrop-blur-sm transition-all hover:-translate-y-0.5 hover:border-primary-300 hover:shadow-md hover:shadow-primary-500/10 dark:border-dark-700/80 dark:bg-dark-800/75 dark:text-gray-200 dark:hover:border-primary-500/40"
              >
                <svg class="h-4 w-4 text-[#1EBAFC]" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
                  <path :d="QQ_PATH" />
                </svg>
                QQ 群
              </a>
            </div>
          </div>

          <!-- 右栏：表单卡 -->
          <div class="mx-auto w-full max-w-md">
            <!-- 移动端品牌 -->
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

            <div
              class="auth-card rounded-3xl border border-gray-100 bg-white p-8 shadow-lg shadow-gray-900/5 dark:border-dark-700/60 dark:bg-dark-800/80 dark:shadow-black/20"
            >
              <slot />
            </div>

            <!-- Footer Links -->
            <div class="auth-card-foot mt-6 text-center text-sm">
              <slot name="footer" />
            </div>

            <!-- 移动端联系入口 -->
            <div
              v-if="telegramUrl || qqGroupUrl"
              class="mt-6 flex justify-center gap-2.5 lg:hidden"
            >
              <a
                v-if="telegramUrl"
                :href="telegramUrl"
                target="_blank"
                rel="noopener noreferrer"
                class="inline-flex items-center gap-2 rounded-full border border-gray-200 bg-white px-4 py-2 text-[13px] font-medium text-gray-700 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200"
              >
                <svg class="h-4 w-4 text-[#26A5E4]" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
                  <path :d="TELEGRAM_PATH" />
                </svg>
                Telegram
              </a>
              <a
                v-if="qqGroupUrl"
                :href="qqGroupUrl"
                target="_blank"
                rel="noopener noreferrer"
                class="inline-flex items-center gap-2 rounded-full border border-gray-200 bg-white px-4 py-2 text-[13px] font-medium text-gray-700 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200"
              >
                <svg class="h-4 w-4 text-[#1EBAFC]" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true">
                  <path :d="QQ_PATH" />
                </svg>
                QQ 群
              </a>
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
import Icon from '@/components/icons/Icon.vue'

const { t } = useI18n()

const appStore = useAppStore()

const siteName = computed(() => appStore.siteName || 'Sub2API')
const siteLogo = computed(() => sanitizeUrl(appStore.siteLogo || '', { allowRelative: true, allowDataUrl: true }))
const siteSubtitle = computed(() => appStore.cachedPublicSettings?.site_subtitle || t('home.heroSubtitle'))
const settingsLoaded = computed(() => appStore.publicSettingsLoaded)

const telegramUrl = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.telegram_url || ''))
const qqGroupUrl = computed(() => sanitizeUrl(appStore.cachedPublicSettings?.qq_group_url || ''))

const baseOrigin = window.location.origin

const currentYear = computed(() => new Date().getFullYear())

// Official brand marks from Simple Icons (Telegram / QQ)
const TELEGRAM_PATH =
  'M11.944 0A12 12 0 0 0 0 12a12 12 0 0 0 12 12 12 12 0 0 0 12-12A12 12 0 0 0 12 0a12 12 0 0 0-.056 0zm4.962 7.224c.1-.002.321.023.465.14a.506.506 0 0 1 .171.325c.016.093.036.306.02.472-.18 1.898-.962 6.502-1.36 8.627-.168.9-.499 1.201-.82 1.23-.696.065-1.225-.46-1.9-.902-1.056-.693-1.653-1.124-2.678-1.8-1.185-.78-.417-1.21.258-1.91.177-.184 3.247-2.977 3.307-3.23.007-.032.014-.15-.056-.212s-.174-.041-.249-.024c-.106.024-1.793 1.14-5.061 3.345-.48.33-.913.49-1.302.48-.428-.008-1.252-.241-1.865-.44-.752-.245-1.349-.374-1.297-.789.027-.216.325-.437.893-.663 3.498-1.524 5.83-2.529 6.998-3.014 3.332-1.386 4.025-1.627 4.476-1.635z'
const QQ_PATH =
  'M21.395 15.035a40 40 0 0 0-.803-2.264l-1.079-2.695c.001-.032.014-.562.014-.836C19.526 4.632 17.351 0 12 0S4.474 4.632 4.474 9.241c0 .274.013.804.014.836l-1.08 2.695a39 39 0 0 0-.802 2.264c-1.021 3.283-.69 4.643-.438 4.673.54.065 2.103-2.472 2.103-2.472 0 1.469.756 3.387 2.394 4.771-.612.188-1.363.479-1.845.835-.434.32-.379.646-.301.778.343.578 5.883.369 7.482.189 1.6.18 7.14.389 7.483-.189.078-.132.132-.458-.301-.778-.483-.356-1.233-.646-1.846-.836 1.637-1.384 2.393-3.302 2.393-4.771 0 0 1.563 2.537 2.103 2.472.251-.03.581-1.39-.438-4.673'

const capabilities = [
  { key: 'chat', icon: 'chat' },
  { key: 'vision', icon: 'eye' },
  { key: 'image', icon: 'sparkles' },
  { key: 'stream', icon: 'bolt' },
] as const

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

/* ============ 叙事入场 ============ */
@keyframes panel-in {
  from {
    opacity: 0;
    transform: translateY(26px) scale(0.97);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

@keyframes rise {
  from {
    opacity: 0;
    transform: translateY(16px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

@keyframes card-in {
  from {
    opacity: 0;
    transform: translateX(26px);
  }
  to {
    opacity: 1;
    transform: none;
  }
}

@keyframes pop {
  0% {
    opacity: 0;
    transform: scale(0.6);
  }
  70% {
    transform: scale(1.08);
  }
  100% {
    opacity: 1;
    transform: scale(1);
  }
}

.auth-panel {
  animation: panel-in 0.8s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.brand-row {
  animation: rise 0.6s 0.15s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.auth-term {
  animation: rise 0.6s 0.25s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.bub-user {
  animation: rise 0.6s 0.85s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.bub-ai {
  animation: rise 0.6s 1.35s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.ai-wipe {
  display: inline-block;
  white-space: nowrap;
  clip-path: inset(0 100% 0 0);
  animation: wipe-in 1.3s ease-out 1.8s forwards;
}

@keyframes wipe-in {
  to {
    clip-path: inset(0 0 0 0);
  }
}

.cap-row .cap {
  opacity: 0;
  animation: pop 0.45s cubic-bezier(0.34, 1.56, 0.64, 1) both;
}

.cap-row .cap:nth-child(1) {
  animation-delay: 2.5s;
}
.cap-row .cap:nth-child(2) {
  animation-delay: 2.62s;
}
.cap-row .cap:nth-child(3) {
  animation-delay: 2.74s;
}
.cap-row .cap:nth-child(4) {
  animation-delay: 2.86s;
}

.contact-row {
  animation: rise 0.6s 0.45s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.auth-card {
  animation: card-in 0.7s 0.25s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.auth-card-foot {
  animation: rise 0.6s 0.45s cubic-bezier(0.16, 1, 0.3, 1) both;
}

/* ============ 终端心跳 ============ */
.auth-term {
  background: linear-gradient(150deg, #10222b 0%, #0a1420 100%);
  border: 1px solid rgba(45, 212, 191, 0.16);
  border-radius: 18px;
  box-shadow:
    0 24px 50px -14px rgba(2, 44, 34, 0.32),
    inset 0 1px 0 rgba(255, 255, 255, 0.08);
  overflow: hidden;
}

.term-bar {
  display: flex;
  gap: 7px;
  padding: 12px 16px;
  background: rgba(255, 255, 255, 0.04);
  border-bottom: 1px solid rgba(255, 255, 255, 0.06);
}

.term-bar span {
  width: 10px;
  height: 10px;
  border-radius: 50%;
}

.dot-r {
  background: #f87171;
}
.dot-y {
  background: #fbbf24;
}
.dot-g {
  background: #34d399;
}

.term-body {
  padding: 16px 18px 18px;
  font-family: ui-monospace, 'Fira Code', Menlo, monospace;
  font-size: 12.5px;
  line-height: 1.95;
  color: #cbd5e1;
  word-break: break-all;
}

.tline {
  display: flex;
  align-items: baseline;
  gap: 7px;
  flex-wrap: wrap;
  opacity: 0;
}

/* 心跳：10 秒一轮，循环重演登录叙事 */
.tl-1 {
  animation: beat-1 10s infinite;
}
.tl-2 {
  animation: beat-2 10s infinite;
}
.tl-3 {
  animation: beat-3 10s infinite;
}
.tl-4 {
  animation: beat-4 10s infinite;
}

@keyframes beat-1 {
  0%, 4% { opacity: 0; transform: translateY(4px); }
  9%, 92% { opacity: 1; transform: none; }
  97%, 100% { opacity: 0; transform: none; }
}

@keyframes beat-2 {
  0%, 18% { opacity: 0; transform: translateY(4px); }
  24%, 92% { opacity: 1; transform: none; }
  97%, 100% { opacity: 0; transform: none; }
}

@keyframes beat-3 {
  0%, 28% { opacity: 0; transform: translateY(4px); }
  34%, 92% { opacity: 1; transform: none; }
  97%, 100% { opacity: 0; transform: none; }
}

@keyframes beat-4 {
  0%, 38% { opacity: 0; transform: translateY(4px); }
  44%, 92% { opacity: 1; transform: none; }
  97%, 100% { opacity: 0; transform: none; }
}

.tk-p {
  color: #34d399;
  font-weight: 700;
}
.tk-c {
  color: #38bdf8;
}
.tk-t {
  color: #cbd5e1;
}
.tk-d {
  color: #64748b;
}
.tk-ok {
  color: #34d399;
  background: rgba(52, 211, 153, 0.12);
  padding: 1px 8px;
  border-radius: 6px;
  font-weight: 600;
}

.term-caret,
.stream-caret {
  display: inline-block;
  width: 7px;
  height: 13px;
  background: #34d399;
  transform: translateY(2px);
  animation: caret-blink 1s step-end infinite;
}

.stream-caret {
  background: #14b8a6;
  transform: none;
}

@keyframes caret-blink {
  0%,
  50% {
    opacity: 1;
  }
  51%,
  100% {
    opacity: 0;
  }
}

/* ============ 表单微调 ============ */
.auth-card :deep(.input) {
  min-height: 46px;
}

.auth-card :deep(.btn-primary) {
  min-height: 46px;
  border-radius: 999px;
  background-image: linear-gradient(90deg, #0f766e, #0e7490);
}

.auth-card :deep(.btn-primary:hover) {
  filter: brightness(1.1);
}

/* ============ 降低动态 ============ */
@media (prefers-reduced-motion: reduce) {
  .auth-panel,
  .brand-row,
  .auth-term,
  .bub-user,
  .bub-ai,
  .ai-wipe,
  .cap-row .cap,
  .contact-row,
  .auth-card,
  .auth-card-foot,
  .tline,
  .term-caret,
  .stream-caret {
    animation: none;
    opacity: 1;
    clip-path: none;
  }
}
</style>
