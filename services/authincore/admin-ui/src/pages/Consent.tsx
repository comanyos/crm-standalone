import { useEffect, useState } from 'react'

const API = import.meta.env.VITE_API_URL ?? ''

interface ConsentRequest {
  id: string
  client_id: string
  client_name: string
  scopes: string[]
  redirect_uri: string
  state: string
}

export default function Consent() {
  const [req, setReq] = useState<ConsentRequest | null>(null)
  const [error, setError] = useState('')
  const consentId = new URLSearchParams(globalThis.location.search).get('consent_id')

  useEffect(() => {
    if (!consentId) { setError('No consent_id'); return }
    fetch(API + `/oauth2/consent?consent_id=${consentId}`, { credentials: 'include', headers: { Accept: 'application/json' } })
      .then(r => { if (!r.ok) throw new Error('unauthorized'); return r.json() })
      .then(setReq)
      .catch(e => setError(e.message))
  }, [consentId])

  async function decide(grant: boolean) {
    if (!req) return
    try {
      const res = await fetch(API + '/oauth2/consent', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        credentials: 'include',
        body: JSON.stringify({ consent_id: req.id, grant, remember: true, scopes: req.scopes }),
      })
      if (!res.ok) { setError(await res.text()); return }
      // After consent, authorize redirects back — follow to the callback
      const params = new URLSearchParams(globalThis.location.search)
      const authParams = new URLSearchParams()
      authParams.set('consent_id', req.id)
      globalThis.location.href = API + '/oauth2/authorize?' + authParams.toString()
    } catch (e: any) {
      setError(e.message)
    }
  }

  // Auto-approve if no error — seamless for first-party clients
  useEffect(() => {
    if (req) decide(true)
  }, [req])

  if (error) return (
    <div className="min-h-screen flex items-center justify-center bg-gray-50">
      <div className="bg-white p-8 rounded-2xl shadow border border-gray-100 max-w-sm w-full">
        <p className="text-red-600 text-sm">{error}</p>
        <a href="/login" className="mt-4 block text-center text-indigo-600 text-sm">Back to login</a>
      </div>
    </div>
  )

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-50">
      <div className="bg-white p-8 rounded-2xl shadow border border-gray-100 max-w-sm w-full text-center">
        <p className="text-gray-500 text-sm">Completing sign in…</p>
      </div>
    </div>
  )
}
