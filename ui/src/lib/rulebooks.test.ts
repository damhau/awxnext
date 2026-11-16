import { describe, expect, it } from 'vitest'
import { nextSteps, objectPath, parseVariables, reportObjects, ruleReasons, shortName, statusView, summaryText, variablesText, type RulebookReport } from './rulebooks'

const report: RulebookReport = {
  summary: { rules: 3, translated: 1, partial: 1, not_translated: 1 },
  rulesets: [
    {
      name: 'Web',
      streams: [{ type: 'event_stream', id: 4, name: 'ops/web.yml: Web' }],
      sources: [{ name: 'hook', plugin: 'ansible.eda.webhook', status: 'translated', object: { type: 'event_stream', id: 5, name: 'ops/web.yml: Web / hook' } }],
      rules: [
        { name: 'Deploy', status: 'translated', actions: [{ action: 'run_job_template', status: 'translated', as: 'launch', object: { type: 'event_rule', id: 9, name: 'ops/web.yml: Web / Deploy' } }] },
        {
          name: 'Both',
          status: 'partial',
          reasons: ['A filter isn’t translated.'],
          actions: [
            { action: 'post_event', status: 'translated', object: { type: 'event_rule', id: 10, name: 'ops/web.yml: Web / Both' } },
            { action: 'run_playbook', status: 'not_translated', reason: 'awxnext runs playbooks through templates.' },
          ],
        },
        { name: 'Stop', status: 'not_translated', actions: [{ action: 'shutdown', status: 'not_translated', reason: 'Nothing to stop.' }] },
      ],
    },
  ],
}

describe('a rulebook report', () => {
  it('links each object made once, streams first', () => {
    expect(reportObjects(report).map((o) => objectPath(o))).toEqual(['/event-streams/4', '/event-streams/5', '/event-rules/9', '/event-rules/10'])
    expect(reportObjects({})).toEqual([])
    expect(objectPath({ type: 'event_connector', id: 2, name: '' })).toBe('/event-connectors/2')
    expect(objectPath({ type: 'something', id: 2, name: '' })).toBeNull()
    expect(objectPath(undefined)).toBeNull()
  })
  it('names objects without their project and rulebook', () => {
    expect(shortName('ops/web.yml: Web / Deploy')).toBe('Web / Deploy')
    expect(shortName('plain')).toBe('plain')
  })
  it('says why a rule is not all translated', () => {
    const [, both, stop] = report.rulesets![0].rules
    expect(ruleReasons(both)).toEqual(['A filter isn’t translated.', 'run_playbook: awxnext runs playbooks through templates.'])
    expect(ruleReasons(stop)).toEqual(['shutdown: Nothing to stop.'])
    expect(ruleReasons(report.rulesets![0].rules[0])).toEqual([])
  })
  it('shows statuses as cells', () => {
    expect(statusView('translated').cell).toBe('ok')
    expect(statusView('partial').cell).toBe('changed')
    expect(statusView('not_translated').cell).toBe('failed')
    expect(statusView('new')).toEqual({ cell: 'empty', label: 'new' })
  })
  it('sums the rules up', () => {
    expect(summaryText(report.summary)).toBe('1 of 3 rules translated · 1 partly · 1 not')
    expect(summaryText({ rules: 1, translated: 1, partial: 0, not_translated: 0 })).toBe('1 of 1 rule translated')
    expect(summaryText(undefined)).toBe('No rules')
  })
  it('lists what to turn on', () => {
    expect(
      nextSteps([
        { type: 'event_stream', credential: null },
        { type: 'event_stream', credential: 3 },
        { type: 'event_connector', enabled: false },
        { type: 'event_rule', enabled: false },
        { type: 'event_rule', enabled: false },
        { type: 'event_rule', enabled: true },
      ]),
    ).toEqual([
      'Give 1 stream a credential: it refuses events until then.',
      'Enable 1 connector, with its execution environment and credentials.',
      'Enable 2 rules in shadow mode, and read their decisions before moving them to auto.',
    ])
    expect(nextSteps([])).toEqual([])
  })
  it('reads variables from YAML or JSON', () => {
    expect(parseVariables('port: 5000\nteam: ops\n')).toEqual({ vars: { port: 5000, team: 'ops' } })
    expect(parseVariables('{"port": 5000}')).toEqual({ vars: { port: 5000 } })
    expect(parseVariables('  ')).toEqual({ vars: {} })
    expect(parseVariables('- a').error).toMatch(/mapping/)
    expect(parseVariables('a: [').error).toBeTruthy()
    expect(variablesText({})).toBe('')
    expect(parseVariables(variablesText({ port: 5000 }))).toEqual({ vars: { port: 5000 } })
  })
})
