import { useQuery } from '@tanstack/react-query'
import { useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList } from '../../api/hooks'
import type { Base, Ref } from '../../api/types'
import { useOrg, usePreselectOrg } from '../../app/context'
import { useCopy } from '../../components/CopyDialog'
import { FormError, Lookup, SelectField, TextInput, useForm } from '../../components/form'
import { Dialog, Menu, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Empty, KV, PageHead } from '../../components/ui'
import { filterCollections, readOfOtherImage, shortDigest, type ContentsRead } from '../../lib/eeContents'
import { plural } from '../../lib/format'
import { applyErrors, FilterBox, KindTag, refOf, useDelete } from './common'
import { UsedByDialog } from '../../components/UsedBy'
import { tr } from '../../lib/i18n'

interface EE extends Base {
  image: string
  pull: '' | 'always' | 'missing' | 'never'
  credential: number | null
  organization: number | null
  managed: boolean
}

const pullLabel: Record<string, string> = { '': 'default', always: 'always pull', missing: 'pull if missing', never: 'never pull' }

/** Splits "registry/org/name:tag" into the parts shown in the list. */
function imageParts(image: string) {
  const at = image.lastIndexOf('@')
  const colon = image.lastIndexOf(':')
  const slash = image.lastIndexOf('/')
  let repo = image
  let tag = 'latest'
  if (at > 0) {
    repo = image.slice(0, at)
    tag = image.slice(at + 1, at + 20)
  } else if (colon > slash) {
    repo = image.slice(0, colon)
    tag = image.slice(colon + 1)
  }
  return { repo, tag }
}

