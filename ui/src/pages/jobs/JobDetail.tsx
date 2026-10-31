import { useQueryClient } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { Link, Navigate, Route, Routes, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useList, useObject, useUiSettings } from '../../api/hooks'
import { useLive } from '../../api/live'
import type { HostSummary, Job } from '../../api/types'
import { varsText } from '../../components/form'
import { Icon } from '../../components/icons'
import { Menu, useConfirm, useToast } from '../../components/overlay'
import { Ago, Cell, Crumbs, Elapsed, Empty, ErrorNote, KV, Loading, Maybe, Panel, StatusBadge, Tab } from '../../components/ui'
import { absolute, active, jobApi, jobPath, plural, shortSha, templatePath, typeLabel, userName } from '../../lib/format'
import { useRelaunch } from '../../components/RelaunchDialog'
import { WorkflowJobGraph } from '../automation/workflow'
import { DEFAULT_MAX_EVENTS, firstFailure, toGrid, toLines, useJobEvents } from './events'
import { Matrix } from './Matrix'
import { Output } from './Output'
import { number, tr } from '../../lib/i18n'

const templateKind: Record<string, string> = { job: 'job_template', workflow_job: 'workflow_job_template', project_update: 'project', inventory_update: 'inventory_source', system_job: 'system_job_template' }

export function JobDetail() {
  const { type = 'job', id } = useParams()
  const jid = Number(id)
  const path = jobApi(type, jid)
  const qc = useQueryClient()
  const job = useObject<Job>(path, { refetchInterval: (q) => (q.state.data && active(q.state.data.status) ? 10_000 : false) })
  // status_changed as it runs; summary once all its events are stored.
  useLive({ jobs: ['status_changed', 'summary'] }, (m) => {
    if (m.unified_job_id === jid) qc.invalidateQueries({ predicate: (q) => typeof q.queryKey[0] === 'string' && q.queryKey[0].startsWith(path) })
  })
  if (type === 'workflow_approval') return <Navigate to="/approvals" replace />
  if (job.isPending) return <div className="page"><Loading /></div>
  if (job.error || !job.data) return <div className="page page-body"><ErrorNote error={job.error} /></div>
  return <JobView key={path} type={type} job={job.data} />
}

