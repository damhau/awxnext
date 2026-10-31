import { Route } from 'react-router'
import { PageHead } from '../../components/ui'
import { lazyModule } from '../../lib/lazy'
import { tr } from '../../lib/i18n'

const m_JobDetail = lazyModule(() => import('./JobDetail'))
const JobDetail = m_JobDetail('JobDetail')
const m_JobsTable = lazyModule(() => import('./JobsTable'))
const JobsTable = m_JobsTable('JobsTable')

function JobList() {
  return (
    <div className="page">
      <PageHead title={tr('Jobs')} subtitle={tr('Every run: playbooks, workflows, syncs and commands.')} bordered={false} />
      <div className="page-body">
        <JobsTable />
      </div>
    </div>
  )
}

// Routes for the jobs list and job output pages.
export function jobRoutes() {
  return (
    <>
      <Route path="jobs" element={<JobList />} />
      <Route path="jobs/:type/:id/*" element={<JobDetail />} />
    </>
  )
}
