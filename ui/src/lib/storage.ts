// Per-browser preferences (starred templates, saved views, nav state).
// localStorage can be unavailable (private mode, blocked storage): every
// access is guarded and the UI works without it.
import { useCallback, useEffect, useState } from 'react'

const prefix = 'awxnext:'

export function load<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(prefix + key)
    return raw === null ? fallback : (JSON.parse(raw) as T)
  } catch {
    return fallback
  }
}

export function save(key: string, value: unknown) {
  try {
    localStorage.setItem(prefix + key, JSON.stringify(value))
  } catch {
    /* storage unavailable */
  }
  window.dispatchEvent(new CustomEvent('awxnext-storage', { detail: key }))
}

/** State persisted in localStorage and shared between components using the same key. */
export function useStored<T>(key: string, fallback: T): [T, (v: T | ((prev: T) => T)) => void] {
  const [value, setValue] = useState<T>(() => load(key, fallback))
  useEffect(() => {
    const on = (e: Event) => {
      if ((e as CustomEvent).detail === key) setValue(load(key, fallback))
    }
    window.addEventListener('awxnext-storage', on)
    return () => window.removeEventListener('awxnext-storage', on)
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key])
  const set = useCallback(
    (v: T | ((prev: T) => T)) => {
      const next = typeof v === 'function' ? (v as (p: T) => T)(load(key, fallback)) : v
      save(key, next)
      setValue(next)
    },
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [key],
  )
  return [value, set]
}
