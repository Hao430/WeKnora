<!--
  CreateUserDialog: SystemAdmin "Create user" in-place popup.

  Anchors to the page action via the default slot, matching TenantMembers
  invite and UserProfile password popups. Form (username + email +
  auto-generate toggle) and, when the server mints a one-time password, a
  reveal view that must be acknowledged before the popup can close. Esc is
  captured on window so the parent Settings modal cannot unmount this and
  discard the password. The idempotent retry path closes with its own
  success toast instead.

  Visibility is owned by the parent via v-model:visible. Every user-visible
  message is also emitted via `announced` so the parent's sr-only live
  region can relay it. Shared popup chrome comes from systemAdminDialog.less.
-->
<template>
  <t-popup :visible="visible && active" trigger="click" placement="left-top" :destroy-on-close="!locked"
    overlay-class-name="wk-popover wk-popover--form system-admin-action-popup-overlay" @visible-change="onVisibleChange">
    <span class="system-admin-action-popup-anchor">
      <slot />
    </span>
    <template #content>
      <div class="system-admin-action-popup-inner" @click.stop>
        <div class="system-admin-action-popup-title">
          {{ createUserSuccess
            ? t('system.globalSettings.createUser.generated.successTitle')
            : t('system.globalSettings.createUser.dialogTitle') }}
        </div>
        <p class="system-admin-action-popup-hint">{{ hintText }}</p>
        <template v-if="createUserSuccess">
          <div class="create-user-reveal">
            <div class="create-user-reveal-item">
              <span class="create-user-reveal-label">
                {{ t('system.globalSettings.createUser.generated.usernameLabel') }}
              </span>
              <span class="create-user-reveal-value">{{ createUserSuccess.username }}</span>
            </div>
            <div class="create-user-reveal-item">
              <span class="create-user-reveal-label">
                {{ t('system.globalSettings.createUser.generated.emailLabel') }}
              </span>
              <span class="create-user-reveal-value">{{ createUserSuccess.email }}</span>
            </div>
            <div v-if="createUserSuccess.generatedPassword" class="create-user-reveal-item">
              <span class="create-user-reveal-label">
                {{ t('system.globalSettings.createUser.generated.passwordLabel') }}
              </span>
              <pre
                class="create-user-reveal-value create-user-reveal-value--mono">{{ createUserSuccess.generatedPassword }}</pre>
            </div>
          </div>
          <div class="system-admin-action-popup-footer">
            <t-button v-if="createUserSuccess.generatedPassword" theme="primary" variant="outline"
              @click="copyAccountDetails">
              {{ t('system.globalSettings.createUser.generated.copyBtn') }}
            </t-button>
            <t-button theme="primary" @click="acknowledge">
              {{ t('system.globalSettings.createUser.generated.acknowledgeBtn') }}
            </t-button>
          </div>
        </template>
        <template v-else>
          <t-form ref="formRef" :data="form" :rules="rules" label-align="top" class="system-admin-action-popup-form">
            <t-form-item :label="t('system.globalSettings.createUser.dingtalkLabel')" name="dingtalkMobile">
              <t-input v-model="form.dingtalkMobile" type="text" clearable autocomplete="off" :disabled="submitting"
                :placeholder="t('system.globalSettings.createUser.dingtalkPlaceholder')" />
            </t-form-item>
            <t-form-item v-if="usingDingtalk" :label="t('system.globalSettings.createUser.tenantLabel')"
              name="tenantId">
              <t-select v-model="form.tenantId" clearable :disabled="submitting"
                :popup-props="tenantSelectPopupProps"
                :placeholder="t('system.globalSettings.createUser.tenantPlaceholder')">
                <t-option v-for="tenant in adminTenantOptions" :key="tenant.id" :value="tenant.id"
                  :label="tenant.name" />
              </t-select>
            </t-form-item>
            <t-form-item v-if="usingDingtalk && form.tenantId" :label="t('tenantMember.add.roleLabel')"
              name="tenantRole">
              <t-select v-model="form.tenantRole" :disabled="submitting" :popup-props="tenantSelectPopupProps">
                <t-option value="contributor" :label="t('tenantMember.role.contributor')" />
                <t-option value="admin" :label="t('tenantMember.role.admin')" />
                <t-option value="viewer" :label="t('tenantMember.role.viewer')" />
              </t-select>
            </t-form-item>
            <template v-if="!usingDingtalk">
              <t-form-item :label="t('system.globalSettings.createUser.usernameLabel')" name="username">
                <t-input v-model="form.username" type="text" clearable autocomplete="off" :disabled="submitting"
                  :placeholder="t('system.globalSettings.createUser.usernamePlaceholder')" />
              </t-form-item>
              <t-form-item :label="t('system.globalSettings.createUser.emailLabel')" name="email">
                <t-input v-model="form.email" type="text" clearable autocomplete="off" :disabled="submitting"
                  :placeholder="t('system.globalSettings.createUser.emailPlaceholder')" />
              </t-form-item>
              <t-form-item name="autoGenerate">
                <t-checkbox v-model="form.autoGenerate" :disabled="submitting">
                  {{ t('system.globalSettings.createUser.autoGenerateLabel') }}
                </t-checkbox>
              </t-form-item>
              <template v-if="!form.autoGenerate">
                <t-form-item :label="t('system.globalSettings.createUser.newPasswordLabel')" name="newPassword">
                  <t-input v-model="form.newPassword" type="password" autocomplete="new-password" :disabled="submitting"
                    :placeholder="t('system.globalSettings.createUser.newPasswordPlaceholder')">
                    <template #prefix-icon><t-icon name="lock-on" /></template>
                  </t-input>
                </t-form-item>
                <t-form-item :label="t('system.globalSettings.createUser.confirmPasswordLabel')" name="confirmPassword">
                  <t-input v-model="form.confirmPassword" type="password" autocomplete="new-password"
                    :disabled="submitting" :placeholder="t('system.globalSettings.createUser.confirmPasswordPlaceholder')"
                    @enter="submit">
                    <template #prefix-icon><t-icon name="lock-on" /></template>
                  </t-input>
                </t-form-item>
              </template>
            </template>
          </t-form>
          <div class="system-admin-action-popup-footer">
            <t-button variant="outline" :disabled="submitting" @click="emit('update:visible', false)">
              {{ t('system.globalSettings.confirm.cancelBtn') }}
            </t-button>
            <t-button theme="primary" :loading="submitting" @click="submit">
              {{ t('system.globalSettings.createUser.confirmBtn') }}
            </t-button>
          </div>
        </template>
      </div>
    </template>
  </t-popup>
