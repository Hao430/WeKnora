<template>
  <!-- "Join a space" dialog: the apply-to-join applicant view.
       Lists PUBLIC tenants (paginated, keyword filter) with an
       apply action, and shows the caller's own requests with
       status + withdraw. Mirrors MyInvitationsDialog's dialog
       pattern — transient inbox, modal keeps the user in context.
       Trigger lives in UserMenu. -->
  <t-dialog v-model:visible="visibleModel" :header="$t('joinSpace.title')" :footer="false" width="560px"
    :close-on-overlay-click="true">
    <!-- Search + tenant directory -->
    <div class="join-space-search">
      <t-input v-model="keyword" clearable :placeholder="$t('joinSpace.searchPlaceholder')" @enter="reloadTenants">
        <template #suffix-icon>
          <search-icon />
        </template>
      </t-input>
    </div>

    <div v-if="tenantsLoading" class="loading-inline">
      <t-loading size="small" />
      <span>{{ $t('tenantMember.loading') }}</span>
    </div>
    <div v-else-if="tenantsError" class="error-inline">
      <t-alert theme="error" :message="tenantsError">
        <template #operation>
          <t-button size="small" @click="reloadTenants">{{ $t('tenantMember.retry') }}</t-button>
        </template>
      </t-alert>
    </div>
    <div v-else-if="tenants.length === 0" class="empty-state">
      <t-empty :description="$t('joinSpace.empty')" />
    </div>
    <ul v-else class="tenant-list">
      <li v-for="tItem in tenants" :key="tItem.id" class="tenant-card">
        <div class="tenant-card-main">
          <div class="tenant-card-header">
            <span class="tenant-name">{{ tItem.name }}</span>
            <span class="tenant-members">{{ $t('joinSpace.memberCount', { count: tItem.member_count }) }}</span>
          </div>
          <p class="tenant-desc">{{ tItem.description || $t('joinSpace.noDescription') }}</p>
        </div>
        <div class="tenant-card-actions">
          <t-button v-if="!hasPendingFor(tItem.id)" theme="primary" variant="outline" size="small"
            :loading="applying === tItem.id" @click="onApply(tItem)">
            {{ $t('joinSpace.applyButton') }}
          </t-button>
          <t-tag v-else theme="warning" size="small">{{ $t('joinSpace.pendingTag') }}</t-tag>
        </div>
      </li>
    </ul>
    <div v-if="tenantsTotal > tenants.length" class="load-more">
      <t-button variant="text" size="small" :disabled="tenantsLoading" @click="loadMoreTenants">
        {{ $t('common.loadMore') }}
      </t-button>
    </div>

    <t-divider align="left">{{ $t('joinSpace.myRequestsTitle') }}</t-divider>

    <div v-if="mineLoading" class="loading-inline">
      <t-loading size="small" />
    </div>
    <div v-else-if="mine.length === 0" class="empty-state">
      <t-empty :description="$t('joinSpace.myRequestsEmpty')" />
    </div>
    <ul v-else class="request-list">
      <li v-for="row in mine" :key="row.id" class="request-card">
        <div class="request-card-main">
          <span class="tenant-name">{{ row.tenant_name || '#' + row.tenant_id }}</span>
          <span v-if="row.message" class="request-message">{{ row.message }}</span>
        </div>
        <div class="request-card-actions">
          <t-tag :theme="statusTagTheme(row.status)" size="small">{{ statusLabel(row.status) }}</t-tag>
          <t-button v-if="row.status === 'pending'" variant="outline" theme="default" size="small"
            :loading="withdrawing === row.id" @click="onWithdraw(row)">
            {{ $t('joinSpace.withdrawButton') }}
          </t-button>
        </div>
      </li>
    </ul>
  </t-dialog>

  <!-- Apply dialog: replaces the old window.prompt, which dropped the native
       browser modal into the middle of a TDesign page. The reason is
       optional; the textarea is the only field. -->
  <t-dialog v-model:visible="applyVisible" width="440px" top="30vh" :header="applyHeader" :on-confirm="submitApply"
    :confirm-btn="{ content: $t('joinSpace.applyDialog.submit'), loading: applying !== null }"
    :cancel-btn="{ content: $t('common.cancel') }" :close-on-overlay-click="applying === null"
    :close-on-esc-keydown="applying === null">
    <p class="apply-dialog-tip">{{ $t('joinSpace.applyDialog.tip') }}</p>
    <t-textarea v-model="applyMessage" :placeholder="$t('joinSpace.applyDialog.placeholder')" :maxlength="500"
      :autosize="{ minRows: 3, maxRows: 5 }" class="apply-dialog-input" />
  </t-dialog>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { MessagePlugin } from 'tdesign-vue-next'
