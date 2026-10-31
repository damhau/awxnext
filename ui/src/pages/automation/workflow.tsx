// Workflow graph: editor for a workflow job template, live view for a workflow job.
import { useQuery, useQueryClient } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { Link, useNavigate } from 'react-router'
import { api, ApiError, getAll } from '../../api/client'
import { useObject } from '../../api/hooks'
import { useLive } from '../../api/live'
import type { Any, Ref } from '../../api/types'
import { Check, Field, FormError, Lookup, parseVars, TextInput, VarsEditor, varsText } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, useConfirm, useToast } from '../../components/overlay'
import { Empty, Loading, StatusDot } from '../../components/ui'
import { duration, statusKind, statusLabel, templatePath } from '../../lib/format'
import { useHotkeys } from '../../lib/hotkeys'
import { addToCondition, outputSuggestions, type OutputDecl, type OutputSuggestion } from '../../lib/outputs'
import './automation.css'
import { edgePath, layout, type Box, type EdgeKind, type GraphNode, type Layout } from './layout'
import { itemLabel, loopProgress, type Iteration, type LoopProgress } from './loops'
import { SavedLabelsAndGroupsFields, useSavedLabelsAndGroups } from './SavedLabelsAndGroups'
import { PromptField, PROMPTS, SurveyField, useTemplatePrompts } from './ScheduleForm'
import { tr, trn } from '../../lib/i18n'

const KINDS: EdgeKind[] = ['success', 'failure', 'always']
const KIND_LABEL: Record<EdgeKind, string> = { success: 'on success', failure: 'on failure', always: 'always' }
const EDGE_STYLE: Record<EdgeKind | 'start', { stroke: string; dash?: string; width: number }> = {
  success: { stroke: 'var(--ok)', width: 1.6 },
  failure: { stroke: 'var(--fail)', dash: '6 4', width: 1.6 },
  always: { stroke: 'var(--faint)', dash: '1.5 3.5', width: 1.9 },
  start: { stroke: 'var(--line-strong)', width: 1.3 },
}

const NODE_TYPE: Record<string, string> = {
  job_template: tr('Playbook'),
  workflow_job_template: tr('Workflow'),
  project: tr('Project sync'),
  inventory_source: tr('Inventory sync'),
  workflow_approval_template: tr('Approval'),
  system_job_template: tr('Management job'),
}

const NODE_TEMPLATE_TYPES = 'job_template,workflow_job_template,project,inventory_source,system_job_template'

interface WfNode {
  id: number
  identifier: string
  unified_job_template: number | null
  success_nodes: number[]
  failure_nodes: number[]
  always_nodes: number[]
  all_parents_must_converge: boolean
  do_not_run?: boolean
  job?: number | null
  extra_data?: Record<string, unknown>
  when?: string
  loop?: string
  loop_var?: string
  loop_concurrency?: number
  skip_reason?: string
  loop_items?: unknown[] | null
  iterations?: Iteration[]
  summary_fields: Any
  [k: string]: unknown
}

function toGraph(nodes: WfNode[]): GraphNode[] {
  return nodes.map((n) => ({
    id: n.id,
    edges: KINDS.flatMap((k) => (n[`${k}_nodes`] as number[]).map((to) => ({ to, kind: k }))),
  }))
}

function nodeName(n: WfNode): string {
  return n.summary_fields?.unified_job_template?.name ?? (n.unified_job_template ? `Template #${n.unified_job_template}` : tr('No template'))
}

function nodeType(n: WfNode): string {
  return n.summary_fields?.unified_job_template?.type ?? ''
}

/** Outputs of earlier steps, as chips that put their reference in a condition or a loop. */
function OutputChips({ items, onPick }: { items: OutputSuggestion[]; onPick: (ref: string) => void }) {
  if (!items.length) return null
  return (
    <div className="row wrap" style={{ gap: 4, marginTop: -6 }}>
      <span className="small muted">{tr('Outputs of earlier steps:')}</span>
      {items.map((s) => (
        <button key={s.ref} type="button" className="chip" style={{ cursor: 'pointer' }} title={tr('Insert {ref}', { ref: s.ref })} onClick={() => onPick(s.ref)}>
          {s.step}.{s.output.name}
          <span className="faint">
            {s.output.type}
            {s.output.required ? '' : '?'}
          </span>
        </button>
      ))}
    </div>
  )
}

/** Identifiers the API generated (UUIDs) carry no meaning worth showing on the graph. */
function isGenerated(identifier: string): boolean {
  return /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i.test(identifier)
}

/** One-line summary of a node's condition, loop and the prompts it overrides. */
function promptSummary(n: WfNode): string {
  const parts: string[] = []
  if (n.when) parts.push(`if ${n.when}`)
  if (n.loop) parts.push(`each of ${n.loop}`)
  if (n.limit) parts.push(`limit ${n.limit}`)
  if (n.scm_branch) parts.push(`branch ${n.scm_branch}`)
  if (n.job_tags) parts.push(`tags ${n.job_tags}`)
  if (n.inventory) parts.push('inventory set')
  const extra = Object.keys(n.extra_data ?? {})
  if (extra.length) parts.push(`${extra.length} var${extra.length === 1 ? '' : 's'}`)
  return parts.join(' · ')
}

// ---------- canvas ----------

function EdgeLayer({ lay, nodes, highlight, dim }: { lay: Layout; nodes: WfNode[]; highlight?: number | null; dim?: (to: number) => boolean }) {
  const roots = nodes.filter((n) => (lay.parents.get(n.id) ?? []).length === 0)
  const edges: { from: Box; to: Box; kind: EdgeKind | 'start'; key: string; hot: boolean; faded: boolean; lane?: number }[] = []
  for (const r of roots) {
    const b = lay.boxes.get(r.id)
    if (b) edges.push({ from: lay.start, to: b, kind: 'start', key: `s-${r.id}`, hot: highlight === r.id, faded: !!dim?.(r.id) })
  }
  for (const n of nodes) {
    for (const k of KINDS) {
      for (const to of n[`${k}_nodes`] as number[]) {
        const a = lay.boxes.get(n.id)
        const b = lay.boxes.get(to)
        if (a && b) edges.push({ from: a, to: b, kind: k, key: `${n.id}-${k}-${to}`, hot: highlight === n.id || highlight === to, faded: !!dim?.(to), lane: lay.lanes.get(`${n.id}-${to}`) })
      }
    }
  }
  return (
    <svg className="wf-edges" width={lay.width} height={lay.height} aria-hidden="true">
      <defs>
        {(['success', 'failure', 'always', 'start'] as const).map((k) => (
          <marker key={k} id={`wf-arrow-${k}`} viewBox="0 0 8 8" refX="7" refY="4" markerWidth="9" markerHeight="9" markerUnits="userSpaceOnUse" orient="auto-start-reverse">
            <path d="M0,0 L8,4 L0,8 z" style={{ fill: EDGE_STYLE[k].stroke }} />
          </marker>
        ))}
      </defs>
      {edges.map((e) => {
        const p = edgePath(e.from, e.to, e.lane)
        const s = EDGE_STYLE[e.kind]
        return (
          <g key={e.key} opacity={e.faded ? 0.3 : 1}>
            <path
              d={p.d}
              fill="none"
              style={{ stroke: s.stroke }}
              strokeWidth={e.hot ? s.width + 1.2 : s.width}
              strokeDasharray={s.dash}
              strokeLinecap="round"
              markerEnd={`url(#wf-arrow-${e.kind})`}
            />
            {(e.kind === 'failure' || e.kind === 'always') && (
              <text className="wf-edge-label" x={p.mx} y={p.my - 5} textAnchor="middle">
                {e.kind === 'failure' ? tr('fail') : tr('always')}
              </text>
            )}
          </g>
        )
      })}
    </svg>
  )
}

