import { useQuery } from '@tanstack/react-query'
import { useState, type FormEvent, type ReactNode } from 'react'
import { Link, Navigate, Outlet, useNavigate, useParams, useSearchParams } from 'react-router'
import { api, getAll, type Page } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import { useLive } from '../../api/live'
import type { Base, Ref } from '../../api/types'
import { useOrg, usePreselectOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { useCopy } from '../../components/CopyDialog'
import { BulkBar, BulkDelete, useBulk, useSelection } from '../../components/BulkBar'
import { Check, FormError, Lookup, MultiLookup, SelectField, TextInput, VarsEditor, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { Menu, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Empty, ErrorNote, KV, Loading, PageHead, Panel, Status, Tab } from '../../components/ui'
import { plural } from '../../lib/format'
import { compile, type KeyTable } from '../../lib/query'
import { syncRelation } from '../../lib/relations'
import { applyErrors, Count, FilterBox, KindTag, ManagedNotice, NameCell, Provenance, refOf, useDelete, useOrgColumn, folderColumn, folderKey } from './common'
import { UsedByPanel } from '../../components/UsedBy'

export interface Inventory extends Base {
  kind: '' | 'smart' | 'constructed'
  organization: number
  folder: number | null
  host_filter: string
  variables: string
  total_hosts: number
  hosts_with_active_failures: number
  total_groups: number
  total_inventory_sources: number
  inventory_sources_with_failures: number
  has_inventory_sources: boolean
  managed_by?: string
  prevent_instance_group_fallback?: boolean
}

const kindLabel = (k: string) => (k === 'smart' ? 'smart' : k === 'constructed' ? 'constructed' : 'inventory')

const keys: KeyTable = {
  kind: { field: 'kind', map: (v) => (v === 'inventory' || v === 'regular' ? '' : v) },
  org: { field: 'organization__name', lookup: 'iexact' },
  folder: folderKey,
  name: { field: 'name', lookup: 'icontains' },
}

export function InventoryList() {
  const ls = useListState({ order: 'name' })
  const { scope } = useOrg()
  const nav = useNavigate()
  const q = compile(ls.q, keys)
  const params = { ...q.params, ...scope, order_by: ls.order, page: ls.page, page_size: ls.pageSize }
  const list = useList<Inventory>('inventories/', params)
  const sel = useSelection<Inventory>(list.data?.results)
  const run = useBulk<Inventory>(['inventory', 'inventories'], 'inventories', 'inventory_sources')
  const del = useDelete('inventories')
  const copy = useCopy('inventories', { to: (id) => `/inventories/${id}`, what: 'Hosts and groups are copied with their variables. Inventory sources are not.' })
  const orgCol = useOrgColumn<Inventory>()
  const cols: Column<Inventory>[] = [
    {
      key: 'name',
      header: 'Inventory',
      sort: 'name',
      render: (r) => <NameCell to={`/inventories/${r.id}`} name={r.name} sub={r.description || (r.kind === 'smart' ? <code>{r.host_filter}</code> : undefined)} />,
    },
    { key: 'kind', header: 'Kind', render: (r) => (r.kind ? <KindTag>{kindLabel(r.kind)}</KindTag> : <span className="faint small">inventory</span>) },
    { key: 'hosts', header: 'Hosts', render: (r) => <Count n={r.total_hosts} bad={r.hosts_with_active_failures} /> },
    { key: 'groups', header: 'Groups', render: (r) => <span className="mono small">{r.kind === 'smart' ? '—' : r.total_groups}</span> },
    {
      key: 'sources',
      header: 'Sources',
      render: (r) =>
        r.inventory_sources_with_failures ? (
          <Status status="failed">
            <span className="small">
              {r.total_inventory_sources} · {r.inventory_sources_with_failures} failing
            </span>
          </Status>
        ) : r.total_inventory_sources ? (
          <span className="mono small">{r.total_inventory_sources}</span>
        ) : (
          <span className="faint small">—</span>
        ),
    },
    ...orgCol,
    ...folderColumn(list.data?.results),
    { key: 'modified', header: 'Edited', sort: 'modified', render: (r) => <span className="small faint nowrap"><Ago at={r.modified} /></span> },
    {
      key: 'menu',
      header: <span className="sr-only">Actions</span>,
      className: 'end',
      render: (r) => (
        <Menu label={`Actions for ${r.name}`} className="btn sm icon ghost">
          <Link to={`/inventories/${r.id}/edit`}>Edit</Link>
          <Link to={`/inventories/${r.id}/hosts`}>Hosts</Link>
          {r.kind !== 'smart' && <Link to={`/inventories/${r.id}/sources`}>Sources</Link>}
          {r.summary_fields.user_capabilities?.copy && <button onClick={() => copy.open(r)}>Copy…</button>}
          <hr />
          <button className="danger" onClick={() => del(r, 'inventory with all its hosts and groups')}>
            Delete…
          </button>
        </Menu>
      ),
    },
  ]
  return (
    <div className="page">
      <PageHead
        title="Inventories"
        subtitle="Hosts and groups that jobs run against. Smart inventories filter hosts across inventories; constructed inventories compute groups from inputs."
        bordered={false}
        actions={<NewInventoryMenu />}
      />
      {copy.dialog}
      <div className="page-body">
        <FilterBox value={ls.q} onApply={ls.setQ} placeholder="name…  try  kind:smart  org:Acme*  folder:checkout" />
        <BulkBar sel={sel} page={list.data?.results} total={list.data?.count} path="inventories/" params={params} style={{ marginBottom: 12 }}>
          <button
            className="btn"
            onClick={() =>
              run('Sync sources', sel.rows, (r) =>
                r.kind === 'smart' ? Promise.reject(new Error(`${r.name} is a smart inventory, with no sources`)) : api.post(`inventories/${r.id}/update_inventory_sources/`, {}),
              )
            }
          >
            Sync sources
          </button>
          <BulkDelete sel={sel} resource="inventories" noun={['inventory', 'inventories']} name={(r) => r.name} note="Their hosts, groups and sources are deleted with them; job history stays." />
        </BulkBar>
        <DataTable
          label="Inventories"
          rows={list.data?.results}
          columns={cols}
          selected={sel.selected}
          onSelect={sel.onSelect}
          columnsKey="inventories"
          loading={list.isPending}
          error={list.error}
          order={ls.order}
          onOrder={ls.setOrder}
          onOpen={(r) => nav(`/inventories/${r.id}`)}
          empty={
            <Empty title={ls.q ? 'No inventory matches' : 'No inventories yet'} action={!ls.q && <NewInventoryMenu />}>
              {ls.q ? 'Try a shorter filter.' : 'Create an inventory, then add hosts or a source that imports them.'}
            </Empty>
          }
          footer={
            <div className="table-foot">
              <span>{list.data ? `${plural(list.data.count, 'inventory', 'inventories')}` : ''}</span>
              <span className="mono">j/k move · enter open · / filter</span>
              <Pager count={list.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
            </div>
          }
        />
      </div>
    </div>
  )
}

function NewInventoryMenu() {
  const nav = useNavigate()
  return (
    <Menu
      label="New inventory"
      className="btn primary"
      trigger={
        <>
          New inventory <Icon name="chevron" size={12} />
        </>
      }
    >
      <button onClick={() => nav('/inventories/new')}>
        <span className="stack" style={{ gap: 0 }}>
          <span>Inventory</span>
          <span className="small faint">Hosts and groups you add or import</span>
        </span>
      </button>
      <button onClick={() => nav('/inventories/new?kind=smart')}>
        <span className="stack" style={{ gap: 0 }}>
          <span>Smart inventory</span>
          <span className="small faint">Hosts matching a filter, across inventories</span>
        </span>
      </button>
      <button onClick={() => nav('/inventories/new?kind=constructed')}>
        <span className="stack" style={{ gap: 0 }}>
          <span>Constructed inventory</span>
          <span className="small faint">Groups computed from input inventories</span>
        </span>
      </button>
    </Menu>
  )
}

interface ConstructedExtra {
  source_vars: string
  limit: string
  update_cache_timeout: number
  verbosity: number
}

/** Create and edit form for all three kinds of inventory. */
export function InventoryForm() {
  const { id } = useParams()
  const [sp] = useSearchParams()
  const editing = id !== undefined
  const existing = useObject<Inventory>(editing ? `inventories/${id}/` : null)
  const kind = (editing ? existing.data?.kind : (sp.get('kind') as Inventory['kind'])) ?? ''
  const constructed = useObject<Inventory & ConstructedExtra>(editing && kind === 'constructed' ? `constructed_inventories/${id}/` : null)
  const inputs = useQuery({
    queryKey: [`inventories/${id}/input_inventories/`, 'all'],
    queryFn: () => getAll<Ref>(`inventories/${id}/input_inventories/`),
    enabled: editing && kind === 'constructed',
  })
  const groups = useQuery({
    queryKey: [`inventories/${id}/instance_groups/`, 'all'],
    queryFn: () => getAll<Ref>(`inventories/${id}/instance_groups/`),
    enabled: editing,
  })
  if (editing && (existing.isPending || groups.isPending || (kind === 'constructed' && (constructed.isPending || inputs.isPending)))) return <Loading />
  if (existing.error) return <ErrorNote error={existing.error} />
  return <InventoryFormInner key={id ?? 'new' + kind} kind={kind} inv={existing.data} extra={constructed.data} inputs={inputs.data ?? []} groups={groups.data ?? []} />
}

function InventoryFormInner({ kind, inv, extra, inputs, groups }: { kind: Inventory['kind']; inv?: Inventory; extra?: ConstructedExtra; inputs: Ref[]; groups: Ref[] }) {
  const { org } = useOrg()
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const toast = useToast()
  const f = useForm({
    name: inv?.name ?? '',
    description: inv?.description ?? '',
    organization: refOf(inv?.summary_fields.organization, inv?.organization) ?? (org as Ref | null),
    folder: inv?.summary_fields.folder ?? null,
    variables: inv?.variables ?? '',
    host_filter: inv?.host_filter ?? '',
    source_vars: extra?.source_vars ?? 'plugin: constructed\nstrict: false\ngroups: {}\n',
    limit: extra?.limit ?? '',
    update_cache_timeout: extra?.update_cache_timeout ?? 0,
    verbosity: extra?.verbosity ?? 1,
    inputs: inputs as Ref[],
    instance_groups: groups,
    prevent_instance_group_fallback: inv?.prevent_instance_group_fallback ?? false,
    labels: (inv?.summary_fields.labels?.results ?? []) as Ref[],
  })
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !inv)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const v = f.values
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    setBusy(true)
    setError(null)
    const body: Record<string, unknown> = {
      name: v.name,
      description: v.description,
      organization: v.organization?.id ?? null,
      folder: v.folder?.id ?? null,
      variables: v.variables,
      prevent_instance_group_fallback: v.prevent_instance_group_fallback,
    }
    if (kind === 'smart') body.host_filter = v.host_filter
    if (!inv) body.kind = kind
    let path = 'inventories/'
    if (kind === 'constructed') {
      path = 'constructed_inventories/'
      delete body.kind
      Object.assign(body, { source_vars: v.source_vars, limit: v.limit, update_cache_timeout: Number(v.update_cache_timeout) || 0, verbosity: Number(v.verbosity) })
    }
    try {
      const saved = inv ? await api.patch<Inventory>(`${path}${inv.id}/`, body) : await api.post<Inventory>(path, body)
      if (kind === 'constructed') {
        const before = new Set(inputs.map((i) => i.id))
        const after = new Set(v.inputs.map((i) => i.id))
        for (const i of v.inputs) if (!before.has(i.id)) await api.post(`inventories/${saved.id}/input_inventories/`, { id: i.id })
        for (const i of inputs) if (!after.has(i.id)) await api.post(`inventories/${saved.id}/input_inventories/`, { id: i.id, disassociate: true })
      }
      // Instance groups are tried in order: a new order rebuilds the list.
      await syncRelation(`inventories/${saved.id}/instance_groups/`, groups.map((g) => g.id), v.instance_groups.map((g) => g.id), true)
      await syncRelation(`inventories/${saved.id}/labels/`, (inv?.summary_fields.labels?.results ?? []).map((l: Ref) => l.id), v.labels.map((l) => l.id))
      invalidate('inventories', 'constructed_inventories')
      toast(inv ? `Saved ${saved.name}` : `Created ${saved.name}`)
      nav(`/inventories/${saved.id}`)
    } catch (err) {
      setError(applyErrors(err, f.setErrors))
    } finally {
      setBusy(false)
    }
  }
  const title = inv ? `Edit ${inv.name}` : kind === 'smart' ? 'New smart inventory' : kind === 'constructed' ? 'New constructed inventory' : 'New inventory'
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: 'Inventories', to: '/inventories' }, ...(inv ? [{ label: inv.name, to: `/inventories/${inv.id}` }] : []), { label: inv ? 'Edit' : 'New' }]}
        title={title}
      />
      <form className="page-body form" onSubmit={submit} style={{ maxWidth: 920 }}>
        <ManagedNotice managedBy={inv?.managed_by} />
        <div className="form-grid">
          <TextInput label="Name" value={v.name} onChange={(x) => f.set('name', x)} error={f.errors.name} autoFocus />
          <Lookup label="Organization" resource="organizations/" value={v.organization} onChange={(x) => f.set('organization', x)} error={f.errors.organization} />
          <TextInput label="Description" optional value={v.description} onChange={(x) => f.set('description', x)} error={f.errors.description} full />
          <Lookup
            label="Folder"
            optional
            resource="folders/"
            params={v.organization ? { organization: v.organization.id } : undefined}
            value={v.folder}
            onChange={(x) => f.set('folder', x)}
            error={f.errors.folder}
            help="Roles granted on the folder apply to this inventory."
          />
        </div>
        {kind === 'smart' && (
          <div className="form-section">
            <h2>Host filter</h2>
            <TextInput
              label="Filter"
              mono
              value={v.host_filter}
              onChange={(x) => f.set('host_filter', x)}
              error={f.errors.host_filter}
              help={
                <>
                  Hosts from every inventory of the organization that match, e.g. <code>name__icontains=web</code> or{' '}
                  <code>groups__name=db and enabled=true</code>.
                </>
              }
              full
            />
          </div>
        )}
        {kind === 'constructed' && (
          <div className="form-section">
            <h2>Construction</h2>
            <MultiLookup
              label="Input inventories"
              resource="inventories/"
              params={{ ...(v.organization ? { organization: v.organization.id } : {}), not__kind: 'constructed' }}
              values={v.inputs}
              onChange={(x) => f.set('inputs', x)}
              help="Hosts and groups of these inventories, in order, feed the constructed plugin."
              full
            />
            <VarsEditor
              label="Constructed plugin configuration"
              optional={false}
              rows={8}
              value={v.source_vars}
              onChange={(x) => f.set('source_vars', x)}
              error={f.errors.source_vars}
              help={
                <>
                  YAML for the <code>constructed</code> inventory plugin: <code>groups</code>, <code>keyed_groups</code>, <code>compose</code>.
                </>
              }
            />
            <div className="form-grid">
              <TextInput label="Limit" optional mono value={v.limit} onChange={(x) => f.set('limit', x)} help="Host pattern applied to the result." />
              <TextInput
                label="Cache timeout (seconds)"
                type="number"
                value={v.update_cache_timeout}
                onChange={(x) => f.set('update_cache_timeout', Number(x))}
                help="Reuse a recent update instead of rebuilding before each job."
              />
              <SelectField label="Verbosity" value={v.verbosity} onChange={(x) => f.set('verbosity', Number(x))} options={[[0, '0 (warning)'], [1, '1 (info)'], [2, '2 (debug)']]} />
            </div>
          </div>
        )}
        <div className="form-section">
          <h2>Where jobs run</h2>
          <div className="form-grid">
            <MultiLookup
              label="Instance groups"
              optional
              resource="instance_groups/"
              values={v.instance_groups}
              onChange={(x) => f.set('instance_groups', x)}
              help="Tried in this order for jobs on this inventory, after the template's. Empty uses the organization's."
              ordered
              full
            />
            <div className="field full">
              <Check
                label="Only these instance groups: do not fall back to the organization's"
                checked={v.prevent_instance_group_fallback}
                onChange={(x) => f.set('prevent_instance_group_fallback', x)}
              />
            </div>
            <MultiLookup
              label="Labels"
              optional
              resource="labels/"
              params={v.organization ? { organization: v.organization.id } : undefined}
              values={v.labels}
              onChange={(x) => f.set('labels', x)}
              full
            />
          </div>
        </div>
        <VarsEditor value={v.variables} onChange={(x) => f.set('variables', x)} error={f.errors.variables} help="Inventory-wide variables, the equivalent of group_vars/all." />
        <FormError error={error} />
        <div className="form-actions">
          <button className="btn primary" disabled={busy || !v.name || !v.organization}>
            {busy ? 'Saving…' : inv ? 'Save' : 'Create inventory'}
          </button>
          <button type="button" className="btn ghost" onClick={() => nav(-1)}>
            Cancel
          </button>
        </div>
      </form>
    </div>
  )
}

