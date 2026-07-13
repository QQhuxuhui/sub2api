<template>
  <div class="flex min-h-screen flex-col overflow-x-clip bg-gray-50 dark:bg-dark-950">
    <div class="flex flex-1 flex-col lg:grid lg:grid-cols-[0.9fr_1.1fr]">
      <!-- Left: Brand Panel -->
      <aside class="auth-brand relative overflow-hidden px-6 py-6 text-white lg:flex lg:flex-col lg:justify-between lg:px-14 lg:py-12">
        <div ref="gridLayer" class="auth-gridlayer" aria-hidden="true"></div>
        <div ref="glowLayer" class="auth-glow" aria-hidden="true"></div>

        <!-- Top: brand + description + code chip -->
        <div class="relative">
          <router-link to="/home" class="brand-row flex items-center gap-4">
            <template v-if="settingsLoaded">
              <div
                class="flex h-12 w-12 shrink-0 items-center justify-center overflow-hidden rounded-2xl bg-white/15 shadow-[inset_0_1px_0_rgba(255,255,255,0.25)]"
              >
                <img
                  :src="siteLogo || '/logo.png'"
                  alt="Logo"
                  class="h-full w-full object-contain"
                />
              </div>
              <div class="min-w-0">
                <h1 class="truncate text-2xl font-bold tracking-tight">{{ siteName }}</h1>
                <p class="mt-0.5 truncate text-sm text-primary-50/85">{{ siteSubtitle }}</p>
              </div>
            </template>
          </router-link>

          <p class="brand-desc mt-7 hidden max-w-sm text-[15px] leading-relaxed text-primary-50/90 lg:block">
            {{ t('home.heroDescription') }}
          </p>

          <div
            class="brand-code mt-6 hidden items-center gap-2 rounded-full border border-primary-200/25 bg-[rgba(2,44,34,0.45)] px-4 py-2.5 font-mono text-xs text-primary-200 lg:inline-flex"
          >
            <span class="code-wipe">base_url = "{{ baseOrigin }}/v1"</span>
          </div>
        </div>

        <!-- Middle: AI chat vignette -->
        <div class="auth-chat relative mt-8 hidden max-w-md flex-col gap-3 lg:flex" aria-hidden="true">
          <div class="bub-user self-end rounded-2xl rounded-br-md border border-white/20 bg-white/15 px-4 py-3 text-[13.5px] leading-relaxed text-primary-50">
            <span
              class="mb-2 inline-flex items-center gap-1.5 rounded-lg border border-primary-200/25 bg-[rgba(2,44,34,0.4)] px-2.5 py-1 font-mono text-[11.5px] text-primary-200"
            >
              <Icon name="document" size="xs" />
              diagram.png
            </span>
            <p>{{ t('home.authPanel.chatQuestion') }}</p>
          </div>
          <div
            class="bub-ai flex items-start gap-2.5 self-start rounded-2xl rounded-bl-md border border-primary-200/20 bg-[rgba(2,44,34,0.45)] px-4 py-3 text-[13.5px] text-primary-100"
          >
            <span class="mt-0.5 flex h-6 w-6 shrink-0 items-center justify-center rounded-full bg-white/15">
              <svg class="h-3.5 w-3.5 text-primary-50" viewBox="0 0 24 24" fill="currentColor" fill-rule="evenodd" aria-hidden="true">
                <path v-for="(d, i) in BRAND_PATHS.anthropic" :key="i" :d="d" />
              </svg>
            </span>
            <span class="flex min-h-6 items-center">
              <span class="ai-wipe">{{ t('home.authPanel.chatAnswer') }}</span>
              <span class="stream-caret ml-1.5"></span>
            </span>
          </div>
          <div class="cap-row mt-1 flex flex-wrap gap-2">
            <span v-for="cap in capabilities" :key="cap.key" class="cap inline-flex items-center gap-1.5 rounded-full border border-white/25 bg-white/10 px-3 py-1.5 text-xs text-primary-50/95">
              <Icon :name="cap.icon" size="xs" />
              {{ t(`home.authPanel.capabilities.${cap.key}`) }}
            </span>
          </div>
        </div>

        <!-- Bottom: providers + contact -->
        <div class="relative mt-8 hidden flex-col gap-4 lg:flex">
          <div class="brand-dots flex">
            <span
              v-for="(paths, key) in BRAND_PATHS"
              :key="key"
              class="flex h-9 w-9 items-center justify-center rounded-full border border-white/25 bg-white/10 text-primary-50"
            >
              <svg class="h-[18px] w-[18px]" viewBox="0 0 24 24" fill="currentColor" fill-rule="evenodd" aria-hidden="true">
                <path v-for="(d, i) in paths" :key="i" :d="d" />
              </svg>
            </span>
          </div>
          <div v-if="telegramUrl || qqGroupUrl" class="contact-row flex flex-wrap items-center gap-2.5">
            <span class="text-[13px] text-primary-50/75">{{ t('home.authPanel.contact') }}</span>
            <a
              v-if="telegramUrl"
              :href="telegramUrl"
              target="_blank"
              rel="noopener noreferrer"
              class="contact-pill inline-flex items-center gap-2 rounded-full border border-white/30 bg-white/12 px-4 py-2 text-[13px] font-medium text-primary-50 transition-all hover:-translate-y-0.5 hover:border-white/55"
            >
              <svg class="h-4 w-4" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path :d="TELEGRAM_PATH" /></svg>
              Telegram
            </a>
            <a
              v-if="qqGroupUrl"
              :href="qqGroupUrl"
              target="_blank"
              rel="noopener noreferrer"
              class="contact-pill inline-flex items-center gap-2 rounded-full border border-white/30 bg-white/12 px-4 py-2 text-[13px] font-medium text-primary-50 transition-all hover:-translate-y-0.5 hover:border-white/55"
            >
              <svg class="h-4 w-4" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path :d="QQ_PATH" /></svg>
              QQ 群
            </a>
          </div>
        </div>
      </aside>

      <!-- Right: Form Side -->
      <main class="flex flex-1 items-center justify-center px-4 py-10 sm:px-8">
        <div class="auth-form-zone w-full max-w-sm">
          <slot />

          <!-- Footer Links -->
          <div class="mt-6 text-center text-sm">
            <slot name="footer" />
          </div>

          <!-- Mobile contact entries -->
          <div v-if="telegramUrl || qqGroupUrl" class="mt-6 flex justify-center gap-2.5 lg:hidden">
            <a
              v-if="telegramUrl"
              :href="telegramUrl"
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex items-center gap-2 rounded-full border border-gray-200 bg-white px-4 py-2 text-[13px] font-medium text-gray-700 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200"
            >
              <svg class="h-4 w-4 text-[#26A5E4]" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path :d="TELEGRAM_PATH" /></svg>
              Telegram
            </a>
            <a
              v-if="qqGroupUrl"
              :href="qqGroupUrl"
              target="_blank"
              rel="noopener noreferrer"
              class="inline-flex items-center gap-2 rounded-full border border-gray-200 bg-white px-4 py-2 text-[13px] font-medium text-gray-700 dark:border-dark-600 dark:bg-dark-800 dark:text-gray-200"
            >
              <svg class="h-4 w-4 text-[#1EBAFC]" viewBox="0 0 24 24" fill="currentColor" aria-hidden="true"><path :d="QQ_PATH" /></svg>
              QQ 群
            </a>
          </div>

          <!-- Copyright -->
          <p class="mt-8 text-center text-xs text-gray-400 dark:text-dark-500">
            &copy; {{ currentYear }} {{ siteName }}. {{ t('home.footer.allRightsReserved') }}
          </p>
        </div>
      </main>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, onUnmounted, ref } from 'vue'
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

