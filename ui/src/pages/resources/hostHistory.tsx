import { Fragment, useMemo } from 'react'
import { Link } from 'react-router'
import { Diffs } from '../../components/Diffs'
import { Ago, Cell, ErrorNote, Loading } from '../../components/ui'
import type { CellState } from '../../lib/cells'
import { taskDiffs } from '../../lib/diff'
import { absolute, clockTime, dayName, plural, weekdayTime } from '../../lib/format'
import {
  flaggedTasks,
  historyBuckets,
  hostFlags,
  rankedFlags,
  taskCells,
  taskName,
  tinted,
  type HistoryCell,
  type HistoryFlag,
  type HistoryTask,
  type HostHistory,
} from '../../lib/hostHistory'
import { tr } from '../../lib/i18n'

const BUCKET_HOURS = 4

const word: Partial<Record<CellState, string>> = { ok: tr('ok'), changed: tr('changed'), failed: tr('failed'), unreachable: tr('unreachable'), skipped: tr('skipped'), empty: tr('no run') }

const jobLink = (id: number | null | undefined) => (id ? `/jobs/job/${id}` : '')

/** The pills by the host's name: failing, not idempotent, drifted. */
export function HostFlagPills({ host, history }: { host: string; history?: HostHistory }) {
  const f = hostFlags(history)
  return (
    <>
      {f.failingSince && (
        <Link to={jobLink(f.failingSince)} className="flag fail">
          {tr('Failing since #{job}', { job: f.failingSince })}
        </Link>
      )}
      {f.drifted > 0 && (
        <Link to={`/drift?q=${encodeURIComponent(`host:${host}`)}`} className="flag warn" title={tr('Drifted since {when}', { when: absolute(f.driftSince) })}>
          {plural(f.drifted, 'task drifted', 'tasks drifted')}
        </Link>
      )}
      {f.notIdempotent > 0 && <span className="flag warn">{plural(f.notIdempotent, 'task not idempotent', 'tasks not idempotent')}</span>}
    </>
  )
}

function flagPill(f: HistoryFlag) {
  switch (f.type) {
    case 'new_failure':
      return <span className="flag fail">{tr('new failure')}</span>
    case 'not_idempotent':
      return <span className="flag warn">{tr('changes every run')}</span>
    case 'drift':
      return f.open ? (
        <span className="flag warn" title={absolute(f.since)}>
          {tr('drifted')}
        </span>
      ) : (
        <span className="flag warn" title={absolute(f.at)}>
          {tr('drift {when}', { when: weekdayTime(f.at) })}
        </span>
      )
  }
}

function flagWords(f: HistoryFlag): string {
  switch (f.type) {
    case 'new_failure':
      return tr('new failure')
    case 'not_idempotent':
      return tr('changes every run')
    case 'drift':
      return f.open ? tr('drifted') : tr('drift {when}', { when: weekdayTime(f.at) })
  }
}

function HistoryCellView({ task, cell }: { task: HistoryTask; cell: HistoryCell }) {
  if (!cell.open) return <Cell state="empty" size="fill" />
  const when = `${weekdayTime(cell.start)}–${clockTime(cell.end)}`
  const runs = cell.results.length > 1 ? ` · ${plural(cell.results.length, 'run')}` : ''
  const check = cell.open.check_mode ? (cell.state === 'changed' ? ` ${tr('(check mode: would change)')}` : ` ${tr('(check mode)')}`) : ''
  const label = `${taskName(task)} · ${when} · ${word[cell.state] ?? cell.state}${check}${runs} · #${cell.open.job}`
  return <Cell state={cell.state} size="fill" to={jobLink(cell.open.job)} label={label} className={cell.open.check_mode ? 'check-mode' : undefined} />
}

