import { useMemo } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Base, Job, Template } from '../../api/types'
import { AccessTab } from '../../components/access/Access'
import { useCopy } from '../../components/CopyDialog'
import { NotificationsTab } from '../../components/NotificationsTab'
import { varsText } from '../../components/form'
import { Icon } from '../../components/icons'
import { Menu, useConfirm, useToast } from '../../components/overlay'
import { Ago, ErrorNote, KV, Loading, Maybe, PageHead, Panel, Tab } from '../../components/ui'
import { duration, jobPath, statusKind, userName } from '../../lib/format'
import { SchedulesTab } from '../automation/SchedulesTab'
import { WorkflowVisualizer } from '../automation/workflow'
import { JobsTable } from '../jobs/JobsTable'
import { ancestors, buildTree, useFolders } from '../../lib/folders'
import type { OutputDecl } from '../../lib/outputs'
import { History } from './History'
import { SurveyEditor } from './SurveyEditor'
import { TemplateForm } from './TemplateForm'
import { DeleteEffects, fetchUsedBy, UsedByPanel } from '../../components/UsedBy'
import { tr } from '../../lib/i18n'

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
    what: kind === 'job' ? tr('Credentials, labels and the survey are copied.') : tr('Nodes, links and the survey are copied.'),
    invalidates: ['unified_job_templates', 'folders'],
  })

  if (t.isPending) return <div className="page"><Loading /></div>
  if (t.error || !t.data) return <div className="page page-body"><ErrorNote error={t.error} /></div>
  const tpl = t.data
  const caps = tpl.summary_fields.user_capabilities ?? {}
  const folder = tpl.folder ? tree.byId.get(tpl.folder) : undefined
  const crumbs = [
    { label: tr('Templates'), to: '/templates' },
    ...ancestors(folder, tree.byId).map((a) => ({ label: a.folder.name, to: `/templates?folder=${a.folder.id}` })),
  ]
  const base = `/templates/${kind}/${id}`

  const remove = async () => {
    const used = await fetchUsedBy(res, tpl.id)
    const body = (
      <>
        <p style={{ margin: 0 }}>{tr('Its schedules are deleted too. Job history stays.')}</p>
        <DeleteEffects u={used} />
      </>
    )
    if (!(await confirm({ title: tr('Delete {name}?', { name: tpl.name }), body, confirm: tr('Delete'), danger: true }))) return
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
                {tr('job template ·')} <span className="mono">{tpl.playbook}</span>
              </>
            ) : (
              tr('workflow template')
            )}
            {tpl.managed_by && (
              <>
                {' '}
                · <span className="tag">{tr('managed by')} {tpl.managed_by}</span>
              </>
            )}
          </span>
        }
        actions={
          <>
            {caps.edit && !tpl.managed_by && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {caps.start !== false && (
              <Link className="btn primary" to={`/launch/${kind}/${id}`}>
                <Icon name="play" size={10} />
                {tr('Launch')}
              </Link>
            )}
            <Menu label={tr('More actions')}>
              {caps.copy && <button onClick={() => copy.open(tpl)}>{tr('Copy…')}</button>}
              <a href={`/api/v2/${res}/${id}/`} target="_blank" rel="noreferrer">
                {tr('View in API')}
              </a>
              {caps.delete && (
                <>
                  <hr />
                  <button className="danger" onClick={remove}>
                    {tr('Delete')}
                  </button>
                </>
              )}
            </Menu>
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              {tr('Details')}
            </Tab>
            {kind === 'workflow' && <Tab to={`${base}/visualizer`}>{tr('Visualizer')}</Tab>}
            <Tab to={`${base}/survey`}>{tr('Survey')}</Tab>
            <Tab to={`${base}/access`}>{tr('Access')}</Tab>
            <Tab to={`${base}/notifications`}>{tr('Notifications')}</Tab>
            <Tab to={`${base}/schedules`} n={schedules.data?.count || undefined}>
              {tr('Schedules')}
            </Tab>
            <Tab to={`${base}/jobs`}>{tr('Jobs')}</Tab>
            <Tab to={`${base}/history`} n={history.data?.count || undefined}>
              {tr('History')}
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
          <Panel title={tr('Settings')}>
            {kind === 'job' ? (
              <KV
                items={[
                  [tr('Description'), <Maybe v={tpl.description} />],
                  [tr('Project'), sf.project ? <Link to={`/projects/${sf.project.id}`}>{sf.project.name}</Link> : <Maybe v={null} />],
                  [tr('Playbook'), <span className="mono">{tpl.playbook}</span>],
                  [tr('Branch'), tpl.scm_branch ? <span className="mono">{tpl.scm_branch}</span> : <span className="faint">{tr('project default')}</span>],
                  [tr('Inventory'), sf.inventory ? <Link to={`/inventories/${sf.inventory.id}`}>{sf.inventory.name}</Link> : <span className="faint">{tr('asked at launch')}</span>],
                  [
                    tr('Credentials'),
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
                      <span className="faint">{tr('none')}</span>
                    ),
                  ],
                  [tr('Execution environment'), sf.execution_environment ? sf.execution_environment.name : <span className="faint">{tr('default')}</span>],
                  [tr('Job type'), tpl.job_type],
                  [tr('Limit'), <Maybe v={tpl.limit} mono />],
                  [tr('Forks'), tpl.forks ? tpl.forks : <span className="faint">{tr('default')}</span>],
                  [tr('Verbosity'), ['0 (normal)', '1 (verbose)', '2 (more verbose)', '3 (debug)', '4 (connection debug)', '5 (WinRM debug)'][tpl.verbosity] ?? tpl.verbosity],
                  [tr('Timeout'), tpl.timeout ? duration(tpl.timeout) : <span className="faint">{tr('none')}</span>],
                  [tr('Slices'), tpl.job_slice_count > 1 ? tpl.job_slice_count : null],
                  [tr('Tags'), <Maybe v={tpl.job_tags} mono />],
                  [tr('Skip tags'), tpl.skip_tags ? <span className="mono">{tpl.skip_tags}</span> : null],
                  [
                    tr('Outputs'),
                    (tpl.outputs as OutputDecl[] | undefined)?.length ? (
                      <span className="row wrap" style={{ gap: 4 }}>
                        {(tpl.outputs as OutputDecl[]).map((o) => (
                          <span key={o.name} className="chip" title={o.required ? tr('required') : tr('optional')}>
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
                    tr('Labels'),
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
                    tr('Options'),
                    [tpl.become_enabled && 'privilege escalation', tpl.diff_mode && 'show changes', tpl.allow_simultaneous && 'concurrent runs', tpl.use_fact_cache && 'fact cache', tpl.force_handlers && 'force handlers']
                      .filter(Boolean)
                      .join(', ') || <span className="faint">{tr('none')}</span>,
                  ],
                  [tr('Asked at launch'), prompts.length ? prompts.join(', ') : <span className="faint">{tr('nothing')}</span>],
                  [tr('Webhook'), tpl.webhook_service ? tr('{webhook_service} · /api/v2/job_templates/{id}/{v}/', { webhook_service: tpl.webhook_service, id: tpl.id, v: tpl.webhook_service === 'generic' ? 'webhook' : tpl.webhook_service }) : null],
                  [tr('Callback'), tpl.host_config_key ? <span className="mono">{tr('/api/v2/job_templates/')}{tpl.id}{tr('/callback/')}</span> : null],
                ]}
              />
            ) : (
              <KV
                items={[
                  [tr('Description'), <Maybe v={tpl.description} />],
                  [tr('Organization'), sf.organization?.name],
                  [tr('Inventory'), sf.inventory ? <Link to={`/inventories/${sf.inventory.id}`}>{sf.inventory.name}</Link> : <span className="faint">{tr('each node\'s own')}</span>],
                  [tr('Limit'), <Maybe v={tpl.limit} mono />],
                  [tr('Branch'), <Maybe v={tpl.scm_branch} mono />],
                  [tr('Options'), [tpl.allow_simultaneous && 'concurrent runs'].filter(Boolean).join(', ') || <span className="faint">{tr('none')}</span>],
                  [tr('Asked at launch'), prompts.length ? prompts.join(', ') : <span className="faint">{tr('nothing')}</span>],
                  [tr('Webhook'), tpl.webhook_service || null],
                ]}
              />
            )}
          </Panel>
          {extra && (
            <Panel title={tr('Extra variables')}>
              <pre className="code">{extra}</pre>
            </Panel>
          )}
        </div>
        <aside className="narrow stack" style={{ gap: 16 }}>
          <Panel title={tr('Recent runs')}>
            {runs.length ? (
              <>
                <div className="row" style={{ gap: 24, alignItems: 'baseline' }}>
                  <div>
                    <div style={{ fontSize: 18, fontWeight: 500 }}>{done.length}</div>
                    <div className="small muted">{tr('finished runs')}</div>
                  </div>
                  <div>
                    <div style={{ fontSize: 18, fontWeight: 500 }} className={failed.length ? 'fail-text' : 'ok-text'}>
                      {failed.length}
                    </div>
                    <div className="small muted">{tr('failed')}</div>
                  </div>
                  <div>
                    <div style={{ fontSize: 18, fontWeight: 500 }}>{median !== null ? duration(median) : '—'}</div>
                    <div className="small muted">{tr('median')}</div>
                  </div>
                </div>
                {failed[0] && (
                  <p className="small" style={{ margin: '10px 0 0' }}>
                    {tr('Last failure')} <Link to={jobPath(failed[0])}>#{failed[0].id}</Link> <Ago at={failed[0].finished} />
                  </p>
                )}
              </>
            ) : (
              <p className="small muted" style={{ margin: 0 }}>
                {tr('Never run.')}
              </p>
            )}
          </Panel>
          <UsedByPanel resource={resOf(kind)} id={tpl.id} empty={tpl.webhook_service ? '' : tr('Only launched by hand.')}>
            {tpl.webhook_service && (
              <div className="small" style={{ marginTop: 10 }}>
                <span>{tpl.webhook_service} {tr('webhook')}</span>
                <div className="faint">{tr('launches on incoming events')}</div>
              </div>
            )}
          </UsedByPanel>
          <Panel title={tr('Source of truth')}>
            {tpl.managed_by ? (
              <p className="small" style={{ margin: 0 }}>
                {tr('Managed from Git (')}{tpl.managed_by.startsWith('gitops:') ? <Link to={`/gitops/${tpl.managed_by.split(':')[1]}`}>{tr('GitOps source')} {tpl.managed_by.split(':')[1]}</Link> : tpl.managed_by}{tr('). Change the files in the repository; edits here are refused.')}
              </p>
            ) : (
              <>
                <p className="small" style={{ margin: 0 }}>
                  {tr('Edited in the UI. Not managed from Git.')}
                </p>
                <Link to="/gitops/new" className="small" style={{ display: 'inline-block', marginTop: 6 }}>
                  {tr('Manage from a Git repository…')}
                </Link>
              </>
            )}
          </Panel>
          <Panel title={tr('Changed')}>
            <p className="small" style={{ margin: 0 }}>
              <Ago at={tpl.modified} /> {tr('by')} {userName(sf.modified_by)} ·{' '}
              <Link to={`/templates/${kind}/${tpl.id}/history`}>{tr('history')}</Link>
            </p>
            <p className="small muted" style={{ margin: '4px 0 0' }}>
              {tr('Created')} <Ago at={tpl.created} /> {tr('by')} {userName(sf.created_by)}
            </p>
          </Panel>
        </aside>
      </div>
    </div>
  )
}