function Legend({ extra }: { extra?: ReactNode }) {
  const sample = (k: EdgeKind) => (
    <svg width="28" height="8" aria-hidden="true">
      <line x1="1" y1="4" x2="27" y2="4" style={{ stroke: EDGE_STYLE[k].stroke }} strokeWidth={EDGE_STYLE[k].width + 0.4} strokeDasharray={EDGE_STYLE[k].dash} strokeLinecap="round" />
    </svg>
  )
  return (
    <div className="wf-legend">
      <span>{sample('success')} {tr('on success')}</span>
      <span>{sample('failure')} {tr('on failure')}</span>
      <span>{sample('always')} {tr('always')}</span>
      {extra}
    </div>
  )
}

/** Zoom that defaults to fitting the graph into the available width. */
function useZoom(lay: Layout) {
  // A callback ref: the measured element mounts after loading finishes.
  const [el, ref] = useState<HTMLDivElement | null>(null)
  const [width, setWidth] = useState(0)
  const [manual, setManual] = useState<number | null>(null)
  useEffect(() => {
    if (!el) return
    const ro = new ResizeObserver(() => setWidth(el.clientWidth))
    ro.observe(el)
    setWidth(el.clientWidth)
    return () => ro.disconnect()
  }, [el])
  const fit = width ? Math.max(0.75, Math.min(1, Math.floor(((width - 4) / lay.width) * 20) / 20)) : 1
  return { ref, zoom: manual ?? fit, fitted: manual === null, setZoom: setManual }
}

function ZoomControls({ zoom, fitted, setZoom }: { zoom: number; fitted: boolean; setZoom: (z: number | null) => void }) {
  return (
    <div className="row" style={{ gap: 4 }}>
      <button className="btn sm" aria-pressed={fitted} onClick={() => setZoom(null)} title={tr('Fit the graph to the width')}>
        {tr('Fit')}
      </button>
      <button className="btn sm icon" aria-label={tr('Zoom out')} disabled={zoom <= 0.5} onClick={() => setZoom(Math.max(0.5, Math.round((zoom - 0.1) * 10) / 10))}>
        −
      </button>
      <span className="mono small" style={{ minWidth: 40, textAlign: 'center' }} aria-live="polite">
        {Math.round(zoom * 100)}%
      </span>
      <button className="btn sm icon" aria-label={tr('Zoom in')} disabled={zoom >= 1.4} onClick={() => setZoom(Math.min(1.4, Math.round((zoom + 0.1) * 10) / 10))}>
        +
      </button>
    </div>
  )
}

function Canvas({
  lay,
  nodes,
  zoom,
  highlight,
  dim,
  start,
  renderNode,
  onKeyDown,
  label,
  short,
}: {
  lay: Layout
  nodes: WfNode[]
  zoom: number
  highlight?: number | null
  dim?: (id: number) => boolean
  start: ReactNode
  renderNode: (n: WfNode, box: Box) => ReactNode
  onKeyDown?: (e: KeyboardEvent<HTMLDivElement>) => void
  label: string
  short?: boolean
}) {
  return (
    <div
      className={`wf-canvas ${short ? 'short' : ''}`}
      role="group"
      aria-label={label}
      onKeyDown={onKeyDown}
      style={{ height: Math.max(220, Math.min(short ? 560 : 620, lay.height * zoom + 2)) }}
    >
      <div style={{ width: lay.width * zoom, height: lay.height * zoom, position: 'relative' }}>
        <div className="wf-inner" style={{ width: lay.width, height: lay.height, transform: `scale(${zoom})` }}>
          <EdgeLayer lay={lay} nodes={nodes} highlight={highlight} dim={dim} />
          <div className="wf-node start" style={{ left: lay.start.x, top: lay.start.y, width: lay.start.w, height: lay.start.h }}>
            {start}
          </div>
          {nodes.map((n) => {
            const b = lay.boxes.get(n.id)
            return b ? <div key={n.id}>{renderNode(n, b)}</div> : null
          })}
        </div>
      </div>
    </div>
  )
}

/** Arrow-key movement between nodes of a layout. */
function neighbour(lay: Layout, nodes: WfNode[], id: number, key: string): number | null {
  const d = lay.depth.get(id)
  if (d === undefined) return null
  const col = lay.columns[d]
  const i = col.indexOf(id)
  const node = nodes.find((n) => n.id === id)
  switch (key) {
    case 'ArrowDown':
      return col[i + 1] ?? null
    case 'ArrowUp':
      return col[i - 1] ?? null
    case 'ArrowRight': {
      const kids = node ? KINDS.flatMap((k) => node[`${k}_nodes`] as number[]) : []
      return kids.sort((a, b) => (lay.boxes.get(a)?.y ?? 0) - (lay.boxes.get(b)?.y ?? 0))[0] ?? null
    }
    case 'ArrowLeft':
      return (lay.parents.get(id) ?? [])[0] ?? null
  }
  return null
}

// ---------- editor ----------

