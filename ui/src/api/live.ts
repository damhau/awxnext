// One websocket per tab, shared by every component. Components declare the
// groups they need (e.g. jobs: ["status_changed"], job_events: [42]); the
// union is (re)sent whenever it changes, and messages fan out to listeners.
import { useEffect, useRef } from 'react'
import { cookie } from './client'

export interface LiveMessage {
  group_name: string
  type?: string
  [k: string]: unknown
}

type Groups = Record<string, (string | number)[]>
type Handler = (m: LiveMessage) => void

interface Sub {
  groups: Groups
  handler: Handler
}

class Live {
  private ws: WebSocket | null = null
  private subs = new Set<Sub>()
  private sent = ''
  private retry = 0
  private timer: number | undefined
  private closed = false
  connected = false
  private stateListeners = new Set<(c: boolean) => void>()

  subscribe(groups: Groups, handler: Handler): () => void {
    const sub = { groups, handler }
    this.subs.add(sub)
    this.ensure()
    this.sync()
    return () => {
      this.subs.delete(sub)
      this.sync()
    }
  }

  onState(fn: (c: boolean) => void): () => void {
    this.stateListeners.add(fn)
    return () => this.stateListeners.delete(fn)
  }

  stop() {
    this.closed = true
    this.ws?.close()
  }

  private ensure() {
    if (this.ws || this.closed) return
    const proto = location.protocol === 'https:' ? 'wss:' : 'ws:'
    const ws = new WebSocket(`${proto}//${location.host}/websocket/`)
    this.ws = ws
    ws.onopen = () => {
      this.retry = 0
      this.sent = ''
    }
    ws.onmessage = (ev) => {
      let m: LiveMessage
      try {
        m = JSON.parse(ev.data)
      } catch {
        return
      }
      if ((m as { accept?: boolean }).accept) {
        this.setState(true)
        this.sync()
        return
      }
      if (!m.group_name) return
      for (const s of this.subs) {
        if (matches(s.groups, m)) s.handler(m)
      }
    }
    ws.onclose = () => {
      this.ws = null
      this.setState(false)
      if (this.closed) return
      const delay = Math.min(30_000, 1000 * 2 ** this.retry++)
      window.clearTimeout(this.timer)
      this.timer = window.setTimeout(() => this.ensure(), delay)
    }
  }

  private setState(c: boolean) {
    this.connected = c
    this.stateListeners.forEach((f) => f(c))
  }

  private sync() {
    const union: Record<string, Set<string>> = {}
    for (const s of this.subs) {
      for (const [g, ids] of Object.entries(s.groups)) {
        union[g] ??= new Set()
        ids.forEach((id) => union[g].add(String(id)))
      }
    }
    const groups: Record<string, (string | number)[]> = {}
    for (const [g, ids] of Object.entries(union)) {
      groups[g] = [...ids].sort().map((x) => (/^\d+$/.test(x) ? Number(x) : x))
    }
    const body = JSON.stringify(groups)
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN || !this.connected || body === this.sent) return
    this.sent = body
    this.ws.send(JSON.stringify({ groups, xrftoken: cookie('csrftoken') ?? '' }))
  }
}

function matches(groups: Groups, m: LiveMessage): boolean {
  const ids = groups[m.group_name]
  if (!ids) return false
  if (m.group_name === 'jobs') return true
  const id = m.job ?? m.project_update ?? m.inventory_update ?? m.ad_hoc_command ?? m.system_job ?? m.workflow_job_id ?? m.unified_job_id
  return ids.some((x) => String(x) === String(id))
}

export const live = new Live()

/** Subscribe to live messages for the component's lifetime. */
export function useLive(groups: Groups | null, handler: Handler) {
  const ref = useRef(handler)
  ref.current = handler
  const key = groups ? JSON.stringify(groups) : ''
  useEffect(() => {
    if (!key) return
    return live.subscribe(JSON.parse(key), (m) => ref.current(m))
  }, [key])
}

/** Event group name for a unified job type. */
export function eventGroup(type: string): string {
  switch (type) {
    case 'job':
      return 'job_events'
    case 'project_update':
      return 'project_update_events'
    case 'inventory_update':
      return 'inventory_update_events'
    case 'ad_hoc_command':
      return 'ad_hoc_command_events'
    case 'system_job':
      return 'system_job_events'
    case 'workflow_job':
      return 'workflow_events'
  }
  return 'job_events'
}
