// Routes for inventories, hosts, projects, credentials and execution environments.
import { Route, useOutletContext } from 'react-router'
import './resources.css'

import type { Inventory } from './inventories'
import { lazyModule } from '../../lib/lazy'

const m_credentials = lazyModule(() => import('./credentials'))
const CredentialAccessTab = m_credentials('CredentialAccessTab')
const CredentialDetail = m_credentials('CredentialDetail')
const CredentialDetailsTab = m_credentials('CredentialDetailsTab')
const CredentialForm = m_credentials('CredentialForm')
const CredentialList = m_credentials('CredentialList')
const m_executionEnvironments = lazyModule(() => import('./executionEnvironments'))
const ExecutionEnvironmentList = m_executionEnvironments('ExecutionEnvironmentList')
const m_hosts = lazyModule(() => import('./hosts'))
const HostDetail = m_hosts('HostDetail')
const HostList = m_hosts('HostList')
const m_inventories = lazyModule(() => import('./inventories'))
const InventoryAccessTab = m_inventories('InventoryAccessTab')
const InventoryDetail = m_inventories('InventoryDetail')
const InventoryDetailsTab = m_inventories('InventoryDetailsTab')
const InventoryForm = m_inventories('InventoryForm')
const InventoryJobsTab = m_inventories('InventoryJobsTab')
const InventoryList = m_inventories('InventoryList')
const InventorySourceRedirect = m_inventories('InventorySourceRedirect')
const m_inventoryGroups = lazyModule(() => import('./inventoryGroups'))
const InventoryGroupsTab = m_inventoryGroups('InventoryGroupsTab')
const m_inventoryHosts = lazyModule(() => import('./inventoryHosts'))
const InventoryHostsTab = m_inventoryHosts('InventoryHostsTab')
const m_inventorySources = lazyModule(() => import('./inventorySources'))
const InventorySourcesTab = m_inventorySources('InventorySourcesTab')
const m_projects = lazyModule(() => import('./projects'))
const ProjectAccessTab = m_projects('ProjectAccessTab')
const ProjectDetail = m_projects('ProjectDetail')
const ProjectDetailsTab = m_projects('ProjectDetailsTab')
const ProjectForm = m_projects('ProjectForm')
const ProjectJobsTab = m_projects('ProjectJobsTab')
const ProjectList = m_projects('ProjectList')
const ProjectSchedulesTab = m_projects('ProjectSchedulesTab')

/** Renders an inventory tab with the inventory loaded by the parent route. */
function InvTab({ tab: Tab }: { tab: (p: { inv: Inventory }) => React.ReactNode }) {
  const inv = useOutletContext<Inventory>()
  return <Tab inv={inv} />
}

export function resourceRoutes() {
  return (
    <>
      <Route path="inventories" element={<InventoryList />} />
      <Route path="inventories/new" element={<InventoryForm />} />
      <Route path="inventories/:id/edit" element={<InventoryForm />} />
      <Route path="inventories/:id" element={<InventoryDetail />}>
        <Route index element={<InvTab tab={InventoryDetailsTab} />} />
        <Route path="hosts" element={<InvTab tab={InventoryHostsTab} />} />
        <Route path="groups" element={<InvTab tab={InventoryGroupsTab} />} />
        <Route path="sources" element={<InvTab tab={InventorySourcesTab} />} />
        <Route path="jobs" element={<InvTab tab={InventoryJobsTab} />} />
        <Route path="access" element={<InvTab tab={InventoryAccessTab} />} />
      </Route>
      <Route path="inventory-sources/:id" element={<InventorySourceRedirect />} />
      <Route path="hosts" element={<HostList />} />
      <Route path="hosts/:id" element={<HostDetail />} />
      <Route path="projects" element={<ProjectList />} />
      <Route path="projects/new" element={<ProjectForm />} />
      <Route path="projects/:id/edit" element={<ProjectForm />} />
      <Route path="projects/:id" element={<ProjectDetail />}>
        <Route index element={<ProjectDetailsTab />} />
        <Route path="jobs" element={<ProjectJobsTab />} />
        <Route path="schedules" element={<ProjectSchedulesTab />} />
        <Route path="access" element={<ProjectAccessTab />} />
      </Route>
      <Route path="credentials" element={<CredentialList />} />
      <Route path="credentials/new" element={<CredentialForm />} />
      <Route path="credentials/:id/edit" element={<CredentialForm />} />
      <Route path="credentials/:id" element={<CredentialDetail />}>
        <Route index element={<CredentialDetailsTab />} />
        <Route path="access" element={<CredentialAccessTab />} />
      </Route>
      <Route path="execution-environments" element={<ExecutionEnvironmentList />} />
    </>
  )
}
