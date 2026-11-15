import { useState } from 'react'
import { Link } from 'react-router'
import { api } from '../../api/client'
import type { Any } from '../../api/types'
import { TextInput } from '../../components/form'
import { DataTable } from '../../components/table'
import { Ago, ErrorNote, Panel } from '../../components/ui'
import { absolute } from '../../lib/format'
import { GUARDRAIL_LABELS, outcomeView, type MissDetails } from '../../lib/eventRules'
import { tr, trn } from '../../lib/i18n'
import { DecisionBody, MissView, Outcome } from './decisions'

const pad = (n: number) => String(n).padStart(2, '0')
/** A date as a datetime-local input takes it, in the browser's zone. */
function localInput(d: Date): string {
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

/** Runs a backtest, of a saved rule (path event_rules/N/backtest/) or of a
 * draft (event_rules/backtest/ with the draft's fields), and shows it. */
export function Backtest({ path, draft, filter }: { path: string; draft?: () => Record<string, unknown>; filter?: string }) {
  const [since, setSince] = useState(() => localInput(new Date(Date.now() - 86400_000)))
  const [until, setUntil] = useState('')
  const [max, setMax] = useState('')
  const [result, setResult] = useState<Any | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const run = async () => {
    setBusy(true)
    setError(null)
    try {
      const body: Record<string, unknown> = { ...draft?.() }
      if (since) body.since = new Date(since).toISOString()
      if (until) body.until = new Date(until).toISOString()
      if (max) body.max_events = Number(max)
      setResult(await api.post<Any>(path, body))
    } catch (e) {
      setError(e)
      setResult(null)
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="stack" style={{ gap: 16 }}>
      <form
        className="backtest-form"
        onSubmit={(e) => {
          e.preventDefault()
          run()
        }}
      >
        <TextInput label={tr('From')} type="datetime-local" value={since} onChange={setSince} />
        <TextInput label={tr('To')} optional type="datetime-local" value={until} onChange={setUntil} />
        <TextInput label={tr('Events at most')} optional type="number" value={max} onChange={setMax} placeholder="10000" />
        <div>
          <button className="btn primary" disabled={busy}>
            {busy ? tr('Running…') : tr('Run backtest')}
          </button>
        </div>
      </form>
      <p className="small muted" style={{ margin: 0 }}>
        {tr('A backtest runs the rule on the events its streams received in the period, test mode’s excepted, as the engine would have: throttles, circuit breakers, guardrails and mode. It launches nothing and records nothing.')}
      </p>
      <ErrorNote error={error} />
      {result && <BacktestResult r={result} filter={filter} />}
    </div>
  )
}

type KeyRow = { key: string; events: number; fired: number; held: number; throttled: number; deduplicated: number }
type Miss = { expression: string; count: number; example: Any }

function BacktestResult({ r, filter }: { r: Any; filter?: string }) {
  const outcomes = Object.entries((r.outcomes ?? {}) as Record<string, number>).sort((a, b) => b[1] - a[1])
  const held = Object.entries((r.held ?? {}) as Record<string, number>)
  const decisions = (r.decisions ?? []) as Any[]
  return (
    <div className="stack" style={{ gap: 16 }}>
      <dl className="facts">
        <div>
          <dt>{tr('Period')}</dt>
          <dd>
            {absolute(r.since)} – {absolute(r.until)}
          </dd>
        </div>
        <div>
          <dt>{tr('Events')}</dt>
          <dd>{r.events}</dd>
        </div>
        <div>
          <dt>{tr('Matched')}</dt>
          <dd>{r.matched}</dd>
        </div>
        <div>
          <dt>{tr('Would fire')}</dt>
          <dd>{r.fired}</dd>
        </div>
        <div>
          <dt>{tr('Keys')}</dt>
          <dd>{r.key_count}</dd>
        </div>
        <div>
          <dt>{tr('Hosts')}</dt>
          <dd>{r.host_count}</dd>
        </div>
      </dl>
      {r.truncated && <div className="notice warn">{tr('The backtest stopped before the end of the period: it reads at most its number of events, for at most 30 seconds.')}</div>}
      {outcomes.length > 0 && (
        <div className="outcome-counts" role="list" aria-label={tr('Outcomes')}>
          {outcomes.map(([o, n]) => (
            <span key={o} className="chip" role="listitem">
              <Outcome outcome={o}>
                {outcomeView(o).label} <b className="mono">{n}</b>
              </Outcome>
            </span>
          ))}
          {held.map(([g, n]) => (
            <span key={g} className="chip faint" role="listitem">
              {tr('{guardrail} held {n}', { guardrail: GUARDRAIL_LABELS[g] ?? g, n })}
            </span>
          ))}
        </div>
      )}
      {(r.misses as Miss[] | undefined)?.length ? (
        <Panel title={tr('Why events didn’t match')}>
          <div className="stack" style={{ gap: 14 }}>
            {(r.misses as Miss[]).map((m) => (
              <div key={m.expression} className="stack" style={{ gap: 6 }}>
                <div className="small">
                  <b>{trn(m.count, '{n} event', '{n} events')}</b>{' '}
                  <span className="muted">
                    {tr('such as')}{' '}
                    <Link to={`/event-streams/${m.example.stream}/events/${m.example.sequence}`}>
                      #{m.example.sequence}
                    </Link>
                  </span>
                </div>
                <MissView miss={m.example.filter as MissDetails} filter={filter} />
              </div>
            ))}
          </div>
        </Panel>
      ) : null}
      {(r.breakers as Any[] | undefined)?.length ? (
        <Panel title={tr('Circuit breakers that would trip')}>
          <ul className="list-plain small">
            {(r.breakers as Any[]).map((b) => (
              <li key={b.key + b.tripped_at}>
                <span className="mono">{b.key || tr('all events')}</span> <span className="faint">{absolute(b.tripped_at)}</span>
              </li>
            ))}
          </ul>
        </Panel>
      ) : null}
      {(r.keys as KeyRow[] | undefined)?.length ? (
        <Panel title={tr('By key')} pad={false}>
          <DataTable<KeyRow>
            label={tr('By key')}
            stack
            rowKey={(k) => k.key}
            rows={r.keys}
            columns={[
              { key: 'key', header: tr('Key'), render: (k) => <span className="mono">{k.key || <span className="faint">{tr('all events')}</span>}</span> },
              { key: 'events', header: tr('Events'), className: 'num', render: (k) => k.events },
              { key: 'fired', header: tr('Would fire'), className: 'num', render: (k) => k.fired },
              { key: 'held', header: tr('Held back'), className: 'num', render: (k) => k.held },
              { key: 'throttled', header: tr('Throttled'), className: 'num', render: (k) => k.throttled },
              { key: 'dedup', header: tr('Deduplicated'), className: 'num', render: (k) => k.deduplicated },
            ]}
          />
        </Panel>
      ) : null}
      {(r.hosts as Any[] | undefined)?.length ? (
        <Panel title={tr('Hosts the launches would target')}>
          <div className="row wrap" style={{ gap: 6 }}>
            {(r.hosts as Any[]).map((h) => (
              <span key={h.host} className="chip">
                <span className="mono">{h.host}</span> <b className="mono">{h.fired}</b>
              </span>
            ))}
          </div>
        </Panel>
      ) : null}
      {decisions.length > 0 && (
        <Panel title={trn(decisions.length, 'The first matching event', 'The first {n} matching events')}>
          <div className="decisions" role="list" aria-label={tr('Decisions')}>
            {decisions.map((d) => (
              <details key={`${d.stream}/${d.sequence}`} className="decision" role="listitem">
                <summary>
                  <Outcome outcome={d.outcome} />
                  <span className="decision-what ellipsis">
                    <Link to={`/event-streams/${d.stream}/events/${d.sequence}`} onClick={(e) => e.stopPropagation()}>
                      <span className="mono">#{d.sequence}</span>
                    </Link>
                    <span className="mono faint small"> {d.event_type}</span>
                  </span>
                  <span className="decision-key mono small ellipsis">{d.key || ''}</span>
                  <span className="decision-reason small muted ellipsis">{d.reason}</span>
                  <span className="small faint nowrap">
                    <Ago at={d.received} />
                  </span>
                </summary>
                <DecisionBody d={d} filter={filter} />
              </details>
            ))}
          </div>
        </Panel>
      )}
      {(r.approximations as string[] | undefined)?.length ? (
        <div>
          <div className="section-title">{tr('What a backtest approximates')}</div>
          <ul className="small muted" style={{ margin: '6px 0 0', paddingLeft: 18 }}>
            {(r.approximations as string[]).map((a) => (
              <li key={a}>{a}</li>
            ))}
          </ul>
        </div>
      ) : null}
    </div>
  )
}