export function ExecutionEnvironmentList() {
  const ls = useListState({ order: 'name' })
  const { org } = useOrg()
  const params = org ? { or__organization: org.id, or__organization__isnull: 'true' } : {}
  const list = useList<EE>('execution_environments/', { search: ls.q || undefined, ...params, order_by: ls.order, page: ls.page, page_size: ls.pageSize })
  const [edit, setEdit] = useState<null | { ee?: EE }>(null)
  const [contents, setContents] = useState<EE | null>(null)
  const [usedBy, setUsedBy] = useState<EE | null>(null)
  const del = useDelete('execution_environments')
  const copy = useCopy('execution_environments', { onCopied: (ee) => setEdit({ ee: ee as EE }) })
  const cols: Column<EE>[] = [
    {
      key: 'name',
      header: tr('Environment'),
      sort: 'name',
      render: (e) => (
        <div>
          <span className="row" style={{ gap: 6 }}>
            <button className="link-btn cell-title" onClick={() => setEdit({ ee: e })}>
              {e.name}
            </button>
            {e.managed && <KindTag>{tr('built-in')}</KindTag>}
          </span>
          {e.description && <div className="faint small">{e.description}</div>}
        </div>
      ),
    },
    {
      key: 'image',
      header: tr('Image'),
      sort: 'image',
      render: (e) => {
        const { repo, tag } = imageParts(e.image)
        return (
          <span className="mono small" title={e.image}>
            {repo}
            <span style={{ color: 'var(--accent-ink)' }}>:{tag}</span>
          </span>
        )
      },
    },
    { key: 'pull', header: tr('Pull'), render: (e) => <span className="small muted">{pullLabel[e.pull] ?? e.pull}</span> },
    {
      key: 'org',
      header: tr('Available to'),
      render: (e) => (e.summary_fields.organization ? <span className="small">{e.summary_fields.organization.name}</span> : <span className="small muted">{tr('every organization')}</span>),
    },
    {
      key: 'cred',
      header: tr('Registry credential'),
      render: (e) => (e.summary_fields.credential ? <Link to={`/credentials/${e.credential}`} className="small">{e.summary_fields.credential.name}</Link> : <span className="faint small">—</span>),
    },
    { key: 'modified', header: tr('Edited'), render: (e) => <span className="small faint nowrap"><Ago at={e.modified} /></span> },
    {
      key: 'menu',
      header: <span className="sr-only">{tr('Actions')}</span>,
      className: 'end',
      render: (e) => (
        <Menu label={tr('Actions for {name}', { name: e.name })} className="btn sm icon ghost">
          <button onClick={() => setContents(e)}>{tr('Contents…')}</button>
          <button onClick={() => setUsedBy(e)}>{tr('Used by…')}</button>
          {e.summary_fields.user_capabilities?.edit && (
            <>
              <button onClick={() => setEdit({ ee: e })}>{tr('Edit…')}</button>
              {e.summary_fields.user_capabilities?.copy && <button onClick={() => copy.open(e)}>{tr('Copy…')}</button>}
              {!e.managed && (
                <>
                  <hr />
                  <button className="danger" onClick={() => del(e, 'execution environment')}>
                    {tr('Delete…')}
                  </button>
                </>
              )}
            </>
          )}
        </Menu>
      ),
    },
  ]
  return (
    <div className="page">
      <PageHead
        title={tr('Execution environments')}
        subtitle={tr('Container images that jobs run in: ansible-core, collections and Python dependencies, unchanged from AWX.')}
        bordered={false}
        actions={
          <button className="btn primary" onClick={() => setEdit({})}>
            {tr('New environment')}
          </button>
        }
      />
      {copy.dialog}
      <div className="page-body">
        <FilterBox value={ls.q} onApply={ls.setQ} placeholder={tr('name or image…')} />
        <DataTable
          label={tr('Execution environments')}
          rows={list.data?.results}
          columns={cols}
          loading={list.isPending}
          error={list.error}
          order={ls.order}
          onOrder={ls.setOrder}
          onOpen={(e) => setEdit({ ee: e })}
          empty={<Empty title={ls.q ? tr('No environment matches') : tr('No execution environments')} />}
          footer={
            <div className="table-foot">
              <span>{list.data ? tr('{count}', { count: plural(list.data.count, 'environment', 'environments') }) : ''}</span>
              <Pager count={list.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
            </div>
          }
        />
      </div>
      {edit && <EEDialog ee={edit.ee} onClose={() => setEdit(null)} />}
      {contents && <ContentsDialog ee={contents} onClose={() => setContents(null)} />}
      {usedBy && <UsedByDialog resource="execution_environments" id={usedBy.id} name={usedBy.name} empty={tr('Nothing uses this execution environment.')} onClose={() => setUsedBy(null)} />}
    </div>
  )
}

/** What the image holds, read from its registry (awxnext's
 * execution_environments/N/contents/), read again on request. */
function ContentsDialog({ ee, onClose }: { ee: EE; onClose: () => void }) {
  const path = `execution_environments/${ee.id}/contents/`
  const read = useQuery({
    queryKey: [path],
    queryFn: () => api.get<ContentsRead>(path),
    refetchInterval: (q) => (q.state.data?.status === 'running' ? 2000 : false),
  })
  const invalidate = useInvalidate()
  const [error, setError] = useState<unknown>(null)
  const [q, setQ] = useState('')
  const start = async () => {
    setError(null)
    try {
      await api.post(path, {})
      invalidate(path)
    } catch (e) {
      setError(e)
    }
  }
  const r = read.data
  const c = r?.status === 'successful' ? r.contents : null
  const running = r?.status === 'running'
  const cols = c ? filterCollections(c.collections, q) : []
  return (
    <Dialog
      title={tr('Contents of {name}', { name: ee.name })}
      onClose={onClose}
      wide
      footer={
        <>
          <span className="small faint grow">
            {r?.finished && r.status !== 'running' ? (
              <>
                {tr('Read')} <Ago at={r.finished} /> {tr('from the registry')}{r.digest ? tr(' · {shortDigest}', { shortDigest: shortDigest(r.digest) }) : ''}
              </>
            ) : (
              tr('Read from the image’s registry, without running it.')
            )}
          </span>
          <button className="btn" type="button" disabled={running || read.isPending} onClick={start}>
            {running ? tr('Reading…') : r?.status === 'none' ? tr('Read contents') : tr('Read again')}
          </button>
          <button className="btn primary" type="button" onClick={onClose}>
            {tr('Done')}
          </button>
        </>
      }
    >
      <div className="stack" style={{ gap: 14 }}>
        <span className="mono small">{ee.image}</span>
        <FormError error={error ?? read.error} />
        {r?.status === 'none' && <p className="muted" style={{ margin: 0 }}>{tr('Not read yet. Reading downloads the image’s layers once; a large image takes a minute or two.')}</p>}
        {running && <p className="muted" style={{ margin: 0 }}>{tr('Reading the image’s files from its registry…')}</p>}
        {r?.status === 'failed' && <p className="err" style={{ margin: 0 }}>{r.error}</p>}
        {r && readOfOtherImage(r) && (
          <p className="muted" style={{ margin: 0 }}>
            {tr('These are the contents of')} <span className="mono">{r.image}</span>{tr('; the environment now uses another image. Read again.')}
          </p>
        )}
        {c && (
          <>
            <KV
              items={[
                [tr('ansible-core'), c.ansible_core || '—'],
                ...(c.ansible ? ([[tr('Ansible'), c.ansible]] as [string, string][]) : []),
                [tr('Python'), c.python || '—'],
                [tr('Platform'), [c.os, c.architecture].filter(Boolean).join('/') || '—'],
                [tr('Built'), c.created ? <Ago at={c.created} /> : '—'],
              ]}
            />
            <div className="row" style={{ gap: 8 }}>
              <h3 style={{ margin: 0 }} className="grow">
                {plural(c.collections.length, 'collection', 'collections')}
              </h3>
              <input className="input" style={{ maxWidth: 240 }} aria-label={tr('Filter collections')} placeholder={tr('Filter…')} value={q} onChange={(e) => setQ(e.target.value)} />
            </div>
            <DataTable
              label={tr('Collections')}
              rows={cols}
              columns={[
                { key: 'name', header: tr('Collection'), render: (x) => <span className="mono small">{x.name}</span> },
                { key: 'version', header: tr('Version'), render: (x) => <span className="mono small">{x.version || '—'}</span> },
                { key: 'path', header: tr('Installed in'), render: (x) => <span className="mono small faint">{x.path}</span> },
              ]}
              empty={<Empty title={q ? tr('No collection matches') : tr('No collections found')}>{tr('Looked in the image’s ansible_collections directories.')}</Empty>}
            />
            <div>
              <h3 style={{ margin: '4px 0 6px' }}>{tr('ansible.cfg')}</h3>
              {c.ansible_cfg ? (
                <pre className="code small" style={{ maxHeight: 260, overflow: 'auto', margin: 0 }}>
                  {c.ansible_cfg}
                </pre>
              ) : (
                <p className="muted small" style={{ margin: 0 }}>
                  {tr('None at')} <span className="mono">{c.ansible_cfg_path}</span>{tr(': Ansible’s defaults apply.')}
                </p>
              )}
            </div>
          </>
        )}
      </div>
    </Dialog>
  )
}

function EEDialog({ ee, onClose }: { ee?: EE; onClose: () => void }) {
  const { org } = useOrg()
  const f = useForm({
    name: ee?.name ?? '',
    description: ee?.description ?? '',
    image: ee?.image ?? '',
    pull: ee?.pull ?? '',
    credential: refOf(ee?.summary_fields.credential, ee?.credential),
    organization: ee ? refOf(ee.summary_fields.organization, ee.organization) : (org as Ref | null),
  })
  usePreselectOrg(f.values.organization, (o) => f.set('organization', o), !ee)
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const invalidate = useInvalidate()
  const toast = useToast()
  const readOnly = !!ee && !ee.summary_fields.user_capabilities?.edit
  const submit = async (e?: FormEvent) => {
    e?.preventDefault()
    setBusy(true)
    setError(null)
    const body = {
      name: f.values.name,
      description: f.values.description,
      image: f.values.image.trim(),
      pull: f.values.pull,
      credential: f.values.credential?.id ?? null,
      organization: f.values.organization?.id ?? null,
    }
    try {
      if (ee) await api.patch(`execution_environments/${ee.id}/`, body)
      else await api.post('execution_environments/', body)
      invalidate('execution_environments')
      toast(ee ? tr('Saved {name}', { name: body.name }) : tr('Created {name}', { name: body.name }))
      onClose()
    } catch (err) {
      setError(applyErrors(err, f.setErrors))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={ee ? ee.name : tr('New execution environment')}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {readOnly ? tr('Close') : tr('Cancel')}
          </button>
          {!readOnly && (
            <button className="btn primary" disabled={busy || !f.values.name || !f.values.image.trim()} onClick={() => submit()}>
              {busy ? tr('Saving…') : ee ? tr('Save') : tr('Create')}
            </button>
          )}
        </>
      }
    >
      <form className="form" onSubmit={submit}>
        <fieldset disabled={readOnly} className="form">
          {ee?.managed && <div className="notice small">{tr('Built-in environment. Changes may be reverted when the platform is upgraded.')}</div>}
          <div className="form-grid">
            <TextInput label={tr('Name')} value={f.values.name} onChange={(x) => f.set('name', x)} error={f.errors.name} autoFocus />
            <TextInput label={tr('Description')} optional value={f.values.description} onChange={(x) => f.set('description', x)} />
            <TextInput
              label={tr('Image')}
              mono
              value={f.values.image}
              onChange={(x) => f.set('image', x)}
              error={f.errors.image}
              placeholder={tr('quay.io/ansible/awx-ee:24.6.1')}
              help={tr('Full reference including registry; pin a tag or digest for reproducible runs.')}
              full
            />
            <SelectField
              label={tr('Pull')}
              value={f.values.pull}
              onChange={(x) => f.set('pull', x as EE['pull'])}
              options={[
                ['', tr('Default (pull if missing)')],
                ['always', tr('Always pull before running')],
                ['missing', tr('Only pull if not present')],
                ['never', tr('Never pull')],
              ]}
            />
            <Lookup
              label={tr('Registry credential')}
              optional
              resource="credentials/"
              params={{ credential_type__kind: 'registry' }}
              value={f.values.credential}
              onChange={(x) => f.set('credential', x)}
              error={f.errors.credential}
              help={tr('For private registries.')}
            />
            <Lookup
              label={tr('Organization')}
              optional
              resource="organizations/"
              value={f.values.organization}
              onChange={(x) => f.set('organization', x)}
              error={f.errors.organization}
              help={tr('Leave empty to offer it to every organization (admins only).')}
            />
          </div>
        </fieldset>
        <FormError error={error} />
        <button type="submit" hidden />
      </form>
    </Dialog>
  )
}
