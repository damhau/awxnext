// Routes for the portal's admin pages: catalog items, requests and deployments.
import { Route } from 'react-router'

import './catalog.css'
import { lazyModule } from '../../lib/lazy'

const m_items = lazyModule(() => import('./items'))
const CatalogItemDetail = m_items('CatalogItemDetail')
const CatalogItemList = m_items('CatalogItemList')
const CatalogItemNew = m_items('CatalogItemNew')
const m_lists = lazyModule(() => import('./lists'))
const CatalogRequestList = m_lists('CatalogRequestList')
const DeploymentList = m_lists('DeploymentList')

export function catalogRoutes() {
  return (
    <>
      <Route path="catalog-items" element={<CatalogItemList />} />
      <Route path="catalog-items/new" element={<CatalogItemNew />} />
      <Route path="catalog-items/:id/*" element={<CatalogItemDetail />} />
      <Route path="catalog-requests" element={<CatalogRequestList />} />
      <Route path="deployments" element={<DeploymentList />} />
    </>
  )
}
