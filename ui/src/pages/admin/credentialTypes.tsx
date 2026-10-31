// Credential types: the built-in ones (read-only), and custom types with
// their inputs and injectors, edited as YAML and validated by the API.
import { useState, type FormEvent } from 'react'
import { Link } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList } from '../../api/hooks'
import type { Base } from '../../api/types'
import { useSession } from '../../app/context'
import { FormError, Segmented, SelectField, TextArea, TextInput, useForm } from '../../components/form'
import { Dialog, Menu, useToast } from '../../components/overlay'
import { DataTable, Pager, useListState, type Column } from '../../components/table'
import { Ago, Empty, PageHead } from '../../components/ui'
import { fieldIds, injectorsExample, inputsExample, parseSpec, specText, undefinedRefs, type Spec } from '../../lib/credentialTypes'
import { plural } from '../../lib/format'
import { applyErrors, FilterBox, KindTag, useDelete } from '../resources/common'
import { asIs, tr } from '../../lib/i18n'

interface CredType extends Base {
  kind: string
  namespace: string | null
  managed: boolean
  inputs: Spec
  injectors: Spec
}

const kindLabel: Record<string, string> = {
  ssh: tr('Machine'),
  scm: tr('Source control'),
  vault: asIs('Vault'),
  net: tr('Network'),
  cloud: tr('Cloud'),
  registry: tr('Container registry'),
  token: tr('Token'),
  insights: asIs('Insights'),
  kubernetes: asIs('Kubernetes'),
  galaxy: asIs('Galaxy'),
  cryptography: tr('Cryptography'),
  external: tr('Secret lookup'),
}

const show = [
  ['all', tr('All')],
  ['custom', tr('Custom')],
  ['managed', tr('Built-in')],
] as const

