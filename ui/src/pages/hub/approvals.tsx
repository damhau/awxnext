import { useState } from 'react'
import { Link, useNavigate, useParams, useSearchParams } from 'react-router'
import { api } from '../../api/client'
import { useInvalidate, useList, useObject } from '../../api/hooks'
import type { Any } from '../../api/types'
import { useConfirm, useToast } from '../../components/overlay'
import { Ago, Empty, ErrorNote, KV, Loading, PageHead, Panel } from '../../components/ui'
import { diffSummary, docsStateLabel, numberedDiff, sortVersions } from '../../lib/hub'
import { tr } from '../../lib/i18n'
import { ListPage } from '../admin/common'
import { fqcnOf, Repositories, Signed, useDocs, type Version } from './collections'
import { ImportLog } from './namespaces'

/** Approves or rejects a version, after asking. */
function useReview() {
  const confirm = useConfirm()
  const toast = useToast()
  const invalidate = useInvalidate()
  return async (v: Version, action: 'approve' | 'reject') => {
    const name = `${fqcnOf(v)} ${v.version}`
    const ok = await confirm(
      action === 'approve'
        ? { title: tr('Publish {name}?', { name }), body: <p style={{ margin: 0 }}>{tr('Clients and jobs can install it at once; the hub’s signing key signs it.')}</p>, confirm: tr('Approve') }
        : { title: tr('Reject {name}?', { name }), body: <p style={{ margin: 0 }}>{tr('It moves to the rejected repository: no one installs it, and an approver can still publish it later.')}</p>, confirm: tr('Reject'), danger: true },
    )
    if (!ok) return false
    try {
      await api.post(`collection_versions/${v.id}/${action}/`, {})
      invalidate('collection_versions', 'collections')
      toast(action === 'approve' ? tr('Published {name}', { name }) : tr('Rejected {name}', { name }))
      return true
    } catch (e) {
      toast((e as Error).message, 'error')
      return false
    }
  }
}

