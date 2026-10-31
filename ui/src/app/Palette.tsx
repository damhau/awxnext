import { useQuery } from '@tanstack/react-query'
import { useEffect, useMemo, useRef, useState, type ReactNode } from 'react'
import { createPortal } from 'react-dom'
import { useNavigate } from 'react-router'
import { api, type Page } from '../api/client'
import type { Base, Job, Template } from '../api/types'
import { Icon } from '../components/icons'
import { ago, jobPath, statusKind, statusLabel, templatePath, typeLabel } from '../lib/format'
import { load, save } from '../lib/storage'
import { useOrg, useSession } from './context'
import { tr, trn } from '../lib/i18n'

interface Item {
  key: string
  group: string
  glyph: string
  tone?: 'act' | 'tpl' | 'bad' | 'good'
  title: string
  meta?: ReactNode
  hint?: string
  run: () => void
  launch?: () => void
}

const scopes = ['all', 'templates', 'jobs', 'hosts'] as const
type Scope = (typeof scopes)[number]

/** Recently opened palette targets, newest first. */
export function remember(entry: { title: string; path: string; glyph: string; meta?: string }) {
  const list = load<{ title: string; path: string; glyph: string; meta?: string }[]>('palette-recent', [])
  save('palette-recent', [entry, ...list.filter((x) => x.path !== entry.path)].slice(0, 8))
}

function highlight(text: string, q: string): ReactNode {
  if (!q) return text
  const i = text.toLowerCase().indexOf(q.toLowerCase())
  if (i < 0) return text
  return (
    <>
      {text.slice(0, i)}
      <mark>{text.slice(i, i + q.length)}</mark>
      {text.slice(i + q.length)}
    </>
  )
}

