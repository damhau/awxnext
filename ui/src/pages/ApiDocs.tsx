import { useEffect, useRef } from 'react'
import SwaggerUIBundle from 'swagger-ui-dist/swagger-ui-es-bundle.js'
import 'swagger-ui-dist/swagger-ui.css'
import { cookie } from '../api/client'
import { PageHead } from '../components/ui'
import { withSession, type DocsRequest } from '../lib/apidocs'
import { tr } from '../lib/i18n'

/**
 * The API, browsable and callable with the session (it replaces AWX's
 * browsable API): Swagger UI over /api/v2/openapi.json.
 */
export default function ApiDocs() {
  const node = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!node.current) return
    SwaggerUIBundle({
      domNode: node.current,
      url: '/api/v2/openapi.json',
      deepLinking: true,
      tryItOutEnabled: true,
      displayRequestDuration: true,
      filter: true,
      requestInterceptor: (req: DocsRequest) => withSession(req, cookie('csrftoken')),
    })
  }, [])
  return (
    <div className="page">
      <PageHead title={tr('API')} subtitle={tr('Every endpoint of /api/v2/, callable with your session.')} />
      <div className="page-body">
        <div ref={node} className="api-docs" />
      </div>
    </div>
  )
}
