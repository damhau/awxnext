import { useQuery } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { Link, useNavigate } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate } from '../../api/hooks'
import type { Base, Ref } from '../../api/types'
import { Check, FormError, Lookup, SelectField, TextInput, VarsEditor, parseVars, useForm, varsError } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, Menu, useConfirm, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Empty, StatusDot } from '../../components/ui'
import { plural } from '../../lib/format'
import { compile, type KeyTable } from '../../lib/query'
import { applyErrors, FilterBox } from './common'
import { useSubList, type Inventory } from './inventories'

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
      toast(e instanceof Error ? e.message : 'Update failed', 'error')
    }
  }
  const remove = async (list: Host[]) => {
    const ok = await confirm({
      title: list.length === 1 ? `Delete ${list[0].name}?` : `Delete ${list.length} hosts?`,
      body: <p style={{ margin: 0 }}>Deleted hosts lose their facts and job history links. A source update may import them again.</p>,
      confirm: 'Delete',
      danger: true,
    })
    if (!ok) return
    try {
      await Promise.all(list.map((h) => api.del(`hosts/${h.id}/`)))
      setSelected(new Set())
      toast(list.length === 1 ? `Deleted ${list[0].name}` : `Deleted ${list.length} hosts`)
      refresh()
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Delete failed', 'error')
    }
  }

  const cols: Column<Host>[] = [
    {
      key: 'name',
      header: 'Host',
      sort: 'name',
      render: (h) => (
        <div className="row" style={{ gap: 6 }}>
          <Link to={`/hosts/${h.id}`} className="cell-title mono" style={{ fontSize: 13, opacity: h.enabled ? 1 : 0.55 }}>
            {h.name}
          </Link>
          {!h.enabled && <span className="tag">disabled</span>}
          {h.description && <span className="faint small ellipsis">{h.description}</span>}
        </div>
      ),
    },
    {
      key: 'groups',
      header: 'Groups',
      render: (h) => {
        const g = (h.summary_fields.groups?.results ?? []) as Ref[]
        if (!g.length) return <span className="faint small">ungrouped</span>
        return (
          <span className="row wrap" style={{ gap: 4 }}>
            {g.slice(0, 3).map((x) => (
              <button key={x.id} className="chip" style={{ cursor: 'pointer' }} onClick={() => ls.setQ(`group:${x.name}`)} title={`Show hosts in ${x.name}`}>
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
      header: 'Last job',
      render: (h) => {
        const j = (h.summary_fields.recent_jobs ?? [])[0] as (Base & { status: string; finished: string }) | undefined
        if (!j) return <span className="faint small">never targeted</span>
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
      header: 'Enabled',
      render: (h) => (
        <label className="check small" title={editable ? 'Disabled hosts are skipped by jobs' : undefined}>
          <input type="checkbox" checked={h.enabled} disabled={!editable} onChange={(e) => setEnabled([h], e.target.checked)} aria-label={`Enable ${h.name}`} />
        </label>
      ),
    },
    {
      key: 'menu',
      header: <span className="sr-only">Actions</span>,
      className: 'end',
      render: (h) => (
        <Menu label={`Actions for ${h.name}`} className="btn sm icon ghost">
          <button onClick={() => setDialog({ kind: 'adhoc', limit: h.name })}>Run command…</button>
          {editable && <button onClick={() => setDialog({ kind: 'host', host: h })}>Edit…</button>}
          <Link to={`/hosts/${h.id}`}>Facts and history</Link>
          {editable && (
            <>
              <hr />
              <button className="danger" onClick={() => remove([h])}>
                Delete…
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
        Hosts matching <code>{inv.host_filter}</code>. Edit them in their own inventory.
      </span>
    ) : inv.kind === 'constructed' ? (
      <span className="small faint">Hosts come from the input inventories when the inventory is updated.</span>
    ) : null

  return (
    <div className="page-body">
      <div className="row wrap" style={{ gap: 8 }}>
        <div className="grow">
          <FilterBox value={ls.q} onApply={ls.setQ} placeholder="name…  try  group:web  enabled:no  failing:yes" />
        </div>
        <button className="btn" onClick={() => setDialog({ kind: 'adhoc', limit: ls.q && q.params['groups__name__iexact'] ? q.params['groups__name__iexact'] : 'all' })}>
          <Icon name="terminal" size={14} /> Run command…
        </button>
        {editable && (
          <button className="btn primary" onClick={() => setDialog({ kind: 'host' })}>
            Add host
          </button>
        )}
      </div>
      {readOnlyNote}
      {selected.size > 0 && (
        <div className="bulkbar" role="region" aria-label="Bulk actions">
          <span className="n">{selected.size} selected</span>
          <button className="btn" onClick={() => setDialog({ kind: 'adhoc', limit: selectedHosts.map((h) => h.name).join(',') })}>
            Run command…
          </button>
          {editable && (
            <>
              <button className="btn" onClick={() => setEnabled(selectedHosts, true)}>
                Enable
              </button>
              <button className="btn" onClick={() => setEnabled(selectedHosts, false)}>
                Disable
              </button>
              <button className="btn warn" onClick={() => remove(selectedHosts)}>
                Delete
              </button>
            </>
          )}
          <button className="btn clear" onClick={() => setSelected(new Set())}>
            Clear
          </button>
        </div>
      )}
      <DataTable
        label="Hosts"
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
            title={ls.q ? 'No host matches' : 'No hosts yet'}
            action={editable && !ls.q ? <button className="btn primary" onClick={() => setDialog({ kind: 'host' })}>Add host</button> : undefined}
          >
            {ls.q ? 'Try a shorter filter.' : inv.kind === '' ? 'Add hosts here, or add a source that imports them.' : null}
          </Empty>
        }
        footer={
          <div className="table-foot">
            <span>{hosts.data ? `${plural(hosts.data.count, 'host', 'hosts')}` : ''}</span>
            <span className="mono">j/k move · x select · r run command · enter open</span>
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
      toast(host ? `Saved ${f.values.name}` : `Added ${f.values.name}`)
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
      title={host ? `Edit ${host.name}` : 'Add host'}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || !f.values.name || !!varsError(f.values.variables)} onClick={() => submit()}>
            {busy ? 'Saving…' : host ? 'Save' : 'Add host'}
          </button>
        </>
      }
    >
      <form className="form" onSubmit={submit}>
        <div className="form-grid">
          <TextInput label="Name" mono value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} help="Hostname or IP address, or a name with ansible_host set below." autoFocus />
          <TextInput label="Description" optional value={f.values.description} onChange={(v) => f.set('description', v)} />
        </div>
        <VarsEditor label="Host variables" value={f.values.variables} onChange={(v) => f.set('variables', v)} error={f.errors.variables} help="e.g. ansible_host: 10.0.4.12" />
        <Check label="Enabled" checked={f.values.enabled} onChange={(v) => f.set('enabled', v)} help="Disabled hosts stay in the inventory but jobs skip them." />
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
      setError(applyErrors(err, f.setErrors))
      setBusy(false)
    }
  }
  return (
    <Dialog
      title="Run a command"
      onClose={onClose}
      wide
      footer={
        <>
          <span className="small faint grow">
            Runs on <span className="mono">{v.limit || 'all'}</span> in {inventory.name}
          </span>
          <button className="btn" onClick={onClose}>
            Cancel
          </button>
          <button className="btn primary" disabled={busy || (needsArgs && !v.module_args.trim()) || !!varsError(v.extra_vars)} onClick={() => submit()}>
            <Icon name="play" size={12} /> {busy ? 'Starting…' : 'Run'}
          </button>
        </>
      }
    >
      <form className="form" onSubmit={submit}>
        <div className="row" style={{ gap: 12, alignItems: 'flex-start' }}>
          <div style={{ flex: '0 0 200px' }}>
            <SelectField label="Module" value={v.module_name} onChange={(x) => f.set('module_name', x)} options={list.map((m) => [m, m] as [string, string])} error={f.errors.module_name} />
          </div>
          <div className="grow">
            <TextInput
              label="Arguments"
              optional={!needsArgs}
              mono
              value={v.module_args}
              onChange={(x) => f.set('module_args', x)}
              error={f.errors.module_args}
              placeholder={v.module_name === 'shell' || v.module_name === 'command' ? 'uptime' : v.module_name === 'service' ? 'name=nginx state=restarted' : ''}
              autoFocus
            />
          </div>
        </div>
        <div className="form-grid">
          <TextInput label="Hosts" mono value={v.limit} onChange={(x) => f.set('limit', x)} error={f.errors.limit} help="Host pattern: names, groups, wildcards (web*), exclusions (!db1)." />
          <Lookup
            label="Machine credential"
            optional
            resource="credentials/"
            params={{ credential_type__kind: 'ssh' }}
            value={v.credential}
            onChange={(x) => f.set('credential', x)}
            error={f.errors.credential}
          />
          <SelectField
            label="Verbosity"
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
          <TextInput label="Forks" type="number" value={v.forks} onChange={(x) => f.set('forks', Number(x))} help="0 uses the default." />
        </div>
        <div className="row wrap" style={{ gap: 20 }}>
          <Check label="Privilege escalation (become)" checked={v.become_enabled} onChange={(x) => f.set('become_enabled', x)} />
          <Check label="Check mode" checked={v.check} onChange={(x) => f.set('check', x)} />
          <Check label="Show diffs" checked={v.diff_mode} onChange={(x) => f.set('diff_mode', x)} />
        </div>
        <VarsEditor label="Extra variables" rows={3} value={v.extra_vars} onChange={(x) => f.set('extra_vars', x)} error={f.errors.extra_vars} />
        <FormError error={error} />
        <button type="submit" hidden />
      </form>
    </Dialog>
  )
}
