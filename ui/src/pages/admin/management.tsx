import { useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Job, Template } from '../../api/types'
import { Check, FormError, TextInput } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog } from '../../components/overlay'
import { DataTable } from '../../components/table'
import { Ago, Elapsed, Empty, ErrorNote, Loading, PageHead, Panel, RunBars, Status, Tab, Upcoming } from '../../components/ui'
import { jobPath } from '../../lib/format'
import { SchedulesTab } from '../automation/SchedulesTab'
import { NotificationsTab } from '../../components/NotificationsTab'
import { useSession } from '../../app/context'
import { tr } from '../../lib/i18n'

type SJT = Template & { job_type: string }

const usesDays = (t: string) => ['cleanup_jobs', 'cleanup_activitystream', 'cleanup_host_metrics'].includes(t)

const explain: Record<string, string> = {
  cleanup_jobs: tr('Deletes jobs, their output and events older than the retention period.'),
  cleanup_activitystream: tr('Deletes activity stream entries older than the retention period.'),
  cleanup_sessions: tr('Removes expired browser sessions.'),
  cleanup_tokens: tr('Removes expired API tokens.'),
  cleanup_host_metrics: tr('Removes soft-deleted host metrics older than the retention period.'),
}

function LaunchDialog({ t, onClose }: { t: SJT; onClose: () => void }) {
  const [days, setDays] = useState(t.job_type === 'cleanup_activitystream' ? '355' : t.job_type === 'cleanup_host_metrics' ? '365' : '90')
  const [dry, setDry] = useState(true)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const launch = async () => {
    setBusy(true)
    setError(null)
    try {
      const extra: Record<string, unknown> = {}
      if (usesDays(t.job_type)) {
        extra.days = Number(days)
        if (dry) extra.dry_run = true
      }
      const r = await api.post<{ id: number; system_job?: number }>(`system_job_templates/${t.id}/launch/`, { extra_vars: extra })
      invalidate('system_job_templates', 'unified_jobs')
      nav(`/jobs/system_job/${r.system_job ?? r.id}`)
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={tr('Run {name}', { name: t.name })}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className={`btn ${dry || !usesDays(t.job_type) ? 'primary' : 'danger solid'}`} disabled={busy || (usesDays(t.job_type) && !(Number(days) >= 0 && days !== ''))} onClick={launch}>
            {busy ? tr('Starting…') : dry && usesDays(t.job_type) ? tr('Run dry run') : tr('Run now')}
          </button>
        </>
      }
    >
      <p style={{ margin: 0 }}>{explain[t.job_type] ?? t.description}</p>
      {usesDays(t.job_type) && (
        <>
          <TextInput label={tr('Keep the last')} type="number" value={days} onChange={setDays} help={tr('Days of history to keep; older records are deleted.')} autoFocus />
          <Check label={tr('Dry run')} checked={dry} onChange={setDry} help={tr('Report what would be deleted without deleting anything.')} />
        </>
      )}
      <FormError error={error} />
    </Dialog>
  )
}

export function ManagementList() {
  const list = useList<SJT>('system_job_templates/', { order_by: 'name', page_size: 50 })
  const [launch, setLaunch] = useState<SJT | null>(null)
  const nav = useNavigate()
  return (
    <div className="page">
      <PageHead title={tr('Management jobs')} subtitle={tr('Built-in housekeeping that keeps the database small. Each runs on a schedule you can change.')} bordered={false} />
      <div className="page-body">
        <DataTable
          label={tr('Management jobs')}
          rows={list.data?.results}
          loading={list.isPending}
          error={list.error}
          onOpen={(t) => nav(`/management-jobs/${t.id}`)}
          keys={{ l: (t) => setLaunch(t) }}
          columns={[
            {
              key: 'name',
              header: tr('Job'),
              render: (t) => (
                <div>
                  <Link to={`/management-jobs/${t.id}`} className="cell-title">
                    {t.name}
                  </Link>
                  <div className="small faint">{explain[t.job_type] ?? t.description}</div>
                </div>
              ),
            },
            { key: 'runs', header: tr('Recent runs'), render: (t) => <RunBars jobs={t.summary_fields.recent_jobs} slots={8} /> },
            { key: 'last', header: tr('Last run'), className: 'small nowrap', render: (t) => (t.last_job_run ? <Status status={t.last_job_failed ? 'failed' : 'successful'}><Ago at={t.last_job_run} /></Status> : <span className="faint">{tr('Never')}</span>) },
            { key: 'next', header: tr('Next run'), className: 'small nowrap', render: (t) => <Upcoming at={t.next_job_run} /> },
            {
              key: 'x',
              header: <span className="sr-only">{tr('Actions')}</span>,
              className: 'end',
              render: (t) => (
                <button className="btn sm" onClick={() => setLaunch(t)}>
                  <Icon name="play" size={10} />
                  {tr('Run')}
                </button>
              ),
            },
          ]}
        />
      </div>
      {launch && <LaunchDialog t={launch} onClose={() => setLaunch(null)} />}
    </div>
  )
}

export function ManagementDetail() {
  const id = Number(useParams().id)
  const { me } = useSession()
  const t = useObject<SJT>(`system_job_templates/${id}/`)
  const jobs = useList<Job>(`system_job_templates/${id}/jobs/`, { order_by: '-id', page_size: 20 })
  const [launch, setLaunch] = useState(false)
  if (t.error) return <div className="page-body"><ErrorNote error={t.error} /></div>
  if (!t.data) return <Loading />
  const sjt = t.data
  const base = `/management-jobs/${id}`
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Management jobs'), to: '/management-jobs' }, { label: sjt.name }]}
        title={sjt.name}
        subtitle={explain[sjt.job_type] ?? sjt.description}
        actions={
          <button className="btn primary" onClick={() => setLaunch(true)}>
            <Icon name="play" size={10} />
            {tr('Run')}
          </button>
        }
        tabs={
          <>
            <Tab to={base} end>
              {tr('Jobs')}
            </Tab>
            <Tab to={`${base}/schedules`}>{tr('Schedules')}</Tab>
            <Tab to={`${base}/notifications`}>{tr('Notifications')}</Tab>
          </>
        }
      />
      <div className="page-body">
        <Routes>
          <Route
            index
            element={
              <Panel pad={false}>
                {jobs.data?.results.length ? (
                  <table className="data">
                    <tbody>
                      {jobs.data.results.map((j) => (
                        <tr key={j.id}>
                          <td style={{ width: 130 }}>
                            <Status status={j.status} />
                          </td>
                          <td>
                            <Link to={jobPath(j)}>#{j.id}</Link> <span className="faint small">{j.launch_type}</span>
                          </td>
                          <td className="small faint mono">{(j.extra_vars as string | undefined) ?? ''}</td>
                          <td className="small nowrap">{j.finished ? <Ago at={j.finished} /> : <Elapsed job={j} />}</td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                ) : (
                  <Empty title={tr('Never run')}>{tr('It runs on its schedule, or now with Run.')}</Empty>
                )}
              </Panel>
            }
          />
          <Route path="schedules" element={<SchedulesTab template={{ id, type: 'system_job_template', name: sjt.name }} />} />
          <Route path="notifications" element={<NotificationsTab resource="system_job_templates" id={sjt.id} editable={me.is_superuser} />} />
        </Routes>
      </div>
      {launch && <LaunchDialog t={sjt} onClose={() => setLaunch(false)} />}
    </div>
  )
}
