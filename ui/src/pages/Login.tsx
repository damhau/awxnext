import { useQuery } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { api } from '../api/client'

export function Login({ onDone, expired }: { onDone: () => void; expired?: boolean }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const backends = useQuery({ queryKey: ['auth-backends'], queryFn: () => api.get<{ oidc?: { login_url: string } }>('auth/'), retry: false })
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api.post('/api/login/', { username, password })
      onDone()
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Sign in failed')
    } finally {
      setBusy(false)
    }
  }
  const oidc = backends.data?.oidc
  const next = encodeURIComponent(window.location.pathname + window.location.search)
  return (
    <main className="login">
      <form className="login-card" onSubmit={submit} aria-labelledby="login-title">
        <div className="brand">
          <span className="brand-mark" aria-hidden="true" />
          <span>
            awx<span className="dim">next</span>
          </span>
        </div>
        <div>
          <h1 id="login-title" style={{ fontSize: 18 }}>
            {expired ? 'Your session ended' : 'Sign in'}
          </h1>
          {expired && <p className="subtitle">Sign in again to continue where you were.</p>}
        </div>
        {oidc && (
          <>
            <a className="btn primary lg block" href={`${oidc.login_url}?next=${next}`}>
              Continue with single sign-on
            </a>
            <div className="divider">or with a local account</div>
          </>
        )}
        <div className="field">
          <label htmlFor="u">Username</label>
          <input id="u" className="input" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
        </div>
        <div className="field">
          <label htmlFor="p">Password</label>
          <input id="p" className="input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </div>
        {error && (
          <div className="form-error" role="alert">
            {error}
          </div>
        )}
        <button className={`btn ${oidc ? '' : 'primary'} lg block`} disabled={busy || !username || !password}>
          {busy ? 'Signing in…' : 'Sign in'}
        </button>
      </form>
    </main>
  )
}
