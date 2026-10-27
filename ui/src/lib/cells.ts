import { statusKind } from './format'

/**
 * What a cell shows. One vocabulary for job results (successful, failed,
 * running…) and per-host task results (ok, changed, skipped…): "pending" is
 * a run that has not started, "none" a task that did not run on a host,
 * "empty" a slot with no run at all.
 */
export type CellState = 'ok' | 'changed' | 'failed' | 'unreachable' | 'error' | 'skipped' | 'canceled' | 'running' | 'pending' | 'none' | 'empty'

export type CellSize = 'xs' | 'sm' | 'md' | 'lg' | 'bar' | 'fill'

/** The cell of a job status. */
export function jobCell(status: string | null | undefined): CellState {
  switch (statusKind(status)) {
    case 'ok':
      return 'ok'
    case 'fail':
      return 'failed'
    case 'error':
      return 'error'
    case 'run':
      return 'running'
    case 'pending':
      return 'pending'
    case 'cancel':
      return 'canceled'
    default:
      return 'empty'
  }
}
