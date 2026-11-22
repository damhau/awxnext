// Routes for schedules, approvals, reports and workflow editing.
import { Route } from 'react-router'
import './automation.css'
import { lazyModule } from '../../lib/lazy'

const m_ApprovalsPage = lazyModule(() => import('./ApprovalsPage'))
const ApprovalsPage = m_ApprovalsPage('ApprovalsPage')
const m_SchedulesPage = lazyModule(() => import('./SchedulesPage'))
const SchedulesPage = m_SchedulesPage('SchedulesPage')
const m_ReportsPage = lazyModule(() => import('./ReportsPage'))
const ReportsPage = m_ReportsPage('ReportsPage')

export function automationRoutes() {
  return (
    <>
      <Route path="schedules" element={<SchedulesPage />} />
      <Route path="approvals" element={<ApprovalsPage />} />
      <Route path="reports" element={<ReportsPage />} />
    </>
  )
}
