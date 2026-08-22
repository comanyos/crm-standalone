import { useEffect, useState } from 'react'
import { activeOrgId, api } from '../lib/api'

interface AuditConfig {
  id: string
  endpoint_url: string
  format: string
  enabled: boolean
}

export default function AuditExport() {
  const [configs, setConfigs] = useState<AuditConfig[]>([])
  const [err, setErr] = useState('')
  const [url, setUrl] = useState('')
  const [format, setFormat] = useState('json')
  const [saving, setSaving] = useState(false)

  const load = () => {
    const org = activeOrgId()
    if (!org) { setErr('Select an organisation in Settings first.'); return Promise.resolve() }
    return api.get<AuditConfig[]>(`/api/v1/orgs/${org}/audit-export`)
      .then(setConfigs)
      .catch(() => setConfigs([]))
  }

  useEffect(() => { load() }, [])

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    setSaving(true)
    setErr('')
    const org = activeOrgId()
    if (!org) { setErr('Select an organisation in Settings first.'); setSaving(false); return }
    await api.post(`/api/v1/orgs/${org}/audit-export`, { endpoint_url: url, format, enabled: true })
      .catch(e => setErr(e.message))
    setUrl('')
    await load()
    setSaving(false)
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-gray-900">Audit Export</h1>
        <p className="text-sm text-gray-500 mt-1">Stream audit events to your SIEM or log aggregator via webhook.</p>
      </div>
      {err && <p className="text-sm text-red-500">{err}</p>}

      <form onSubmit={save} className="bg-white border border-gray-200 rounded-lg p-5 space-y-4">
        <h2 className="text-sm font-medium text-gray-700">Add endpoint</h2>
        <div className="flex gap-3 items-end">
          <div className="flex-1">
            <label className="text-xs text-gray-500 block mb-1">Endpoint URL</label>
            <input value={url} onChange={e => setUrl(e.target.value)} required placeholder="https://logs.example.com/ingest"
              className="w-full border border-gray-200 rounded px-3 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-blue-500" />
          </div>
          <div>
            <label className="text-xs text-gray-500 block mb-1">Format</label>
            <select value={format} onChange={e => setFormat(e.target.value)}
              className="border border-gray-200 rounded px-3 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-blue-500">
              <option value="json">JSON</option>
              <option value="cef">CEF</option>
            </select>
          </div>
          <button disabled={saving} className="px-4 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700 disabled:opacity-40">
            {saving ? 'Saving…' : 'Add'}
          </button>
        </div>
      </form>

      <div className="bg-white border border-gray-200 rounded-lg divide-y divide-gray-100">
        {configs.length === 0
          ? <p className="text-sm text-gray-400 p-5 text-center">No endpoints configured</p>
          : configs.map(c => (
            <div key={c.id} className="flex items-center justify-between px-5 py-3">
              <div>
                <p className="text-sm font-medium text-gray-800">{c.endpoint_url}</p>
                <p className="text-xs text-gray-400 mt-0.5">{c.format.toUpperCase()}</p>
              </div>
              <span className={`text-xs px-2 py-0.5 rounded-full font-medium ${c.enabled ? 'bg-green-100 text-green-700' : 'bg-gray-100 text-gray-500'}`}>
                {c.enabled ? 'Active' : 'Disabled'}
              </span>
            </div>
          ))
        }
      </div>
    </div>
  )
}
