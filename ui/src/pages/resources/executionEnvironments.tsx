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
import { Ago, Empty, PageHead } from '../../components/ui'
import { plural } from '../../lib/format'
import { applyErrors, FilterBox, KindTag, refOf, useDelete } from './common'

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
  const del = useDelete('execution_environments')
  const copy = useCopy('execution_environments', { onCopied: (ee) => setEdit({ ee: ee as EE }) })
  const cols: Column<EE>[] = [
    {
      key: 'name',
      header: 'Environment',
      sort: 'name',
      render: (e) => (
        <div>
          <span className="row" style={{ gap: 6 }}>
            <button className="link-btn cell-title" onClick={() => setEdit({ ee: e })}>
              {e.name}
            </button>
            {e.managed && <KindTag>built-in</KindTag>}
          </span>
          {e.description && <div className="faint small">{e.description}</div>}
        </div>
      ),
    },
    {
      key: 'image',
      header: 'Image',
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
    { key: 'pull', header: 'Pull', render: (e) => <span className="small muted">{pullLabel[e.pull] ?? e.pull}</span> },
    {
      key: 'org',
      header: 'Available to',
      render: (e) => (e.summary_fields.organization ? <span className="small">{e.summary_fields.organization.name}</span> : <span className="small muted">every organization</span>),
    },
    {
      key: 'cred',
      header: 'Registry credential',
      render: (e) => (e.summary_fields.credential ? <Link to={`/credentials/${e.credential}`} className="small">{e.summary_fields.credential.name}</Link> : <span className="faint small">—</span>),
    },
    { key: 'modified', header: 'Edited', render: (e) => <span className="small faint nowrap"><Ago at={e.modified} /></span> },
    {
      key: 'menu',
      header: <span className="sr-only">Actions</span>,
      className: 'end',
      render: (e) =>
        e.summary_fields.user_capabilities?.edit ? (
          <Menu label={`Actions for ${e.name}`} className="btn sm icon ghost">
            <button onClick={() => setEdit({ ee: e })}>Edit…</button>
            {e.summary_fields.user_capabilities?.copy && <button onClick={() => copy.open(e)}>Copy…</button>}
            {!e.managed && (
              <>
                <hr />
                <button className="danger" onClick={() => del(e, 'execution environment')}>
                  Delete…
                </button>
              </>
            )}
          </Menu>
        ) : null,
    },
  ]
  return (
    <div className="page">
      <PageHead
        title="Execution environments"
        subtitle="Container images that jobs run in: ansible-core, collections and Python dependencies, unchanged from AWX."
        bordered={false}
        actions={
          <button className="btn primary" onClick={() => setEdit({})}>
            New environment
          </button>
        }
      />
      {copy.dialog}
      <div className="page-body">
        <FilterBox value={ls.q} onApply={ls.setQ} placeholder="name or image…" />
        <DataTable
          label="Execution environments"
          rows={list.data?.results}
          columns={cols}
          loading={list.isPending}
          error={list.error}
          order={ls.order}
          onOrder={ls.setOrder}
          onOpen={(e) => setEdit({ ee: e })}
          empty={<Empty title={ls.q ? 'No environment matches' : 'No execution environments'} />}
          footer={
            <div className="table-foot">
              <span>{list.data ? `${plural(list.data.count, 'environment', 'environments')}` : ''}</span>
              <Pager count={list.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
            </div>
          }
        />
      </div>
      {edit && <EEDialog ee={edit.ee} onClose={() => setEdit(null)} />}
    </div>
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
      toast(ee ? `Saved ${body.name}` : `Created ${body.name}`)
      onClose()
    } catch (err) {
      setError(applyErrors(err, f.setErrors))
    } finally {
      setBusy(false)
    }
  }
  return (
    <Dialog
      title={ee ? ee.name : 'New execution environment'}
      onClose={onClose}
      wide
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {readOnly ? 'Close' : 'Cancel'}
          </button>
          {!readOnly && (
            <button className="btn primary" disabled={busy || !f.values.name || !f.values.image.trim()} onClick={() => submit()}>
              {busy ? 'Saving…' : ee ? 'Save' : 'Create'}
            </button>
          )}
        </>
      }
    >
      <form className="form" onSubmit={submit}>
        <fieldset disabled={readOnly} className="form">
          {ee?.managed && <div className="notice small">Built-in environment. Changes may be reverted when the platform is upgraded.</div>}
          <div className="form-grid">
            <TextInput label="Name" value={f.values.name} onChange={(x) => f.set('name', x)} error={f.errors.name} autoFocus />
            <TextInput label="Description" optional value={f.values.description} onChange={(x) => f.set('description', x)} />
            <TextInput
              label="Image"
              mono
              value={f.values.image}
              onChange={(x) => f.set('image', x)}
              error={f.errors.image}
              placeholder="quay.io/ansible/awx-ee:24.6.1"
              help="Full reference including registry; pin a tag or digest for reproducible runs."
              full
            />
            <SelectField
              label="Pull"
              value={f.values.pull}
              onChange={(x) => f.set('pull', x as EE['pull'])}
              options={[
                ['', 'Default (pull if missing)'],
                ['always', 'Always pull before running'],
                ['missing', 'Only pull if not present'],
                ['never', 'Never pull'],
              ]}
            />
            <Lookup
              label="Registry credential"
              optional
              resource="credentials/"
              params={{ credential_type__kind: 'registry' }}
              value={f.values.credential}
              onChange={(x) => f.set('credential', x)}
              error={f.errors.credential}
              help="For private registries."
            />
            <Lookup
              label="Organization"
              optional
              resource="organizations/"
              value={f.values.organization}
              onChange={(x) => f.set('organization', x)}
              error={f.errors.organization}
              help="Leave empty to offer it to every organization (admins only)."
            />
          </div>
        </fieldset>
        <FormError error={error} />
        <button type="submit" hidden />
      </form>
    </Dialog>
  )
}
