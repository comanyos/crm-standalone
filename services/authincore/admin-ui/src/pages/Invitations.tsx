import { useEffect, useState } from 'react'
import { invitations, type Invitation } from '../lib/api'
import Table from '../components/Table'
import Badge from '../components/Badge'

export default function Invitations() {
  const [rows, setRows] = useState<Invitation[]>([])
  const [email, setEmail] = useState('')
  const [role, setRole] = useState('member')
  const [err, setErr] = useState('')
  const [sending, setSending] = useState(false)

  const load = () => invitations.list().then(setRows).catch(e => setErr(e.message))
  useEffect(() => { load() }, [])

  const send = async (e: React.FormEvent) => {
    e.preventDefault()
    setSending(true)
    await invitations.create(email, role).catch(e => setErr(e.message))
    setEmail('')
    await load()
    setSending(false)
  }

  return (
    <div className="space-y-6">
      <h1 className="text-xl font-semibold text-gray-900">Invitations</h1>
      {err && <p className="text-red-500 text-sm">{err}</p>}

      <form onSubmit={send} className="flex gap-3 items-end bg-white border border-gray-200 rounded-lg p-4">
        <div>
          <label htmlFor="inv-email" className="text-xs text-gray-500 block mb-1">Email</label>
          <input id="inv-email" type="email" value={email} onChange={e => setEmail(e.target.value)} required
            className="border border-gray-200 rounded px-3 py-1.5 text-sm w-56 focus:outline-none focus:ring-1 focus:ring-brand-500" />
        </div>
        <div>
          <label htmlFor="inv-role" className="text-xs text-gray-500 block mb-1">Role</label>
          <select id="inv-role" value={role} onChange={e => setRole(e.target.value)}
            className="border border-gray-200 rounded px-3 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-brand-500">
            <option value="member">Member</option>
            <option value="admin">Admin</option>
            <option value="viewer">Viewer</option>
          </select>
        </div>
        <button disabled={sending} className="px-4 py-1.5 bg-brand-600 text-white text-sm rounded hover:bg-brand-700 disabled:opacity-40">
          {sending ? 'Sending…' : 'Invite'}
        </button>
      </form>

      <Table<Invitation>
        columns={[
          { key: 'email', header: 'Email' },
          { key: 'role', header: 'Role' },
          { key: 'expires_at', header: 'Expires', render: r => new Date(r.expires_at).toLocaleDateString() },
          { key: 'accepted_at', header: 'Status', render: r =>
            r.accepted_at ? <Badge label="Accepted" color="green" /> : <Badge label="Pending" color="yellow" />
          },
        ]}
        rows={rows}
      />
    </div>
  )
}
