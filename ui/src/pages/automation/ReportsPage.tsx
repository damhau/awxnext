import { keepPreviousData, useQuery } from '@tanstack/react-query'
import { useEffect, useRef, useState, type KeyboardEvent, type ReactNode } from 'react'
import { Link, useSearchParams } from 'react-router'
import { api } from '../../api/client'
import { useObject } from '../../api/hooks'
import type { Ref } from '../../api/types'
import { useOrg } from '../../app/context'
import { Lookup, Segmented, SelectField } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog } from '../../components/overlay'
import { DataTable, type Column } from '../../components/table'
import { Ago, Empty, ErrorNote, PageHead, Panel } from '../../components/ui'
import { duration, plural } from '../../lib/format'
import { asIs, tr } from '../../lib/i18n'
import { usePref } from '../../lib/prefs'
import {
  apiParams,
  automatedCsv,
  bucketLabel,
  byLaunchTypeCsv,
  byTeamCsv,
  byUserCsv,
  csvName,
  labelEvery,
  LAUNCH_TYPES,
  launchTypeLabel,
  mostFailedCsv,
  durationScale,
  niceScale,
  percent,
  points,
  readState,
  sameParams,
  savedParams,
  templateCsv,
  toCsv,
  trendCsv,
  type CsvColumn,
  type FilterKey,
  type HostsReport,
  type Interval,
  type LaunchesReport,
  type LaunchRow,
  type Period,
  type ReportPeriod,
  type ReportState,
  type SavedReport,
  type TemplateRow,
  type Totals,
  type TrendBucket,
} from '../../lib/reports'
import './reports.css'

function useReport<T>(name: string, params: Record<string, string>) {
  return useQuery<T>({
    queryKey: ['reports/', name, params],
    queryFn: ({ signal }) => api.get<T>(`reports/${name}/`, params, signal),
    placeholderData: keepPreviousData,
  })
}

function download(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: 'text/csv;charset=utf-8' }))
  const a = document.createElement('a')
  a.href = url
  a.download = name
  a.click()
  setTimeout(() => URL.revokeObjectURL(url), 0)
}

function CsvButton<T>({ report, columns, rows, period }: { report: string; columns: CsvColumn<T>[]; rows: T[] | undefined; period: ReportPeriod | undefined }) {
  return (
    <button className="btn sm" disabled={!rows} onClick={() => rows && download(csvName(report, period), toCsv(columns, rows))} title={tr('The rows the API returns, as CSV')}>
      <Icon name="download" size={13} />
      {tr('CSV')}
    </button>
  )
}

const lookups: { key: FilterKey; label: string; resource: string }[] = [
  { key: 'organization', label: tr('Organization'), resource: 'organizations' },
  { key: 'template', label: tr('Template'), resource: 'job_templates' },
  { key: 'inventory', label: tr('Inventory'), resource: 'inventories' },
  { key: 'project', label: tr('Project'), resource: 'projects' },
  { key: 'label', label: tr('Label'), resource: 'labels' },
]

function FilterLookup({ label, resource, id, onChange }: { label: string; resource: string; id: number | undefined; onChange: (v: Ref | null) => void }) {
  const obj = useObject<Ref>(id ? `${resource}/${id}/` : null)
  const value = id ? { id, name: obj.data?.name ?? asIs(`#${id}`) } : null
  return <Lookup label={label} resource={`${resource}/`} value={value} onChange={onChange} optional />
}

