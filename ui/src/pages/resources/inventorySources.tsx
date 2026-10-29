import { useState, type FormEvent } from 'react'
import { Link, useSearchParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useOptions } from '../../api/hooks'
import { useLive } from '../../api/live'
import type { Base, Ref } from '../../api/types'
import { Check, FormError, Lookup, SelectField, TextInput, VarsEditor, useForm, varsError } from '../../components/form'
import { Icon } from '../../components/icons'
import { NotificationsDialog } from '../../components/NotificationsTab'
import { Dialog, Menu, useConfirm, useToast } from '../../components/overlay'
import { DataTable, type Column } from '../../components/table'
import { Ago, Empty, RunBars, Status } from '../../components/ui'
import { applyErrors, refOf } from './common'
import { useSubList, type Inventory } from './inventories'

export interface Source extends Base {
  inventory: number
  source: string
  source_path: string
  source_project: number | null
  source_vars: string
  credential: number | null
  execution_environment: number | null
  status: string
  last_updated: string | null
  last_job_failed: boolean
  update_on_launch: boolean
  update_cache_timeout: number
  overwrite: boolean
  overwrite_vars: boolean
  enabled_var: string
  enabled_value: string
  host_filter: string
  limit: string
  scm_branch: string
  timeout: number
  verbosity: number
  managed_by?: string
}

/** Human names and the credential type kind each cloud source uses. */
export const sourceInfo: Record<string, { label: string; credential?: string; credentialKind?: string; hint?: string }> = {
  scm: { label: 'Sourced from a project', hint: 'An inventory file or plugin config in a project repository.' },
  ec2: { label: 'Amazon EC2', credential: 'Amazon Web Services', credentialKind: 'cloud' },
  gce: { label: 'Google Compute Engine', credential: 'Google Compute Engine', credentialKind: 'cloud' },
  azure_rm: { label: 'Microsoft Azure Resource Manager', credential: 'Microsoft Azure Resource Manager', credentialKind: 'cloud' },
  vmware: { label: 'VMware vCenter', credential: 'VMware vCenter', credentialKind: 'cloud' },
  satellite6: { label: 'Red Hat Satellite 6', credential: 'Red Hat Satellite 6', credentialKind: 'cloud' },
  openstack: { label: 'OpenStack', credential: 'OpenStack', credentialKind: 'cloud' },
  rhv: { label: 'Red Hat Virtualization', credential: 'Red Hat Virtualization', credentialKind: 'cloud' },
  controller: { label: 'Red Hat Ansible Automation Platform', credential: 'Red Hat Ansible Automation Platform', credentialKind: 'cloud' },
  insights: { label: 'Red Hat Insights', credential: 'Insights', credentialKind: 'insights' },
  terraform: { label: 'Terraform state', credential: 'Terraform backend configuration', credentialKind: 'cloud' },
  constructed: { label: 'Constructed' },
}

