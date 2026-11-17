import { useState } from 'react'
import { api, ApiError } from '../../api/client'
import { useInvalidate, useList } from '../../api/hooks'
import type { Any } from '../../api/types'
import { useMe } from '../../app/context'
import { Check, FormError, TextInput, useForm } from '../../components/form'
import { Icon } from '../../components/icons'
import { Dialog, useToast } from '../../components/overlay'
import { Ago, Cell, Empty, ErrorNote, Loading, PageHead, Panel } from '../../components/ui'
import { tr } from '../../lib/i18n'
import { CopyButton, useDelete } from '../admin/common'

type Key = Any & { id: number }

function NewKey({ rotating, onClose }: { rotating: boolean; onClose: () => void }) {
  const invalidate = useInvalidate()
  const toast = useToast()
  const f = useForm<{ name: string; email: string; sign_published: boolean }>({ name: '', email: '', sign_published: true })
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const save = async () => {
    setBusy(true)
    setError(null)
    const body: Any = { email: f.values.email, sign_published: f.values.sign_published }
    if (f.values.name.trim()) body.name = f.values.name.trim()
    try {
      await api.post('collection_signing_keys/', body)
      invalidate('collection_signing_keys', 'collection_versions', 'collections')
      toast(rotating ? tr('The new key signs from now on') : tr('Created the signing key'))
      onClose()
    } catch (e) {
      setError(e)
      if (e instanceof ApiError) f.setErrors(e.fields)
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={rotating ? tr('Rotate the signing key') : tr('New signing key')}
      onClose={onClose}
      footer={
        <>
          <button className="btn ghost" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" disabled={busy} onClick={save}>
            {busy ? tr('Creating…') : rotating ? tr('Create and rotate') : tr('Create key')}
          </button>
        </>
      }
    >
      <div className="stack" style={{ gap: 14 }}>
        {rotating && (
          <p className="small" style={{ margin: 0 }}>
            {tr('A new key signs every approval from now on. The current key retires: its signatures stay valid, and the keyring keeps it, until you delete it.')}
          </p>
        )}
        <TextInput label={tr('Name')} optional value={f.values.name} onChange={(v) => f.set('name', v)} error={f.errors.name} help={tr('The name in the key’s user ID, which gpg shows for a good signature. Empty: the server’s default.')} />
        <TextInput label={tr('Email')} optional value={f.values.email} onChange={(v) => f.set('email', v)} error={f.errors.email} />
        <Check
          label={tr('Sign every published version with it now')}
          checked={f.values.sign_published}
          onChange={(v) => f.set('sign_published', v)}
          help={tr('Beside the older keys’ signatures, so clients holding only the new key verify them all.')}
        />
        <FormError error={error} />
      </div>
    </Dialog>
  )
}

function Keyring() {
  const asc = `${window.location.origin}/api/galaxy/keyring.asc`
  const gpg = `${window.location.origin}/api/galaxy/keyring.gpg`
  const verify = `curl -so hub.gpg ${gpg}\nansible-galaxy collection verify <namespace>.<name> \\\n  --keyring hub.gpg \\\n  --server ${window.location.origin}/api/galaxy/content/published/`
  return (
    <Panel title={tr('Keyring')}>
      <div className="stack" style={{ gap: 10 }}>
        <p className="small muted" style={{ margin: 0 }}>
          {tr('The public keys of every key, the active one first: what ansible-galaxy’s --keyring needs to verify the hub’s signatures. No sign-in needed.')}
        </p>
        {[
          [tr('Armored'), asc],
          [tr('Binary, for ansible-galaxy'), gpg],
        ].map(([label, url]) => (
          <div key={url} className="stack" style={{ gap: 4 }}>
            <div className="row">
              <span className="small faint grow">{label}</span>
              <CopyButton text={url} label={tr('Copy')} />
            </div>
            <pre className="code hub-url">{url}</pre>
          </div>
        ))}
        <div className="stack" style={{ gap: 4 }}>
          <div className="row">
            <span className="small faint grow">{tr('Verify an installed collection')}</span>
            <CopyButton text={verify} label={tr('Copy')} />
          </div>
          <pre className="code hub-url">{verify}</pre>
        </div>
      </div>
    </Panel>
  )
}

export function SigningKeys() {
  const me = useMe()
  const keys = useList<Key>('collection_signing_keys/', { order_by: '-id', page_size: 50 })
  const del = useDelete()
  const [creating, setCreating] = useState(false)
  const active = keys.data?.results.find((k) => k.active)
  return (
    <div className="page">
      <PageHead
        title={tr('Signing keys')}
        subtitle={tr('The hub signs each version it publishes with its active key; ansible-galaxy verifies the signatures with the keyring.')}
        actions={
          me.is_superuser && (
            <button className="btn primary" onClick={() => setCreating(true)}>
              <Icon name={active ? 'refresh' : 'plus'} size={14} />
              {active ? tr('Rotate the key') : tr('New key')}
            </button>
          )
        }
        bordered={false}
      />
      <div className="page-body">
        <div className="split">
          <div className="wide">
            {keys.error ? (
              <ErrorNote error={keys.error} />
            ) : !keys.data ? (
              <Loading />
            ) : !keys.data.results.length ? (
              <Empty title={tr('No signing key yet')}>{tr('The first approval makes one; a superuser can also make it now.')}</Empty>
            ) : (
              <div className="stack" style={{ gap: 12 }}>
                {keys.data.results.map((k) => (
                  <Panel
                    key={k.id}
                    className={k.active ? 'hub-key-active' : undefined}
                    title={
                      <span className="status">
                        <Cell state={k.active ? 'ok' : 'empty'} />
                        <h2>{k.name}</h2>
                        {k.email && <span className="faint small">{k.email}</span>}
                      </span>
                    }
                    actions={
                      <>
                        <CopyButton text={k.public_key} label={tr('Copy the public key')} />
                        {k.summary_fields?.user_capabilities?.delete && (
                          <button
                            className="btn sm danger"
                            onClick={() =>
                              del({
                                path: `collection_signing_keys/${k.id}/`,
                                name: k.name,
                                what: tr('signing key'),
                                body: <p style={{ margin: 0 }}>{tr('Its signatures are deleted with it: clients no longer verify the versions only it signed.')}</p>,
                                invalidate: ['collection_signing_keys', 'collection_versions', 'collections'],
                              })
                            }
                          >
                            {tr('Delete')}
                          </button>
                        )}
                      </>
                    }
                  >
                    <dl className="facts">
                      <div>
                        <dt>{tr('State')}</dt>
                        <dd className="sans">{k.active ? tr('Active: signs approvals') : k.retired ? <>{tr('Retired')} <Ago at={k.retired} /></> : tr('Retired')}</dd>
                      </div>
                      <div>
                        <dt>{tr('Fingerprint')}</dt>
                        <dd className="hub-sha">{k.fingerprint}</dd>
                      </div>
                      <div>
                        <dt>{tr('Algorithm')}</dt>
                        <dd>{k.algorithm}</dd>
                      </div>
                      <div>
                        <dt>{tr('Created')}</dt>
                        <dd className="sans">
                          <Ago at={k.created} />
                        </dd>
                      </div>
                    </dl>
                  </Panel>
                ))}
              </div>
            )}
          </div>
          <div className="narrow">
            <Keyring />
          </div>
        </div>
      </div>
      {creating && <NewKey rotating={!!active} onClose={() => setCreating(false)} />}
    </div>
  )
}