/** How job template runs did over a period, from the report history, which outlives the jobs. */
export function ReportsPage() {
  const [sp, setSp] = useSearchParams()
  const { scope } = useOrg()
  const state = readState(sp)
  // The organization chosen in the top bar, until the page picks another or clears it.
  if (!sp.has('organization') && scope.organization) state.filters.organization = scope.organization
  const update = (patch: Record<string, string | null>) =>
    setSp(
      (prev) => {
        const n = new URLSearchParams(prev)
        for (const [k, v] of Object.entries(patch)) {
          if (v === null || v === '') n.delete(k)
          else n.set(k, v)
        }
        return n
      },
      { replace: true },
    )
  const params = apiParams(state)
  const totals = useReport<Totals>('totals', params)
  const period = totals.data?.period
  const [views, setViews] = usePref<SavedReport[]>('report-views', [])
  const [saving, setSaving] = useState(false)
  const current = savedParams(state)
  const setPeriod = (p: Period) => {
    if (p === 'custom') update({ period: null, from: period?.from ?? new Date(Date.now() - 29 * 864e5).toISOString().slice(0, 10), to: period?.to ?? null })
    else update({ period: p === '30' ? null : p, from: null, to: null })
  }

  return (
    <div className="page reports">
      <PageHead title={tr('Reports')} subtitle={tr('How job template runs did over a period: success rates, durations, hosts and who launches what. Days are UTC days.')} bordered={false} />
      <div className="page-body stack" style={{ gap: 20 }}>
        <div className="report-filters">
          <Segmented<Period>
            legend={tr('Period')}
            value={state.period}
            onChange={setPeriod}
            options={[
              ['7', tr('7 days')],
              ['30', tr('30 days')],
              ['90', tr('90 days')],
              ['custom', tr('Dates')],
            ]}
          />
          {state.period === 'custom' && (
            <>
              <div className="field">
                <label htmlFor="report-from">{tr('From')}</label>
                <input id="report-from" type="date" className="input" value={state.from} max={state.to || undefined} onChange={(e) => e.target.value && update({ from: e.target.value })} />
              </div>
              <div className="field">
                <label htmlFor="report-to">{tr('To')}</label>
                <input id="report-to" type="date" className="input" value={state.to || (period?.to ?? '')} min={state.from} onChange={(e) => update({ to: e.target.value || null })} />
              </div>
            </>
          )}
          {lookups.map((l) => (
            <FilterLookup
              key={l.key}
              label={l.label}
              resource={l.resource}
              id={state.filters[l.key]}
              onChange={(v) => update({ [l.key]: v ? String(v.id) : l.key === 'organization' && scope.organization ? 'any' : null })}
            />
          ))}
          <SelectField label={tr('Launched by')} value={state.launchType} onChange={(v) => update({ launch_type: v || null })} options={[['', tr('Any launch')], ...LAUNCH_TYPES]} />
        </div>
        <div className="chips" role="group" aria-label={tr('Saved reports')}>
          {views.map((v) => {
            const on = sameParams(v.params, current)
            return (
              <span key={v.name} className={`qchip${on ? ' on' : ''}`}>
                <button className="qchip-main" aria-pressed={on} onClick={() => setSp(new URLSearchParams({ organization: 'any', ...v.params }), { replace: true })}>
                  {v.name}
                </button>
                <button className="qchip-x" aria-label={tr('Delete saved report {name}', { name: v.name })} onClick={() => setViews((all) => all.filter((x) => x.name !== v.name))}>
                  <Icon name="x" size={11} />
                </button>
              </span>
            )
          })}
          <button className="qchip add" onClick={() => setSaving(true)} title={tr('Keep this period and these filters as a chip, with your account')}>
            {tr('+ Save report')}
          </button>
        </div>
        {period?.cut_short && (
          <div className="notice warn" role="status">
            {tr('The report history starts on {day}: this period is cut short. REPORT_HISTORY_DAYS sets how many days it keeps.', { day: period.kept_since })}
          </div>
        )}
        <ErrorNote error={totals.error} />
        <TotalsTiles totals={totals.data} stale={totals.isPlaceholderData} />
        <TemplatesPanel state={state} params={params} period={period} onOrder={(o) => update({ order_by: o })} />
        <TrendsPanel state={state} params={params} period={period} onInterval={(i) => update({ interval: i })} />
        <HostsPanel params={params} period={period} />
        <LaunchesPanel params={params} period={period} />
      </div>
      {saving && (
        <SaveReportDialog
          onClose={() => setSaving(false)}
          onSave={(name) => {
            setViews((all) => [...all.filter((v) => v.name !== name), { name, params: current }])
            setSaving(false)
          }}
        />
      )}
    </div>
  )
}

function Tile({ label, value, children }: { label: string; value: ReactNode; children?: ReactNode }) {
  return (
    <div className="report-tile">
      <span className="report-tile-label">{label}</span>
      <span className="report-tile-value">{value}</span>
      {children && <span className="small muted">{children}</span>}
    </div>
  )
}

