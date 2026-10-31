import { useState, type FormEvent } from 'react'
import { Link, useParams, useSearchParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useObject, useOptions } from '../../api/hooks'
import { useLive } from '../../api/live'
import type { Base, Ref } from '../../api/types'
import { Check, FormError, Lookup, SelectField, TextInput, VarsEditor, useForm, varsError } from '../../components/form'
import { Icon } from '../../components/icons'
import { NotificationsDialog } from '../../components/NotificationsTab'
import { Dialog, Menu, useConfirm, useToast } from '../../components/overlay'
import { DataTable, type Column } from '../../components/table'
import { Ago, Empty, ErrorNote, Loading, RunBars, Status } from '../../components/ui'
import { SchedulesTab } from '../automation/SchedulesTab'
import { applyErrors, refOf } from './common'
import { useSubList, type Inventory } from './inventories'
import { enabledDefault, enabledHelp, isImportedFile, sourceChoices } from '../../lib/sourceDefaults'
import { asIs, tr } from '../../lib/i18n'

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
  file: { label: tr('Imported file'), hint: tr('Recorded by the awxnext inventory-import command; it cannot be synced from here.') },
  scm: { label: tr('Sourced from a project'), hint: tr('An inventory file or plugin config in a project repository.') },
  ec2: { label: tr('Amazon EC2'), credential: tr('Amazon Web Services'), credentialKind: 'cloud' },
  gce: { label: tr('Google Compute Engine'), credential: tr('Google Compute Engine'), credentialKind: 'cloud' },
  azure_rm: { label: tr('Microsoft Azure Resource Manager'), credential: tr('Microsoft Azure Resource Manager'), credentialKind: 'cloud' },
  vmware: { label: tr('VMware vCenter'), credential: 'VMware vCenter', credentialKind: 'cloud' },
  satellite6: { label: tr('Red Hat Satellite 6'), credential: tr('Red Hat Satellite 6'), credentialKind: 'cloud' },
  openstack: { label: tr('OpenStack'), credential: asIs('OpenStack'), credentialKind: 'cloud' },
  rhv: { label: tr('Red Hat Virtualization'), credential: tr('Red Hat Virtualization'), credentialKind: 'cloud' },
  controller: { label: tr('Red Hat Ansible Automation Platform'), credential: tr('Red Hat Ansible Automation Platform'), credentialKind: 'cloud' },
  insights: { label: tr('Red Hat Insights'), credential: asIs('Insights'), credentialKind: 'insights' },
  terraform: { label: tr('Terraform state'), credential: tr('Terraform backend configuration'), credentialKind: 'cloud' },
  constructed: { label: tr('Constructed') },
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
          {tr('Updating')} {s.name}
          {jid ? (
            <>
              {' · '}
              <Link to={`/jobs/inventory_update/${jid}`}>{tr('follow output')}</Link>
            </>
          ) : null}
        </span>,
      )
      invalidate(path, 'inventories')
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Update failed'), 'error')
    }
  }
  const syncAll = async () => {
    try {
      await api.post(`inventories/${inv.id}/update_inventory_sources/`)
      toast(tr('Updating every source of this inventory'))
      invalidate(path, 'inventories')
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Update failed'), 'error')
    }
  }
  const remove = async (s: Source) => {
    const ok = await confirm({
      title: tr('Delete source {name}?', { name: s.name }),
      body: <p style={{ margin: 0 }}>{tr('Hosts and groups it imported stay in the inventory until you delete them.')}</p>,
      confirm: tr('Delete source'),
      danger: true,
    })
    if (!ok) return
    try {
      await api.del(`inventory_sources/${s.id}/`)
      invalidate(path, 'inventories')
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Delete failed'), 'error')
    }
  }
  const cols: Column<Source>[] = [
    {
      key: 'name',
      header: tr('Source'),
      render: (s) => (
        <div style={{ outline: s.id === highlight ? '2px solid var(--accent-ring)' : undefined }}>
          <button className="link-btn cell-title" onClick={() => setEdit({ source: s })}>
            {s.name}
          </button>
          <div className="cell-sub">
            {s.source === 'scm' ? (
              <>
                {s.summary_fields.source_project?.name ?? tr('project')} · {s.source_path || tr('(project root)')}
              </>
            ) : (
              sourceInfo[s.source]?.label ?? s.source
            )}
          </div>
        </div>
      ),
    },
    { key: 'runs', header: tr('Recent updates'), render: (s) => <RunBars jobs={s.summary_fields.recent_jobs} slots={10} /> },
    {
      key: 'status',
      header: tr('Last update'),
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
      header: tr('Behaviour'),
      render: (s) => (
        <span className="small muted">
          {[s.update_on_launch && 'update on launch', s.overwrite && 'overwrite', s.overwrite_vars && 'overwrite vars'].filter(Boolean).join(' · ') || '—'}
        </span>
      ),
    },
    {
      key: 'act',
      header: <span className="sr-only">{tr('Actions')}</span>,
      className: 'end',
      render: (s) => (
        <div className="row" style={{ justifyContent: 'flex-end', gap: 4 }}>
          {s.summary_fields.user_capabilities?.start !== false && !isImportedFile(s.source) && (
            <button className="btn sm" onClick={() => sync(s)} disabled={s.status === 'running' || s.status === 'pending'}>
              <Icon name="refresh" size={14} /> {tr('Sync')}
            </button>
          )}
          <Menu label={tr('Actions for {name}', { name: s.name })} className="btn sm icon ghost">
            <button onClick={() => setEdit({ source: s })}>{tr('Edit…')}</button>
            <button onClick={() => setNotify(s.id)}>{tr('Notifications…')}</button>
            {!isImportedFile(s.source) && <Link to={`/inventories/${inv.id}/sources/${s.id}/schedules`}>{tr('Schedules')}</Link>}
            <Link to={`/jobs?q=type:inventory_update ${encodeURIComponent(s.name)}`}>{tr('Update history')}</Link>
            {editable && (
              <>
                <hr />
                <button className="danger" onClick={() => remove(s)}>
                  {tr('Delete…')}
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
          {tr('Sources import hosts and groups from a project or a cloud API. A sync runs')} <span className="mono">{tr('ansible-inventory')}</span> {tr('in an execution environment.')}
        </p>
        {!!sources.data?.results.some((s) => !isImportedFile(s.source)) && (
          <button className="btn" onClick={syncAll}>
            <Icon name="refresh" size={14} /> {tr('Sync all')}
          </button>
        )}
        {editable && (
          <button className="btn primary" onClick={() => setEdit({})}>
            {tr('New source')}
          </button>
        )}
      </div>
      <DataTable
        label={tr('Inventory sources')}
        rows={sources.data?.results}
        columns={cols}
        loading={sources.isPending}
        error={sources.error}
        onOpen={(s) => setEdit({ source: s })}
        keys={{ s: (s) => sync(s) }}
        empty={
          <Empty title={tr('No sources')} action={editable ? <button className="btn primary" onClick={() => setEdit({})}>{tr('New source')}</button> : undefined}>
            {tr('Without a source, hosts are added by hand or through the API.')}
          </Empty>
        }
      />
      {notify && (
        <NotificationsDialog
          title={tr('Notifications for {name}', { name: sources.data?.results.find((s) => s.id === notify)?.name ?? tr('source') })}
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

/** One source's schedules (inventories/:id/sources/:sourceId/schedules):
 * syncs at set times, created, edited and switched off as a template's. */
export function InventorySourceSchedulesTab({ inv }: { inv: Inventory }) {
  const { sourceId } = useParams()
  const src = useObject<Source>(`inventory_sources/${sourceId}/`)
  if (src.isPending) return <Loading />
  if (src.error || !src.data || src.data.inventory !== inv.id) return <div className="page-body"><ErrorNote error={src.error ?? tr('Not a source of this inventory')} /></div>
  return (
    <div className="page-body stack" style={{ gap: 12 }}>
      <div className="row" style={{ gap: 8 }}>
        <Link className="btn ghost sm" to={`/inventories/${inv.id}/sources`}>
          <Icon name="arrowLeft" size={14} /> {tr('All sources')}
        </Link>
        <h2 style={{ margin: 0 }}>{tr('Schedules of')} {src.data.name}</h2>
      </div>
      <SchedulesTab template={{ id: src.data.id, type: 'inventory_source', name: src.data.name }} />
    </div>
  )
}

function SourceDialog({ inv, source, onClose }: { inv: Inventory; source?: Source; onClose: () => void }) {
  const options = useOptions('inventory_sources/')
  const choices = sourceChoices((options.data?.actions?.POST?.source?.choices ?? Object.keys(sourceInfo).map((k) => [k, sourceInfo[k].label])) as [string, string][], source?.source)
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
      toast(source ? tr('Saved {name}', { name: v.name }) : tr('Created {name}', { name: v.name }))
      onClose()
    } catch (err) {
      setError(applyErrors(err, f.setErrors))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={source ? source.name : tr('New inventory source')}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {readOnly ? tr('Close') : tr('Cancel')}
          </button>
          {!readOnly && (
            <button className="btn primary" disabled={busy || !v.name || (v.source === 'scm' && !v.source_project) || !!varsError(v.source_vars)} onClick={() => submit()}>
              {busy ? tr('Saving…') : source ? tr('Save') : tr('Create source')}
            </button>
          )}
        </>
      }
    >
      <form className="form" onSubmit={submit}>
        <fieldset disabled={readOnly} className="form">
          <div className="form-grid">
            <TextInput label={tr('Name')} value={v.name} onChange={(x) => f.set('name', x)} error={f.errors.name} autoFocus />
            <SelectField
              label={tr('Source')}
              value={v.source}
              onChange={(x) => f.set('source', x)}
              options={choices.filter(([k]) => k !== 'constructed').map(([k, l]) => [k, sourceInfo[k]?.label ?? l] as [string, string])}
              error={f.errors.source}
              help={info.hint}
            />
            <TextInput label={tr('Description')} optional value={v.description} onChange={(x) => f.set('description', x)} full />
          </div>
          {v.source === 'scm' ? (
            <div className="form-grid">
              <Lookup
                label={tr('Project')}
                resource="projects/"
                params={{ organization: inv.organization }}
                value={v.source_project}
                onChange={(x) => f.set('source_project', x)}
                error={f.errors.source_project}
              />
              <TextInput
                label={tr('Inventory file')}
                optional
                mono
                value={v.source_path}
                onChange={(x) => f.set('source_path', x)}
                error={f.errors.source_path}
                placeholder={tr('inventories/prod/hosts.yml')}
                help={tr('Path within the project; a directory or plugin config also works.')}
              />
              <TextInput label={tr('Branch')} optional mono value={v.scm_branch} onChange={(x) => f.set('scm_branch', x)} help={tr('Only if the project allows branch override.')} />
            </div>
          ) : (
            <div className="form-grid">
              <Lookup
                label={tr('Credential')}
                optional={v.source === 'ec2'}
                resource="credentials/"
                params={info.credential ? { credential_type__name: info.credential } : info.credentialKind ? { credential_type__kind: info.credentialKind } : undefined}
                value={v.credential}
                onChange={(x) => f.set('credential', x)}
                error={f.errors.credential}
                help={info.credential ? tr('A {credential} credential.', { credential: info.credential }) : undefined}
              />
            </div>
          )}
          <VarsEditor
            label={v.source === 'scm' ? tr('Source variables') : tr('Plugin options')}
            value={v.source_vars}
            onChange={(x) => f.set('source_vars', x)}
            error={f.errors.source_vars}
            help={
              v.source === 'scm' ? (
                <>
                  {tr('Set as environment variables for the inventory script or plugin, e.g.')} <code>REGION: eu-west-1</code>.
                </>
              ) : (
                <>
                  {tr('Options of the inventory plugin, e.g.')} <code>regions: [eu-west-1]</code> {tr('or')} <code>keyed_groups</code>.
                </>
              )
            }
          />
          <div className="row wrap" style={{ gap: 20 }}>
            <Check label={tr('Overwrite hosts and groups')} checked={v.overwrite} onChange={(x) => f.set('overwrite', x)} help={tr('Remove what the source no longer reports.')} />
            <Check label={tr('Overwrite variables')} checked={v.overwrite_vars} onChange={(x) => f.set('overwrite_vars', x)} help={tr('Replace variables instead of merging.')} />
            <Check label={tr('Update on launch')} checked={v.update_on_launch} onChange={(x) => f.set('update_on_launch', x)} help={tr('Sync before each job using this inventory.')} />
          </div>
          {v.update_on_launch && (
            <div className="form-grid">
              <TextInput
                label={tr('Cache timeout (seconds)')}
                type="number"
                value={v.update_cache_timeout}
                onChange={(x) => f.set('update_cache_timeout', Number(x))}
                help={tr('Skip the sync when the last one is newer than this.')}
              />
            </div>
          )}
          <button type="button" className="link-btn small" style={{ alignSelf: 'flex-start' }} onClick={() => setAdvanced((a) => !a)}>
            {advanced ? tr('Hide') : tr('Show')} {tr('host filtering, enablement and runtime options')}
          </button>
          {advanced && (
            <div className="form-grid">
              <TextInput label={tr('Host filter')} optional mono value={v.host_filter} onChange={(x) => f.set('host_filter', x)} error={f.errors.host_filter} help={tr('Regular expression; only matching host names are imported.')} />
              <TextInput label={tr('Limit')} optional mono value={v.limit} onChange={(x) => f.set('limit', x)} help={tr('Host pattern applied by ansible-inventory.')} />
              <TextInput label={tr('Enabled variable')} optional mono value={v.enabled_var} onChange={(x) => f.set('enabled_var', x)} placeholder={enabledDefault(v.source)?.variable} help={enabledHelp(v.source)} />
              <TextInput label={tr('Enabled value')} optional mono value={v.enabled_value} onChange={(x) => f.set('enabled_value', x)} placeholder={v.enabled_var ? undefined : enabledDefault(v.source)?.value} help={tr('Value of that variable meaning enabled.')} />
              <Lookup label={tr('Execution environment')} optional resource="execution_environments/" value={v.execution_environment} onChange={(x) => f.set('execution_environment', x)} />
              <SelectField label={tr('Verbosity')} value={v.verbosity} onChange={(x) => f.set('verbosity', Number(x))} options={[[0, '0 (warning)'], [1, '1 (info)'], [2, '2 (debug)']]} />
              <TextInput label={tr('Timeout (seconds)')} type="number" value={v.timeout} onChange={(x) => f.set('timeout', Number(x))} help={tr('0 means no timeout.')} />
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