// Pointer parallax on the brand panel decoration layers
const gridLayer = ref<HTMLElement | null>(null)
const glowLayer = ref<HTMLElement | null>(null)
let parallaxHandler: ((e: PointerEvent) => void) | null = null

function initParallax() {
  const reduce = window.matchMedia('(prefers-reduced-motion: reduce)').matches
  const finePointer = window.matchMedia('(pointer: fine)').matches
  if (reduce || !finePointer) return
  parallaxHandler = (e: PointerEvent) => {
    const nx = e.clientX / window.innerWidth - 0.5
    const ny = e.clientY / window.innerHeight - 0.5
    if (gridLayer.value) gridLayer.value.style.transform = `translate(${(nx * 14).toFixed(1)}px, ${(ny * 14).toFixed(1)}px)`
    if (glowLayer.value) glowLayer.value.style.transform = `translate(${(nx * 30).toFixed(1)}px, ${(ny * 30).toFixed(1)}px)`
  }
  window.addEventListener('pointermove', parallaxHandler, { passive: true })
}

onMounted(() => {
  appStore.fetchPublicSettings()
  initParallax()
})

onUnmounted(() => {
  if (parallaxHandler) {
    window.removeEventListener('pointermove', parallaxHandler)
    parallaxHandler = null
  }
})
</script>