export function InventorySourcesTab({ inv }: { inv: Inventory }) {
  const path = `inventories/${inv.id}/inventory_sources/`
  const sources = useSubList<Source>(path, { order_by: 'name', page_size: 100 })
  const [sp, setSp] = useSearchParams()
  const [edit, setEdit] = useState<null | { source?: Source }>(null)
  // ?source=<id>&notifications=1 (links from a notification) opens that source's notifications.
  const [notify, setNotify] = useState<number | null>(sp.get('notifications') ? Number(sp.get('source')) || null : null)
  const invalidate = useInvalidate()
  const toast = useToast()
  const confirm = useConfirm()
  const editable = !inv.managed_by && !!inv.summary_fields.user_capabilities?.edit
  const highlight = Number(sp.get('source') ?? 0)
  useLive({ jobs: ['status_changed'] }, (m) => {
    if (m.type === 'inventory_update') invalidate(path)
  })
  const sync = async (s: Source) => {
    try {
      const r = await api.post<{ inventory_update?: number; id?: number }>(`inventory_sources/${s.id}/update/`)
      const jid = r.inventory_update ?? r.id
      toast(
        <span>
          Updating {s.name}
          {jid ? (
            <>
              {' · '}
              <Link to={`/jobs/inventory_update/${jid}`}>follow output</Link>
            </>
          ) : null}
        </span>,
      )
      invalidate(path, 'inventories')
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Update failed', 'error')
    }
  }
  const syncAll = async () => {
    try {
      await api.post(`inventories/${inv.id}/update_inventory_sources/`)
      toast('Updating every source of this inventory')
      invalidate(path, 'inventories')
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Update failed', 'error')
    }
  }
  const remove = async (s: Source) => {
    const ok = await confirm({
      title: `Delete source ${s.name}?`,
      body: <p style={{ margin: 0 }}>Hosts and groups it imported stay in the inventory until you delete them.</p>,
      confirm: 'Delete source',
      danger: true,
    })
    if (!ok) return
    try {
      await api.del(`inventory_sources/${s.id}/`)
      invalidate(path, 'inventories')
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Delete failed', 'error')
    }
  }
  const cols: Column<Source>[] = [
    {
      key: 'name',
      header: 'Source',
      render: (s) => (
        <div style={{ outline: s.id === highlight ? '2px solid var(--accent-ring)' : undefined }}>
          <button className="link-btn cell-title" onClick={() => setEdit({ source: s })}>
            {s.name}
          </button>
          <div className="cell-sub">
            {s.source === 'scm' ? (
              <>
                {s.summary_fields.source_project?.name ?? 'project'} · {s.source_path || '(project root)'}
              </>
            ) : (
              sourceInfo[s.source]?.label ?? s.source
            )}
          </div>
        </div>
      ),
    },
    { key: 'runs', header: 'Recent updates', render: (s) => <RunBars jobs={s.summary_fields.recent_jobs} slots={10} /> },
    {
      key: 'status',
      header: 'Last update',
      render: (s) => (
        <div className="small">
          <Status status={s.status} />
          {s.last_updated && (
            <div className="faint" style={{ marginTop: 2 }}>
              <Ago at={s.last_updated} />
            </div>
          )}
        </div>
      ),
    },
    {
      key: 'opts',
      header: 'Behaviour',
      render: (s) => (
        <span className="small muted">
          {[s.update_on_launch && 'update on launch', s.overwrite && 'overwrite', s.overwrite_vars && 'overwrite vars'].filter(Boolean).join(' · ') || '—'}
        </span>
      ),
    },
    {
      key: 'act',
      header: <span className="sr-only">Actions</span>,
      className: 'end',
      render: (s) => (
        <div className="row" style={{ justifyContent: 'flex-end', gap: 4 }}>
          {s.summary_fields.user_capabilities?.start !== false && (
            <button className="btn sm" onClick={() => sync(s)} disabled={s.status === 'running' || s.status === 'pending'}>
              <Icon name="refresh" size={14} /> Sync
            </button>
          )}
          <Menu label={`Actions for ${s.name}`} className="btn sm icon ghost">
            <button onClick={() => setEdit({ source: s })}>Edit…</button>
            <button onClick={() => setNotify(s.id)}>Notifications…</button>
            <Link to={`/jobs?q=type:inventory_update ${encodeURIComponent(s.name)}`}>Update history</Link>
            {editable && (
              <>
                <hr />
                <button className="danger" onClick={() => remove(s)}>
                  Delete…
                </button>
              </>
            )}
          </Menu>
        </div>
      ),
    },
  ]
  return (
    <div className="page-body">
      <div className="row wrap" style={{ gap: 8 }}>
        <p className="muted grow" style={{ margin: 0 }}>
          Sources import hosts and groups from a project or a cloud API. A sync runs <span className="mono">ansible-inventory</span> in an execution environment.
        </p>
        {!!sources.data?.count && (
          <button className="btn" onClick={syncAll}>
            <Icon name="refresh" size={14} /> Sync all
          </button>
        )}
        {editable && (
          <button className="btn primary" onClick={() => setEdit({})}>
            New source
          </button>
        )}
      </div>
      <DataTable
        label="Inventory sources"
        rows={sources.data?.results}
        columns={cols}
        loading={sources.isPending}
        error={sources.error}
        onOpen={(s) => setEdit({ source: s })}
        keys={{ s: (s) => sync(s) }}
        empty={
          <Empty title="No sources" action={editable ? <button className="btn primary" onClick={() => setEdit({})}>New source</button> : undefined}>
            Without a source, hosts are added by hand or through the API.
          </Empty>
        }
      />
      {notify && (
        <NotificationsDialog
          title={`Notifications for ${sources.data?.results.find((s) => s.id === notify)?.name ?? 'source'}`}
          resource="inventory_sources"
          id={notify}
          editable={editable}
          onClose={() => {
            setNotify(null)
            if (sp.get('notifications')) setSp({}, { replace: true })
          }}
        />
      )}
      {edit && (
        <SourceDialog
          inv={inv}
          source={edit.source}
          onClose={() => {
            setEdit(null)
            if (sp.get('source')) setSp({}, { replace: true })
          }}
        />
      )}
    </div>
  )
}

