import { describe, expect, it } from 'vitest'
import {
  checkBody,
  completionsAt,
  draftOf,
  fieldErrors,
  inString,
  mergeSchemas,
  missParts,
  nodeAt,
  offsetAt,
  outcomeView,
  parseCelErrors,
  pathBefore,
  ruleBody,
  seenText,
  setupSnippets,
  splitMiss,
  splitSeconds,
  type StreamSchema,
} from './eventRules'

const schema: StreamSchema = {
  events: 3,
  event: {
    type: 'object',
    fields: {
      id: { type: 'string' },
      type: { type: 'string' },
      time: { type: 'timestamp' },
      data: {
        type: 'object',
        fields: {
          labels: { type: 'map', values: { type: 'string' } },
          'alert-name': { type: 'string' },
          alerts: { type: 'list', items: { type: 'object', fields: { status: { type: 'string' }, startsAt: { type: 'string' } } } },
          count: { type: 'int' },
        },
      },
    },
  },
}

const at = (text: string) => completionsAt(schema, text, text.length)
const labels = (text: string) => at(text).map((c) => c.label)

describe('CEL completion', () => {
  it('reads the path before the cursor', () => {
    expect(pathBefore('event.data.labels', 17)).toEqual(['data', 'labels'])
    expect(pathBefore('x && event.data["alert-name"]', 29)).toEqual(['data', 'alert-name'])
    expect(pathBefore('events[0].data.alerts[1]', 24)).toEqual(['data', 'alerts', '[]'])
    expect(pathBefore('foo.bar', 7)).toBeNull()
    expect(pathBefore('events.data', 11)).toBeNull()
  })
  it('finds the schema node of a path', () => {
    expect(nodeAt(schema.event, ['data', 'alerts', '[]', 'status'])?.type).toBe('string')
    expect(nodeAt(schema.event, ['data', 'labels', 'severity'])?.type).toBe('string')
    expect(nodeAt(schema.event, ['data', 'nothere', 'x'])).toBeNull()
  })
  it('lists an object’s fields after a dot, filtered by what is typed', () => {
    expect(labels('event.')).toEqual(['data', 'id', 'time', 'type'])
    expect(labels('event.t')).toEqual(['time', 'type'])
    expect(labels('event.data.alerts[0].')).toEqual(['startsAt', 'status'])
    expect(labels('has(event.data.c')).toEqual(['count'])
    expect(at('events[0].ty')[0]).toMatchObject({ label: 'type', insert: 'type', from: 10 })
  })
  it('reads a field that isn’t a name with brackets', () => {
    const c = at('event.data.al').find((x) => x.label === 'alert-name')
    expect(c).toMatchObject({ insert: '["alert-name"]', from: 'event.data'.length })
  })
  it('offers a value’s methods', () => {
    expect(labels('event.type.st')).toEqual(['startsWith('])
    expect(labels('event.data.alerts.ex')).toEqual(['exists(x, ', 'exists_one(x, '])
  })
  it('offers event and functions at the start of a name, not inside strings or numbers', () => {
    expect(labels('ev')).toEqual(['event', 'events'])
    expect(labels('a && ha')).toEqual(['has('])
    expect(labels('event.type == "ev')).toEqual([])
    expect(labels('x == 1.')).toEqual([])
    expect(labels('')).toEqual([])
    expect(inString(`"a\\"b" + 'c`)).toBe(true)
    expect(inString(`"a\\"b" + 'c'`)).toBe(false)
  })
  it('knows nothing without a schema', () => {
    expect(completionsAt(null, 'event.', 6)).toEqual([])
  })
  it('merges the schemas of a rule’s streams', () => {
    const other: StreamSchema = { events: 2, event: { type: 'object', fields: { data: { type: 'object', fields: { count: { type: 'number' }, labels: { type: 'string' }, extra: { type: 'bool' } } } } } }
    const m = mergeSchemas([schema, other])!
    const data = m.event.fields!.data.fields!
    expect(m.events).toBe(5)
    expect(data.count.type).toBe('number')
    expect(data.labels.type).toBe('dyn')
    expect(data.extra.type).toBe('bool')
    expect(data.alerts.type).toBe('list')
    expect(mergeSchemas([])).toBeNull()
  })
})

describe('CEL errors', () => {
  it('reads cel-go’s positions', () => {
    const msg = "ERROR: <input>:1:12: undefined field 'nope'\n | event.data.nope == 1\n | ...........^\nERROR: <input>:2:3: Syntax error: mismatched input"
    expect(parseCelErrors(msg)).toEqual([
      { line: 1, column: 12, message: "undefined field 'nope'" },
      { line: 2, column: 3, message: 'Syntax error: mismatched input' },
    ])
    expect(parseCelErrors('The expression must be true or false, not string.')).toEqual([{ message: 'The expression must be true or false, not string.' }])
    expect(parseCelErrors('')).toEqual([])
  })
  it('turns a line and column into an offset', () => {
    expect(offsetAt('abc\ndef', 2, 2)).toBe(5)
    expect(offsetAt('abc', 1, 1)).toBe(0)
    expect(offsetAt('abc', 1, 9)).toBe(3)
  })
  it('sends each variable’s error to its row', () => {
    expect(fieldErrors({ filter: ['bad'], extra_vars: ['host: ERROR: <input>:1:1: x\n | y', 'Expected an object.'], 'extra_vars.port': 'no' })).toEqual({
      filter: 'bad',
      'extra_vars.host': 'ERROR: <input>:1:1: x\n | y',
      extra_vars: 'Expected an object.',
      'extra_vars.port': 'no',
    })
  })
})