export function ApprovalList() {
  const nav = useNavigate()
  const [sp, setSp] = useSearchParams()
  const repo = sp.get('repository') === 'rejected' ? 'rejected' : 'staging'
  const review = useReview()
  const pick = (r: string) => {
    const n = new URLSearchParams(sp)
    if (r === 'staging') n.delete('repository')
    else n.set('repository', r)
    n.delete('page')
    setSp(n, { replace: true })
  }
  return (
    <ListPage<Version>
      title={tr('Collection approvals')}
      subtitle={tr('Uploads to namespaces that wait for an approver: read the import’s log and what changed since the previous version, then publish or reject.')}
      path="collection_versions/"
      params={{ repository: repo }}
      order="-id"
      placeholder={tr('Filter by version or file name…')}
      onOpen={(v) => nav(`/collection-approvals/${v.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Collection'),
          render: (v) => (
            <div>
              <Link to={`/collection-approvals/${v.id}`} className="cell-title mono">
                {fqcnOf(v)} {v.version}
              </Link>
              {v.metadata?.description && <div className="cell-sub hub-desc">{v.metadata.description}</div>}
            </div>
          ),
        },
        {
          key: 'uploaded',
          header: tr('Uploaded'),
          sort: 'id',
          render: (v) => (
            <span>
              <Ago at={v.created} />
              {v.summary_fields?.created_by && <span className="faint"> · {v.summary_fields.created_by.username}</span>}
            </span>
          ),
        },
        ...(repo === 'rejected'
          ? [
              {
                key: 'reviewed',
                header: tr('Rejected'),
                render: (v: Version) => (
                  <span>
                    <Ago at={v.reviewed} />
                    {v.summary_fields?.reviewed_by && <span className="faint"> · {v.summary_fields.reviewed_by.username}</span>}
                  </span>
                ),
              },
            ]
          : []),
        {
          key: 'actions',
          header: '',
          label: tr('Actions'),
          className: 'end',
          render: (v) =>
            v.summary_fields?.user_capabilities?.approve ? (
              <span className="row" style={{ gap: 6, justifyContent: 'flex-end' }} onClick={(e) => e.stopPropagation()}>
                <button className="btn sm primary" onClick={() => review(v, 'approve')}>
                  {tr('Approve')}
                </button>
                {repo === 'staging' && (
                  <button className="btn sm" onClick={() => review(v, 'reject')}>
                    {tr('Reject')}
                  </button>
                )}
              </span>
            ) : null,
        },
      ]}
      empty={
        repo === 'staging' ? (
          <Empty title={tr('Nothing waits for approval')}>{tr('Uploads to a namespace that doesn’t publish them at once wait here for its approvers.')}</Empty>
        ) : (
          <Empty title={tr('No rejected version')} />
        )
      }
    >
      <div className="hub-facets" role="group" aria-label={tr('Repository')}>
        <button type="button" className={`hub-facet ${repo === 'staging' ? 'on' : ''}`} aria-pressed={repo === 'staging'} onClick={() => pick('staging')}>
          {tr('Awaiting approval')}
        </button>
        <button type="button" className={`hub-facet ${repo === 'rejected' ? 'on' : ''}`} aria-pressed={repo === 'rejected'} onClick={() => pick('rejected')}>
          {tr('Rejected')}
        </button>
      </div>
    </ListPage>
  )
}

interface FileDiff {
  name: string
  status: 'added' | 'removed' | 'changed'
  diff?: string
  omitted?: 'binary' | 'too_large' | 'total'
}

interface DiffResult {
  version: { id: number; version: string }
  against: { id: number; version: string } | null
  files: FileDiff[]
  counts: { added: number; removed: number; changed: number }
}

const statusLabel: Record<string, string> = { added: tr('added'), removed: tr('removed'), changed: tr('changed') }
const omittedLabel: Record<string, string> = {
  binary: tr('A binary file: not compared.'),
  too_large: tr('Too large to compare here.'),
  total: tr('Not shown: the diff is long enough already.'),
}

function FileChange({ f, open }: { f: FileDiff; open: boolean }) {
  return (
    <details className="hub-file" open={open}>
      <summary>
        <span className={`tag hub-file-${f.status}`}>{statusLabel[f.status]}</span>
        <span className="mono ellipsis">{f.name}</span>
      </summary>
      {f.diff ? (
        <div className="diff hub-diff" role="table" aria-label={tr('Changes to {path}', { path: f.name })}>
          {numberedDiff(f.diff).map((r, i) => (
            <div key={i} role="row" className={r.kind === 'ctx' ? '' : r.kind}>
              <span className="n">{r.a ?? ''}</span>
              <span className="n">{r.b ?? ''}</span>
              <span className="m">{r.kind === 'add' ? '+' : r.kind === 'del' ? '-' : ''}</span>
              <span>{r.text}</span>
            </div>
          ))}
        </div>
      ) : (
        <p className="small muted" style={{ margin: '6px 0 0' }}>
          {f.omitted ? omittedLabel[f.omitted] : tr('Empty.')}
        </p>
      )}
    </details>
  )
}

/** A version's files against another version's of the same collection. */
export function VersionDiff({ v, others }: { v: Version; others: Version[] }) {
  const [against, setAgainst] = useState<number | null>(null)
  const d = useObject<DiffResult>(`collection_versions/${v.id}/diff/${against ? `?against=${against}` : ''}`)
  const choices = sortVersions(others.filter((o) => o.id !== v.id))
  return (
    <Panel
      title={tr('Files')}
      actions={
        choices.length > 0 && (
          <label className="row small" style={{ gap: 6 }}>
            <span className="faint">{tr('Against')}</span>
            <select className="select" value={against ?? ''} onChange={(e) => setAgainst(Number(e.target.value) || null)}>
              <option value="">{tr('The previous version')}</option>
              {choices.map((o) => (
                <option key={o.id} value={o.id}>
                  {o.version}
                </option>
              ))}
            </select>
          </label>
        )
      }
    >
      {d.error ? (
        <ErrorNote error={d.error} />
      ) : !d.data ? (
        <Loading rows={4} />
      ) : (
        <div className="stack" style={{ gap: 10 }}>
          <p className="small muted" style={{ margin: 0 }}>
            {d.data.against ? tr('Against {version}:', { version: d.data.against.version }) : tr('The first version you can see: every file is new.')} {diffSummary(d.data.counts)}
          </p>
          {d.data.files.map((f) => (
            <FileChange key={f.name} f={f} open={d.data.files.length <= 12 && f.status !== 'removed'} />
          ))}
        </div>
      )}
    </Panel>
  )
}

export function ReviewPage() {
  const id = Number(useParams().id)
  const nav = useNavigate()
  const ver = useObject<Version>(`collection_versions/${id}/`)
  const siblings = useList<Version>(ver.data ? 'collection_versions/' : null, { collection: ver.data?.collection, page_size: 200 })
  const imports = useList<Any>('collection_imports/', { collection_version: id, order_by: '-id', page_size: 1 })
  const docs = useDocs(ver.data?.id)
  const review = useReview()
  if (ver.error)
    return (
      <div className="page-body">
        <ErrorNote error={ver.error} />
      </div>
    )
  if (!ver.data) return <Loading />
  const v = ver.data
  const repos: string[] = v.summary_fields?.repositories ?? []
  const canReview = v.summary_fields?.user_capabilities?.approve
  const name = `${fqcnOf(v)} ${v.version}`
  const imp = imports.data?.results[0]
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Collection approvals'), to: '/collection-approvals' }, { label: name }]}
        title={<span className="hub-title">{name}</span>}
        badge={<Repositories repos={repos} />}
        subtitle={v.metadata?.description || undefined}
        actions={
          <>
            <Link className="btn" to={`/collections/${v.collection}?version=${v.id}`}>
              {tr('Open the collection')}
            </Link>
            {canReview && !repos.includes('published') && (
              <button className="btn primary" onClick={async () => (await review(v, 'approve')) && nav('/collection-approvals')}>
                {tr('Approve')}
              </button>
            )}
            {canReview && repos.includes('staging') && (
              <button className="btn" onClick={async () => (await review(v, 'reject')) && nav('/collection-approvals')}>
                {tr('Reject')}
              </button>
            )}
          </>
        }
      />
      <div className="page-body">
        <div className="split">
          <div className="wide">{siblings.data ? <VersionDiff v={v} others={siblings.data.results} /> : <Loading />}</div>
          <div className="narrow stack" style={{ gap: 16 }}>
            <Panel title={tr('Version')}>
              <KV
                items={[
                  [
                    tr('Uploaded'),
                    <span>
                      <Ago at={v.created} />
                      {v.summary_fields?.created_by && <> · {v.summary_fields.created_by.username}</>}
                    </span>,
                  ],
                  [tr('Signature'), <Signed v={v} />],
                  [tr('Requires Ansible'), v.requires_ansible ? <span className="mono">{v.requires_ansible}</span> : ''],
                  [tr('Docs'), docs.data ? <Link to={`/collections/${v.collection}/docs?version=${v.id}`}>{docsStateLabel(docs.data.state)}</Link> : ''],
                  [tr('File'), <span className="mono small">{v.filename}</span>],
                  [tr('SHA-256'), <span className="mono small hub-sha">{v.sha256}</span>],
                ]}
              />
            </Panel>
            <Panel title={tr('Import log')}>
              {imports.isPending ? <Loading rows={4} /> : imp ? <ImportLog imp={imp} /> : <span className="faint small">{tr('The import’s log is gone: logs are kept for a while only.')}</span>}
            </Panel>
          </div>
        </div>
      </div>
    </div>
  )
}
