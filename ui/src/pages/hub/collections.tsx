import { useState } from 'react'
import { Link, Navigate, Route, Routes, useNavigate, useParams, useSearchParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Any } from '../../api/types'
import { useOrg } from '../../app/context'
import { useConfirm, useToast } from '../../components/overlay'
import { Icon } from '../../components/icons'
import { useListState } from '../../components/table'
import { UsedByPanel } from '../../components/UsedBy'
import { Ago, Cell, Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab } from '../../components/ui'
import {
  dependencies,
  docsStateLabel,
  galaxyServer,
  groupContents,
  installCommand,
  installRepository,
  repositoryLabel,
  requirementsEntry,
  setTerm,
  sizeText,
  sortVersions,
  termValue,
  type DocEntry,
} from '../../lib/hub'
import { asIs, tr, trn } from '../../lib/i18n'
import type { KeyTable } from '../../lib/query'
import { CopyButton, ListPage, useDelete } from '../admin/common'
import { DocView, Markdown, type DocContext } from './render'

export type Version = Any & { id: number; version: string }
type Coll = Any & { id: number }

export interface Docs {
  state: string
  message: string
  read: string | null
  readme: { name: string; text: string }
  contents: DocEntry[]
}

export const fqcnOf = (v: Any) => `${v.summary_fields?.namespace?.name ?? '?'}.${v.summary_fields?.collection?.name ?? '?'}`

/** Signed, with the signers. */
export function Signed({ v }: { v: Version }) {
  const sigs: Any[] = v.summary_fields?.signatures ?? []
  if (!sigs.length) return <span className="faint">{tr('Not signed')}</span>
  return (
    <span className="status" title={sigs.map((s) => `${s.signer || s.pubkey_fingerprint}`).join('\n')}>
      <Cell state="ok" />
      {trn(sigs.length, 'Signed', 'Signed {n} times')}
    </span>
  )
}

export function Repositories({ repos }: { repos: string[] | undefined }) {
  if (!repos?.length) return <span className="faint">{tr('In no repository')}</span>
  return (
    <span className="chips">
      {repos.map((r) => (
        <span key={r} className={`tag hub-repo hub-repo-${r === 'published' || r === 'staging' || r === 'rejected' ? r : 'remote'}`}>
          {repositoryLabel(r)}
        </span>
      ))}
    </span>
  )
}

const signedMap = (v: string) => (/^(yes|true|1)$/i.test(v) ? 'true' : /^(no|false|0)$/i.test(v) ? 'false' : v)

const collectionKeys: KeyTable = {
  namespace: { field: 'namespace__name', help: tr('a namespace') },
  tag: { field: 'tag', help: tr('a tag') },
  repository: { field: 'repository', help: tr('published, staging, rejected, or a remote’s repository') },
  signed: { field: 'signed', map: signedMap, help: tr('yes or no') },
  deprecated: { field: 'deprecated', map: signedMap, help: tr('yes or no') },
}

function Facets() {
  const ls = useListState({ order: 'name' })
  const repo = termValue(ls.q, 'repository') ?? ''
  const signed = signedMap(termValue(ls.q, 'signed') ?? '')
  const pick = (key: string, value: string, label: string, current: string) => (
    <button key={key + value} type="button" className={`hub-facet ${current === value ? 'on' : ''}`} aria-pressed={current === value} onClick={() => ls.setQ(setTerm(ls.q, key, value || null))}>
      {label}
    </button>
  )
  return (
    <div className="hub-facets" role="group" aria-label={tr('Quick filters')}>
      <span className="faint small">{tr('Where')}</span>
      {pick('repository', '', tr('Anywhere'), repo)}
      {pick('repository', 'published', tr('Published'), repo)}
      {pick('repository', 'staging', tr('Awaiting approval'), repo)}
      <span className="faint small hub-facets-gap">{tr('Signature')}</span>
      {pick('signed', '', tr('Any'), signed)}
      {pick('signed', 'true', tr('Signed'), signed)}
      {pick('signed', 'false', tr('Not signed'), signed)}
    </div>
  )
}