import { SearchIcon } from 'tdesign-icons-vue-next'
import { useAuthStore } from '@/stores/auth'
import {
  listPublicTenants,
  createJoinRequest,
  listMyJoinRequests,
  withdrawJoinRequest,
  type PublicTenant,
  type TenantJoinRequest,
  type TenantJoinRequestStatus,
} from '@/api/tenant/joinRequests'

// v-model:visible — parent owns open/close so the entry point stays
// the single source of truth. Reload both views on every open.
const props = defineProps<{ visible: boolean }>()
const emit = defineEmits<{ (e: 'update:visible', v: boolean): void }>()
const visibleModel = computed({
  get: () => props.visible,
  set: (v) => emit('update:visible', v),
})

const { t } = useI18n()
const authStore = useAuthStore()

const PAGE_SIZE = 10
const keyword = ref('')
const tenants = ref<PublicTenant[]>([])
const tenantsTotal = ref(0)
const page = ref(1)
const tenantsLoading = ref(false)
const tenantsError = ref('')
const mine = ref<TenantJoinRequest[]>([])
const mineLoading = ref(false)
const applying = ref<number | null>(null)
const withdrawing = ref<number | null>(null)
const applyVisible = ref(false)
const applyTarget = ref<PublicTenant | null>(null)
const applyMessage = ref('')

const applyHeader = computed(() =>
  applyTarget.value ? t('joinSpace.applyDialog.title', { name: applyTarget.value.name }) : '',
)

function hasPendingFor(tenantId: number): boolean {
  return mine.value.some((r) => r.tenant_id === tenantId && r.status === 'pending')
}

function statusTagTheme(status: TenantJoinRequestStatus): 'warning' | 'success' | 'danger' | 'default' {
  switch (status) {
    case 'pending':
      return 'warning'
    case 'accepted':
      return 'success'
    case 'declined':
      return 'danger'
    default:
      return 'default'
  }
}

function statusLabel(status: TenantJoinRequestStatus): string {
  return t('joinSpace.status.' + status)
}

async function reloadTenants() {
  tenantsLoading.value = true
  tenantsError.value = ''
  page.value = 1
  try {
    const resp = await listPublicTenants({ keyword: keyword.value.trim(), page: 1, page_size: PAGE_SIZE })
    if (resp.success && resp.data) {
      tenants.value = resp.data.tenants
      tenantsTotal.value = resp.data.total
    } else {
      tenantsError.value = resp.message || t('joinSpace.errors.generic')
    }
  } catch (err: any) {
    tenantsError.value = err?.message || t('joinSpace.errors.generic')
  } finally {
    tenantsLoading.value = false
  }
}

async function loadMoreTenants() {
  const next = page.value + 1
  tenantsLoading.value = true
  try {
    const resp = await listPublicTenants({ keyword: keyword.value.trim(), page: next, page_size: PAGE_SIZE })
    if (resp.success && resp.data) {
      tenants.value = tenants.value.concat(resp.data.tenants)
      tenantsTotal.value = resp.data.total
      page.value = next
    }
  } catch (err: any) {
    MessagePlugin.error(err?.message || t('joinSpace.errors.generic'))
  } finally {
    tenantsLoading.value = false
  }
}

