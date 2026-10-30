// A user's UI preferences kept on the server (/api/v2/me/preferences/), so
// every browser sees them: saved views, starred templates, list settings.
// What a browser kept before is uploaded the first time.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useCallback, useEffect, useRef } from 'react'
import { api } from '../api/client'
import { forget, load } from './storage'

/** The preferences kept on the server; the rest stay in the browser. */
export const SERVER_PREFS = ['starred-templates', 'template-views', 'templates-folder-tree']

export type Prefs = Record<string, unknown>

const queryKey = ['me/preferences/']

/** What this browser kept that the server has not got: uploaded once. A
 * preference the server has wins. */
export function pendingUploads(server: Prefs, local: (key: string) => unknown): Prefs {
  const out: Prefs = {}
  for (const k of SERVER_PREFS) {
    if (k in server) continue
    const v = local(k)
    if (v !== undefined && v !== null) out[k] = v
  }
  return out
}

export function usePrefs() {
  return useQuery({ queryKey, queryFn: () => api.get<Prefs>('me/preferences/'), staleTime: 60_000 })
}

/** A preference, saved on the server when set (shown at once). */
export function usePref<T>(key: string, fallback: T): [T, (v: T | ((prev: T) => T)) => void] {
  const qc = useQueryClient()
  const prefs = usePrefs()
  const value = (prefs.data && key in prefs.data ? prefs.data[key] : fallback) as T
  const set = useCallback(
    (v: T | ((prev: T) => T)) => {
      const cur = qc.getQueryData<Prefs>(queryKey) ?? {}
      const prev = (key in cur ? cur[key] : fallback) as T
      const next = typeof v === 'function' ? (v as (p: T) => T)(prev) : v
      qc.setQueryData<Prefs>(queryKey, { ...cur, [key]: next })
      api
        .patch<Prefs>('me/preferences/', { [key]: next })
        .then((saved) => qc.setQueryData(queryKey, saved))
        .catch(() => qc.invalidateQueries({ queryKey }))
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key, qc],
  )
  return [value, set]
}

/** Uploads, once, the preferences this browser kept before they moved to
 * the server, then forgets the browser's copies. */
export function useUploadLocalPrefs() {
  const prefs = usePrefs()
  const qc = useQueryClient()
  const done = useRef(false)
  useEffect(() => {
    if (!prefs.data || done.current) return
    done.current = true
    const up = pendingUploads(prefs.data, (k) => load<unknown>(k, undefined))
    const clear = () => SERVER_PREFS.forEach(forget)
    if (!Object.keys(up).length) {
      clear()
      return
    }
    api
      .patch<Prefs>('me/preferences/', up)
      .then((saved) => {
        qc.setQueryData(queryKey, saved)
        clear()
      })
      .catch(() => {
        done.current = false
      })
  }, [prefs.data, qc])
}
