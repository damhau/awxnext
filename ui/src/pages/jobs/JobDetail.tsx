import { useQueryClient } from '@tanstack/react-query'
import { useMemo, useState } from 'react'
import { Link, Navigate, Route, Routes, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useList, useObject } from '../../api/hooks'
import { useLive } from '../../api/live'
import type { HostSummary, Job } from '../../api/types'
import { varsText } from '../../components/form'
import { Icon } from '../../components/icons'
import { Menu, useConfirm, useToast } from '../../components/overlay'
import { Ago, Cell, Crumbs, Elapsed, Empty, ErrorNote, KV, Loading, Maybe, Panel, StatusBadge, Tab } from '../../components/ui'
import { absolute, active, jobApi, jobPath, plural, shortSha, templatePath, typeLabel, userName } from '../../lib/format'
import { relaunchJob } from '../../lib/jobs'
import { WorkflowJobGraph } from '../automation/workflow'
import { firstFailure, toGrid, toLines, useJobEvents } from './events'
import { Matrix } from './Matrix'
import { Output } from './Output'

const templateKind: Record<string, string> = { job: 'job_template', workflow_job: 'workflow_job_template', project_update: 'project', inventory_update: 'inventory_source', system_job: 'system_job_template' }

export function JobDetail() {
  const { type = 'job', id } = useParams()
  const jid = Number(id)
  const path = jobApi(type, jid)
  const qc = useQueryClient()
  const job = useObject<Job>(path, { refetchInterval: (q) => (q.state.data && active(q.state.data.status) ? 10_000 : false) })
  useLive({ jobs: ['status_changed'] }, (m) => {
    if (m.unified_job_id === jid) qc.invalidateQueries({ queryKey: [path] })
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

  const relaunch = async (body: Record<string, unknown> = {}) => {
    try {
      nav(jobPath(await relaunchJob(job, body)))
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  const cancel = async () => {
    if (!(await confirm({ title: `Cancel #${job.id}?`, body: 'The playbook is stopped; hosts may be left part-way through a task.', confirm: 'Cancel job', danger: true }))) return
    await api.post(`${jobApi(type, job.id)}cancel/`, {}).catch((e) => toast((e as Error).message, 'error'))
  }
  const remove = async () => {
    if (!(await confirm({ title: `Delete #${job.id}?`, body: 'Its output and events are removed.', confirm: 'Delete', danger: true }))) return
    await api.del(jobApi(type, job.id))
    nav('/jobs')
  }

  return (
    <div className="page ground">
      <section style={{ borderBottom: '1px solid var(--line)', padding: '18px 24px 0' }}>
        <Crumbs items={[{ label: 'Jobs', to: '/jobs' }, { label: `#${job.id}` }]} />
        <div className="row wrap" style={{ gap: 12, marginTop: 6 }}>
          <h1>{job.name}</h1>
          <StatusBadge status={job.status}>
            {job.status === 'failed' && failedHosts.length > 0 && summaries.data ? `Failed on ${failedHosts.length} of ${plural(summaries.data.count, 'host')}` : undefined}
          </StatusBadge>
          <span className="small muted">{typeLabel(type)}</span>
          <div style={{ marginLeft: 'auto' }} className="row wrap">
            {running && (
              <button className="btn" onClick={cancel}>
                <Icon name="stop" size={10} />
                Cancel
              </button>
            )}
            {!running && type !== 'workflow_approval' && (
              <button className="btn" onClick={() => relaunch()}>
                <Icon name="refresh" size={13} />
                Relaunch
              </button>
            )}
            {!running && type === 'job' && failedHosts.length > 0 && (
              <button className="btn primary" onClick={() => relaunch({ hosts: 'failed' })}>
                Relaunch on {plural(failedHosts.length, 'failed host')}
              </button>
            )}
            <Menu label="More actions">
              {ujtType && job.unified_job_template && <Link to={templatePath({ id: job.unified_job_template, type: ujtType })}>Open template</Link>}
              {!isWorkflow && <a href={`/api/v2/${jobApi(type, job.id)}stdout/?format=txt_download`}>Download output</a>}
              <a href={`/api/v2/${jobApi(type, job.id)}`} target="_blank" rel="noreferrer">
                View in API
              </a>
              {!running && (
                <>
                  <hr />
                  <button className="danger" onClick={remove}>
                    Delete
                  </button>
                </>
              )}
            </Menu>
          </div>
        </div>
        <dl className="facts" style={{ marginTop: 10 }}>
          <div>
            <dt>Started</dt>
            <dd>{job.started ? absolute(job.started) : 'not yet'}</dd>
          </div>
          <div>
            <dt>Duration</dt>
            <dd>
              <Elapsed job={job} />
            </dd>
          </div>
          <div>
            <dt>Launched by</dt>
            <dd className="sans">
              {job.launch_type === 'scheduled' ? 'schedule' : job.launch_type === 'workflow' ? 'workflow' : job.launch_type === 'webhook' ? 'webhook' : job.launched_by?.name ?? userName(sf.created_by)}
            </dd>
          </div>
          {job.scm_revision && (
            <div>
              <dt>Revision</dt>
              <dd>
                {job.scm_branch ? `${job.scm_branch}@` : ''}
                {shortSha(job.scm_revision)}
              </dd>
            </div>
          )}
          {sf.inventory && (
            <div>
              <dt>Inventory</dt>
              <dd>
                <Link to={`/inventories/${sf.inventory.id}`}>{sf.inventory.name}</Link>
                {summaries.data && ` · ${plural(summaries.data.count, 'host')}`}
              </dd>
            </div>
          )}
          {job.limit && (
            <div>
              <dt>Limit</dt>
              <dd>{job.limit}</dd>
            </div>
          )}
          {sf.execution_environment && (
            <div>
              <dt>Env</dt>
              <dd>{sf.execution_environment.name}</dd>
            </div>
          )}
          {job.execution_node && (
            <div>
              <dt>Node</dt>
              <dd>{job.execution_node}</dd>
            </div>
          )}
          {shortVars && (
            <div>
              <dt>Vars</dt>
              <dd>{shortVars}</dd>
            </div>
          )}
          {job.job_slice_count > 1 && (
            <div>
              <dt>Slice</dt>
              <dd>
                {job.job_slice_number} of {job.job_slice_count}
              </dd>
            </div>
          )}
        </dl>
        <nav className="tabs" role="tablist" style={{ marginTop: 12 }}>
          <Tab to={base} end>
            {isWorkflow ? 'Graph' : 'Output'}
          </Tab>
          {type === 'job' && (
            <Tab to={`${base}/hosts`} n={summaries.data?.count}>
              Hosts
            </Tab>
          )}
          {(extra || type === 'job') && <Tab to={`${base}/variables`}>Variables</Tab>}
          {type === 'job' && <Tab to={`${base}/artifacts`}>Artifacts</Tab>}
          <Tab to={`${base}/details`}>Details</Tab>
        </nav>
      </section>
      <Routes>
        <Route index element={isWorkflow ? <div className="page-body"><WorkflowJobGraph jobId={job.id} /></div> : <OutputTab type={type} job={job} summaries={summaries.data?.results ?? []} />} />
        <Route path="hosts" element={<HostsTab job={job} rows={summaries.data?.results ?? []} />} />
        <Route path="variables" element={<VariablesTab job={job} />} />
        <Route path="artifacts" element={<div className="page-body"><Panel title="Artifacts">{job.artifacts && Object.keys(job.artifacts).length ? <pre className="code">{JSON.stringify(job.artifacts, null, 2)}</pre> : <p className="muted small" style={{ margin: 0 }}>The playbook set no artifacts (set_stats with per_host: false).</p>}</Panel></div>} />
        <Route path="details" element={<DetailsTab type={type} job={job} />} />
      </Routes>
    </div>
  )
}

function OutputTab({ type, job, summaries }: { type: string; job: Job; summaries: HostSummary[] }) {
  const running = active(job.status)
  const { events, loading, truncated } = useJobEvents(type, job.id, running || job.event_processing_finished === false)
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
        {truncated && <div className="notice">This output is very long; the first 60,000 events are shown. Download the full output from the menu.</div>}
        {loading && lines.length === 0 ? (
          <div className="term">
            <div className="term-empty">Loading output…</div>
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
    <aside aria-label="Failure summary" className="panel narrow" style={{ padding: 14, flex: '1 1 300px' }}>
      <h2 style={{ marginBottom: 8 }}>Why it failed</h2>
      {failure ? (
        <>
          <p style={{ margin: '0 0 10px', lineHeight: 1.5 }}>
            {plural(failure.hosts.length, 'host')} failed <b style={{ fontWeight: 600 }}>{failure.task}</b>
            {failure.stopped ? ', and the play stopped: no hosts were left to continue (max_fail_percentage or any_errors_fatal).' : '.'}
          </p>
          <pre className="code" style={{ whiteSpace: 'pre-wrap' }}>
            {failure.message}
          </pre>
          <dl className="kv small" style={{ marginTop: 12 }}>
            {failure.path && (
              <>
                <dt>Task</dt>
                <dd className="mono">{failure.path}</dd>
              </>
            )}
            <dt>Hosts</dt>
            <dd className="mono">{failure.hosts.slice(0, 8).join(', ') + (failure.hosts.length > 8 ? ` +${failure.hosts.length - 8}` : '')}</dd>
            <dt>Previous run</dt>
            <dd>{p ? <><Link to={jobPath(p)}>#{p.id}</Link> succeeded <Ago at={p.finished} /></> : 'no earlier success'}</dd>
          </dl>
          <div className="row wrap" style={{ marginTop: 14 }}>
            <button className="btn sm" onClick={() => onJump(failure.event.start_line + 1)}>
              Show in output
            </button>
          </div>
        </>
      ) : (
        <>
          <p style={{ margin: '0 0 10px' }}>{job.job_explanation || 'The job failed before any task reported a failure.'}</p>
          {job.result_traceback && <pre className="code">{String(job.result_traceback).slice(-1500)}</pre>}
          <dl className="kv small">
            <dt>Previous run</dt>
            <dd>{p ? <><Link to={jobPath(p)}>#{p.id}</Link> succeeded <Ago at={p.finished} /></> : 'no earlier success'}</dd>
          </dl>
        </>
      )}
    </aside>
  )
}

function HostsTab({ job, rows }: { job: Job; rows: HostSummary[] }) {
  if (!rows.length) return <div className="page-body"><Panel><Empty title={active(job.status) ? 'No host results yet' : 'No host results'}>Results appear as hosts finish the play.</Empty></Panel></div>
  return (
    <div className="page-body">
      <div className="table-wrap">
        <table className="data">
          <thead>
            <tr>
              <th>Host</th>
              <th>Result</th>
              <th className="end">ok</th>
              <th className="end">changed</th>
              <th className="end">failed</th>
              <th className="end">unreachable</th>
              <th className="end">skipped</th>
              <th className="end">rescued</th>
              <th className="end">ignored</th>
            </tr>
          </thead>
          <tbody>
            {[...rows].sort((a, b) => Number(b.failed || b.dark > 0) - Number(a.failed || a.dark > 0) || a.host_name.localeCompare(b.host_name)).map((h) => (
              <tr key={h.id}>
                <td className="mono">{h.host ? <Link to={`/hosts/${h.host}`}>{h.host_name}</Link> : h.host_name}</td>
                <td>{h.dark > 0 ? <span className="status"><Cell state="error" />Unreachable</span> : h.failed ? <span className="status"><Cell state="failed" />Failed</span> : h.changed ? <span className="status"><Cell state="changed" />Changed</span> : <span className="status"><Cell state="ok" />Ok</span>}</td>
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
        <Panel title="Extra variables" className="wide">
          {extra ? <pre className="code">{extra}</pre> : <p className="muted small" style={{ margin: 0 }}>None.</p>}
        </Panel>
        <Panel title="Launch values" className="narrow">
          <KV
            items={[
              ['Limit', <Maybe v={job.limit} mono />],
              ['Job type', job.job_type],
              ['Tags', <Maybe v={job.job_tags} mono />],
              ['Skip tags', <Maybe v={job.skip_tags} mono />],
              ['Verbosity', job.verbosity],
              ['Forks', job.forks || 'default'],
              ['Diff mode', job.diff_mode ? 'yes' : 'no'],
              ['Branch', <Maybe v={job.scm_branch} mono />],
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
        <Panel title="Details" className="wide">
          <KV
            items={[
              ['Status', <StatusBadge status={job.status} />],
              ['Explanation', job.job_explanation || null],
              ['Type', typeLabel(type)],
              ['Template', sf.unified_job_template ? <Link to={templatePath({ id: sf.unified_job_template.id, type: templateKind[type] ?? '' })}>{sf.unified_job_template.name}</Link> : null],
              ['Project', sf.project ? <Link to={`/projects/${sf.project.id}`}>{sf.project.name}</Link> : null],
              ['Playbook', job.playbook ? <span className="mono">{job.playbook}</span> : null],
              ['Revision', job.scm_revision ? <span className="mono">{job.scm_revision}</span> : null],
              ['Inventory', sf.inventory ? <Link to={`/inventories/${sf.inventory.id}`}>{sf.inventory.name}</Link> : null],
              ['Credentials', sf.credentials?.length ? sf.credentials.map((c) => c.name).join(', ') : null],
              ['Execution environment', sf.execution_environment?.name ?? null],
              ['Instance group', sf.instance_group?.name ?? null],
              ['Execution node', job.execution_node ? <span className="mono">{job.execution_node}</span> : null],
              ['Controller node', job.controller_node ? <span className="mono">{job.controller_node}</span> : null],
              ['Launch type', job.launch_type],
              ['Created', absolute(job.created)],
              ['Started', absolute(job.started)],
              ['Finished', job.finished ? absolute(job.finished) : null],
              ['Working directory', job.job_cwd ? <span className="mono">{job.job_cwd}</span> : null],
            ]}
          />
        </Panel>
        {job.job_args && (
          <Panel title="Command" className="narrow">
            <pre className="code">{(() => { try { return (JSON.parse(job.job_args) as string[]).join(' ') } catch { return job.job_args } })()}</pre>
          </Panel>
        )}
      </div>
      {job.result_traceback && (
        <Panel title="Traceback">
          <pre className="code">{job.result_traceback}</pre>
        </Panel>
      )}
    </div>
  )
}