/** Inventory page frame with tabs; tab content renders in the outlet. */
export function InventoryDetail() {
  const { id } = useParams()
  const inv = useObject<Inventory>(`inventories/${id}/`)
  const del = useDelete('inventories', '/inventories')
  const copy = useCopy('inventories', { to: (id) => `/inventories/${id}`, what: 'Hosts and groups are copied with their variables. Inventory sources are not.' })
  const invalidate = useInvalidate()
  // Source updates and commands change hosts, groups and statuses.
  useLive({ jobs: ['status_changed'] }, (m) => {
    if (m.type === 'inventory_update' || m.type === 'ad_hoc_command') invalidate(`inventories/${id}/`, 'hosts', 'groups')
  })
  if (inv.isPending) return <Loading />
  if (inv.error || !inv.data) return <div className="page-body"><ErrorNote error={inv.error ?? 'Not found'} /></div>
  const i = inv.data
  const caps = i.summary_fields.user_capabilities ?? {}
  const base = `/inventories/${i.id}`
  return (
    <div className="page">
      {copy.dialog}
      <PageHead
        crumbs={[{ label: 'Inventories', to: '/inventories' }, { label: i.name }]}
        title={i.name}
        badge={i.kind ? <KindTag>{kindLabel(i.kind)}</KindTag> : undefined}
        subtitle={i.description || undefined}
        actions={
          <>
            {caps.edit && !i.managed_by && (
              <Link className="btn" to={`${base}/edit`}>
                Edit
              </Link>
            )}
            <Menu label="More actions">
              <a href={`/api/v2/inventories/${i.id}/script/?hostvars=1`} target="_blank" rel="noreferrer">
                View as JSON inventory <Icon name="external" size={12} />
              </a>
              {caps.copy && <button onClick={() => copy.open(i)}>Copy…</button>}
              {caps.delete && (
                <>
                  <hr />
                  <button className="danger" onClick={() => del(i, 'inventory with all its hosts and groups')}>
                    Delete…
                  </button>
                </>
              )}
            </Menu>
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              Details
            </Tab>
            <Tab to={`${base}/hosts`} n={i.total_hosts}>
              Hosts
            </Tab>
            {i.kind !== 'smart' && (
              <Tab to={`${base}/groups`} n={i.total_groups}>
                Groups
              </Tab>
            )}
            {i.kind === '' && (
              <Tab to={`${base}/sources`} n={i.total_inventory_sources}>
                Sources
              </Tab>
            )}
            <Tab to={`${base}/jobs`}>Jobs</Tab>
            <Tab to={`${base}/access`}>Access</Tab>
          </>
        }
      />
      <Outlet context={i} />
    </div>
  )
}