export function WorkflowVisualizer({ templateId, editable }: { templateId: number; editable: boolean }) {
  const qc = useQueryClient()
  const toast = useToast()
  const confirm = useConfirm()
  const key = `workflow_job_templates/${templateId}/workflow_nodes/`
  const wf = useObject<Any>(`workflow_job_templates/${templateId}/`)
  const q = useQuery({ queryKey: [key, 'all'], queryFn: () => getAll<WfNode>(key, { order_by: 'id' }) })
  const nodes = useMemo(() => q.data ?? [], [q.data])
  const lay = useMemo(() => layout(toGraph(nodes), { nodeH: 70 }), [nodes])
  const [selected, setSelected] = useState<number | 'start' | null>(null)
  const [adding, setAdding] = useState<{ parent: number | null; kind: EdgeKind } | null>(null)
  const { ref: mainRef, zoom, fitted, setZoom } = useZoom(lay)
  const managed = wf.data?.managed_by as string | undefined
  const canEdit = editable && wf.data?.summary_fields?.user_capabilities?.edit !== false && !managed
  const sel = typeof selected === 'number' ? nodes.find((n) => n.id === selected) ?? null : null
  const refresh = () => qc.invalidateQueries({ queryKey: [key] })

  useEffect(() => {
    if (typeof selected === 'number' && q.data && !q.data.some((n) => n.id === selected)) setSelected(null)
  }, [q.data, selected])
  useEffect(() => {
    if (typeof selected === 'number') document.querySelector<HTMLElement>(`[data-node="${selected}"]`)?.focus({ preventScroll: false })
  }, [selected])

  const remove = async (n: WfNode) => {
    const kids = KINDS.flatMap((k) => n[`${k}_nodes`] as number[]).length
    const ok = await confirm({
      title: tr('Remove “{nodeName}”?', { nodeName: nodeName(n) }),
      body: (
        <p style={{ margin: 0 }}>
          {tr('The step and its links are removed.')}
          {kids > 0 && tr(' Steps that followed it run from the start unless another step leads to them.')}
        </p>
      ),
      confirm: tr('Remove step'),
      danger: true,
    })
    if (!ok) return
    try {
      await api.del(`workflow_job_template_nodes/${n.id}/`)
      setSelected(null)
      await refresh()
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Could not remove the step'), 'error')
    }
  }

  useHotkeys(
    {
      escape: () => setSelected(null),
      a: () => canEdit && setAdding({ parent: typeof selected === 'number' ? selected : null, kind: 'success' }),
    },
    !adding,
  )

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    if ((e.target as HTMLElement).closest('input,textarea,select')) return
    if (typeof selected === 'number' && e.key.startsWith('Arrow')) {
      const next = neighbour(lay, nodes, selected, e.key)
      if (next !== null) {
        e.preventDefault()
        setSelected(next)
      } else if (e.key === 'ArrowLeft') {
        e.preventDefault()
        setSelected('start')
      }
    } else if (selected === 'start' && e.key === 'ArrowRight') {
      e.preventDefault()
      const first = lay.columns[1]?.[0]
      if (first !== undefined) setSelected(first)
    } else if ((e.key === 'Delete' || e.key === 'Backspace') && sel && canEdit) {
      e.preventDefault()
      remove(sel)
    }
  }

  if (q.isPending || wf.isPending) return <Loading />
  if (q.error) return <FormError error={q.error} />

  return (
    <div className="wf">
      <div className="wf-main" ref={mainRef}>
        <div className="wf-toolbar">
          <Legend />
          <div style={{ marginLeft: 'auto' }} className="row">
            {nodes.length > 0 && <ZoomControls zoom={zoom} fitted={fitted} setZoom={setZoom} />}
            {canEdit && (
              <button className="btn sm primary" onClick={() => setAdding({ parent: null, kind: 'success' })}>
                <Icon name="plus" size={14} />
                {tr('Add step')}
              </button>
            )}
          </div>
        </div>
        {managed && (
          <div className="notice small">
            {tr('Managed by')} <span className="mono">{managed}</span>{tr('. Change the workflow at its source; the graph here is read-only.')}
          </div>
        )}
        {nodes.length === 0 ? (
          <div className="wf-canvas" style={{ height: 'auto' }}>
            <Empty
              title={tr('This workflow has no steps')}
              action={
                canEdit ? (
                  <button className="btn primary" onClick={() => setAdding({ parent: null, kind: 'success' })}>
                    {tr('Add the first step')}
                  </button>
                ) : undefined
              }
            >
              {tr('Steps run templates, syncs or wait for an approval. Link them to run on success, on failure or always.')}
            </Empty>
          </div>
        ) : (
          <Canvas
            lay={lay}
            nodes={nodes}
            zoom={zoom}
            highlight={typeof selected === 'number' ? selected : null}
            label={tr('Workflow steps')}
            onKeyDown={onKeyDown}
            start={
              <button
                className="link-btn"
                style={{ color: '#fff', textDecoration: 'none' }}
                onClick={() => setSelected('start')}
                aria-pressed={selected === 'start'}
              >
                {tr('Start')}
              </button>
            }
            renderNode={(n, b) => {
              const t = nodeType(n)
              const meta = [isGenerated(n.identifier) ? '' : n.identifier, promptSummary(n), n.all_parents_must_converge ? 'waits for all parents' : ''].filter(Boolean).join(' · ')
              return (
                <button
                  type="button"
                  data-node={n.id}
                  className={`wf-node ${selected === n.id ? 'selected' : ''} ${t === 'workflow_approval_template' ? 'approval' : ''} ${n.unified_job_template ? '' : 'missing'}`}
                  style={{ left: b.x, top: b.y, width: b.w, height: b.h }}
                  onClick={() => setSelected(n.id)}
                  aria-pressed={selected === n.id}
                >
                  <span className="k">
                    {t === 'workflow_approval_template' ? <Icon name="approve" size={12} /> : t === 'workflow_job_template' ? <Icon name="workflow" size={12} /> : <Icon name="play" size={10} />}
                    {NODE_TYPE[t] ?? tr('Missing template')}
                  </span>
                  <span className="t" title={nodeName(n)}>
                    {nodeName(n)}
                  </span>
                  {meta && <span className="m">{meta}</span>}
                </button>
              )
            }}
          />
        )}
        <div className="wf-hint">
          {canEdit ? tr('Click a step to edit it · arrows move · a add a step · Delete removes · Esc deselects') : tr('Click a step to see its settings · arrows move between steps')}
        </div>
      </div>
      <aside className="wf-side" aria-label={tr('Step details')}>
        {sel ? (
          <NodePanel
            key={sel.id}
            node={sel}
            nodes={nodes}
            wf={wf.data}
            canEdit={canEdit}
            onSelect={setSelected}
            onChanged={refresh}
            onAdd={(kind) => setAdding({ parent: sel.id, kind })}
            onRemove={() => remove(sel)}
          />
        ) : selected === 'start' ? (
          <section className="panel">
            <div className="panel-head">
              <h2>{tr('Start')}</h2>
            </div>
            <div className="panel-body">
              <p className="muted" style={{ margin: 0 }}>
                {lay.columns[1]?.length
                  ? trn(lay.columns[1].length, '{n} step runs as soon as the workflow starts.', '{n} steps run as soon as the workflow starts.')
                  : tr('Nothing runs at the start yet.')}
              </p>
              {canEdit && (
                <button className="btn" onClick={() => setAdding({ parent: null, kind: 'success' })}>
                  <Icon name="plus" size={14} />
                  {tr('Add a step at the start')}
                </button>
              )}
            </div>
          </section>
        ) : (
          <section className="panel">
            <div className="panel-head">
              <h2>{wf.data?.name ?? tr('Workflow')}</h2>
            </div>
            <div className="panel-body small">
              <p className="muted" style={{ margin: 0 }}>
                {nodes.length} {tr('step')}{nodes.length === 1 ? '' : 's'}, {nodes.filter((n) => nodeType(n) === 'workflow_approval_template').length} {tr('approval')}
                {nodes.filter((n) => nodeType(n) === 'workflow_approval_template').length === 1 ? '' : 's'}{tr('. Select a step to')} {canEdit ? tr('edit it, link it or add what follows') : tr('see what it runs')}.
              </p>
              <p className="muted" style={{ margin: 0 }}>
                {tr('A step runs when a step before it ends the way its link says: on success, on failure, or always. Steps marked “waits for all parents” run only when every link into them fired.')}
              </p>
            </div>
          </section>
        )}
      </aside>
      {adding && (
        <AddStepDialog
          templateId={templateId}
          parent={adding.parent !== null ? nodes.find((n) => n.id === adding.parent) ?? null : null}
          kind={adding.kind}
          onClose={() => setAdding(null)}
          onCreated={async (id) => {
            setAdding(null)
            await refresh()
            setSelected(id)
          }}
        />
      )}
    </div>
  )
}

