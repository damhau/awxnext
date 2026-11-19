// A project's rulebooks of ansible-rulebook, imported into event streams,
// connectors and rules, and the report of each import (roadmap M14-09).
import { useState, type FormEvent } from 'react'
import { Link, useNavigate, useParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Any, Base } from '../../api/types'
import { FormError, Lookup, TextArea, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Cell, Empty, ErrorNote, KV, Loading, PageHead, Panel } from '../../components/ui'
import { asIs, tr, trn } from '../../lib/i18n'
import {
  mappingRows,
  nextSteps,
  objectPath,
  parseVariables,
  ruleReasons,
  shortName,
  sourceStreamsBody,
  statusView,
  summaryText,
  variablesText,
  type ActionReport,
  type MappingRow,
  type ReportObject,
  type RulebookReport,
  type RuleReport,
  type RulebookSource,
  type RulesetReport,
  type SourceReport,
} from '../../lib/rulebooks'
import { useDelete } from '../admin/common'
import { applyErrors } from './common'
import { useProject } from './projects'

export interface Rulebook extends Base {
  project: number
  organization: number
  path: string
  rulesets: string
  variables: Record<string, unknown>
  source_streams: Record<string, number>
  revision: string
  imported: string | null
  import_error: string
  report: RulebookReport
}

/** The rules of a rulebook as cells, in rulebook order. */
function RuleCells({ report }: { report: RulebookReport }) {
  const rules = (report.rulesets ?? []).flatMap((rs) => rs.rules.map((r) => ({ rs: rs.name, r })))
  if (!rules.length) return <span className="faint small">{tr('No rules')}</span>
  return (
    <span className="runbars" role="group" aria-label={summaryText(report.summary)}>
      {rules.map(({ rs, r }, i) => (
        <Cell key={i} state={statusView(r.status).cell} size="bar" label={tr('{ruleset} / {rule}: {status}', { ruleset: rs, rule: r.name, status: statusView(r.status).label })} />
      ))}
    </span>
  )
}

