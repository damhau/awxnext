import { Route, useParams } from 'react-router'
import { LaunchPage, RunHome } from './Launch'
import { TemplateDetail, type Kind } from './TemplateDetail'
import { TemplateForm } from './TemplateForm'
import { TemplateList } from './TemplateList'

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
