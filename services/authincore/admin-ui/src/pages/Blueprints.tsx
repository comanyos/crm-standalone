import { useEffect, useState } from 'react'
import { api } from '../lib/api'

interface Blueprint {
  id?: string
  name?: string
  version?: string
  created_at?: string
  [key: string]: unknown
}

interface ApplyResult {
  blueprint: Blueprint
  diff: string | null
}

const EXAMPLE_YAML = `# AuthinCore Blueprint
# Define your org configuration as code

schemas:
  - id: default
    name: Default Identity Schema

webhooks:
  - url: https://example.com/hooks
    events:
      - identity.created
      - session.created

settings:
  session_lifespan: 86400
  mfa_required: false
`

export default function Blueprints() {
  const [blueprints, setBlueprints] = useState<Blueprint[]>([])
  const [yaml, setYaml] = useState(EXAMPLE_YAML)
  const [dryRun, setDryRun] = useState(true)
  const [result, setResult] = useState<ApplyResult | null>(null)
  const [err, setErr] = useState('')
  const [applying, setApplying] = useState(false)
  const [tab, setTab] = useState<'apply' | 'history'>('apply')

  useEffect(() => {
    api.get<Blueprint[]>('/api/v1/admin/blueprints')
      .then(setBlueprints)
      .catch(() => {})
  }, [])

  const apply = async () => {
    setErr('')
    setResult(null)
    setApplying(true)
    try {
      const res = await api.post<ApplyResult>('/api/v1/admin/blueprints', { yaml, dry_run: dryRun })
      setResult(res)
      if (!dryRun) {
        // Refresh history
        const bps = await api.get<Blueprint[]>('/api/v1/admin/blueprints').catch(() => [])
        setBlueprints(bps ?? [])
      }
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : 'Apply failed')
    } finally {
      setApplying(false)
    }
  }

  const tabCls = (t: 'apply' | 'history') =>
    `px-4 py-2 text-sm font-medium border-b-2 transition-colors ${
      tab === t
        ? 'border-blue-600 text-blue-700'
        : 'border-transparent text-gray-500 hover:text-gray-700'
    }`

  return (
    <div className="space-y-5">
      <div>
        <h1 className="text-xl font-semibold text-gray-900">Blueprints</h1>
        <p className="text-sm text-gray-500 mt-0.5">Manage your org configuration as declarative YAML.</p>
      </div>

      <div className="border-b border-gray-200 flex gap-0">
        <button className={tabCls('apply')} onClick={() => setTab('apply')}>Apply</button>
        <button className={tabCls('history')} onClick={() => setTab('history')}>History ({blueprints.length})</button>
      </div>

      {tab === 'apply' ? (
        <div className="space-y-4">
          <div className="bg-white rounded-lg border border-gray-200 p-4">
            <div className="flex items-center justify-between mb-2">
              <label className="text-xs font-semibold text-gray-500 uppercase tracking-wider">Blueprint YAML</label>
              <span className="text-xs text-gray-400">{yaml.length} chars</span>
            </div>
            <textarea
              value={yaml}
              onChange={e => setYaml(e.target.value)}
              rows={16}
              className="w-full bg-gray-50 border border-gray-200 rounded p-3 text-xs font-mono focus:outline-none focus:ring-1 focus:ring-blue-500 resize-y"
            />
          </div>

          <div className="flex items-center gap-4">
            <label className="flex items-center gap-2 text-sm text-gray-700 cursor-pointer">
              <input
                type="checkbox"
                checked={dryRun}
                onChange={e => setDryRun(e.target.checked)}
                className="rounded border-gray-300 text-blue-600 focus:ring-blue-500"
              />
              Dry run (preview changes without applying)
            </label>
            <button
              onClick={apply}
              disabled={applying || !yaml.trim()}
              className={`px-5 py-1.5 text-sm rounded font-medium disabled:opacity-40 ${
                dryRun
                  ? 'border border-blue-600 text-blue-600 hover:bg-blue-50'
                  : 'bg-blue-600 text-white hover:bg-blue-700'
              }`}
            >
              {applying ? 'Running…' : dryRun ? 'Preview diff' : 'Apply blueprint'}
            </button>
          </div>

          {err && <p className="text-sm text-red-500 bg-red-50 border border-red-100 rounded p-3">{err}</p>}

          {result && (
            <div className="bg-white rounded-lg border border-gray-200 p-4 space-y-3">
              <div className="flex items-center gap-2">
                <span className={`text-xs font-semibold px-2 py-0.5 rounded-full ${
                  dryRun ? 'bg-yellow-100 text-yellow-700' : 'bg-green-100 text-green-700'
                }`}>
                  {dryRun ? 'DRY RUN' : 'APPLIED'}
                </span>
                <span className="text-sm text-gray-600">
                  {result.blueprint?.name ?? 'Blueprint'} {result.blueprint?.version && `v${result.blueprint.version}`}
                </span>
              </div>

              {result.diff ? (
                <div>
                  <p className="text-xs font-semibold text-gray-400 uppercase tracking-wider mb-1">Diff</p>
                  <pre className="bg-gray-900 text-gray-100 rounded p-3 text-xs overflow-x-auto whitespace-pre-wrap">{result.diff}</pre>
                </div>
              ) : (
                <p className="text-xs text-gray-400">No changes detected.</p>
              )}
            </div>
          )}
        </div>
      ) : (
        <div className="bg-white rounded-lg border border-gray-200 overflow-hidden">
          {blueprints.length === 0 ? (
            <div className="p-8 text-center text-sm text-gray-400">
              No blueprints applied yet.
            </div>
          ) : (
            <table className="w-full text-sm">
              <thead className="bg-gray-50 border-b border-gray-200">
                <tr>
                  <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase tracking-wider">ID</th>
                  <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase tracking-wider">Name</th>
                  <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase tracking-wider">Version</th>
                  <th className="px-4 py-3 text-left text-xs font-semibold text-gray-500 uppercase tracking-wider">Applied at</th>
                </tr>
              </thead>
              <tbody className="divide-y divide-gray-100">
                {blueprints.map((bp, i) => (
                  <tr key={bp.id ?? i} className="hover:bg-gray-50">
                    <td className="px-4 py-3 font-mono text-xs text-gray-500">{String(bp.id ?? '—').slice(0, 8)}…</td>
                    <td className="px-4 py-3 text-gray-800">{String(bp.name ?? '—')}</td>
                    <td className="px-4 py-3 text-gray-500">{String(bp.version ?? '—')}</td>
                    <td className="px-4 py-3 text-gray-500">
                      {bp.created_at ? new Date(String(bp.created_at)).toLocaleString() : '—'}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
        </div>
      )}
    </div>
  )
}
