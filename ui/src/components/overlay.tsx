import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { Icon } from './icons'
import { tr } from '../lib/i18n'

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
  const ref = useRef<HTMLDivElement>(null)
  useEffect(() => {
    if (!open) return
    const on = (e: MouseEvent) => {
      if (!ref.current?.contains(e.target as Node)) setOpen(false)
    }
    const key = (e: KeyboardEvent) => e.key === 'Escape' && setOpen(false)
    document.addEventListener('mousedown', on)
    window.addEventListener('keydown', key)
    return () => {
      document.removeEventListener('mousedown', on)
      window.removeEventListener('keydown', key)
    }
  }, [open])
  const close = () => setOpen(false)
  return (
    <div className="menu" ref={ref}>
      <button type="button" className={className} aria-haspopup="menu" aria-expanded={open} aria-label={label} onClick={() => setOpen((o) => !o)}>
        {trigger ?? <Icon name="more" />}
      </button>
      {open && (
        <div className={`menu-list ${align === 'left' ? 'left' : ''}`} role="menu" onClick={(e) => (e.target as HTMLElement).closest('button,a') && close()}>
          {typeof children === 'function' ? children(close) : children}
        </div>
      )}
    </div>
  )
}
