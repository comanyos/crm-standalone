import { useEffect, useState } from 'react'

interface Flow {
  id: string
  identity?: { traits?: { email?: string } }
}

export default function SelfServiceSettings() {
  const [flow, setFlow] = useState<Flow | null>(null)
  const [email, setEmail] = useState('')
  const [currentPw, setCurrentPw] = useState('')
  const [newPw, setNewPw] = useState('')
  const [err, setErr] = useState('')
  const [success, setSuccess] = useState('')
  const [loading, setLoading] = useState(false)
  const [activeTab, setActiveTab] = useState<'profile' | 'password'>('profile')

  const inp = 'w-full border border-gray-200 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500'

  useEffect(() => {
    fetch('/self-service/settings', { credentials: 'include' })
      .then(r => r.json())
      .then((f: Flow) => {
        setFlow(f)
        setEmail(f.identity?.traits?.email ?? '')
      })
      .catch(e => setErr(e.message))
  }, [])

  const submit = async (method: string, body: Record<string, unknown>) => {
    if (!flow) return
    setErr('')
    setSuccess('')
    setLoading(true)
    try {
      const res = await fetch(`/self-service/settings?flow=${flow.id}`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ method, body }),
      })
      const data = await res.json().catch(() => ({}))
      if (!res.ok) throw new Error(data.error ?? `Error ${res.status}`)
      setSuccess('Settings updated successfully.')
      setCurrentPw('')
      setNewPw('')
      // Refresh flow
      const newFlow = await fetch('/self-service/settings', { credentials: 'include' }).then(r => r.json())
      setFlow(newFlow)
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : 'Update failed')
    } finally {
      setLoading(false)
    }
  }

  const saveProfile = (e: React.FormEvent) => {
    e.preventDefault()
    submit('profile', { traits: { email } })
  }

  const changePassword = (e: React.FormEvent) => {
    e.preventDefault()
    submit('password', { password: newPw, current_password: currentPw })
  }

  const tabCls = (t: 'profile' | 'password') =>
    `px-4 py-2 text-sm font-medium border-b-2 transition-colors ${
      activeTab === t
        ? 'border-blue-600 text-blue-700'
        : 'border-transparent text-gray-500 hover:text-gray-700'
    }`

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-50">
      <div className="bg-white border border-gray-200 rounded-xl shadow-sm w-full max-w-md">
        <div className="px-8 pt-8 pb-4">
          <h1 className="text-xl font-semibold text-gray-900 mb-1">Account Settings</h1>
          <p className="text-sm text-gray-500">Update your profile and password.</p>
        </div>

        <div className="border-b border-gray-200 px-8 flex gap-0">
          <button className={tabCls('profile')} onClick={() => { setActiveTab('profile'); setErr(''); setSuccess('') }}>Profile</button>
          <button className={tabCls('password')} onClick={() => { setActiveTab('password'); setErr(''); setSuccess('') }}>Password</button>
        </div>

        <div className="px-8 py-6">
          {err && <p className="text-sm text-red-500 mb-4 bg-red-50 border border-red-100 rounded p-2">{err}</p>}
          {success && <p className="text-sm text-green-700 mb-4 bg-green-50 border border-green-100 rounded p-2">{success}</p>}

          {!flow ? (
            <p className="text-gray-400 text-sm">Loading…</p>
          ) : activeTab === 'profile' ? (
            <form onSubmit={saveProfile} className="space-y-4">
              <div>
                <label className="text-xs font-medium text-gray-600 block mb-1">Email</label>
                <input type="email" required className={inp} value={email}
                  onChange={e => setEmail(e.target.value)} />
              </div>
              <button type="submit" disabled={loading}
                className="w-full bg-blue-600 text-white rounded-lg py-2 text-sm font-medium hover:bg-blue-700 disabled:opacity-40">
                {loading ? 'Saving…' : 'Save profile'}
              </button>
            </form>
          ) : (
            <form onSubmit={changePassword} className="space-y-4">
              <div>
                <label className="text-xs font-medium text-gray-600 block mb-1">Current password</label>
                <input type="password" required className={inp} value={currentPw}
                  onChange={e => setCurrentPw(e.target.value)} />
              </div>
              <div>
                <label className="text-xs font-medium text-gray-600 block mb-1">New password</label>
                <input type="password" required minLength={8} className={inp} value={newPw}
                  onChange={e => setNewPw(e.target.value)} />
              </div>
              <button type="submit" disabled={loading}
                className="w-full bg-blue-600 text-white rounded-lg py-2 text-sm font-medium hover:bg-blue-700 disabled:opacity-40">
                {loading ? 'Updating…' : 'Change password'}
              </button>
            </form>
          )}
        </div>
      </div>
    </div>
  )
}
