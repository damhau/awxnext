import type { ReactNode } from 'react'
import { Icon, type IconName } from '../../components/icons'
import type { QuotaSlot } from '../../lib/quotas'
import type { StepState } from '../../lib/portal'

/** A heatmap cell: a step's state, a quota's place, a day of a lifetime. */
export function Cell({ state, size }: { state: StepState | QuotaSlot; size?: 'sm' | 'lg' }) {
  return <span className={`portal-cell ${size ?? ''}`} data-state={state} aria-hidden="true" />
}

export function Pill({ tone, children }: { tone: 'neutral' | 'waiting' | 'running' | 'ok' | 'denied' | 'fail'; children: ReactNode }) {
  return <span className={`portal-pill ${tone}`}>{children}</span>
}

const itemIcons: IconName[] = ['server', 'box', 'key', 'shield', 'users', 'team', 'inventory', 'host', 'plug', 'window', 'terminal', 'workflow', 'bolt', 'folder']

/** An item's icon: the one it names, or a box. */
export function ItemIcon({ name, size = 22 }: { name: string; size?: number }) {
  const icon = (itemIcons as string[]).includes(name) ? (name as IconName) : 'box'
  return (
    <span className="portal-item-icon" aria-hidden="true">
      <Icon name={icon} size={size} />
    </span>
  )
}