export function ProjectRulebooksTab() {
  const p = useProject()
  const ls = useListState({ order: 'name' })
  const nav = useNavigate()
  const toast = useToast()
  const invalidate = useInvalidate()
  const [busy, setBusy] = useState(false)
  const list = useList<Rulebook>(`projects/${p.id}/rulebooks/`, { order_by: ls.order, page: ls.page, page_size: ls.pageSize })
  const canEdit = !!p.summary_fields.user_capabilities?.edit
  const importAll = async () => {
    setBusy(true)
    try {
      const res = await api.post<{ count: number }>(`projects/${p.id}/import_rulebooks/`, {})
      invalidate(`projects/${p.id}/rulebooks/`, 'rulebooks')
      toast(trn(res.count, 'Imported {n} rulebook', 'Imported {n} rulebooks'))
    } catch (e) {
      toast((e as Error).message, 'error')
    } finally {
      setBusy(false)
    }
  }
  const cols: Column<Rulebook>[] = [
    {
      key: 'name',
      header: tr('Rulebook'),
      sort: 'name',
      render: (r) => (
        <div>
          <Link to={`/rulebooks/${r.id}`} className="cell-title">
            {r.name}
          </Link>
          <div className="cell-sub mono">{r.path || tr('uploaded')}</div>
        </div>
      ),
    },
    { key: 'rules', header: tr('Rules'), render: (r) => (r.import_error ? <span className="small" style={{ color: 'var(--fail)' }}>{tr('Can’t be read')}</span> : <RuleCells report={r.report} />) },
    { key: 'summary', header: tr('Translated'), render: (r) => <span className="small">{summaryText(r.report.summary)}</span> },
    { key: 'imported', header: tr('Imported'), sort: 'imported', render: (r) => <span className="small nowrap">{r.imported ? <Ago at={r.imported} /> : <span className="faint">{tr('Never')}</span>}</span> },
  ]
  return (
    <div className="page-body">
      <div className="row" style={{ marginBottom: 12, gap: 8 }}>
        <p className="small muted" style={{ margin: 0 }}>
          {p.scm_type
            ? tr('Each sync imports the rulebooks in extensions/eda/rulebooks/ and rulebooks/ into event streams, connectors and rules, which start disabled.')
            : tr('Import again reads the rulebooks in extensions/eda/rulebooks/ and rulebooks/ of the project’s directory into event streams, connectors and rules, which start disabled.')}
        </p>
        {canEdit && (
          <div className="row" style={{ marginLeft: 'auto', gap: 8 }}>
            <button className="btn" disabled={busy} onClick={importAll}>
              <Icon name="refresh" size={14} />
              {tr('Import again')}
            </button>
            <Link className="btn primary" to={`/projects/${p.id}/rulebooks/new`}>
              <Icon name="plus" size={14} />
              {tr('Upload a rulebook')}
            </Link>
          </div>
        )}
      </div>
      <DataTable
        label={tr('Rulebooks')}
        rows={list.data?.results}
        columns={cols}
        loading={list.isPending}
        error={list.error}
        order={ls.order}
        onOrder={ls.setOrder}
        onOpen={(r) => nav(`/rulebooks/${r.id}`)}
        empty={<Empty title={tr('No rulebooks')}>{p.scm_type ? tr('Add rulebooks to the project’s repository and sync it, or upload one.') : tr('Upload a rulebook to import it.')}</Empty>}
        footer={
          <div className="table-foot">
            <span>{list.data ? trn(list.data.count, '{n} rulebook', '{n} rulebooks') : ''}</span>
            <Pager count={list.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
          </div>
        }
      />
    </div>
  )
}

/** The names of the event streams with these ids, those the user can see. */
function useStreamNames(ids: number[]): Record<number, string> {
  const list = useList<Any>(ids.length ? 'event_streams/' : null, { id__in: [...ids].sort((a, b) => a - b).join(','), page_size: 200 })
  return Object.fromEntries((list.data?.results ?? []).map((o) => [o.id, o.name]))
}

function SourceMappings({ rows, onChange, error }: { rows: MappingRow[]; onChange: (rows: MappingRow[]) => void; error?: string }) {
  const known = useStreamNames(rows.flatMap((r) => (r.stream === null ? [] : [r.stream])))
  const [picked, setPicked] = useState<Record<number, string>>({})
  const names = { ...known, ...picked }
  return (
    <section className="form-section">
      <h2>{tr('Source mappings')}</h2>
      <p className="small muted" style={{ margin: 0 }}>
        {tr('A source mapped to an event stream doesn’t run: its rules get the stream’s events instead, as with EDA’s source mappings.')}
      </p>
      {rows.length === 0 ? (
        <span className="faint small">{tr('The rulebook has no sources.')}</span>
      ) : (
        <div className="form-grid">
          {rows.map((r, i) => (
            <Lookup
              key={r.name}
              label={
                <>
                  <span className="mono">{r.name}</span>
                  {r.plugin && <span className="faint small"> {r.plugin}</span>}
                </>
              }
              optional
              resource="event_streams/"
              value={r.stream === null ? null : { id: r.stream, name: names[r.stream] ?? asIs(`#${r.stream}`) }}
              onChange={(ref) => {
                if (ref) setPicked((p) => ({ ...p, [ref.id]: ref.name }))
                onChange(rows.map((x, j) => (j === i ? { ...x, stream: ref?.id ?? null } : x)))
              }}
              help={r.stale ? tr('No longer a source of the rulebook: the mapping does nothing.') : undefined}
            />
          ))}
        </div>
      )}
      {error && <div className="form-error">{error}</div>}
    </section>
  )
}

function MappedStreams({ mapped }: { mapped: Record<string, number> }) {
  const names = useStreamNames(Object.values(mapped))
  return (
    <ul className="list-plain small">
      {Object.entries(mapped).map(([source, id]) => (
        <li key={source}>
          <span className="mono">{source}</span> → <Link to={`/event-streams/${id}`}>{names[id] ?? asIs(`#${id}`)}</Link>
        </li>
      ))}
    </ul>
  )
}

/** The form of an uploaded rulebook, or of a file's variables and source mappings. */
function RulebookForm({ project, rb }: { project: number; rb?: Rulebook }) {
  const nav = useNavigate()
  const invalidate = useInvalidate()
  const file = !!rb?.path
  const { values: v, set, errors, setErrors } = useForm({ name: rb?.name ?? '', rulesets: rb?.rulesets ?? '', variables: variablesText(rb?.variables) })
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const parsed = parseVariables(v.variables)
  const sources = useList<RulebookSource>(rb ? `/api/eda/v1/rulebooks/${rb.id}/sources/` : null, { page_size: 200 })
  const [mappings, setMappings] = useState<MappingRow[] | null>(null)
  const rows = mappings ?? (sources.data ? mappingRows(sources.data.results, rb?.source_streams) : null)
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (parsed.error) return
    setBusy(true)
    setError(null)
    try {
      const body: Record<string, unknown> = { variables: parsed.vars }
      if (!file) Object.assign(body, { name: v.name, rulesets: v.rulesets })
      if (mappings) body.source_streams = sourceStreamsBody(mappings)
      const saved = rb ? await api.patch<Rulebook>(`rulebooks/${rb.id}/`, body) : await api.post<Rulebook>('rulebooks/', { ...body, project })
      invalidate('rulebooks', `projects/${project}/rulebooks/`, 'event_rules', 'event_streams', 'event_connectors')
      nav(`/rulebooks/${saved.id}`)
    } catch (err) {
      setError(applyErrors(err, setErrors))
    } finally {
      setBusy(false)
    }
  }
  return (
    <form className="form" onSubmit={submit}>
      <div className="form-grid">
        {!file && <TextInput label={tr('Name')} value={v.name} onChange={(x) => set('name', x)} error={errors.name} autoFocus={!rb} placeholder={asIs('alertmanager.yml')} />}
        {!file && <TextArea label={tr('Rulebook')} mono rows={18} value={v.rulesets} onChange={(x) => set('rulesets', x)} error={errors.rulesets} help={tr('The YAML of ansible-rulebook’s rulesets. Saving imports it.')} />}
        <TextArea
          label={tr('Variables')}
          optional
          mono
          rows={6}
          value={v.variables}
          onChange={(x) => set('variables', x)}
          error={parsed.error ?? errors.variables}
          help={tr('As ansible-rulebook’s --vars, in YAML or JSON: vars.name in conditions, {{ name }} in the arguments of sources and actions.')}
        />
      </div>
      {rows && <SourceMappings rows={rows} onChange={setMappings} error={errors.source_streams} />}
      <FormError error={error} />
      <div className="form-actions">
        <button className="btn primary" disabled={busy || !!parsed.error || (!file && (!v.name || !v.rulesets.trim()))}>
          {busy ? tr('Importing…') : rb ? tr('Save and import') : tr('Upload and import')}
        </button>
        <button type="button" className="btn ghost" onClick={() => nav(-1)}>
          {tr('Cancel')}
        </button>
      </div>
    </form>
  )
}

