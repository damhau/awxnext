import { tr } from './i18n'
// The single sign-on buttons of the login page, from /api/v2/auth/: OIDC,
// the OAuth2 logins (GitHub's, by name), then each SAML identity provider
// ("saml:<name>").

export interface Backend {
  login_url: string
  name?: string
}

export interface SSOLogin {
  key: string
  label: string
  href: string
}

/** Adds next to a login URL that may already have a query. */
function withNext(url: string, next: string): string {
  return `${url}${url.includes('?') ? '&' : '?'}next=${encodeURIComponent(next)}`
}

export function ssoLogins(backends: Record<string, Backend> | undefined, next: string): SSOLogin[] {
  if (!backends) return []
  const out: SSOLogin[] = []
  if (backends.oidc) out.push({ key: 'oidc', label: tr('Continue with single sign-on'), href: withNext(backends.oidc.login_url, next) })
  const keys = Object.keys(backends).sort()
  for (const key of keys) {
    if (key === 'oidc' || key.startsWith('saml:')) continue
    out.push({ key, label: tr('Continue with {v}', { v: backends[key].name ?? key }), href: withNext(backends[key].login_url, next) })
  }
  for (const key of keys) {
    if (!key.startsWith('saml:')) continue
    const name = backends[key].name ?? key.slice(5)
    out.push({ key, label: tr('Continue with {name} (SAML)', { name }), href: withNext(backends[key].login_url, next) })
  }
  return out
}
