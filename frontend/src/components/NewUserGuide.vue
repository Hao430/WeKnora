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
  markContextualGuideDone,
} from '@/config/contextualGuides'
import type { SpotlightGuideStep } from '@/types/spotlightGuide'

const router = useRouter()
const active = ref(false)

const steps = computed<SpotlightGuideStep[]>(() => [
  // 单步三洞同屏：侧边栏切换 + @ 知识库 + 上传附件，每洞旁独立文案
  {
    key: 'overview',
    targets: [
      { selector: '[data-guide="sidebar-toggle"]', itemKey: 'sidebar', placement: 'right' },
      { selector: '[data-guide="chat-kb-mention"]', itemKey: 'kb', placement: 'top' },
      { selector: '[data-guide="chat-attachment"]', itemKey: 'attach', placement: 'bottom' },
    ],
    before: async () => { await router.push('/platform/creatChat') },
  },
])

const onFinish = () => {
  localStorage.setItem(GLOBAL_USER_GUIDE_KEY, '1')
  // 引导已在对话页覆盖 @ 选择知识库等要点，避免情境引导紧接着再次弹出
  markContextualGuideDone('chat')
}

const handleOpenEvent = () => {
  if (active.value) return
  active.value = true
}

onMounted(() => {
  window.addEventListener(OPEN_NEW_USER_GUIDE_EVENT, handleOpenEvent)
})

onBeforeUnmount(() => {
  window.removeEventListener(OPEN_NEW_USER_GUIDE_EVENT, handleOpenEvent)
})
</script>
