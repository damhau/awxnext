// A friendly model of the schedule rules AWX accepts, converted to and from
// the iCalendar text the API stores:
//
//   DTSTART;TZID=Europe/Zurich:20250102T060000 RRULE:FREQ=WEEKLY;BYDAY=TH
//
// Rules the model cannot express (several RRULEs, EXRULE, RDATE, BYHOUR...)
// stay editable as text ("advanced").

export type Freq = 'once' | 'MINUTELY' | 'HOURLY' | 'DAILY' | 'WEEKLY' | 'MONTHLY' | 'YEARLY'
export type Day = 'MO' | 'TU' | 'WE' | 'TH' | 'FR' | 'SA' | 'SU'
export const DAYS: Day[] = ['MO', 'TU', 'WE', 'TH', 'FR', 'SA', 'SU']
export const DAY_NAMES: Record<Day, string> = { MO: 'Monday', TU: 'Tuesday', WE: 'Wednesday', TH: 'Thursday', FR: 'Friday', SA: 'Saturday', SU: 'Sunday' }
export const MONTHS = ['January', 'February', 'March', 'April', 'May', 'June', 'July', 'August', 'September', 'October', 'November', 'December']
const ORDINAL: Record<number, string> = { 1: 'first', 2: 'second', 3: 'third', 4: 'fourth', [-1]: 'last' }

export interface RuleModel {
  freq: Freq
  interval: number
  /** Weekly: days of the week. */
  byday: Day[]
  /** Monthly: on a day of the month, or on the nth weekday. */
  monthly: 'day' | 'weekday'
  monthday: number
  setpos: number
  setposDay: Day
  /** Yearly: month (1-12) and day. */
  month: number
  /** Local start "YYYY-MM-DDTHH:MM" in tz. */
  start: string
  tz: string
  end: 'never' | 'count' | 'until'
  count: number
  /** Last day (local, "YYYY-MM-DD"). */
  until: string
  /** Skipped dates (local, "YYYY-MM-DD"), at the start time. */
  exdates: string[]
}

export interface Parsed {
  model: RuleModel | null
  /** Why the text cannot be edited with the form. */
  reason?: string
}

export function browserZone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone || 'UTC'
  } catch {
    return 'UTC'
  }
}

interface Parts {
  y: number
  m: number
  d: number
  h: number
  mi: number
  s: number
}

/** Wall-clock parts of an instant in a time zone. */
export function partsIn(date: Date, tz: string): Parts {
  const f = new Intl.DateTimeFormat('en-US', {
    timeZone: tz,
    hourCycle: 'h23',
    year: 'numeric',
    month: '2-digit',
    day: '2-digit',
    hour: '2-digit',
    minute: '2-digit',
    second: '2-digit',
  })
  const p: Record<string, number> = {}
  for (const x of f.formatToParts(date)) if (x.type !== 'literal') p[x.type] = Number(x.value)
  return { y: p.year, m: p.month, d: p.day, h: p.hour % 24, mi: p.minute, s: p.second }
}

function offsetMs(date: Date, tz: string): number {
  const p = partsIn(date, tz)
  return Date.UTC(p.y, p.m - 1, p.d, p.h, p.mi, p.s) - Math.floor(date.getTime() / 1000) * 1000
}

/** The instant of a wall-clock time in a zone. */
export function zonedToUtc(local: string, tz: string): Date {
  const [date, time = '00:00'] = local.split('T')
  const [y, m, d] = date.split('-').map(Number)
  const [h, mi, s = 0] = time.split(':').map(Number)
  const guess = Date.UTC(y, m - 1, d, h, mi, s)
  let t = guess - offsetMs(new Date(guess), tz)
  t = guess - offsetMs(new Date(t), tz)
  return new Date(t)
}

const pad = (n: number, w = 2) => String(n).padStart(w, '0')

/** "YYYY-MM-DDTHH:MM" for an instant in a zone. */
export function localString(date: Date, tz: string): string {
  const p = partsIn(date, tz)
  return `${p.y}-${pad(p.m)}-${pad(p.d)}T${pad(p.h)}:${pad(p.mi)}`
}

