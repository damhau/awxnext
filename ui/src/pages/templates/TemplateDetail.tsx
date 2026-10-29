import { useQuery } from '@tanstack/react-query'
import { useMemo } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api, type Page } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Base, Job, Template } from '../../api/types'
import { AccessTab } from '../../components/access/Access'
import { useCopy } from '../../components/CopyDialog'
import { NotificationsTab } from '../../components/NotificationsTab'
import { varsText } from '../../components/form'
import { Icon } from '../../components/icons'
import { Menu, useConfirm, useToast } from '../../components/overlay'
import { Ago, ErrorNote, KV, Loading, Maybe, PageHead, Panel, Tab, Upcoming } from '../../components/ui'
import { duration, jobPath, statusKind, userName } from '../../lib/format'
import { SchedulesTab } from '../automation/SchedulesTab'
import { WorkflowVisualizer } from '../automation/workflow'
import { JobsTable } from '../jobs/JobsTable'
import { ancestors, buildTree, useFolders } from '../../lib/folders'
import type { OutputDecl } from '../../lib/outputs'
import { History } from './History'
import { SurveyEditor } from './SurveyEditor'
import { TemplateForm } from './TemplateForm'

export type Kind = 'job' | 'workflow'
export const resOf = (k: Kind) => (k === 'job' ? 'job_templates' : 'workflow_job_templates')

