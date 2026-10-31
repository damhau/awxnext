// About awxnext, and the keyboard shortcuts overlay (roadmap M7-15). Both
// open from the account menu, the command palette, and "?" for shortcuts.
import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { api } from '../api/client'
import { dateTimeText } from '../lib/format'
import { isMac, keyCaps, SHORTCUTS } from '../lib/shortcuts'
import { Dialog } from './overlay'
import { KV, Loading } from './ui'
import { tr } from '../lib/i18n'

interface Config {
  version: string
  awxnext?: { version: string; commit: string; built: string; modified: boolean; go: string }
}

const REPO = 'https://github.com/damhau/awxnext'

export function AboutDialog({ onClose }: { onClose: () => void }) {
  const config = useQuery({ queryKey: ['config/'], queryFn: () => api.get<Config>('config/'), staleTime: 300_000 })
  const [copied, setCopied] = useState(false)
  const c = config.data
  const own = c?.awxnext
  const built = own?.built ? new Date(own.built) : null
  const details = own
    ? [`awxnext ${own.version}`, own.commit && `commit ${own.commit}${own.modified ? ' (modified)' : ''}`, own.built && `built ${own.built}`, `AWX API ${c?.version}`, own.go, navigator.userAgent]
        .filter(Boolean)
        .join('\n')
    : ''
  return (
    <Dialog
      title={tr('About awxnext')}
      onClose={onClose}
      footer={
        <>
          {own && (
            <button
              className="btn"
              onClick={() =>
                navigator.clipboard
                  ?.writeText(details)
                  .then(() => setCopied(true))
                  .catch(() => setCopied(false))
              }
            >
              {copied ? tr('Copied') : tr('Copy details')}
            </button>
          )}
          <button className="btn primary" onClick={onClose}>
            {tr('Done')}
          </button>
        </>
      }
    >
      {config.isPending ? (
        <Loading rows={3} />
      ) : !c ? (
        <p className="small muted">{tr('The server’s version could not be read.')}</p>
      ) : (
        <div className="stack" style={{ gap: 14 }}>
          <p style={{ margin: 0 }}>{tr('An automation controller that speaks AWX’s API: the awx CLI, the awx.awx collection and the Terraform provider work against it.')}</p>
          <KV
            items={[
              [tr('Version'), own ? (own.version === 'dev' ? tr('development build') : own.version) : '—'],
              [
                tr('Build'),
                own?.commit ? (
                  <span>
                    <a href={`${REPO}/commit/${own.commit}`} target="_blank" rel="noreferrer" className="mono">
                      {own.commit.slice(0, 12)}
                    </a>
                    {own.modified && <span className="faint"> {tr('with local changes')}</span>}
                    {built && !isNaN(built.getTime()) && <span className="faint"> · {dateTimeText(built)}</span>}
                  </span>
                ) : (
                  <span className="faint">{tr('not recorded')}</span>
                ),
              ],
              [tr('AWX API'), <span key="awx" className="mono">{c.version}</span>],
              [tr('Go'), <span key="go" className="mono">{own?.go ?? '—'}</span>],
            ]}
          />
        </div>
      )}
    </Dialog>
  )
}

export function ShortcutsDialog({ onClose }: { onClose: () => void }) {
  const mac = isMac()
  return (
    <Dialog
      title={tr('Keyboard shortcuts')}
      wide
      onClose={onClose}
      footer={
        <button className="btn primary" onClick={onClose}>
          {tr('Done')}
        </button>
      }
    >
      <div className="shortcuts">
        {SHORTCUTS.map((g) => (
          <section key={g.group}>
            <h3 className="small">{g.group}</h3>
            <dl>
              {g.items.map((s) => (
                <div key={s.keys.join(' ') + s.what}>
                  <dt>
                    {s.keys.map((k, i) => (
                      <span key={i}>
                        {i > 0 && <span className="faint">{s.keys[0] === 'g' ? tr(' then ') : ' '}</span>}
                        {keyCaps(k, mac).map((cap, j) => (
                          <kbd key={j} className="keycap">
                            {cap}
                          </kbd>
                        ))}
                      </span>
                    ))}
                  </dt>
                  <dd>{s.what}</dd>
                </div>
              ))}
            </dl>
          </section>
        ))}
      </div>
    </Dialog>
  )
}