</template>

<script setup lang="ts">
import { computed, nextTick, onMounted, onUnmounted, reactive, ref, watch } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import type { FormInstanceFunctions, FormRule } from 'tdesign-vue-next'
import { useI18n } from 'vue-i18n'
import { createSystemUser, provisionDingTalkUser, type CreateSystemUserRequest } from '@/api/system'
import { getAuthConfig } from '@/api/auth'
import { useAuthStore } from '@/stores/auth'
import { copyWithToast } from '@/utils/clipboard'
import { newPasswordRules } from '@/utils/passwordPolicy'
import {
  applyCreateUserResponse,
  captureLockedEscape,
  formatCreateUserCredentials,
  resolveCreateUserView,
  shouldAcceptCreateUserSubmit,
  type CreateUserReveal,
} from './createUserDialogState'

const props = defineProps<{ visible: boolean; active: boolean }>()
const emit = defineEmits<{
  'update:visible': [boolean]
  announced: [string]
  created: []
}>()

const { t } = useI18n()

const submitting = ref(false)
const dismissing = ref(false)
const formRef = ref<FormInstanceFunctions>()
const form = reactive({
  dingtalkMobile: '',
  tenantId: undefined as number | undefined,
  tenantRole: 'contributor',
  username: '',
  email: '',
  autoGenerate: true,
  newPassword: '',
  confirmPassword: '',
})
/** 可分配的空间 = 自己拥有 admin/owner 角色的空间；后端会做同样校验 */
const adminTenantOptions = computed(() =>
  useAuthStore()
    .memberships.filter((m) => m.role === 'admin' || m.role === 'owner')
    .map((m) => ({ id: m.tenant_id, name: m.tenant_name || String(m.tenant_id) })),
)
/** 下拉层须高于本弹窗遮罩（3050），参照 TenantMembers 的 roleSelectPopupProps */
const tenantSelectPopupProps = {
  zIndex: 6200,
  overlayClassName: 'create-user-tenant-select-popup',
}
const usingDingtalk = computed(() => form.dingtalkMobile.trim().length > 0)
const hintText = computed(() => {
  if (createUserSuccess.value && !createUserSuccess.value.generatedPassword) {
    return t('system.globalSettings.createUser.dingtalkSuccessHint')
  }
  return createUserSuccess.value
    ? t('system.globalSettings.createUser.generated.successBody')
    : t('system.globalSettings.createUser.warning')
})
const createUserSuccess = ref<CreateUserReveal | null>(null)
const locked = computed(() => submitting.value || createUserSuccess.value !== null)
const complexPasswordEnabled = ref(false)

