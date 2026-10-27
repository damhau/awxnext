import { describe as suite, expect, it } from 'vitest'
import { defaultModel, describe, describeText, parseRRule, shortRule, toRRule, zonedToUtc } from './rrule'

suite('rrule model', () => {
  it('round-trips the forms the editor produces', () => {
    const cases = [
      'DTSTART;TZID=Europe/Zurich:20250102T060000 RRULE:FREQ=WEEKLY;INTERVAL=1;BYDAY=TH',
      'DTSTART:20250101T020000Z RRULE:FREQ=DAILY;INTERVAL=1',
      'DTSTART:20250101T020000Z RRULE:FREQ=MINUTELY;INTERVAL=1;COUNT=1',
      'DTSTART;TZID=America/New_York:20250103T090000 RRULE:FREQ=MONTHLY;INTERVAL=1;BYDAY=-1FR;COUNT=12',
      'DTSTART:20250115T000000Z RRULE:FREQ=MONTHLY;INTERVAL=2;BYMONTHDAY=15',
      'DTSTART:20250301T080000Z RRULE:FREQ=YEARLY;INTERVAL=1;BYMONTH=3;BYMONTHDAY=1',
      'DTSTART;TZID=Europe/Zurich:20250106T060000 RRULE:FREQ=WEEKLY;INTERVAL=2;BYDAY=MO,WE,FR EXDATE;TZID=Europe/Zurich:20250120T060000',
    ]
    for (const c of cases) {
      const p = parseRRule(c)
      expect(p.model, c).not.toBeNull()
      expect(toRRule(p.model!)).toBe(c)
    }
  })

  it('keeps unsupported rules as text', () => {
    expect(parseRRule('DTSTART:20250101T000000Z RRULE:FREQ=DAILY;BYHOUR=3').model).toBeNull()
    expect(parseRRule('DTSTART:20250101T000000Z RRULE:FREQ=DAILY RRULE:FREQ=WEEKLY').model).toBeNull()
    expect(parseRRule('DTSTART:20250101T000000Z EXRULE:FREQ=DAILY RRULE:FREQ=WEEKLY').model).toBeNull()
    expect(parseRRule('RRULE:FREQ=DAILY').model).toBeNull()
  })

  it('writes UNTIL in UTC at the end of the local day', () => {
    const m = { ...defaultModel('Europe/Zurich'), start: '2025-06-01T09:00', freq: 'DAILY' as const, end: 'until' as const, until: '2025-06-30' }
    expect(toRRule(m)).toBe('DTSTART;TZID=Europe/Zurich:20250601T090000 RRULE:FREQ=DAILY;INTERVAL=1;UNTIL=20250630T215959Z')
    expect(parseRRule(toRRule(m)).model!.until).toBe('2025-06-30')
  })

  it('converts wall-clock times across DST', () => {
    expect(zonedToUtc('2025-01-15T06:00', 'Europe/Zurich').toISOString()).toBe('2025-01-15T05:00:00.000Z')
    expect(zonedToUtc('2025-07-15T06:00', 'Europe/Zurich').toISOString()).toBe('2025-07-15T04:00:00.000Z')
  })

  it('describes rules in words', () => {
    expect(describeText('DTSTART;TZID=Europe/Zurich:20250102T060000 RRULE:FREQ=WEEKLY;INTERVAL=1;BYDAY=TH')).toBe('Every Thursday at 06:00 Europe/Zurich')
    expect(describeText('DTSTART:20250101T020000Z RRULE:FREQ=DAILY;INTERVAL=1')).toBe('Every day at 02:00 UTC')
    expect(describeText('DTSTART:20250101T020000Z RRULE:FREQ=WEEKLY;BYDAY=MO,TU,WE,TH,FR')).toBe('Every weekday at 02:00 UTC')
    expect(describeText('DTSTART:20250101T000000Z RRULE:FREQ=MINUTELY;INTERVAL=15')).toBe('Every 15 minutes, from 00:00 UTC')
    expect(describeText('DTSTART:20250310T060000Z RRULE:FREQ=MINUTELY;INTERVAL=1;COUNT=1')).toBe('Once on 10 Mar 2025 at 06:00 UTC')
    expect(describeText('DTSTART:20250103T090000Z RRULE:FREQ=MONTHLY;BYDAY=2TU;COUNT=3')).toBe('Monthly on the second Tuesday at 09:00 UTC, 3 times')
    expect(describeText('weird')).toBe('Custom rule')
    expect(describe({ ...defaultModel('UTC'), freq: 'WEEKLY', byday: ['MO', 'WE'], start: '2025-01-06T07:30', interval: 2 })).toBe('Every 2 weeks on Monday and Wednesday at 07:30 UTC')
  })
})

suite('shortRule', () => {
  it('fits a list column', () => {
    expect(shortRule('DTSTART:20250101T020000Z RRULE:FREQ=HOURLY;INTERVAL=3')).toBe('every 3 h')
    expect(shortRule('DTSTART:20250101T020000Z RRULE:FREQ=HOURLY;INTERVAL=1')).toBe('hourly')
    expect(shortRule('DTSTART;TZID=Europe/Zurich:20250106T060000 RRULE:FREQ=WEEKLY;INTERVAL=1;BYDAY=MO,TU,WE,TH,FR')).toBe('weekdays 06:00')
    expect(shortRule('DTSTART;TZID=Europe/Zurich:20250106T060000 RRULE:FREQ=WEEKLY;INTERVAL=1;BYDAY=MO,TH')).toBe('Mon, Thu 06:00')
    expect(shortRule('DTSTART:20250101T020000Z RRULE:FREQ=DAILY;INTERVAL=1')).toBe('daily 02:00')
    expect(shortRule('DTSTART:20250101T020000Z RRULE:FREQ=MINUTELY;INTERVAL=1;COUNT=1')).toBe('once 1 Jan')
    expect(shortRule('DTSTART:20250101T020000Z RRULE:FREQ=MINUTELY;INTERVAL=15')).toBe('every 15 min')
    expect(shortRule('DTSTART:20250101T000000Z RRULE:FREQ=DAILY;BYHOUR=3')).toBe('custom')
    expect(shortRule(null)).toBe('—')
  })
})