async function reloadMine() {
  mineLoading.value = true
  try {
    const resp = await listMyJoinRequests()
    if (resp.success && resp.data) {
      mine.value = resp.data.join_requests
    }
  } catch {
    // best-effort; the directory above still works
  } finally {
    mineLoading.value = false
  }
}

function onApply(tItem: PublicTenant) {
  applyTarget.value = tItem
  applyMessage.value = ''
  applyVisible.value = true
}

async function submitApply() {
  const target = applyTarget.value
  if (!target) return
  applying.value = target.id
  try {
    const resp = await createJoinRequest(target.id, applyMessage.value.trim())
    if (resp.success) {
      MessagePlugin.success(t('joinSpace.applySuccess', { name: target.name }))
      applyVisible.value = false
      await reloadMine()
    } else {
      MessagePlugin.error(resp.message || t('joinSpace.errors.generic'))
    }
  } catch (err: any) {
    const status = err?.status
    if (status === 404) MessagePlugin.error(t('joinSpace.errors.notPublic'))
    else if (status === 409) MessagePlugin.error(err?.message || t('joinSpace.errors.conflict'))
    else MessagePlugin.error(err?.message || t('joinSpace.errors.generic'))
  } finally {
    applying.value = null
  }
}

async function onWithdraw(row: TenantJoinRequest) {
  withdrawing.value = row.id
  try {
    const resp = await withdrawJoinRequest(row.id)
    if (resp.success) {
      await reloadMine()
      MessagePlugin.success(t('joinSpace.withdrawSuccess'))
    } else {
      MessagePlugin.error(resp.message || t('joinSpace.errors.generic'))
    }
  } catch (err: any) {
    const status = err?.status
    if (status === 403) MessagePlugin.error(t('joinSpace.errors.forbidden'))
    else if (status === 409) MessagePlugin.error(err?.message || t('joinSpace.errors.notPending'))
    else MessagePlugin.error(err?.message || t('joinSpace.errors.generic'))
  } finally {
    withdrawing.value = null
  }
}

// Refetch on every open — same rationale as MyInvitationsDialog:
// the user clicked here to see the latest state.
watch(
  () => props.visible,
  (v) => {
    if (v) {
      reloadTenants()
      reloadMine()
    }
  },
)
</script>

<style lang="less" scoped>
.join-space-search {
  margin-bottom: 12px;
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

.tenant-list,
.request-list {
  list-style: none;
  margin: 0;
  padding: 0;
  display: flex;
  flex-direction: column;
  gap: 10px;
  max-height: 42vh;
  overflow-y: auto;
}

.tenant-card,
.request-card {
  display: flex;
  align-items: center;
  gap: 12px;
  padding: 12px 14px;
  background: var(--td-bg-color-container);
  border: 1px solid var(--td-component-stroke);
  border-radius: var(--app-radius-md);
}

.tenant-card-main,
.request-card-main {
  flex: 1 1 auto;
  min-width: 0;
  display: flex;
  flex-direction: column;
  gap: 4px;
}

.tenant-card-header {
  display: flex;
  align-items: baseline;
  gap: 8px;

  .tenant-name {
    font-size: var(--app-text-base);
    font-weight: 600;
    color: var(--td-text-color-primary);
  }

  .tenant-members {
    font-size: var(--app-text-sm);
    color: var(--td-text-color-secondary);
  }
}

.tenant-desc {
  margin: 0;
  font-size: var(--app-text-sm);
  color: var(--td-text-color-secondary);
  word-break: break-word;
}

.request-message {
  font-size: var(--app-text-sm);
  color: var(--td-text-color-secondary);
  word-break: break-word;
}

.tenant-card-actions,
.request-card-actions {
  display: flex;
  align-items: center;
  gap: 8px;
  flex-shrink: 0;
}

.load-more {
  display: flex;
  justify-content: center;
  padding-top: 8px;
}

.apply-dialog-tip {
  margin: 0 0 12px 0;
  color: var(--td-text-color-secondary);
  font-size: var(--app-text-sm);
  line-height: 1.55;
}

.apply-dialog-input {
  width: 100%;
}
</style>