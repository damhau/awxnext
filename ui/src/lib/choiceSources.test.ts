import { describe, expect, it } from 'vitest'
import { answerNames, parseParameters, refreshState, sourceOrigin } from './choiceSources'

describe('choice sources', () => {
  it('says where a list comes from', () => {
    expect(sourceOrigin({ kind: 'http', url: 'https://netbox.example.com:8443/api/ipam/vlans/' })).toBe('netbox.example.com:8443')
    expect(sourceOrigin({ kind: 'http', url: 'not a url' })).toBe('not a url')
    expect(sourceOrigin({ kind: 'job', url: '', summary_fields: { job_template: { name: 'Find images' } } })).toBe('Published by Find images')
  })
  it('tells a job source’s refresh state', () => {
    expect(refreshState({ kind: 'http', last_refresh: null, last_error: '' })).toBeNull()
    expect(refreshState({ kind: 'job', last_refresh: null, last_error: '' })).toBe('empty')
    expect(refreshState({ kind: 'job', last_refresh: '2026-11-18T10:00:00Z', last_error: '' })).toBe('ok')
    expect(refreshState({ kind: 'job', last_refresh: '2026-11-18T10:00:00Z', last_error: 'Job 8 failed' })).toBe('failed')
  })
  it('reads parameters as a mapping of text', () => {
    expect(parseParameters('')).toEqual({ value: {}, error: null })
    expect(parseParameters('site: "{{ answers.site }}"\nlimit: 50')).toEqual({ value: { site: '{{ answers.site }}', limit: '50' }, error: null })
    expect(parseParameters('{"q": "x"}').value).toEqual({ q: 'x' })
    expect(parseParameters('- a').error).toBe('Parameters must be a mapping (name: value).')
    expect(parseParameters('site: {a: 1}').error).toBe('The value of site must be text.')
    expect(parseParameters('site: [').error).not.toBeNull()
  })
  it('finds the answers a source reads', () => {
    expect(answerNames({ site: '{{ answers.datacenter }}', owner: '{{ user.username }}' }, 'answers.env != "prod" || item.tenant == answers.tenant')).toEqual(['datacenter', 'env', 'tenant'])
    expect(answerNames({}, '')).toEqual([])
  })
})
