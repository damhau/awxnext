import { describe, expect, it } from 'vitest'
import {
  actionSummary,
  checkBody,
  checkOutcomeView,
  completionsAt,
  conditionsFor,
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
  patternSummary,
  progressCells,
  progressText,
  rateText,
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
  it('sends each condition’s error to its row, from 0 as the check names them', () => {
    expect(fieldErrors({ conditions: ['2: undefined field', 'An absence pattern has 2 conditions.'], 'conditions.0': 'bad' })).toEqual({
      'conditions.1': 'undefined field',
      conditions: 'An absence pattern has 2 conditions.',
      'conditions.0': 'bad',
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
    expect(outcomeView('logged')).toMatchObject({ cell: 'ok', kind: 'acted' })
    expect(outcomeView('shadow')).toMatchObject({ cell: 'changed', kind: 'would' })
    expect(outcomeView('held')).toMatchObject({ cell: 'canceled', kind: 'held' })
    expect(outcomeView('not_matched')).toMatchObject({ cell: 'none', kind: 'missed' })
    expect(outcomeView('refused').kind).toBe('failed')
    expect(outcomeView('expired')).toMatchObject({ cell: 'skipped', kind: 'held' })
    expect(outcomeView('cleared').kind).toBe('held')
    expect(outcomeView('ignored')).toMatchObject({ cell: 'none', kind: 'held' })
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
  it('keeps a log’s message, and a launch’s follow-ups only for a launch', () => {
    const d = draftOf({ action: 'log', message: 'event.data', retries: 2, retry_delay: 30, post_artifacts: 'event', artifacts_stream: 5 })
    expect(ruleBody(d)).toMatchObject({ action: 'log', message: 'event.data', retries: 0, retry_delay: 0, post_artifacts: '', artifacts_stream: null })
    d.action = 'launch'
    expect(ruleBody(d)).toMatchObject({ message: '', retries: 2, retry_delay: 30, post_artifacts: 'event', artifacts_stream: 5 })
    d.retries = 0
    d.post_artifacts = ''
    expect(ruleBody(d)).toMatchObject({ retry_delay: 0, artifacts_stream: null })
    expect(draftOf()).toMatchObject({ retries: 0, retry_delay: 0, post_artifacts: '', artifacts_stream: null })
    expect(actionSummary({ action: 'log' })).toBe('Log a message')
  })
  it('starts new rules in shadow mode, with a window ready for a throttle', () => {
    expect(draftOf()).toMatchObject({ mode: 'shadow', enabled: true, throttle: '', throttle_window: 3600, action: 'launch', vars: [] })
  })
  it('sends a pattern’s fields only, and no throttle with it', () => {
    const d = { ...draftOf({ throttle: 'once_within', throttle_window: 60, conditions: ['a', 'b', 'c'] }), pattern: 'count', pattern_count: 4 }
    expect(ruleBody(d)).toMatchObject({ pattern: 'count', pattern_count: 4, pattern_window: 600, conditions: [], throttle: '', throttle_window: 0 })
    d.pattern = 'sequence'
    expect(ruleBody(d)).toMatchObject({ conditions: ['a', 'b', 'c'], pattern_count: 0 })
    d.pattern = 'absence'
    expect(ruleBody(d).conditions).toEqual(['a', 'b'])
    expect(checkBody(d)).toMatchObject({ pattern: 'absence', conditions: ['a', 'b'] })
    d.pattern = ''
    expect(ruleBody(d)).toMatchObject({ pattern: '', conditions: [], pattern_window: 0, throttle: 'once_within', throttle_window: 60 })
    expect(draftOf().conditions).toEqual(['', ''])
  })
  it('gives each pattern the conditions it takes', () => {
    expect(conditionsFor('all', ['a'])).toEqual(['a', ''])
    expect(conditionsFor('absence', ['a', 'b', 'c'])).toEqual(['a', 'b'])
    expect(conditionsFor('sequence', ['a', 'b', 'c'])).toEqual(['a', 'b', 'c'])
  })
  it('says what a pattern waits for', () => {
    expect(patternSummary({ pattern: 'count', pattern_count: 5, pattern_window: 600 })).toBe('5 events of a key within 10m 00s')
    expect(patternSummary({ pattern: 'sequence', conditions: ['a', 'b', 'c'], pattern_window: 60 })).toBe('3 steps in order within 1m 00s')
    expect(patternSummary({ pattern: '' })).toBe('None: acts on each event')
  })
  it('writes windows in their largest whole unit', () => {
    expect(splitSeconds(7200)).toEqual({ n: 2, unit: 3600 })
    expect(splitSeconds(90)).toEqual({ n: 90, unit: 1 })
    expect(splitSeconds(0)).toEqual({ n: 0, unit: 1 })
    expect(splitSeconds(172800)).toEqual({ n: 2, unit: 86400 })
  })
  it('sends the chosen check’s fields only', () => {
    const d = draftOf({ check: 'event', check_condition: 'event.type == "resolved"', check_window: 300, check_template: 7, check_notification_template: 3, check_approval: true })
    expect(ruleBody(d)).toMatchObject({ check: 'event', check_condition: 'event.type == "resolved"', check_window: 300, check_template: null, check_notification_template: 3, check_approval: true })
    expect(checkBody(d)).toMatchObject({ check: 'event', check_condition: 'event.type == "resolved"' })
    d.check = 'template'
    expect(ruleBody(d)).toMatchObject({ check: 'template', check_condition: '', check_window: 300, check_template: 7 })
    expect(checkBody(d).check_condition).toBe('')
    d.check = ''
    expect(ruleBody(d)).toMatchObject({ check: '', check_condition: '', check_window: 0, check_template: null, check_notification_template: null, check_approval: false })
  })
  it('gives a new check ten minutes, and keeps a saved check’s window', () => {
    expect(draftOf()).toMatchObject({ check: '', check_window: 600, check_approval: false })
    expect(draftOf({ check: 'template', check_window: 0 }).check_window).toBe(0)
  })
})

describe('checks', () => {
  it('shows each check outcome as a cell', () => {
    expect(checkOutcomeView('fixed')).toEqual({ cell: 'ok', label: 'Fixed' })
    expect(checkOutcomeView('not_fixed').cell).toBe('failed')
    expect(checkOutcomeView('action_failed').cell).toBe('failed')
    expect(checkOutcomeView('pending').cell).toBe('running')
    expect(checkOutcomeView('resolved').cell).toBe('changed')
    expect(checkOutcomeView('something_new')).toEqual({ cell: 'empty', label: 'something_new' })
  })
  it('writes a success rate as a whole percentage', () => {
    expect(rateText(0.666)).toBe('67%')
    expect(rateText(1)).toBe('100%')
    expect(rateText(0)).toBe('0%')
    expect(rateText(null)).toBe('—')
    expect(rateText(undefined)).toBe('—')
  })
})

describe('pattern progress', () => {
  it('fills a cell per event, condition or step seen', () => {
    expect(progressCells({ kind: 'count', events: 2, seen: 2, needed: 4 })).toEqual(['running', 'running', 'empty', 'empty'])
    expect(progressCells({ kind: 'sequence', events: 3, seen: 3, needed: 3 })).toEqual(['ok', 'ok', 'ok'])
    expect(progressCells({ kind: 'all', events: 4, seen: 2, needed: 3, conditions: [1, 3] })).toEqual(['running', 'empty', 'running'])
    expect(progressCells({ kind: 'absence', events: 2 })).toEqual(['running', 'running'])
    expect(progressCells({ kind: 'count', events: 1, seen: 1, needed: 500 })).toEqual([])
    expect(progressCells(undefined)).toEqual([])
  })
  it('writes the progress', () => {
    expect(progressText({ kind: 'count', events: 3, seen: 3, needed: 5 })).toBe('3 of 5 events')
    expect(progressText({ kind: 'all', events: 3, seen: 1, needed: 2 })).toBe('1 of 2 conditions')
    expect(progressText({ kind: 'sequence', events: 2, seen: 2, needed: 3 })).toBe('Step 2 of 3')
    expect(progressText({ kind: 'absence', events: 1 })).toBe('1 event waiting for condition 2')
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
