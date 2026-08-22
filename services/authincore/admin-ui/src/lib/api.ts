const BASE = import.meta.env.VITE_API_URL ?? ''
const ADMIN_TOKEN = import.meta.env.VITE_ADMIN_TOKEN ?? ''

async function req<T>(method: string, path: string, body?: unknown, adminAuth = false): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  if (adminAuth && ADMIN_TOKEN) headers['Authorization'] = `Bearer ${ADMIN_TOKEN}`
  const res = await fetch(BASE + path, {
    method,
    headers,
    credentials: 'include',
    body: body ? JSON.stringify(body) : undefined,
  })
  if (res.status === 401 && !adminAuth) {
    globalThis.location.href = '/login'
    throw new Error('unauthorized')
  }
  if (!res.ok) {
    const text = await res.text().catch(() => res.statusText)
    throw new Error(text || res.statusText)
  }
  if (res.status === 204) return undefined as T
  return res.json()
}

export const api = {
  get:    <T>(path: string)              => req<T>('GET',    path),
  post:   <T>(path: string, body: unknown) => req<T>('POST',   path, body),
  put:    <T>(path: string, body: unknown) => req<T>('PUT',    path, body),
  delete: <T>(path: string)              => req<T>('DELETE', path),
}

// --- resource helpers ---
export const orgId = () => localStorage.getItem('org_id') ?? ''
export const activeOrgId = () => orgId() || import.meta.env.VITE_ORG_ID || ''

export const identities = {
  list:   (page = 1) => api.get<{items: Identity[], total: number}>(`/api/v1/identities?page=${page}`),
  get:    (id: string) => api.get<Identity>(`/api/v1/identities/${id}`),
  patch:  (id: string, traits: Record<string, unknown>) => req<Identity>('PATCH', `/api/v1/identities/${id}`, { traits }),
  delete: (id: string) => api.delete(`/api/v1/identities/${id}`),
}

export const sessions = {
  list:      (identityId: string) => api.get<{sessions: Session[]}>(`/api/v1/identities/${identityId}/sessions`).then(r => r.sessions ?? []),
	revokeAll: (identityId: string) => req<void>('DELETE', `/api/v1/identities/${identityId}/sessions`),
}

const INSTANCE_ID  = import.meta.env.VITE_INSTANCE_ID ?? '00000000-0000-0000-0000-000000000001'

export const orgs = {
  get:    (id: string) => api.get<Org>(`/api/v1/admin/orgs/${id}`),
  list:   ()           => api.get<{orgs: Org[]}>(`/api/v1/admin/orgs?instance_id=${INSTANCE_ID}`).then(r => r.orgs ?? []),
  create: (name: string, primaryDomain: string) => api.post<Org>('/api/v1/admin/orgs', { name, primary_domain: primaryDomain, instance_id: INSTANCE_ID }),
}

export const invitations = {
  list:   () => {
    const id = activeOrgId()
    if (!id) return Promise.reject(new Error('Select an organisation in Settings first.'))
    return api.get<{ invitations: Invitation[] }>(`/api/v1/orgs/${id}/invitations`).then(r => r.invitations ?? [])
  },
  create: (email: string, role: string) => api.post('/api/v1/invitations', { email, role }),
}

// --- FGA (fine-grained authorization) ---
const fgaQuery = (f: Partial<FgaTuple>) => {
  const q = new URLSearchParams()
  if (f.object_type)  q.set('object_type', f.object_type)
  if (f.object_id)    q.set('object_id', f.object_id)
  if (f.relation)     q.set('relation', f.relation)
  if (f.subject_type) q.set('subject_type', f.subject_type)
  if (f.subject_id)   q.set('subject_id', f.subject_id)
  const s = q.toString()
  return s ? `?${s}` : ''
}

export const fga = {
  model:       ()                 => api.get<FgaModel>('/api/v1/admin/fga/model'),
  listTuples:  (filter: Partial<FgaTuple> = {}) =>
    api.get<{ tuples: FgaTuple[]; total: number }>(`/api/v1/admin/fga/tuples${fgaQuery(filter)}`)
      .then(r => r.tuples ?? []),
  writeTuple:  (t: FgaTuple)      => api.post('/api/v1/admin/fga/tuples', t),
  deleteTuple: (t: FgaTuple)      => req<void>('DELETE', '/api/v1/admin/fga/tuples', t),
  check:       (req: Omit<FgaTuple, 'subject_relation'>) =>
    api.post<{ allowed: boolean }>('/api/v1/admin/fga/check', req).then(r => r.allowed),
}

// --- types ---
export interface FgaTuple {
  object_type: string
  object_id: string
  relation: string
  subject_type: string
  subject_id: string
  subject_relation?: string
}
export interface FgaTypeDef {
  relations: string[]
  implies?: Record<string, string[]>
}
export interface FgaModel {
  types: Record<string, FgaTypeDef>
}

export interface Identity {
  id: string; org_id: string; schema_id: string
  traits: { email?: string; [key: string]: unknown }; state: number
}
export interface Session {
  id: string; identity_id: string; org_id: string
  state: number; aal: number
  authentication_methods?: { method: string }[]
  authenticated_at: string; expires_at: string
}
export interface Org {
  id: string; name: string; primary_domain: string; state: number
}
export interface Invitation {
  id: string; email: string; role: string; expires_at: string; accepted_at?: string
}