function TotalsTiles({ totals: t, stale }: { totals: Totals | undefined; stale: boolean }) {
  if (!t) return <div className="report-tiles" aria-busy="true" />
  return (
    <div className={`report-tiles${stale ? ' stale' : ''}`}>
      <Tile label={tr('Runs')} value={t.runs.toLocaleString()}>
        {tr('{ok} successful · {failed} failed · {canceled} canceled', { ok: t.successful, failed: t.failed, canceled: t.canceled })}
      </Tile>
      <Tile label={tr('Success rate')} value={percent(t.success_rate)}>
        {tr('Canceled runs are left out.')}
      </Tile>
      <Tile label={tr('Hours of automation')} value={t.hours.toLocaleString()}>
        {tr('The runs’ total duration.')}
      </Tile>
      <Tile label={tr('Hosts automated')} value={t.hosts.toLocaleString()}>
        {plural(t.host_runs, 'host run', 'host runs')}
      </Tile>
      <div className="report-tile">
        <span className="report-tile-label">{tr('Top projects')}</span>
        {t.top_projects.length ? (
          <ol className="report-top">
            {t.top_projects.map((p) => (
              <li key={p.project}>
                <Link to={`/projects/${p.project}`} className="ellipsis">
                  {p.name ?? asIs(`#${p.project}`)}
                </Link>
                <span className="small muted">{p.runs.toLocaleString()}</span>
              </li>
            ))}
          </ol>
        ) : (
          <span className="faint small">{tr('No run in this period')}</span>
        )}
      </div>
    </div>
  )
}

function Rate({ rate, change }: { rate: number | null; change?: number | null }) {
  return (
    <span className="nowrap">
      {percent(rate)}
      {change !== undefined && change !== null && <span className={`small report-change${change < 0 ? ' down' : ''}`}> {points(change)}</span>}
    </span>
  )
}

const num = (n: number) => <span className="small">{n.toLocaleString()}</span>

function TemplatesPanel({ state, params, period, onOrder }: { state: ReportState; params: Record<string, string>; period: ReportPeriod | undefined; onOrder: (o: string) => void }) {
  const order = state.order || 'success_rate'
  const r = useReport<{ results: TemplateRow[] }>('templates', { ...params, order_by: order })
  const cols: Column<TemplateRow>[] = [
    {
      key: 'name',
      header: tr('Template'),
      sort: 'name',
      render: (t) =>
        t.template ? (
          <Link to={`/templates/job/${t.template}/jobs`} className="cell-title">
            {t.name}
          </Link>
        ) : (
          <span>
            {t.name} <span className="faint small">{tr('(deleted)')}</span>
          </span>
        ),
    },
    { key: 'runs', header: tr('Runs'), sort: 'runs', className: 'num', render: (t) => num(t.runs) },
    { key: 'failed', header: tr('Failed'), sort: 'failed', className: 'num', render: (t) => num(t.failed) },
    {
      key: 'rate',
      header: tr('Success rate'),
      sort: 'success_rate',
      className: 'num',
      render: (t) => <Rate rate={t.success_rate} change={t.success_rate_change} />,
    },
    { key: 'median', header: tr('Median'), sort: 'median_duration', className: 'num', render: (t) => <span className="small mono">{duration(t.median_duration)}</span> },
    { key: 'p95', header: tr('95th percentile'), sort: 'p95_duration', className: 'num', render: (t) => <span className="small mono">{duration(t.p95_duration)}</span> },
    { key: 'flips', header: tr('Flips'), sort: 'flips', className: 'num', render: (t) => num(t.flips) },
    {
      key: 'last',
      header: tr('Last failure'),
      sort: 'last_failure',
      render: (t) =>
        t.last_failure ? (
          <span className="small nowrap">
            {t.last_failure.exists ? <Link to={`/jobs/job/${t.last_failure.job}`}>{asIs(`#${t.last_failure.job}`)}</Link> : <span className="faint">{asIs(`#${t.last_failure.job}`)}</span>} <Ago at={t.last_failure.finished} />
          </span>
        ) : (
          <span className="faint">—</span>
        ),
    },
  ]
  return (
    <Panel
      title={<h2>{tr('Templates')}<span className="small muted">{tr('worst first: the lowest success rate, then the most failures')}</span></h2>}
      actions={<CsvButton report="templates" columns={templateCsv} rows={r.data?.results} period={period} />}
      pad={false}
    >
      <div className={r.isPlaceholderData ? 'stale' : undefined}>
        <DataTable
          label={tr('Templates')}
          rows={r.data?.results}
          rowKey={(t) => t.template ?? 'name:' + t.name}
          columns={cols}
          order={order}
          onOrder={onOrder}
          loading={r.isPending}
          error={r.error}
          keyboard={false}
          stack
          empty={<Empty title={tr('No run in this period')}>{tr('Runs of job templates show here once they finish.')}</Empty>}
          footer={
            <div className="table-foot">
              <span>{tr('A flip is a run whose result differs from the run before. The change is against the {days} days before.', { days: period?.days ?? '' })}</span>
            </div>
          }
        />
      </div>
    </Panel>
  )
}

