import type { ReactNode } from 'react'
import { Link, Route, Routes, useNavigate, useParams, useSearchParams } from 'react-router'
import { useList, useObject } from '../../api/hooks'
import type { Any, Ref } from '../../api/types'
import { useMe, useOrg } from '../../app/context'
import { AccessTab } from '../../components/access/Access'
import { Icon } from '../../components/icons'
import { NotificationsTab } from '../../components/NotificationsTab'
import { Empty, ErrorNote, KV, Loading, PageHead, Panel, Tab } from '../../components/ui'
import {
  approverText,
  itemForm,
  itemRefs,
  questionEdits,
  questionSummary,
  timeoutText,
  type CatalogItem,
} from '../../lib/catalogItems'
import { tr } from '../../lib/i18n'
import type { KeyTable } from '../../lib/query'
import { ListPage, useDelete } from '../admin/common'
import { ItemForm, useSurvey } from './ItemForm'
import { RequestsTable } from './lists'

type Item = Any & CatalogItem

const keys: KeyTable = {
  kind: { field: 'kind', map: (v) => (v.startsWith('dep') ? 'deployment' : 'run_once'), help: tr('run_once or deployment') },
  category: { field: 'category', lookup: 'iexact' },
  template: { field: 'unified_job_template__name', lookup: 'icontains', help: tr('template name') },
  org: { field: 'organization__name', lookup: 'iexact', help: tr('organization name') },
}

const kindText = (k: string) => (k === 'deployment' ? tr('Creates a deployment') : tr('Runs once'))

const templateLink = (t: { id: number; name: string; type?: string } | undefined) =>
  t ? <Link to={`/templates/${t.type === 'workflow_job_template' ? 'workflow' : 'job'}/${t.id}`}>{t.name}</Link> : <span className="faint">{tr('Deleted')}</span>

/** Publishing takes Admin on a template: offered to superusers and to the admins of a job or workflow template. */
function useCanPublish() {
  const me = useMe()
  const jobs = useList<Ref>(me.is_superuser ? null : 'job_templates/', { role_level: 'admin_role', page_size: 1 })
  const workflows = useList<Ref>(me.is_superuser ? null : 'workflow_job_templates/', { role_level: 'admin_role', page_size: 1 })
  return me.is_superuser || !!jobs.data?.count || !!workflows.data?.count
}

export function CatalogItemList() {
  const nav = useNavigate()
  const { scope } = useOrg()
  const canPublish = useCanPublish()
  return (
    <ListPage<Item>
      title={tr('Catalog items')}
      subtitle={tr('The templates published in the self-service portal, with the questions requesters answer and who approves.')}
      path="catalog_items/"
      params={scope}
      keys={keys}
      placeholder={tr('Filter items…')}
      actions={
        canPublish && (
          <Link to="/catalog-items/new" className="btn primary">
            <Icon name="plus" size={14} />
            {tr('Publish a template')}
          </Link>
        )
      }
      onOpen={(i) => nav(`/catalog-items/${i.id}`)}
      columns={[
        {
          key: 'name',
          header: tr('Item'),
          sort: 'name',
          render: (i) => (
            <div>
              <Link to={`/catalog-items/${i.id}`} className="cell-title">
                {i.name}
              </Link>
              {i.description && <div className="cell-sub">{i.description}</div>}
            </div>
          ),
        },
        { key: 'template', header: tr('Template'), render: (i) => templateLink(i.summary_fields.unified_job_template) },
        { key: 'kind', header: tr('Kind'), sort: 'kind', render: (i) => kindText(i.kind) },
        { key: 'category', header: tr('Category'), sort: 'category', render: (i) => i.category || <span className="faint">—</span> },
        { key: 'org', header: tr('Organization'), render: (i) => i.summary_fields.organization?.name ?? '—' },
      ]}
      empty={
        <Empty title={tr('No catalog items yet')} action={canPublish && <Link to="/catalog-items/new" className="btn primary">{tr('Publish a template')}</Link>}>
          {tr('An item puts a job or workflow template in the portal, for people who request it without seeing awxnext.')}
        </Empty>
      }
    />
  )
}

const useNamed = (path: string, ids: number[]) => useList<Ref>(ids.length ? path : null, { id__in: ids.join(','), page_size: 50 })

/** The names of what an item refers to, for its form; null while they load. */
function useItemNames(item: Item) {
  const refs = itemRefs(item)
  const teams = useNamed('teams/', refs.teams)
  const templates = useNamed('unified_job_templates/', refs.templates)
  const notifications = useNamed('notification_templates/', refs.notifications)
  const all = { teams, templates, notifications }
  if (Object.entries(all).some(([k, q]) => refs[k as keyof typeof refs].length && q.isPending)) return null
  return (resource: keyof typeof all, id: number) => all[resource].data?.results.find((r) => r.id === id) ?? { id, name: `#${id}` }
}

