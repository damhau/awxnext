import { useEffect, useLayoutEffect, useMemo, useRef, useState } from 'react'
import { Icon } from '../../components/icons'
import { parseAnsi } from '../../lib/ansi'
import type { Line } from './events'

const ROW = 20
const OVERSCAN = 40

/**
 * Job output terminal. Rows have a fixed height, so only the visible slice
 * is rendered and outputs of any size scroll smoothly.
 */
export function Output({
  lines,
  running,
  downloadUrl,
  host,
  onHost,
  hosts,
  focusLine,
}: {
  lines: Line[]
  running: boolean
  downloadUrl: string
  host: string
  onHost: (h: string) => void
  hosts: string[]
  /** Scroll to this output line number when it changes. */
  focusLine?: { n: number; at: number } | null
}) {
  const [q, setQ] = useState('')
  const [failedOnly, setFailedOnly] = useState(false)
  const [changedOnly, setChangedOnly] = useState(false)
  const [collapsed, setCollapsed] = useState<Set<number>>(new Set())
  const [hit, setHit] = useState(0)
  const [follow, setFollow] = useState(running)
  const box = useRef<HTMLDivElement>(null)
  const [scroll, setScroll] = useState({ top: 0, height: 600 })

  const filtering = failedOnly || changedOnly || !!host
  const visible = useMemo(() => {
    const keepHeader = (l: Line) => l.kind === 'play' || l.kind === 'task' || l.kind === 'recap'
    let out = lines
    if (filtering) {
      const match = (l: Line) =>
        (!failedOnly || l.kind === 'fatal') && (!changedOnly || l.kind === 'changed' || l.kind === 'diff' || l.event.changed) && (!host || l.event.host_name === host)
      // Keep headers only when something under them matches.
      const taskHas = new Set<number>()
      for (const l of lines) if (!keepHeader(l) && match(l)) taskHas.add(l.task)
      out = lines.filter((l) => (keepHeader(l) ? l.kind === 'recap' || taskHas.has(l.task) || l.kind === 'play' : match(l)))
      // Drop play headers that ended up with nothing after them.
      out = out.filter((l, i) => l.kind !== 'play' || (out[i + 1] && out[i + 1].kind !== 'play'))
    }
    if (collapsed.size) out = out.filter((l) => l.kind === 'task' || l.kind === 'play' || !collapsed.has(l.task))
    return out
  }, [lines, failedOnly, changedOnly, host, filtering, collapsed])

  const needle = q.trim().toLowerCase()
  const hits = useMemo(() => (needle ? visible.reduce<number[]>((acc, l, i) => (l.plain.toLowerCase().includes(needle) ? (acc.push(i), acc) : acc), []) : []), [visible, needle])
  useEffect(() => setHit(0), [needle])
  const firstFatal = useMemo(() => visible.findIndex((l) => l.kind === 'fatal'), [visible])

  const scrollTo = (i: number) => {
    const el = box.current
    if (!el || i < 0) return
    el.scrollTop = Math.max(0, i * ROW - el.clientHeight / 3)
  }
  useEffect(() => {
    if (hits.length) scrollTo(hits[Math.min(hit, hits.length - 1)])
  }, [hit, hits])
  useEffect(() => {
    if (!focusLine) return
    const i = visible.findIndex((l) => l.n >= focusLine.n)
    if (i >= 0) {
      setFollow(false)
      scrollTo(i)
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusLine?.at])
  useEffect(() => setFollow(running), [running])
  useLayoutEffect(() => {
    const el = box.current
    if (el && follow) el.scrollTop = el.scrollHeight
  }, [visible.length, follow])
  useEffect(() => {
    const el = box.current
    if (!el) return
    const ro = new ResizeObserver(() => setScroll({ top: el.scrollTop, height: el.clientHeight }))
    ro.observe(el)
    return () => ro.disconnect()
  }, [])

  const start = Math.max(0, Math.floor(scroll.top / ROW) - OVERSCAN)
  const end = Math.min(visible.length, Math.ceil((scroll.top + scroll.height) / ROW) + OVERSCAN)
  const current = hits[Math.min(hit, hits.length - 1)]
  const hitSet = useMemo(() => new Set(hits), [hits])

  const toggleTask = (idx: number) =>
    setCollapsed((c) => {
      const n = new Set(c)
      if (n.has(idx)) n.delete(idx)
      else n.add(idx)
      return n
    })
  const allTasks = useMemo(() => lines.flatMap((l, i) => (l.kind === 'task' ? [i] : [])), [lines])

  return (
    <section aria-label="Job output" className="term">
      <div className="term-bar">
        <div className="term-search">
          <Icon name="search" size={13} style={{ color: 'var(--term-faint)' }} />
          <label htmlFor="search-out" className="sr-only">
            Search output
          </label>
          <input
            id="search-out"
            value={q}
            placeholder="Search output"
            onChange={(e) => setQ(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === 'Enter' && hits.length) setHit((h) => (e.shiftKey ? (h - 1 + hits.length) % hits.length : (h + 1) % hits.length))
              if (e.key === 'Escape') setQ('')
            }}
          />
          {needle && (
            <span style={{ fontSize: 11, color: 'var(--term-faint)', whiteSpace: 'nowrap' }}>
              {hits.length ? `${Math.min(hit, hits.length - 1) + 1} of ${hits.length}` : 'no match'}
            </span>
          )}
        </div>
        <button className="term-btn" aria-pressed={failedOnly} onClick={() => setFailedOnly((x) => !x)}>
          Failed only
        </button>
        <button className="term-btn" aria-pressed={changedOnly} onClick={() => setChangedOnly((x) => !x)}>
          Changed
        </button>
        {hosts.length > 1 && (
          <select className="term-btn" value={host} onChange={(e) => onHost(e.target.value)} aria-label="Show one host">
            <option value="">Host: all</option>
            {hosts.map((h) => (
              <option key={h} value={h}>
                {h}
              </option>
            ))}
          </select>
        )}
        {allTasks.length > 0 && (
          <button className="term-btn" onClick={() => setCollapsed(collapsed.size ? new Set() : new Set(allTasks))}>
            {collapsed.size ? 'Expand all' : 'Collapse all'}
          </button>
        )}
        <div style={{ marginLeft: 'auto', display: 'flex', gap: 8 }}>
          {firstFatal >= 0 && (
            <button className="term-btn alarm" onClick={() => { setFollow(false); scrollTo(firstFatal) }}>
              ↓ First failure
            </button>
          )}
          {running && (
            <button className="term-btn" aria-pressed={follow} onClick={() => setFollow((f) => !f)} title="Keep the newest output in view">
              Follow
            </button>
          )}
          <a className="term-btn" href={downloadUrl} style={{ display: 'inline-flex', alignItems: 'center', textDecoration: 'none' }}>
            Download
          </a>
        </div>
      </div>
      <div
        ref={box}
        className="term-lines"
        role="log"
        aria-live={running ? 'polite' : 'off'}
        tabIndex={0}
        onScroll={(e) => {
          const el = e.currentTarget
          setScroll({ top: el.scrollTop, height: el.clientHeight })
          const atBottom = el.scrollHeight - el.scrollTop - el.clientHeight < 40
          if (running && follow !== atBottom) setFollow(atBottom)
        }}
      >
        {visible.length === 0 ? (
          <div className="term-empty">{running ? 'Waiting for output…' : filtering ? 'No lines match the filters.' : 'No output.'}</div>
        ) : (
          <div style={{ height: visible.length * ROW, position: 'relative' }}>
            <div style={{ position: 'absolute', top: start * ROW, left: 0, right: 0 }}>
              {visible.slice(start, end).map((l, k) => {
                const i = start + k
                const isTask = l.kind === 'task'
                const bad = l.taskState === 'failed' || l.taskState === 'unreachable'
                const cls = ['term-line', l.kind === 'fatal' ? 'fatal' : '', l.diff ? `diff-${l.diff}` : '', hitSet.has(i) ? 'hit' : '', i === current ? 'current' : '', isTask ? 'task' : '', bad ? 'bad' : ''].join(' ')
                return (
                  <div key={l.n + ':' + i} className={cls} style={{ height: ROW }} onClick={isTask ? () => toggleTask(l.task) : undefined}>
                    <span className="ln">{l.n}</span>
                    <span className="tx">
                      {isTask && <span aria-hidden="true">{collapsed.has(l.task) ? '▸ ' : '▾ '}</span>}
                      {isTask && l.taskNo && (
                        <span className="tnum" data-state={l.taskState} title={`Task ${l.taskNo} in the hosts × tasks grid`}>
                          {l.taskNo}
                        </span>
                      )}
                      {l.kind === 'play' || isTask ? (
                        <span style={{ color: l.kind === 'play' ? '#fff' : '#e9ebe7', fontWeight: l.kind === 'play' ? 500 : 400 }}>{l.plain}</span>
                      ) : (
                        parseAnsi(l.text).map((s, j) => (
                          <span key={j} className={s.cls}>
                            {s.text}
                          </span>
                        ))
                      )}
                      {l.meta && <span className="meta">{l.meta}</span>}
                    </span>
                  </div>
                )
              })}
            </div>
          </div>
        )}
      </div>
    </section>
  )
}
