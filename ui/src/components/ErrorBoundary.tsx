import { Component, type ErrorInfo, type ReactNode } from 'react'

interface Props {
  children: ReactNode
  /** The error clears when this changes (e.g. the current path). */
  resetKey?: unknown
}

/**
 * Shows an error in place of a component tree that failed to render,
 * instead of React unmounting the whole app. Uses plain links so it works
 * outside the router too.
 */
export class ErrorBoundary extends Component<Props, { error: Error | null }> {
  state: { error: Error | null } = { error: null }

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error(error, info.componentStack)
  }

  componentDidUpdate(prev: Props) {
    if (this.state.error && prev.resetKey !== this.props.resetKey) this.setState({ error: null })
  }

  render() {
    const { error } = this.state
    if (!error) return this.props.children
    // After an upgrade, the browser may ask for page chunks that no longer exist.
    const stale = /dynamically imported module|Importing a module script failed|ChunkLoadError/i.test(error.message)
    return (
      <div className="page">
        <div className="page-body" style={{ maxWidth: 720 }}>
          <div className="form-error" role="alert">
            <b style={{ fontWeight: 600 }}>{stale ? 'awxnext was updated.' : 'This page hit an error.'}</b>{' '}
            {stale ? 'Reload to get the new version.' : error.message}
          </div>
          <div className="row">
            <button className="btn primary" onClick={() => window.location.reload()}>
              Reload
            </button>
            <a className="btn" href="/">
              Back to Now
            </a>
          </div>
        </div>
      </div>
    )
  }
}
