import { useState, type ReactNode } from 'react'
import { Link } from 'react-router'
import { useList } from '../../api/hooks'
import type { Any } from '../../api/types'
import { Pager } from '../../components/table'
import { Ago, Cell, Empty, ErrorNote, Loading } from '../../components/ui'
import { absolute } from '../../lib/format'
import { checkOutcomeView, conditionLabel, GUARDRAIL_LABELS, missParts, MODE_LABELS, OUTCOME_FILTERS, outcomeView, PATTERN_LABELS, progressCells, progressText, seenText, type MissDetails, type PatternProgress } from '../../lib/eventRules'
import { asIs, tr, trn } from '../../lib/i18n'
import { FilterBox } from '../admin/common'

export function Outcome({ outcome, children }: { outcome: string; children?: ReactNode }) {
  const v = outcomeView(outcome)
  return (
    <span className="status">
      <Cell state={v.cell} />
      <span>{children ?? v.label}</span>
    </span>
  )
}

/** Why a filter didn't match: its failed part, marked in the filter when
 * the rule still has it there, and the values the part saw. */
export function MissView({ miss, filter }: { miss: MissDetails | undefined; filter?: string }) {
  if (!miss) return null
  const parts = filter ? missParts(filter, miss) : null
  return (
    <div className="miss stack" style={{ gap: 6 }}>
      <pre className="code cel-miss">
        {parts ? (
          <>
            {parts[0]}
            <mark>{parts[1]}</mark>
            {parts[2]}
          </>
        ) : (
          <mark>{miss.expression}</mark>
        )}
      </pre>
      {miss.error && <div className="small fail-text">{miss.error}</div>}
      {!!miss.values?.length && (
        <ul className="cel-seen mono small">
          {miss.values.map((v, i) => (
            <li key={i}>{seenText(v)}</li>
          ))}
        </ul>
      )}
    </div>
  )
}

/** Why an event matched none of a pattern's conditions: each one's part
 * that failed. */
export function ConditionsMiss({ misses, conditions, pattern }: { misses: MissDetails[]; conditions?: string[]; pattern?: string }) {
  return (
    <div className="stack" style={{ gap: 8 }}>
      {misses.map((m, i) => (
        <div key={i} className="stack" style={{ gap: 4 }}>
          <div className="small muted">{conditionLabel(pattern ?? 'all', i)}</div>
          <MissView miss={m} filter={conditions?.[i]} />
        </div>
      ))}
    </div>
  )
}

/** How far a key's window got: a cell per event, condition or step it
 * needs, and until when it waits. */
export function WindowProgress({ p, until = true }: { p: PatternProgress; until?: boolean }) {
  const cells = progressCells(p)
  return (
    <div className="window-progress">
      {cells.length > 0 && (
        <span className="cells">
          {cells.map((c, i) => (
            <Cell key={i} state={c} />
          ))}
        </span>
      )}
      <span className="small">{progressText(p)}</span>
      {until && p.until && <span className="small faint">{tr('until {time}', { time: absolute(p.until) })}</span>}
    </div>
  )
}

const pretty = (v: unknown) => JSON.stringify(v, null, 2)

