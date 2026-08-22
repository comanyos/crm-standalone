import { useState } from 'react'

export default function Settings() {
  const [orgId, setOrgId] = useState(localStorage.getItem('org_id') ?? '')
  const [saved, setSaved] = useState(false)

  const save = (e: React.FormEvent) => {
    e.preventDefault()
    localStorage.setItem('org_id', orgId)
    setSaved(true)
    setTimeout(() => setSaved(false), 2000)
  }

  return (
    <div>
      <h1 className="text-xl font-semibold text-gray-900 mb-6">Settings</h1>
      <div className="bg-white border border-gray-200 rounded-lg p-6 max-w-md">
        <form onSubmit={save} className="space-y-4">
          <div>
            <label className="text-sm font-medium text-gray-700 block mb-1">Default Org ID</label>
            <input value={orgId} onChange={e => setOrgId(e.target.value)}
              placeholder="uuid of your org"
              className="w-full border border-gray-200 rounded px-3 py-2 text-sm font-mono focus:outline-none focus:ring-1 focus:ring-brand-500" />
            <p className="text-xs text-gray-400 mt-1">Stored locally and used for tenant-scoped API paths. The server still validates it against the authenticated tenant.</p>
          </div>
          <button className="px-4 py-2 bg-brand-600 text-white text-sm rounded hover:bg-brand-700">
            {saved ? 'Saved ✓' : 'Save'}
          </button>
        </form>
      </div>
    </div>
  )
}
