import { useQuery } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useInvalidate } from '../../api/hooks'
import type { Base, Ref } from '../../api/types'
import { Check, FormError, Lookup, SelectField, TextInput, VarsEditor, parseVars, useForm, varsError } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, Menu, useConfirm, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Empty, StatusDot } from '../../components/ui'
import { plural } from '../../lib/format'
import { askedPasswords, passwordLabel } from '../../lib/jobs'
import { compile, type KeyTable } from '../../lib/query'
import { applyErrors, FilterBox } from './common'
import { useSubList, type Inventory } from './inventories'
import { tr } from '../../lib/i18n'

export interface Host extends Base {
  inventory: number
  enabled: boolean
  variables: string
  instance_id: string
  has_active_failures: boolean
  last_job: number | null
}

const hostKeys: KeyTable = {
  group: { field: 'groups__name', lookup: 'iexact' },
  enabled: { field: 'enabled', map: (v) => (/^(y|yes|true|on|1)$/i.test(v) ? 'true' : 'false') },
  name: { field: 'name', lookup: 'icontains' },
  failing: { field: 'last_job_host_summary__failed', map: (v) => (/^(n|no|false|0)$/i.test(v) ? 'false' : 'true') },
}

export function InventoryHostsTab({ inv }: { inv: Inventory }) {
  const ls = useListState({ order: 'name', pageSize: 50 })
  const q = compile(ls.q, hostKeys)
  const path = `inventories/${inv.id}/hosts/`
  const hosts = useSubList<Host>(path, { ...q.params, order_by: ls.order, page: ls.page, page_size: ls.pageSize })
  const [selected, setSelected] = useState<Set<number | string>>(new Set())
  const [dialog, setDialog] = useState<null | { kind: 'host'; host?: Host } | { kind: 'adhoc'; limit: string }>(null)
  const invalidate = useInvalidate()
  const toast = useToast()
  const confirm = useConfirm()
  const nav = useNavigate()
  const editable = inv.kind === '' && !inv.managed_by && !!inv.summary_fields.user_capabilities?.edit
  const rows = hosts.data?.results ?? []
  const selectedHosts = rows.filter((h) => selected.has(h.id))
  const refresh = () => invalidate(path, 'inventories', 'hosts')

  const setEnabled = async (list: Host[], enabled: boolean) => {
    try {
      await Promise.all(list.map((h) => api.patch(`hosts/${h.id}/`, { enabled })))
      refresh()
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Update failed'), 'error')
    }
  }
  const remove = async (list: Host[]) => {
    const ok = await confirm({
      title: list.length === 1 ? tr('Delete {name}?', { name: list[0].name }) : tr('Delete {count} hosts?', { count: list.length }),
      body: <p style={{ margin: 0 }}>{tr('Deleted hosts lose their facts and job history links. A source update may import them again.')}</p>,
      confirm: tr('Delete'),
      danger: true,
    })
    if (!ok) return
    try {
      await Promise.all(list.map((h) => api.del(`hosts/${h.id}/`)))
      setSelected(new Set())
      toast(list.length === 1 ? tr('Deleted {name}', { name: list[0].name }) : tr('Deleted {count} hosts', { count: list.length }))
      refresh()
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Delete failed'), 'error')
    }
  }

  const cols: Column<Host>[] = [
    {
      key: 'name',
      header: tr('Host'),
      sort: 'name',
      render: (h) => (
        <div className="row" style={{ gap: 6 }}>
          <Link to={`/hosts/${h.id}`} className="cell-title mono" style={{ fontSize: 13, opacity: h.enabled ? 1 : 0.55 }}>
            {h.name}
          </Link>
          {!h.enabled && <span className="tag">{tr('disabled')}</span>}
          {h.description && <span className="faint small ellipsis">{h.description}</span>}
        </div>
      ),
    },
    {
      key: 'groups',
      header: tr('Groups'),
      render: (h) => {
        const g = (h.summary_fields.groups?.results ?? []) as Ref[]
        if (!g.length) return <span className="faint small">{tr('ungrouped')}</span>
        return (
          <span className="row wrap" style={{ gap: 4 }}>
            {g.slice(0, 3).map((x) => (
              <button key={x.id} className="chip" style={{ cursor: 'pointer' }} onClick={() => ls.setQ(`group:${x.name}`)} title={tr('Show hosts in {name}', { name: x.name })}>
                {x.name}
              </button>
            ))}
            {g.length > 3 && <span className="faint small">+{g.length - 3}</span>}
          </span>
        )
      },
    },
    {
      key: 'last',
      header: tr('Last job'),
      render: (h) => {
        const j = (h.summary_fields.recent_jobs ?? [])[0] as (Base & { status: string; finished: string }) | undefined
        if (!j) return <span className="faint small">{tr('never targeted')}</span>
        return (
          <Link to={`/jobs/${j.type}/${j.id}`} className="row small" style={{ gap: 6, color: 'var(--ink-2)' }} title={j.name}>
            <StatusDot status={j.status} />
            <span className="ellipsis" style={{ maxWidth: 220 }}>
              {j.name}
            </span>
            <span className="faint nowrap">
              <Ago at={j.finished} />
            </span>
          </Link>
        )
      },
    },
    {
      key: 'enabled',
      header: tr('Enabled'),
      render: (h) => (
        <label className="check small" title={editable ? tr('Disabled hosts are skipped by jobs') : undefined}>
          <input type="checkbox" checked={h.enabled} disabled={!editable} onChange={(e) => setEnabled([h], e.target.checked)} aria-label={tr('Enable {name}', { name: h.name })} />
        </label>
      ),
    },
    {
      key: 'menu',
      header: <span className="sr-only">{tr('Actions')}</span>,
      className: 'end',
      render: (h) => (
        <Menu label={tr('Actions for {name}', { name: h.name })} className="btn sm icon ghost">
          <button onClick={() => setDialog({ kind: 'adhoc', limit: h.name })}>{tr('Run command…')}</button>
          {editable && <button onClick={() => setDialog({ kind: 'host', host: h })}>{tr('Edit…')}</button>}
          <Link to={`/hosts/${h.id}`}>{tr('Facts and history')}</Link>
          {editable && (
            <>
              <hr />
              <button className="danger" onClick={() => remove([h])}>
                {tr('Delete…')}
              </button>
            </>
          )}
        </Menu>
      ),
    },
  ]

  const readOnlyNote =
    inv.kind === 'smart' ? (
      <span className="small faint">
        {tr('Hosts matching')} <code>{inv.host_filter}</code>{tr('. Edit them in their own inventory.')}
      </span>
    ) : inv.kind === 'constructed' ? (
      <span className="small faint">{tr('Hosts come from the input inventories when the inventory is updated.')}</span>
    ) : null

  return (
    <div className="page-body">
      <div className="row wrap" style={{ gap: 8 }}>
        <div className="grow">
          <FilterBox value={ls.q} onApply={ls.setQ} placeholder={tr('name…  try  group:web  enabled:no  failing:yes')} />
        </div>
        <button className="btn" onClick={() => setDialog({ kind: 'adhoc', limit: ls.q && q.params['groups__name__iexact'] ? q.params['groups__name__iexact'] : 'all' })}>
          <Icon name="terminal" size={14} /> {tr('Run command…')}
        </button>
        {editable && (
          <button className="btn primary" onClick={() => setDialog({ kind: 'host' })}>
            {tr('Add host')}
          </button>
        )}
      </div>
      {readOnlyNote}
      {selected.size > 0 && (
        <div className="bulkbar" role="region" aria-label={tr('Bulk actions')}>
          <span className="n">{selected.size} {tr('selected')}</span>
          <button className="btn" onClick={() => setDialog({ kind: 'adhoc', limit: selectedHosts.map((h) => h.name).join(',') })}>
            {tr('Run command…')}
          </button>
          {editable && (
            <>
              <button className="btn" onClick={() => setEnabled(selectedHosts, true)}>
                {tr('Enable')}
              </button>
              <button className="btn" onClick={() => setEnabled(selectedHosts, false)}>
                {tr('Disable')}
              </button>
              <button className="btn warn" onClick={() => remove(selectedHosts)}>
                {tr('Delete')}
              </button>
            </>
          )}
          <button className="btn clear" onClick={() => setSelected(new Set())}>
            {tr('Clear')}
          </button>
        </div>
      )}
      <DataTable
        label={tr('Hosts')}
        rows={hosts.data?.results}
        columns={cols}
        loading={hosts.isPending}
        error={hosts.error}
        order={ls.order}
        onOrder={ls.setOrder}
        selected={selected}
        onSelect={setSelected}
        onOpen={(h) => nav(`/hosts/${h.id}`)}
        keys={{ r: (h) => setDialog({ kind: 'adhoc', limit: h.name }) }}
        empty={
          <Empty
            title={ls.q ? tr('No host matches') : tr('No hosts yet')}
            action={editable && !ls.q ? <button className="btn primary" onClick={() => setDialog({ kind: 'host' })}>{tr('Add host')}</button> : undefined}
          >
            {ls.q ? tr('Try a shorter filter.') : inv.kind === '' ? tr('Add hosts here, or add a source that imports them.') : null}
          </Empty>
        }
        footer={
          <div className="table-foot">
            <span>{hosts.data ? tr('{count}', { count: plural(hosts.data.count, 'host', 'hosts') }) : ''}</span>
            <span className="mono">{tr('j/k move · x select · r run command · enter open')}</span>
            <Pager count={hosts.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
          </div>
        }
      />
      {dialog?.kind === 'host' && <HostDialog inventory={inv.id} host={dialog.host} onClose={() => setDialog(null)} onSaved={refresh} />}
      {dialog?.kind === 'adhoc' && <AdHocDialog inventory={inv} limit={dialog.limit} onClose={() => setDialog(null)} />}
    </div>
  )
}

export function HostDialog({ inventory, host, onClose, onSaved }: { inventory: number; host?: Host; onClose: () => void; onSaved: () => void }) {
  const f = useForm({ name: host?.name ?? '', description: host?.description ?? '', variables: host?.variables ?? '', enabled: host?.enabled ?? true })
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const toast = useToast()
  const submit = async (e?: FormEvent) => {
    e?.preventDefault()
    setBusy(true)
    setError(null)
    try {
      if (host) await api.patch(`hosts/${host.id}/`, f.values)
      else await api.post(`inventories/${inventory}/hosts/`, f.values)
      toast(host ? tr('Saved {name}', { name: f.values.name }) : tr('Added {name}', { name: f.values.name }))
      onSaved()
      onClose()
    } catch (err) {
      setError(applyErrors(err, f.setErrors))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={host ? tr('Edit {name}', { name: host.name }) : tr('Add host')}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" disabled={busy || !f.values.name || !!varsError(f.values.variables)} onClick={() => submit()}>
            {busy ? tr('Saving…') : host ? tr('Save') : tr('Add host')}
          </button>
        </>
      }
    >
      <form className="form" onSubmit={submit}>
        <div className="form-grid">
          <TextInput label={tr('Name')} mono value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} help={tr('Hostname or IP address, or a name with ansible_host set below.')} autoFocus />
          <TextInput label={tr('Description')} optional value={f.values.description} onChange={(v) => f.set('description', v)} />
        </div>
        <VarsEditor label={tr('Host variables')} value={f.values.variables} onChange={(v) => f.set('variables', v)} error={f.errors.variables} help={tr('e.g. ansible_host: 10.0.4.12')} />
        <Check label={tr('Enabled')} checked={f.values.enabled} onChange={(v) => f.set('enabled', v)} help={tr('Disabled hosts stay in the inventory but jobs skip them.')} />
        <FormError error={error} />
        <button type="submit" hidden />
      </form>
    </Dialog>
  )
}

