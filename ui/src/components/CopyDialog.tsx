import { useState, type FormEvent, type ReactNode } from 'react'
import { useNavigate } from 'react-router'
import { api, ApiError } from '../api/client'
import { useInvalidate } from '../api/hooks'
import { TextInput } from './form'
import { Dialog, useToast } from './overlay'
import { tr } from '../lib/i18n'

interface Copyable {
  id: number
  name: string
}

/**
 * Copies an object through the API's /copy/ endpoint under a new name, then
 * opens the copy at `to(id)` or hands it to `onCopied`. `what` says what comes
 * along (hosts, credentials, ...).
 */
export function CopyDialog({
  resource,
  obj,
  to,
  onCopied,
  what,
  invalidates = [],
  onClose,
}: {
  resource: string
  obj: Copyable
  to?: (id: number) => string
  onCopied?: (copy: Copyable) => void
  what?: ReactNode
  invalidates?: string[]
  onClose: () => void
}) {
  const [name, setName] = useState(`${obj.name} (copy)`)
  const [error, setError] = useState<string>()
  const [busy, setBusy] = useState(false)
  const toast = useToast()
  const invalidate = useInvalidate()
  const nav = useNavigate()
  const submit = async (e?: FormEvent) => {
    e?.preventDefault()
    setBusy(true)
    setError(undefined)
    try {
      const r = await api.post<Copyable>(`${resource}/${obj.id}/copy/`, { name: name.trim() })
      toast(tr('Copied to "{name}"', { name: r.name }))
      invalidate(resource, ...invalidates)
      onClose()
      if (to) nav(to(r.id))
      onCopied?.(r)
    } catch (err) {
      setError(err instanceof ApiError ? (err.fields.name ?? err.message) : (err as Error).message)
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={tr('Copy {name}', { name: obj.name })}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" disabled={!name.trim() || busy} onClick={() => submit()}>
            {tr('Copy')}
          </button>
        </>
      }
    >
      <form onSubmit={submit}>
        <TextInput label={tr('Name of the copy')} value={name} onChange={setName} error={error} autoFocus />
        {what && <p className="small muted" style={{ margin: '8px 0 0' }}>{what}</p>}
      </form>
    </Dialog>
  )
}

/** State for a CopyDialog: `open(obj)` shows it, render `dialog` once. */
export function useCopy(resource: string, opts: { to?: (id: number) => string; onCopied?: (copy: Copyable) => void; what?: ReactNode; invalidates?: string[] }) {
  const [obj, setObj] = useState<Copyable | null>(null)
  const dialog = obj ? <CopyDialog resource={resource} obj={obj} {...opts} onClose={() => setObj(null)} /> : null
  return { open: setObj, dialog }
}
