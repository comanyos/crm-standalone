import { useEffect, useState } from 'react'
import { activeOrgId, api } from '../lib/api'

interface BrandingConfig {
  logo_url: string
  primary_color: string
  custom_css: string
  theme: Record<string, unknown>
}

export default function Branding() {
  const [cfg, setCfg] = useState<BrandingConfig>({ logo_url: '', primary_color: '#2563eb', custom_css: '', theme: {} })
  const [saved, setSaved] = useState(false)
  const [err, setErr] = useState('')

  useEffect(() => {
    const org = activeOrgId()
    if (!org) { setErr('Select an organisation in Settings first.'); return }
    api.get<BrandingConfig>(`/api/v1/orgs/${org}/branding`)
      .then(setCfg)
      .catch(() => {})
  }, [])

  const save = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    const org = activeOrgId()
    if (!org) { setErr('Select an organisation in Settings first.'); return }
    await api.post(`/api/v1/orgs/${org}/branding`, cfg)
      .then(() => { setSaved(true); setTimeout(() => setSaved(false), 2000) })
      .catch(e => setErr(e.message))
  }

  const field = (key: 'logo_url' | 'primary_color' | 'custom_css', label: string, placeholder: string, type = 'text') => (
    <div>
      <label className="text-xs font-medium text-gray-600 block mb-1">{label}</label>
      <input type={type} value={cfg[key]} onChange={e => setCfg(p => ({ ...p, [key]: e.target.value }))}
        placeholder={placeholder}
        className="w-full border border-gray-200 rounded px-3 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-blue-500" />
    </div>
  )

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-gray-900">Custom Branding</h1>
        <p className="text-sm text-gray-500 mt-1">Customise the login experience for your organisation.</p>
      </div>
      {err && <p className="text-sm text-red-500">{err}</p>}

      <form onSubmit={save} className="bg-white border border-gray-200 rounded-lg p-5 space-y-4 max-w-lg">
        {field('logo_url', 'Logo URL', 'https://example.com/logo.png')}
        <div>
          <label className="text-xs font-medium text-gray-600 block mb-1">Primary colour</label>
          <div className="flex items-center gap-3">
            <input type="color" value={cfg.primary_color} onChange={e => setCfg(p => ({ ...p, primary_color: e.target.value }))}
              className="h-8 w-14 cursor-pointer rounded border border-gray-200" />
            <span className="text-sm text-gray-500 font-mono">{cfg.primary_color}</span>
          </div>
        </div>
        <div>
          <label htmlFor="custom-css" className="text-xs font-medium text-gray-600 block mb-1">Custom CSS</label>
          <textarea id="custom-css" value={cfg.custom_css} onChange={e => setCfg(p => ({ ...p, custom_css: e.target.value }))}
            placeholder=".login-card { border-radius: 1rem; }"
            className="w-full min-h-28 border border-gray-200 rounded px-3 py-2 text-sm font-mono focus:outline-none focus:ring-1 focus:ring-blue-500" />
        </div>

        {cfg.logo_url && (
          <div className="border border-dashed border-gray-200 rounded p-4 flex items-center gap-3">
            <img src={cfg.logo_url} alt="Logo preview" className="h-8 object-contain" onError={e => (e.currentTarget.style.display='none')} />
            <span className="text-xs text-gray-400">Preview</span>
          </div>
        )}

        <button type="submit" className="px-4 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700">
          {saved ? '✓ Saved' : 'Save branding'}
        </button>
      </form>
    </div>
  )
}
