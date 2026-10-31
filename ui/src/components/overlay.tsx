import { createContext, useCallback, useContext, useEffect, useLayoutEffect, useRef, useState, type KeyboardEvent as ReactKeyboardEvent, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useBlocker } from 'react-router'
import { Icon } from './icons'
import { tr, trn } from '../lib/i18n'
import { placeMenu } from '../lib/menuPosition'

/** Modal dialog: focus moves inside, Escape and backdrop clicks close it. */
export function Dialog({
  title,
  onClose,
  children,
  footer,
  wide,
  label,
}: {
  title: ReactNode
  onClose: () => void
  children: ReactNode
  footer?: ReactNode
  wide?: boolean
  label?: string
}) {
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    const prev = document.activeElement as HTMLElement | null
    const first = ref.current?.querySelector<HTMLElement>('input, select, textarea, button:not(.dialog-close), [tabindex]')
    ;(first ?? ref.current)?.focus()
    const on = (e: KeyboardEvent) => {
      if (e.key === 'Escape') {
        e.stopPropagation()
        onClose()
      }
    }
    window.addEventListener('keydown', on)
    return () => {
      window.removeEventListener('keydown', on)
      prev?.focus?.()
    }
  }, [onClose])
  return createPortal(
    <div className="overlay" onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div ref={ref} role="dialog" aria-modal="true" aria-label={label ?? (typeof title === 'string' ? title : undefined)} className={`dialog ${wide ? 'wide' : ''}`} tabIndex={-1}>
        <div className="dialog-head">
          <h2>{title}</h2>
          <button className="btn ghost sm icon dialog-close" style={{ marginLeft: 'auto' }} onClick={onClose} aria-label={tr('Close')}>
            <Icon name="x" />
          </button>
        </div>
        <div className="dialog-body">{children}</div>
        {footer && <div className="dialog-foot">{footer}</div>}
      </div>
    </div>,
    document.body,
  )
}

interface ConfirmOpts {
  title: string
  body?: ReactNode
  confirm?: string
  danger?: boolean
}

const ConfirmCtx = createContext<(o: ConfirmOpts) => Promise<boolean>>(async () => false)

export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<(ConfirmOpts & { resolve: (v: boolean) => void }) | null>(null)
  const ask = useCallback((o: ConfirmOpts) => new Promise<boolean>((resolve) => setState({ ...o, resolve })), [])
  const close = (v: boolean) => {
    state?.resolve(v)
    setState(null)
  }
  return (
    <ConfirmCtx.Provider value={ask}>
      {children}
      {state && (
        <Dialog
          title={state.title}
          onClose={() => close(false)}
          footer={
            <>
              <button className="btn" onClick={() => close(false)}>
                {tr('Cancel')}
              </button>
              <button className={`btn ${state.danger ? 'danger solid' : 'primary'}`} onClick={() => close(true)} autoFocus>
                {state.confirm ?? tr('Confirm')}
              </button>
            </>
          }
        >
          {state.body ?? <p style={{ margin: 0 }}>{tr('Are you sure?')}</p>}
        </Dialog>
      )}
    </ConfirmCtx.Provider>
  )
}

export const useConfirm = () => useContext(ConfirmCtx)

/**
 * Asks before a page with changes not saved (`count`) is left: by a link,
 * the palette or Back, when `leaving` says the next path is a way out; and,
 * through the browser, before the tab is closed or reloaded.
 */