export function RulebookNew() {
  const { id } = useParams()
  const proj = useObject<Any>(`projects/${id}/`)
  if (proj.error) return <div className="page-body"><ErrorNote error={proj.error} /></div>
  if (!proj.data) return <Loading />
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Projects'), to: '/projects' }, { label: proj.data.name, to: `/projects/${id}/rulebooks` }, { label: tr('Upload a rulebook') }]}
        title={tr('Upload a rulebook')}
        subtitle={tr('The import makes event streams, connectors and rules, all disabled, and reports what doesn’t translate.')}
      />
      <div className="page-body">
        <RulebookForm project={Number(id)} />
      </div>
    </div>
  )
}

export function RulebookEdit() {
  const { id } = useParams()
  const rb = useObject<Rulebook>(`rulebooks/${id}/`)
  if (rb.error) return <div className="page-body"><ErrorNote error={rb.error} /></div>
  if (!rb.data) return <Loading />
  const r = rb.data
  return (
    <div className="page">
      <PageHead
        crumbs={[
          { label: tr('Projects'), to: '/projects' },
          { label: r.summary_fields.project?.name ?? asIs(`#${r.project}`), to: `/projects/${r.project}/rulebooks` },
          { label: r.name, to: `/rulebooks/${r.id}` },
          { label: tr('Edit') },
        ]}
        title={tr('Edit {name}', { name: r.name })}
        subtitle={r.path ? tr('The rulebook is {path} of its project: change it there, and sync the project.', { path: r.path }) : undefined}
      />
      <div className="page-body">
        <RulebookForm project={r.project} rb={r} />
      </div>
    </div>
  )
}

function ObjectLink({ o }: { o?: ReportObject }) {
  const to = objectPath(o)
  if (!o || !to) return null
  return (
    <Link to={to} className="small">
      {shortName(o.name)}
    </Link>
  )
}