export function CatalogItemNew() {
  const nav = useNavigate()
  const [sp] = useSearchParams()
  const tid = Number(sp.get('template')) || null
  const tpl = useList<Ref>(tid ? 'unified_job_templates/' : null, { id: tid ?? 0 })
  const template = tpl.data?.results[0] ?? null
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Catalog items'), to: '/catalog-items' }, { label: tr('New') }]} title={template ? tr('Publish {name}', { name: template.name }) : tr('Publish a template')} />
      <div className="page-body">
        {tid && tpl.isPending ? (
          <Loading />
        ) : (
          <ItemForm initial={{ ...itemForm(null, () => null), template, name: template?.name ?? '', description: template?.description ?? '' }} onDone={(id) => nav(id ? `/catalog-items/${id}` : '/catalog-items')} />
        )}
      </div>
    </div>
  )
}

export function CatalogItemDetail() {
  const id = Number(useParams().id)
  const path = `catalog_items/${id}/`
  const q = useObject<Item>(path)
  const del = useDelete()
  if (q.error) return <div className="page-body"><ErrorNote error={q.error} /></div>
  if (!q.data) return <Loading />
  const item = q.data
  const caps = item.summary_fields.user_capabilities ?? {}
  const base = `/catalog-items/${id}`
  return (
    <div className="page">
      <PageHead
        crumbs={[{ label: tr('Catalog items'), to: '/catalog-items' }, { label: item.name }]}
        title={item.name}
        badge={<span className="small muted">{[kindText(item.kind), item.category].filter(Boolean).join(' · ')}</span>}
        subtitle={item.description || undefined}
        actions={
          <>
            <Link className="btn" to={`/portal/items/${id}`}>
              <Icon name="external" size={14} />
              {tr('Open in the portal')}
            </Link>
            {caps.edit && (
              <Link className="btn" to={`${base}/edit`}>
                {tr('Edit')}
              </Link>
            )}
            {caps.delete && (
              <button className="btn danger" onClick={() => del({ path, name: item.name, what: tr('catalog item'), then: '/catalog-items', body: <p style={{ margin: 0 }}>{tr('Its requests and deployments stay, and keep its name.')}</p> })}>
                {tr('Delete')}
              </button>
            )}
          </>
        }
        tabs={
          <>
            <Tab to={base} end>
              {tr('Details')}
            </Tab>
            <Tab to={`${base}/requests`}>{tr('Requests')}</Tab>
            <Tab to={`${base}/notifications`}>{tr('Notifications')}</Tab>
            <Tab to={`${base}/access`}>{tr('Access')}</Tab>
          </>
        }
      />
      <div className="page-body">
        <Routes>
          <Route index element={<ItemDetails item={item} />} />
          <Route path="edit" element={<ItemEdit item={item} />} />
          <Route path="requests" element={<RequestsTable path={`${path}requests/`} actions={item.actions} />} />
          <Route
            path="notifications"
            element={
              <NotificationsTab
                resource="catalog_items"
                id={id}
                editable={!!caps.edit}
                events={[
                  { key: 'requests', label: tr('Request changes') },
                  { key: 'reminders', label: tr('Expiry reminders') },
                ]}
                intro={tr('Request changes go to a webhook or email when a request waits, is approved or denied, runs, fails or is ready, and when its deployment is about to expire. Expiry reminders get the reminders a deployment’s owners are mailed before it expires.')}
              />
            }
          />
          <Route path="access" element={<AccessTab resource="catalog_items" id={id} name={item.name} />} />
        </Routes>
      </div>
    </div>
  )
}

function ItemEdit({ item }: { item: Item }) {
  const nav = useNavigate()
  const names = useItemNames(item)
  if (!names) return <Loading />
  return <ItemForm item={item} initial={itemForm(item, names)} onDone={() => nav(`/catalog-items/${item.id}`)} />
}

const none = (text = tr('None')) => <span className="faint">{text}</span>
const days = (n: number | null, empty: string) => (n == null ? none(empty) : n === 1 ? tr('1 day') : tr('{n} days', { n }))