describe('decisions', () => {
  it('splits a filter around the part that failed, in characters', () => {
    expect(splitMiss('a && é == "x"', 5, 13)).toEqual(['a && ', 'é == "x"', ''])
    expect(splitMiss('abc', -1, -1)).toBeNull()
    expect(splitMiss('abc', 1, 9)).toBeNull()
  })
  it('marks the failed part only where the filter still has it', () => {
    const f = 'event.type == "a" && event.data.n > 3'
    expect(missParts(f, { expression: 'event.data.n > 3', start: 21, end: 37 })).toEqual(['event.type == "a" && ', 'event.data.n > 3', ''])
    expect(missParts(f, { expression: 'event.data.n>3', start: 21, end: 37 })).not.toBeNull()
    expect(missParts(f, { expression: 'event.data.n…', start: 21, end: 37 })).not.toBeNull()
    expect(missParts(f, { expression: 'event.subject == "x"', start: 21, end: 37 })).toBeNull()
    expect(missParts(f, { expression: 'event.data.n > 3' })).toBeNull()
    expect(missParts(f, undefined)).toBeNull()
  })
  it('writes the values the failed part saw', () => {
    expect(seenText({ expression: 'event.data.n', value: 3 })).toBe('event.data.n = 3')
    expect(seenText({ expression: 'event.data', summary: 'is an object of 12 keys' })).toBe('event.data is an object of 12 keys')
    expect(seenText({ expression: 'event.data.x', error: 'no such key' })).toBe('event.data.x: no such key')
  })
  it('shows each outcome as a cell', () => {
    expect(outcomeView('launched')).toMatchObject({ cell: 'ok', kind: 'acted' })
    expect(outcomeView('shadow')).toMatchObject({ cell: 'changed', kind: 'would' })
    expect(outcomeView('held')).toMatchObject({ cell: 'canceled', kind: 'held' })
    expect(outcomeView('not_matched')).toMatchObject({ cell: 'none', kind: 'missed' })
    expect(outcomeView('refused').kind).toBe('failed')
    expect(outcomeView('something_new')).toMatchObject({ cell: 'empty', label: 'something_new' })
  })
})

describe('the rule draft', () => {
  it('keeps only the chosen action’s fields', () => {
    const d = { ...draftOf({ action: 'notify', unified_job_template: 4, extra_vars: { host: 'event.subject' }, notification_template: 2, message: '"hi"', throttle: '', throttle_window: 60 }) }
    const b = ruleBody(d)
    expect(b).toMatchObject({ action: 'notify', unified_job_template: null, extra_vars: {}, notification_template: 2, message: '"hi"', throttle_window: 0, host_lock: false })
    d.action = 'launch'
    d.vars = [['host', 'event.subject'], [' ', 'x'], ['bad name', 'y']]
    expect(ruleBody(d)).toMatchObject({ unified_job_template: 4, extra_vars: { host: 'event.subject', 'bad name': 'y' }, notification_template: null })
    expect(checkBody(d, 5)).toMatchObject({ extra_vars: { host: 'event.subject' }, events: 5 })
  })
  it('starts new rules in shadow mode, with a window ready for a throttle', () => {
    expect(draftOf()).toMatchObject({ mode: 'shadow', enabled: true, throttle: '', throttle_window: 3600, action: 'launch', vars: [] })
  })
  it('writes windows in their largest whole unit', () => {
    expect(splitSeconds(7200)).toEqual({ n: 2, unit: 3600 })
    expect(splitSeconds(90)).toEqual({ n: 90, unit: 1 })
    expect(splitSeconds(0)).toEqual({ n: 0, unit: 1 })
    expect(splitSeconds(172800)).toEqual({ n: 2, unit: 86400 })
  })
})

describe('setup snippets', () => {
  const url = 'https://awx.example.com/api/v2/event_streams/u/post/'
  it('points Alertmanager at the URL with the credential’s header', () => {
    const am = setupSnippets(url, 'event_stream_token').find((s) => s.key === 'alertmanager')!
    expect(am.code).toContain(`- url: ${url}`)
    expect(am.code).toContain('type: Bearer')
    expect(setupSnippets(url, 'event_stream_basic')[0].code).toContain('basic_auth:')
  })
  it('offers curl only for credentials it can send', () => {
    expect(setupSnippets(url, 'event_stream_token').map((s) => s.key)).toEqual(['alertmanager', 'github', 'gitlab', 'curl'])
    expect(setupSnippets(url, 'event_stream_gitlab').find((s) => s.key === 'curl')!.code).toContain('X-Gitlab-Token')
    expect(setupSnippets(url, 'event_stream_hmac').map((s) => s.key)).toEqual(['alertmanager', 'github', 'gitlab'])
  })
})