function JobView({ type, job }: { type: string; job: Job }) {
  const nav = useNavigate()
  const toast = useToast()
  const confirm = useConfirm()
  const running = active(job.status)
  const isWorkflow = type === 'workflow_job'
  const base = jobPath(job)
  const summaries = useList<HostSummary>(type === 'job' ? `jobs/${job.id}/job_host_summaries/` : null, { page_size: 200, order_by: 'host_name' }, { refetchInterval: running ? 8000 : false })
  const failedHosts = (summaries.data?.results ?? []).filter((h) => h.failed || h.dark > 0)
  const sf = job.summary_fields
  const ujtType = templateKind[type]
  const extra = varsText(job.extra_vars)
  const shortVars = Object.entries((() => { try { return JSON.parse(job.extra_vars || '{}') as Record<string, unknown> } catch { return {} } })())
    .filter(([, v]) => typeof v !== 'object')
    .slice(0, 3)
    .map(([k, v]) => `${k}=${v}`)
    .join(' ')

  const { relaunch: relaunchAs, dialog: relaunchDialog } = useRelaunch()
  const relaunch = (body: Record<string, unknown> = {}) => relaunchAs(job, body)
  const cancel = async () => {
    if (!(await confirm({ title: tr('Cancel #{id}?', { id: job.id }), body: tr('The playbook is stopped; hosts may be left part-way through a task.'), confirm: tr('Cancel job'), danger: true }))) return
    await api.post(`${jobApi(type, job.id)}cancel/`, {}).catch((e) => toast((e as Error).message, 'error'))
  }
  const remove = async () => {
    if (!(await confirm({ title: tr('Delete #{id}?', { id: job.id }), body: tr('Its output and events are removed.'), confirm: tr('Delete'), danger: true }))) return
    await api.del(jobApi(type, job.id))
    nav('/jobs')
  }

  return (
    <div className="page ground">
      {relaunchDialog}
      <section style={{ borderBottom: '1px solid var(--line)', padding: '18px 24px 0' }}>
        <Crumbs items={[{ label: tr('Jobs'), to: '/jobs' }, { label: tr('#{id}', { id: job.id }) }]} />
        <div className="row wrap" style={{ gap: 12, marginTop: 6 }}>
          <h1>{job.name}</h1>
          <StatusBadge status={job.status}>
            {job.status === 'failed' && failedHosts.length > 0 && summaries.data ? tr('Failed on {count} of {count2}', { count: failedHosts.length, count2: plural(summaries.data.count, 'host') }) : undefined}
          </StatusBadge>
          <span className="small muted">{typeLabel(type)}</span>
          <div style={{ marginLeft: 'auto' }} className="row wrap">
            {running && (
              <button className="btn" onClick={cancel}>
                <Icon name="stop" size={10} />
                {tr('Cancel')}
              </button>
            )}
            {!running && type !== 'workflow_approval' && (
              <button className="btn" onClick={() => relaunch()}>
                <Icon name="refresh" size={13} />
                {tr('Relaunch')}
              </button>
            )}
            {!running && type === 'job' && failedHosts.length > 0 && (
              <button className="btn primary" onClick={() => relaunch({ hosts: 'failed' })}>
                {tr('Relaunch on')} {plural(failedHosts.length, 'failed host')}
              </button>
            )}
            <Menu label={tr('More actions')}>
              {ujtType && job.unified_job_template && <Link to={templatePath({ id: job.unified_job_template, type: ujtType })}>{tr('Open template')}</Link>}
              {!isWorkflow && <a href={`/api/v2/${jobApi(type, job.id)}stdout/?format=txt_download`}>{tr('Download output')}</a>}
              <a href={`/api/v2/${jobApi(type, job.id)}`} target="_blank" rel="noreferrer">
                {tr('View in API')}
              </a>
              {!running && (
                <>
                  <hr />
                  <button className="danger" onClick={remove}>
                    {tr('Delete')}
                  </button>
                </>
              )}
            </Menu>
          </div>
        </div>
        <dl className="facts" style={{ marginTop: 10 }}>
          <div>
            <dt>{tr('Started')}</dt>
            <dd>{job.started ? absolute(job.started) : tr('not yet')}</dd>
          </div>
          <div>
            <dt>{tr('Duration')}</dt>
            <dd>
              <Elapsed job={job} />
            </dd>
          </div>
          <div>
            <dt>{tr('Launched by')}</dt>
            <dd className="sans">
              {job.launch_type === 'scheduled' ? tr('schedule') : job.launch_type === 'workflow' ? tr('workflow') : job.launch_type === 'webhook' ? tr('webhook') : job.launched_by?.name ?? userName(sf.created_by)}
            </dd>
          </div>
          {job.scm_revision && (
            <div>
              <dt>{tr('Revision')}</dt>
              <dd>
                {job.scm_branch ? tr('{scm_branch}@', { scm_branch: job.scm_branch }) : ''}
                {shortSha(job.scm_revision)}
              </dd>
            </div>
          )}
          {sf.inventory && (
            <div>
              <dt>{tr('Inventory')}</dt>
              <dd>
                <Link to={`/inventories/${sf.inventory.id}`}>{sf.inventory.name}</Link>
                {summaries.data && tr(' · {count}', { count: plural(summaries.data.count, 'host') })}
              </dd>
            </div>
          )}
          {job.limit && (
            <div>
              <dt>{tr('Limit')}</dt>
              <dd>{job.limit}</dd>
            </div>
          )}
          {sf.execution_environment && (
            <div>
              <dt>{tr('Env')}</dt>
              <dd>{sf.execution_environment.name}</dd>
            </div>
          )}
          {job.execution_node && (
            <div>
              <dt>{tr('Node')}</dt>
              <dd>{job.execution_node}</dd>
            </div>
          )}
          {shortVars && (
            <div>
              <dt>{tr('Vars')}</dt>
              <dd>{shortVars}</dd>
            </div>
          )}
          {job.job_slice_count > 1 && (
            <div>
              <dt>{tr('Slice')}</dt>
              <dd>
                {job.job_slice_number} {tr('of')} {job.job_slice_count}
              </dd>
            </div>
          )}
        </dl>
        <nav className="tabs" role="tablist" style={{ marginTop: 12 }}>
          <Tab to={base} end>
            {isWorkflow ? tr('Graph') : tr('Output')}
          </Tab>
          {type === 'job' && (
            <Tab to={`${base}/hosts`} n={summaries.data?.count}>
              {tr('Hosts')}
            </Tab>
          )}
          {(extra || type === 'job') && <Tab to={`${base}/variables`}>{tr('Variables')}</Tab>}
          {type === 'job' && <Tab to={`${base}/artifacts`}>{tr('Artifacts')}</Tab>}
          <Tab to={`${base}/details`}>{tr('Details')}</Tab>
        </nav>
      </section>
      <Routes>
        <Route index element={isWorkflow ? <div className="page-body"><WorkflowJobGraph jobId={job.id} /></div> : <OutputTab type={type} job={job} summaries={summaries.data?.results ?? []} />} />
        <Route path="hosts" element={<HostsTab job={job} rows={summaries.data?.results ?? []} />} />
        <Route path="variables" element={<VariablesTab job={job} />} />
        <Route path="artifacts" element={<div className="page-body"><Panel title={tr('Artifacts')}>{job.artifacts && Object.keys(job.artifacts).length ? <pre className="code">{JSON.stringify(job.artifacts, null, 2)}</pre> : <p className="muted small" style={{ margin: 0 }}>{tr('The playbook set no artifacts (set_stats with per_host: false).')}</p>}</Panel></div>} />
        <Route path="details" element={<DetailsTab type={type} job={job} />} />
      </Routes>
    </div>
  )
}

