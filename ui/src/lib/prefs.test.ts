import { describe, expect, it } from 'vitest'
import { pendingUploads } from './prefs'

describe('preferences on the server', () => {
  const local: Record<string, unknown> = { 'starred-templates': ['job:4'], 'template-views': [{ name: 'prod', q: 'org:prod' }], 'nav-collapsed': true }
  it('uploads what the browser kept and the server has not got', () => {
    expect(pendingUploads({}, (k) => local[k])).toEqual({ 'starred-templates': ['job:4'], 'template-views': [{ name: 'prod', q: 'org:prod' }] })
  })
  it('keeps what the server has, even empty, and the browser-only settings', () => {
    expect(pendingUploads({ 'starred-templates': [], 'template-views': [] }, (k) => local[k])).toEqual({})
    expect(pendingUploads({ 'starred-templates': [] }, (k) => local[k])).toEqual({ 'template-views': [{ name: 'prod', q: 'org:prod' }] })
  })
  it('uploads nothing a browser never kept', () => {
    expect(pendingUploads({}, () => undefined)).toEqual({})
  })
})
