// Light or dark (roadmap M7-13): the system's, unless the user picks one.
// The choice is kept in this browser; public/theme.js applies it before the
// first paint, and this keeps it in step while the app runs.
import { useEffect } from 'react'
import { useStored } from './storage'

export type ThemeChoice = 'system' | 'light' | 'dark'

export const THEMES: { value: ThemeChoice; label: string }[] = [
  { value: 'system', label: 'System' },
  { value: 'light', label: 'Light' },
  { value: 'dark', label: 'Dark' },
]

export function resolveTheme(choice: ThemeChoice | string, systemDark: boolean): 'light' | 'dark' {
  if (choice === 'light' || choice === 'dark') return choice
  return systemDark ? 'dark' : 'light'
}

const query = () => (typeof window !== 'undefined' && window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null)

export function useTheme(): [ThemeChoice, (t: ThemeChoice) => void] {
  const [choice, setChoice] = useStored<ThemeChoice>('theme', 'system')
  useEffect(() => {
    const mq = query()
    const apply = () => document.documentElement.setAttribute('data-theme', resolveTheme(choice, !!mq?.matches))
    apply()
    if (choice !== 'system' || !mq) return
    mq.addEventListener('change', apply)
    return () => mq.removeEventListener('change', apply)
  }, [choice])
  return [choice, setChoice]
}