function useWidth<T extends HTMLElement>() {
  const ref = useRef<T>(null)
  const [w, setW] = useState(0)
  useEffect(() => {
    const el = ref.current
    if (!el) return
    const ro = new ResizeObserver(([e]) => setW(Math.floor(e.contentRect.width)))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])
  return [ref, w] as const
}

const PLOT_H = 150
const AXIS_H = 22
const LEFT = 44
const TOP = 10

/** A column with a 4px round end, square on the baseline. */
function columnPath(x: number, y: number, w: number, h: number, round: boolean): string {
  if (h <= 0) return ''
  const r = round ? Math.min(4, w / 2, h) : 0
  return `M${x},${y + h}V${y + r}${r ? `Q${x},${y} ${x + r},${y}` : ''}H${x + w - r}${r ? `Q${x + w},${y} ${x + w},${y + r}` : ''}V${y + h}Z`
}

interface ChartProps {
  buckets: TrendBucket[]
  interval: Interval
  label: string
}

/** The hovered or focused bucket, moved with the arrow keys. */
function useCursor(n: number) {
  const [at, setAt] = useState<number | null>(null)
  const onKey = (e: KeyboardEvent) => {
    if (e.key !== 'ArrowLeft' && e.key !== 'ArrowRight') return
    e.preventDefault()
    e.stopPropagation()
    setAt((i) => Math.max(0, Math.min(n - 1, (i ?? (e.key === 'ArrowLeft' ? n : -1)) + (e.key === 'ArrowLeft' ? -1 : 1))))
  }
  return { at, setAt, onKey }
}

function Tooltip({ b, interval, x, width, children }: { b: TrendBucket; interval: Interval; x: number; width: number; children: ReactNode }) {
  const left = Math.min(Math.max(x, 90), width - 90)
  return (
    <div className="chart-tip" style={{ left }} role="status">
      <div className="chart-tip-head">{bucketLabel(b.start, interval, true)}</div>
      {children}
    </div>
  )
}

function TipRow({ value, label, kind }: { value: ReactNode; label: string; kind?: string }) {
  return (
    <div className="chart-tip-row">
      {kind && <span className={`chart-key ${kind}`} aria-hidden="true" />}
      <strong>{value}</strong>
      <span>{label}</span>
    </div>
  )
}

function XAxis({ buckets, interval, x, band }: { buckets: TrendBucket[]; interval: Interval; x: (i: number) => number; band: number }) {
  const every = labelEvery(buckets.length, Math.max(2, Math.floor((band * buckets.length) / 64)))
  return (
    <g className="chart-axis">
      {buckets.map((b, i) =>
        i % every === 0 ? (
          <text key={b.start} x={x(i) + band / 2} y={TOP + PLOT_H + 15} textAnchor="middle">
            {bucketLabel(b.start, interval)}
          </text>
        ) : null,
      )}
    </g>
  )
}

function YAxis({ ticks, top, width, format }: { ticks: number[]; top: number; width: number; format: (v: number) => string }) {
  const y = (v: number) => TOP + PLOT_H - (v / top) * PLOT_H
  return (
    <g className="chart-axis">
      {ticks.map((t) => (
        <g key={t}>
          <line x1={LEFT} x2={width} y1={y(t)} y2={y(t)} className={t === 0 ? 'baseline' : 'grid'} />
          <text x={LEFT - 6} y={y(t) + 4} textAnchor="end">
            {format(t)}
          </text>
        </g>
      ))}
    </g>
  )
}

