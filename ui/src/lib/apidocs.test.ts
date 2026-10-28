import { describe, expect, it } from 'vitest'
import { withSession } from './apidocs'

describe('withSession', () => {
  it('adds the CSRF token to requests that change something', () => {
    const r = withSession({ url: '/api/v2/teams/', method: 'post', headers: { Accept: 'application/json' } }, 'tok')
    expect(r.headers['X-CSRFToken']).toBe('tok')
    expect(r.headers.Accept).toBe('application/json')
    expect(r.credentials).toBe('same-origin')
  })
  it('leaves reads as they are', () => {
    for (const method of ['GET', 'head', 'OPTIONS', undefined]) {
      expect(withSession({ url: '/api/v2/', method, headers: {} }, 'tok').headers['X-CSRFToken']).toBeUndefined()
    }
  })
  it('sends no token when there is none', () => {
    expect(withSession({ url: '/api/v2/teams/', method: 'DELETE', headers: {} }, undefined).headers).toEqual({})
  })
})
