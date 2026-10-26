import { Route, useParams } from 'react-router'

import type { Kind } from './TemplateDetail'
import { lazyModule } from '../../lib/lazy'

const m_Launch = lazyModule(() => import('./Launch'))
const LaunchPage = m_Launch('LaunchPage')
const RunHome = m_Launch('RunHome')
const m_TemplateDetail = lazyModule(() => import('./TemplateDetail'))
const TemplateDetail = m_TemplateDetail('TemplateDetail')
const m_TemplateForm = lazyModule(() => import('./TemplateForm'))
const TemplateForm = m_TemplateForm('TemplateForm')
const m_TemplateList = lazyModule(() => import('./TemplateList'))
const TemplateList = m_TemplateList('TemplateList')

function NewTemplate() {
  const { kind } = useParams()
  return <TemplateForm key={kind} kind={kind === 'workflow' ? 'workflow' : 'job'} />
}

function Detail() {
  const { kind, id } = useParams()
  return <TemplateDetail key={`${kind}-${id}`} kind={(kind === 'workflow' ? 'workflow' : 'job') as Kind} />
}

// Routes for templates, template details and the launch page.
export function templateRoutes() {
  return (
    <>
      <Route path="templates" element={<TemplateList />} />
      <Route path="templates/new/:kind" element={<NewTemplate />} />
      <Route path="templates/:kind/:id/*" element={<Detail />} />
      <Route path="launch/:kind/:id" element={<LaunchPage />} />
      <Route path="run" element={<RunHome />} />
      <Route path="run/:kind/:id" element={<LaunchPage operator />} />
    </>
  )
}
