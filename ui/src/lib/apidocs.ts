/** A request as Swagger UI hands it to its requestInterceptor. */
export interface DocsRequest {
  url: string
  method?: string
  headers: Record<string, string>
  credentials?: RequestCredentials
}

/**
 * Sends the API docs' "try it out" calls as the UI's own: with the session
 * cookie, and the CSRF token on requests that change something.
 */
export function withSession(req: DocsRequest, csrfToken: string | undefined): DocsRequest {
  req.credentials = 'same-origin'
  const method = (req.method ?? 'GET').toUpperCase()
  if (csrfToken && !['GET', 'HEAD', 'OPTIONS'].includes(method)) {
    req.headers = { ...req.headers, 'X-CSRFToken': csrfToken }
  }
  return req
}