const defaultModules = ['command', 'shell', 'ping', 'setup', 'service', 'yum', 'apt', 'user', 'group', 'mount']

/** Ad hoc command launcher for an inventory and a host pattern. */
export function AdHocDialog({ inventory, limit, onClose }: { inventory: Pick<Inventory, 'id' | 'name'>; limit: string; onClose: () => void }) {
  const modules = useQuery({
    queryKey: ['settings/jobs/', 'ad-hoc-modules'],
    queryFn: () => api.get<{ AD_HOC_COMMANDS?: string[] }>('settings/jobs/').then((s) => s.AD_HOC_COMMANDS ?? defaultModules),
    retry: false,
    staleTime: 5 * 60_000,
  })
  const list = modules.data ?? defaultModules
  const f = useForm({
    module_name: 'command',
    module_args: '',
    limit,
    credential: null as Ref | null,
    execution_environment: null as Ref | null,
    verbosity: 0,
    forks: 0,
    become_enabled: false,
    check: false,
    diff_mode: false,
    extra_vars: '',
  })
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const v = f.values
  const needsArgs = v.module_name === 'command' || v.module_name === 'shell'
  // The credential's ASK inputs, and any the API reports missing.
  const [reported, setReported] = useState<string[]>([])
  const [passwords, setPasswords] = useState<Record<string, string>>({})
  const asked = [...new Set([...askedPasswords((v.credential as { inputs?: Record<string, unknown> } | null)?.inputs), ...reported])]
  const passwordsMissing = asked.some((p) => !passwords[p])
  const submit = async (e?: FormEvent) => {
    e?.preventDefault()
    setBusy(true)
    setError(null)
    try {
      const job = await api.post<{ id: number }>(`inventories/${inventory.id}/ad_hoc_commands/`, {
        module_name: v.module_name,
        module_args: v.module_args,
        limit: v.limit,
        credential: v.credential?.id ?? null,
        execution_environment: v.execution_environment?.id ?? null,
        credential_passwords: Object.fromEntries(asked.map((p) => [p, passwords[p] ?? ''])),
        verbosity: Number(v.verbosity),
        forks: Number(v.forks) || 0,
        become_enabled: v.become_enabled,
        job_type: v.check ? 'check' : 'run',
        diff_mode: v.diff_mode,
        extra_vars: Object.keys(parseVars(v.extra_vars)).length ? JSON.stringify(parseVars(v.extra_vars)) : '',
      })
      invalidate('ad_hoc_commands', 'unified_jobs')
      onClose()
      nav(`/jobs/ad_hoc_command/${job.id}`)
    } catch (err) {
      const needed = err instanceof ApiError && err.status === 400 ? (err.body as { passwords_needed_to_start?: string[] })?.passwords_needed_to_start : undefined
      if (needed?.length) {
        setReported(needed)
        setError(new Error(tr('Enter {list}.', { list: needed.map(passwordLabel).join(', ') })))
      } else setError(applyErrors(err, f.setErrors))
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={tr('Run a command')}
      onClose={onClose}
      wide
      footer={
        <>
          <span className="small faint grow">
            {tr('Runs on')} <span className="mono">{v.limit || tr('all')}</span> {tr('in')} {inventory.name}
          </span>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" disabled={busy || (needsArgs && !v.module_args.trim()) || !!varsError(v.extra_vars) || passwordsMissing} onClick={() => submit()}>
            <Icon name="play" size={12} /> {busy ? tr('Starting…') : tr('Run')}
          </button>
        </>
      }
    >
      <form className="form" onSubmit={submit}>
        <div className="row" style={{ gap: 12, alignItems: 'flex-start' }}>
          <div style={{ flex: '0 0 200px' }}>
            <SelectField label={tr('Module')} value={v.module_name} onChange={(x) => f.set('module_name', x)} options={list.map((m) => [m, m] as [string, string])} error={f.errors.module_name} />
          </div>
          <div className="grow">
            <TextInput
              label={tr('Arguments')}
              optional={!needsArgs}
              mono
              value={v.module_args}
              onChange={(x) => f.set('module_args', x)}
              error={f.errors.module_args}
              placeholder={v.module_name === 'shell' || v.module_name === 'command' ? tr('uptime') : v.module_name === 'service' ? tr('name=nginx state=restarted') : ''}
              autoFocus
            />
          </div>
        </div>
        <div className="form-grid">
          <TextInput label={tr('Hosts')} mono value={v.limit} onChange={(x) => f.set('limit', x)} error={f.errors.limit} help={tr('Host pattern: names, groups, wildcards (web*), exclusions (!db1).')} />
          <Lookup
            label={tr('Machine credential')}
            optional
            resource="credentials/"
            params={{ credential_type__kind: 'ssh' }}
            value={v.credential}
            onChange={(x) => f.set('credential', x)}
            error={f.errors.credential}
          />
          {asked.map((p) => (
            <TextInput key={p} label={passwordLabel(p)} type="password" value={passwords[p] ?? ''} onChange={(x) => setPasswords((o) => ({ ...o, [p]: x }))} help={tr('The credential asks for it at launch.')} />
          ))}
          <Lookup
            label={tr('Execution environment')}
            optional
            resource="execution_environments/"
            value={v.execution_environment}
            onChange={(x) => f.set('execution_environment', x)}
            error={f.errors.execution_environment}
            placeholder={tr('The inventory\'s organization default')}
            meta={(e) => e.image}
          />
          <SelectField
            label={tr('Verbosity')}
            value={v.verbosity}
            onChange={(x) => f.set('verbosity', Number(x))}
            options={[
              [0, '0 (normal)'],
              [1, '1 (verbose)'],
              [2, '2 (more verbose)'],
              [3, '3 (debug)'],
              [4, '4 (connection debug)'],
            ]}
          />
          <TextInput label={tr('Forks')} type="number" value={v.forks} onChange={(x) => f.set('forks', Number(x))} help={tr('0 uses the default.')} />
        </div>
        <div className="row wrap" style={{ gap: 20 }}>
          <Check label={tr('Privilege escalation (become)')} checked={v.become_enabled} onChange={(x) => f.set('become_enabled', x)} />
          <Check label={tr('Check mode')} checked={v.check} onChange={(x) => f.set('check', x)} />
          <Check label={tr('Show diffs')} checked={v.diff_mode} onChange={(x) => f.set('diff_mode', x)} />
        </div>
        <VarsEditor label={tr('Extra variables')} rows={3} value={v.extra_vars} onChange={(x) => f.set('extra_vars', x)} error={f.errors.extra_vars} />
        <FormError error={error} />
        <button type="submit" hidden />
      </form>
    </Dialog>
  )
}