function Status({ status }: { status: string }) {
  const v = statusView(status)
  return (
    <span className="status">
      <Cell state={v.cell} />
      <span>{v.label}</span>
    </span>
  )
}

function Lines({ items, tone }: { items?: string[]; tone?: 'muted' }) {
  if (!items?.length) return null
  return (
    <ul className={`list-plain small${tone ? ' muted' : ''}`} style={{ gap: 2, margin: '4px 0 0' }}>
      {items.map((t, i) => (
        <li key={i} style={{ justifyContent: 'flex-start' }}>
          {t}
        </li>
      ))}
    </ul>
  )
}

const asLabel = (a: ActionReport) => (a.as === 'decisions' ? tr('recorded by the rule’s decisions') : a.as ? tr('as {kind}', { kind: a.as }) : '')

function RuleRow({ r }: { r: RuleReport }) {
  const reasons = ruleReasons(r)
  return (
    <li className="rulebook-rule">
      <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
        <Status status={r.status} />
        <strong>{r.name}</strong>
      </div>
      <ul className="list-plain small" style={{ gap: 2, margin: '4px 0 0' }}>
        {r.actions.map((a, i) => (
          <li key={i} style={{ justifyContent: 'flex-start', gap: 8 }}>
            <Cell state={statusView(a.status).cell} label={statusView(a.status).label} />
            <span className="mono">{a.action}</span>
            {a.status === 'translated' && <span className="faint">{asLabel(a)}</span>}
            <ObjectLink o={a.object} />
          </li>
        ))}
      </ul>
      {reasons.length > 0 && <Lines items={reasons} />}
      <Lines items={r.notes} tone="muted" />
    </li>
  )
}

function SourceRow({ s }: { s: SourceReport }) {
  return (
    <li className="rulebook-rule">
      <div className="row" style={{ gap: 8, flexWrap: 'wrap' }}>
        <Status status={s.status} />
        <span className="mono">{s.plugin}</span>
        {s.name !== s.plugin && <span className="faint small">{s.name}</span>}
        <ObjectLink o={s.object} />
      </div>
      <Lines items={s.reasons} />
      <Lines items={s.notes} tone="muted" />
    </li>
  )
}

function RulesetPanel({ rs }: { rs: RulesetReport }) {
  return (
    <Panel
      title={tr('Ruleset {name}', { name: rs.name })}
      actions={
        rs.streams?.length ? (
          <span className="small">
            {rs.streams.map((o) => (
              <ObjectLink key={o.id} o={o} />
            ))}
          </span>
        ) : undefined
      }
    >
      <div className="stack" style={{ gap: 12 }}>
        <Lines items={rs.notes} tone="muted" />
        {rs.sources.length > 0 && (
          <div>
            <h3 className="small muted" style={{ margin: '0 0 4px' }}>
              {tr('Sources')}
            </h3>
            <ul className="list-plain rulebook-list">
              {rs.sources.map((s, i) => (
                <SourceRow key={i} s={s} />
              ))}
            </ul>
          </div>
        )}
        <div>
          <h3 className="small muted" style={{ margin: '0 0 4px' }}>
            {tr('Rules')}
          </h3>
          {rs.rules.length ? (
            <ul className="list-plain rulebook-list">
              {rs.rules.map((r, i) => (
                <RuleRow key={i} r={r} />
              ))}
            </ul>
          ) : (
            <span className="faint small">{tr('No rules')}</span>
          )}
        </div>
      </div>
    </Panel>
  )
}

