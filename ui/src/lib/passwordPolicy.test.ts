import { describe, expect, it } from 'vitest'
import { checkPassword, describeRules, passwordRules } from './passwordPolicy'

const rules = passwordRules({ LOCAL_PASSWORD_MIN_LENGTH: 10, LOCAL_PASSWORD_MIN_DIGITS: 2, LOCAL_PASSWORD_MIN_UPPER: 1, LOCAL_PASSWORD_MIN_SPECIAL: 1 })

describe('password rules', () => {
  it('reads the settings', () => {
    expect(rules).toEqual({ length: 10, digits: 2, upper: 1, special: 1 })
    expect(passwordRules(undefined)).toEqual({ length: 0, digits: 0, upper: 0, special: 0 })
  })
  it('describes them', () => {
    expect(describeRules(rules)).toBe('At least 10 characters, 2 digits, 1 uppercase and 1 special character.')
    expect(describeRules(passwordRules({ LOCAL_PASSWORD_MIN_LENGTH: 8 }))).toBe('At least 8 characters.')
    expect(describeRules(passwordRules({}))).toBe('')
  })
  it('checks in the server order, with its words', () => {
    expect(checkPassword('a', rules)).toBe('Password must be at least 10 characters long.')
    expect(checkPassword('abcdefghij1', rules)).toBe('Password must contain at least 2 digits.')
    expect(checkPassword('abcdefghij12', rules)).toBe('Password must contain at least 1 uppercase characters.')
    expect(checkPassword('Abcdefghij12', rules)).toBe('Password must contain at least 1 special characters.')
    expect(checkPassword('Abcdefgh ij12', rules)).toBeNull()
    expect(checkPassword('Ünïcödé1２!x', rules)).toBeNull()
  })
})