export function InventoryDetailsTab({ inv }: { inv: Inventory }) {
  const inputs = useList<Ref>(inv.kind === 'constructed' ? `inventories/${inv.id}/input_inventories/` : null, { page_size: 50 })
  const constructed = useObject<ConstructedExtra>(inv.kind === 'constructed' ? `constructed_inventories/${inv.id}/` : null)
  const sources = useList<Base>(inv.kind === 'constructed' ? `inventories/${inv.id}/inventory_sources/` : null, { page_size: 1 })
  const toast = useToast()
  const invalidate = useInvalidate()
  const src = sources.data?.results[0]
  const syncConstructed = async () => {
    if (!src) return
    try {
      await api.post(`inventory_sources/${src.id}/update/`)
      toast('Constructed inventory update started')
      invalidate('inventories', 'inventory_sources', 'inventory_updates')
    } catch (e) {
      toast(e instanceof Error ? e.message : 'Update failed', 'error')
    }
  }
  return (
    <div className="page-body">
      <ManagedNotice managedBy={inv.managed_by} />
      <div className="split">
        <div className="wide stack" style={{ gap: 16 }}>
          <Panel title="About">
            <KV
              items={[
                ['Organization', inv.summary_fields.organization ? <Link to={`/organizations/${inv.organization}`}>{inv.summary_fields.organization.name}</Link> : '—'],
                ['Kind', inv.kind === 'smart' ? 'Smart inventory' : inv.kind === 'constructed' ? 'Constructed inventory' : 'Inventory'],
                ['Folder', inv.summary_fields.folder?.name ?? ''],
                inv.kind === 'smart' ? ['Host filter', <code>{inv.host_filter || '—'}</code>] : [null, null],
                inv.kind === 'constructed'
                  ? [
                      'Inputs',
                      inputs.data?.results.length ? (
                        <span className="row wrap" style={{ gap: 6 }}>
                          {inputs.data.results.map((x) => (
                            <Link key={x.id} to={`/inventories/${x.id}`} className="chip" style={{ fontFamily: 'var(--sans)' }}>
                              {x.name}
                            </Link>
                          ))}
                        </span>
                      ) : (
                        <span className="faint">none</span>
                      ),
                    ]
                  : [null, null],
                ['History', <Provenance obj={inv} />],
              ].filter(([k]) => k !== null) as [string, ReactNode][]}
            />
          </Panel>
          {inv.kind === 'constructed' && (
            <Panel
              title="Constructed plugin configuration"
              actions={
                src ? (
                  <>
                    <Status status={src.status} />
                    <button className="btn sm" onClick={syncConstructed}>
                      <Icon name="refresh" size={14} /> Update now
                    </button>
                  </>
                ) : undefined
              }
            >
              <pre className="code">{constructed.data?.source_vars || '—'}</pre>
            </Panel>
          )}
          <Panel title="Variables">{inv.variables?.trim() ? <pre className="code">{inv.variables}</pre> : <span className="faint">No inventory variables.</span>}</Panel>
        </div>
        <div className="narrow stack" style={{ gap: 16 }}>
          <Panel title="Hosts">
            <div className="stack small" style={{ gap: 6 }}>
              <div className="row">
                <span className="grow">Total</span>
                <Link to={`/inventories/${inv.id}/hosts`} className="mono">
                  {inv.total_hosts}
                </Link>
              </div>
              <div className="row">
                <span className="grow">Failed in their last job</span>
                <span className={`mono ${inv.hosts_with_active_failures ? 'fail-text' : ''}`}>{inv.hosts_with_active_failures}</span>
              </div>
              {inv.kind !== 'smart' && (
                <div className="row">
                  <span className="grow">Groups</span>
                  <span className="mono">{inv.total_groups}</span>
                </div>
              )}
              {inv.kind === '' && (
                <div className="row">
                  <span className="grow">Sources</span>
                  <span className={`mono ${inv.inventory_sources_with_failures ? 'fail-text' : ''}`}>
                    {inv.total_inventory_sources}
                    {inv.inventory_sources_with_failures ? ` · ${inv.inventory_sources_with_failures} failing` : ''}
                  </span>
                </div>
              )}
            </div>
          </Panel>
          <UsedByPanel resource="inventories" id={inv.id} empty="Nothing uses this inventory." />
        </div>
      </div>
    </div>
  )
}