function ItemDetails({ item }: { item: Item }) {
  const sf = item.summary_fields
  const names = useItemNames(item)
  const template = sf.unified_job_template as { id: number; name: string; type?: string } | undefined
  const { spec } = useSurvey(template ?? null)
  const name = (resource: 'teams' | 'templates' | 'notifications', id: number | null) => (id == null || !names ? null : names(resource, id))
  const dep = item.kind === 'deployment'
  const forever = item.default_lifetime_days == null && item.max_lifetime_days == null
  const rows = spec && item.questions ? questionEdits(spec, item.questions) : null
  return (
    <div className="stack" style={{ gap: 16 }}>
      <Panel title={tr('The item')}>
        <KV
          items={[
            [tr('Template'), templateLink(template)],
            [tr('A request'), kindText(item.kind)],
            [tr('Category'), item.category || none()],
            [tr('Icon'), item.icon ? <span className="mono small">{item.icon}</span> : none(tr('Box (the default)'))],
            [tr('Owner team'), item.owner_team ? <Link to={`/teams/${item.owner_team}`}>{name('teams', item.owner_team)?.name}</Link> : none()],
            [tr('Organization'), sf.organization?.name],
          ]}
        />
      </Panel>
      {rows && spec && (
        <Panel title={tr('Questions')} pad={rows.length === 0}>
          {rows.length === 0 ? (
            <p className="faint small" style={{ margin: 0 }}>
              {tr('The template has no survey turned on: requesters answer nothing.')}
            </p>
          ) : (
            <table className="data">
              <thead>
                <tr>
                  <th>{tr('Question')}</th>
                  <th>{tr('Variable')}</th>
                  <th>{tr('Requesters')}</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => {
                  const q = spec.find((x) => x.variable === r.variable)!
                  return (
                    <tr key={r.variable}>
                      <td>{q.question_name}</td>
                      <td className="mono small">{r.variable}</td>
                      <td>{questionSummary(q, r)}</td>
                    </tr>
                  )
                })}
              </tbody>
            </table>
          )}
        </Panel>
      )}
      <Panel title={tr('Approvals')} pad={item.approval_rules.length === 0}>
        {item.approval_rules.length === 0 ? (
          <p className="faint small" style={{ margin: 0 }}>
            {tr('No rules: requests run at once.')}
          </p>
        ) : (
          <table className="data">
            <thead>
              <tr>
                <th>{tr('Rule')}</th>
                <th>{tr('Condition')}</th>
                <th>{tr('Approvers')}</th>
                <th>{tr('Needed')}</th>
                <th>{tr('Timeout')}</th>
              </tr>
            </thead>
            <tbody>
              {item.approval_rules.map((r) => (
                <tr key={r.name}>
                  <td>{r.name}</td>
                  <td className="mono small">{r.condition || <span className="faint">{tr('Every request')}</span>}</td>
                  <td>{approverText(r, r.team ? name('teams', r.team)?.name : undefined)}</td>
                  <td>{r.required ?? 1}</td>
                  <td>{timeoutText(r.timeout)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        )}
      </Panel>
      <Panel title={tr('Freezes and mail')}>
        <KV
          items={[
            [tr('Freeze exception team'), item.freeze_exception_team ? name('teams', item.freeze_exception_team)?.name : none(tr('None: a request during a freeze is refused'))],
            [tr('Email notification'), item.notification_template ? <Link to={`/notifications/${item.notification_template}`}>{name('notifications', item.notification_template)?.name}</Link> : none()],
          ]}
        />
      </Panel>
      {dep && (
        <Panel title={tr('The deployment')}>
          <KV
            items={[
              [tr('Name'), item.deployment_name ? <span className="mono small">{item.deployment_name}</span> : none(tr('The item’s name and the request’s number'))],
              ...(forever
                ? [[tr('Lifetime'), tr('Kept until deleted')] as [string, ReactNode]]
                : ([
                    [tr('Default lifetime'), days(item.default_lifetime_days, tr('None'))],
                    [tr('Longest lifetime'), days(item.max_lifetime_days, tr('No limit'))],
                    [tr('Extensions'), item.extension_days.length ? tr('{list} days', { list: item.extension_days.join(', ') }) : none()],
                    [tr('Most extensions'), item.max_extensions ?? none(tr('No limit'))],
                    [tr('Expiry in a freeze'), item.expiry_waits_for_freeze ? tr('Waits for its end') : tr('Does not wait')],
                  ] as [string, ReactNode][])),
              [tr('Teardown'), item.teardown_template ? templateLink(name('templates', item.teardown_template) ?? undefined) : none(tr('None: deleted at once'))],
              [
                tr('How to connect'),
                item.connection.length ? (
                  <div className="stack" style={{ gap: 4 }}>
                    {item.connection.map((c) => (
                      <div key={c.label}>
                        {c.label}: <span className="mono small">{c.value}</span>
                        {c.copy === false && <span className="faint small"> · {tr('no copy button')}</span>}
                      </div>
                    ))}
                  </div>
                ) : (
                  none(tr('Every output under its name'))
                ),
              ],
              [
                tr('Actions'),
                item.actions.length ? (
                  <div className="stack" style={{ gap: 4 }}>
                    {item.actions.map((a) => (
                      <div key={a.id}>
                        {a.name} <span className="mono small faint">{a.id}</span> · {templateLink(a.template ? (name('templates', a.template) ?? undefined) : undefined)}
                      </div>
                    ))}
                  </div>
                ) : (
                  none()
                ),
              ],
            ]}
          />
        </Panel>
      )}
      <Panel title={tr('Quotas and steps')}>
        <KV
          items={[
            ...(dep ? [[tr('Deployments at once'), item.max_deployments ?? none(tr('No limit'))] as [string, ReactNode]] : []),
            [tr('Requests a day'), item.max_requests_per_day ?? none(tr('No limit'))],
            [tr('Over quota'), item.over_quota === 'approve' ? tr('Waits for the owner team') : tr('Is refused')],
            [
              tr('Step labels'),
              Object.keys(item.step_labels ?? {}).length ? (
                <div className="stack" style={{ gap: 4 }}>
                  {Object.entries(item.step_labels).map(([s, l]) => (
                    <div key={s}>
                      {s} → {l}
                    </div>
                  ))}
                </div>
              ) : (
                none(tr('Steps show under their own names'))
              ),
            ],
          ]}
        />
      </Panel>
    </div>
  )
}