export function Palette({ onClose, onAbout, onShortcuts }: { onClose: () => void; onAbout?: () => void; onShortcuts?: () => void }) {
  const [q, setQ] = useState('')
  const [hi, setHi] = useState(0)
  const [scope, setScope] = useState<Scope>('all')
  const nav = useNavigate()
  const { org, scope: orgScope } = useOrg()
  const { logout } = useSession()
  const input = useRef<HTMLInputElement>(null)
  const commandMode = q.startsWith('>')
  const term = (commandMode ? q.slice(1) : q).trim()
  const debounced = useDebounced(term, 120)

  const search = useQuery({
    queryKey: ['palette', debounced, scope, orgScope],
    enabled: !commandMode && debounced.length > 0,
    queryFn: async ({ signal }) => {
      const want = (s: Scope) => scope === 'all' || scope === s
      const num = /^#?\d+$/.test(debounced) ? Number(debounced.replace('#', '')) : null
      const [tpls, jobs, hosts, invs] = await Promise.all([
        want('templates')
          ? api.get<Page<Template>>('unified_job_templates/', { search: debounced, type__in: 'job_template,workflow_job_template', page_size: scope === 'all' ? 5 : 15, ...orgScope }, signal)
          : null,
        want('jobs')
          ? num
            ? api.get<Page<Job>>('unified_jobs/', { id: num }, signal)
            : api.get<Page<Job>>('unified_jobs/', { search: debounced, order_by: '-id', page_size: scope === 'all' ? 4 : 15, not__type: 'workflow_approval' }, signal)
          : null,
        want('hosts') ? api.get<Page<Base>>('hosts/', { search: debounced, page_size: scope === 'all' ? 4 : 15 }, signal) : null,
        scope === 'all' ? api.get<Page<Base>>('inventories/', { search: debounced, page_size: 3, ...orgScope }, signal) : null,
      ])
      return { tpls: tpls?.results ?? [], jobs: jobs?.results ?? [], hosts: hosts?.results ?? [], invs: invs?.results ?? [] }
    },
    placeholderData: (prev) => prev,
  })

  const go = (path: string, title: string, glyph: string, meta?: string) => {
    remember({ title, path, glyph, meta })
    nav(path)
    onClose()
  }

  const commands: Item[] = useMemo(
    () =>
      [
        [tr('New job template'), '/templates/new/job'],
        [tr('New workflow template'), '/templates/new/workflow'],
        [tr('New inventory'), '/inventories/new'],
        [tr('New project'), '/projects/new'],
        [tr('New credential'), '/credentials/new'],
        [tr('Go to jobs that failed today'), '/jobs?q=status:failed'],
        [tr('Go to running jobs'), '/jobs?q=status:running'],
        [tr('Go to approvals'), '/approvals'],
        [tr('Go to settings'), '/settings'],
        [tr('Go to activity stream'), '/activity'],
      ]
        .map(([title, path]): Item => ({ key: 'c' + path, group: tr('Commands'), glyph: '›', tone: 'act' as const, title, run: () => go(path, title, '›') }))
        .concat(
          [
            [tr('Keyboard shortcuts'), onShortcuts, '?'],
            [tr('About awxnext'), onAbout, undefined],
          ]
            .filter((x): x is [string, () => void, string | undefined] => !!x[1])
            .map(([title, fn, hint]) => ({ key: 'c-' + title, group: tr('Commands'), glyph: '›', tone: 'act' as const, title, hint, run: () => (onClose(), fn()) })),
        ),
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [],
  )

  const items: Item[] = useMemo(() => {
    const out: Item[] = []
    if (commandMode || !term) {
      const cmds = commands.filter((c) => c.title.toLowerCase().includes(term.toLowerCase()))
      if (!term && !commandMode) {
        const recent = load<{ title: string; path: string; glyph: string; meta?: string }[]>('palette-recent', [])
        recent.forEach((r) => out.push({ key: 'r' + r.path, group: tr('Recent'), glyph: r.glyph, title: r.title, meta: r.meta, run: () => go(r.path, r.title, r.glyph, r.meta) }))
        out.push({ key: 'c-signout', group: tr('Commands'), glyph: '›', tone: 'act', title: tr('Sign out'), run: () => logout() })
      }
      out.push(...cmds)
      return out
    }
    const d = search.data
    if (!d) return out
    for (const t of d.tpls.slice(0, 3)) {
      if (!t.summary_fields?.user_capabilities?.start) continue
      const launch = () => go(`/launch/${t.type === 'workflow_job_template' ? 'workflow' : 'job'}/${t.id}`, `Launch ${t.name}`, '▶')
      out.push({ key: 'l' + t.id, group: tr('Actions'), glyph: '▶', tone: 'act', title: tr('Launch {name}…', { name: t.name }), meta: tr('opens launch form'), hint: '⌘↵', run: launch })
    }
    for (const t of d.tpls) {
      const path = templatePath(t)
      const folder = t.summary_fields?.folder?.name
      out.push({
        key: 't' + t.id,
        group: tr('Templates'),
        glyph: t.type === 'workflow_job_template' ? 'W' : 'T',
        tone: 'tpl',
        title: t.name,
        meta: [t.type === 'workflow_job_template' ? 'workflow' : null, folder, !org ? t.summary_fields?.organization?.name : null].filter(Boolean).join(' · '),
        run: () => go(path, t.name, 'T'),
        launch: t.summary_fields?.user_capabilities?.start ? () => go(`/launch/${t.type === 'workflow_job_template' ? 'workflow' : 'job'}/${t.id}`, t.name, '▶') : undefined,
      })
    }
    for (const j of d.jobs) {
      const k = statusKind(j.status)
      out.push({
        key: 'j' + j.id,
        group: tr('Jobs'),
        glyph: '#',
        tone: k === 'fail' || k === 'error' ? 'bad' : k === 'ok' ? 'good' : undefined,
        title: tr('{id} {name}', { id: j.id, name: j.name }),
        meta: tr('{toLowerCase} {ago} · {typeLabel}', { toLowerCase: statusLabel(j.status).toLowerCase(), ago: ago(j.finished ?? j.started ?? j.created), typeLabel: typeLabel(j.type) }),
        run: () => go(jobPath(j), `#${j.id} ${j.name}`, '#'),
      })
    }
    for (const h of d.hosts) {
      out.push({
        key: 'h' + h.id,
        group: tr('Hosts'),
        glyph: 'H',
        title: h.name,
        meta: h.summary_fields?.inventory?.name,
        run: () => go(`/hosts/${h.id}`, h.name, 'H', h.summary_fields?.inventory?.name),
      })
    }
    for (const i of d.invs) {
      out.push({ key: 'i' + i.id, group: tr('Inventories'), glyph: 'I', title: i.name, meta: trn(i.total_hosts ?? 0, '{n} host', '{n} hosts'), run: () => go(`/inventories/${i.id}`, i.name, 'I') })
    }
    return out
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [search.data, term, commandMode, commands, org])

  useEffect(() => setHi(0), [term, scope])
  useEffect(() => {
    document.querySelector('.palette-item.hi')?.scrollIntoView({ block: 'nearest' })
  }, [hi])

  const onKey = (e: React.KeyboardEvent) => {
    if (e.key === 'ArrowDown') {
      e.preventDefault()
      setHi((h) => Math.min(items.length - 1, h + 1))
    } else if (e.key === 'ArrowUp') {
      e.preventDefault()
      setHi((h) => Math.max(0, h - 1))
    } else if (e.key === 'Enter') {
      e.preventDefault()
      const it = items[hi]
      if (!it) return
      if ((e.metaKey || e.ctrlKey) && it.launch) it.launch()
      else it.run()
    } else if (e.key === 'Escape') {
      onClose()
    } else if (e.key === 'Tab') {
      e.preventDefault()
      setScope((s) => scopes[(scopes.indexOf(s) + (e.shiftKey ? scopes.length - 1 : 1)) % scopes.length])
    }
  }

  let lastGroup = ''
  return createPortal(
    <div className="overlay" style={{ paddingTop: 96 }} onMouseDown={(e) => e.target === e.currentTarget && onClose()}>
      <div className="palette" role="dialog" aria-modal="true" aria-label={tr('Command palette')}>
        <div className="palette-input">
          <Icon name="search" />
          <label htmlFor="palette" className="sr-only">
            {tr('Search or run a command')}
          </label>
          <input
            id="palette"
            ref={input}
            autoFocus
            value={q}
            placeholder={tr('Search templates, jobs, hosts…  or > for commands')}
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={onKey}
            role="combobox"
            aria-expanded="true"
            aria-controls="palette-results"
            autoComplete="off"
          />
          <span className="small faint nowrap">
            {scope !== 'all' ? tr('only {scope}', { scope }) : org ? tr('in {name}', { name: org.name }) : tr('everywhere')}
          </span>
        </div>
        <div className="palette-results" role="listbox" id="palette-results">
          {items.length === 0 && (
            <div className="palette-item faint">{term ? (search.isFetching ? tr('Searching…') : tr('Nothing matches. Try fewer letters, or press Tab to narrow the scope.')) : tr('Type to search.')}</div>
          )}
          {items.map((it, i) => {
            const head = it.group !== lastGroup ? <div className="palette-group">{it.group}</div> : null
            lastGroup = it.group
            return (
              <div key={it.key}>
                {head}
                <div
                  role="option"
                  aria-selected={i === hi}
                  className={`palette-item ${i === hi ? 'hi' : ''}`}
                  onMouseMove={() => setHi(i)}
                  onClick={() => it.run()}
                >
                  <span className={`glyph ${it.tone ?? ''}`} aria-hidden="true">
                    {it.glyph}
                  </span>
                  <span className="grow ellipsis">
                    {highlight(it.title, term)}
                    {it.meta && <span className="meta">{it.meta}</span>}
                  </span>
                  {it.hint && <span className="mono small faint">{it.hint}</span>}
                </div>
              </div>
            )
          })}
        </div>
        <div className="palette-foot">
          <span>
            <span className="mono">↑↓</span> {tr('move')}
          </span>
          <span>
            <span className="mono">↵</span> {tr('open')}
          </span>
          <span>
            <span className="mono">⌘↵</span> {tr('launch')}
          </span>
          <span>
            <span className="mono">{tr('tab')}</span> {tr('only templates · jobs · hosts')}
          </span>
          <span style={{ marginLeft: 'auto' }}>
            <span className="mono">{tr('>')}</span> {tr('commands')}
          </span>
        </div>
      </div>
    </div>,
    document.body,
  )
}

function useDebounced<T>(v: T, ms: number): T {
  const [d, setD] = useState(v)
  useEffect(() => {
    const t = window.setTimeout(() => setD(v), ms)
    return () => window.clearTimeout(t)
  }, [v, ms])
  return d
}