function compact(local: string): string {
  // "2025-01-02T06:00" -> "20250102T060000"
  const [d, t = '00:00'] = local.split('T')
  return d.replace(/-/g, '') + 'T' + t.replace(/:/g, '').padEnd(6, '0').slice(0, 6)
}

function expand(c: string): string {
  // "20250102T060000" -> "2025-01-02T06:00"
  return `${c.slice(0, 4)}-${c.slice(4, 6)}-${c.slice(6, 8)}T${c.slice(9, 11)}:${c.slice(11, 13)}`
}

export function defaultModel(tz = browserZone(), now = new Date()): RuleModel {
  const next = new Date(Math.ceil(now.getTime() / 3600_000) * 3600_000)
  const start = localString(next, tz)
  const day = DAYS[(new Date(start.slice(0, 10) + 'T12:00:00Z').getUTCDay() + 6) % 7]
  return {
    freq: 'DAILY',
    interval: 1,
    byday: [day],
    monthly: 'day',
    monthday: Number(start.slice(8, 10)),
    setpos: 1,
    setposDay: day,
    month: Number(start.slice(5, 7)),
    start,
    tz,
    end: 'never',
    count: 10,
    until: '',
    exdates: [],
  }
}

/** Builds the API's rrule text from a model. */
export function toRRule(m: RuleModel): string {
  const utc = m.tz === 'UTC'
  const out = [utc ? `DTSTART:${compact(m.start)}Z` : `DTSTART;TZID=${m.tz}:${compact(m.start)}`]
  const parts: string[] = []
  if (m.freq === 'once') {
    parts.push('FREQ=MINUTELY', 'INTERVAL=1', 'COUNT=1')
  } else {
    parts.push(`FREQ=${m.freq}`, `INTERVAL=${Math.max(1, Math.floor(m.interval) || 1)}`)
    if (m.freq === 'WEEKLY' && m.byday.length) parts.push(`BYDAY=${DAYS.filter((d) => m.byday.includes(d)).join(',')}`)
    if (m.freq === 'MONTHLY') {
      if (m.monthly === 'day') parts.push(`BYMONTHDAY=${m.monthday}`)
      else parts.push(`BYDAY=${m.setpos}${m.setposDay}`)
    }
    if (m.freq === 'YEARLY') parts.push(`BYMONTH=${m.month}`, `BYMONTHDAY=${m.monthday}`)
    if (m.end === 'count') parts.push(`COUNT=${Math.min(999, Math.max(1, Math.floor(m.count) || 1))}`)
    if (m.end === 'until' && m.until) {
      // Through the end of the last day, expressed in UTC as the API requires.
      const last = zonedToUtc(`${m.until}T23:59:59`, m.tz)
      parts.push(`UNTIL=${last.toISOString().replace(/[-:]/g, '').replace(/\.\d+/, '')}`)
    }
  }
  out.push('RRULE:' + parts.join(';'))
  const ex = m.exdates.filter(Boolean)
  if (ex.length && m.freq !== 'once') {
    const time = m.start.slice(11)
    const list = ex.map((d) => compact(`${d}T${time}`) + (utc ? 'Z' : '')).join(',')
    out.push(utc ? `EXDATE:${list}` : `EXDATE;TZID=${m.tz}:${list}`)
  }
  return out.join(' ')
}

