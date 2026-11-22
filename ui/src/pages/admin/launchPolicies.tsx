import { useQuery } from '@tanstack/react-query'
import { useMemo, useState, type ReactNode } from 'react'
import { Link, Route, Routes, useNavigate, useParams } from 'react-router'
import { api, ApiError } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import type { Any, Ref } from '../../api/types'
import { useMe, useOrg, usePreselectOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { FormError, Lookup, SelectField, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { AttachedList, PolicyDecisions, PolicyMode } from '../../components/LaunchPolicies'
import { DataTable } from '../../components/table'
import { Ago, Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab, useDebounced } from '../../components/ui'
import { absolute, jobPath } from '../../lib/format'
import { asIs, tr, trn } from '../../lib/i18n'
import { fieldGroups, fieldsTree, MODES, modeLabel, POLICY_EXAMPLES, policyCompletions, type PolicyField } from '../../lib/launchPolicies'
import { CelInput } from '../events/CelInput'
import '../events/events.css'
import { ListPage, useDelete } from './common'

type Policy = Any & { id: number }

function scopeText(p: Policy): string {
  if (!p.organization) return tr('Global: every launch')
  const n = (p.attached ?? []).length
  return n ? trn(n, 'Attached to {n} object', 'Attached to {n} objects') : tr('Not attached')
}

export function LaunchPolicyList() {
  const nav = useNavigate()
  const { scope } = useOrg()
  return (
    <ListPage<Policy>
      title={tr('Launch policies')}
      subtitle={tr('Conditions checked before each launch: a policy refuses the launches its condition matches, or only records them in shadow mode.')}
      path="launch_policies/"
      params={scope}
      placeholder={tr('Filter policies…')}
      actions={
        <Link to="/launch-policies/new" className="btn primary">
          <Icon name="plus" size={14} />
          {tr('New policy')}
        </Link>
      }
      onOpen={(p) => nav(`/launch-policies/${p.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Policy'),
          sort: 'name',
          render: (p) => (
            <div>
              <Link to={`/launch-policies/${p.id}`} className="cell-title">
                {p.name}
              </Link>
              <div className="cell-sub mono" style={{ maxWidth: 560, overflow: 'hidden', textOverflow: 'ellipsis', whiteSpace: 'nowrap' }}>
                {p.condition}
              </div>
            </div>
          ),
        },
        { key: 'mode', header: tr('Mode'), sort: 'mode', render: (p) => <PolicyMode mode={p.mode} /> },
        { key: 'checks', header: tr('Checks'), render: (p) => <span className="small">{scopeText(p)}</span> },
        { key: 'org', header: tr('Organization'), render: (p) => p.summary_fields.organization?.name ?? <span className="faint">{tr('Global')}</span> },
        { key: 'modified', header: tr('Changed'), sort: 'modified', render: (p) => <Ago at={p.modified} /> },
      ]}
      empty={<Empty title={tr('No launch policies yet')}>{tr('A policy is a CEL condition on each launch: a change window, a limit on large inventories, images from your registry only.')}</Empty>}
    />
  )
}

export function LaunchPolicyNew() {
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Launch policies'), to: '/launch-policies' }, { label: tr('New') }]} title={tr('New launch policy')} />
      <div className="page-body">
        <PolicyForm />
      </div>
    </div>
  )
}

export function LaunchPolicyDetail() {
  const id = Number(useParams().id)
  const pol = useObject<Policy>(`launch_policies/${id}/`)
  const del = useDelete()
  if (pol.error)
    return (
      <div className="page-body">
        <ErrorNote error={pol.error} />
      </div>
    )
  if (!pol.data) return <Loading />
  const p = pol.data
  const caps = p.summary_fields.user_capabilities ?? {}
  const base = `/launch-policies/${id}`
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Launch policies'), to: '/launch-policies' }, { label: p.name }]}
        title={p.name}
        badge={<PolicyMode mode={p.mode} />}
        subtitle={p.description || scopeText(p)}
        actions={
          <>
            {caps.edit && !p.managed_by && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {caps.delete && (
              <button className="btn danger" onClick={() => del({ path: `launch_policies/${id}/`, name: p.name, what: tr('launch policy'), then: '/launch-policies' })}>
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
            <Tab to={`${base}/decisions`}>{tr('Decisions')}</Tab>
            <Tab to={`${base}/backtest`}>{tr('Backtest')}</Tab>
            <Tab to={`${base}/access`}>{tr('Access')}</Tab>
          </>
        }
      />
      <div className="page-body">
        <Routes>
          <Route
            index
            element={
              <div className="split">
                <Panel title={tr('Refuses a launch when')} className="wide">
                  <div className="stack" style={{ gap: 12 }}>
                    <pre className="code">{p.condition}</pre>
                    <KV
                      items={[
                        [tr('Message'), p.message || <span className="faint">{tr('None')}</span>],
                        [tr('Mode'), <span>{modeLabel(p.mode)} · <span className="muted small">{p.mode === 'enforce' ? tr('it refuses the launches its condition matches') : tr('it records the launches it would refuse, and lets them go')}</span></span>],
                      ]}
                    />
                  </div>
                </Panel>
                <Panel title={tr('Checks')} className="narrow">
                  <KV
                    items={[
                      [tr('Organization'), p.organization ? <Link to={`/organizations/${p.organization}`}>{p.summary_fields.organization?.name}</Link> : tr('None: a global policy checks every launch')],
                      ...(p.organization ? ([[tr('Attached to'), <AttachedList attached={p.attached ?? []} />]] as [string, ReactNode][]) : []),
                      [tr('Changed'), <Ago at={p.modified} />],
                    ]}
                  />
                </Panel>
              </div>
            }
          />
          <Route path="decisions" element={<PolicyDecisions path={`launch_policies/${id}/decisions/`} of="policy" />} />
          <Route
            path="backtest"
            element={
              <Panel title={tr('Backtest')}>
                <PolicyBacktest path={`launch_policies/${id}/backtest/`} />
              </Panel>
            }
          />
          <Route path="edit" element={<PolicyForm policy={p} />} />
          <Route path="access" element={<AccessTab resource="launch_policies" id={id} name={p.name} />} />
        </Routes>
      </div>
    </div>
  )
}

/** Runs a policy's condition, saved or drafted, over the past launches the
 * user can see, and lists the ones it would have refused. */
function PolicyBacktest({ path, draft }: { path: string; draft?: () => Record<string, unknown> }) {
  const [days, setDays] = useState('7')
  const [result, setResult] = useState<Any | null>(null)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const run = async () => {
    setBusy(true)
    setError(null)
    try {
      setResult(await api.post<Any>(path, { ...draft?.(), days: Number(days) }))
    } catch (e) {
      setError(e)
      setResult(null)
    } finally {
      setBusy(false)
    }
  }
  const launches = (result?.launches ?? []) as Any[]
  return (
    <div className="stack" style={{ gap: 12 }}>
      <div className="row" style={{ gap: 8, alignItems: 'flex-end' }}>
        <TextInput label={tr('Past days')} type="number" value={days} onChange={setDays} />
        <button type="button" className="btn" disabled={busy} onClick={run}>
          {busy ? tr('Running…') : tr('Run backtest')}
        </button>
      </div>
      <p className="small muted" style={{ margin: 0 }}>
        {tr('The condition runs on the launches you can see of the past days, up to 365, rebuilt from their jobs, with the user’s teams and the inventories’ hosts as they are today. It refuses nothing and records nothing.')}
      </p>
      <ErrorNote error={error} />
      {result && (
        <>
          <p style={{ margin: 0 }} role="status">
            {trn(result.refused, '{n} launch of {examined} would have been refused', '{n} launches of {examined} would have been refused', { examined: result.examined })}
            {result.errors ? tr(', {errors} of them because the condition failed', { errors: result.errors }) : ''}.
            {result.truncated ? ' ' + tr('Only the latest {count} launches were read.', { count: result.examined }) : ''}
          </p>
          {launches.length > 0 && (
            <DataTable
              rows={launches}
              label={tr('Launches it would have refused')}
              columns={[
                { key: 'when', header: tr('Launched'), render: (l) => <span title={absolute(l.created)}><Ago at={l.created} /></span> },
                { key: 'name', header: tr('Launch'), render: (l) => <Link to={jobPath({ id: l.id, type: l.type })}>{l.name}</Link> },
                { key: 'type', header: tr('Launch type'), render: (l) => l.launch_type },
                { key: 'user', header: tr('User'), render: (l) => l.user || '—' },
                { key: 'error', header: tr('Condition error'), render: (l) => (l.error ? <span className="small mono fail-text">{l.error}</span> : '') },
              ]}
            />
          )}
        </>
      )}
    </div>
  )
}

interface PolicyValues extends Record<string, unknown> {
  name: string
  description: string
  organization: Ref | null
  condition: string
  message: string
  mode: string
}

function PolicyForm({ policy }: { policy?: Policy }) {
  const nav = useNavigate()
  const me = useMe()
  const { org } = useOrg()
  const invalidate = useInvalidate()
  const f = useForm<PolicyValues>({
    name: policy?.name ?? '',
    description: policy?.description ?? '',
    organization: policy ? (policy.summary_fields.organization ?? null) : org,
    condition: policy?.condition ?? '',
    message: policy?.message ?? '',
    mode: policy?.mode ?? 'shadow',
  })
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !policy)
  const fields = useQuery({ queryKey: ['launch_policies/fields/'], queryFn: () => api.get<{ fields: PolicyField[] }>('launch_policies/fields/'), staleTime: Infinity })
  const tree = useMemo(() => (fields.data ? fieldsTree(fields.data.fields) : null), [fields.data])
  const complete = useMemo(() => policyCompletions(tree), [tree])
  const cond = useDebounced(f.values.condition, 400)
  const check = useQuery({
    queryKey: ['launch_policies/check/', cond],
    enabled: cond.trim() !== '',
    queryFn: () => api.post<{ valid: boolean; error: string }>('launch_policies/check/', { condition: cond }),
    staleTime: Infinity,
  })
  const checkError = cond.trim() && check.data && !check.data.valid && cond === f.values.condition ? check.data.error : ''
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    setError(null)
    const body = { ...f.values, organization: f.values.organization?.id ?? null }
    try {
      const saved = policy ? await api.patch<Policy>(`launch_policies/${policy.id}/`, body) : await api.post<Policy>('launch_policies/', body)
      invalidate('launch_policies')
      nav(`/launch-policies/${saved.id}`)
    } catch (e) {
      setError(e)
      if (e instanceof ApiError) f.setErrors(e.fields)
    } finally {
      setBusy(false)
    }
  }
  const global = !f.values.organization
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
        <TextInput label={tr('Name')} value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} autoFocus={!policy} />
        <Lookup
          label={tr('Organization')}
          optional={me.is_superuser}
          resource="organizations/"
          value={f.values.organization}
          onChange={(v) => f.set('organization', v)}
          error={f.errors.organization}
          help={global ? tr('None: a global policy, which checks every launch. Only superusers manage them.') : tr('It checks the launches of what it is attached to in this organization.')}
        />
        <SelectField
          label={tr('Mode')}
          value={f.values.mode}
          onChange={(v) => f.set('mode', v)}
          options={MODES}
          error={f.errors.mode}
          help={f.values.mode === 'enforce' ? tr('It refuses the launches its condition matches.') : tr('It records the launches it would refuse, and lets them go: try a policy in shadow mode first.')}
        />
        <TextArea label={tr('Description')} optional rows={2} value={f.values.description} onChange={(v) => f.set('description', v)} />
      </div>
      <section className="form-section">
        <h2>{tr('Refuses a launch when')}</h2>
        {!policy && (
          <div className="row" style={{ gap: 6, flexWrap: 'wrap' }}>
            <span className="small muted">{tr('Start from an example:')}</span>
            {POLICY_EXAMPLES.map((x) => (
              <button
                key={x.name}
                type="button"
                className="btn sm ghost"
                onClick={() => {
                  f.set('condition', x.condition)
                  f.set('message', x.message)
                  if (!f.values.name.trim()) f.set('name', x.name)
                }}
              >
                {x.name}
              </button>
            ))}
          </div>
        )}
        <CelInput
          label={tr('Condition')}
          value={f.values.condition}
          onChange={(v) => f.set('condition', v)}
          schema={null}
          complete={complete}
          rows={3}
          error={f.errors.condition || checkError}
          placeholder={asIs('launch.inventory.name == "prod" && launch.limit == ""')}
          help={
            check.data?.valid && cond === f.values.condition ? (
              <span className="ok-text">{tr('The condition checks: it gives true or false.')}</span>
            ) : (
              tr('A CEL expression over launch and now; Ctrl+Space completes. True refuses the launch.')
            )
          }
        />
        <TextArea label={tr('Message')} rows={2} value={f.values.message} onChange={(v) => f.set('message', v)} error={f.errors.message} help={tr('What a refused launch says: why, and what to do instead.')} />
        {fields.data && <FieldReference fields={fields.data.fields} />}
      </section>
      <section className="form-section">
        <h2>{tr('Backtest')}</h2>
        <PolicyBacktest
          path={policy ? `launch_policies/${policy.id}/backtest/` : 'launch_policies/backtest/'}
          draft={() => ({ condition: f.values.condition, ...(!policy && f.values.organization ? { organization: f.values.organization.id } : {}) })}
        />
      </section>
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !f.values.name.trim() || !f.values.condition.trim() || (global && !me.is_superuser)}>
          {busy ? tr('Saving…') : policy ? tr('Save changes') : tr('Create policy')}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(policy ? `/launch-policies/${policy.id}` : '/launch-policies')}>
          {tr('Cancel')}
        </button>
      </div>
    </form>
  )
}

/** The fields a condition reads, with their types and meaning. */
function FieldReference({ fields }: { fields: PolicyField[] }) {
  return (
    <details className="small">
      <summary>{tr('What a condition reads')}</summary>
      <div className="stack" style={{ gap: 10, marginTop: 8 }}>
        <p className="muted" style={{ margin: 0 }}>
          {tr('Every field is there, empty when it doesn’t apply: "" for a text, 0 for an id or a number. window(now, "Mon-Thu 20:00-06:00", "Europe/Zurich") is true inside the window.')}
        </p>
        {fieldGroups(fields).map((g) => (
          <table key={g.group || 'other'} className="data small">
            <tbody>
              {g.fields.map((x) => (
                <tr key={x.path}>
                  <td className="mono" style={{ width: '38%' }}>
                    {x.path}
                  </td>
                  <td className="mono muted" style={{ width: '16%' }}>
                    {x.type}
                  </td>
                  <td>{x.help}</td>
                </tr>
              ))}
            </tbody>
          </table>
        ))}
      </div>
    </details>
  )
}