function NodePanel({
  node,
  nodes,
  wf,
  canEdit,
  onSelect,
  onChanged,
  onAdd,
  onRemove,
}: {
  node: WfNode
  nodes: WfNode[]
  wf: Any | undefined
  canEdit: boolean
  onSelect: (id: number) => void
  onChanged: () => Promise<unknown>
  onAdd: (kind: EdgeKind) => void
  onRemove: () => void
}) {
  const toast = useToast()
  const t = nodeType(node)
  const isApproval = t === 'workflow_approval_template'
  const ujt = node.summary_fields?.unified_job_template
  const [identifier, setIdentifier] = useState(node.identifier ?? '')
  const [converge, setConverge] = useState(!!node.all_parents_must_converge)
  const [when, setWhen] = useState(node.when ?? '')
  const [loopOn, setLoopOn] = useState(!!node.loop)
  const [loop, setLoop] = useState(node.loop ?? '')
  const [loopVar, setLoopVar] = useState(node.loop_var ?? '')
  const [loopConcurrency, setLoopConcurrency] = useState(node.loop_concurrency ? String(node.loop_concurrency) : '')
  const [tpl, setTpl] = useState<Ref | null>(ujt && !isApproval ? ({ id: ujt.id, name: ujt.name, type: ujt.type } as Ref) : null)
  const [apName, setApName] = useState(isApproval ? ujt?.name ?? '' : '')
  const [apDesc, setApDesc] = useState(isApproval ? ujt?.description ?? '' : '')
  const [apTimeout, setApTimeout] = useState(isApproval && ujt?.timeout ? String(Math.round(ujt.timeout / 60)) : '')
  const [prompts, setPrompts] = useState<Record<string, unknown>>(() => Object.fromEntries(PROMPTS.map((p) => [p.field, node[p.field] ?? null])))
  const [inv, setInv] = useState<Ref | null>(node.summary_fields?.inventory ?? null)
  const [ee, setEe] = useState<Ref | null>(node.summary_fields?.execution_environment ?? null)
  const { template, survey } = useTemplatePrompts(!isApproval && tpl ? tpl.type : undefined, tpl?.id)
  const surveyVars = new Set(survey.map((s) => s.variable))
  const [answers, setAnswers] = useState<Record<string, unknown>>(node.extra_data ?? {})
  const [extraText, setExtraText] = useState('')
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const [linkTo, setLinkTo] = useState<string>('')
  const [linkKind, setLinkKind] = useState<EdgeKind>('success')
  const surveyKey = survey.map((s) => s.variable).join(',')
  useEffect(() => {
    const rest: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(node.extra_data ?? {})) if (!surveyVars.has(k)) rest[k] = v
    setExtraText(varsText(rest))
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [surveyKey])

  const allowed = PROMPTS.filter((p) => template?.[p.ask])
  const askVars = !!template?.ask_variables_on_launch
  // Labels and instance groups saved on the step (only for the template it already runs).
  const rel = useSavedLabelsAndGroups(`workflow_job_template_nodes/${node.id}/`, tpl?.id === node.unified_job_template ? template : undefined)
  const byId = new Map(nodes.map((n) => [n.id, n]))
  const outgoing = KINDS.flatMap((k) => (node[`${k}_nodes`] as number[]).map((to) => ({ kind: k, to })))
  const incoming = nodes.flatMap((n) => KINDS.filter((k) => (n[`${k}_nodes`] as number[]).includes(node.id)).map((k) => ({ kind: k, from: n })))
  const linkable = nodes.filter((n) => n.id !== node.id && !outgoing.some((o) => o.to === n.id))
  // Outputs declared by the templates of earlier steps, offered in the condition and the loop.
  const jtIds = [...new Set(nodes.filter((n) => nodeType(n) === 'job_template' && n.unified_job_template).map((n) => n.unified_job_template!))].sort((a, b) => a - b)
  const declared = useQuery({
    queryKey: ['job_templates/', 'outputs', jtIds.join(',')],
    enabled: jtIds.length > 0,
    queryFn: () => getAll<{ id: number; outputs?: OutputDecl[] }>('job_templates/', { id__in: jtIds.join(',') }),
  })
  const outputsOf = (t: number) => declared.data?.find((x) => x.id === t)?.outputs
  const whenOutputs = outputSuggestions(node.id, nodes, outputsOf)
  const loopOutputs = outputSuggestions(node.id, nodes, outputsOf, true)

  const save = async () => {
    setBusy(true)
    setErrors({})
    setError(null)
    try {
      const body: Any = { identifier, all_parents_must_converge: converge, when: when.trim() }
      if (!isApproval) {
        body.loop = loopOn ? loop.trim() : ''
        body.loop_var = loopOn ? loopVar.trim() : ''
        body.loop_concurrency = loopOn && loopConcurrency ? Number(loopConcurrency) : 0
        if (tpl && tpl.id !== node.unified_job_template) body.unified_job_template = tpl.id
        for (const p of PROMPTS) if (allowed.includes(p)) body[p.field] = p.field === 'inventory' ? inv?.id ?? null : p.field === 'execution_environment' ? ee?.id ?? null : emptyNull(prompts[p.field])
        if (askVars || survey.length) {
          const extra: Record<string, unknown> = askVars ? parseVars(extraText) : {}
          for (const s of survey) {
            const v = answers[s.variable]
            if (v !== undefined && v !== null && v !== '') extra[s.variable] = v
          }
          body.extra_data = extra
        }
      }
      await api.patch(`workflow_job_template_nodes/${node.id}/`, body)
      if (!isApproval) await rel.save(`workflow_job_template_nodes/${node.id}/`)
      if (isApproval) {
        await api.post(`workflow_job_template_nodes/${node.id}/create_approval_template/`, {
          name: apName,
          description: apDesc,
          timeout: apTimeout ? Math.round(Number(apTimeout) * 60) : 0,
        })
      }
      await onChanged()
      toast(tr('Step saved'))
    } catch (e) {
      if (e instanceof ApiError && Object.keys(e.fields).length) setErrors(e.fields)
      setError(e)
    } finally {
      setBusy(false)
    }
  }

  const link = async (to: number, kind: EdgeKind, disassociate = false) => {
    try {
      await api.post(`workflow_job_template_nodes/${node.id}/${kind}_nodes/`, disassociate ? { id: to, disassociate: true } : { id: to })
      await onChanged()
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Could not change the link'), 'error')
    }
  }
  const changeKind = async (to: number, from: EdgeKind, kind: EdgeKind) => {
    if (from === kind) return
    try {
      await api.post(`workflow_job_template_nodes/${node.id}/${from}_nodes/`, { id: to, disassociate: true })
    } catch (e) {
      toast(e instanceof Error ? tr('Could not change the link: {message}', { message: e.message }) : tr('Could not change the link'), 'error')
      await onChanged()
      return
    }
    try {
      await api.post(`workflow_job_template_nodes/${node.id}/${kind}_nodes/`, { id: to })
    } catch (e) {
      // Put the original link back so the graph does not lose an edge.
      await api.post(`workflow_job_template_nodes/${node.id}/${from}_nodes/`, { id: to }).catch(() => {})
      toast(e instanceof Error ? tr('Could not change the link: {message}', { message: e.message }) : tr('Could not change the link'), 'error')
    }
    await onChanged()
  }

  const ro = !canEdit
  return (
    <section className="panel">
      <div className="panel-head">
        <div className="grow">
          <div className="small faint">{NODE_TYPE[t] ?? tr('Step')}</div>
          <h2 className="ellipsis" title={nodeName(node)}>
            {nodeName(node)}
          </h2>
        </div>
        {ujt && !isApproval && (
          <Link className="small nowrap" to={templatePath({ id: ujt.id, type: ujt.type })}>
            {tr('Open template')}
          </Link>
        )}
      </div>
      <div className="panel-body">
        <FormError error={error && !Object.keys(errors).length ? error : null} />
        {isApproval ? (
          <>
            <TextInput label={tr('Approval name')} value={apName} onChange={setApName} disabled={ro} error={errors.name} />
            <TextInput label={tr('Instructions')} value={apDesc} onChange={setApDesc} optional disabled={ro} help={tr('Shown to approvers.')} />
            <TextInput label={tr('Time out after (minutes)')} type="number" value={apTimeout} onChange={setApTimeout} optional disabled={ro} help={tr('Empty: waits until someone decides.')} error={errors.timeout} />
          </>
        ) : ro ? (
          <Field label={tr('Runs')}>
            <span>{nodeName(node)}</span>
          </Field>
        ) : (
          <Lookup
            label={tr('Runs')}
            resource="unified_job_templates/"
            params={{ type__in: NODE_TEMPLATE_TYPES, not__id: wf?.id ?? 0 }}
            value={tpl}
            onChange={setTpl}
            meta={(r) => NODE_TYPE[r.type] ?? r.type}
            error={errors.unified_job_template}
          />
        )}
        <TextInput label={tr('Identifier')} value={identifier} onChange={setIdentifier} mono disabled={ro} help={tr('Stable name for this step, used by GitOps and the API.')} error={errors.identifier} />
        {incoming.length > 1 && <Check label={tr('Wait for all parents')} checked={converge} onChange={(v) => !ro && setConverge(v)} help={tr('Run only when every link into this step fired, not the first one.')} />}
        <TextInput
          label={tr('Run only when')}
          value={when}
          onChange={setWhen}
          mono
          optional
          disabled={ro}
          placeholder={tr('deploy_env == \'prod\'')}
          error={errors.when}
          help={
            <>
              {tr('False skips this step. Use variables and earlier steps by identifier:')} <span className="mono">{tr('nodes.build.status == \'successful\'')}</span>, <span className="mono">{tr('nodes.build.artifacts.version')}</span>.
            </>
          }
        />
        {!ro && <OutputChips items={whenOutputs} onPick={(ref) => setWhen((w) => addToCondition(w, ref))} />}
        {!isApproval && <Check label={tr('Repeat for each item of a list')} checked={loopOn} onChange={(v) => !ro && setLoopOn(v)} help={tr('Runs the template once per item. The step fails if any item fails.')} />}
        {!isApproval && loopOn && (
          <div className="stack" style={{ gap: 12, paddingLeft: 24 }}>
            <TextInput label={tr('Items')} value={loop} onChange={setLoop} mono disabled={ro} placeholder={tr('regions   or   [\'eu\', \'us\']')} error={errors.loop} help={tr('A variable or expression giving a list.')} />
            {!ro && <OutputChips items={loopOutputs} onPick={setLoop} />}
            <div className="row" style={{ gap: 12, alignItems: 'flex-start' }}>
              <TextInput label={tr('Item variable')} value={loopVar} onChange={setLoopVar} mono optional disabled={ro} placeholder={tr('item')} error={errors.loop_var} help={tr('loop_index holds its position.')} />
              <TextInput label={tr('At most at once')} type="number" value={loopConcurrency} onChange={setLoopConcurrency} optional disabled={ro} placeholder={tr('all')} error={errors.loop_concurrency} help={tr('1 runs the items one by one.')} />
            </div>
          </div>
        )}
        {!isApproval && allowed.length > 0 && (
          <div className="stack" style={{ gap: 12 }}>
            {allowed.map((p) => (
              <PromptField
                key={p.field}
                field={p.field}
                value={prompts[p.field]}
                onChange={(v) => setPrompts((x) => ({ ...x, [p.field]: v }))}
                error={errors[p.field]}
                inventory={inv}
                setInventory={setInv}
                ee={ee}
                setEe={setEe}
                orgId={template?.organization}
              />
            ))}
          </div>
        )}
        {!isApproval && rel.any && (
          <div className="stack" style={{ gap: 12 }}>
            <SavedLabelsAndGroupsFields rel={rel} orgId={template?.organization} errors={errors} />
          </div>
        )}
        {!isApproval && survey.map((s) => <SurveyField key={s.variable} q={s} value={answers[s.variable] ?? s.default} onChange={(v) => setAnswers((a) => ({ ...a, [s.variable]: v }))} />)}
        {!isApproval && askVars && <VarsEditor label={tr('Extra variables')} value={extraText} onChange={setExtraText} error={errors.extra_data} rows={4} />}
        {!isApproval && template && !allowed.length && !askVars && !survey.length && !rel.any && (
          <p className="small faint" style={{ margin: 0 }}>
            {tr('The template prompts for nothing, so this step runs it as configured.')}
          </p>
        )}
        {canEdit && (
          <div className="form-actions">
            <button className="btn primary" disabled={busy || (isApproval ? !apName.trim() : !tpl || (loopOn && !loop.trim()))} onClick={save}>
              {busy ? tr('Saving…') : tr('Save step')}
            </button>
          </div>
        )}

        <div className="stack" style={{ gap: 8, borderTop: '1px solid var(--line-soft)', paddingTop: 12 }}>
          <h2>{tr('Runs after')}</h2>
          {incoming.length === 0 ? (
            <span className="small muted">{tr('The start of the workflow.')}</span>
          ) : (
            <ul className="wf-links">
              {incoming.map((i) => (
                <li key={`${i.from.id}-${i.kind}`}>
                  <span className={`wf-kind ${i.kind}`}>{KIND_LABEL[i.kind]}</span>
                  <button className="link-btn ellipsis" onClick={() => onSelect(i.from.id)}>
                    {nodeName(i.from)}
                  </button>
                </li>
              ))}
            </ul>
          )}
        </div>

        <div className="stack" style={{ gap: 8, borderTop: '1px solid var(--line-soft)', paddingTop: 12 }}>
          <h2>{tr('Then')}</h2>
          {outgoing.length === 0 && <span className="small muted">{tr('Nothing follows this step.')}</span>}
          {outgoing.length > 0 && (
            <ul className="wf-links">
              {outgoing.map((o) => (
                <li key={`${o.kind}-${o.to}`}>
                  {canEdit ? (
                    <select className="select" style={{ width: 'auto', height: 26, fontSize: 12 }} aria-label={tr('Link kind')} value={o.kind} onChange={(e) => changeKind(o.to, o.kind, e.target.value as EdgeKind)}>
                      {KINDS.map((k) => (
                        <option key={k} value={k}>
                          {KIND_LABEL[k]}
                        </option>
                      ))}
                    </select>
                  ) : (
                    <span className={`wf-kind ${o.kind}`}>{KIND_LABEL[o.kind]}</span>
                  )}
                  <button className="link-btn ellipsis grow" style={{ textAlign: 'left' }} onClick={() => onSelect(o.to)}>
                    {byId.get(o.to) ? nodeName(byId.get(o.to)!) : tr('#{to}', { to: o.to })}
                  </button>
                  {canEdit && (
                    <button className="btn ghost sm icon" aria-label={tr('Remove link')} title={tr('Remove link')} onClick={() => link(o.to, o.kind, true)}>
                      <Icon name="x" size={12} />
                    </button>
                  )}
                </li>
              ))}
            </ul>
          )}
          {canEdit && (
            <>
              <div className="row wrap" style={{ gap: 6 }}>
                <button className="btn sm" onClick={() => onAdd('success')}>
                  <Icon name="plus" size={12} />
                  {tr('On success')}
                </button>
                <button className="btn sm" onClick={() => onAdd('failure')}>
                  <Icon name="plus" size={12} />
                  {tr('On failure')}
                </button>
                <button className="btn sm" onClick={() => onAdd('always')}>
                  <Icon name="plus" size={12} />
                  {tr('Always')}
                </button>
              </div>
              {linkable.length > 0 && (
                <div className="row wrap" style={{ gap: 6 }}>
                  <label className="sr-only" htmlFor={`link-${node.id}`}>
                    {tr('Link to an existing step')}
                  </label>
                  <select id={`link-${node.id}`} className="select" style={{ flex: '1 1 140px', height: 28, fontSize: 12 }} value={linkTo} onChange={(e) => setLinkTo(e.target.value)}>
                    <option value="">{tr('Link to an existing step…')}</option>
                    {linkable.map((n) => (
                      <option key={n.id} value={n.id}>
                        {nodeName(n)}
                        {n.identifier ? tr(' ({identifier})', { identifier: n.identifier }) : ''}
                      </option>
                    ))}
                  </select>
                  <select className="select" aria-label={tr('Link kind')} style={{ width: 'auto', height: 28, fontSize: 12 }} value={linkKind} onChange={(e) => setLinkKind(e.target.value as EdgeKind)}>
                    {KINDS.map((k) => (
                      <option key={k} value={k}>
                        {KIND_LABEL[k]}
                      </option>
                    ))}
                  </select>
                  <button
                    className="btn sm"
                    disabled={!linkTo}
                    onClick={async () => {
                      await link(Number(linkTo), linkKind)
                      setLinkTo('')
                    }}
                  >
                    {tr('Link')}
                  </button>
                </div>
              )}
            </>
          )}
        </div>
        {canEdit && (
          <div style={{ borderTop: '1px solid var(--line-soft)', paddingTop: 12 }}>
            <button className="btn danger sm" onClick={onRemove}>
              <Icon name="trash" size={12} />
              {tr('Remove step')}
            </button>
          </div>
        )}
      </div>
    </section>
  )
}