function RunsChart({ buckets, interval, label }: ChartProps) {
  const [ref, width] = useWidth<HTMLDivElement>()
  const { at, setAt, onKey } = useCursor(buckets.length)
  const max = Math.max(0, ...buckets.map((b) => b.runs))
  const { top, ticks } = niceScale(max)
  const plotW = Math.max(0, width - LEFT)
  const band = buckets.length ? plotW / buckets.length : 0
  const barW = Math.max(2, Math.min(24, band * 0.7))
  const x = (i: number) => LEFT + i * band
  const h = (v: number) => (v / top) * PLOT_H
  const hovered = at !== null ? buckets[at] : null
  return (
    <figure className="chart">
      <figcaption>
        <span className="chart-title">{label}</span>
        <span className="chart-legend" aria-hidden="true">
          <span><i className="swatch ok" />{tr('Successful')}</span>
          <span><i className="swatch fail" />{tr('Failed')}</span>
          <span><i className="swatch cancel" />{tr('Canceled')}</span>
        </span>
      </figcaption>
      <div ref={ref} className="chart-box" style={{ height: TOP + PLOT_H + AXIS_H }}>
        {width > 0 && (
          <svg width={width} height={TOP + PLOT_H + AXIS_H} role="img" aria-label={tr('{label}: the table below has the values', { label })} tabIndex={0} onKeyDown={onKey} onBlur={() => setAt(null)} onMouseLeave={() => setAt(null)}>
            <YAxis ticks={ticks} top={top} width={width} format={(v) => v.toLocaleString()} />
            {buckets.map((b, i) => {
              const bx = x(i) + (band - barW) / 2
              const parts = [
                ['ok', b.successful],
                ['fail', b.failed],
                ['cancel', b.canceled],
              ] as const
              const last = parts.map(([, v]) => v > 0).lastIndexOf(true)
              let y = TOP + PLOT_H
              return (
                <g key={b.start} className={at === i ? 'on' : undefined}>
                  {parts.map(([kind, v], k) => {
                    if (!v) return null
                    // A 2px gap of the surface between the stacked parts.
                    const ph = Math.max(1, h(v) - (k > 0 && y < TOP + PLOT_H ? 2 : 0))
                    y -= h(v)
                    return <path key={kind} className={`bar ${kind}`} d={columnPath(bx, y, barW, ph, k === last)} />
                  })}
                  <rect className="hit" x={x(i)} y={TOP} width={band} height={PLOT_H} onMouseEnter={() => setAt(i)} />
                </g>
              )
            })}
            <XAxis buckets={buckets} interval={interval} x={x} band={band} />
          </svg>
        )}
        {hovered && (
          <Tooltip b={hovered} interval={interval} x={x(at!) + band / 2} width={width}>
            <TipRow value={hovered.runs} label={tr('runs')} />
            <TipRow value={hovered.successful} label={tr('successful')} kind="ok" />
            <TipRow value={hovered.failed} label={tr('failed')} kind="fail" />
            <TipRow value={hovered.canceled} label={tr('canceled')} kind="cancel" />
          </Tooltip>
        )}
      </div>
    </figure>
  )
}

