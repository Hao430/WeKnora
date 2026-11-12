import { get, post } from '@/utils/request'
import type { LoginResponse } from './index'

// DingTalk login (网页应用). The backend owns the app credentials; the browser
// only ever sees the authorization URL and the short-lived code.

export interface DingTalkConfigResponse {
  success: boolean
  enabled: boolean
  provider_display_name?: string
  corp_id?: string
}

export interface DingTalkExchangeRequest {
  code: string
  // 'inner' = code from the DingTalk container JSAPI, 'oauth' = scan redirect.
  source: 'inner' | 'oauth'
  invite_token?: string
  // 'bind' attaches the identity to the current bearer token instead of
  // logging in.
  mode?: 'bind'
}

export const getDingTalkConfig = async (): Promise<DingTalkConfigResponse> => {
  const response = await get('/api/v1/auth/dingtalk/config')
  return response as unknown as DingTalkConfigResponse
}

// Returns the authorization URL for the bind flow. A browser cannot follow a
// cross-origin 302 through XHR, so this variant answers with JSON.
export const getDingTalkBindURL = async (): Promise<{ success: boolean; authorization_url?: string }> => {
  const response = await get('/api/v1/auth/dingtalk/start', { params: { mode: 'bind' } })
  return response as unknown as { success: boolean; authorization_url?: string }
}

export const exchangeDingTalkCode = async (payload: DingTalkExchangeRequest): Promise<LoginResponse> => {
  const response = await post('/api/v1/auth/dingtalk/callback', payload)
  return response as unknown as LoginResponse
}

// The full-page login entry: the backend 302s to DingTalk, so this must be a
// navigation, not an XHR. BASE_URL keeps it working under a sub-path deploy.
export const dingTalkLoginURL = (): string => {
  const base = (import.meta.env.BASE_URL || '/').replace(/\/+$/, '')
  return `${base}/api/v1/auth/dingtalk/start`
}

// Bound external accounts of the caller. The profile page uses this to show
// "already bound" instead of offering the bind button again.
export interface UserIdentitySummary {
  provider: string
  display_name?: string
  bound_at: string
}

export const listMyIdentities = async (): Promise<{
  success: boolean
  data?: { identities: UserIdentitySummary[] }
}> => {
  const response = await get('/api/v1/auth/identities')
  return response as unknown as { success: boolean; data?: { identities: UserIdentitySummary[] } }
}
