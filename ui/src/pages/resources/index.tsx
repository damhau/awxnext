// Routes for inventories, hosts, projects, credentials, choice sources and
// execution environments.
import { Route, useOutletContext } from 'react-router'
import './resources.css'

import type { Inventory } from './inventories'
import { lazyModule } from '../../lib/lazy'

const m_choiceSources = lazyModule(() => import('./choiceSources'))
const ChoiceSourceDetail = m_choiceSources('ChoiceSourceDetail')
const ChoiceSourceList = m_choiceSources('ChoiceSourceList')
const ChoiceSourceNew = m_choiceSources('ChoiceSourceNew')
const m_credentials = lazyModule(() => import('./credentials'))
const CredentialAccessTab = m_credentials('CredentialAccessTab')
const CredentialDetail = m_credentials('CredentialDetail')
const CredentialDetailsTab = m_credentials('CredentialDetailsTab')
const CredentialForm = m_credentials('CredentialForm')
const CredentialList = m_credentials('CredentialList')
const m_executionEnvironments = lazyModule(() => import('./executionEnvironments'))
const ExecutionEnvironmentList = m_executionEnvironments('ExecutionEnvironmentList')
const m_drift = lazyModule(() => import('./drift'))
const DriftList = m_drift('DriftList')
const m_hosts = lazyModule(() => import('./hosts'))
const HostDetail = m_hosts('HostDetail')
const HostList = m_hosts('HostList')
const m_inventories = lazyModule(() => import('./inventories'))
const InventoryAccessTab = m_inventories('InventoryAccessTab')
const InventoryDetail = m_inventories('InventoryDetail')
const InventoryDetailsTab = m_inventories('InventoryDetailsTab')
const InventoryForm = m_inventories('InventoryForm')
const InventoryJobsTab = m_inventories('InventoryJobsTab')
const InventoryPoliciesTab = m_inventories('InventoryPoliciesTab')
const InventoryList = m_inventories('InventoryList')
const InventorySourceRedirect = m_inventories('InventorySourceRedirect')
const m_inventoryGroups = lazyModule(() => import('./inventoryGroups'))
const InventoryGroupsTab = m_inventoryGroups('InventoryGroupsTab')
const m_inventoryHosts = lazyModule(() => import('./inventoryHosts'))
const InventoryHostsTab = m_inventoryHosts('InventoryHostsTab')
const m_inventorySources = lazyModule(() => import('./inventorySources'))
const InventorySourcesTab = m_inventorySources('InventorySourcesTab')
const InventorySourceSchedulesTab = m_inventorySources('InventorySourceSchedulesTab')
const m_projects = lazyModule(() => import('./projects'))
const ProjectAccessTab = m_projects('ProjectAccessTab')
const ProjectNotificationsTab = m_projects('ProjectNotificationsTab')
const ProjectDetail = m_projects('ProjectDetail')
const ProjectDetailsTab = m_projects('ProjectDetailsTab')
const ProjectForm = m_projects('ProjectForm')
const ProjectJobsTab = m_projects('ProjectJobsTab')
const ProjectList = m_projects('ProjectList')
const ProjectSchedulesTab = m_projects('ProjectSchedulesTab')
const m_rulebooks = lazyModule(() => import('./rulebooks'))
const ProjectRulebooksTab = m_rulebooks('ProjectRulebooksTab')
const RulebookDetail = m_rulebooks('RulebookDetail')
const RulebookEdit = m_rulebooks('RulebookEdit')
const RulebookNew = m_rulebooks('RulebookNew')

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
        <Route path="sources/:sourceId/schedules" element={<InvTab tab={InventorySourceSchedulesTab} />} />
        <Route path="jobs" element={<InvTab tab={InventoryJobsTab} />} />
        <Route path="policies" element={<InvTab tab={InventoryPoliciesTab} />} />
        <Route path="access" element={<InvTab tab={InventoryAccessTab} />} />
      </Route>
      <Route path="inventory-sources/:id" element={<InventorySourceRedirect />} />
      <Route path="hosts" element={<HostList />} />
      <Route path="hosts/:id" element={<HostDetail />} />
      <Route path="drift" element={<DriftList />} />
      <Route path="projects" element={<ProjectList />} />
      <Route path="projects/new" element={<ProjectForm />} />
      <Route path="projects/:id/edit" element={<ProjectForm />} />
      <Route path="projects/:id/rulebooks/new" element={<RulebookNew />} />
      <Route path="projects/:id" element={<ProjectDetail />}>
        <Route index element={<ProjectDetailsTab />} />
        <Route path="jobs" element={<ProjectJobsTab />} />
        <Route path="rulebooks" element={<ProjectRulebooksTab />} />
        <Route path="schedules" element={<ProjectSchedulesTab />} />
        <Route path="notifications" element={<ProjectNotificationsTab />} />
        <Route path="access" element={<ProjectAccessTab />} />
      </Route>
      <Route path="rulebooks/:id" element={<RulebookDetail />} />
      <Route path="rulebooks/:id/edit" element={<RulebookEdit />} />
      <Route path="credentials" element={<CredentialList />} />
      <Route path="credentials/new" element={<CredentialForm />} />
      <Route path="credentials/:id/edit" element={<CredentialForm />} />
      <Route path="credentials/:id" element={<CredentialDetail />}>
        <Route index element={<CredentialDetailsTab />} />
        <Route path="access" element={<CredentialAccessTab />} />
      </Route>
      <Route path="choice-sources" element={<ChoiceSourceList />} />
      <Route path="choice-sources/new" element={<ChoiceSourceNew />} />
      <Route path="choice-sources/:id/*" element={<ChoiceSourceDetail />} />
      <Route path="execution-environments" element={<ExecutionEnvironmentList />} />
    </>
  )
}