/** Every task that touched the host, by template, in 4-hour blocks over the last days. */
export function TaskHistory({ history, loading, error }: { history?: HostHistory; loading: boolean; error: unknown }) {
  const buckets = useMemo(() => (history ? historyBuckets(Date.parse(history.until), BUCKET_HOURS, (history.days * 24) / BUCKET_HOURS) : []), [history])
  const head = (
    <div className="panel-head">
      <h2>{tr('Task history · last {n} days', { n: history?.days ?? 5 })}</h2>
      <span className="small muted">{tr('Every task that touched this host, across templates. One column = 4 h.')}</span>
      <div className="legend" style={{ marginLeft: 'auto' }}>
        {(['ok', 'changed', 'failed', 'empty'] as const).map((s) => (
          <span key={s}>
            <Cell state={s} />
            {word[s]}
          </span>
        ))}
        <span>
          <Cell state="changed" className="check-mode" />
          {tr('check mode')}
        </span>
      </div>
    </div>
  )
  let body
  if (loading) body = <Loading rows={3} />
  else if (error) body = <ErrorNote error={error} />
  else if (!history?.templates.length)
    body = (
      <p className="muted small" style={{ margin: 0 }}>
        {history?.retention_days === 0
          ? tr('The host history is off: HOST_HISTORY_DAYS is 0 in the job settings.')
          : tr('No job ran a task on this host in the last {n} days.', { n: history?.days ?? 5 })}
      </p>
    )
  else
    body = (
      <div className="hist">
        <div className="hist-row hist-days mono" aria-hidden="true">
          <span />
          {buckets.map((b) => (
            <span key={b}>{new Date(b).getHours() < BUCKET_HOURS ? dayName(b) : ''}</span>
          ))}
          <span />
        </div>
        {history.templates.map((tpl) => (
          <Fragment key={tpl.id}>
            <div className="hist-template mono">
              {tpl.name ? <Link to={`/templates/job_template/${tpl.id}`}>{tpl.name}</Link> : tr('Deleted template')}
            </div>
            {tpl.tasks.map((t) => {
              const flags = rankedFlags(t)
              const cells = taskCells(t, buckets, BUCKET_HOURS)
              return (
                <div key={`${t.play}\u0000${t.role}\u0000${t.task}`} className={`hist-row${tinted(t) ? ' tint' : ''}`} role="group" aria-label={[taskName(t), ...flags.map(flagWords)].join(', ')}>
                  <span className="hist-name" title={t.play ? tr('Play: {play}', { play: t.play }) : undefined}>
                    {taskName(t)}
                  </span>
                  {cells.map((c) => (
                    <HistoryCellView key={c.start} task={t} cell={c} />
                  ))}
                  <span className="hist-flag">{flags.length > 0 && flagPill(flags[0])}</span>
                </div>
              )
            })}
          </Fragment>
        ))}
      </div>
    )
  return (
    <section className="panel" aria-label={tr('Task history')}>
      {head}
      <div className="panel-body">{body}</div>
    </section>
  )
}

/** One sentence per flag, each with the run to open. */
export function MatrixNotes({ history }: { history?: HostHistory }) {
  const notes = flaggedTasks(history)
  if (!notes.length) return null
  return (
    <section className="panel">
      <div className="panel-head">
        <h2>{tr('What the matrix is telling you')}</h2>
      </div>
      <div className="panel-body notes">
        {notes.map(({ template, task, flag }, i) => (
          <div key={i}>
            <Cell state={flag.type === 'new_failure' ? 'failed' : 'changed'} />
            <div className="grow" style={{ minWidth: 0 }}>
              <Note template={template.name} task={task} flag={flag} />
            </div>
          </div>
        ))}
      </div>
    </section>
  )
}

function Note({ template, task, flag }: { template: string; task: HistoryTask; flag: HistoryFlag }) {
  const name = <strong>{taskName(task)}</strong>
  const tpl = template ? <span className="faint"> · {template}</span> : null
  switch (flag.type) {
    case 'new_failure':
      return (
        <>
          {name}{' '}
          {flag.failures === 1
            ? tr('failed in its latest run, after succeeding in #{ok}.', { ok: flag.last_success })
            : tr('failed in its last {n} runs, since #{job}, after succeeding in #{ok}.', { n: flag.failures, job: flag.job, ok: flag.last_success })}{' '}
          <Link to={jobLink(flag.job)}>{tr('Open #{job}', { job: flag.job })}</Link>
          {tpl}
        </>
      )
    case 'not_idempotent':
      return (
        <>
          {name} {tr('reported changed on {n} of {n} runs: it probably writes something new every time, so it isn\'t idempotent.', { n: flag.runs })}
          {tpl}
        </>
      )
    case 'drift':
      if (!flag.open)
        return (
          <>
            {name}{' '}
            {tr('changed in #{job}, {when}, between two ok runs and with no change to the template, its project\'s revision or its variables: someone changed the host outside Ansible, and the run put it back.', {
              job: flag.job,
              when: weekdayTime(flag.at),
            })}{' '}
            <Link to={jobLink(flag.job)}>{tr('Open #{job}', { job: flag.job })}</Link>
            {tpl}
          </>
        )
      return (
        <>
          {name} {tr('would change this host: a check-mode run found it')} <Ago at={flag.since} />
          {'. '}
          {tr('It stays drifted until a run finds nothing to change.')}{' '}
          {flag.last_job && <Link to={jobLink(flag.last_job)}>{tr('Open #{job}', { job: flag.last_job })}</Link>}
          {tpl}
          <DriftDiff diff={flag.diff} />
        </>
      )
  }
}

/** What a check-mode run would change, from its diff mode. */
export function DriftDiff({ diff }: { diff: unknown }) {
  if (diff && typeof diff === 'object' && (diff as Record<string, unknown>).too_large)
    return <p className="small muted" style={{ margin: '6px 0 0' }}>{tr('The diff is too large to keep: see the run\'s output.')}</p>
  const diffs = taskDiffs(diff)
  if (!diffs.length) return <p className="small muted" style={{ margin: '6px 0 0' }}>{tr('No diff: run the template with diff mode on to see what would change.')}</p>
  return <Diffs diffs={diffs} />
}