/** Reads rrule text into a model, when the form can represent it. */
export function parseRRule(text: string): Parsed {
  const fields = text.trim().split(/\s+/).filter(Boolean)
  const m = defaultModel('UTC')
  let rrule: string | null = null
  let sawStart = false
  const exdates: string[] = []
  for (const f of fields) {
    const up = f.toUpperCase()
    if (up.startsWith('DTSTART')) {
      const x = f.match(/^DTSTART(?:;TZID=([^:]+))?:(\d{8}T\d{6})(Z?)$/i)
      if (!x) return { model: null, reason: 'DTSTART is not in a supported form.' }
      m.tz = x[1] ?? 'UTC'
      m.start = expand(x[2])
      sawStart = true
    } else if (up.startsWith('RRULE:')) {
      if (rrule !== null) return { model: null, reason: 'The rule combines several RRULE lines.' }
      rrule = f.slice(6)
    } else if (up.startsWith('EXDATE')) {
      const value = f.slice(f.indexOf(':') + 1)
      for (const d of value.split(',')) {
        if (!/^\d{8}T\d{6}Z?$/i.test(d)) return { model: null, reason: 'Exceptions use a format the form does not support.' }
        exdates.push(expand(d).slice(0, 10))
      }
    } else {
      return { model: null, reason: `The rule uses ${f.split(/[:;]/)[0]}.` }
    }
  }
  if (!sawStart || rrule === null) return { model: null, reason: 'The rule needs a DTSTART and an RRULE.' }
  const parts: Record<string, string> = {}
  for (const kv of rrule.split(';')) {
    const [k, v] = kv.split('=')
    if (!k || v === undefined) return { model: null, reason: 'The RRULE is malformed.' }
    parts[k.toUpperCase()] = v.toUpperCase()
  }
  const known = new Set(['FREQ', 'INTERVAL', 'BYDAY', 'BYMONTHDAY', 'BYMONTH', 'COUNT', 'UNTIL', 'WKST'])
  const extra = Object.keys(parts).find((k) => !known.has(k))
  if (extra) return { model: null, reason: `The RRULE uses ${extra}.` }
  const freq = parts.FREQ as Freq
  if (!['MINUTELY', 'HOURLY', 'DAILY', 'WEEKLY', 'MONTHLY', 'YEARLY'].includes(freq)) return { model: null, reason: `Frequency ${parts.FREQ ?? '(none)'} is not supported here.` }
  m.interval = Number(parts.INTERVAL ?? 1) || 1
  m.exdates = exdates
  if (freq === 'MINUTELY' && m.interval === 1 && parts.COUNT === '1' && !parts.BYDAY) {
    m.freq = 'once'
    return { model: m }
  }
  m.freq = freq
  const startDay = DAYS[(new Date(m.start.slice(0, 10) + 'T12:00:00Z').getUTCDay() + 6) % 7]
  m.byday = [startDay]
  m.setposDay = startDay
  m.monthday = Number(m.start.slice(8, 10))
  m.month = Number(m.start.slice(5, 7))
  if (parts.BYDAY) {
    const days = parts.BYDAY.split(',')
    if (freq === 'WEEKLY') {
      if (!days.every((d) => DAYS.includes(d as Day))) return { model: null, reason: 'Weekly days use a format the form does not support.' }
      m.byday = days as Day[]
    } else if (freq === 'MONTHLY' && days.length === 1) {
      const x = days[0].match(/^(-1|[1-4])(MO|TU|WE|TH|FR|SA|SU)$/)
      if (!x) return { model: null, reason: 'The monthly weekday is not supported by the form.' }
      m.monthly = 'weekday'
      m.setpos = Number(x[1])
      m.setposDay = x[2] as Day
    } else {
      return { model: null, reason: 'BYDAY is only supported for weekly and monthly rules.' }
    }
  }
  if (parts.BYMONTHDAY) {
    if (freq !== 'MONTHLY' && freq !== 'YEARLY') return { model: null, reason: 'BYMONTHDAY is only supported for monthly and yearly rules.' }
    m.monthday = Number(parts.BYMONTHDAY)
    if (!(m.monthday >= 1 && m.monthday <= 31)) return { model: null, reason: 'Negative month days are not supported by the form.' }
  }
  if (parts.BYMONTH) {
    if (freq !== 'YEARLY' || parts.BYMONTH.includes(',')) return { model: null, reason: 'BYMONTH is only supported for yearly rules on one month.' }
    m.month = Number(parts.BYMONTH)
  }
  if (parts.COUNT) {
    m.end = 'count'
    m.count = Number(parts.COUNT)
  } else if (parts.UNTIL) {
    const u = parts.UNTIL.match(/^(\d{8}T\d{6})Z$/)
    if (!u) return { model: null, reason: 'UNTIL must be a UTC time.' }
    m.end = 'until'
    const instant = new Date(expand(u[1]) + ':00Z')
    m.until = localString(instant, m.tz).slice(0, 10)
  }
  return { model: m }
}

const timeFmt = (local: string) => local.slice(11, 16)

