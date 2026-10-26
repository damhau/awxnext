// Routes for schedules, approvals and workflow editing.
import { Route } from 'react-router'
import './automation.css'
import { ApprovalsPage } from './ApprovalsPage'
import { SchedulesPage } from './SchedulesPage'

export function automationRoutes() {
  return (
    <>
      <Route path="schedules" element={<SchedulesPage />} />
      <Route path="approvals" element={<ApprovalsPage />} />
    </>
  )
}