function OutputTab({ type, job, summaries }: { type: string; job: Job; summaries: HostSummary[] }) {
  const running = active(job.status)
  const ui = useUiSettings()
  const max = ui.data?.MAX_UI_JOB_EVENTS ?? DEFAULT_MAX_EVENTS
  const liveOff = ui.data?.UI_LIVE_UPDATES_ENABLED === false
  const { events, loading, truncated, held, loadMore, refresh } = useJobEvents(type, job.id, running || job.event_processing_finished === false, max)
  const lines = useMemo(() => toLines(events), [events])
  const grid = useMemo(() => toGrid(events, summaries.map((s) => s.host_name)), [events, summaries])
  const failure = useMemo(() => firstFailure(events), [events])
  const [host, setHost] = useState('')
  const [focus, setFocus] = useState<{ n: number; at: number } | null>(null)
  const showGrid = (type === 'job' || type === 'ad_hoc_command') && grid.tasks.length > 0 && grid.hosts.length > 0
  // Until the events are in, a failed job has no task to blame yet.
  const failedJob = ['failed', 'error'].includes(job.status) && !loading
  return (
    <>
      {(showGrid || failedJob) && (
        <div className="page-body" style={{ paddingBottom: 0 }}>
          {showGrid ? (
            <Matrix grid={grid} job={job} failure={failure} onHost={setHost} onJump={(n) => setFocus({ n, at: Date.now() })} />
          ) : (
            <FailurePanel job={job} failure={failure} onJump={(n) => setFocus({ n, at: Date.now() })} />
          )}
        </div>
      )}
      <div className="page-body">
        {truncated && (
          <div className="notice">
            {tr('Showing the first')} {number(held)} {tr('events (setting MAX_UI_JOB_EVENTS).')}{' '}
            <button className="link-btn" onClick={loadMore}>
              {tr('Load')} {number(max > 0 ? max : DEFAULT_MAX_EVENTS)} {tr('more')}
            </button>{' '}
            {tr('or download the full output.')}
          </div>
        )}
        {running && liveOff && (
          <div className="notice">
            {tr('Live updates are turned off (setting UI_LIVE_UPDATES_ENABLED).')}{' '}
            <button className="link-btn" onClick={refresh}>
              {tr('Show new output')}
            </button>
          </div>
        )}
        {loading && lines.length === 0 ? (
          <div className="term">
            <div className="term-empty">{tr('Loading output…')}</div>
          </div>
        ) : (
          <Output
            lines={lines}
            running={running}
            downloadUrl={`/api/v2/${jobApi(type, job.id)}stdout/?format=txt_download`}
            host={host}
            onHost={setHost}
            hosts={grid.hosts.map((h) => h.name)}
            focusLine={focus}
          />
        )}
      </div>
    </>
  )
}