function SourceDialog({ inv, source, onClose }: { inv: Inventory; source?: Source; onClose: () => void }) {
  const options = useOptions('inventory_sources/')
  const choices = (options.data?.actions?.POST?.source?.choices ?? Object.keys(sourceInfo).map((k) => [k, sourceInfo[k].label])) as [string, string][]
  const f = useForm({
    name: source?.name ?? '',
    description: source?.description ?? '',
    source: source?.source ?? 'scm',
    source_project: refOf(source?.summary_fields.source_project, source?.source_project),
    source_path: source?.source_path ?? '',
    scm_branch: source?.scm_branch ?? '',
    credential: refOf(source?.summary_fields.credential, source?.credential),
    execution_environment: refOf(source?.summary_fields.execution_environment, source?.execution_environment),
    source_vars: source?.source_vars ?? '',
    update_on_launch: source?.update_on_launch ?? false,
    update_cache_timeout: source?.update_cache_timeout ?? 0,
    overwrite: source?.overwrite ?? false,
    overwrite_vars: source?.overwrite_vars ?? false,
    enabled_var: source?.enabled_var ?? '',
    enabled_value: source?.enabled_value ?? '',
    host_filter: source?.host_filter ?? '',
    limit: source?.limit ?? '',
    verbosity: source?.verbosity ?? 1,
    timeout: source?.timeout ?? 0,
  })
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [advanced, setAdvanced] = useState(false)
  const invalidate = useInvalidate()
  const toast = useToast()
  const v = f.values
  const info = sourceInfo[v.source] ?? { label: v.source }
  const readOnly = !!source?.managed_by || !inv.summary_fields.user_capabilities?.edit
  const submit = async (e?: FormEvent) => {
    e?.preventDefault()
    setBusy(true)
    setError(null)
    const body: Record<string, unknown> = {
      name: v.name,
      description: v.description,
      source: v.source,
      source_project: v.source === 'scm' ? v.source_project?.id ?? null : null,
      source_path: v.source === 'scm' ? v.source_path : '',
      scm_branch: v.source === 'scm' ? v.scm_branch : '',
      credential: v.credential?.id ?? null,
      execution_environment: v.execution_environment?.id ?? null,
      source_vars: v.source_vars,
      update_on_launch: v.update_on_launch,
      update_cache_timeout: Number(v.update_cache_timeout) || 0,
      overwrite: v.overwrite,
      overwrite_vars: v.overwrite_vars,
      enabled_var: v.enabled_var,
      enabled_value: v.enabled_value,
      host_filter: v.host_filter,
      limit: v.limit,
      verbosity: Number(v.verbosity),
      timeout: Number(v.timeout) || 0,
    }
    try {
      if (source) await api.patch(`inventory_sources/${source.id}/`, body)
      else await api.post(`inventories/${inv.id}/inventory_sources/`, body)
      invalidate(`inventories/${inv.id}`, 'inventory_sources', 'inventories')
      toast(source ? `Saved ${v.name}` : `Created ${v.name}`)
      onClose()
    } catch (err) {
      setError(applyErrors(err, f.setErrors))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={source ? source.name : 'New inventory source'}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {readOnly ? 'Close' : 'Cancel'}
          </button>
          {!readOnly && (
            <button className="btn primary" disabled={busy || !v.name || (v.source === 'scm' && !v.source_project) || !!varsError(v.source_vars)} onClick={() => submit()}>
              {busy ? 'Saving…' : source ? 'Save' : 'Create source'}
            </button>
          )}
        </>
      }
    >
      <form className="form" onSubmit={submit}>
        <fieldset disabled={readOnly} className="form">
          <div className="form-grid">
            <TextInput label="Name" value={v.name} onChange={(x) => f.set('name', x)} error={f.errors.name} autoFocus />
            <SelectField
              label="Source"
              value={v.source}
              onChange={(x) => f.set('source', x)}
              options={choices.filter(([k]) => k !== 'constructed').map(([k, l]) => [k, sourceInfo[k]?.label ?? l] as [string, string])}
              error={f.errors.source}
              help={info.hint}
            />
            <TextInput label="Description" optional value={v.description} onChange={(x) => f.set('description', x)} full />
          </div>
          {v.source === 'scm' ? (
            <div className="form-grid">
              <Lookup
                label="Project"
                resource="projects/"
                params={{ organization: inv.organization }}
                value={v.source_project}
                onChange={(x) => f.set('source_project', x)}
                error={f.errors.source_project}
              />
              <TextInput
                label="Inventory file"
                optional
                mono
                value={v.source_path}
                onChange={(x) => f.set('source_path', x)}
                error={f.errors.source_path}
                placeholder="inventories/prod/hosts.yml"
                help="Path within the project; a directory or plugin config also works."
              />
              <TextInput label="Branch" optional mono value={v.scm_branch} onChange={(x) => f.set('scm_branch', x)} help="Only if the project allows branch override." />
            </div>
          ) : (
            <div className="form-grid">
              <Lookup
                label="Credential"
                optional={v.source === 'ec2'}
                resource="credentials/"
                params={info.credential ? { credential_type__name: info.credential } : info.credentialKind ? { credential_type__kind: info.credentialKind } : undefined}
                value={v.credential}
                onChange={(x) => f.set('credential', x)}
                error={f.errors.credential}
                help={info.credential ? `A ${info.credential} credential.` : undefined}
              />
            </div>
          )}
          <VarsEditor
            label={v.source === 'scm' ? 'Source variables' : 'Plugin options'}
            value={v.source_vars}
            onChange={(x) => f.set('source_vars', x)}
            error={f.errors.source_vars}
            help={v.source === 'scm' ? 'Override variables passed to the inventory plugin.' : <>Options of the inventory plugin, e.g. <code>regions: [eu-west-1]</code> or <code>keyed_groups</code>.</>}
          />
          <div className="row wrap" style={{ gap: 20 }}>
            <Check label="Overwrite hosts and groups" checked={v.overwrite} onChange={(x) => f.set('overwrite', x)} help="Remove what the source no longer reports." />
            <Check label="Overwrite variables" checked={v.overwrite_vars} onChange={(x) => f.set('overwrite_vars', x)} help="Replace variables instead of merging." />
            <Check label="Update on launch" checked={v.update_on_launch} onChange={(x) => f.set('update_on_launch', x)} help="Sync before each job using this inventory." />
          </div>
          {v.update_on_launch && (
            <div className="form-grid">
              <TextInput
                label="Cache timeout (seconds)"
                type="number"
                value={v.update_cache_timeout}
                onChange={(x) => f.set('update_cache_timeout', Number(x))}
                help="Skip the sync when the last one is newer than this."
              />
            </div>
          )}
          <button type="button" className="link-btn small" style={{ alignSelf: 'flex-start' }} onClick={() => setAdvanced((a) => !a)}>
            {advanced ? 'Hide' : 'Show'} host filtering, enablement and runtime options
          </button>
          {advanced && (
            <div className="form-grid">
              <TextInput label="Host filter" optional mono value={v.host_filter} onChange={(x) => f.set('host_filter', x)} error={f.errors.host_filter} help="Regular expression; only matching host names are imported." />
              <TextInput label="Limit" optional mono value={v.limit} onChange={(x) => f.set('limit', x)} help="Host pattern applied by ansible-inventory." />
              <TextInput label="Enabled variable" optional mono value={v.enabled_var} onChange={(x) => f.set('enabled_var', x)} help="Host variable deciding whether a host is enabled, e.g. status.power_state." />
              <TextInput label="Enabled value" optional mono value={v.enabled_value} onChange={(x) => f.set('enabled_value', x)} help="Value of that variable meaning enabled." />
              <Lookup label="Execution environment" optional resource="execution_environments/" value={v.execution_environment} onChange={(x) => f.set('execution_environment', x)} />
              <SelectField label="Verbosity" value={v.verbosity} onChange={(x) => f.set('verbosity', Number(x))} options={[[0, '0 (warning)'], [1, '1 (info)'], [2, '2 (debug)']]} />
              <TextInput label="Timeout (seconds)" type="number" value={v.timeout} onChange={(x) => f.set('timeout', Number(x))} help="0 means no timeout." />
            </div>
          )}
        </fieldset>
        <FormError error={error} />
        <button type="submit" hidden />
      </form>
    </Dialog>
  )
}

export type { Ref }