function dateFmt(local: string): string {
  const [y, mo, d] = local.slice(0, 10).split('-').map(Number)
  return `${d} ${MONTHS[mo - 1].slice(0, 3)} ${y}`
}

function list(items: string[]): string {
  if (items.length <= 1) return items.join('')
  return items.slice(0, -1).join(', ') + ' and ' + items[items.length - 1]
}

/** "Every Thursday at 06:00 Europe/Zurich", "Once on 2 Jan 2025 at 06:00 UTC". */
export function describe(m: RuleModel, withZone = true): string {
  const at = `at ${timeFmt(m.start)}${withZone ? ' ' + m.tz : ''}`
  if (m.freq === 'once') return `Once on ${dateFmt(m.start)} ${at}`
  const n = m.interval
  let s: string
  switch (m.freq) {
    case 'MINUTELY':
      s = n === 1 ? 'Every minute' : `Every ${n} minutes`
      break
    case 'HOURLY':
      s = n === 1 ? 'Every hour' : `Every ${n} hours`
      break
    case 'DAILY':
      s = (n === 1 ? 'Every day' : `Every ${n} days`) + ' ' + at
      break
    case 'WEEKLY': {
      const days = DAYS.filter((d) => m.byday.includes(d))
      const names =
        days.length === 5 && !days.includes('SA') && !days.includes('SU')
          ? 'weekday'
          : list(days.map((d) => (days.length > 2 ? DAY_NAMES[d].slice(0, 3) : DAY_NAMES[d])))
      s = n === 1 ? `Every ${names}` : `Every ${n} weeks on ${names}`
      s += ' ' + at
      break
    }
    case 'MONTHLY': {
      const on = m.monthly === 'day' ? `day ${m.monthday}` : `the ${ORDINAL[m.setpos]} ${DAY_NAMES[m.setposDay]}`
      s = (n === 1 ? 'Monthly' : `Every ${n} months`) + ` on ${on} ${at}`
      break
    }
    case 'YEARLY':
      s = (n === 1 ? 'Yearly' : `Every ${n} years`) + ` on ${m.monthday} ${MONTHS[m.month - 1]} ${at}`
      break
  }
  if (m.freq === 'MINUTELY' || m.freq === 'HOURLY') s += `, from ${timeFmt(m.start)}${withZone ? ' ' + m.tz : ''}`
  if (m.end === 'count') s += `, ${m.count} time${m.count === 1 ? '' : 's'}`
  if (m.end === 'until' && m.until) s += `, until ${dateFmt(m.until)}`
  if (m.exdates.length) s += `, except ${m.exdates.length} date${m.exdates.length === 1 ? '' : 's'}`
  return s
}

/** Words for stored rrule text, falling back to a short label. */
export function describeText(text: string | null | undefined): string {
  if (!text) return '—'
  const p = parseRRule(text)
  return p.model ? describe(p.model) : 'Custom rule'
}

/** A few words for a list column: "every 3 h", "weekdays 06:00", "Mon, Thu 06:00". */
export function shortRule(text: string | null | undefined): string {
  const m = text ? parseRRule(text).model : null
  if (!m) return text ? 'custom' : '—'
  const n = m.interval
  const at = timeFmt(m.start)
  switch (m.freq) {
    case 'once':
      return `once ${dateFmt(m.start).replace(/ \d{4}$/, '')}`
    case 'MINUTELY':
      return n === 1 ? 'every minute' : `every ${n} min`
    case 'HOURLY':
      return n === 1 ? 'hourly' : `every ${n} h`
    case 'DAILY':
      return n === 1 ? `daily ${at}` : `every ${n} days ${at}`
    case 'WEEKLY': {
      const days = DAYS.filter((d) => m.byday.includes(d))
      const names = days.length === 7 ? 'daily' : days.length === 5 && !days.includes('SA') && !days.includes('SU') ? 'weekdays' : days.map((d) => DAY_NAMES[d].slice(0, 3)).join(', ')
      return `${n === 1 ? '' : `every ${n} wk, `}${names} ${at}`
    }
    case 'MONTHLY':
      return `${n === 1 ? 'monthly' : `every ${n} months`} ${at}`
    case 'YEARLY':
      return `yearly ${at}`
  }
}