function LineChart({ buckets, interval, label, value, kind, top: fixedTop, format }: ChartProps & { value: (b: TrendBucket) => number | null; kind: string; top?: number; format: (v: number) => string }) {
  const [ref, width] = useWidth<HTMLDivElement>()
  const { at, setAt, onKey } = useCursor(buckets.length)
  const values = buckets.map(value)
  const max = Math.max(0, ...values.map((v) => v ?? 0))
  const scale = fixedTop ? { top: fixedTop, ticks: [0, 0.25, 0.5, 0.75, 1].map((t) => t * fixedTop) } : kind === 'ink' ? durationScale(max) : niceScale(max)
  const plotW = Math.max(0, width - LEFT)
  const band = buckets.length ? plotW / buckets.length : 0
  const x = (i: number) => LEFT + i * band
  const cx = (i: number) => x(i) + band / 2
  const y = (v: number) => TOP + PLOT_H - (v / scale.top) * PLOT_H
  // A bucket without a value breaks the line.
  let d = ''
  values.forEach((v, i) => {
    if (v === null) return
    d += `${i > 0 && values[i - 1] !== null ? 'L' : 'M'}${cx(i)},${y(v)}`
  })
  const lastAt = values.map((v) => v !== null).lastIndexOf(true)
  const hovered = at !== null ? buckets[at] : null
  const dot = at !== null && values[at] !== null ? at : null
  return (
    <figure className="chart">
      <figcaption>
        <span className="chart-title">{label}</span>
      </figcaption>
      <div ref={ref} className="chart-box" style={{ height: TOP + PLOT_H + AXIS_H }}>
        {width > 0 && (
          <svg width={width} height={TOP + PLOT_H + AXIS_H} role="img" aria-label={tr('{label}: the table below has the values', { label })} tabIndex={0} onKeyDown={onKey} onBlur={() => setAt(null)} onMouseLeave={() => setAt(null)}>
            <YAxis ticks={scale.ticks} top={scale.top} width={width} format={format} />
            {at !== null && <line className="crosshair" x1={cx(at)} x2={cx(at)} y1={TOP} y2={TOP + PLOT_H} />}
            <path className={`line ${kind}`} d={d} />
            {lastAt >= 0 && (
              <>
                <circle className={`dot ${kind}`} cx={cx(lastAt)} cy={y(values[lastAt]!)} r={4} />
                <text className="chart-end" x={cx(lastAt) - 6} y={y(values[lastAt]!) - 8} textAnchor="end">
                  {format(values[lastAt]!)}
                </text>
              </>
            )}
            {dot !== null && <circle className={`dot ${kind}`} cx={cx(dot)} cy={y(values[dot]!)} r={4} />}
            {buckets.map((b, i) => (
              <rect key={b.start} className="hit" x={x(i)} y={TOP} width={band} height={PLOT_H} onMouseEnter={() => setAt(i)} />
            ))}
            <XAxis buckets={buckets} interval={interval} x={x} band={band} />
          </svg>
        )}
        {hovered && (
          <Tooltip b={hovered} interval={interval} x={cx(at!)} width={width}>
            <TipRow value={values[at!] === null ? '—' : format(values[at!]!)} label={label.toLowerCase()} kind={kind} />
            <TipRow value={hovered.runs} label={tr('runs')} />
          </Tooltip>
        )}
      </div>
    </figure>
  )
}

function TrendsPanel({ state, params, period, onInterval }: { state: ReportState; params: Record<string, string>; period: ReportPeriod | undefined; onInterval: (i: Interval) => void }) {
  const r = useReport<{ results: TrendBucket[]; period: ReportPeriod }>('trends', state.interval ? { ...params, interval: state.interval } : params)
  const [table, setTable] = useState(false)
  const interval = r.data?.period.interval ?? 'day'
  const buckets = r.data?.results ?? []
  const per = interval === 'week' ? tr('per week') : tr('per day')
  const cols: Column<TrendBucket>[] = [
    { key: 'start', header: interval === 'week' ? tr('Week of') : tr('Day'), render: (b) => <span className="small nowrap">{bucketLabel(b.start, interval, true)}</span> },
    { key: 'runs', header: tr('Runs'), className: 'num', render: (b) => num(b.runs) },
    { key: 'ok', header: tr('Successful'), className: 'num', render: (b) => num(b.successful) },
    { key: 'failed', header: tr('Failed'), className: 'num', render: (b) => num(b.failed) },
    { key: 'canceled', header: tr('Canceled'), className: 'num', render: (b) => num(b.canceled) },
    { key: 'rate', header: tr('Failure rate'), className: 'num', render: (b) => <span className="small">{percent(b.failure_rate)}</span> },
    { key: 'median', header: tr('Median duration'), className: 'num', render: (b) => <span className="small mono">{duration(b.median_duration)}</span> },
  ]
  return (
    <Panel
      title={<h2>{tr('Trends')}</h2>}
      actions={
        <>
          <div className="segmented sm" role="radiogroup" aria-label={tr('Interval')}>
            {(['day', 'week'] as const).map((i) => (
              <label key={i} className={interval === i ? 'on' : ''}>
                <input type="radio" name="report-interval" checked={interval === i} onChange={() => onInterval(i)} />
                {i === 'day' ? tr('Days') : tr('Weeks')}
              </label>
            ))}
          </div>
          <button className="btn sm" aria-pressed={table} onClick={() => setTable(!table)}>
            {table ? tr('Charts') : tr('Table')}
          </button>
          <CsvButton report="trends" columns={trendCsv} rows={r.data?.results} period={period} />
        </>
      }
      pad={!table}
    >
      <ErrorNote error={r.error} />
      {table ? (
        <DataTable label={tr('Trends')} rows={buckets} rowKey={(b) => b.start} columns={cols} loading={r.isPending} keyboard={false} />
      ) : (
        <div className={`chart-grid report-chart${r.isPlaceholderData ? ' stale' : ''}`}>
          <RunsChart buckets={buckets} interval={interval} label={tr('Runs {per}', { per })} />
          <LineChart buckets={buckets} interval={interval} label={tr('Failure rate {per}', { per })} value={(b) => b.failure_rate} kind="fail" top={1} format={percent} />
          {state.filters.template ? (
            <LineChart buckets={buckets} interval={interval} label={tr('Median duration {per}', { per })} value={(b) => b.median_duration} kind="ink" format={duration} />
          ) : (
            <p className="small muted chart-hint">{tr('Pick a template to see its median duration over time.')}</p>
          )}
        </div>
      )}
    </Panel>
  )
}

