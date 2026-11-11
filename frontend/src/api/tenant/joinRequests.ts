import { get, post, del } from '@/utils/request'

// TenantJoinRequestStatus mirrors internal/types/tenant_join_request.go's
// five-state machine. pending is the only non-terminal state; the rest
// are recorded for the audit trail. Withdrawn is the requester's own
// cancel action — the reverse-flow analogue of invitation revoked.
export type TenantJoinRequestStatus =
  | 'pending'
  | 'accepted'
  | 'declined'
  | 'withdrawn'
  | 'expired'

// TenantJoinRequest is the API projection of a tenant_join_requests
// row, hydrated with requester / handler names and the tenant name
// when the backend has them.
export interface TenantJoinRequest {
  id: number
  tenant_id: number
  tenant_name?: string
  requester_user_id: string
  requester_name?: string
  requester_email?: string
  message: string
  status: TenantJoinRequestStatus
  handled_by?: string | null
  handler_name?: string
  handled_at?: string | null
  expires_at: string
  created_at: string
}

// PublicTenant is the slim directory entry served by the public
// tenant list endpoint — discoverability metadata only.
export interface PublicTenant {
  id: number
  name: string
  description: string
  member_count: number
  // joined=true means the caller already belongs to this space; the
  // directory then offers "enter" instead of "apply".
  joined?: boolean
  created_at: string
}

export interface ListPublicTenantsResponse {
  success: boolean
  data?: { tenants: PublicTenant[]; total: number; page?: number; page_size?: number }
  message?: string
}

export interface ListJoinRequestsResponse {
  success: boolean
  data?: { join_requests: TenantJoinRequest[] }
  message?: string
}

export interface PendingJoinCountResponse {
  success: boolean
  data?: { pending_count: number }
  message?: string
}

export interface SimpleResponse {
  success: boolean
  message?: string
}

export interface AcceptJoinResponse {
  success: boolean
  data?: { member: { tenant_id: number; user_id: string; role: string; status: string } }
  message?: string
}

/**
 * Browse/search PUBLIC tenants for the apply-to-join directory.
 * Keyword filters by name/description; paging is server-side.
 * Backend: GET /api/v1/tenants/public (authenticated).
 */
export async function listPublicTenants(
  options: { keyword?: string; page?: number; page_size?: number } = {},
): Promise<ListPublicTenantsResponse> {
  const u = new URLSearchParams()
  if (options.keyword) u.set('keyword', options.keyword)
  if (options.page != null && options.page > 0) u.set('page', String(options.page))
  if (options.page_size != null && options.page_size > 0) u.set('page_size', String(options.page_size))
  const qs = u.toString()
  return (await get(
    `/api/v1/tenants/public${qs ? `?${qs}` : ''}`,
  )) as unknown as ListPublicTenantsResponse
}

/**
 * Submit a join request against a public tenant. 409 when a pending
 * request/invitation already covers the pair or the caller is
 * already a member; 404 when the tenant is missing or private.
 * Backend: POST /api/v1/tenant-join-requests (authenticated).
 */
export async function createJoinRequest(
  tenantId: number,
  message: string,
): Promise<{ success: boolean; data?: TenantJoinRequest; message?: string }> {
  return (await post('/api/v1/tenant-join-requests', {
    tenant_id: tenantId,
    message,
  })) as unknown as { success: boolean; data?: TenantJoinRequest; message?: string }
}

/**
 * List MY join requests. Defaults to pending only; pass
 * includeTerminal=true for the history view.
 * Backend: GET /api/v1/tenant-join-requests/mine (authenticated).
 */
export async function listMyJoinRequests(
  options: { includeTerminal?: boolean } = {},
): Promise<ListJoinRequestsResponse> {
  const qs = options.includeTerminal ? '?include_terminal=true' : ''
  return (await get(
    `/api/v1/tenant-join-requests/mine${qs}`,
  )) as unknown as ListJoinRequestsResponse
}

/**
 * Withdraw MY pending join request (pending -> withdrawn). Can
 * re-apply afterwards.
 * Backend: DELETE /api/v1/tenant-join-requests/:id (authenticated).
 */
export async function withdrawJoinRequest(id: number): Promise<SimpleResponse> {
  return (await del(
    `/api/v1/tenant-join-requests/${id}`,
  )) as unknown as SimpleResponse
}

/**
 * Approver inbox: pending join requests across every tenant I
 * actively Owner/admin.
 * Backend: GET /api/v1/tenant-join-requests/inbox (authenticated).
 */
export async function listApproverInbox(): Promise<ListJoinRequestsResponse> {
  return (await get(
    '/api/v1/tenant-join-requests/inbox',
  )) as unknown as ListJoinRequestsResponse
}

/**
 * Lightweight pending-approval count for the bell badge poller.
 * Backend: GET /api/v1/tenant-join-requests/pending-count.
 */
export async function getPendingJoinRequestCount(): Promise<PendingJoinCountResponse> {
  return (await get(
    '/api/v1/tenant-join-requests/pending-count',
  )) as unknown as PendingJoinCountResponse
}

/**
 * Tenant-scoped join request history (management view).
 * Backend: GET /api/v1/tenants/:id/join-requests (Viewer+).
 */
export async function listTenantJoinRequests(
  tenantId: number,
  options: { includeTerminal?: boolean } = {},
): Promise<ListJoinRequestsResponse> {
  const qs = options.includeTerminal ? '?include_terminal=true' : ''
  return (await get(
    `/api/v1/tenants/${tenantId}/join-requests${qs}`,
  )) as unknown as ListJoinRequestsResponse
}

/**
 * Approve a pending join request; the backend creates the viewer
 * membership in the same flow.
 * Backend: POST /api/v1/tenants/:id/join-requests/:req_id/accept (Admin+).
 */
export async function acceptJoinRequest(
  tenantId: number,
  reqId: number,
): Promise<AcceptJoinResponse> {
  return (await post(
    `/api/v1/tenants/${tenantId}/join-requests/${reqId}/accept`,
  )) as unknown as AcceptJoinResponse
}

/**
 * Reject a pending join request. The requester may re-apply.
 * Backend: POST /api/v1/tenants/:id/join-requests/:req_id/decline (Admin+).
 */
export async function declineJoinRequest(
  tenantId: number,
  reqId: number,
): Promise<SimpleResponse> {
  return (await post(
    `/api/v1/tenants/${tenantId}/join-requests/${reqId}/decline`,
  )) as unknown as SimpleResponse
}