export function useLeaveGuard(count: number, leaving: (path: string) => boolean = () => true) {
  const confirm = useConfirm()
  const blocker = useBlocker(({ currentLocation, nextLocation }) => count > 0 && currentLocation.pathname !== nextLocation.pathname && leaving(nextLocation.pathname))
  useEffect(() => {
    if (blocker.state !== 'blocked') return
    confirm({
      title: tr('Leave without saving?'),
      body: <p style={{ margin: 0 }}>{trn(count, '{n} change is not saved: it is lost if you leave.', '{n} changes are not saved: they are lost if you leave.')}</p>,
      confirm: tr('Leave'),
      danger: true,
    }).then((ok) => (ok ? blocker.proceed() : blocker.reset()))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [blocker.state])
  const dirty = count > 0
  useEffect(() => {
    if (!dirty) return
    const on = (e: BeforeUnloadEvent) => e.preventDefault()
    window.addEventListener('beforeunload', on)
    return () => window.removeEventListener('beforeunload', on)
  }, [dirty])
}

interface Toast {
  id: number
  text: ReactNode
  kind: 'info' | 'error'
}

const ToastCtx = createContext<(text: ReactNode, kind?: 'info' | 'error') => void>(() => {})

export function ToastProvider({ children }: { children: ReactNode }) {
  const [toasts, setToasts] = useState<Toast[]>([])
  const push = useCallback((text: ReactNode, kind: 'info' | 'error' = 'info') => {
    const id = Date.now() + Math.random()
    setToasts((t) => [...t.slice(-3), { id, text, kind }])
    window.setTimeout(() => setToasts((t) => t.filter((x) => x.id !== id)), kind === 'error' ? 9000 : 5000)
  }, [])
  return (
    <ToastCtx.Provider value={push}>
      {children}
      <div className="toasts" role="status" aria-live="polite">
        {toasts.map((t) => (
          <div key={t.id} className={`toast ${t.kind === 'error' ? 'error' : ''}`}>
            <div>{t.text}</div>
            <button aria-label={tr('Dismiss')} onClick={() => setToasts((all) => all.filter((x) => x.id !== t.id))}>
              <Icon name="x" size={14} />
            </button>
          </div>
        ))}
      </div>
    </ToastCtx.Provider>
  )
}

export const useToast = () => useContext(ToastCtx)

/** Dropdown menu anchored to a trigger button. */
export function Menu({
  label,
  trigger,
  children,
  align = 'right',
  className = 'btn icon',
}: {
  label: string
  trigger?: ReactNode
  children: ReactNode | ((close: () => void) => ReactNode)
  align?: 'left' | 'right'
  className?: string
}) {
  const [open, setOpen] = useState(false)
  const [pos, setPos] = useState<{ top: number; left: number } | null>(null)
  const ref = useRef<HTMLDivElement>(null)
  const button = useRef<HTMLButtonElement>(null)
  const list = useRef<HTMLDivElement>(null)
  // Closing from the menu gives the focus back to its button.
  const close = () => {
    setOpen(false)
    button.current?.focus()
  }
  useEffect(() => {
    if (!open) return
    const on = (e: MouseEvent) => {
      const t = e.target as Node
      if (!ref.current?.contains(t) && !list.current?.contains(t)) setOpen(false)
    }
    const key = (e: KeyboardEvent) => {
      if (e.key !== 'Escape') return
      setOpen(false)
      button.current?.focus()
    }
    document.addEventListener('mousedown', on)
    window.addEventListener('keydown', key)
    return () => {
      document.removeEventListener('mousedown', on)
      window.removeEventListener('keydown', key)
    }
  }, [open])
  // The list is drawn on the body, so that a scrolling container (a table)
  // cannot clip it. It is placed on its button in a layout effect, so the
  // first paint already has it there (and it stays focusable: not hidden).
  useLayoutEffect(() => {
    if (!open) return
    const place = () => {
      const b = button.current?.getBoundingClientRect()
      const l = list.current
      if (b && l) setPos(placeMenu(b, { width: l.offsetWidth, height: l.offsetHeight }, { width: window.innerWidth, height: window.innerHeight }, align))
    }
    place()
    window.addEventListener('resize', place)
    window.addEventListener('scroll', place, true)
    return () => {
      window.removeEventListener('resize', place)
      window.removeEventListener('scroll', place, true)
    }
  }, [open, align])
  // Opening moves the focus to the first item: the list is not after its
  // button in the tab order.
  useEffect(() => {
    if (open) list.current?.querySelector<HTMLElement>(MENU_ITEMS)?.focus({ preventScroll: true })
  }, [open])
  const onKey = (e: ReactKeyboardEvent) => {
    if (e.key === 'Tab') {
      // Back to the button, from which Tab goes on to what follows it.
      close()
    } else if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault()
      const items = Array.from(list.current?.querySelectorAll<HTMLElement>(MENU_ITEMS) ?? [])
      const at = items.indexOf(document.activeElement as HTMLElement)
      const next = e.key === 'ArrowDown' ? at + 1 : at - 1
      items[(next + items.length) % items.length]?.focus()
    }
  }
  return (
    <div className="menu" ref={ref}>
      <button ref={button} type="button" className={className} aria-haspopup="menu" aria-expanded={open} aria-label={label} onClick={() => setOpen((o) => !o)}>
        {trigger ?? <Icon name="more" />}
      </button>
      {open &&
        createPortal(
          <div
            ref={list}
            className="menu-list"
            role="menu"
            aria-label={label}
            style={{ top: pos?.top ?? 0, left: pos?.left ?? 0 }}
            onKeyDown={onKey}
            onClick={(e) => (e.target as HTMLElement).closest('button,a') && close()}
          >
            {typeof children === 'function' ? children(close) : children}
          </div>,
          document.body,
        )}
    </div>
  )
}

const MENU_ITEMS = 'button:not([disabled]), a[href], input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])'