type Automated = HostsReport['automated'][number]
type Failing = HostsReport['most_failed'][number]

function HostsPanel({ params, period }: { params: Record<string, string>; period: ReportPeriod | undefined }) {
  const r = useReport<HostsReport>('hosts', params)
  const month = (m: string) => new Intl.DateTimeFormat(undefined, { month: 'long', year: 'numeric', timeZone: 'UTC' }).format(new Date(m + '-01T00:00:00Z'))
  const automated: Column<Automated>[] = [
    { key: 'month', header: tr('Month'), render: (a) => <span className="small nowrap">{month(a.month)}</span> },
    { key: 'org', header: tr('Organization'), render: (a) => (a.organization ? <Link to={`/organizations/${a.organization}`}>{a.name ?? asIs(`#${a.organization}`)}</Link> : <span className="faint">{tr('None')}</span>) },
    { key: 'hosts', header: tr('Hosts automated'), className: 'num', render: (a) => num(a.hosts) },
  ]
  const failing: Column<Failing>[] = [
    {
      key: 'host',
      header: tr('Host'),
      render: (h) =>
        h.host ? (
          <Link to={`/hosts/${h.host}`} className="cell-title mono">
            {h.host_name}
          </Link>
        ) : (
          <span className="mono">
            {h.host_name} <span className="faint small">{tr('(deleted)')}</span>
          </span>
        ),
    },
    { key: 'inv', header: tr('Inventory'), render: (h) => (h.inventory ? <Link to={`/inventories/${h.inventory}/hosts`} className="small">{h.inventory_name}</Link> : <span className="faint">—</span>) },
    { key: 'runs', header: tr('Runs'), className: 'num', render: (h) => num(h.runs) },
    { key: 'failed', header: tr('Failed'), className: 'num', render: (h) => num(h.failed) },
    { key: 'unreachable', header: tr('Unreachable'), className: 'num', render: (h) => num(h.unreachable) },
  ]
  return (
    <div className={`report-pair${r.isPlaceholderData ? ' stale' : ''}`}>
      <Panel title={tr('Hosts automated per month')} actions={<CsvButton report="hosts-automated" columns={automatedCsv} rows={r.data?.automated} period={period} />} pad={false}>
        <DataTable label={tr('Hosts automated per month')} rows={r.data?.automated} rowKey={(a) => `${a.month}:${a.organization}`} columns={automated} loading={r.isPending} error={r.error} keyboard={false} empty={<Empty title={tr('No host automated in this period')} />} />
      </Panel>
      <Panel title={tr('Hosts that failed most')} actions={<CsvButton report="hosts-failed" columns={mostFailedCsv} rows={r.data?.most_failed} period={period} />} pad={false}>
        <DataTable
          label={tr('Hosts that failed most')}
          rows={r.data?.most_failed}
          rowKey={(h) => `${h.host_name}:${h.host}`}
          columns={failing}
          loading={r.isPending}
          error={r.error}
          keyboard={false}
          empty={<Empty title={tr('No host failed in this period')} />}
          footer={<div className="table-foot"><span>{tr('Runs in which the host failed a task, or could not be reached. The 50 worst.')}</span></div>}
        />
      </Panel>
    </div>
  )
}