function FailurePanel({ job, failure, onJump }: { job: Job; failure: ReturnType<typeof firstFailure>; onJump: (line: number) => void }) {
  const prev = useList<Job>(job.unified_job_template ? 'unified_jobs/' : null, { unified_job_template: job.unified_job_template!, status: 'successful', id__lt: job.id, order_by: '-id', page_size: 1 })
  const p = prev.data?.results[0]
  return (
    <aside aria-label={tr('Failure summary')} className="panel narrow" style={{ padding: 14, flex: '1 1 300px' }}>
      <h2 style={{ marginBottom: 8 }}>{tr('Why it failed')}</h2>
      {failure ? (
        <>
          <p style={{ margin: '0 0 10px', lineHeight: 1.5 }}>
            {plural(failure.hosts.length, 'host')} {tr('failed')} <b style={{ fontWeight: 600 }}>{failure.task}</b>
            {failure.stopped ? tr(', and the play stopped: no hosts were left to continue (max_fail_percentage or any_errors_fatal).') : '.'}
          </p>
          <pre className="code" style={{ whiteSpace: 'pre-wrap' }}>
            {failure.message}
          </pre>
          <dl className="kv small" style={{ marginTop: 12 }}>
            {failure.path && (
              <>
                <dt>{tr('Task')}</dt>
                <dd className="mono">{failure.path}</dd>
              </>
            )}
            <dt>{tr('Hosts')}</dt>
            <dd className="mono">{failure.hosts.slice(0, 8).join(', ') + (failure.hosts.length > 8 ? ` +${failure.hosts.length - 8}` : '')}</dd>
            <dt>{tr('Previous run')}</dt>
            <dd>{p ? <><Link to={jobPath(p)}>#{p.id}</Link> {tr('succeeded')} <Ago at={p.finished} /></> : tr('no earlier success')}</dd>
          </dl>
          <div className="row wrap" style={{ marginTop: 14 }}>
            <button className="btn sm" onClick={() => onJump(failure.event.start_line + 1)}>
              {tr('Show in output')}
            </button>
          </div>
        </>
      ) : (
        <>
          <p style={{ margin: '0 0 10px' }}>{job.job_explanation || tr('The job failed before any task reported a failure.')}</p>
          {job.result_traceback && <pre className="code">{String(job.result_traceback).slice(-1500)}</pre>}
          <dl className="kv small">
            <dt>{tr('Previous run')}</dt>
            <dd>{p ? <><Link to={jobPath(p)}>#{p.id}</Link> {tr('succeeded')} <Ago at={p.finished} /></> : tr('no earlier success')}</dd>
          </dl>
        </>
      )}
    </aside>
  )
}

function HostsTab({ job, rows }: { job: Job; rows: HostSummary[] }) {
  if (!rows.length) return <div className="page-body"><Panel><Empty title={active(job.status) ? tr('No host results yet') : tr('No host results')}>{tr('Results appear as hosts finish the play.')}</Empty></Panel></div>
  return (
    <div className="page-body">
      <div className="table-wrap">
        <table className="data">
          <thead>
            <tr>
              <th>{tr('Host')}</th>
              <th>{tr('Result')}</th>
              <th className="end">{tr('ok')}</th>
              <th className="end">{tr('changed')}</th>
              <th className="end">{tr('failed')}</th>
              <th className="end">{tr('unreachable')}</th>
              <th className="end">{tr('skipped')}</th>
              <th className="end">{tr('rescued')}</th>
              <th className="end">{tr('ignored')}</th>
            </tr>
          </thead>
          <tbody>
            {[...rows].sort((a, b) => Number(b.failed || b.dark > 0) - Number(a.failed || a.dark > 0) || a.host_name.localeCompare(b.host_name)).map((h) => (
              <tr key={h.id}>
                <td className="mono">{h.host ? <Link to={`/hosts/${h.host}`}>{h.host_name}</Link> : h.host_name}</td>
                <td>{h.dark > 0 ? <span className="status"><Cell state="error" />{tr('Unreachable')}</span> : h.failed ? <span className="status"><Cell state="failed" />{tr('Failed')}</span> : h.changed ? <span className="status"><Cell state="changed" />{tr('Changed')}</span> : <span className="status"><Cell state="ok" />{tr('Ok')}</span>}</td>
                <td className="end mono">{h.ok}</td>
                <td className="end mono">{h.changed}</td>
                <td className={`end mono ${h.failures ? 'fail-text' : ''}`}>{h.failures}</td>
                <td className={`end mono ${h.dark ? 'fail-text' : ''}`}>{h.dark}</td>
                <td className="end mono">{h.skipped}</td>
                <td className="end mono">{h.rescued}</td>
                <td className="end mono">{h.ignored}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}

function VariablesTab({ job }: { job: Job }) {
  const extra = varsText(job.extra_vars)
  return (
    <div className="page-body">
      <div className="split">
        <Panel title={tr('Extra variables')} className="wide">
          {extra ? <pre className="code">{extra}</pre> : <p className="muted small" style={{ margin: 0 }}>{tr('None.')}</p>}
        </Panel>
        <Panel title={tr('Launch values')} className="narrow">
          <KV
            items={[
              [tr('Limit'), <Maybe v={job.limit} mono />],
              [tr('Job type'), job.job_type],
              [tr('Tags'), <Maybe v={job.job_tags} mono />],
              [tr('Skip tags'), <Maybe v={job.skip_tags} mono />],
              [tr('Verbosity'), job.verbosity],
              [tr('Forks'), job.forks || tr('default')],
              [tr('Diff mode'), job.diff_mode ? tr('yes') : tr('no')],
              [tr('Branch'), <Maybe v={job.scm_branch} mono />],
            ]}
          />
        </Panel>
      </div>
    </div>
  )
}

function DetailsTab({ type, job }: { type: string; job: Job }) {
  const sf = job.summary_fields
  return (
    <div className="page-body">
      <div className="split">
        <Panel title={tr('Details')} className="wide">
          <KV
            items={[
              [tr('Status'), <StatusBadge status={job.status} />],
              [tr('Explanation'), job.job_explanation || null],
              [tr('Type'), typeLabel(type)],
              [tr('Template'), sf.unified_job_template ? <Link to={templatePath({ id: sf.unified_job_template.id, type: templateKind[type] ?? '' })}>{sf.unified_job_template.name}</Link> : null],
              [tr('Project'), sf.project ? <Link to={`/projects/${sf.project.id}`}>{sf.project.name}</Link> : null],
              [tr('Playbook'), job.playbook ? <span className="mono">{job.playbook}</span> : null],
              [tr('Revision'), job.scm_revision ? <span className="mono">{job.scm_revision}</span> : null],
              [tr('Inventory'), sf.inventory ? <Link to={`/inventories/${sf.inventory.id}`}>{sf.inventory.name}</Link> : null],
              [tr('Credentials'), sf.credentials?.length ? sf.credentials.map((c) => c.name).join(', ') : null],
              [tr('Execution environment'), sf.execution_environment?.name ?? null],
              [tr('Instance group'), sf.instance_group?.name ?? null],
              [tr('Execution node'), job.execution_node ? <span className="mono">{job.execution_node}</span> : null],
              [tr('Controller node'), job.controller_node ? <span className="mono">{job.controller_node}</span> : null],
              [tr('Launch type'), job.launch_type],
              [tr('Created'), absolute(job.created)],
              [tr('Started'), absolute(job.started)],
              [tr('Finished'), job.finished ? absolute(job.finished) : null],
              [tr('Working directory'), job.job_cwd ? <span className="mono">{job.job_cwd}</span> : null],
            ]}
          />
        </Panel>
        {job.job_args && (
          <Panel title={tr('Command')} className="narrow">
            <pre className="code">{(() => { try { return (JSON.parse(job.job_args) as string[]).join(' ') } catch { return job.job_args } })()}</pre>
          </Panel>
        )}
      </div>
      {job.result_traceback && (
        <Panel title={tr('Traceback')}>
          <pre className="code">{job.result_traceback}</pre>
        </Panel>
      )}
    </div>
  )
}
