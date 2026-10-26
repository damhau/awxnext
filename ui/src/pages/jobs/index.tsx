import { Route } from 'react-router'
import { PageHead } from '../../components/ui'
import { JobDetail } from './JobDetail'
import { JobsTable } from './JobsTable'

function JobList() {
  return (
    <div className="page">
      <PageHead title="Jobs" subtitle="Every run: playbooks, workflows, syncs and commands." bordered={false} />
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
