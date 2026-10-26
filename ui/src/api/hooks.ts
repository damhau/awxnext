import { keepPreviousData, useMutation, useQuery, useQueryClient, type UseQueryOptions } from '@tanstack/react-query'
import { api, type Page, type Params } from './client'
import type { OptionsMeta } from './types'

/** List query keyed by resource path and params. */
export function useList<T>(path: string | null, params?: Params, opts: Partial<UseQueryOptions<Page<T>>> = {}) {
  return useQuery<Page<T>>({
    queryKey: [path, 'list', params ?? {}],
    queryFn: ({ signal }) => api.get<Page<T>>(path as string, params, signal),
    enabled: path !== null,
    placeholderData: keepPreviousData,
    ...opts,
  })
}

/** Detail query; path like "job_templates/7/". */
export function useObject<T>(path: string | null, opts: Partial<UseQueryOptions<T>> = {}) {
  return useQuery<T>({
    queryKey: [path],
    queryFn: ({ signal }) => api.get<T>(path as string, undefined, signal),
    enabled: path !== null,
    ...opts,
  })
}

export function useOptions(path: string | null) {
  return useQuery<OptionsMeta>({
    queryKey: [path, 'options'],
    queryFn: () => api.options<OptionsMeta>(path as string),
    enabled: path !== null,
    staleTime: 5 * 60_000,
  })
}

/** Invalidates every query whose key starts with one of the given resource paths. */
export function useInvalidate() {
  const qc = useQueryClient()
  return (...prefixes: string[]) =>
    qc.invalidateQueries({
      predicate: (q) => {
        const k = q.queryKey[0]
        return typeof k === 'string' && prefixes.some((p) => k.startsWith(p))
      },
    })
}

/** A mutation that invalidates resource prefixes when it succeeds. */
export function useApiMutation<V, R = unknown>(fn: (v: V) => Promise<R>, invalidates: string[] = []) {
  const invalidate = useInvalidate()
  return useMutation<R, Error, V>({
    mutationFn: fn,
    onSuccess: () => {
      if (invalidates.length) invalidate(...invalidates)
    },
  })
}