export function CollectionList() {
  const nav = useNavigate()
  const { org } = useOrg()
  const ls = useListState({ order: 'name' })
  const filterBy = (key: string, value: string) => ls.setQ(setTerm(ls.q, key, value))
  return (
    <ListPage<Coll>
      title={tr('Collections')}
      subtitle={tr('The Ansible collections of the hub: those you may install, and those waiting for your approval.')}
      path="collections/"
      params={org ? { namespace__organization: org.id } : undefined}
      keys={collectionKeys}
      placeholder={tr('Search collections…  try  tag:networking  namespace:acme  signed:yes')}
      onOpen={(c) => nav(`/collections/${c.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Collection'),
          sort: 'name',
          render: (c) => {
            const latest = c.summary_fields?.latest_version
            return (
              <div style={{ minWidth: 0 }}>
                <Link to={`/collections/${c.id}`} className="cell-title">
                  {c.summary_fields?.namespace?.name}.{c.name}
                </Link>
                {c.deprecated && <span className="tag hub-deprecated">{tr('Deprecated')}</span>}
                {latest?.description && <div className="cell-sub hub-desc">{latest.description}</div>}
              </div>
            )
          },
        },
        {
          key: 'latest',
          header: tr('Latest version'),
          render: (c) => {
            const latest = c.summary_fields?.latest_version
            if (!latest) return <span className="faint">—</span>
            return (
              <div>
                <span className="mono">{latest.version}</span>
                {latest.signed && (
                  <span className="hub-signed" title={tr('Signed')}>
                    <Icon name="shield" size={12} />
                  </span>
                )}
                <div className="cell-sub">
                  <Ago at={latest.created} />
                </div>
              </div>
            )
          },
        },
        {
          key: 'where',
          header: tr('Where'),
          render: (c) => <Repositories repos={c.summary_fields?.latest_version?.repositories} />,
        },
        {
          key: 'tags',
          header: tr('Tags'),
          render: (c) => (
            <span className="chips">
              {((c.summary_fields?.latest_version?.tags ?? []) as string[]).slice(0, 5).map((t) => (
                <button
                  key={t}
                  type="button"
                  className="chip hub-tag"
                  onClick={(e) => {
                    e.stopPropagation()
                    filterBy('tag', t)
                  }}
                >
                  {t}
                </button>
              ))}
            </span>
          ),
        },
        {
          key: 'versions',
          header: tr('Versions'),
          render: (c) => <span className="tnum">{c.summary_fields?.version_count ?? 0}</span>,
        },
      ]}
      empty={
        <Empty title={tr('No collections yet')}>
          {tr('Upload one with ansible-galaxy collection publish to a namespace you may upload to, or sync a remote.')}{' '}
          <Link to="/collection-namespaces">{tr('Namespaces')}</Link>
        </Empty>
      }
    >
      <Facets />
    </ListPage>
  )
}

/** A version's page, by its id: its collection's page with it selected. */
export function VersionRedirect() {
  const id = Number(useParams().id)
  const v = useObject<Version>(`collection_versions/${id}/`)
  if (v.error)
    return (
      <div className="page-body">
        <ErrorNote error={v.error} />
      </div>
    )
  if (!v.data) return <Loading />
  return <Navigate replace to={`/collections/${v.data.collection}?version=${id}`} />
}

/** Docs are read in the background after the upload: until then, ask
 * again. */
export function useDocs(version: number | undefined) {
  return useObject<Docs>(version ? `collection_versions/${version}/docs/` : null, {
    refetchInterval: (q) => (q.state.data && (q.state.data.state === 'pending' || q.state.data.state === 'reading') ? 3000 : false),
  })
}

export function CollectionDetail() {
  const id = Number(useParams().id)
  const [sp, setSp] = useSearchParams()
  const nav = useNavigate()
  const coll = useObject<Coll>(`collections/${id}/`)
  const versions = useList<Version>('collection_versions/', { collection: id, page_size: 200 })
  const invalidate = useInvalidate()
  const toast = useToast()
  const del = useDelete()
  if (coll.error)
    return (
      <div className="page-body">
        <ErrorNote error={coll.error} />
      </div>
    )
  if (!coll.data || !versions.data) return <Loading />
  const c = coll.data
  const sorted = sortVersions(versions.data.results)
  const chosen = Number(sp.get('version'))
  const v = sorted.find((x) => x.id === chosen) ?? sorted.find((x) => installRepository(x.summary_fields?.repositories ?? [])) ?? sorted[0]
  const ns = c.summary_fields?.namespace
  const fqcn = `${ns?.name}.${c.name}`
  const base = `/collections/${id}`
  const search = sp.get('version') ? `?version=${sp.get('version')}` : ''
  const caps = c.summary_fields?.user_capabilities ?? {}
  const deprecate = async () => {
    try {
      await api.patch(`collections/${id}/`, { deprecated: !c.deprecated })
      invalidate('collections')
      toast(c.deprecated ? tr('{name} is no longer deprecated', { name: fqcn }) : tr('Deprecated {name}', { name: fqcn }))
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Collections'), to: '/collections' }, ...(ns ? [{ label: ns.name, to: `/collection-namespaces/${ns.id}` }] : []), { label: c.name }]}
        title={<span className="hub-title">{fqcn}</span>}
        badge={c.deprecated ? <span className="badge fail">{tr('Deprecated')}</span> : undefined}
        subtitle={v?.metadata?.description || undefined}
        actions={
          <>
            {sorted.length > 0 && (
              <label className="hub-version-pick">
                <span className="faint small">{tr('Version')}</span>
                <select
                  className="select"
                  value={v?.id}
                  onChange={(e) => {
                    const n = new URLSearchParams(sp)
                    n.set('version', e.target.value)
                    setSp(n)
                  }}
                >
                  {sorted.map((x) => (
                    <option key={x.id} value={x.id}>
                      {x.version}
                      {(x.summary_fields?.repositories ?? []).includes('published') ? '' : asIs(` (${(x.summary_fields?.repositories ?? []).map(repositoryLabel).join(', ') || tr('in no repository')})`)}
                    </option>
                  ))}
                </select>
              </label>
            )}
            {caps.edit && (
              <button className="btn" onClick={deprecate}>
                {c.deprecated ? tr('Undeprecate') : tr('Deprecate')}
              </button>
            )}
            {caps.delete && (
              <button className="btn danger" onClick={() => del({ path: `collections/${id}/`, name: fqcn, what: tr('collection'), body: <p style={{ margin: 0 }}>{tr('Every version of it is deleted. This cannot be undone.')}</p>, then: '/collections', invalidate: ['collections', 'collection_versions'] })}>
                {tr('Delete')}
              </button>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base + search} end>
              {tr('Overview')}
            </Tab>
            <Tab to={`${base}/docs${search}`}>{tr('Docs')}</Tab>
            <Tab to={`${base}/versions${search}`} n={sorted.length}>
              {tr('Versions')}
            </Tab>
          </>
        }
      />
      <div className="page-body">
        {!v ? (
          <Empty title={tr('No version you can see')} />
        ) : (
          <Routes>
            <Route index element={<Overview c={c} v={v} fqcn={fqcn} />} />
            <Route path="docs/*" element={<DocsTab v={v} fqcn={fqcn} base={`${base}/docs`} search={search} />} />
            <Route path="versions" element={<VersionsTab versions={sorted} current={v} onPick={(x) => nav(`${base}?version=${x.id}`)} />} />
          </Routes>
        )}
      </div>
    </div>
  )
}

function Install({ v, fqcn }: { v: Version; fqcn: string }) {
  const repo = installRepository(v.summary_fields?.repositories ?? [])
  if (!repo)
    return (
      <Panel title={tr('Install')}>
        <p className="small muted" style={{ margin: 0 }}>
          {tr('This version is not published: clients install it once it is approved.')}
        </p>
      </Panel>
    )
  const server = galaxyServer(window.location.origin, repo)
  const cmd = installCommand(fqcn, v.version, server)
  const req = requirementsEntry(fqcn, v.version, server)
  return (
    <Panel title={tr('Install')}>
      <div className="stack" style={{ gap: 12 }}>
        <div className="stack" style={{ gap: 6 }}>
          <div className="row">
            <span className="small faint grow">{tr('With ansible-galaxy')}</span>
            <CopyButton text={cmd} label={tr('Copy')} />
          </div>
          <pre className="code hub-url">{cmd}</pre>
        </div>
        <div className="stack" style={{ gap: 6 }}>
          <div className="row">
            <span className="small faint grow">{tr('In requirements.yml')}</span>
            <CopyButton text={req} label={tr('Copy')} />
          </div>
          <pre className="code hub-url">{req}</pre>
        </div>
        <p className="small faint" style={{ margin: 0 }}>
          {tr('ansible-galaxy signs in with a token of yours: pass it with --token, or set it for this server in ansible.cfg. Jobs of the namespace’s organization install from the hub without one.')}
        </p>
      </div>
    </Panel>
  )
}

const httpLink = (u: unknown) => (typeof u === 'string' && /^https?:\/\//i.test(u) ? u : null)

function Links({ meta }: { meta: Any }) {
  const links = (
    [
      [tr('Repository'), httpLink(meta.repository)],
      [tr('Documentation'), httpLink(meta.documentation)],
      [tr('Homepage'), httpLink(meta.homepage)],
      [tr('Issues'), httpLink(meta.issues)],
    ] as [string, string | null][]
  ).filter(([, u]) => u)
  if (!links.length) return null
  return (
    <span className="stack" style={{ gap: 2 }}>
      {links.map(([l, u]) => (
        <a key={l} href={u!} target="_blank" rel="noopener noreferrer nofollow" className="ellipsis">
          {l} <Icon name="external" size={12} />
        </a>
      ))}
    </span>
  )
}

function Overview({ c, v, fqcn }: { c: Coll; v: Version; fqcn: string }) {
  const docs = useDocs(v.id)
  const meta: Any = v.metadata ?? {}
  const deps = dependencies(meta)
  const sf = v.summary_fields ?? {}
  const license = [...(Array.isArray(meta.license) ? meta.license : []), meta.license_file].filter(Boolean).join(', ')
  return (
    <div className="split">
      <div className="wide stack" style={{ gap: 16 }}>
        <Panel title={docs.data?.readme?.name || tr('README')}>
          {docs.isPending ? <Loading rows={4} /> : docs.data?.readme?.text ? <Markdown text={docs.data.readme.text} /> : <span className="faint">{tr('The collection has no README.')}</span>}
        </Panel>
      </div>
      <div className="narrow stack" style={{ gap: 16 }}>
        <Install v={v} fqcn={fqcn} />
        <Panel title={tr('Version {version}', { version: v.version })}>
          <KV
            items={[
              [tr('Where'), <Repositories repos={sf.repositories} />],
              [tr('Signature'), <Signed v={v} />],
              [tr('Requires Ansible'), v.requires_ansible ? <span className="mono">{v.requires_ansible}</span> : ''],
              [
                tr('Uploaded'),
                <span>
                  <Ago at={v.created} />
                  {sf.created_by ? <> · {sf.created_by.username}</> : null}
                </span>,
              ],
              [
                tr('Reviewed'),
                v.reviewed ? (
                  <span>
                    <Ago at={v.reviewed} />
                    {sf.reviewed_by ? <> · {sf.reviewed_by.username}</> : null}
                  </span>
                ) : (
                  ''
                ),
              ],
              [tr('Authors'), (meta.authors ?? []).join(', ')],
              [tr('License'), license],
              [
                tr('Tags'),
                (meta.tags ?? []).length ? (
                  <span className="chips">
                    {(meta.tags as string[]).map((t) => (
                      <Link key={t} className="chip hub-tag" to={`/collections?q=${encodeURIComponent('tag:' + t)}`}>
                        {t}
                      </Link>
                    ))}
                  </span>
                ) : (
                  ''
                ),
              ],
              [tr('Links'), <Links meta={meta} />],
              [tr('Size'), v.size ? sizeText(v.size) : ''],
              [tr('Docs'), docs.data ? <Link to={`/collections/${c.id}/docs?version=${v.id}`}>{docsStateLabel(docs.data.state)}</Link> : ''],
            ]}
          />
        </Panel>
        <Panel title={tr('Dependencies')}>
          {deps.length ? (
            <ul className="list-plain small" style={{ gap: 6 }}>
              {deps.map((d) => (
                <li key={d.name} className="row" style={{ justifyContent: 'space-between' }}>
                  <Link to={`/collections?q=${encodeURIComponent(d.name)}`} className="mono ellipsis">
                    {d.name}
                  </Link>
                  <span className="mono faint">{d.spec}</span>
                </li>
              ))}
            </ul>
          ) : (
            <span className="faint small">{tr('None')}</span>
          )}
        </Panel>
        <UsedByPanel resource="collections" id={c.id} empty={tr('No project requires it, and no execution environment you can see holds it.')} />
      </div>
    </div>
  )
}

function DocsState({ v, docs }: { v: Version; docs: Docs }) {
  const toast = useToast()
  const invalidate = useInvalidate()
  const [busy, setBusy] = useState(false)
  const canRead = v.summary_fields?.user_capabilities?.approve
  const again = async () => {
    setBusy(true)
    try {
      await api.post(`collection_versions/${v.id}/read_docs/`, {})
      invalidate(`collection_versions/${v.id}/docs/`)
    } catch (e) {
      toast((e as Error).message, 'error')
    } finally {
      setBusy(false)
    }
  }
  const waiting = docs.state === 'pending' || docs.state === 'reading'
  const bad = docs.state === 'none' || docs.state === 'failed'
  return (
    <div className={`notice small ${bad ? 'warn' : ''} hub-docs-state`} role="status">
      <span className="status">
        <Cell state={waiting ? 'running' : docs.state === 'read' ? 'ok' : docs.state === 'failed' ? 'failed' : 'empty'} />
        <b>{docsStateLabel(docs.state)}</b>
      </span>
      <span className="grow">
        {waiting ? tr('ansible-doc reads the docs of its modules and plugins in the background, after the upload.') : docs.message}
        {docs.read && !waiting && (
          <span className="faint">
            {' '}
            <Ago at={docs.read} />
          </span>
        )}
      </span>
      {canRead && !waiting && (
        <button className="btn sm" disabled={busy} onClick={again}>
          <Icon name="refresh" size={12} />
          {tr('Read again')}
        </button>
      )}
    </div>
  )
}

function DocsTab({ v, fqcn, base, search }: { v: Version; fqcn: string; base: string; search: string }) {
  const docs = useDocs(v.id)
  const open = useParams()['*'] ?? ''
  if (docs.error) return <ErrorNote error={docs.error} />
  if (!docs.data) return <Loading />
  const groups = groupContents(docs.data.contents)
  const ctx: DocContext = { collection: fqcn, base, search }
  return (
    <div className="stack" style={{ gap: 12 }}>
      <DocsState v={v} docs={docs.data} />
      {groups.length > 0 && (
        <div className={open ? 'hub-docs' : undefined}>
          {open && (
            <nav className="hub-docs-index" aria-label={tr('Modules and plugins')}>
              {groups.map((g) => (
                <div key={g.type} className="group">
                  <div className="group-head">
                    <span>{g.label}</span>
                  </div>
                  {g.items.map((i) => (
                    <DocNavLink key={i.name} to={`${base}/${encodeURIComponent(g.type)}/${encodeURIComponent(i.name)}${search}`} entry={i} active={open === `${g.type}/${i.name}`} />
                  ))}
                </div>
              ))}
            </nav>
          )}
          <div className="hub-docs-main">
            <Routes>
              <Route index element={<DocsIndex groups={groups} base={base} search={search} />} />
              <Route path=":type/:name" element={<PluginDoc v={v} ctx={ctx} />} />
            </Routes>
          </div>
        </div>
      )}
    </div>
  )
}

function DocNavLink({ to, entry, active }: { to: string; entry: DocEntry; active: boolean }) {
  return (
    <Link to={to} className={active ? 'active' : ''} aria-current={active ? 'page' : undefined}>
      <span className="mono ellipsis">{entry.name}</span>
      {entry.error && <span className="n bad">!</span>}
    </Link>
  )
}

function DocsIndex({ groups, base, search }: { groups: ReturnType<typeof groupContents>; base: string; search: string }) {
  return (
    <div className="stack" style={{ gap: 16 }}>
      {groups.map((g) => (
        <Panel key={g.type} title={asIs(`${g.label} · ${g.items.length}`)} pad={false}>
          <ul className="hub-doc-rows">
            {g.items.map((i) => (
              <li key={i.name}>
                <Link to={`${base}/${encodeURIComponent(g.type)}/${encodeURIComponent(i.name)}${search}`} className="mono">
                  {i.name}
                </Link>
                <span className={i.error ? 'fail-text small' : 'muted small'}>{i.error ? tr('Its docs could not be read.') : i.short_description}</span>
              </li>
            ))}
          </ul>
        </Panel>
      ))}
    </div>
  )
}

function PluginDoc({ v, ctx }: { v: Version; ctx: DocContext }) {
  const { type = '', name = '' } = useParams()
  const doc = useObject<Any>(`collection_versions/${v.id}/docs/?type=${encodeURIComponent(type)}&name=${encodeURIComponent(name)}`)
  if (doc.error) return <ErrorNote error={doc.error} />
  if (!doc.data) return <Loading />
  const d = doc.data
  return (
    <article className="panel hub-doc">
      <div className="panel-head">
        <Link to={ctx.base + ctx.search} className="small hub-doc-back">
          ← {tr('All modules and plugins')}
        </Link>
        <h2 className="mono">
          {ctx.collection}.{d.name}
        </h2>
        <span className="tag">{type}</span>
        {d.short_description && <span className="muted grow">{d.short_description}</span>}
      </div>
      <div className="panel-body">
        {d.error ? (
          <div className="notice warn small">
            <b>{tr('ansible-doc could not read its docs:')}</b>
            <pre className="code" style={{ marginTop: 8, whiteSpace: 'pre-wrap' }}>
              {d.error}
            </pre>
          </div>
        ) : d.doc ? (
          <DocView type={type} doc={d.doc} ctx={ctx} />
        ) : (
          <span className="faint">{tr('No docs.')}</span>
        )}
      </div>
    </article>
  )
}

function VersionActions({ v }: { v: Version }) {
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  const del = useDelete()
  const caps = v.summary_fields?.user_capabilities ?? {}
  const repos: string[] = v.summary_fields?.repositories ?? []
  const fqcn = fqcnOf(v)
  const run = async (action: string, done: string) => {
    try {
      await api.post(`collection_versions/${v.id}/${action}/`, {})
      invalidate('collection_versions', 'collections')
      toast(done)
    } catch (e) {
      toast((e as Error).message, 'error')
    }
  }
  return (
    <span className="row" style={{ gap: 6, justifyContent: 'flex-end' }}>
      {caps.approve && repos.includes('staging') && (
        <Link className="btn sm" to={`/collection-approvals/${v.id}`}>
          {tr('Review')}
        </Link>
      )}
      {caps.approve && repos.includes('published') && !(v.summary_fields?.signatures ?? []).length && (
        <button className="btn sm" onClick={() => run('sign', tr('Signed {name} {version}', { name: fqcn, version: v.version }))}>
          {tr('Sign')}
        </button>
      )}
      {caps.approve && repos.includes('rejected') && (
        <button
          className="btn sm"
          onClick={async () => (await confirm({ title: tr('Publish {name} {version}?', { name: fqcn, version: v.version }), confirm: tr('Approve') })) && run('approve', tr('Approved {name} {version}', { name: fqcn, version: v.version }))}
        >
          {tr('Approve')}
        </button>
      )}
      {caps.delete && (
        <button className="btn sm danger" onClick={() => del({ path: `collection_versions/${v.id}/`, name: `${fqcn} ${v.version}`, what: tr('version'), invalidate: ['collection_versions', 'collections'] })}>
          {tr('Delete')}
        </button>
      )}
    </span>
  )
}

function VersionsTab({ versions, current, onPick }: { versions: Version[]; current: Version; onPick: (v: Version) => void }) {
  return (
    <div className="panel">
      <div className="table-wrap">
        <table className="data stack-sm">
          <thead>
            <tr>
              <th>{tr('Version')}</th>
              <th>{tr('Where')}</th>
              <th>{tr('Signature')}</th>
              <th>{tr('Uploaded')}</th>
              <th className="end" />
            </tr>
          </thead>
          <tbody>
            {versions.map((x) => (
              <tr key={x.id} className={x.id === current.id ? 'hub-current' : undefined}>
                <td data-label="">
                  <button type="button" className="link-btn mono" onClick={() => onPick(x)}>
                    {x.version}
                  </button>
                  {x.requires_ansible && <div className="cell-sub">{tr('Ansible {range}', { range: x.requires_ansible })}</div>}
                </td>
                <td data-label={tr('Where')}>
                  <Repositories repos={x.summary_fields?.repositories} />
                </td>
                <td data-label={tr('Signature')}>
                  <Signed v={x} />
                </td>
                <td data-label={tr('Uploaded')}>
                  <Ago at={x.created} />
                  {x.summary_fields?.created_by && <span className="faint"> · {x.summary_fields.created_by.username}</span>}
                </td>
                <td className="end">
                  <VersionActions v={x} />
                </td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
    </div>
  )
}
