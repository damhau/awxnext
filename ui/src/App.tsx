import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { BrowserRouter, Link, Route, Routes } from 'react-router'
import { ApiError } from './api/client'
import { OrgProvider, SessionGate } from './app/context'
import { Shell } from './app/Shell'
import { ConfirmProvider, ToastProvider } from './components/overlay'
import { Empty } from './components/ui'
import { Now } from './pages/Now'
import { adminRoutes } from './pages/admin'
import { automationRoutes } from './pages/automation'
import { jobRoutes } from './pages/jobs'
import { resourceRoutes } from './pages/resources'
import { templateRoutes } from './pages/templates'

const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      staleTime: 15_000,
      refetchOnWindowFocus: true,
      retry: (n, e) => !(e instanceof ApiError && e.status >= 400 && e.status < 500) && n < 2,
    },
  },
})

function NotFound() {
  return (
    <div className="page">
      <Empty title="This page does not exist" action={<Link to="/" className="btn">Back to Now</Link>}>
        The link may be outdated, or the object was deleted.
      </Empty>
    </div>
  )
}

export function App() {
  return (
    <QueryClientProvider client={queryClient}>
      <ToastProvider>
        <ConfirmProvider>
          <BrowserRouter>
            <SessionGate>
              <OrgProvider>
                <Routes>
                  <Route element={<Shell />}>
                    <Route index element={<Now />} />
                    {templateRoutes()}
                    {jobRoutes()}
                    {automationRoutes()}
                    {resourceRoutes()}
                    {adminRoutes()}
                    <Route path="*" element={<NotFound />} />
                  </Route>
                </Routes>
              </OrgProvider>
            </SessionGate>
          </BrowserRouter>
        </ConfirmProvider>
      </ToastProvider>
    </QueryClientProvider>
  )
}
