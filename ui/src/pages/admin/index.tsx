// Routes for organizations, teams, users, instances, notifications, GitOps,
// management jobs, activity stream and settings.
import { Route } from 'react-router'
import { Activity } from './activity'
import './admin.css'
import { GitOpsDetail, GitOpsList, GitOpsNew } from './gitops'
import { GroupDetail, GroupNew, InstanceDetail, InstanceList } from './instances'
import { ManagementDetail, ManagementList } from './management'
import { NotificationDetail, NotificationList, NotificationNew } from './notifications'
import { OrganizationDetail, OrganizationList, OrganizationNew } from './organizations'
import { Settings } from './settings'
import { TeamDetail, TeamList, TeamNew } from './teams'
import { UserDetail, UserList, UserNew } from './users'

export function adminRoutes() {
  return (
    <>
      <Route path="organizations" element={<OrganizationList />} />
      <Route path="organizations/new" element={<OrganizationNew />} />
      <Route path="organizations/:id/*" element={<OrganizationDetail />} />
      <Route path="teams" element={<TeamList />} />
      <Route path="teams/new" element={<TeamNew />} />
      <Route path="teams/:id/*" element={<TeamDetail />} />
      <Route path="users" element={<UserList />} />
      <Route path="users/new" element={<UserNew />} />
      <Route path="users/:id/*" element={<UserDetail />} />
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
