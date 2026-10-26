import { lazy, type ComponentType } from 'react'

/**
 * Splits a page module into its own chunk: lazyModule(() => import('./x'))
 * returns a picker for named component exports, all sharing one load.
 */
export function lazyModule<M extends Record<string, unknown>>(load: () => Promise<M>) {
  let p: Promise<M> | null = null
  const get = () => (p ??= load())
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  return <K extends keyof M>(name: K) => lazy(() => get().then((m) => ({ default: m[name] as ComponentType<any> })))
}
