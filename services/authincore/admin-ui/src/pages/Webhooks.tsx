import { useEffect, useState } from 'react'
import { api } from '../lib/api'
import Badge from '../components/Badge'

interface Webhook {
  id: string
  url: string
  events: string[]
  enabled: boolean
  signing_secret?: string
}

const ALL_EVENTS = [
  'identity.created', 'identity.updated', 'identity.deleted',
  'session.created', 'session.revoked',
  'invitation.created', 'invitation.accepted',
]

export default function Webhooks() {
  const [rows, setRows] = useState<Webhook[]>([])
  const [err, setErr] = useState('')
  const [saving, setSaving] = useState(false)
  const [url, setUrl] = useState('')
  const [secret, setSecret] = useState('')
  const [events, setEvents] = useState<string[]>(['identity.created', 'session.created'])

  const load = () =>
    api.get<{ webhooks: Webhook[] }>('/api/v1/webhooks')
      .then(r => setRows(r.webhooks ?? []))
      .catch(e => setErr(e.message))

  useEffect(() => { load() }, [])

  const toggleEvent = (ev: string) =>
    setEvents(es => es.includes(ev) ? es.filter(e => e !== ev) : [...es, ev])

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    if (events.length === 0) { setErr('Select at least one event'); return }
    setErr('')
    setSaving(true)
    await api.post('/api/v1/webhooks', { url, events, signing_secret: secret || undefined })
      .catch(e => setErr(e.message))
    setUrl('')
    setSecret('')
    setEvents(['identity.created', 'session.created'])
    await load()
    setSaving(false)
  }

  const del = async (id: string) => {
    if (!confirm('Delete this webhook?')) return
    await api.delete(`/api/v1/webhooks/${id}`).catch(e => setErr(e.message))
    await load()
  }

  const inp = 'w-full border border-gray-200 rounded px-3 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-blue-500'

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-gray-900">Webhooks</h1>
        <p className="text-sm text-gray-500 mt-1">Receive real-time event notifications via HTTP POST.</p>
      </div>
      {err && <p className="text-sm text-red-500 bg-red-50 border border-red-100 rounded p-2">{err}</p>}

      {/* Add form */}
      <form onSubmit={save} className="bg-white border border-gray-200 rounded-lg p-5 space-y-4">
        <h2 className="text-sm font-semibold text-gray-700">Add endpoint</h2>
        <div className="grid grid-cols-2 gap-3">
          <div className="col-span-2">
            <label className="text-xs text-gray-500 block mb-1">Endpoint URL</label>
            <input className={inp} required type="url" value={url} onChange={e => setUrl(e.target.value)}
              placeholder="https://your-server.com/webhook" />
          </div>
          <div className="col-span-2">
            <label className="text-xs text-gray-500 block mb-1">Signing secret <span className="text-gray-400">(optional — HMAC-SHA256)</span></label>
            <input className={inp} type="password" value={secret} onChange={e => setSecret(e.target.value)}
              placeholder="whsec_…" />
          </div>
        </div>

        <div>
          <label className="text-xs text-gray-500 block mb-2">Events to subscribe</label>
          <div className="flex flex-wrap gap-2">
            {ALL_EVENTS.map(ev => (
              <label key={ev} className="flex items-center gap-1.5 text-xs cursor-pointer">
                <input type="checkbox" checked={events.includes(ev)} onChange={() => toggleEvent(ev)}
                  className="rounded border-gray-300" />
                <span className="font-mono text-gray-700">{ev}</span>
              </label>
            ))}
          </div>
        </div>

        <button disabled={saving} type="submit"
          className="px-4 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700 disabled:opacity-40">
          {saving ? 'Saving…' : 'Add webhook'}
        </button>
      </form>

      {/* Webhook list */}
      <div className="bg-white border border-gray-200 rounded-lg overflow-hidden">
        {rows.length === 0 ? (
          <p className="text-sm text-gray-400 p-5">No webhooks configured yet.</p>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b border-gray-200">
              <tr>
                {['URL', 'Events', 'Status', ''].map(h => (
                  <th key={h} className="px-4 py-2 text-left text-xs font-medium text-gray-500">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {rows.map(wh => (
                <tr key={wh.id} className="hover:bg-gray-50">
                  <td className="px-4 py-2 font-mono text-xs truncate max-w-xs">{wh.url}</td>
                  <td className="px-4 py-2">
                    <div className="flex flex-wrap gap-1">
                      {(wh.events ?? []).map(ev => (
                        <span key={ev} className="text-xs bg-gray-100 text-gray-600 px-1.5 py-0.5 rounded font-mono">{ev}</span>
                      ))}
                    </div>
                  </td>
                  <td className="px-4 py-2">
                    <Badge label={wh.enabled ? 'active' : 'disabled'} color={wh.enabled ? 'green' : 'gray'} />
                  </td>
                  <td className="px-4 py-2 text-right">
                    <button onClick={() => del(wh.id)}
                      className="text-xs text-red-500 hover:text-red-700">Delete</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Payload info */}
      <div className="bg-gray-50 border border-gray-200 rounded-lg p-4 text-xs text-gray-600 space-y-1">
        <p className="font-medium text-gray-700">Payload format</p>
        <pre className="bg-white border border-gray-200 rounded p-3 overflow-x-auto">{`{
  "id": "uuid",
  "type": "identity.created",
  "created_at": "2026-06-11T12:00:00Z",
  "data": { ... }
}`}</pre>
        <p>Signature header: <code className="font-mono">X-Authincore-Signature: sha256=&lt;hmac&gt;</code></p>
      </div>
    </div>
  )
}
