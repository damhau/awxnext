import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { lazy, Suspense, useState } from 'react'
import { createBrowserRouter, createRoutesFromElements, Link, Outlet, Route, RouterProvider, useRouteError } from 'react-router'
import { ApiError } from './api/client'
import { OrgProvider, SessionGate } from './app/context'
import { Shell } from './app/Shell'
import { ErrorBoundary, ErrorFallback } from './components/ErrorBoundary'
import { ConfirmProvider, ToastProvider } from './components/overlay'
import { Empty, Loading } from './components/ui'
import { Now } from './pages/Now'
import { adminRoutes } from './pages/admin'
import { automationRoutes } from './pages/automation'
import { jobRoutes } from './pages/jobs'
import { resourceRoutes } from './pages/resources'
import { templateRoutes } from './pages/templates'
import { tr } from './lib/i18n'

// Swagger UI is large: loaded only on the API page.
const ApiDocs = lazy(() => import('./pages/ApiDocs'))

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
      <Empty title={tr('This page does not exist')} action={<Link to="/" className="btn">{tr('Back to Now')}</Link>}>
        {tr('The link may be outdated, or the object was deleted.')}
      </Empty>
    </div>
  )
}

// The providers are inside the router: toasts and dialogs contain links,
// which only work inside it.
function Root() {
  return (
    <ToastProvider>
      <ConfirmProvider>
        <SessionGate>
          <OrgProvider>
            <Outlet />
          </OrgProvider>
        </SessionGate>
      </ConfirmProvider>
    </ToastProvider>
  )
}

/** An error the shell's own boundary did not catch, caught by the router. */
function RouteError() {
  const e = useRouteError()
  return <ErrorFallback error={e instanceof Error ? e : new Error(String((e as { statusText?: string })?.statusText ?? e))} />
}

// A data router: a page can ask before it is left (useBlocker), as the
// settings do when changes are not saved.
const routes = createRoutesFromElements(
  <Route element={<Root />} errorElement={<RouteError />}>
    <Route element={<Shell />}>
      <Route index element={<Now />} />
      {templateRoutes()}
      {jobRoutes()}
      {automationRoutes()}
      {resourceRoutes()}
      {adminRoutes()}
      <Route path="api/docs" element={<Suspense fallback={<Loading />}><ApiDocs /></Suspense>} />
      <Route path="*" element={<NotFound />} />
    </Route>
  </Route>,
)

export function App() {
  const [router] = useState(() => createBrowserRouter(routes))
  return (
    <ErrorBoundary>
      <QueryClientProvider client={queryClient}>
        <RouterProvider router={router} />
      </QueryClientProvider>
    </ErrorBoundary>
  )
}
