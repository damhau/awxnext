import { useQuery } from '@tanstack/react-query'
import { useEffect, useState, type FormEvent } from 'react'
import { api } from '../api/client'
import { BrandMark } from '../components/ui'
import { ssoLogins, type Backend } from '../lib/sso'
import { tr } from '../lib/i18n'

export function Login({ onDone, expired, replaced }: { onDone: () => void; expired?: boolean; replaced?: boolean }) {
  const [username, setUsername] = useState('')
  const [password, setPassword] = useState('')
  // A failed single sign-on comes back with ?error=.
  const [error, setError] = useState(() => new URLSearchParams(window.location.search).get('error') ?? '')
  const [busy, setBusy] = useState(false)
  const backends = useQuery({ queryKey: ['auth-backends'], queryFn: () => api.get<Record<string, Backend>>('auth/'), retry: false })
  // The API root carries the login page settings, readable before signing in.
  const root = useQuery({
    queryKey: ['api-root'],
    queryFn: () => api.get<{ custom_logo?: string; custom_login_info?: string; login_redirect_override?: string }>('/api/'),
    retry: false,
  })
  const redirect = root.data?.login_redirect_override
  const local = new URLSearchParams(window.location.search).get('login') === 'local'
  useEffect(() => {
    // LOGIN_REDIRECT_OVERRIDE sends users elsewhere to log in; ?login=local keeps this page.
    if (redirect && !local) window.location.replace(redirect)
  }, [redirect, local])
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError('')
    try {
      await api.post('/api/login/', { username, password })
      onDone()
    } catch (err) {
      setError(err instanceof Error ? err.message : tr('Sign in failed'))
    } finally {
      setBusy(false)
    }
  }
  const params = new URLSearchParams(window.location.search)
  params.delete('error')
  const query = params.toString()
  const sso = ssoLogins(backends.data, window.location.pathname === '/login' ? '/' : window.location.pathname + (query ? `?${query}` : ''))
  return (
    <main className="login">
      <form className="login-card" onSubmit={submit} aria-labelledby="login-title">
        <div className="brand">
          <BrandMark />
          <span>{tr('awxnext')}</span>
        </div>
        {root.data?.custom_logo && <img className="login-logo" src={root.data.custom_logo} alt="" />}
        <div>
          <h1 id="login-title" style={{ fontSize: 18 }}>
            {expired ? tr('Your session ended') : tr('Sign in')}
          </h1>
          {expired && (
            <p className="subtitle">
              {replaced ? tr('You signed in elsewhere, beyond the sessions allowed per user. ') : ''}{tr('Sign in again to continue where you were.')}
            </p>
          )}
        </div>
        {sso.length > 0 && (
          <>
            {sso.map((s, i) => (
              <a key={s.key} className={`btn ${i === 0 ? 'primary' : ''} lg block`} href={s.href}>
                {s.label}
              </a>
            ))}
            <div className="divider">{tr('or with a local account')}</div>
          </>
        )}
        <div className="field">
          <label htmlFor="u">{tr('Username')}</label>
          <input id="u" className="input" autoComplete="username" value={username} onChange={(e) => setUsername(e.target.value)} autoFocus />
        </div>
        <div className="field">
          <label htmlFor="p">{tr('Password')}</label>
          <input id="p" className="input" type="password" autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
        </div>
        {error && (
          <div className="form-error" role="alert">
            {error}
          </div>
        )}
        <button className={`btn ${sso.length ? '' : 'primary'} lg block`} disabled={busy || !username || !password}>
          {busy ? tr('Signing in…') : tr('Sign in')}
        </button>
        {root.data?.custom_login_info && <p className="login-info">{root.data.custom_login_info}</p>}
      </form>
    </main>
  )
}