/** What a decision recorded beyond its outcome. */
export function DecisionBody({ d, filter, conditions }: { d: Any; filter?: string; conditions?: string[] }) {
  const details = (d.details ?? {}) as Record<string, unknown>
  const { filter: miss, pattern, conditions: condMisses, cleared_by: clearedBy, ...rest } = details
  const window = pattern as PatternProgress | undefined
  const cleared = clearedBy as { stream: number; sequence: number } | undefined
  const facts: [string, ReactNode][] = []
  if (d.guardrail) facts.push([tr('Guardrail'), GUARDRAIL_LABELS[d.guardrail] ?? d.guardrail])
  if (d.mode) facts.push([tr('Mode'), MODE_LABELS[d.mode] ?? d.mode])
  if (d.key) facts.push([tr('Key'), <span className="mono">{d.key}</span>])
  if (d.job) facts.push([tr('Job'), <Link to={`/jobs/${d.summary_fields?.job?.type ?? 'job'}/${d.job}`}>#{d.job}</Link>])
  if (d.approval) facts.push([tr('Approval'), <Link to="/approvals">#{d.approval}</Link>])
  if (d.check_outcome) {
    const v = checkOutcomeView(d.check_outcome)
    facts.push([
      tr('Fix'),
      <Link to={`/event-rules/${d.rule}`} className="status" title={tr('Check {id}', { id: d.check })}>
        <Cell state={v.cell} />
        {v.label}
      </Link>,
    ])
  }
  if (cleared) facts.push([tr('Cleared by'), <Link to={`/event-streams/${cleared.stream}/events/${cleared.sequence}`}>#{cleared.sequence}</Link>])
  if (d.event_id) facts.push([tr('Event ID'), <span className="mono">{d.event_id}</span>])
  if (d.node) facts.push([tr('Node'), <span className="mono">{d.node}</span>])
  if (d.trace_id) facts.push([tr('Trace'), <span className="mono">{d.trace_id}</span>])
  return (
    <div className="decision-body stack" style={{ gap: 10 }}>
      {d.reason && <p style={{ margin: 0 }}>{d.reason}</p>}
      {miss ? <MissView miss={miss as MissDetails} filter={filter} /> : null}
      {Array.isArray(condMisses) ? <ConditionsMiss misses={condMisses as MissDetails[]} conditions={conditions} /> : null}
      {window?.kind && window.events > 0 ? (
        <div className="stack" style={{ gap: 4 }}>
          <div className="small muted">{tr('The key’s window: {pattern}', { pattern: PATTERN_LABELS[window.kind] ?? window.kind })}</div>
          <WindowProgress p={window} />
        </div>
      ) : null}
      {facts.length > 0 && (
        <dl className="facts">
          {facts.map(([k, v]) => (
            <div key={k}>
              <dt>{k}</dt>
              <dd>{v}</dd>
            </div>
          ))}
        </dl>
      )}
      {Object.keys(rest).length > 0 && <pre className="code">{pretty(rest)}</pre>}
    </div>
  )
}

/** A rule's or an event's decisions, newest first, each opening on what it
 * recorded. */
export function DecisionList({ path, by, filter, conditions, empty }: { path: string; by: 'rule' | 'event'; filter?: string; conditions?: string[]; empty: ReactNode }) {
  const [kind, setKind] = useState('')
  const [q, setQ] = useState('')
  const [page, setPage] = useState(1)
  const outcomes = OUTCOME_FILTERS.find((o) => o.value === kind)?.outcomes ?? []
  const pageSize = 50
  const list = useList<Any>(path, { order_by: '-received,-sequence,-id', page, page_size: pageSize, search: q || undefined, outcome__in: outcomes.length ? outcomes.join(',') : undefined }, { refetchInterval: 10_000 })
  const rows = list.data?.results
  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row wrap" style={{ gap: 12 }}>
        <div className="segmented outcome-filter" role="group" aria-label={tr('Outcomes')}>
          {OUTCOME_FILTERS.map((o) => (
            <label key={o.value} className={o.value === kind ? 'on' : ''}>
              <input
                type="radio"
                name={`outcomes-${path}`}
                checked={o.value === kind}
                onChange={() => {
                  setKind(o.value)
                  setPage(1)
                }}
              />
              {o.label}
            </label>
          ))}
        </div>
        <div className="grow" style={{ minWidth: 220 }}>
          <FilterBox
            value={q}
            onChange={(v) => {
              setQ(v)
              setPage(1)
            }}
            placeholder={tr('Filter by key, event or reason…')}
          />
        </div>
      </div>
      {list.error ? (
        <ErrorNote error={list.error} />
      ) : !rows ? (
        <Loading rows={4} />
      ) : !rows.length ? (
        kind || q ? <Empty title={tr('No decision matches')} /> : empty
      ) : (
        <div className="decisions" role="list" aria-label={tr('Decisions')}>
          {rows.map((d) => (
            <details key={d.id} className="decision" role="listitem">
              <summary>
                <Outcome outcome={d.outcome} />
                <span className="decision-what ellipsis">
                  {by === 'event' ? (
                    <Link to={`/event-rules/${d.rule}/decisions`} onClick={(e) => e.stopPropagation()}>
                      {d.summary_fields?.rule?.name ?? asIs(`#${d.rule}`)}
                    </Link>
                  ) : (
                    <Link to={`/event-streams/${d.stream}/events/${d.sequence}`} onClick={(e) => e.stopPropagation()}>
                      {d.summary_fields?.stream?.name ?? asIs(`#${d.stream}`)} <span className="mono faint">#{d.sequence}</span>
                    </Link>
                  )}
                  <span className="mono faint small"> {d.event_type}</span>
                  {d.check_outcome && <span className="tag decision-check">{checkOutcomeView(d.check_outcome).label}</span>}
                </span>
                <span className="decision-key mono small ellipsis">{d.key || ''}</span>
                <span className="decision-reason small muted ellipsis">{d.reason}</span>
                <span className="small faint nowrap">
                  <Ago at={d.created ?? d.received} />
                </span>
              </summary>
              <DecisionBody d={d} filter={filter} conditions={conditions} />
            </details>
          ))}
        </div>
      )}
      {list.data && list.data.count > pageSize && (
        <div className="table-foot">
          <span>{trn(list.data.count, '{n} decision', '{n} decisions')}</span>
          <Pager count={list.data.count} page={page} pageSize={pageSize} onPage={setPage} />
        </div>
      )}
    </div>
  )
}
