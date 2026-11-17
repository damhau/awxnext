// Routes for the collection hub: collections and their docs, namespaces,
// the approval queue, remotes and the signing keys.
import { Route } from 'react-router'
import './hub.css'
import { lazyModule } from '../../lib/lazy'

const m_collections = lazyModule(() => import('./collections'))
const CollectionList = m_collections('CollectionList')
const CollectionDetail = m_collections('CollectionDetail')
const VersionRedirect = m_collections('VersionRedirect')
const m_namespaces = lazyModule(() => import('./namespaces'))
const NamespaceList = m_namespaces('NamespaceList')
const NamespaceNew = m_namespaces('NamespaceNew')
const NamespaceDetail = m_namespaces('NamespaceDetail')
const m_approvals = lazyModule(() => import('./approvals'))
const ApprovalList = m_approvals('ApprovalList')
const ReviewPage = m_approvals('ReviewPage')
const m_remotes = lazyModule(() => import('./remotes'))
const RemoteList = m_remotes('RemoteList')
const RemoteNew = m_remotes('RemoteNew')
const RemoteDetail = m_remotes('RemoteDetail')
const m_keys = lazyModule(() => import('./keys'))
const SigningKeys = m_keys('SigningKeys')

export function hubRoutes() {
  return (
    <>
      <Route path="collections" element={<CollectionList />} />
      <Route path="collections/:id/*" element={<CollectionDetail />} />
      <Route path="collection-versions/:id" element={<VersionRedirect />} />
      <Route path="collection-namespaces" element={<NamespaceList />} />
      <Route path="collection-namespaces/new" element={<NamespaceNew />} />
      <Route path="collection-namespaces/:id/*" element={<NamespaceDetail />} />
      <Route path="collection-approvals" element={<ApprovalList />} />
      <Route path="collection-approvals/:id" element={<ReviewPage />} />
      <Route path="collection-remotes" element={<RemoteList />} />
      <Route path="collection-remotes/new" element={<RemoteNew />} />
      <Route path="collection-remotes/:id/*" element={<RemoteDetail />} />
      <Route path="collection-signing-keys" element={<SigningKeys />} />
    </>
  )
}
