import { describe, expect, it } from 'vitest'
import { ssoLogins } from './sso'

describe('single sign-on buttons', () => {
  it('lists OIDC, then SAML identity providers', () => {
    const logins = ssoLogins(
      {
        'saml:okta': { login_url: '/sso/login/saml/?idp=okta', name: 'okta' },
        oidc: { login_url: '/sso/login/oidc/' },
        'saml:adfs': { login_url: '/sso/login/saml/?idp=adfs', name: 'adfs' },
      },
      '/jobs?page=2',
    )
    expect(logins.map((l) => l.label)).toEqual(['Continue with single sign-on', 'Continue with adfs (SAML)', 'Continue with okta (SAML)'])
    expect(logins[0].href).toBe('/sso/login/oidc/?next=%2Fjobs%3Fpage%3D2')
    expect(logins[1].href).toBe('/sso/login/saml/?idp=adfs&next=%2Fjobs%3Fpage%3D2')
  })
  it('is empty without backends', () => {
    expect(ssoLogins(undefined, '/')).toEqual([])
    expect(ssoLogins({}, '/')).toEqual([])
  })
})
