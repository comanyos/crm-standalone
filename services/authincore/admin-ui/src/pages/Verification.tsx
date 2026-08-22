import { useState } from 'react'

export default function Verification() {
  const [email, setEmail] = useState('')
  const [code, setCode] = useState('')
  const [flowID, setFlowID] = useState('')
  const [step, setStep] = useState<'email' | 'code' | 'done'>('email')
  const [err, setErr] = useState('')
  const [loading, setLoading] = useState(false)

  const inp = 'w-full border border-gray-200 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500'

  const initFlow = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    setLoading(true)
    try {
      const flowRes = await fetch('/self-service/verification', { credentials: 'include' })
      if (!flowRes.ok) throw new Error(`Failed to init flow: ${flowRes.status}`)
      const flow = await flowRes.json()
      const fid = flow.id ?? flow.ID
      setFlowID(fid)

      const res = await fetch(`/self-service/verification?flow=${fid}`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ method: 'link', body: { email } }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        throw new Error(data.error ?? `Error ${res.status}`)
      }
      setStep('code')
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : 'Failed to send verification email')
    } finally {
      setLoading(false)
    }
  }

  const submitCode = async (e: React.FormEvent) => {
    e.preventDefault()
    setErr('')
    setLoading(true)
    try {
      const res = await fetch(`/self-service/verification?flow=${flowID}`, {
        method: 'POST',
        credentials: 'include',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ method: 'link', body: { code } }),
      })
      if (!res.ok) {
        const data = await res.json().catch(() => ({}))
        throw new Error(data.error ?? `Error ${res.status}`)
      }
      setStep('done')
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : 'Verification failed')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-50">
      <div className="bg-white border border-gray-200 rounded-xl p-8 w-full max-w-sm shadow-sm">
        <h1 className="text-xl font-semibold text-gray-900 mb-1">Verify your email</h1>

        {step === 'done' ? (
          <div className="text-center py-6">
            <div className="text-4xl mb-3">✓</div>
            <p className="text-sm text-gray-700 font-medium">Email verified!</p>
            <p className="text-xs text-gray-400 mt-1">Your email address has been verified successfully.</p>
            <a href="/" className="mt-4 inline-block text-sm text-blue-600 hover:underline">Go to dashboard</a>
          </div>
        ) : (
          <>
            <p className="text-sm text-gray-500 mb-6">
              {step === 'email'
                ? 'Enter your email address to receive a verification link.'
                : `We sent a verification code to ${email}. Enter it below.`}
            </p>

            {err && <p className="text-sm text-red-500 mb-4 bg-red-50 border border-red-100 rounded p-2">{err}</p>}

            {step === 'email' ? (
              <form onSubmit={initFlow} className="space-y-4">
                <div>
                  <label className="text-xs font-medium text-gray-600 block mb-1">Email</label>
                  <input type="email" required className={inp} value={email}
                    onChange={e => setEmail(e.target.value)} />
                </div>
                <button type="submit" disabled={loading}
                  className="w-full bg-blue-600 text-white rounded-lg py-2 text-sm font-medium hover:bg-blue-700 disabled:opacity-40">
                  {loading ? 'Sending…' : 'Send verification email'}
                </button>
              </form>
            ) : (
              <form onSubmit={submitCode} className="space-y-4">
                <div>
                  <label className="text-xs font-medium text-gray-600 block mb-1">Verification code</label>
                  <input type="text" required className={inp} value={code} autoFocus
                    onChange={e => setCode(e.target.value)} placeholder="Enter code from email" />
                </div>
                <button type="submit" disabled={loading}
                  className="w-full bg-blue-600 text-white rounded-lg py-2 text-sm font-medium hover:bg-blue-700 disabled:opacity-40">
                  {loading ? 'Verifying…' : 'Verify'}
                </button>
                <button type="button" onClick={() => { setStep('email'); setErr('') }}
                  className="w-full text-sm text-gray-500 hover:underline">
                  Use a different email
                </button>
              </form>
            )}
          </>
        )}
      </div>
    </div>
  )
}
