import { describe, expect, it } from 'vitest'
import { askedPasswords, passwordLabel } from './jobs'

describe('launch passwords', () => {
  it('asks for the machine credential inputs set to ASK', () => {
    expect(askedPasswords({ username: 'u', password: 'ASK', become_password: 'ASK' })).toEqual(['ssh_password', 'become_password'])
    expect(askedPasswords({ ssh_key_unlock: 'ASK' })).toEqual(['ssh_key_unlock'])
    expect(askedPasswords({ password: '$encrypted$' })).toEqual([])
    expect(askedPasswords(null)).toEqual([])
  })
  it('names them', () => {
    expect(passwordLabel('ssh_password')).toBe('SSH password')
    expect(passwordLabel('vault_password.prod')).toBe('Vault password (prod)')
  })
})
