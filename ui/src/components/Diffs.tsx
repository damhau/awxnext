import { diffLines, hunks, tooLargeToCompare, unifiedKind, type TaskDiff } from '../lib/diff'
import { plural } from '../lib/format'
import { tr } from '../lib/i18n'

/** Diff mode: each file the task changed on this host, before and after. */
export function Diffs({ diffs, onJump }: { diffs: TaskDiff[]; onJump?: () => void }) {
  if (!diffs.length) return null
  return (
    <div className="cd-diffs">
      {diffs.map((d, i) => (
        <div key={i}>
          <div className="mono small cd-diff-path" title={d.path}>
            {d.item !== undefined && <span className="faint">{d.item} · </span>}
            {d.path || tr('Changes')}
          </div>
          {d.prepared ? (
            <div className="diff" role="table" aria-label={d.path ? tr('Changes to {path}', { path: d.path }) : tr('Changes to the host')}>
              {d.prepared.replace(/\n$/, '').split('\n').map((l, j) => {
                const k = unifiedKind(l)
                return (
                  <div key={j} role="row" className={k === 'add' || k === 'del' ? k : ''}>
                    <span>{l}</span>
                  </div>
                )
              })}
            </div>
          ) : tooLargeToCompare(d.before, d.after) ? (
            <p className="small muted" style={{ margin: '6px 0 0' }}>
              {tr('Too large to compare here.')}{' '}
              {onJump && (
                <button className="link-btn" onClick={onJump}>
                  {tr('See the diff in the output')}
                </button>
              )}
            </p>
          ) : (
            <div className="diff" role="table" aria-label={d.path ? tr('Diff of {path}', { path: d.path }) : tr('Diff of the change')}>
              {hunks(diffLines(d.before, d.after)).map((l, j) =>
                l.kind === 'skip' ? (
                  <div key={j} role="row" className="skip">
                    <span>⋯ {plural(l.n, 'unchanged line')}</span>
                  </div>
                ) : (
                  <div key={j} role="row" className={l.kind === 'ctx' ? '' : l.kind}>
                    <span className="n">{l.a ?? ''}</span>
                    <span className="n">{l.b ?? ''}</span>
                    <span className="m">{l.kind === 'add' ? '+' : l.kind === 'del' ? '−' : ' '}</span>
                    <span>{l.text}</span>
                  </div>
                ),
              )}
            </div>
          )}
        </div>
      ))}
    </div>
  )
}