export function RulebookDetail() {
  const { id } = useParams()
  const rb = useObject<Rulebook>(`rulebooks/${id}/`)
  const streams = useList<Any>(`rulebooks/${id}/event_streams/`, { page_size: 200 })
  const connectors = useList<Any>(`rulebooks/${id}/event_connectors/`, { page_size: 200 })
  const rules = useList<Any>(`rulebooks/${id}/event_rules/`, { page_size: 200 })
  const del = useDelete()
  const toast = useToast()
  const invalidate = useInvalidate()
  const [busy, setBusy] = useState(false)
  if (rb.error) return <div className="page-body"><ErrorNote error={rb.error} /></div>
  if (!rb.data) return <Loading />
  const r = rb.data
  const caps = r.summary_fields.user_capabilities ?? {}
  const projectName = r.summary_fields.project?.name ?? asIs(`#${r.project}`)
  const steps = nextSteps([
    ...(streams.data?.results ?? []).map((o) => ({ type: 'event_stream', credential: o.credential })),
    ...(connectors.data?.results ?? []).map((o) => ({ type: 'event_connector', enabled: o.enabled })),
    ...(rules.data?.results ?? []).map((o) => ({ type: 'event_rule', enabled: o.enabled })),
  ])
  const reimport = async () => {
    setBusy(true)
    try {
      await api.post(`rulebooks/${r.id}/import/`, {})
      invalidate('rulebooks', 'event_rules', 'event_streams', 'event_connectors')
      toast(tr('Imported {name}', { name: r.name }))
    } catch (e) {
      toast((e as Error).message, 'error')
    } finally {
      setBusy(false)
    }
  }
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Projects'), to: '/projects' }, { label: projectName, to: `/projects/${r.project}/rulebooks` }, { label: r.name }]}
        title={r.name}
        subtitle={r.import_error ? undefined : summaryText(r.report.summary)}
        actions={
          <>
            {caps.edit && (
              <button className="btn" disabled={busy} onClick={reimport}>
                <Icon name="refresh" size={14} />
                {tr('Import again')}
              </button>
            )}
            {caps.edit && (
              <Link className="btn" to={`/rulebooks/${r.id}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {caps.delete && !r.path && (
              <button className="btn danger" onClick={() => del({ path: `rulebooks/${r.id}/`, name: r.name, what: tr('rulebook'), body: <p style={{ margin: 0 }}>{tr('Its streams, connectors and rules are deleted with it.')}</p>, then: `/projects/${r.project}/rulebooks`, invalidate: ['rulebooks', 'event_rules', 'event_streams', 'event_connectors'] })}>
                {tr('Delete')}
              </button>
            )}
          </>
        }
      />
      <div className="page-body">
        {r.import_error && <div className="notice warn" style={{ marginBottom: 16 }}>{tr('The last import couldn’t read the rulebook: {error} The objects of the import before stay as they were.', { error: r.import_error })}</div>}
        <div className="split">
          <div className="wide stack" style={{ gap: 16 }}>
            {steps.length > 0 && (
              <Panel title={tr('To turn it on')}>
                <ol className="small" style={{ margin: 0, paddingLeft: 18 }}>
                  {steps.map((s, i) => (
                    <li key={i}>{s}</li>
                  ))}
                </ol>
              </Panel>
            )}
            {(r.report.rulesets ?? []).map((rs, i) => (
              <RulesetPanel key={i} rs={rs} />
            ))}
            {!r.report.rulesets?.length && !r.import_error && <Empty title={tr('Nothing imported yet')} />}
          </div>
          <div className="narrow stack" style={{ gap: 16 }}>
            <Panel title={tr('About')}>
              <KV
                items={[
                  [tr('Project'), <Link to={`/projects/${r.project}/rulebooks`}>{projectName}</Link>],
                  [tr('File'), r.path ? <span className="mono">{r.path}</span> : <span className="faint">{tr('Uploaded')}</span>],
                  [tr('Revision'), r.revision ? <span className="mono" title={r.revision}>{r.revision.slice(0, 10)}</span> : false],
                  [tr('Imported'), r.imported ? <Ago at={r.imported} /> : <span className="faint">{tr('Never')}</span>],
                  [tr('Variables'), Object.keys(r.variables ?? {}).length ? <pre className="code">{variablesText(r.variables)}</pre> : <span className="faint">{tr('None')}</span>],
                  [tr('Source mappings'), Object.keys(r.source_streams ?? {}).length ? <MappedStreams mapped={r.source_streams} /> : <span className="faint">{tr('None')}</span>],
                ]}
              />
            </Panel>
            <Panel title={tr('Made by the import')}>
              <KV
                items={[
                  [tr('Event streams'), streams.data?.count ?? '…'],
                  [tr('Event connectors'), connectors.data?.count ?? '…'],
                  [tr('Event rules'), rules.data?.count ?? '…'],
                ]}
              />
            </Panel>
            <Panel title={tr('Rulebook')} pad={false}>
              <pre className="code rulebook-yaml">{r.rulesets}</pre>
            </Panel>
          </div>
        </div>
      </div>
    </div>
  )
}
