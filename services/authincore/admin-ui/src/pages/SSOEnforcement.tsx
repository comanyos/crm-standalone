import { useEffect, useState } from 'react'
import { activeOrgId, api } from '../lib/api'

interface LoginPolicy {
	sso_enforced: boolean
	sso_provider_id?: string | null
	allow_password_fallback: boolean
}

export default function SSOEnforcement() {
	const [policy, setPolicy] = useState<LoginPolicy>({ sso_enforced: false, sso_provider_id: null, allow_password_fallback: true })
  const [saved, setSaved] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    const org = activeOrgId()
    if (!org) { setErr('Select an organisation in Settings first.'); return }
    api.get<LoginPolicy>(`/api/v1/orgs/${org}/login-policy`)
      .then(setPolicy)
      .catch(() => {})
  }, [])

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    const org = activeOrgId()
    if (!org) { setErr('Select an organisation in Settings first.'); return }
    await api.post(`/api/v1/orgs/${org}/login-policy`, policy)
      .then(() => { setSaved(true); setTimeout(() => setSaved(false), 2000) })
      .catch(e => setErr(e.message))
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-gray-900">SSO Enforcement</h1>
        <p className="text-sm text-gray-500 mt-1">Control which authentication methods are allowed for this org.</p>
      </div>
      {err && <p className="text-sm text-red-500">{err}</p>}

      <form onSubmit={save} className="bg-white border border-gray-200 rounded-lg p-5 space-y-4 max-w-lg">
        {[
		  { key: 'sso_enforced' as const, label: 'Require enterprise SSO', desc: 'Only configured SAML or OIDC authentication is accepted' },
		  { key: 'allow_password_fallback' as const, label: 'Allow password fallback', desc: 'Permit break-glass password login while SSO is required' },
        ].map(({ key, label, desc }) => (
          <label key={key} className="flex items-start gap-3 cursor-pointer">
            <input type="checkbox" checked={policy[key]} onChange={e => setPolicy(p => ({ ...p, [key]: e.target.checked }))}
              className="mt-0.5 h-4 w-4 rounded border-gray-300 text-blue-600 focus:ring-blue-500" />
            <div>
              <p className="text-sm font-medium text-gray-800">{label}</p>
              <p className="text-xs text-gray-400">{desc}</p>
            </div>
          </label>
        ))}

        <button type="submit" className="px-4 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700">
          {saved ? '✓ Saved' : 'Save policy'}
        </button>
      </form>
    </div>
  )
}
