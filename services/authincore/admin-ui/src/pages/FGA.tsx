import { useEffect, useMemo, useState } from 'react'
import { fga, type FgaTuple, type FgaModel } from '../lib/api'

const inp = 'w-full border border-gray-200 rounded px-3 py-1.5 text-sm focus:outline-none focus:ring-1 focus:ring-blue-500'
const empty: FgaTuple = { object_type: '', object_id: '', relation: '', subject_type: 'user', subject_id: '', subject_relation: '' }

export default function FGA() {
  const [model, setModel] = useState<FgaModel | null>(null)
  const [rows, setRows] = useState<FgaTuple[]>([])
  const [err, setErr] = useState('')

  const [filter, setFilter] = useState<Partial<FgaTuple>>({})
  const [draft, setDraft] = useState<FgaTuple>(empty)
  const [saving, setSaving] = useState(false)

  const [probe, setProbe] = useState<FgaTuple>({ ...empty })
  const [probeResult, setProbeResult] = useState<null | boolean>(null)

  const load = (f: Partial<FgaTuple> = filter) =>
    fga.listTuples(f).then(setRows).catch(e => setErr(e.message))

  useEffect(() => {
    fga.model().then(setModel).catch(e => setErr(e.message))
    load({})
  }, [])

  // Relation suggestions: type-specific if the object type is in the model, else all known relations.
  const relationsFor = (objectType: string): string[] => {
    if (!model) return []
    const td = model.types[objectType]
    if (td) return td.relations
    return [...new Set(Object.values(model.types).flatMap(t => t.relations))].sort()
  }
  const allTypes = useMemo(() => model ? Object.keys(model.types) : [], [model])

  const tupleKey = (t: FgaTuple) =>
    `${t.object_type}:${t.object_id}#${t.relation}@${t.subject_type}:${t.subject_id}${t.subject_relation ? '#' + t.subject_relation : ''}`

  const add = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    setSaving(true)
    try {
      await fga.writeTuple({ ...draft, subject_relation: draft.subject_relation || undefined })
      setDraft({ ...empty, object_type: draft.object_type, relation: draft.relation, subject_type: draft.subject_type })
      await load()
    } catch (e) { setErr((e as Error).message) }
    setSaving(false)
  }

  const del = async (t: FgaTuple) => {
    if (!confirm(`Delete tuple?\n\n${tupleKey(t)}`)) return
    setErr('')
    try {
      await fga.deleteTuple(t)
      await load()
    } catch (e) { setErr((e as Error).message) }
  }

  const runProbe = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    setProbeResult(null)
    try {
      setProbeResult(await fga.check(probe))
    } catch (e) { setErr((e as Error).message) }
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-xl font-semibold text-gray-900">Fine-Grained Authorization</h1>
        <p className="text-sm text-gray-500 mt-1">
          Browse and edit Zanzibar-style relation tuples: <span className="font-mono">object#relation@subject</span>.
        </p>
      </div>
      {err && <p className="text-sm text-red-500 bg-red-50 border border-red-100 rounded p-2">{err}</p>}

      {/* Add tuple */}
      <form onSubmit={add} className="bg-white border border-gray-200 rounded-lg p-5 space-y-4">
        <h2 className="text-sm font-semibold text-gray-700">Add tuple</h2>
        <div className="grid grid-cols-6 gap-3 items-end">
          <Field label="Object type" className="col-span-1">
            <input className={inp} required list="fga-types" value={draft.object_type}
              onChange={e => setDraft({ ...draft, object_type: e.target.value })} placeholder="document" />
          </Field>
          <Field label="Object ID" className="col-span-2">
            <input className={inp} required value={draft.object_id}
              onChange={e => setDraft({ ...draft, object_id: e.target.value })} placeholder="d-123" />
          </Field>
          <Field label="Relation" className="col-span-1">
            <input className={inp} required list="fga-relations" value={draft.relation}
              onChange={e => setDraft({ ...draft, relation: e.target.value })} placeholder="editor" />
            <datalist id="fga-relations">
              {relationsFor(draft.object_type).map(r => <option key={r} value={r} />)}
            </datalist>
          </Field>
          <Field label="Subject type" className="col-span-1">
            <select className={inp} value={draft.subject_type}
              onChange={e => setDraft({ ...draft, subject_type: e.target.value })}>
              <option value="user">user</option>
              <option value="group">group</option>
              <option value="service_account">service_account</option>
            </select>
          </Field>
          <Field label="Subject ID" className="col-span-1">
            <input className={inp} required value={draft.subject_id}
              onChange={e => setDraft({ ...draft, subject_id: e.target.value })} placeholder="alice-uuid" />
          </Field>
        </div>
        {draft.subject_type === 'group' && (
          <Field label="Subject relation" className="max-w-xs">
            <input className={inp} value={draft.subject_relation ?? ''}
              onChange={e => setDraft({ ...draft, subject_relation: e.target.value })} placeholder="member" />
            <p className="text-xs text-gray-400 mt-1">e.g. <span className="font-mono">group:sales#member</span> grants all members.</p>
          </Field>
        )}
        <button disabled={saving} type="submit"
          className="px-4 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700 disabled:opacity-40">
          {saving ? 'Adding…' : 'Add tuple'}
        </button>
      </form>

      {/* Check access */}
      <form onSubmit={runProbe} className="bg-white border border-gray-200 rounded-lg p-5 space-y-4">
        <h2 className="text-sm font-semibold text-gray-700">Check access</h2>
        <p className="text-xs text-gray-500">Evaluates inheritance and group expansion — not just direct tuples.</p>
        <div className="grid grid-cols-6 gap-3 items-end">
          <Field label="Object type" className="col-span-1">
            <input className={inp} required list="fga-types" value={probe.object_type}
              onChange={e => setProbe({ ...probe, object_type: e.target.value })} />
          </Field>
          <Field label="Object ID" className="col-span-2">
            <input className={inp} required value={probe.object_id}
              onChange={e => setProbe({ ...probe, object_id: e.target.value })} />
          </Field>
          <Field label="Relation" className="col-span-1">
            <input className={inp} required value={probe.relation}
              onChange={e => setProbe({ ...probe, relation: e.target.value })} />
          </Field>
          <Field label="Subject type" className="col-span-1">
            <select className={inp} value={probe.subject_type}
              onChange={e => setProbe({ ...probe, subject_type: e.target.value })}>
              <option value="user">user</option>
              <option value="service_account">service_account</option>
            </select>
          </Field>
          <Field label="Subject ID" className="col-span-1">
            <input className={inp} required value={probe.subject_id}
              onChange={e => setProbe({ ...probe, subject_id: e.target.value })} />
          </Field>
        </div>
        <div className="flex items-center gap-3">
          <button type="submit"
            className="px-4 py-1.5 bg-gray-800 text-white text-sm rounded hover:bg-gray-900">Check</button>
          {probeResult !== null && (
            <span className={`text-sm font-medium px-2 py-1 rounded ${probeResult ? 'bg-green-50 text-green-700' : 'bg-red-50 text-red-600'}`}>
              {probeResult ? 'Allowed ✓' : 'Denied ✗'}
            </span>
          )}
        </div>
      </form>

      {/* Filter + tuple list */}
      <div className="bg-white border border-gray-200 rounded-lg overflow-hidden">
        <div className="flex flex-wrap gap-2 items-end p-4 border-b border-gray-100 bg-gray-50">
          <input className={`${inp} w-32`} list="fga-types" placeholder="object type"
            value={filter.object_type ?? ''} onChange={e => setFilter({ ...filter, object_type: e.target.value })} />
          <input className={`${inp} w-32`} placeholder="object id"
            value={filter.object_id ?? ''} onChange={e => setFilter({ ...filter, object_id: e.target.value })} />
          <input className={`${inp} w-32`} placeholder="relation"
            value={filter.relation ?? ''} onChange={e => setFilter({ ...filter, relation: e.target.value })} />
          <input className={`${inp} w-40`} placeholder="subject id"
            value={filter.subject_id ?? ''} onChange={e => setFilter({ ...filter, subject_id: e.target.value })} />
          <button onClick={() => load()}
            className="px-3 py-1.5 bg-blue-600 text-white text-sm rounded hover:bg-blue-700">Filter</button>
          <button onClick={() => { setFilter({}); load({}) }}
            className="px-3 py-1.5 text-gray-500 text-sm hover:text-gray-700">Clear</button>
        </div>
        {rows.length === 0 ? (
          <p className="text-sm text-gray-400 p-5">No tuples match.</p>
        ) : (
          <table className="w-full text-sm">
            <thead className="bg-gray-50 border-b border-gray-200">
              <tr>
                {['Object', 'Relation', 'Subject', ''].map(h => (
                  <th key={h} className="px-4 py-2 text-left text-xs font-medium text-gray-500">{h}</th>
                ))}
              </tr>
            </thead>
            <tbody className="divide-y divide-gray-100">
              {rows.map(t => (
                <tr key={tupleKey(t)} className="hover:bg-gray-50">
                  <td className="px-4 py-2 font-mono text-xs">{t.object_type}:{t.object_id}</td>
                  <td className="px-4 py-2">
                    <span className="text-xs bg-blue-50 text-blue-700 px-1.5 py-0.5 rounded font-mono">{t.relation}</span>
                  </td>
                  <td className="px-4 py-2 font-mono text-xs">
                    {t.subject_type}:{t.subject_id}{t.subject_relation ? <span className="text-gray-400">#{t.subject_relation}</span> : null}
                  </td>
                  <td className="px-4 py-2 text-right">
                    <button onClick={() => del(t)} className="text-xs text-red-500 hover:text-red-700">Delete</button>
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </div>

      {/* Authorization model */}
      {model && (
        <div className="bg-gray-50 border border-gray-200 rounded-lg p-4 text-xs text-gray-600 space-y-3">
          <p className="font-medium text-gray-700">Authorization model</p>
          <div className="grid grid-cols-2 gap-3">
            {Object.entries(model.types).map(([type, td]) => (
              <div key={type} className="bg-white border border-gray-200 rounded p-3">
                <p className="font-mono font-semibold text-gray-800">{type}</p>
                <p className="mt-1">relations: {td.relations.map(r => <span key={r} className="font-mono bg-gray-100 px-1 rounded mr-1">{r}</span>)}</p>
                {td.implies && Object.entries(td.implies).map(([rel, kids]) => (
                  <p key={rel} className="mt-0.5 font-mono text-gray-500">{rel} → {kids.join(', ')}</p>
                ))}
              </div>
            ))}
          </div>
        </div>
      )}

      <datalist id="fga-types">{allTypes.map(t => <option key={t} value={t} />)}</datalist>
    </div>
  )
}

function Field({ label, className = '', children }: { label: string; className?: string; children: React.ReactNode }) {
  return (
    <div className={className}>
      <label className="text-xs text-gray-500 block mb-1">{label}</label>
      {children}
    </div>
  )
}
