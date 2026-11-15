import { useEffect, useState } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Any, Ref } from '../../api/types'
import { useOrg, usePreselectOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { Check, Field, FormError, Lookup, MultiLookup, SelectField, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { Ago, Cell, Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab, Upcoming } from '../../components/ui'
import { duration } from '../../lib/format'
import { splitSeconds, UNITS } from '../../lib/eventRules'
import { tr, trn } from '../../lib/i18n'
import { describeText } from '../automation/rrule'
import { RRuleEditor } from '../automation/ScheduleForm'
import { ListPage, useDelete } from '../admin/common'

type Win = Any & { id: number }

const KINDS: [string, string][] = [
  ['maintenance', tr('Maintenance window')],
  ['freeze', tr('Change freeze')],
]
const kindLabel = (k: string) => KINDS.find(([v]) => v === k)?.[1] ?? k

function WindowState({ w }: { w: Win }) {
  if (!w.enabled)
    return (
      <span className="status">
        <Cell state="empty" />
        {tr('Disabled')}
      </span>
    )
  if (w.active)
    return (
      <span className="status">
        <Cell state="running" />
        <span>
          {tr('On until')} <Upcoming at={w.active_until} />
        </span>
      </span>
    )
  return (
    <span className="status">
      <Cell state="pending" />
      <span>{w.next_start ? <>{tr('Next')} <Upcoming at={w.next_start} /></> : tr('No next occurrence')}</span>
    </span>
  )
}

/** What a window covers: its rules, its groups' hosts, or its organization. */
function coverage(w: Win): string {
  const rules = (w.event_rules ?? []).length
  const groups = (w.groups ?? []).length
  if (!rules && !groups) return tr('Every rule of its organization')
  return [rules && trn(rules, '{n} rule', '{n} rules'), groups && trn(groups, '{n} group', '{n} groups')].filter(Boolean).join(' · ')
}

export function WindowList() {
  const nav = useNavigate()
  const { scope } = useOrg()
  return (
    <ListPage<Win>
      title={tr('Maintenance windows')}
      subtitle={tr('Recurring periods when event rules hold their actions back or ask for approval: maintenance windows and change freezes.')}
      path="maintenance_windows/"
      params={scope}
      placeholder={tr('Filter windows…')}
      actions={
        <Link to="/maintenance-windows/new" className="btn primary">
          <Icon name="plus" size={14} />
          {tr('New window')}
        </Link>
      }
      onOpen={(w) => nav(`/maintenance-windows/${w.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Window'),
          sort: 'name',
          render: (w) => (
            <div>
              <Link to={`/maintenance-windows/${w.id}`} className="cell-title">
                {w.name}
              </Link>
              <div className="cell-sub">
                {kindLabel(w.kind)} · {describeText(w.rrule)} · {duration(w.duration)}
              </div>
            </div>
          ),
        },
        { key: 'state', header: tr('State'), render: (w) => <WindowState w={w} /> },
        { key: 'covers', header: tr('Covers'), render: (w) => <span className="small">{coverage(w)}</span> },
        { key: 'org', header: tr('Organization'), render: (w) => w.summary_fields.organization?.name ?? '—' },
      ]}
      empty={<Empty title={tr('No maintenance windows yet')}>{tr('A window holds back the actions of event rules while it is on, or makes them wait for approval.')}</Empty>}
    />
  )
}

export function WindowNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Maintenance windows'), to: '/maintenance-windows' }, { label: tr('New') }]} title={tr('New maintenance window')} />
      <div className="page-body">
        <WindowForm />
      </div>
    </div>
  )
}

/** The rules and groups a window lists, by name. */
function useRefs(path: string, ids: number[] | undefined) {
  const list = useList<Any>(ids?.length ? path : null, { id__in: ids?.join(','), page_size: 200 })
  return ids?.length ? list.data?.results.map((r): Ref => ({ id: r.id, name: r.name, summary_fields: r.summary_fields })) : []
}

export function WindowDetail() {
  const id = Number(useParams().id)
  const win = useObject<Win>(`maintenance_windows/${id}/`)
  const del = useDelete()
  const rules = useRefs('event_rules/', win.data?.event_rules)
  const groups = useRefs('groups/', win.data?.groups)
  if (win.error) return <div className="page-body"><ErrorNote error={win.error} /></div>
  if (!win.data) return <Loading />
  const w = win.data
  const caps = w.summary_fields.user_capabilities ?? {}
  const base = `/maintenance-windows/${id}`
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Maintenance windows'), to: '/maintenance-windows' }, { label: w.name }]}
        title={w.name}
        badge={<WindowState w={w} />}
        subtitle={w.description || kindLabel(w.kind)}
        actions={
          <>
            {caps.edit && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {caps.delete && (
              <button className="btn danger" onClick={() => del({ path: `maintenance_windows/${id}/`, name: w.name, what: tr('maintenance window'), then: '/maintenance-windows' })}>
                {tr('Delete')}
              </button>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              {tr('Details')}
            </Tab>
            <Tab to={`${base}/access`}>{tr('Access')}</Tab>
          </>
        }
      />
      <div className="page-body">
        <Routes>
          <Route
            index
            element={
              <Panel title={kindLabel(w.kind)}>
                <KV
                  items={[
                    [tr('When'), describeText(w.rrule)],
                    [tr('Lasts'), duration(w.duration)],
                    [tr('Next start'), w.next_start ? <Upcoming at={w.next_start} /> : <span className="faint">{tr('None')}</span>],
                    [
                      tr('Rules'),
                      rules === undefined ? (
                        <Loading rows={1} />
                      ) : rules.length ? (
                        <span>
                          {rules.map((r, i) => (
                            <span key={r.id}>
                              {i > 0 && ', '}
                              <Link to={`/event-rules/${r.id}`}>{r.name}</Link>
                            </span>
                          ))}
                        </span>
                      ) : (
                        <span className="faint">{(w.groups ?? []).length ? tr('The rules launching on the groups’ hosts') : tr('Every rule of the organization')}</span>
                      ),
                    ],
                    [tr('Groups'), groups?.length ? groups.map((g) => g.name).join(', ') : ''],
                    [tr('Organization'), <Link to={`/organizations/${w.organization}`}>{w.summary_fields.organization?.name}</Link>],
                    [tr('Changed'), <Ago at={w.modified} />],
                  ]}
                />
              </Panel>
            }
          />
          <Route path="edit" element={rules && groups ? <WindowForm win={w} rules={rules} groups={groups} /> : <Loading />} />
          <Route path="access" element={<AccessTab resource="maintenance_windows" id={id} name={w.name} />} />
        </Routes>
      </div>
    </div>
  )
}

interface WinValues extends Record<string, unknown> {
  name: string
  description: string
  organization: Ref | null
  kind: string
  rrule: string
  n: string
  unit: number
  enabled: boolean
  rules: Ref[]
  groups: Ref[]
}

function WindowForm({ win, rules, groups }: { win?: Win; rules?: Ref[]; groups?: Ref[] }) {
  const nav = useNavigate()
  const { org } = useOrg()
  const invalidate = useInvalidate()
  const split = splitSeconds(win?.duration ?? 7200)
  const f = useForm<WinValues>({
    name: win?.name ?? '',
    description: win?.description ?? '',
    organization: win?.summary_fields.organization ?? org,
    kind: win?.kind ?? 'maintenance',
    rrule: win?.rrule ?? '',
    n: String(split.n),
    unit: split.unit,
    enabled: win?.enabled ?? true,
    rules: rules ?? [],
    groups: groups ?? [],
  })
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !win)
  const orgId = f.values.organization?.id
  useEffect(() => {
    if (!win && !rules) f.set('rules', [])
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [orgId])
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    setError(null)
    const body = {
      name: f.values.name,
      description: f.values.description,
      organization: orgId,
      kind: f.values.kind,
      rrule: f.values.rrule,
      duration: Math.round(Number(f.values.n) * f.values.unit),
      enabled: f.values.enabled,
      event_rules: f.values.rules.map((r) => r.id),
      groups: f.values.groups.map((g) => g.id),
    }
    try {
      const saved = win ? await api.patch<Win>(`maintenance_windows/${win.id}/`, body) : await api.post<Win>('maintenance_windows/', body)
      invalidate('maintenance_windows')
      nav(`/maintenance-windows/${saved.id}`)
    } catch (e) {
      setError(e)
      if (e instanceof ApiError) f.setErrors({ ...e.fields, rules: e.fields.event_rules ?? '', n: e.fields.duration ?? '' })
    } finally {
      setBusy(false)
    }
  }
  return (
    <form
      className="form"
      style={{ maxWidth: 980 }}
      onSubmit={(ev) => {
        ev.preventDefault()
        save()
      }}
    >
      <div className="form-grid">
        <TextInput label={tr('Name')} value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus={!win} />
        <Lookup label={tr('Organization')} resource="organizations/" value={f.values.organization} onChange={(v) => f.set('organization', v)} error={f.errors.organization} />
        <SelectField
          label={tr('Kind')}
          value={f.values.kind}
          onChange={(v) => f.set('kind', v)}
          options={KINDS}
          error={f.errors.kind}
          help={f.values.kind === 'freeze' ? tr('A change freeze: nothing should change while it is on.') : tr('A maintenance window: the hosts are being worked on.')}
        />
        <TextArea label={tr('Description')} optional rows={2} value={f.values.description} onChange={(v) => f.set('description', v)} />
      </div>
      <section className="form-section">
        <h2>{tr('When')}</h2>
        <RRuleEditor value={f.values.rrule} onChange={(v) => f.set('rrule', v)} error={f.errors.rrule} />
        <div className="form-grid">
          <Field label={tr('Each occurrence lasts')} error={f.errors.n} help={tr('From one minute to 90 days.')}>
            <div className="row" style={{ gap: 6 }}>
              <input className="input" style={{ flex: '1 1 90px' }} type="number" min={1} aria-label={tr('Number')} value={f.values.n} onChange={(e) => f.set('n', e.target.value)} />
              <select className="select" style={{ flex: '1 1 110px' }} aria-label={tr('Unit')} value={f.values.unit} onChange={(e) => f.set('unit', Number(e.target.value))}>
                {UNITS.filter(([u]) => u >= 60).map(([u, l]) => (
                  <option key={u} value={u}>
                    {l}
                  </option>
                ))}
              </select>
            </div>
          </Field>
        </div>
      </section>
      <section className="form-section">
        <h2>{tr('Covers')}</h2>
        <p className="small muted" style={{ margin: 0 }}>
          {tr('The rules it lists, and the launches on the hosts of the groups it lists. Listing neither, it covers every rule of its organization. Each rule says whether it holds its action back or asks for approval.')}
        </p>
        <div className="form-grid">
          <MultiLookup label={tr('Rules')} optional resource="event_rules/" params={orgId ? { organization: orgId } : undefined} values={f.values.rules} onChange={(v) => f.set('rules', v)} error={f.errors.rules} />
          <MultiLookup
            label={tr('Inventory groups')}
            optional
            resource="groups/"
            values={f.values.groups}
            onChange={(v) => f.set('groups', v)}
            error={f.errors.groups}
            meta={(r) => <span className="faint small">{r.summary_fields?.inventory?.name}</span>}
          />
        </div>
        <Check label={tr('Enabled')} checked={f.values.enabled} onChange={(v) => f.set('enabled', v)} />
      </section>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim() || !f.values.organization || !f.values.rrule}>
          {busy ? tr('Saving…') : win ? tr('Save changes') : tr('Create window')}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(win ? `/maintenance-windows/${win.id}` : '/maintenance-windows')}>
          {tr('Cancel')}
        </button>
      </div>
    </form>
  )
}
