import { describe, expect, it } from 'vitest'
import {
  apiParams,
  byLaunchTypeCsv,
  csvName,
  durationScale,
  labelEvery,
  mostFailedCsv,
  niceScale,
  percent,
  points,
  readState,
  sameParams,
  savedParams,
  templateCsv,
  toCsv,
  trendCsv,
  type TemplateRow,
} from './reports'

const row = (over: Partial<TemplateRow>): TemplateRow => ({
  template: 7,
  name: 'deploy',
  runs: 6,
  successful: 3,
  failed: 2,
  canceled: 1,
  success_rate: 0.6,
  previous_success_rate: 1,
  success_rate_change: -0.4,
  median_duration: 30,
  p95_duration: 48,
  flips: 3,
  last_failure: { job: 42, finished: '2026-11-21T12:00:00Z', exists: true },
  ...over,
})

describe('reports', () => {
  it('reads the period, filters and order from the URL', () => {
    const s = readState(new URLSearchParams('period=7&organization=3&template=x&label=0&launch_type=portal&order_by=-runs&interval=week'))
    expect(s).toEqual({ period: '7', from: '', to: '', filters: { organization: 3 }, launchType: 'portal', order: '-runs', interval: 'week' })
    expect(apiParams(s)).toEqual({ period: '7', organization: '3', launch_type: 'portal' })
    expect(savedParams(s)).toEqual({ period: '7', organization: '3', launch_type: 'portal', interval: 'week' })
  })
  it('defaults to 30 days, and a from date makes it custom', () => {
    expect(apiParams(readState(new URLSearchParams('period=12&launch_type=nope&interval=month')))).toEqual({ period: '30' })
    expect(readState(new URLSearchParams('interval=month')).interval).toBe('')
    const custom = readState(new URLSearchParams('period=7&from=2026-10-01&to=2026-10-31'))
    expect(custom.period).toBe('custom')
    expect(apiParams(custom)).toEqual({ from: '2026-10-01', to: '2026-10-31' })
    expect(apiParams(readState(new URLSearchParams('from=2026-10-01&to=soon')))).toEqual({ from: '2026-10-01' })
  })
  it('compares saved reports by their parameters', () => {
    expect(sameParams({ period: '7', organization: '3' }, { organization: '3', period: '7' })).toBe(true)
    expect(sameParams({ period: '7' }, { period: '7', organization: '3' })).toBe(false)
    expect(sameParams({ period: '7' }, { period: '30' })).toBe(false)
  })
  it('shows rates and their change', () => {
    expect(percent(0.7143)).toBe('71%')
    expect(percent(null)).toBe('—')
    expect(percent(0)).toBe('0%')
    expect(points(-0.4)).toBe('−40 pts')
    expect(points(0.05)).toBe('+5 pts')
    expect(points(0.001)).toBe('±0 pts')
    expect(points(null)).toBe('')
  })
  it('exports the API rows as CSV, with the API field names', () => {
    const csv = toCsv(templateCsv, [row({}), row({ template: null, name: 'gone, "old"', success_rate: null, previous_success_rate: null, success_rate_change: null, last_failure: null })])
    expect(csv.split('\r\n')).toEqual([
      'template,name,runs,successful,failed,canceled,success_rate,previous_success_rate,success_rate_change,median_duration,p95_duration,flips,last_failure.job,last_failure.finished',
      '7,deploy,6,3,2,1,0.6,1,-0.4,30,48,3,42,2026-11-21T12:00:00Z',
      ',"gone, ""old""",6,3,2,1,,,,30,48,3,,',
      '',
    ])
    expect(toCsv(trendCsv, [{ start: '2026-11-16', runs: 2, successful: 1, failed: 1, canceled: 0, failure_rate: 0.5, median_duration: 28.5 }])).toBe(
      'start,runs,successful,failed,canceled,failure_rate,median_duration\r\n2026-11-16,2,1,1,0,0.5,28.5\r\n',
    )
    expect(toCsv(byLaunchTypeCsv, [])).toBe('launch_type,runs,successful,failed,canceled,success_rate\r\n')
  })
  it("doesn't let a spreadsheet run a name as a formula", () => {
    const csv = toCsv(mostFailedCsv, [{ host_name: '=HYPERLINK("http://x")', host: null, inventory: 2, inventory_name: '@prod', runs: 1, failed: -1, unreachable: 0 }])
    expect(csv.split('\r\n')[1]).toBe(`"'=HYPERLINK(""http://x"")",,2,'@prod,1,-1,0`)
  })
  it('names the CSV after the report and period', () => {
    expect(csvName('templates', { from: '2026-10-24', to: '2026-11-22' } as never)).toBe('awxnext-templates-2026-10-24-to-2026-11-22.csv')
    expect(csvName('templates', undefined)).toBe('awxnext-templates.csv')
  })
  it('rounds the y axis', () => {
    expect(niceScale(0)).toEqual({ top: 1, ticks: [0, 1] })
    expect(niceScale(7)).toEqual({ top: 8, ticks: [0, 2, 4, 6, 8] })
    expect(niceScale(10)).toEqual({ top: 10, ticks: [0, 2.5, 5, 7.5, 10] })
    expect(niceScale(1)).toEqual({ top: 1, ticks: [0, 0.25, 0.5, 0.75, 1] })
    expect(niceScale(1234)).toEqual({ top: 1500, ticks: [0, 500, 1000, 1500] })
    expect(durationScale(45)).toEqual({ top: 60, ticks: [0, 20, 40, 60] })
    expect(durationScale(600)).toEqual({ top: 600, ticks: [0, 150, 300, 450, 600] })
    expect(durationScale(5 * 3600)).toEqual({ top: 21600, ticks: [0, 7200, 14400, 21600] })
    expect(labelEvery(7)).toBe(1)
    expect(labelEvery(30)).toBe(4)
    expect(labelEvery(14)).toBe(2)
  })
})
