// Routes for inventories, hosts, projects, credentials and execution environments.
import { Route, useOutletContext } from 'react-router'
import './resources.css'
import { CredentialAccessTab, CredentialDetail, CredentialDetailsTab, CredentialForm, CredentialList } from './credentials'
import { ExecutionEnvironmentList } from './executionEnvironments'
import { HostDetail, HostList } from './hosts'
import {
  InventoryAccessTab,
  InventoryDetail,
  InventoryDetailsTab,
  InventoryForm,
  InventoryJobsTab,
  InventoryList,
  InventorySourceRedirect,
  type Inventory,
} from './inventories'
import { InventoryGroupsTab } from './inventoryGroups'
import { InventoryHostsTab } from './inventoryHosts'
import { InventorySourcesTab } from './inventorySources'
import { ProjectAccessTab, ProjectDetail, ProjectDetailsTab, ProjectForm, ProjectJobsTab, ProjectList, ProjectSchedulesTab } from './projects'

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