export function InventoryAccessTab({ inv }: { inv: Inventory }) {
  return (
    <div className="page-body">
      <AccessTab resource="inventories" id={inv.id} name={inv.name} />
    </div>
  )
}

/** /inventory-sources/:id → the Sources tab of its inventory. */
export function InventorySourceRedirect() {
  const { id } = useParams()
  const src = useObject<Base>(`inventory_sources/${id}/`)
  if (src.isPending) return <Loading />
  if (src.error || !src.data) return <div className="page-body"><ErrorNote error={src.error ?? 'Not found'} /></div>
  return <Navigate replace to={`/inventories/${src.data.inventory}/sources?source=${id}`} />
}

/** Jobs that targeted this inventory: source updates and commands. */
export function InventoryJobsTab({ inv }: { inv: Inventory }) {
  const [page, setPage] = useState(1)
  const updates = useList<Base>('inventory_updates/', { inventory: inv.id, order_by: '-id', page_size: 25 * page })
  const adhoc = useList<Base>('ad_hoc_commands/', { inventory: inv.id, order_by: '-id', page_size: 25 * page })
  const nav = useNavigate()
  const rows = [...(updates.data?.results ?? []), ...(adhoc.data?.results ?? [])].sort((a, b) => b.id - a.id).slice(0, 25 * page)
  const total = (updates.data?.count ?? 0) + (adhoc.data?.count ?? 0)
  const cols: Column<Base>[] = [
    { key: 'status', header: 'Status', width: 130, render: (r) => <Status status={r.status} /> },
    {
      key: 'name',
      header: 'Job',
      render: (r) => (
        <NameCell
          to={`/jobs/${r.type}/${r.id}`}
          name={r.type === 'ad_hoc_command' ? `${r.module_name} ${r.module_args ?? ''}`.trim() : r.name}
          sub={r.type === 'ad_hoc_command' ? `command · limit ${r.limit || 'all'}` : 'source update'}
        />
      ),
    },
    { key: 'id', header: 'ID', render: (r) => <span className="mono small faint">#{r.id}</span> },
    { key: 'by', header: 'Launched by', render: (r) => <span className="small">{r.launched_by?.name ?? r.launch_type}</span> },
    { key: 'when', header: 'Finished', render: (r) => <span className="small nowrap">{r.finished ? <Ago at={r.finished} /> : <span className="faint">{r.status}</span>}</span> },
  ]
  return (
    <div className="page-body">
      <DataTable
        label="Jobs for this inventory"
        rows={rows}
        columns={cols}
        loading={updates.isPending || adhoc.isPending}
        error={updates.error || adhoc.error}
        onOpen={(r) => nav(`/jobs/${r.type}/${r.id}`)}
        empty={<Empty title="No jobs yet">Source updates and commands run on this inventory show up here.</Empty>}
        footer={
          rows.length < total ? (
            <div className="table-foot">
              <span>
                {rows.length} of {total}
              </span>
              <button className="btn sm" onClick={() => setPage((p) => p + 1)}>
                Show more
              </button>
            </div>
          ) : undefined
        }
      />
    </div>
  )
}

/** Fetches a page of an inventory sublist; used by the hosts and groups tabs. */
export function useSubList<T>(path: string, params: Record<string, string | number | undefined>) {
  return useQuery({
    queryKey: [path, 'list', params],
    queryFn: ({ signal }) => api.get<Page<T>>(path, params, signal),
    placeholderData: (prev) => prev,
  })
}
