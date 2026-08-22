import { useEffect, useState } from 'react'
import { orgs, type Org } from '../lib/api'
import Table from '../components/Table'
import Badge from '../components/Badge'

export default function Orgs() {
  const [rows, setRows] = useState<Org[]>([])
  const [err, setErr] = useState('')
  const [name, setName] = useState('')
  const [domain, setDomain] = useState('')
  const [creating, setCreating] = useState(false)

  const load = () => orgs.list().then(r => setRows(r)).catch(e => setErr(e.message))
  useEffect(() => { load() }, [])

  const create = async (e: React.FormEvent) => {
    e.preventDefault()
    setCreating(true)
    setErr('')
    await orgs.create(name, domain).catch(e => setErr(e.message))
    setName(''); setDomain('')
    await load()
    setCreating(false)
  }

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-semibold text-gray-900">Organisations</h1>
      {err && <p className="text-red-500 text-sm">{err}</p>}

      <form onSubmit={create} className="flex gap-3 items-end bg-white border border-gray-200 rounded-lg p-4">
        <div>
          <label className="text-xs text-gray-500 block mb-1">Name (slug)</label>
          <input value={name} onChange={e => setName(e.target.value)} required placeholder="acme"
            className="border border-gray-200 rounded px-3 py-1.5 text-sm w-40 focus:outline-none focus:ring-1 focus:ring-blue-500" />
        </div>
        <div>
          <label className="text-xs text-gray-500 block mb-1">Primary domain</label>
          <input value={domain} onChange={e => setDomain(e.target.value)} required placeholder="acme.example.com"
            className="border border-gray-200 rounded px-3 py-1.5 text-sm w-52 focus:outline-none focus:ring-1 focus:ring-blue-500" />
        </div>
        <button disabled={creating} className="px-4 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700 disabled:opacity-40">
          {creating ? 'Creating…' : 'Create'}
        </button>
      </form>

      <Table<Org>
        columns={[
          { key: 'id', header: 'ID', render: r => <span className="font-mono text-xs">{r.id.slice(0,8)}…</span> },
          { key: 'name', header: 'Name' },
          { key: 'primary_domain', header: 'Domain' },
          { key: 'state', header: 'State', render: r => <Badge label={String(r.state)} color={r.state === 1 ? 'green' : 'gray'} /> },
        ]}
        rows={rows}
      />
    </div>
  )
}
