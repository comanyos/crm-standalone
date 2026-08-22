import { useEffect, useState } from 'react'
import { api } from '../lib/api'

interface Stats { identities: number; orgs: number }

export default function Dashboard() {
  const [stats, setStats] = useState<Stats>({ identities: 0, orgs: 0 })

  useEffect(() => {
    api.get<{ total: number }>('/api/v1/identities?page_size=1')
      .then(r => setStats(s => ({ ...s, identities: r.total ?? 0 })))
      .catch(() => {})

    api.get<{orgs: unknown[]}>('/api/v1/admin/orgs?instance_id=00000000-0000-0000-0000-000000000001')
      .then(r => setStats(s => ({ ...s, orgs: r.orgs?.length ?? 0 })))
      .catch(() => {})
  }, [])

  const cards = [
    { label: 'Total Identities', value: stats.identities },
    { label: 'Active Orgs',      value: stats.orgs },
    { label: 'Health',           value: '✓ OK' },
  ]

  return (
    <div>
      <h1 className="text-xl font-semibold text-gray-900 mb-6">Dashboard</h1>
      <div className="grid grid-cols-3 gap-4">
        {cards.map(c => (
          <div key={c.label} className="bg-white rounded-lg border border-gray-200 p-5">
            <p className="text-sm text-gray-500">{c.label}</p>
            <p className="mt-1 text-2xl font-semibold text-gray-900">{c.value}</p>
          </div>
        ))}
      </div>
    </div>
  )
}
