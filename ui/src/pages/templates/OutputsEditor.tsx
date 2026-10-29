import { Icon } from '../../components/icons'
import { OUTPUT_TYPES, type OutputDecl, type OutputType } from '../../lib/outputs'

/** Edits the outputs a job template declares: one row per set_stats variable. */
export function OutputsEditor({ value, onChange, error }: { value: OutputDecl[]; onChange: (v: OutputDecl[]) => void; error?: string }) {
  const update = (i: number, patch: Partial<OutputDecl>) => onChange(value.map((o, j) => (j === i ? { ...o, ...patch } : o)))
  return (
    <div className="field full">
      <span className="help" style={{ marginTop: 0 }}>
        Variables the playbook publishes with <span className="mono">set_stats</span> (use <span className="mono">run_once: true</span>). A job whose outputs are missing or of the wrong type fails, and workflows
        offer them in step conditions and loops.
      </span>
      {value.length > 0 && (
        <div className="stack" style={{ gap: 6, marginTop: 8 }}>
          {value.map((o, i) => (
            <div key={i} className="row" style={{ gap: 8 }}>
              <input className="input mono" style={{ flex: 2 }} aria-label="Output name" placeholder="regions" value={o.name} onChange={(e) => update(i, { name: e.target.value })} />
              <select className="select" style={{ flex: 1 }} aria-label="Output type" value={o.type} onChange={(e) => update(i, { type: e.target.value as OutputType })}>
                {OUTPUT_TYPES.map((t) => (
                  <option key={t} value={t}>
                    {t}
                  </option>
                ))}
              </select>
              <label className="check small">
                <input type="checkbox" checked={o.required} onChange={(e) => update(i, { required: e.target.checked })} />
                required
              </label>
              <button type="button" className="btn ghost sm icon" aria-label={`Remove output ${o.name}`} onClick={() => onChange(value.filter((_, j) => j !== i))}>
                <Icon name="x" size={12} />
              </button>
            </div>
          ))}
        </div>
      )}
      <div className="row" style={{ marginTop: 8 }}>
        <button type="button" className="btn sm" onClick={() => onChange([...value, { name: '', type: 'string', required: false }])}>
          <Icon name="plus" size={12} />
          Add output
        </button>
      </div>
      {error && <span className="fail-text">{error}</span>}
    </div>
  )
}