const loadAuthConfig = async () => {
  const resp = await getAuthConfig()
  complexPasswordEnabled.value = !!resp.complex_password_enabled
  if (!resp.success) {
    MessagePlugin.warning(t('system.globalSettings.messages.loadFailed'))
  }
}

const rules = computed<Record<string, FormRule[]>>(() => ({
  dingtalkMobile: [
    {
      validator: (value: string) => !value || /^[+\d][\d\s-]{5,19}$/.test(value.trim()),
      message: t('system.globalSettings.createUser.validation.dingtalkMobileInvalid'),
      trigger: 'blur',
    },
  ],
  ...(!usingDingtalk.value
    ? {
        username: [
          { required: true, message: t('system.globalSettings.createUser.validation.usernameRequired'), trigger: 'blur' },
          { min: 2, max: 50, message: t('system.globalSettings.createUser.validation.usernameLength'), trigger: 'blur' },
        ],
        email: [
          { required: true, message: t('system.globalSettings.createUser.validation.emailRequired'), trigger: 'blur' },
          { email: true, message: t('system.globalSettings.createUser.validation.emailInvalid'), trigger: 'blur' },
        ],
        newPassword: newPasswordRules(t, complexPasswordEnabled.value),
        confirmPassword: [
          { required: true, message: t('system.globalSettings.createUser.validation.confirmRequired'), trigger: 'blur' },
          {
            validator: (value: string) => value === form.newPassword,
            message: t('system.globalSettings.createUser.validation.passwordMismatch'),
            trigger: 'blur',
          },
        ],
      }
    : {}),
}))

watch(() => props.visible, async (visible) => {
  if (!visible) {
    createUserSuccess.value = null
    dismissing.value = false
    resetFormFields()
    return
  }
  dismissing.value = false
  createUserSuccess.value = null
  resetFormFields()
  await loadAuthConfig()
  await nextTick()
  formRef.value?.clearValidate?.()
})

function onVisibleChange(visible: boolean) {
  if (!visible && locked.value && !dismissing.value) return
  emit('update:visible', visible)
}

function onWindowEscCapture(e: KeyboardEvent) {
  captureLockedEscape(props.visible, locked.value && !dismissing.value, e)
}

onMounted(() => {
  window.addEventListener('keydown', onWindowEscCapture, true)
})

onUnmounted(() => {
  window.removeEventListener('keydown', onWindowEscCapture, true)
})

function resetFormFields() {
  form.dingtalkMobile = ''
  form.tenantId = undefined
  form.tenantRole = 'contributor'
  form.username = ''
  form.email = ''
  form.autoGenerate = true
  form.newPassword = ''
  form.confirmPassword = ''
  formRef.value?.clearValidate?.()
}

function announce(msg: string, kind: 'success' | 'error' = 'success') {
  emit('announced', msg)
  if (kind === 'error') {
    MessagePlugin.error(msg)
  } else {
    MessagePlugin.success(msg)
  }
}

