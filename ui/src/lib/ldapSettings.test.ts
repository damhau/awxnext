import { describe, expect, it } from 'vitest'
import { ldapPrefix, ldapServer, ldapServerOn } from './ldapSettings'

describe('LDAP servers', () => {
  it('groups keys by server', () => {
    expect(ldapServer('AUTH_LDAP_SERVER_URI')).toBe('Default server')
    expect(ldapServer('AUTH_LDAP_3_BIND_DN')).toBe('Server 3')
    expect(ldapPrefix('AUTH_LDAP_5_USER_SEARCH')).toBe('AUTH_LDAP_5_')
    expect(ldapPrefix('AUTH_LDAP_USER_SEARCH')).toBe('AUTH_LDAP_')
  })
  it('knows which servers are on', () => {
    const values = { AUTH_LDAP_SERVER_URI: 'ldaps://ldap.example.com', AUTH_LDAP_2_SERVER_URI: ' ' }
    expect(ldapServerOn(values, 'Default server')).toBe(true)
    expect(ldapServerOn(values, 'Server 2')).toBe(false)
    expect(ldapServerOn(values, 'Server 4')).toBe(false)
  })
})
