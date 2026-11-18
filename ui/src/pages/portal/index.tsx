// Routes of the self-service portal, outside the admin shell.
import { Suspense } from 'react'
import { Route } from 'react-router'
import { lazyModule } from '../../lib/lazy'

const m_shell = lazyModule(() => import('./PortalShell'))
const PortalShell = m_shell('PortalShell')
const PortalNotFound = m_shell('PortalNotFound')
const m_catalog = lazyModule(() => import('./Catalog'))
const Catalog = m_catalog('Catalog')
const m_form = lazyModule(() => import('./RequestForm'))
const RequestForm = m_form('RequestForm')
const m_requests = lazyModule(() => import('./Requests'))
const Requests = m_requests('Requests')

export function portalRoutes() {
  return (
    <Route
      path="portal"
      element={
        <Suspense fallback={<div className="shell" aria-busy="true" />}>
          <PortalShell />
        </Suspense>
      }
    >
      <Route index element={<Catalog />} />
      <Route path="items/:id" element={<RequestForm />} />
      <Route path="requests" element={<Requests />} />
      <Route path="requests/:id" element={<Requests />} />
      <Route path="*" element={<PortalNotFound />} />
    </Route>
  )
}
