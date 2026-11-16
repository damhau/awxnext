// Routes for event streams, their events, event rules, maintenance windows
// and event connectors.
import { Route } from 'react-router'
import './events.css'
import { lazyModule } from '../../lib/lazy'

const m_streams = lazyModule(() => import('./streams'))
const EventPage = m_streams('EventPage')
const StreamDetail = m_streams('StreamDetail')
const StreamList = m_streams('StreamList')
const StreamNew = m_streams('StreamNew')
const m_rules = lazyModule(() => import('./rules'))
const RuleDetail = m_rules('RuleDetail')
const RuleList = m_rules('RuleList')
const RuleNew = m_rules('RuleNew')
const m_connectors = lazyModule(() => import('./connectors'))
const ConnectorDetail = m_connectors('ConnectorDetail')
const ConnectorList = m_connectors('ConnectorList')
const ConnectorNew = m_connectors('ConnectorNew')
const m_windows = lazyModule(() => import('./windows'))
const WindowDetail = m_windows('WindowDetail')
const WindowList = m_windows('WindowList')
const WindowNew = m_windows('WindowNew')

export function eventRoutes() {
  return (
    <>
      <Route path="event-streams" element={<StreamList />} />
      <Route path="event-streams/new" element={<StreamNew />} />
      <Route path="event-streams/:id/events/:seq" element={<EventPage />} />
      <Route path="event-streams/:id/*" element={<StreamDetail />} />
      <Route path="event-rules" element={<RuleList />} />
      <Route path="event-rules/new" element={<RuleNew />} />
      <Route path="event-rules/:id/*" element={<RuleDetail />} />
      <Route path="event-connectors" element={<ConnectorList />} />
      <Route path="event-connectors/new" element={<ConnectorNew />} />
      <Route path="event-connectors/:id/*" element={<ConnectorDetail />} />
      <Route path="maintenance-windows" element={<WindowList />} />
      <Route path="maintenance-windows/new" element={<WindowNew />} />
      <Route path="maintenance-windows/:id/*" element={<WindowDetail />} />
    </>
  )
}
