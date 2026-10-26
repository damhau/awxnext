import { useEffect, useRef } from 'react'

/** True when the event target is a text field, where single-key shortcuts must not fire. */
export function typing(e: KeyboardEvent): boolean {
  const t = e.target as HTMLElement | null
  if (!t) return false
  const tag = t.tagName
  return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT' || t.isContentEditable
}

/**
 * Binds keys while the component is mounted. Keys are like "j", "shift+x",
 * "mod+k" (Cmd on macOS, Ctrl elsewhere), "escape". Single keys are ignored
 * while typing in a field.
 */
export function useHotkeys(bindings: Record<string, (e: KeyboardEvent) => void>, enabled = true) {
  const ref = useRef(bindings)
  ref.current = bindings
  useEffect(() => {
    if (!enabled) return
    const on = (e: KeyboardEvent) => {
      const parts: string[] = []
      if (e.metaKey || e.ctrlKey) parts.push('mod')
      if (e.shiftKey && e.key.length > 1) parts.push('shift')
      if (e.altKey) parts.push('alt')
      parts.push(e.key.toLowerCase())
      const combo = parts.join('+')
      const fn = ref.current[combo]
      if (!fn) return
      if (!combo.startsWith('mod+') && combo !== 'escape' && typing(e)) return
      if (document.querySelector('.overlay') && combo !== 'escape' && !combo.startsWith('mod+')) return
      e.preventDefault()
      fn(e)
    }
    window.addEventListener('keydown', on)
    return () => window.removeEventListener('keydown', on)
  }, [enabled])
}
