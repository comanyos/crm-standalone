export type Message = { Text?: string; text?: string; Type?: string; type?: string }
export type NodeAttributes = Record<string, string | boolean | number | undefined>
export type UINode = {
  Type?: string
  type?: string
  Group?: string
  group?: string
  Attributes?: NodeAttributes
  attributes?: NodeAttributes
}
export type Flow = {
  ID?: string
  id?: string
  State?: number
  state?: number
  UI?: { Nodes?: UINode[]; Messages?: Message[] }
  ui?: { nodes?: UINode[]; messages?: Message[] }
}

export function flowID(flow: Flow): string {
  return flow.id ?? flow.ID ?? ''
}

export function flowNodes(flow: Flow): UINode[] {
  return flow.ui?.nodes ?? flow.UI?.Nodes ?? []
}

export function flowMessages(flow: Flow): Message[] {
  return flow.ui?.messages ?? flow.UI?.Messages ?? []
}

export function attributes(node: UINode): NodeAttributes {
  return node.attributes ?? node.Attributes ?? {}
}

export function group(node: UINode): string {
  return node.group ?? node.Group ?? ''
}

async function decode<T>(response: Response): Promise<T> {
  const contentType = response.headers.get('content-type') ?? ''
  const payload: unknown = contentType.includes('application/json')
    ? await response.json()
    : await response.text()
  if (!response.ok) {
    const message = typeof payload === 'object' && payload && 'error' in payload
      ? String((payload as { error: unknown }).error)
      : String(payload || `Request failed (${response.status})`)
    throw new Error(message)
  }
  return payload as T
}

export async function startFlow(kind: string): Promise<Flow> {
  const params = new URLSearchParams(location.search)
  const returnTo = params.get('return_to')
  const query = returnTo ? `?return_to=${encodeURIComponent(returnTo)}` : ''
  return decode<Flow>(await fetch(`/self-service/${kind}${query}`, {
    credentials: 'include', headers: { Accept: 'application/json' },
  }))
}

export async function submitFlow(kind: string, flow: Flow, method: string, body: Record<string, unknown>): Promise<Flow | Record<string, unknown>> {
  const response = await fetch(`/self-service/${kind}?flow=${encodeURIComponent(flowID(flow))}`, {
    method: 'POST',
    credentials: 'include',
    headers: { 'Content-Type': 'application/json', Accept: 'application/json' },
    body: JSON.stringify({ method, body }),
  })
  if (response.status === 422) return response.json() as Promise<Flow>
  return decode<Record<string, unknown>>(response)
}

export async function getInvitation(token: string): Promise<{ email: string; role: string }> {
  return decode(await fetch(`/invitations/${encodeURIComponent(token)}`, { headers: { Accept: 'application/json' } }))
}

export async function acceptInvitation(token: string, email: string): Promise<void> {
  await decode(await fetch(`/invitations/${encodeURIComponent(token)}/accept`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ email }),
  }))
}
