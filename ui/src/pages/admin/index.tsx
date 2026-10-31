// Routes for organizations, teams, users, instances, credential types, notifications, GitOps,
// management jobs, activity stream and settings.
import { Route } from 'react-router'

import './admin.css'
import { lazyModule } from '../../lib/lazy'

const m_activity = lazyModule(() => import('./activity'))
const Activity = m_activity('Activity')
const m_credentialTypes = lazyModule(() => import('./credentialTypes'))
const CredentialTypeList = m_credentialTypes('CredentialTypeList')
const m_roles = lazyModule(() => import('./roles'))
const RoleDefinitionList = m_roles('RoleDefinitionList')
const m_folders = lazyModule(() => import('./folders'))
const FolderDetail = m_folders('FolderDetail')
const FolderList = m_folders('FolderList')
const m_gitops = lazyModule(() => import('./gitops'))
const GitOpsDetail = m_gitops('GitOpsDetail')
const GitOpsList = m_gitops('GitOpsList')
const GitOpsNew = m_gitops('GitOpsNew')
const m_hostMetrics = lazyModule(() => import('./hostMetrics'))
const HostMetrics = m_hostMetrics('HostMetrics')
const m_instances = lazyModule(() => import('./instances'))
const GroupDetail = m_instances('GroupDetail')
const GroupNew = m_instances('GroupNew')
const InstanceDetail = m_instances('InstanceDetail')
const InstanceList = m_instances('InstanceList')
const m_management = lazyModule(() => import('./management'))
const ManagementDetail = m_management('ManagementDetail')
const ManagementList = m_management('ManagementList')
const m_notifications = lazyModule(() => import('./notifications'))
const NotificationDetail = m_notifications('NotificationDetail')
const NotificationList = m_notifications('NotificationList')
const NotificationNew = m_notifications('NotificationNew')
const m_organizations = lazyModule(() => import('./organizations'))
const OrganizationDetail = m_organizations('OrganizationDetail')
const OrganizationList = m_organizations('OrganizationList')
const OrganizationNew = m_organizations('OrganizationNew')
const m_settings = lazyModule(() => import('./settings'))
const Settings = m_settings('Settings')
const m_teams = lazyModule(() => import('./teams'))
const TeamDetail = m_teams('TeamDetail')
const TeamList = m_teams('TeamList')
const TeamNew = m_teams('TeamNew')
const m_users = lazyModule(() => import('./users'))
const UserDetail = m_users('UserDetail')
const UserList = m_users('UserList')
const UserNew = m_users('UserNew')

export function adminRoutes() {
  return (
    <>
      <Route path="organizations" element={<OrganizationList />} />
      <Route path="organizations/new" element={<OrganizationNew />} />
      <Route path="organizations/:id/*" element={<OrganizationDetail />} />
      <Route path="teams" element={<TeamList />} />
      <Route path="teams/new" element={<TeamNew />} />
      <Route path="teams/:id/*" element={<TeamDetail />} />
      <Route path="folders" element={<FolderList />} />
      <Route path="folders/:id/*" element={<FolderDetail />} />
      <Route path="users" element={<UserList />} />
      <Route path="users/new" element={<UserNew />} />
      <Route path="users/:id/*" element={<UserDetail />} />
      <Route path="credential-types" element={<CredentialTypeList />} />
      <Route path="roles" element={<RoleDefinitionList />} />
      <Route path="host-metrics" element={<HostMetrics />} />
      <Route path="instances" element={<InstanceList />} />
      <Route path="instances/:id" element={<InstanceDetail />} />
      <Route path="instance-groups/new" element={<GroupNew />} />
      <Route path="instance-groups/:id/*" element={<GroupDetail />} />
      <Route path="notifications" element={<NotificationList />} />
      <Route path="notifications/new" element={<NotificationNew />} />
      <Route path="notifications/:id/*" element={<NotificationDetail />} />
      <Route path="gitops" element={<GitOpsList />} />
      <Route path="gitops/new" element={<GitOpsNew />} />
      <Route path="gitops/:id/*" element={<GitOpsDetail />} />
      <Route path="management-jobs" element={<ManagementList />} />
      <Route path="management-jobs/:id/*" element={<ManagementDetail />} />
      <Route path="activity" element={<Activity />} />
      <Route path="settings" element={<Settings />} />
      <Route path="settings/:category" element={<Settings />} />
    </>
  )
}
