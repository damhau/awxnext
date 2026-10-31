import { describe, expect, it } from 'vitest'
import { samlIdps, stepLabel, stepState, testsKey, unsavedFor } from './connectionTest'

describe('connection tests', () => {
  it('send the settings of their login only', () => {
    expect(testsKey('ldap', 'AUTH_LDAP_SERVER_URI', 'ldap')).toBe(true)
    expect(testsKey('ldap', 'AUTH_LDAP_1_SERVER_URI', 'ldap')).toBe(false)
    expect(testsKey('ldap3', 'AUTH_LDAP_3_BIND_PASSWORD', 'ldap')).toBe(true)
    expect(testsKey('ldap3', 'AUTH_LDAP_BIND_PASSWORD', 'ldap')).toBe(false)
    expect(testsKey('ldap6', 'AUTH_LDAP_SERVER_URI', 'ldap')).toBe(false)
    expect(testsKey('oidc', 'OIDC_SCOPES', 'oidc')).toBe(true)
    expect(testsKey('oidc', 'SOCIAL_AUTH_ORGANIZATION_MAP', 'authentication')).toBe(false)
    expect(testsKey('saml', 'SOCIAL_AUTH_SAML_ENABLED_IDPS', 'saml')).toBe(true)
  })

  it('send what is changed and not saved, secrets as the form has them', () => {
    const categories = { AUTH_LDAP_2_SERVER_URI: 'ldap', AUTH_LDAP_2_BIND_PASSWORD: 'ldap', AUTH_LDAP_2_START_TLS: 'ldap', AUTH_LDAP_SERVER_URI: 'ldap', TOWER_URL_BASE: 'system' }
    const saved = { AUTH_LDAP_2_SERVER_URI: '', AUTH_LDAP_2_BIND_PASSWORD: '$encrypted$', AUTH_LDAP_2_START_TLS: false, AUTH_LDAP_SERVER_URI: '', TOWER_URL_BASE: 'a' }
    const values = { ...saved, AUTH_LDAP_2_SERVER_URI: 'ldap://dc', AUTH_LDAP_2_BIND_PASSWORD: 'new', AUTH_LDAP_SERVER_URI: 'ldap://other', TOWER_URL_BASE: 'b' }
    expect(unsavedFor('ldap2', categories, values, saved)).toEqual({ AUTH_LDAP_2_SERVER_URI: 'ldap://dc', AUTH_LDAP_2_BIND_PASSWORD: 'new' })
    expect(unsavedFor('ldap2', categories, saved, saved)).toEqual({})
  })

  it('name each step and show its status', () => {
    expect(stepLabel('connect')).toBe('Connection')
    expect(stepLabel('sso')).toBe('Sign-in URL')
    expect(stepLabel('something-new')).toBe('something-new')
    expect(stepState.warning).toBe('changed')
  })

  it("list SAML's identity providers, and whether metadata is at their entity ID", () => {
    expect(
      samlIdps({
        okta: { entity_id: 'http://www.okta.com/exk1', url: 'https://x' },
        corp: { entity_id: 'urn:corp:idp' },
        broken: 'x',
      }),
    ).toEqual([
      { name: 'broken', entityId: '', isUrl: false },
      { name: 'corp', entityId: 'urn:corp:idp', isUrl: false },
      { name: 'okta', entityId: 'http://www.okta.com/exk1', isUrl: true },
    ])
    expect(samlIdps(null)).toEqual([])
    expect(samlIdps([])).toEqual([])
  })
})
