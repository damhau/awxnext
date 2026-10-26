// Routes for schedules, approvals and workflow editing.
import { Route } from 'react-router'
import './automation.css'
import { lazyModule } from '../../lib/lazy'

const m_ApprovalsPage = lazyModule(() => import('./ApprovalsPage'))
const ApprovalsPage = m_ApprovalsPage('ApprovalsPage')
const m_SchedulesPage = lazyModule(() => import('./SchedulesPage'))
const SchedulesPage = m_SchedulesPage('SchedulesPage')

export function automationRoutes() {
  return (
    <>
      <Route path="schedules" element={<SchedulesPage />} />
      <Route path="approvals" element={<ApprovalsPage />} />
    </>
  )
}
