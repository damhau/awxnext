import { useState, type FormEvent } from 'react'
import { useNavigate } from 'react-router'
import { api } from '../api/client'
import type { Job } from '../api/types'
import { jobApi, jobPath } from '../lib/format'
import { passwordLabel, relaunchJob } from '../lib/jobs'
import { Dialog, useToast } from './overlay'

type Relaunchable = Pick<Job, 'id' | 'type' | 'unified_job_template'>

/**
 * Relaunches a job and opens the new one. When the job's credentials ask for
 * passwords at launch, a dialog asks for them first, as the launch page does.
 */
export function useRelaunch() {
  const [ask, setAsk] = useState<{ job: Relaunchable; body: Record<string, unknown>; needed: string[] } | null>(null)
  const nav = useNavigate()
  const toast = useToast()
  const run = async (job: Relaunchable, body: Record<string, unknown>) => {
    try {
      nav(jobPath(await relaunchJob(job, body)))
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  const relaunch = async (job: Relaunchable, body: Record<string, unknown> = {}) => {
    if (job.type === 'job' || job.type === 'workflow_job') {
      try {
        const info = await api.get<{ passwords_needed_to_start?: string[] }>(`${jobApi(job.type, job.id)}relaunch/`)
        if (info.passwords_needed_to_start?.length) {
          setAsk({ job, body, needed: info.passwords_needed_to_start })
          return
        }
      } catch (e) {
        toast((e as Error).message, 'error')
        return
      }
    }
    await run(job, body)
  }
  const dialog = ask && (
    <PasswordsDialog
      needed={ask.needed}
      onClose={() => setAsk(null)}
      onSubmit={(passwords) => {
        setAsk(null)
        run(ask.job, { ...ask.body, credential_passwords: passwords })
      }}
    />
  )
  return { relaunch, dialog }
}

function PasswordsDialog({ needed, onClose, onSubmit }: { needed: string[]; onClose: () => void; onSubmit: (p: Record<string, string>) => void }) {
  const [values, setValues] = useState<Record<string, string>>({})
  const missing = needed.filter((k) => !values[k])
  const submit = (e?: FormEvent) => {
    e?.preventDefault()
    if (!missing.length) onSubmit(values)
  }
  return (
    <Dialog
      title="Passwords to relaunch"
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={missing.length > 0} onClick={() => submit()}>
            Relaunch
          </button>
        </>
      }
    >
      <form className="stack" style={{ gap: 14 }} onSubmit={submit}>
        <p className="muted" style={{ margin: 0 }}>
          The job's credentials ask for these at launch. They are used for this run only.
        </p>
        {needed.map((k, i) => (
          <div key={k} className="field">
            <label htmlFor={`pw-${k}`}>{passwordLabel(k)}</label>
            <input id={`pw-${k}`} className="input" type="password" autoComplete="off" autoFocus={i === 0} value={values[k] ?? ''}
              onChange={(e) => setValues((v) => ({ ...v, [k]: e.target.value }))} style={{ maxWidth: 320 }} />
          </div>
        ))}
        <button type="submit" hidden />
      </form>
    </Dialog>
  )
}
