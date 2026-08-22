import { useState } from 'react'
import { useParams } from 'react-router-dom'

export default function InvitationAccept() {
  const { token } = useParams<{ token: string }>()
  const [email, setEmail] = useState('')
  const [err, setErr] = useState('')
  const [done, setDone] = useState(false)
  const [loading, setLoading] = useState(false)

  const inp = 'w-full border border-gray-200 rounded-lg px-3 py-2 text-sm focus:outline-none focus:ring-2 focus:ring-blue-500'

  const accept = async (e: React.FormEvent) => {
    e.preventDefault()
    if (!token) return
    setErr('')
    setLoading(true)
    try {
      const res = await fetch(`/invitations/${token}/accept`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email }),
      })
      if (res.status === 404) throw new Error('Invitation not found or already used.')
      if (res.status === 410) throw new Error('This invitation has expired.')
      if (res.status === 409) throw new Error('Invitation was already accepted.')
      if (!res.ok) {
        const text = await res.text()
        throw new Error(text || `Error ${res.status}`)
      }
      setDone(true)
    } catch (e: unknown) {
      setErr(e instanceof Error ? e.message : 'Failed to accept invitation')
    } finally {
      setLoading(false)
    }
  }

  return (
    <div className="min-h-screen flex items-center justify-center bg-gray-50">
      <div className="bg-white border border-gray-200 rounded-xl p-8 w-full max-w-sm shadow-sm">
        <h1 className="text-xl font-semibold text-gray-900 mb-1">Accept Invitation</h1>

        {done ? (
          <div className="text-center py-6">
            <div className="text-4xl mb-3">🎉</div>
            <p className="text-sm text-gray-700 font-medium">Invitation accepted!</p>
            <p className="text-xs text-gray-400 mt-1">Your account is ready. Sign in to get started.</p>
            <a href="/login"
              className="mt-5 inline-block w-full text-center bg-blue-600 text-white rounded-lg py-2 text-sm font-medium hover:bg-blue-700">
              Sign in
            </a>
          </div>
        ) : (
          <>
            <p className="text-sm text-gray-500 mb-6">
              Confirm your email address to accept this invitation and activate your account.
            </p>

            {err && <p className="text-sm text-red-500 mb-4 bg-red-50 border border-red-100 rounded p-2">{err}</p>}

            <form onSubmit={accept} className="space-y-4">
              <div>
                <label className="text-xs font-medium text-gray-600 block mb-1">Your email address</label>
                <input type="email" required className={inp} value={email}
                  onChange={e => setEmail(e.target.value)}
                  placeholder="Enter the email you were invited with" />
              </div>
              <button type="submit" disabled={loading}
                className="w-full bg-blue-600 text-white rounded-lg py-2 text-sm font-medium hover:bg-blue-700 disabled:opacity-40">
                {loading ? 'Accepting…' : 'Accept invitation'}
              </button>
            </form>
          </>
        )}
      </div>
    </div>
  )
}