function emptyNull(v: unknown): unknown {
  return v === '' || v === undefined ? null : v
}

function AddStepDialog({
  templateId,
  parent,
  kind: initialKind,
  onClose,
  onCreated,
}: {
  templateId: number
  parent: WfNode | null
  kind: EdgeKind
  onClose: () => void
  onCreated: (id: number) => void
}) {
  const [what, setWhat] = useState<'template' | 'approval'>('template')
  const [kind, setKind] = useState<EdgeKind>(initialKind)
  const [tpl, setTpl] = useState<Ref | null>(null)
  const [identifier, setIdentifier] = useState('')
  const [apName, setApName] = useState('')
  const [apDesc, setApDesc] = useState('')
  const [apTimeout, setApTimeout] = useState('')
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const create = async () => {
    setBusy(true)
    setError(null)
    try {
      const body: Any = {}
      if (identifier.trim()) body.identifier = identifier.trim()
      if (what === 'template') body.unified_job_template = tpl?.id
      const node = parent
        ? await api.post<Any>(`workflow_job_template_nodes/${parent.id}/${kind}_nodes/`, body)
        : await api.post<Any>(`workflow_job_templates/${templateId}/workflow_nodes/`, body)
      if (what === 'approval') {
        try {
          await api.post(`workflow_job_template_nodes/${node.id}/create_approval_template/`, {
            name: apName,
            description: apDesc,
            timeout: apTimeout ? Math.round(Number(apTimeout) * 60) : 0,
          })
        } catch (e) {
          await api.del(`workflow_job_template_nodes/${node.id}/`).catch(() => {})
          throw e
        }
      }
      onCreated(node.id)
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  const ready = what === 'template' ? !!tpl : !!apName.trim()
  return (
    <Dialog
      title={parent ? tr('Add a step after “{nodeName}”', { nodeName: nodeName(parent) }) : tr('Add a step at the start')}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" disabled={busy || !ready} onClick={create}>
            {busy ? tr('Adding…') : tr('Add step')}
          </button>
        </>
      }
    >
      <FormError error={error} />
      {parent && (
        <fieldset>
          <legend>{tr('Run it')}</legend>
          <div className="segmented">
            {KINDS.map((k) => (
              <label key={k} className={kind === k ? 'on' : ''}>
                <input type="radio" name="add-kind" checked={kind === k} onChange={() => setKind(k)} />
                {k === 'success' ? tr('When it succeeds') : k === 'failure' ? tr('When it fails') : tr('Always')}
              </label>
            ))}
          </div>
        </fieldset>
      )}
      <fieldset>
        <legend>{tr('Step')}</legend>
        <div className="segmented">
          <label className={what === 'template' ? 'on' : ''}>
            <input type="radio" name="add-what" checked={what === 'template'} onChange={() => setWhat('template')} />
            {tr('Run a template or sync')}
          </label>
          <label className={what === 'approval' ? 'on' : ''}>
            <input type="radio" name="add-what" checked={what === 'approval'} onChange={() => setWhat('approval')} />
            {tr('Wait for approval')}
          </label>
        </div>
      </fieldset>
      {what === 'template' ? (
        <Lookup
          label={tr('Runs')}
          resource="unified_job_templates/"
          params={{ type__in: NODE_TEMPLATE_TYPES, not__id: templateId }}
          value={tpl}
          onChange={setTpl}
          meta={(r) => NODE_TYPE[r.type] ?? r.type}
        />
      ) : (
        <>
          <TextInput label={tr('Approval name')} value={apName} onChange={setApName} autoFocus placeholder={tr('Release manager sign-off')} />
          <TextInput label={tr('Instructions')} value={apDesc} onChange={setApDesc} optional help={tr('Shown to approvers.')} />
          <TextInput label={tr('Time out after (minutes)')} type="number" value={apTimeout} onChange={setApTimeout} optional help={tr('Empty: waits until someone decides. A timeout counts as a failure.')} />
        </>
      )}
      <TextInput label={tr('Identifier')} value={identifier} onChange={setIdentifier} mono optional help={tr('Defaults to a generated id.')} />
    </Dialog>
  )
}

// ---------- live job graph ----------

/** A run node's status and label: its job's, its loop's, or why it did not run. */
function runState(n: WfNode): { status: string; label: string; loop?: LoopProgress } {
  if (Array.isArray(n.loop_items)) {
    const p = loopProgress(n.loop_items, n.iterations ?? [])
    const label = p.total === 0 ? tr('No items') : `${p.done} of ${p.total} item${p.total === 1 ? '' : 's'} done${p.failed ? ` · ${p.failed} failed` : ''}`
    return { status: p.status, label, loop: p }
  }
  const job = n.summary_fields?.job
  if (job) return { status: job.status, label: statusLabel(job.status) }
  if (n.do_not_run) return { status: 'none', label: n.skip_reason ? tr('Skipped by its condition') : tr('Will not run') }
  return { status: 'waiting', label: tr('Waiting') }
}

/** The jobs of a loop node, one per item. */
function LoopItems({ node, onClose }: { node: WfNode; onClose: () => void }) {
  const items = node.loop_items ?? []
  const byIndex = new Map((node.iterations ?? []).map((i) => [i.index, i]))
  return (
    <Dialog title={trn(items.length, '{step}: {n} item', '{step}: {n} items', { step: nodeName(node) })} onClose={onClose}>
      <p className="small muted" style={{ marginTop: 0 }}>
        {tr('Each item of')} <span className="mono">{node.loop}</span> {tr('runs as its own job, with the item in')} <span className="mono">{node.loop_var || tr('item')}</span>
        {node.loop_concurrency ? tr(', {loop_concurrency} at a time', { loop_concurrency: node.loop_concurrency }) : ''}.
      </p>
      <ul className="wf-links">
        {items.map((item, i) => {
          const job = byIndex.get(i)?.job
          return (
            <li key={i}>
              <StatusDot status={job?.status ?? 'waiting'} />
              <span className="mono ellipsis" title={typeof item === 'string' ? item : JSON.stringify(item)}>
                {itemLabel(item, i)}
              </span>
              <span style={{ marginLeft: 'auto' }} className="small nowrap">
                {job ? (
                  <>
                    <Link to={`/jobs/${job.type}/${job.id}`} onClick={onClose}>
                      #{job.id}
                    </Link>{' '}
                    {statusLabel(job.status)}
                    {job.elapsed ? tr(' · {duration}', { duration: duration(job.elapsed) }) : ''}
                  </>
                ) : (
                  <span className="muted">{tr('waiting')}</span>
                )}
              </span>
            </li>
          )
        })}
      </ul>
    </Dialog>
  )
}

export function WorkflowJobGraph({ jobId }: { jobId: number }) {
  const qc = useQueryClient()
  const nav = useNavigate()
  const toast = useToast()
  const confirm = useConfirm()
  const key = `workflow_jobs/${jobId}/workflow_nodes/`
  const q = useQuery({ queryKey: [key, 'all'], queryFn: () => getAll<WfNode>(key, { order_by: 'id' }) })
  const nodes = useMemo(() => q.data ?? [], [q.data])
  const lay = useMemo(() => layout(toGraph(nodes), { nodeH: 74, nodeW: 200 }), [nodes])
  const { ref: mainRef, zoom, fitted, setZoom } = useZoom(lay)
  const pendingApprovals = nodes.filter((n) => n.summary_fields?.job?.type === 'workflow_approval' && n.summary_fields?.job?.status === 'pending').map((n) => n.summary_fields.job.id as number)
  const approvals = useQuery({
    queryKey: ['workflow_approvals', 'graph', pendingApprovals.join(',')],
    queryFn: () => api.get<{ results: Any[] }>('workflow_approvals/', { id__in: pendingApprovals.join(','), page_size: 50 }),
    enabled: pendingApprovals.length > 0,
  })
  const canDecide = new Map((approvals.data?.results ?? []).map((a) => [a.id as number, !!a.can_approve_or_deny]))
  const [loopNodeId, setLoopNodeId] = useState<number | null>(null)
  const loopNode = nodes.find((n) => n.id === loopNodeId)
  const timer = useRef<number | undefined>(undefined)
  useLive({ workflow_events: [jobId], jobs: ['status_changed'] }, (m) => {
    if (m.group_name === 'jobs' && m.workflow_job_id !== jobId && m.unified_job_id !== jobId) return
    window.clearTimeout(timer.current)
    timer.current = window.setTimeout(() => qc.invalidateQueries({ queryKey: [key] }), 300)
  })

  const decide = async (approvalId: number, name: string, action: 'approve' | 'deny') => {
    const ok = await confirm({
      title: action === 'approve' ? tr('Approve “{name}”?', { name }) : tr('Deny “{name}”?', { name }),
      confirm: action === 'approve' ? tr('Approve') : tr('Deny'),
      danger: action === 'deny',
      body: <p style={{ margin: 0 }}>{action === 'approve' ? tr('The workflow continues.') : tr('The workflow takes its failure path.')}</p>,
    })
    if (!ok) return
    try {
      await api.post(`workflow_approvals/${approvalId}/${action}/`)
      toast(action === 'approve' ? tr('{name} approved', { name }) : tr('{name} denied', { name }))
      qc.invalidateQueries({ queryKey: [key] })
      qc.invalidateQueries({ queryKey: ['workflow_approvals'] })
    } catch (e) {
      toast(e instanceof Error ? e.message : tr('Could not record the decision'), 'error')
    }
  }

  if (q.isPending) return <Loading />
  if (q.error) return <FormError error={q.error} />
  if (!nodes.length) return <Empty title={tr('This workflow had no steps')} />

  const counts: Record<string, number> = {}
  for (const n of nodes) {
    const s = runState(n).status
    const k = s === 'none' ? 'skipped' : statusKind(s)
    counts[k] = (counts[k] ?? 0) + 1
  }
  const dnr = new Set(nodes.filter((n) => n.do_not_run).map((n) => n.id))

  return (
    <div className="wf-main" style={{ flex: 'none', width: '100%' }} ref={mainRef}>
      {loopNode && <LoopItems node={loopNode} onClose={() => setLoopNodeId(null)} />}
      <div className="wf-toolbar">
        <div className="wf-counts">
          {counts.ok ? (
            <span>
              <StatusDot status="successful" /> {counts.ok} {tr('succeeded')}
            </span>
          ) : null}
          {counts.fail || counts.error ? (
            <span>
              <StatusDot status="failed" /> {(counts.fail ?? 0) + (counts.error ?? 0)} {tr('failed')}
            </span>
          ) : null}
          {counts.run ? (
            <span>
              <StatusDot status="running" /> {counts.run} {tr('running')}
            </span>
          ) : null}
          {counts.pending ? (
            <span>
              <StatusDot status="pending" /> {counts.pending} {tr('waiting')}
            </span>
          ) : null}
          {counts.skipped ? (
            <span>
              <StatusDot status="none" /> {counts.skipped} {tr('not run')}
            </span>
          ) : null}
        </div>
        <div style={{ marginLeft: 'auto' }} className="row">
          <Legend />
          <ZoomControls zoom={zoom} fitted={fitted} setZoom={setZoom} />
        </div>
      </div>
      <Canvas
        lay={lay}
        nodes={nodes}
        zoom={zoom}
        short
        label={tr('Workflow run')}
        dim={(id) => dnr.has(id)}
        start={<span>{tr('Start')}</span>}
        renderNode={(n, b) => {
          const job = n.summary_fields?.job
          const t = nodeType(n)
          const { status, label, loop } = runState(n)
          const k = statusKind(status)
          const approvalPending = job?.type === 'workflow_approval' && job.status === 'pending'
          const decidable = approvalPending && canDecide.get(job.id)
          const clickable = !!job || !!loop
          const open = () => {
            if (loop) setLoopNodeId(n.id)
            else if (!job) return
            else if (job.type === 'workflow_approval') nav('/approvals')
            else nav(`/jobs/${job.type}/${job.id}`)
          }
          return (
            <div
              role="button"
              tabIndex={0}
              data-node={n.id}
              className={`wf-node ${k} ${n.do_not_run ? 'dnr' : ''} ${t === 'workflow_approval_template' ? 'approval' : ''}`}
              style={{ left: b.x, top: b.y, width: b.w, height: b.h, cursor: clickable ? 'pointer' : 'default' }}
              onClick={(e) => !(e.target as HTMLElement).closest('button') && open()}
              onKeyDown={(e) => e.key === 'Enter' && open()}
              title={job ? tr('#{id} {nodeName}: {label}', { id: job.id, nodeName: nodeName(n), label }) : n.skip_reason ? tr('{nodeName}: {skip_reason}', { nodeName: nodeName(n), skip_reason: n.skip_reason }) : nodeName(n)}
            >
              <span className="k">
                <StatusDot status={status} />
                {NODE_TYPE[t] ?? tr('Step')}
                {loop && <span className="mono">× {loop.total}</span>}
                {job && <span className="mono">#{job.id}</span>}
              </span>
              <span className="t">{nodeName(n)}</span>
              {decidable ? (
                <span className="acts">
                  <button className="btn sm primary" onClick={() => decide(job.id, nodeName(n), 'approve')}>
                    {tr('Approve')}
                  </button>
                  <button className="btn sm" onClick={() => decide(job.id, nodeName(n), 'deny')}>
                    {tr('Deny')}
                  </button>
                </span>
              ) : (
                <span className="m">
                  {label}
                  {job && job.elapsed ? tr(' · {duration}', { duration: duration(job.elapsed) }) : ''}
                  {approvalPending && !decidable ? tr(' · waiting on approvers') : ''}
                </span>
              )}
            </div>
          )
        }}
      />
    </div>
  )
}