async function submit() {
  if (!shouldAcceptCreateUserSubmit(submitting.value, createUserSuccess.value !== null)) return
  submitting.value = true
  try {
    const valid = await formRef.value?.validate?.()
    if (valid !== true) return

    if (usingDingtalk.value) {
      const resp = await provisionDingTalkUser({
        mobile: form.dingtalkMobile.trim(),
        ...(form.tenantId
          ? { tenant_id: form.tenantId, role: form.tenantRole }
          : {}),
      })
      emit('created')
      createUserSuccess.value = {
        username: resp.user.username,
        email: resp.user.email,
        generatedPassword: '',
      }
      if (resp.idempotent) {
        emit('announced', t('system.globalSettings.createUser.successIdempotent'))
      } else {
        emit('announced', t('system.globalSettings.createUser.dingtalkSuccess'))
      }
      return
    }

    const payload: CreateSystemUserRequest = {
      username: form.username.trim(),
      email: form.email.trim(),
    }
    if (!form.autoGenerate) {
      payload.password = form.newPassword
    }
    const response = await createSystemUser(payload)
    emit('created')
    const view = resolveCreateUserView(
      response,
      { username: payload.username, email: payload.email },
      form.autoGenerate,
    )
    const next = applyCreateUserResponse(createUserSuccess.value, view)
    createUserSuccess.value = next.success
    if (next.notice === 'reveal') {
      emit('announced', t('system.globalSettings.createUser.success'))
    } else if (next.notice === 'created') {
      announce(t('system.globalSettings.createUser.success'))
    } else if (next.notice === 'idempotent') {
      announce(t('system.globalSettings.createUser.successIdempotent'))
    } else if (next.notice === 'missingPassword') {
      announce(t('system.globalSettings.createUser.missingPassword'), 'error')
    }
    if (next.close) {
      dismissing.value = true
      emit('update:visible', false)
    }
  } catch (err: any) {
    // 2400 = 钉钉通讯录未找到该手机号（后端 ErrDingTalkMobileNotFound）
    const msg = err?.error?.code === 2400
      ? t('system.globalSettings.createUser.dingtalkMobileNotFound')
      : err?.message || t('system.globalSettings.createUser.failed')
    announce(msg, 'error')
  } finally {
    submitting.value = false
  }
}

async function copyAccountDetails() {
  if (!createUserSuccess.value) return
  await copyWithToast(
    formatCreateUserCredentials(createUserSuccess.value, {
      username: t('system.globalSettings.createUser.generated.usernameLabel'),
      email: t('system.globalSettings.createUser.generated.emailLabel'),
      password: t('system.globalSettings.createUser.generated.passwordLabel'),
    }),
    'system.globalSettings.createUser.generated.copySuccess',
  )
}

function acknowledge() {
  if (!createUserSuccess.value) return
  dismissing.value = true
  MessagePlugin.success(t('system.globalSettings.createUser.success'))
  emit('update:visible', false)
}
</script>

<style lang="less">
@import './systemAdminDialog.less';

.create-user-reveal {
  display: flex;
  flex-direction: column;
  gap: 0;
  padding: 2px 0 0;
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-md);
  background: var(--td-bg-color-page);
  overflow: hidden;
}

.create-user-reveal-item {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: 10px 12px;
  border-bottom: 1px solid var(--td-component-stroke);

  &:last-child {
    border-bottom: none;
  }
}

.create-user-reveal-label {
  font-size: var(--app-text-sm);
  line-height: 16px;
  color: var(--td-text-color-secondary);
}

.create-user-reveal-value {
  font-size: var(--app-text-base);
  line-height: 20px;
  color: var(--td-text-color-primary);
  word-break: break-all;
}

.create-user-reveal-value--mono {
  margin: 0;
  font-family: var(--td-font-family-mono);
  font-size: var(--app-text-sm);
  line-height: 18px;
  white-space: pre-wrap;
  user-select: all;
}
</style>
