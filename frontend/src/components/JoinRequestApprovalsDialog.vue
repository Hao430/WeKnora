<template>
  <!-- Approver inbox: pending join requests across every tenant the
       caller actively Owner/admins. Mirrors MyInvitationsDialog —
       modal, act in place, dismiss. The trigger lives in
       GlobalInvitationBell's pending section. -->
  <t-dialog v-model:visible="visibleModel" :header="$t('joinSpace.approvals.title')" :footer="false" width="560px"
    :close-on-overlay-click="true">
    <p class="approvals-desc">{{ $t('joinSpace.approvals.description') }}</p>

    <div v-if="loading" class="loading-inline">
      <t-loading size="small" />
      <span>{{ $t('tenantMember.loading') }}</span>
    </div>

    <div v-else-if="error" class="error-inline">
      <t-alert theme="error" :message="error">
        <template #operation>
          <t-button size="small" @click="reload">{{ $t('tenantMember.retry') }}</t-button>
        </template>
      </t-alert>
    </div>

    <div v-else-if="requests.length === 0" class="empty-state">
      <t-empty :description="$t('joinSpace.approvals.empty')" />
    </div>

    <ul v-else class="request-list">
      <li v-for="row in requests" :key="row.id" class="request-card">
        <div class="request-card-main">
          <div class="request-card-header">
            <span class="tenant-name">{{ row.tenant_name || '#' + row.tenant_id }}</span>
          </div>
          <div class="request-card-meta">
            <span class="meta-row">
              <t-icon name="user" size="14px" class="meta-icon" />
              <span class="meta-value">{{ requesterDisplay(row) }}</span>
            </span>
            <span v-if="row.message" class="meta-row meta-row--message">
              <t-icon name="chat" size="14px" class="meta-icon" />
              <span class="meta-value">{{ row.message }}</span>
            </span>
          </div>
        </div>
        <div class="request-card-actions">
          <t-button theme="primary" size="small" :loading="acting === row.id" @click="onAccept(row)">
            {{ $t('joinSpace.approvals.acceptButton') }}
          </t-button>
          <t-button theme="default" variant="outline" size="small" :loading="acting === row.id" @click="onDecline(row)">
            {{ $t('joinSpace.approvals.declineButton') }}
          </t-button>
        </div>
      </li>
    </ul>
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import { useAuthStore } from '@/stores/auth'
import {
  listApproverInbox,
  acceptJoinRequest,
  declineJoinRequest,
  type TenantJoinRequest,
} from '@/api/tenant/joinRequests'

const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{ (e: 'update:visible', v: boolean): void }>()
const visibleModel = computed({
  get: () => props.visible,
  set: (v) => emit('update:visible', v),
})

const { t } = useI18n()
const authStore = useAuthStore()

const requests = ref<TenantJoinRequest[]>([])
const loading = ref(false)
const error = ref('')
const acting = ref<number | null>(null)

function requesterDisplay(row: TenantJoinRequest): string {
  return row.requester_name?.trim() || row.requester_email?.trim() || row.requester_user_id
}

async function reload() {
  loading.value = true
  error.value = ''
  try {
    const resp = await listApproverInbox()
    if (resp.success && resp.data) {
      requests.value = resp.data.join_requests
      authStore.setPendingJoinRequestCount(requests.value.length)
    } else {
      error.value = resp.message || t('joinSpace.errors.generic')
    }
  } catch (err: any) {
    error.value = err?.message || t('joinSpace.errors.generic')
  } finally {
    loading.value = false
  }
}

function bumpPendingCount(delta: number) {
  authStore.setPendingJoinRequestCount(Math.max(0, authStore.pendingJoinRequestCount + delta))
}

async function onAccept(row: TenantJoinRequest) {
  acting.value = row.id
  try {
    const resp = await acceptJoinRequest(row.tenant_id, row.id)
    if (resp.success) {
      requests.value = requests.value.filter((x) => x.id !== row.id)
      bumpPendingCount(-1)
      MessagePlugin.success(
        t('joinSpace.approvals.acceptSuccess', { name: row.requester_name || row.requester_email || row.requester_user_id }),
      )
    } else {
      MessagePlugin.error(resp.message || t('joinSpace.errors.generic'))
    }
  } catch (err: any) {
    const status = err?.status
    if (status === 404) MessagePlugin.error(t('joinSpace.errors.notFound'))
    else if (status === 403) MessagePlugin.error(t('joinSpace.errors.forbidden'))
    else if (status === 409) MessagePlugin.error(err?.message || t('joinSpace.errors.notPending'))
    else MessagePlugin.error(err?.message || t('joinSpace.errors.generic'))
  } finally {
    acting.value = null
  }
}

async function onDecline(row: TenantJoinRequest) {
  acting.value = row.id
  try {
    const resp = await declineJoinRequest(row.tenant_id, row.id)
    if (resp.success) {
      requests.value = requests.value.filter((x) => x.id !== row.id)
      bumpPendingCount(-1)
      MessagePlugin.success(t('joinSpace.approvals.declineSuccess'))
    } else {
      MessagePlugin.error(resp.message || t('joinSpace.errors.generic'))
    }
  } catch (err: any) {
    const status = err?.status
    if (status === 404) MessagePlugin.error(t('joinSpace.errors.notFound'))
    else if (status === 403) MessagePlugin.error(t('joinSpace.errors.forbidden'))
    else if (status === 409) MessagePlugin.error(err?.message || t('joinSpace.errors.notPending'))
    else MessagePlugin.error(err?.message || t('joinSpace.errors.generic'))
  } finally {
    acting.value = null
  }
}

watch(
  () => props.visible,
  (v) => {
    if (v) reload()
  },
)
</script>

<style lang="less" scoped>
.approvals-desc {
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-md);
  line-height: 1.55;
  margin: 0 0 16px 0;
}

.loading-inline,
.error-inline {
  display: flex;
  align-items: center;
  gap: 8px;
  padding: 8px 0;
}

.empty-state {
  padding: 16px 0 8px;
  display: flex;
  justify-content: center;
}

.request-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: 60vh;
  overflow-y: auto;
}

.request-card {
  display: flex;
  align-items: stretch;
  gap: 12px;
  padding: 12px 14px;
  background: var(--td-bg-color-container);
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-md);

  .request-card-main {
    flex: 1 1 auto;
    min-width: 0;
    display: flex;
    flex-direction: column;
    gap: 6px;
  }

  .request-card-header {
    display: flex;
    align-items: center;
    gap: 8px;

    .tenant-name {
      font-size: var(--app-text-base);
      font-weight: 600;
      color: var(--td-text-color-primary);
    }
  }

  .request-card-meta {
    display: flex;
    flex-direction: column;
    gap: 3px;
    color: var(--td-text-color-secondary);
    font-size: var(--app-text-sm);

    .meta-row {
      display: inline-flex;
      align-items: center;
      gap: 4px;

      .meta-icon {
        color: var(--td-text-color-placeholder);
        flex-shrink: 0;
      }

      .meta-value {
        color: var(--td-text-color-primary);
        word-break: break-word;
      }
    }
  }

  .request-card-actions {
    display: flex;
    flex-direction: column;
    align-items: stretch;
    justify-content: center;
    gap: 6px;
    flex-shrink: 0;
  }
}
</style>