export function TemplateDetail({ kind }: { kind: Kind }) {
  const { id } = useParams()
  const res = resOf(kind)
  const t = useObject<Template>(`${res}/${id}/`)
  const history = useList<Base>(`${res}/${id}/history/`, { page_size: 1 })
  const schedules = useList<Base>(`${res}/${id}/schedules/`, { page_size: 1 })
  const folders = useFolders()
  const nav = useNavigate()
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  const tree = useMemo(() => buildTree(folders.data ?? []), [folders.data])
  const copy = useCopy(res, {
    to: (nid) => `/templates/${kind}/${nid}`,
    what: kind === 'job' ? 'Credentials, labels and the survey are copied.' : 'Nodes, links and the survey are copied.',
    invalidates: ['unified_job_templates', 'folders'],
  })

  if (t.isPending) return <div className="page"><Loading /></div>
  if (t.error || !t.data) return <div className="page page-body"><ErrorNote error={t.error} /></div>
  const tpl = t.data
  const caps = tpl.summary_fields.user_capabilities ?? {}
  const folder = tpl.folder ? tree.byId.get(tpl.folder) : undefined
  const crumbs = [
    { label: 'Templates', to: '/templates' },
    ...ancestors(folder, tree.byId).map((a) => ({ label: a.folder.name, to: `/templates?folder=${a.folder.id}` })),
  ]
  const base = `/templates/${kind}/${id}`

  const remove = async () => {
    if (!(await confirm({ title: `Delete ${tpl.name}?`, body: 'Its schedules are deleted too. Job history stays.', confirm: 'Delete', danger: true }))) return
    try {
      await api.del(`${res}/${id}/`)
      invalidate('unified_job_templates', res)
      nav('/templates')
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }

  return (
    <div className="page">
      {copy.dialog}
      <PageHead
        crumbs={crumbs}
        title={tpl.name}
        badge={
          <span className="small muted">
            {kind === 'job' ? (
              <>
                job template · <span className="mono">{tpl.playbook}</span>
              </>
            ) : (
              'workflow template'
            )}
            {tpl.managed_by && (
              <>
                {' '}
                · <span className="tag">managed by {tpl.managed_by}</span>
              </>
            )}
          </span>
        }
        actions={
          <>
            {caps.edit && !tpl.managed_by && (
              <Link className="btn" to={`${base}/edit`}>
                Edit
              </Link>
            )}
            {caps.start !== false && (
              <Link className="btn primary" to={`/launch/${kind}/${id}`}>
                <Icon name="play" size={10} />
                Launch
              </Link>
            )}
            <Menu label="More actions">
              {caps.copy && <button onClick={() => copy.open(tpl)}>Copy…</button>}
              <a href={`/api/v2/${res}/${id}/`} target="_blank" rel="noreferrer">
                View in API
              </a>
              {caps.delete && (
                <>
                  <hr />
                  <button className="danger" onClick={remove}>
                    Delete
                  </button>
                </>
              )}
            </Menu>
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              Details
            </Tab>
            {kind === 'workflow' && <Tab to={`${base}/visualizer`}>Visualizer</Tab>}
            <Tab to={`${base}/survey`}>Survey</Tab>
            <Tab to={`${base}/access`}>Access</Tab>
            <Tab to={`${base}/notifications`}>Notifications</Tab>
            <Tab to={`${base}/schedules`} n={schedules.data?.count || undefined}>
              Schedules
            </Tab>
            <Tab to={`${base}/jobs`}>Jobs</Tab>
            <Tab to={`${base}/history`} n={history.data?.count || undefined}>
              History
            </Tab>
          </>
        }
      />
      <Routes>
        <Route index element={<Details kind={kind} tpl={tpl} />} />
        <Route path="visualizer" element={<div className="page-body"><WorkflowVisualizer templateId={tpl.id} editable={!!caps.edit && !tpl.managed_by} /></div>} />
        <Route path="survey" element={<div className="page-body"><SurveyEditor kind={kind} tpl={tpl} /></div>} />
        <Route path="access" element={<div className="page-body"><AccessTab resource={res} id={tpl.id} name={tpl.name} /></div>} />
        <Route path="notifications" element={<div className="page-body"><NotificationsTab resource={res} id={tpl.id} approvals={kind === 'workflow'} editable={!!caps.edit && !tpl.managed_by} /></div>} />
        <Route path="schedules" element={<div className="page-body"><SchedulesTab template={{ id: tpl.id, type: tpl.type, name: tpl.name }} /></div>} />
        <Route path="jobs" element={<div className="page-body"><JobsTable fixed={{ unified_job_template: tpl.id }} /></div>} />
        <Route path="history/*" element={<History kind={kind} tpl={tpl} />} />
        <Route path="edit" element={<div className="page-body"><TemplateForm kind={kind} tpl={tpl} /></div>} />
      </Routes>
    </div>
  )
}

const asked = (t: Template) =>
  Object.keys(t)
    .filter((k) => k.startsWith('ask_') && k.endsWith('_on_launch') && t[k])
    .map((k) => k.slice(4, -10).replace(/_/g, ' '))

function Details({ kind, tpl }: { kind: Kind; tpl: Template }) {
  const sf = tpl.summary_fields
  const recent = useList<Job>('unified_jobs/', { unified_job_template: tpl.id, order_by: '-id', page_size: 30 })
  const usedBy = useQuery({
    queryKey: ['workflow_job_template_nodes/', 'used-by', tpl.id],
    queryFn: () => api.get<Page<Base>>('workflow_job_template_nodes/', { unified_job_template: tpl.id, page_size: 20 }),
  })
  const schedules = useList<Base>(`${resOf(kind)}/${tpl.id}/schedules/`, { page_size: 10 })
  const runs = recent.data?.results ?? []
  const done = runs.filter((j) => j.finished)
  const failed = done.filter((j) => ['fail', 'error'].includes(statusKind(j.status)))
  const durations = done.map((j) => j.elapsed).sort((a, b) => a - b)
  const median = durations.length ? durations[Math.floor(durations.length / 2)] : null
  const extra = varsText(tpl.extra_vars)
  const prompts = asked(tpl)

  return (
    <div className="page-body">
      <div className="split">
        <div className="wide stack" style={{ gap: 16 }}>
          <Panel title="Settings">
            {kind === 'job' ? (
              <KV
                items={[
                  ['Description', <Maybe v={tpl.description} />],
                  ['Project', sf.project ? <Link to={`/projects/${sf.project.id}`}>{sf.project.name}</Link> : <Maybe v={null} />],
                  ['Playbook', <span className="mono">{tpl.playbook}</span>],
                  ['Branch', tpl.scm_branch ? <span className="mono">{tpl.scm_branch}</span> : <span className="faint">project default</span>],
                  ['Inventory', sf.inventory ? <Link to={`/inventories/${sf.inventory.id}`}>{sf.inventory.name}</Link> : <span className="faint">asked at launch</span>],
                  [
                    'Credentials',
                    sf.credentials?.length ? (
                      <span className="row wrap" style={{ gap: 4 }}>
                        {sf.credentials.map((c) => (
                          <Link key={c.id} to={`/credentials/${c.id}`} className="chip" style={{ fontFamily: 'var(--sans)' }}>
                            {c.name}
                            {c.kind && <span className="faint">{c.kind}</span>}
                          </Link>
                        ))}
                      </span>
                    ) : (
                      <span className="faint">none</span>
                    ),
                  ],
                  ['Execution environment', sf.execution_environment ? sf.execution_environment.name : <span className="faint">default</span>],
                  ['Job type', tpl.job_type],
                  ['Limit', <Maybe v={tpl.limit} mono />],
                  ['Forks', tpl.forks ? tpl.forks : <span className="faint">default</span>],
                  ['Verbosity', ['0 (normal)', '1 (verbose)', '2 (more verbose)', '3 (debug)', '4 (connection debug)', '5 (WinRM debug)'][tpl.verbosity] ?? tpl.verbosity],
                  ['Timeout', tpl.timeout ? duration(tpl.timeout) : <span className="faint">none</span>],
                  ['Slices', tpl.job_slice_count > 1 ? tpl.job_slice_count : null],
                  ['Tags', <Maybe v={tpl.job_tags} mono />],
                  ['Skip tags', tpl.skip_tags ? <span className="mono">{tpl.skip_tags}</span> : null],
                  [
                    'Outputs',
                    (tpl.outputs as OutputDecl[] | undefined)?.length ? (
                      <span className="row wrap" style={{ gap: 4 }}>
                        {(tpl.outputs as OutputDecl[]).map((o) => (
                          <span key={o.name} className="chip" title={o.required ? 'required' : 'optional'}>
                            {o.name}
                            <span className="faint">
                              {o.type}
                              {o.required ? '' : '?'}
                            </span>
                          </span>
                        ))}
                      </span>
                    ) : null,
                  ],
                  [
                    'Labels',
                    sf.labels?.results.length ? (
                      <span className="row wrap" style={{ gap: 4 }}>
                        {sf.labels.results.map((l) => (
                          <span key={l.id} className="tag">
                            {l.name}
                          </span>
                        ))}
                      </span>
                    ) : null,
                  ],
                  [
                    'Options',
                    [tpl.become_enabled && 'privilege escalation', tpl.diff_mode && 'show changes', tpl.allow_simultaneous && 'concurrent runs', tpl.use_fact_cache && 'fact cache', tpl.force_handlers && 'force handlers']
                      .filter(Boolean)
                      .join(', ') || <span className="faint">none</span>,
                  ],
                  ['Asked at launch', prompts.length ? prompts.join(', ') : <span className="faint">nothing</span>],
                  ['Webhook', tpl.webhook_service ? `${tpl.webhook_service} · /api/v2/job_templates/${tpl.id}/${tpl.webhook_service === 'generic' ? 'webhook' : tpl.webhook_service}/` : null],
                  ['Callback', tpl.host_config_key ? <span className="mono">/api/v2/job_templates/{tpl.id}/callback/</span> : null],
                ]}
              />
            ) : (
              <KV
                items={[
                  ['Description', <Maybe v={tpl.description} />],
                  ['Organization', sf.organization?.name],
                  ['Inventory', sf.inventory ? <Link to={`/inventories/${sf.inventory.id}`}>{sf.inventory.name}</Link> : <span className="faint">each node's own</span>],
                  ['Limit', <Maybe v={tpl.limit} mono />],
                  ['Branch', <Maybe v={tpl.scm_branch} mono />],
                  ['Options', [tpl.allow_simultaneous && 'concurrent runs'].filter(Boolean).join(', ') || <span className="faint">none</span>],
                  ['Asked at launch', prompts.length ? prompts.join(', ') : <span className="faint">nothing</span>],
                  ['Webhook', tpl.webhook_service || null],
                ]}
              />
            )}
          </Panel>
          {extra && (
            <Panel title="Extra variables">
              <pre className="code">{extra}</pre>
            </Panel>
          )}
        </div>
        <aside className="narrow stack" style={{ gap: 16 }}>
          <Panel title="Recent runs">
            {runs.length ? (
              <>
                <div className="row" style={{ gap: 24, alignItems: 'baseline' }}>
                  <div>
                    <div style={{ fontSize: 18, fontWeight: 500 }}>{done.length}</div>
                    <div className="small muted">finished runs</div>
                  </div>
                  <div>
                    <div style={{ fontSize: 18, fontWeight: 500 }} className={failed.length ? 'fail-text' : 'ok-text'}>
                      {failed.length}
                    </div>
                    <div className="small muted">failed</div>
                  </div>
                  <div>
                    <div style={{ fontSize: 18, fontWeight: 500 }}>{median !== null ? duration(median) : '—'}</div>
                    <div className="small muted">median</div>
                  </div>
                </div>
                {failed[0] && (
                  <p className="small" style={{ margin: '10px 0 0' }}>
                    Last failure <Link to={jobPath(failed[0])}>#{failed[0].id}</Link> <Ago at={failed[0].finished} />
                  </p>
                )}
              </>
            ) : (
              <p className="small muted" style={{ margin: 0 }}>
                Never run.
              </p>
            )}
          </Panel>
          <Panel title="Used by">
            <ul className="list-plain small" style={{ gap: 10 }}>
              {(usedBy.data?.results ?? []).map((n) => (
                <li key={n.id} style={{ display: 'block' }}>
                  <Link to={`/templates/workflow/${n.workflow_job_template}/visualizer`}>{n.summary_fields?.workflow_job_template?.name ?? `workflow ${n.workflow_job_template}`}</Link>
                  <div className="faint">workflow node {n.identifier ? <span className="mono">{n.identifier}</span> : ''}</div>
                </li>
              ))}
              {(schedules.data?.results ?? []).map((s) => (
                <li key={'s' + s.id} style={{ display: 'block' }}>
                  <Link to={`/templates/${kind}/${tpl.id}/schedules`}>{s.name}</Link>
                  <div className="faint">
                    schedule · {s.enabled ? <>next <Upcoming at={s.next_run} /></> : 'paused'}
                  </div>
                </li>
              ))}
              {tpl.webhook_service && (
                <li style={{ display: 'block' }}>
                  <span>{tpl.webhook_service} webhook</span>
                  <div className="faint">launches on incoming events</div>
                </li>
              )}
              {!usedBy.data?.results.length && !schedules.data?.results.length && !tpl.webhook_service && <li className="muted">Only launched by hand.</li>}
            </ul>
          </Panel>
          <Panel title="Source of truth">
            {tpl.managed_by ? (
              <p className="small" style={{ margin: 0 }}>
                Managed from Git ({tpl.managed_by.startsWith('gitops:') ? <Link to={`/gitops/${tpl.managed_by.split(':')[1]}`}>GitOps source {tpl.managed_by.split(':')[1]}</Link> : tpl.managed_by}). Change the files in the repository; edits here are refused.
              </p>
            ) : (
              <>
                <p className="small" style={{ margin: 0 }}>
                  Edited in the UI. Not managed from Git.
                </p>
                <Link to="/gitops/new" className="small" style={{ display: 'inline-block', marginTop: 6 }}>
                  Manage from a Git repository…
                </Link>
              </>
            )}
          </Panel>
          <Panel title="Changed">
            <p className="small" style={{ margin: 0 }}>
              <Ago at={tpl.modified} /> by {userName(sf.modified_by)} ·{' '}
              <Link to={`/templates/${kind}/${tpl.id}/history`}>history</Link>
            </p>
            <p className="small muted" style={{ margin: '4px 0 0' }}>
              Created <Ago at={tpl.created} /> by {userName(sf.created_by)}
            </p>
          </Panel>
        </aside>
      </div>
    </div>
  )
}