<style scoped>
/* ============ 品牌面板 ============ */
.auth-brand {
  background: linear-gradient(160deg, #0f766e 0%, #115e59 45%, #164e63 100%);
  background-size: 170% 170%;
  animation: pan-grad 18s ease-in-out infinite alternate;
}

@keyframes pan-grad {
  from {
    background-position: 0% 0%;
  }
  to {
    background-position: 100% 100%;
  }
}

.auth-gridlayer {
  position: absolute;
  inset: -24px;
  background:
    linear-gradient(rgba(255, 255, 255, 0.05) 1px, transparent 1px),
    linear-gradient(90deg, rgba(255, 255, 255, 0.05) 1px, transparent 1px);
  background-size: 48px 48px;
  -webkit-mask-image: radial-gradient(ellipse 90% 70% at 30% 20%, #000, transparent);
  mask-image: radial-gradient(ellipse 90% 70% at 30% 20%, #000, transparent);
  pointer-events: none;
  will-change: transform;
  transition: transform 0.35s cubic-bezier(0.16, 1, 0.3, 1);
}

.auth-glow {
  position: absolute;
  right: -100px;
  bottom: -120px;
  width: 340px;
  height: 340px;
  border-radius: 50%;
  background: rgba(103, 232, 249, 0.18);
  filter: blur(70px);
  pointer-events: none;
  will-change: transform;
  transition: transform 0.35s cubic-bezier(0.16, 1, 0.3, 1);
}

/* ============ 入场编排 ============ */
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

.brand-row {
  animation: rise 0.6s 0.05s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.brand-desc {
  animation: rise 0.6s 0.15s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.brand-code {
  animation: rise 0.6s 0.25s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.code-wipe {
  display: inline-block;
  white-space: nowrap;
  clip-path: inset(0 100% 0 0);
  animation: wipe-in 1.4s ease-out 0.7s forwards;
}

@keyframes wipe-in {
  to {
    clip-path: inset(0 0 0 0);
  }
}

.bub-user {
  animation: rise 0.6s 1s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.bub-ai {
  animation: rise 0.6s 1.55s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.ai-wipe {
  display: inline-block;
  white-space: nowrap;
  clip-path: inset(0 100% 0 0);
  animation: wipe-in 1.3s ease-out 2s forwards;
}

.stream-caret {
  display: inline-block;
  width: 7px;
  height: 13px;
  background: #5eead4;
  animation: caret-blink 1s step-end infinite;
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

.cap-row .cap {
  opacity: 0;
  animation: pop 0.45s cubic-bezier(0.34, 1.56, 0.64, 1) both;
}

.cap-row .cap:nth-child(1) {
  animation-delay: 2.7s;
}
.cap-row .cap:nth-child(2) {
  animation-delay: 2.82s;
}
.cap-row .cap:nth-child(3) {
  animation-delay: 2.94s;
}
.cap-row .cap:nth-child(4) {
  animation-delay: 3.06s;
}

.brand-dots span {
  opacity: 0;
  animation: pop 0.5s cubic-bezier(0.34, 1.56, 0.64, 1) both;
}

.brand-dots span:nth-child(1) {
  animation-delay: 0.45s;
}
.brand-dots span:nth-child(2) {
  animation-delay: 0.57s;
}
.brand-dots span:nth-child(3) {
  animation-delay: 0.69s;
}
.brand-dots span:nth-child(4) {
  animation-delay: 0.81s;
}

.brand-dots span + span {
  margin-left: -7px;
}

.contact-row {
  animation: rise 0.6s 0.9s cubic-bezier(0.16, 1, 0.3, 1) both;
}

.auth-form-zone {
  animation: rise 0.6s 0.15s cubic-bezier(0.16, 1, 0.3, 1) both;
}

/* 表单侧输入与主按钮微调（仅认证页生效；按钮加深保证白字过 WCAG AA） */
.auth-form-zone :deep(.input) {
  min-height: 46px;
  border-radius: 14px;
}

.auth-form-zone :deep(.btn-primary) {
  min-height: 46px;
  border-radius: 999px;
  background-image: linear-gradient(90deg, #0f766e, #0e7490);
}

.auth-form-zone :deep(.btn-primary:hover) {
  filter: brightness(1.1);
}

/* ============ 降低动态 ============ */
@media (prefers-reduced-motion: reduce) {
  .auth-brand,
  .brand-row,
  .brand-desc,
  .brand-code,
  .code-wipe,
  .bub-user,
  .bub-ai,
  .ai-wipe,
  .stream-caret,
  .cap-row .cap,
  .brand-dots span,
  .contact-row,
  .auth-form-zone {
    animation: none;
    opacity: 1;
    clip-path: none;
  }

  .auth-gridlayer,
  .auth-glow {
    transition: none;
  }
}
</style>
