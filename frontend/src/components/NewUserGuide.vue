<template>
  <SpotlightGuide v-model:active="active" :steps="steps" step-i18n-prefix="newUserGuide.steps"
    @finish="onFinish" />
</template>

<script setup lang="ts">
import { computed, onBeforeUnmount, onMounted, ref } from 'vue'
import { useRouter } from 'vue-router'
import SpotlightGuide from '@/components/SpotlightGuide.vue'
import {
  GLOBAL_USER_GUIDE_KEY,
  OPEN_NEW_USER_GUIDE_EVENT,
  isGlobalUserGuideDone,
  markContextualGuideDone,
} from '@/config/contextualGuides'
import type { SpotlightGuideStep } from '@/types/spotlightGuide'

const router = useRouter()
const active = ref(false)
let openTimer: ReturnType<typeof setTimeout> | null = null

const steps = computed<SpotlightGuideStep[]>(() => [
  {
    key: 'sidebar',
    target: '[data-guide="sidebar-toggle"]',
    placement: 'right',
  },
  {
    key: 'kbMention',
    target: '[data-guide="chat-kb-mention"]',
    placement: 'top',
    before: async () => { await router.push('/platform/creatChat') },
  },
  {
    key: 'attachment',
    target: '[data-guide="chat-attachment"]',
    placement: 'top',
  },
])

const onFinish = () => {
  localStorage.setItem(GLOBAL_USER_GUIDE_KEY, '1')
  // 引导已在对话页覆盖 @ 选择知识库等要点，避免情境引导紧接着再次弹出
  markContextualGuideDone('chat')
}

const handleOpenEvent = () => {
  if (active.value) return
  if (openTimer) clearTimeout(openTimer)
  active.value = true
}

const scheduleAutoGuide = () => {
  if (openTimer) clearTimeout(openTimer)
  openTimer = null
  if (active.value || isGlobalUserGuideDone()) return

  openTimer = setTimeout(() => {
    openTimer = null
    if (active.value || isGlobalUserGuideDone()) return
    active.value = true
  }, 700)
}

onMounted(() => {
  window.addEventListener(OPEN_NEW_USER_GUIDE_EVENT, handleOpenEvent)
  scheduleAutoGuide()
})

onBeforeUnmount(() => {
  if (openTimer) clearTimeout(openTimer)
  window.removeEventListener(OPEN_NEW_USER_GUIDE_EVENT, handleOpenEvent)
})
</script>
