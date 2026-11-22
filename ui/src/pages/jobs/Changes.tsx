import { useEffect, useState, type ReactNode } from 'react'
import { Link, useLocation, useNavigate, useSearchParams } from 'react-router'
import { useObject } from '../../api/hooks'
import type { Job } from '../../api/types'
import { Icon } from '../../components/icons'
import { Menu } from '../../components/overlay'
import { Ago, Empty, ErrorNote, Loading } from '../../components/ui'
import { changesPath, commitsMissing, commitsShown, comparedWith, diffText, fieldName, inputValue, multiline, sections, summarize, type FieldChange, type JobChanges, type ObjectChange, type Section } from '../../lib/changes'
import { diffLines } from '../../lib/diff'
import { jobPath, plural, shortSha, templatePath } from '../../lib/format'
import { tr, trn } from '../../lib/i18n'
import { Ref, Row } from '../templates/History'

/** Whether a job has a Changes tab: a finished run of a template. */
export function hasChanges(job: Job): boolean {
  return (job.type === 'job' || job.type === 'workflow_job') && !!job.unified_job_template && !!job.finished
}

export function useChanges(job: Job, since?: string | null) {
  return useObject<JobChanges>(hasChanges(job) ? changesPath(job, since) : null, { staleTime: 30_000, retry: false })
}

/** "Last success of this template", and what changed since, where a failure is shown. */
export function ChangesSummary({ job }: { job: Job }) {
  const q = useChanges(job)
  const c = q.data
  const last = c?.last_success
  const s = summarize(c)
  const tab = `${jobPath(job)}/changes`
  return (
    <div className="changes-summary">
      <p>
        {tr('Last success of this template:')}{' '}
        {last ? (
          <>
            <Link to={jobPath(last)}>#{last.id}</Link>, <Ago at={last.finished} />
          </>
        ) : q.isLoading ? (
          '…'
        ) : (
          tr('none before this run')
        )}
      </p>
      {s?.nothing && <p>{s.nothing}</p>}
      {s && !s.nothing && (
        <>
          <p className="cs-head">{s.since.id === last?.id ? tr('What changed since #{id}', { id: s.since.id }) : tr('What changed since #{id}, the previous run', { id: s.since.id })}</p>
          <ul className="cs-lines">
            {s.lines.map((l) => (
              <li key={l.anchor + l.text}>
                <Link to={`${tab}#${l.anchor}`}>
                  <span className="cs-label">{l.label}</span>
                  <span className="cs-text">
                    {l.text}
                    {l.by && <>, {tr('by {name}', { name: l.by })}</>}
                    {l.at && (
                      <>
                        , <Ago at={l.at} />
                      </>
                    )}
                  </span>
                </Link>
              </li>
            ))}
          </ul>
          {s.more && <Link to={tab}>{trn(s.count, 'See all {n} change →', 'See all {n} changes →')}</Link>}
        </>
      )}
    </div>
  )
}

/** The job page's Changes tab: what differs from an earlier run of its template. */
export function ChangesTab({ job }: { job: Job }) {
  const [sp] = useSearchParams()
  const since = sp.get('since')
  const q = useChanges(job, since)
  const loc = useLocation()
  const c = q.data
  useEffect(() => {
    if (!c || !loc.hash) return
    document.getElementById(loc.hash.slice(1))?.scrollIntoView({ block: 'start' })
  }, [c, loc.hash])
  if (q.isPending && !q.error) return <div className="page-body"><Loading /></div>
  return (
    <div className="page-body">
      <div className="row wrap" style={{ gap: 10, alignItems: 'center' }}>
        {c?.compared_with ? (
          <p style={{ margin: 0 }} className="grow">
            {comparedWith(c)} <Ago at={c.compared_with.finished} />
            {c.count > 0 && <span className="muted"> · {plural(c.count, 'change')}</span>}
          </p>
        ) : (
          <span className="grow" />
        )}
        {(c?.last_success || c?.previous || since) && <CompareWith job={job} c={c} />}
      </div>
      <ErrorNote error={q.error} />
      {c && <ChangesBody c={c} />}
    </div>
  )
}