type ByUser = LaunchesReport['by_user'][number]
type ByTeam = LaunchesReport['by_team'][number]
type ByType = LaunchesReport['by_launch_type'][number]

function LaunchesPanel({ params, period }: { params: Record<string, string>; period: ReportPeriod | undefined }) {
  const r = useReport<LaunchesReport>('launches', params)
  const counts: Column<LaunchRow>[] = [
    { key: 'runs', header: tr('Runs'), className: 'num', render: (l) => num(l.runs) },
    { key: 'failed', header: tr('Failed'), className: 'num', render: (l) => num(l.failed) },
    { key: 'rate', header: tr('Success rate'), className: 'num', render: (l) => <Rate rate={l.success_rate} /> },
  ]
  const byUser: Column<ByUser>[] = [
    {
      key: 'user',
      header: tr('User'),
      render: (u) => (u.username ? <Link to={`/jobs?q=${encodeURIComponent(`by:${u.username}`)}`}>{u.username}</Link> : <span className="faint">{u.user ? asIs(`#${u.user}`) : tr('No user (schedules, callbacks…)')}</span>),
    },
    ...counts,
  ]
  const byTeam: Column<ByTeam>[] = [{ key: 'team', header: tr('Team'), render: (t) => <Link to={`/teams/${t.team}`}>{t.name}</Link> }, ...counts]
  const byType: Column<ByType>[] = [
    {
      key: 'type',
      header: tr('Launch type'),
      render: (l) =>
        l.launch_type === 'portal' || l.launch_type === 'event_rule' ? (
          <span>{launchTypeLabel(l.launch_type)}</span>
        ) : (
          <Link to={`/jobs?q=${encodeURIComponent(`launch:${l.launch_type}`)}`}>{launchTypeLabel(l.launch_type)}</Link>
        ),
    },
    ...counts,
  ]
  const stale = r.isPlaceholderData ? ' stale' : ''
  return (
    <div className={`report-triple${stale}`}>
      <Panel title={tr('Launched by user')} actions={<CsvButton report="launches-users" columns={byUserCsv} rows={r.data?.by_user} period={period} />} pad={false}>
        <DataTable label={tr('Launched by user')} rows={r.data?.by_user} rowKey={(u) => u.user ?? 0} columns={byUser} loading={r.isPending} error={r.error} keyboard={false} empty={<Empty title={tr('No run in this period')} />} />
      </Panel>
      <Panel title={tr('Launched by team')} actions={<CsvButton report="launches-teams" columns={byTeamCsv} rows={r.data?.by_team} period={period} />} pad={false}>
        <DataTable
          label={tr('Launched by team')}
          rows={r.data?.by_team}
          rowKey={(t) => t.team}
          columns={byTeam}
          loading={r.isPending}
          error={r.error}
          keyboard={false}
          empty={<Empty title={tr('No team member launched a run')} />}
          footer={<div className="table-foot"><span>{tr('A team counts the runs its members launched; a user in two teams counts in both.')}</span></div>}
        />
      </Panel>
      <Panel title={tr('Launched how')} actions={<CsvButton report="launches-types" columns={byLaunchTypeCsv} rows={r.data?.by_launch_type} period={period} />} pad={false}>
        <DataTable label={tr('Launched how')} rows={r.data?.by_launch_type} rowKey={(l) => l.launch_type} columns={byType} loading={r.isPending} error={r.error} keyboard={false} empty={<Empty title={tr('No run in this period')} />} />
      </Panel>
    </div>
  )
}

function SaveReportDialog({ onClose, onSave }: { onClose: () => void; onSave: (name: string) => void }) {
  const [name, setName] = useState('')
  return (
    <Dialog
      title={tr('Save report')}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" disabled={!name.trim()} onClick={() => onSave(name.trim())}>
            {tr('Save')}
          </button>
        </>
      }
    >
      <div className="field">
        <label htmlFor="report-name">{tr('Name')}</label>
        <input id="report-name" className="input" value={name} onChange={(e) => setName(e.target.value)} onKeyDown={(e) => e.key === 'Enter' && name.trim() && onSave(name.trim())} placeholder={tr('Prod deploys · last 90 days')} />
        <span className="help">{tr('Saved with your account, with the period and filters: a chip above the report in every browser you sign in to.')}</span>
      </div>
    </Dialog>
  )
}
