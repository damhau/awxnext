import { describe, expect, it } from 'vitest'
import { connectorCell, connectorStatus, parseArgs, parseFilters, presetArgs, restartText, runStatus, sourceError, statsText, yamlText } from './eventConnectors'

describe('event connectors', () => {
  it('labels statuses, and a run lost with its node', () => {
    expect(connectorStatus('running')).toBe('Running')
    expect(connectorCell('running')).toBe('running')
    expect(connectorCell('restarting')).toBe('changed')
    expect(connectorCell('failed')).toBe('failed')
    expect(connectorCell('unknown')).toBe('empty')
    expect(runStatus('lost')).toEqual(['Node lost', 'unreachable'])
    expect(runStatus('completed')).toEqual(['Completed', 'ok'])
  })

  it('says how it restarts', () => {
    expect(restartText('never', 5, 0)).toBe('Never restarts: it stays completed or failed.')
    expect(restartText('on-failure', 5, 0)).toBe('Restarts when it fails, after 5 s, the delay doubling each time, with no limit.')
    expect(restartText('always', 2, 1)).toBe('Restarts whenever it stops, after 2 s, the delay doubling each time, at most 1 time in a row.')
  })

  it('sums up what the runner reported', () => {
    expect(statsText(null)).toBe('')
    expect(statsText({})).toBe('')
    expect(statsText({ sent: 0 })).toBe('0 sent')
    expect(statsText({ sent: 1200, buffered: 30, dropped: 0, rejected: 2, unreachable: true })).toBe('1,200 sent · 30 buffered · 2 rejected · awxnext unreachable')
  })

  it('checks a plugin’s full name', () => {
    expect(sourceError('')).toBeNull()
    expect(sourceError('ansible.eda.kafka')).toBeNull()
    expect(sourceError('kafka')).toMatch(/full name/)
    expect(sourceError('ansible.eda.kafka; rm')).toMatch(/full name/)
  })

  it('parses arguments as a mapping', () => {
    expect(parseArgs('')).toEqual({ args: {} })
    expect(parseArgs('host: kafka\nport: 9092')).toEqual({ args: { host: 'kafka', port: 9092 } })
    expect(parseArgs('{"limit": 3}')).toEqual({ args: { limit: 3 } })
    expect(parseArgs('- a').error).toMatch(/mapping/)
    expect(parseArgs('a: [').error).toBeTruthy()
  })

  it('parses filters as a rulebook writes them', () => {
    expect(parseFilters('')).toEqual({ filters: [] })
    expect(parseFilters('- ansible.eda.json_filter:\n    exclude_keys: [password]\n- ansible.eda.dashes_to_underscores:')).toEqual({
      filters: [{ 'ansible.eda.json_filter': { exclude_keys: ['password'] } }, { 'ansible.eda.dashes_to_underscores': null }],
    })
    expect(parseFilters('ansible.eda.json_filter: {}').error).toMatch(/list/)
    expect(parseFilters('- json_filter: {}').error).toMatch(/Filter 1: give/)
    expect(parseFilters('- {a.b.c: {}, d.e.f: {}}').error).toMatch(/Filter 1 names one plugin/)
    expect(parseFilters('- a.b.c: [1]').error).toMatch(/mapping/)
  })

  it('writes YAML for the editors, nothing for nothing', () => {
    expect(yamlText({})).toBe('')
    expect(yamlText([])).toBe('')
    expect(yamlText(null)).toBe('')
    expect(yamlText({ limit: 3 })).toBe('limit: 3\n')
    expect(parseArgs(yamlText(presetArgs('ansible.eda.kafka'))).args).toEqual(presetArgs('ansible.eda.kafka'))
  })
})