function CompareWith({ job, c }: { job: Job; c?: JobChanges }) {
  const navigate = useNavigate()
  const [n, setN] = useState('')
  const go = (id: number | null) => navigate({ pathname: `${jobPath(job)}/changes`, search: id ? `?since=${id}` : '' })
  return (
    <Menu
      label={tr('Compare with')}
      className="btn"
      trigger={
        <>
          {tr('Compare with')} <Icon name="chevron" size={14} />
        </>
      }
    >
      {c?.last_success && <button onClick={() => go(null)}>{tr('The last success, #{id}', { id: c.last_success.id })}</button>}
      {c?.previous && c.previous.id !== c.last_success?.id && <button onClick={() => go(c.last_success ? c.previous!.id : null)}>{tr('The previous run, #{id}', { id: c.previous.id })}</button>}
      <form className="row" style={{ gap: 6, padding: '6px 10px' }} onSubmit={(e) => e.preventDefault()}>
        <label htmlFor="since" className="sr-only">
          {tr('Run number')}
        </label>
        <input id="since" className="input mono" inputMode="numeric" placeholder={tr('Run #')} value={n} onChange={(e) => setN(e.target.value)} style={{ width: 110, height: 30 }} />
        {/* Enter clicks this button, and a click in the menu closes it, form included: so it navigates on the click. */}
        <button
          type="submit"
          className="btn sm"
          onClick={(e) => {
            e.preventDefault()
            const id = Number(n.trim().replace(/^#/, ''))
            if (id) go(id)
          }}
        >
          {tr('Compare')}
        </button>
      </form>
    </Menu>
  )
}

function ChangesBody({ c }: { c: JobChanges }) {
  if (!c.compared_with) return <Empty title={tr('Nothing to compare with')}>{tr('This is the first finished run of its template.')}</Empty>
  const shown = sections(c)
  if (!c.count && !shown.length) {
    return (
      <Empty title={tr('Nothing changed')}>
        {summarize(c)?.nothing} {tr('The cause is likely outside awxnext: the target hosts, the network or a cloud API.')}
      </Empty>
    )
  }
  const render: Record<Section, () => ReactNode> = {
    project: () => <ProjectSection c={c} />,
    template: () => <ObjectSection id="template" title={c.job.type === 'workflow_job' ? tr('Workflow') : tr('Template')} o={c.template!} history />,
    inventory: () => <ObjectSection id="inventory" title={tr('Inventory')} o={c.inventory!} to={`/inventories/${c.inventory!.id}`} />,
    credentials: () => (
      <>
        {c.credentials.map((cr) => (
          <ObjectSection key={cr.id} id={`credential-${cr.id}`} title={tr('Credential')} o={cr} to={`/credentials/${cr.id}`} />
        ))}
      </>
    ),
    inputs: () => <InputsSection c={c} />,
    execution_environment: () => <ImageSection c={c} />,
    hosts: () => <HostsSection c={c} />,
    nodes: () => <NodesSection c={c} />,
  }
  return (
    <>
      {shown.map((s) => (
        <div key={s}>{render[s]()}</div>
      ))}
    </>
  )
}

function Block({ id, title, meta, children }: { id: string; title: ReactNode; meta?: ReactNode; children: ReactNode }) {
  return (
    <section id={id} className="panel change-block" aria-labelledby={`${id}-h`}>
      <div className="panel-head">
        <h2 id={`${id}-h`}>{title}</h2>
        {meta && <div className="small muted" style={{ marginLeft: 'auto' }}>{meta}</div>}
      </div>
      <div className="change-body">{children}</div>
    </section>
  )
}

function ProjectSection({ c }: { c: JobChanges }) {
  const p = c.project!
  const shownOf = commitsShown(p)
  const missing = commitsMissing(p)
  const revs = (
    <span className="mono">
      {shortSha(p.before.revision) || tr('none')} → {shortSha(p.after.revision) || tr('none')}
      {p.total_commits !== undefined && <> · {plural(p.total_commits, 'commit')}</>}
    </span>
  )
  return (
    <Block id="project" title={<>{tr('Project')} · {p.id ? <Link to={`/projects/${p.id}`}>{p.name}</Link> : p.name}</>} meta={p.before.revision !== p.after.revision ? revs : undefined}>
      {p.other_project && (
        <p className="small" style={{ margin: 0 }}>
          {tr('The earlier run used another project:')} <Link to={`/projects/${p.other_project.id}`}>{p.other_project.name}</Link>
        </p>
      )}
      {!p.readable && <p className="small muted" style={{ margin: 0 }}>{tr('You cannot read this project: only its revisions are shown.')}</p>}
      {missing && <p className="small muted" style={{ margin: 0 }}>{missing}</p>}
      {p.diverged && <p className="small muted" style={{ margin: 0 }}>{tr('The newer revision does not descend from the older one: the commits listed are those only the newer one has.')}</p>}
      {p.commits.length > 0 && (
        <table className="data commits" aria-label={tr('Commits')}>
          <tbody>
            {p.commits.map((k) => (
              <tr key={k.sha}>
                <td className="mono" title={k.sha}>
                  {shortSha(k.sha)}
                </td>
                <td className="grow">{k.message}</td>
                <td className="muted">{k.author}</td>
                <td className="muted">
                  <Ago at={k.date} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
      {shownOf && <p className="small faint" style={{ margin: 0 }}>{shownOf}</p>}
      {p.readable && !!p.versions?.length && <Edits o={p} />}
    </Block>
  )
}

function ObjectSection({ id, title, o, to, history }: { id: string; title: string; o: ObjectChange; to?: string; history?: boolean }) {
  const last = o.versions?.[o.versions.length - 1]
  const historyLink = history && o.readable && o.id && o.from_version && o.to_version ? `${templatePath({ id: o.id, type: o.kind })}/history?from=${o.from_version}&to=${o.to_version}` : null
  const name = to && o.readable ? <Link to={to}>{o.name}</Link> : history && o.id ? <Link to={templatePath({ id: o.id, type: o.kind })}>{o.name}</Link> : o.name
  return (
    <Block
      id={id}
      title={
        <>
          {title} · {name}
        </>
      }
      meta={
        last && (
          <>
            {last.actor?.username ?? tr('the system')}, <Ago at={last.created} />
            {historyLink && (
              <>
                {' · '}
                <Link to={historyLink}>{tr('View in History →')}</Link>
              </>
            )}
          </>
        )
      }
    >
      {o.readable ? <Edits o={o} /> : <p className="small muted" style={{ margin: 0 }}>{tr('It changed between the runs. You cannot read it, so the change is not shown.')}</p>}
    </Block>
  )
}

function Edits({ o }: { o: ObjectChange }) {
  const fields = o.fields ?? []
  const flat = fields.filter((f) => !multiline(f))
  const long = fields.filter(multiline)
  return (
    <>
      <ul className="change-versions small muted">
        {(o.versions ?? []).map((v) => (
          <li key={v.version}>
            v{v.version} · {v.actor?.username ?? tr('the system')} · <Ago at={v.created} />
            {v.comment && <> · “{v.comment}”</>}
          </li>
        ))}
      </ul>
      {flat.length > 0 && (
        <div className="changes">
          <div className="head">{tr('field')}</div>
          <div className="head">{tr('before')}</div>
          <div className="head">{tr('after')}</div>
          {flat.map((f) => (
            <Row key={fieldName(f)} name={fieldName(f)} before={<FieldValue f={f} v={f.before} />} after={<FieldValue f={f} v={f.after} />} same={false} />
          ))}
        </div>
      )}
      {long.map((f) => (
        <div key={fieldName(f)}>
          <div className="mono small" style={{ marginBottom: 6 }}>
            {fieldName(f)}
          </div>
          <Diff before={diffText(f.before)} after={diffText(f.after)} label={tr('{field} diff', { field: fieldName(f) })} />
        </div>
      ))}
      {!fields.length && <p className="small muted" style={{ margin: 0 }}>{tr('Edited and set back: the fields are the same as in the earlier run.')}</p>}
    </>
  )
}

function FieldValue({ f, v }: { f: FieldChange; v: unknown }) {
  if (f.secret) return <span className="muted">{tr('secret')}</span>
  if (f.key) return <span className="mono">{inputValue(v)}</span>
  return <Ref field={f.field} v={v} />
}

function Diff({ before, after, label }: { before: string; after: string; label: string }) {
  return (
    <div className="diff" role="table" aria-label={label}>
      {diffLines(before, after).map((l, i) => (
        <div key={i} role="row" className={l.kind === 'ctx' ? '' : l.kind}>
          <span className="n">{l.a ?? ''}</span>
          <span className="n">{l.b ?? ''}</span>
          <span className="m">{l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ' '}</span>
          <span>{l.text}</span>
        </div>
      ))}
    </div>
  )
}

function InputsSection({ c }: { c: JobChanges }) {
  return (
    <Block id="inputs" title={tr('Launch inputs')}>
      <div className="changes">
        <div className="head">{tr('input')}</div>
        <div className="head">#{c.compared_with!.id}</div>
        <div className="head">#{c.job.id}</div>
        {c.inputs.map((f) => (
          <Row key={fieldName(f)} name={fieldName(f)} before={f.secret ? <span className="muted">{tr('secret')}</span> : <span className="mono">{inputValue(f.before)}</span>} after={f.secret ? <span className="muted">{tr('secret, changed')}</span> : <span className="mono">{inputValue(f.after)}</span>} same={false} />
        ))}
      </div>
    </Block>
  )
}

function ImageSection({ c }: { c: JobChanges }) {
  const e = c.execution_environment!
  return (
    <Block id="execution_environment" title={<>{tr('Execution environment')}{e.name ? <> · {e.id ? <Link to={`/execution-environments/${e.id}`}>{e.name}</Link> : e.name}</> : null}</>} meta={e.same_tag ? <span className="badge run">{tr('same tag, new image')}</span> : undefined}>
      <div className="changes">
        <div className="head" />
        <div className="head">#{c.compared_with!.id}</div>
        <div className="head">#{c.job.id}</div>
        <Row name={tr('image')} before={<span className="mono">{e.before.image || tr('(none)')}</span>} after={<span className="mono">{e.after.image || tr('(none)')}</span>} same={e.before.image === e.after.image} />
        <Row name={tr('digest')} before={<span className="mono">{e.before.digest || tr('unknown')}</span>} after={<span className="mono">{e.after.digest || tr('unknown')}</span>} same={e.before.digest === e.after.digest || !e.before.digest || !e.after.digest} />
      </div>
      {e.readable && !!e.versions?.length && <Edits o={e} />}
    </Block>
  )
}

function HostsSection({ c }: { c: JobChanges }) {
  const h = c.hosts!
  const more = (shown: number, total: number) => (total > shown ? tr(' and {n} more', { n: total - shown }) : '')
  return (
    <Block id="hosts" title={tr('Hosts')} meta={tr('{before} → {after} hosts', { before: h.before_count, after: h.after_count })}>
      {h.added_count > 0 && (
        <p className="mono small ok-text" style={{ margin: 0 }}>
          + {h.added.join(', ')}
          {more(h.added.length, h.added_count)}
        </p>
      )}
      {h.removed_count > 0 && (
        <p className="mono small fail-text" style={{ margin: 0 }}>
          − {h.removed.join(', ')}
          {more(h.removed.length, h.removed_count)}
        </p>
      )}
    </Block>
  )
}

function NodesSection({ c }: { c: JobChanges }) {
  const word: Record<string, string> = { added: tr('added'), removed: tr('removed'), template: tr('another template') }
  return (
    <Block id="nodes" title={tr('Nodes')}>
      <table className="data" aria-label={tr('Nodes')}>
        <thead>
          <tr>
            <th>{tr('Node')}</th>
            <th>{tr('Difference')}</th>
            <th>#{c.compared_with!.id}</th>
            <th>#{c.job.id}</th>
            <th />
          </tr>
        </thead>
        <tbody>
          {c.nodes.map((n) => {
            const jobs = n.before_job && n.after_job && n.before_job.type === n.after_job.type && (n.after_job.type === 'job' || n.after_job.type === 'workflow_job')
            return (
              <tr key={n.identifier}>
                <td>
                  {n.template ? n.template.name : <span className="muted">{tr('(no template)')}</span>}
                  {n.change === 'template' && n.before_template && <div className="small muted">{tr('was {name}', { name: n.before_template.name })}</div>}
                </td>
                <td>{n.change ? <span className="badge">{word[n.change]}</span> : <span className="faint">—</span>}</td>
                <td>{n.before_job ? <Link to={jobPath(n.before_job)}>#{n.before_job.id}</Link> : <span className="faint">—</span>}</td>
                <td>{n.after_job ? <Link to={jobPath(n.after_job)}>#{n.after_job.id}</Link> : <span className="faint">—</span>}</td>
                <td>{jobs && <Link to={`${jobPath(n.after_job!)}/changes?since=${n.before_job!.id}`}>{tr('Its changes →')}</Link>}</td>
              </tr>
            )
          })}
        </tbody>
      </table>
    </Block>
  )
}