export function CredentialTypeList() {
  const ls = useListState({ order: 'name' })
  const { me } = useSession()
  const [only, setOnly] = useState<(typeof show)[number][0]>('all')
  const params = only === 'custom' ? { managed: 'false' } : only === 'managed' ? { managed: 'true' } : {}
  const list = useList<CredType>('credential_types/', { search: ls.q || undefined, ...params, order_by: ls.order, page: ls.page, page_size: ls.pageSize })
  const [open, setOpen] = useState<null | { type?: CredType }>(null)
  const del = useDelete('credential_types')
  const cols: Column<CredType>[] = [
    {
      key: 'name',
      header: tr('Type'),
      sort: 'name',
      render: (t) => (
        <div>
          <span className="row" style={{ gap: 6 }}>
            <button className="link-btn cell-title" onClick={() => setOpen({ type: t })}>
              {t.name}
            </button>
            {t.managed && <KindTag>{tr('built-in')}</KindTag>}
          </span>
          {t.description && <div className="faint small">{t.description}</div>}
        </div>
      ),
    },
    { key: 'kind', header: tr('Kind'), sort: 'kind', render: (t) => <span className="small">{kindLabel[t.kind] ?? t.kind}</span> },
    { key: 'fields', header: tr('Inputs'), render: (t) => <span className="small muted">{plural(fieldIds(t.inputs ?? {}).length, 'field', 'fields')}</span> },
    { key: 'ns', header: tr('Namespace'), render: (t) => (t.namespace ? <span className="mono small">{t.namespace}</span> : <span className="faint small">—</span>) },
    { key: 'modified', header: tr('Edited'), render: (t) => <span className="small faint nowrap"><Ago at={t.modified} /></span> },
    {
      key: 'menu',
      header: <span className="sr-only">{tr('Actions')}</span>,
      className: 'end',
      render: (t) =>
        t.summary_fields.user_capabilities?.edit ? (
          <Menu label={tr('Actions for {name}', { name: t.name })} className="btn sm icon ghost">
            <button onClick={() => setOpen({ type: t })}>{tr('Edit…')}</button>
            <Link to={`/credentials?q=type:${encodeURIComponent(`"${t.name}"`)}`}>{tr('Credentials of this type')}</Link>
            {t.summary_fields.user_capabilities?.delete && (
              <>
                <hr />
                <button className="danger" onClick={() => del(t, 'credential type')}>
                  {tr('Delete…')}
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
        title={tr('Credential types')}
        subtitle={tr('What a credential holds and how jobs receive it: the built-in types, and custom ones with your own inputs and injectors.')}
        bordered={false}
        actions={
          me?.is_superuser ? (
            <button className="btn primary" onClick={() => setOpen({})}>
              {tr('New type')}
            </button>
          ) : undefined
        }
      />
      <div className="page-body">
        <div className="row wrap" style={{ gap: 8 }}>
          <FilterBox value={ls.q} onApply={ls.setQ} placeholder={tr('name…')} />
          <Segmented legend={<span className="sr-only">{tr('Show')}</span>} value={only} onChange={setOnly} options={show.map(([k, label]) => [k, label])} />
        </div>
        <DataTable
          label={tr('Credential types')}
          rows={list.data?.results}
          columns={cols}
          loading={list.isPending}
          error={list.error}
          order={ls.order}
          onOrder={ls.setOrder}
          onOpen={(t) => setOpen({ type: t })}
          empty={<Empty title={ls.q || only !== 'all' ? tr('No type matches') : tr('No credential types')} />}
          footer={
            <div className="table-foot">
              <span>{list.data ? plural(list.data.count, 'type', 'types') : ''}</span>
              <Pager count={list.data?.count ?? 0} page={ls.page} pageSize={ls.pageSize} onPage={ls.setPage} />
            </div>
          }
        />
      </div>
      {open && <CredentialTypeDialog type={open.type} onClose={() => setOpen(null)} />}
    </div>
  )
}

function CredentialTypeDialog({ type, onClose }: { type?: CredType; onClose: () => void }) {
  const editable = !type || (!!type.summary_fields.user_capabilities?.edit && !type.managed)
  const used = useList<Base>(type ? `credential_types/${type.id}/credentials/` : '', { page_size: 1 }, { enabled: !!type })
  const inUse = (used.data?.count ?? 0) > 0
  const f = useForm({
    name: type?.name ?? '',
    description: type?.description ?? '',
    kind: type?.kind ?? 'cloud',
    inputs: type ? specText(type.inputs) : inputsExample,
    injectors: type ? specText(type.injectors) : injectorsExample,
  })
  const v = f.values
  const inputs = parseSpec(v.inputs)
  const injectors = parseSpec(v.injectors)
  const missing = inputs.value && injectors.value ? undefinedRefs(inputs.value, injectors.value) : []
  const [error, setError] = useState<unknown>(null)
  const [busy, setBusy] = useState(false)
  const invalidate = useInvalidate()
  const toast = useToast()
  const submit = async (e: FormEvent) => {
    e.preventDefault()
    if (!inputs.value || !injectors.value) return
    setBusy(true)
    setError(null)
    const body = { name: v.name, description: v.description, kind: v.kind, inputs: inputs.value, injectors: injectors.value }
    try {
      const saved = type ? await api.patch<CredType>(`credential_types/${type.id}/`, body) : await api.post<CredType>('credential_types/', body)
      invalidate('credential_types')
      toast(type ? tr('Saved {name}', { name: saved.name }) : tr('Created {name}', { name: saved.name }))
      onClose()
    } catch (err) {
      setError(applyErrors(err, f.setErrors))
    } finally {
      setBusy(false)
    }
  }
  const title = !type ? tr('New credential type') : editable ? `Edit ${type.name}` : type.name
  return (
    <Dialog
      title={title}
      onClose={onClose}
      wide
      footer={
        editable ? (
          <>
            <button className="btn" type="button" onClick={onClose}>
              {tr('Cancel')}
            </button>
            <button className="btn primary" form="credtype-form" disabled={busy || !v.name.trim() || !inputs.value || !injectors.value}>
              {busy ? tr('Saving…') : type ? tr('Save') : tr('Create type')}
            </button>
          </>
        ) : (
          <button className="btn" type="button" onClick={onClose}>
            {tr('Close')}
          </button>
        )
      }
    >
      <form id="credtype-form" className="form" onSubmit={submit}>
        {type?.managed && <p className="muted" style={{ marginTop: 0 }}>{tr('A built-in type: read-only. Make a custom type for other inputs.')}</p>}
        {inUse && editable && (
          <p className="muted" style={{ marginTop: 0 }}>
            {plural(used.data!.count, 'credential uses', 'credentials use')} {tr('this type: its inputs cannot change, its injectors can.')}
          </p>
        )}
        <div className="form-grid">
          <TextInput label={tr('Name')} value={v.name} onChange={(x) => f.set('name', x)} error={f.errors.name} disabled={!editable} />
          <SelectField
            label={tr('Kind')}
            value={v.kind}
            onChange={(x) => f.set('kind', x)}
            options={type?.managed ? [[v.kind, kindLabel[v.kind] ?? v.kind]] : [['cloud', tr('Cloud')], ['net', tr('Network')]]}
            error={f.errors.kind}
            help={editable ? tr('Custom types are cloud or network credentials.') : undefined}
          />
          <TextInput label={tr('Description')} optional value={v.description} onChange={(x) => f.set('description', x)} error={f.errors.description} full disabled={!editable} />
          <TextArea
            label={tr('Input configuration')}
            mono
            rows={12}
            value={v.inputs}
            onChange={(x) => editable && !inUse && f.set('inputs', x)}
            error={inputs.error ?? f.errors.inputs}
            help={
              <>
                {tr('YAML or JSON:')} <code>fields</code> {tr('(each with an')} <code>id</code>, a <code>label</code>, a <code>type</code>, <code>secret: true</code> {tr('to hide it),')}{' '}
                <code>required</code>.
              </>
            }
          />
          <TextArea
            label={tr('Injector configuration')}
            mono
            rows={8}
            value={v.injectors}
            onChange={(x) => editable && f.set('injectors', x)}
            error={injectors.error ?? f.errors.injectors}
            help={
              missing.length ? (
                <span className="err">{tr('No input field named')} {missing.join(', ')}.</span>
              ) : (
                <>
                  {tr('How jobs receive the inputs:')} <code>env</code>, <code>extra_vars</code> {tr('and')} <code>file</code>{tr(', as Jinja2 templates such as')} <code>{'{{ token }}'}</code>.
                </>
              )
            }
          />
        </div>
        <FormError error={error} />
      </form>
    </Dialog>
  )
}
