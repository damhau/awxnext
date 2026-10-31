import { useQuery } from '@tanstack/react-query'
import { useId, useState, type ReactNode } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { api, ApiError, getAll } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import type { Ref, Template } from '../../api/types'
import { useOrg, usePreselectOrg } from '../../app/context'
import { Check, FormError, Lookup, MultiLookup, SelectField, TextArea, TextInput, VarsEditor, varsError, varsText } from '../../components/form'
import { useConfirm } from '../../components/overlay'
import { PageHead, Panel } from '../../components/ui'
import { buildTree, flatten, useFolders } from '../../lib/folders'
import { cleanOutputs, type OutputDecl } from '../../lib/outputs'
import { syncRelation } from '../../lib/relations'
import { OutputsEditor } from './OutputsEditor'
import { resOf, type Kind } from './TemplateDetail'
import { asIs, tr } from '../../lib/i18n'

/** A field with an "Ask at launch" toggle beside its label. */
function Askable({ label, ask, onAsk, children, help, error, full }: { label: ReactNode; ask: boolean; onAsk: (v: boolean) => void; children: ReactNode; help?: ReactNode; error?: string; full?: boolean }) {
  const id = useId()
  return (
    <div className={`field ${full ? 'full' : ''}`}>
      <div className="row" style={{ justifyContent: 'space-between' }}>
        <span className="label" style={{ fontWeight: 500 }}>
          {label}
        </span>
        <label className="check small muted" htmlFor={id}>
          <input id={id} type="checkbox" checked={ask} onChange={(e) => onAsk(e.target.checked)} />
          {tr('Ask at launch')}
        </label>
      </div>
      {children}
      {error ? <span className="err">{error}</span> : help ? <span className="help">{help}</span> : null}
    </div>
  )
}

type Values = Record<string, any> // eslint-disable-line @typescript-eslint/no-explicit-any

/** The template's webhook key, shown on demand and rotated after a confirm:
 * the service sending the webhook then needs the new one. */
function WebhookKey({ res, id }: { res: string; id: number }) {
  const confirm = useConfirm()
  const [key, setKey] = useState<string | null>(null)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState<unknown>(null)
  const call = async (rotate: boolean) => {
    setBusy(true)
    setError(null)
    try {
      const path = `${res}/${id}/webhook_key/`
      const r = rotate ? await api.post<{ webhook_key: string }>(path, {}) : await api.get<{ webhook_key: string }>(path)
      setKey(r.webhook_key)
    } catch (e) {
      setError(e)
    } finally {
      setBusy(false)
    }
  }
  const rotate = async () => {
    const ok = await confirm({
      title: tr('Rotate the webhook key?'),
      body: <p style={{ margin: 0 }}>{tr('The current key stops working at once: set the new one where the webhook is configured.')}</p>,
      confirm: tr('Rotate key'),
      danger: true,
    })
    if (ok) await call(true)
  }
  return (
    <div className="field full">
      <span className="label" style={{ fontWeight: 500 }}>
        {tr('Webhook key')}
      </span>
      <div className="row" style={{ gap: 8 }}>
        <input className="input mono" readOnly aria-label={tr('Webhook key')} value={key ?? '••••••••••••••••••••••••'} style={{ flex: 1 }} />
        {key === null && (
          <button type="button" className="btn" disabled={busy} onClick={() => call(false)}>
            {tr('Show')}
          </button>
        )}
        <button type="button" className="btn" disabled={busy} onClick={rotate}>
          {tr('Rotate')}
        </button>
      </div>
      <span className="help">{tr('The secret the sending service signs or authenticates its requests with.')}</span>
      {error ? <FormError error={error} /> : null}
    </div>
  )
}

const relOf = (t?: Template, k?: string): Ref[] => {
  if (!t || !k) return []
  const v = t.summary_fields?.[k]
  return Array.isArray(v) ? v : v?.results ?? []
}

export function TemplateForm({ kind, tpl }: { kind: Kind; tpl?: Template }) {
  const res = resOf(kind)
  const nav = useNavigate()
  const [sp] = useSearchParams()
  const { org: currentOrg } = useOrg()
  const invalidate = useInvalidate()
  const folders = useFolders()
  const igs = useQuery({ queryKey: [`${res}/${tpl?.id}/instance_groups/`], enabled: !!tpl && kind === 'job', queryFn: () => getAll<Ref>(`${res}/${tpl!.id}/instance_groups/`) })
  const sf = tpl?.summary_fields ?? {}
  const [v, setV] = useState<Values>(() => ({
    name: tpl?.name ?? '',
    description: tpl?.description ?? '',
    folder: tpl?.folder ?? (Number(sp.get('folder')) || null),
    organization: sf.organization ?? (kind === 'workflow' ? currentOrg : null),
    project: sf.project ?? null,
    playbook: tpl?.playbook ?? '',
    scm_branch: tpl?.scm_branch ?? '',
    inventory: sf.inventory ?? null,
    credentials: relOf(tpl, 'credentials'),
    labels: relOf(tpl, 'labels'),
    instance_groups: null as Ref[] | null,
    execution_environment: sf.execution_environment ?? null,
    job_type: tpl?.job_type ?? 'run',
    limit: tpl?.limit ?? '',
    forks: tpl?.forks ?? 0,
    verbosity: tpl?.verbosity ?? 0,
    timeout: tpl?.timeout ?? 0,
    job_slice_count: tpl?.job_slice_count ?? 1,
    job_tags: tpl?.job_tags ?? '',
    skip_tags: tpl?.skip_tags ?? '',
    extra_vars: varsText(tpl?.extra_vars ?? ''),
    become_enabled: tpl?.become_enabled ?? false,
    diff_mode: tpl?.diff_mode ?? false,
    allow_simultaneous: tpl?.allow_simultaneous ?? false,
    use_fact_cache: tpl?.use_fact_cache ?? false,
    force_handlers: tpl?.force_handlers ?? false,
    host_config_key: tpl?.host_config_key ?? '',
    start_at_task: tpl?.start_at_task ?? '',
    prevent_instance_group_fallback: tpl?.prevent_instance_group_fallback ?? false,
    webhook_service: tpl?.webhook_service ?? '',
    webhook_credential: sf.webhook_credential ?? null,
    outputs: ((tpl?.outputs as OutputDecl[] | undefined) ?? []).map((o) => ({ ...o })),
    ask: Object.fromEntries(Object.keys(tpl ?? {}).filter((k) => k.startsWith('ask_') && k.endsWith('_on_launch')).map((k) => [k, !!tpl![k]])) as Record<string, boolean>,
    comment: '',
  }))
  const [errors, setErrors] = useState<Record<string, string>>({})
  const [error, setError] = useState<unknown>(null)
  const [saving, setSaving] = useState(false)
  const set = (k: string, val: unknown) => {
    setV((p) => ({ ...p, [k]: val }))
    if (errors[k]) setErrors((e) => ({ ...e, [k]: '' }))
  }
  // Job templates take their organization from the project; workflows pick one.
  usePreselectOrg(v.organization, (o) => set('organization', o), kind === 'workflow' && !tpl)
  const ask = (k: string) => !!v.ask[`ask_${k}_on_launch`]
  const setAsk = (k: string, on: boolean) => setV((p) => ({ ...p, ask: { ...p.ask, [`ask_${k}_on_launch`]: on } }))

  const project = useObject<Template>(v.project ? `projects/${v.project.id}/` : null)
  const playbooks = useQuery({ queryKey: ['projects/', v.project?.id, 'playbooks'], enabled: !!v.project, queryFn: () => api.get<string[]>(`projects/${v.project.id}/playbooks/`) })
  const orgId = kind === 'job' ? project.data?.organization : v.organization?.id
  const tree = buildTree((folders.data ?? []).filter((f) => !orgId || f.organization === orgId))
  const instanceGroups = v.instance_groups ?? igs.data ?? []

  const submit = async () => {
    const ve = varsError(v.extra_vars)
    if (ve) {
      setErrors({ extra_vars: ve })
      return
    }
    setSaving(true)
    setError(null)
    setErrors({})
    const body: Values = {
      name: v.name,
      description: v.description,
      folder: v.folder,
      inventory: v.inventory?.id ?? null,
      limit: v.limit,
      scm_branch: v.scm_branch,
      extra_vars: v.extra_vars,
      allow_simultaneous: v.allow_simultaneous,
      webhook_service: v.webhook_service,
      webhook_credential: v.webhook_credential?.id ?? null,
      ...v.ask,
    }
    if (kind === 'job') {
      Object.assign(body, {
        project: v.project?.id ?? null,
        playbook: v.playbook,
        execution_environment: v.execution_environment?.id ?? null,
        job_type: v.job_type,
        forks: Number(v.forks) || 0,
        verbosity: Number(v.verbosity) || 0,
        timeout: Number(v.timeout) || 0,
        job_slice_count: Number(v.job_slice_count) || 1,
        job_tags: v.job_tags,
        skip_tags: v.skip_tags,
        become_enabled: v.become_enabled,
        diff_mode: v.diff_mode,
        use_fact_cache: v.use_fact_cache,
        force_handlers: v.force_handlers,
        host_config_key: v.host_config_key,
        start_at_task: v.start_at_task,
        prevent_instance_group_fallback: v.prevent_instance_group_fallback,
        outputs: cleanOutputs(v.outputs),
      })
    } else {
      body.organization = v.organization?.id ?? null
    }
    const headers = v.comment.trim() ? { 'X-Change-Comment': v.comment.trim() } : undefined
    try {
      const saved = tpl ? await api.patch<Template>(`${res}/${tpl.id}/`, body, headers) : await api.post<Template>(`${res}/`, body)
      const sync = async (sub: string, want: Ref[], had: Ref[]) => {
        const w = new Set(want.map((x) => x.id))
        const h = new Set(had.map((x) => x.id))
        for (const x of had) if (!w.has(x.id)) await api.post(`${res}/${saved.id}/${sub}/`, { id: x.id, disassociate: true })
        for (const x of want) if (!h.has(x.id)) await api.post(`${res}/${saved.id}/${sub}/`, { id: x.id })
      }
      if (kind === 'job') await sync('credentials', v.credentials, relOf(tpl, 'credentials'))
      await sync('labels', v.labels, relOf(tpl, 'labels'))
      // Instance groups are tried in order: a new order rebuilds the list.
      if (kind === 'job' && v.instance_groups)
        await syncRelation(`${res}/${saved.id}/instance_groups/`, (igs.data ?? []).map((g) => g.id), (v.instance_groups as Ref[]).map((g) => g.id), true)
      invalidate('unified_job_templates', res, 'folders')
      nav(`/templates/${kind}/${saved.id}`)
    } catch (e) {
      if (e instanceof ApiError && Object.keys(e.fields).length) {
        setErrors(e.fields)
        const unknown = Object.keys(e.fields).filter((k) => !(k in v) && !(k in body))
        setError(unknown.length ? e : new Error(tr('Some fields need attention.')))
      } else setError(e)
    } finally {
      setSaving(false)
    }
  }

  const form = (
    <form
      className="form"
      style={{ maxWidth: 980 }}
      onSubmit={(e) => {
        e.preventDefault()
        submit()
      }}
    >
      <FormError error={error} />
      <Panel title={tr('Basics')}>
        <div className="form-grid">
          <TextInput label={tr('Name')} value={v.name} onChange={(x) => set('name', x)} error={errors.name} autoFocus={!tpl} />
          {kind === 'workflow' && <Lookup label={tr('Organization')} resource="organizations/" value={v.organization} onChange={(x) => set('organization', x)} error={errors.organization} />}
          <SelectField
            label={tr('Folder')}
            value={v.folder ?? ''}
            onChange={(x) => set('folder', x ? Number(x) : null)}
            options={[['', tr('None (organization level)')], ...flatten(tree.roots).map((n): [number, string] => [n.folder.id, `${'   '.repeat(n.depth)}${n.path}`])]}
            error={errors.folder}
            help={tr('Access granted on the folder applies to the template.')}
          />
          <TextInput label={tr('Description')} value={v.description} onChange={(x) => set('description', x)} optional full />
        </div>
      </Panel>

      {kind === 'job' && (
        <Panel title={tr('What runs')}>
          <div className="form-grid">
            <Lookup label={tr('Project')} resource="projects/" value={v.project} onChange={(x) => { set('project', x); set('playbook', '') }} error={errors.project} meta={(p) => p.scm_url ?? ''} />
            <SelectField
              label={tr('Playbook')}
              value={v.playbook}
              onChange={(x) => set('playbook', x)}
              options={[['', v.project ? (playbooks.isPending ? 'Loading…' : tr('Choose a playbook')) : tr('Choose a project first')], ...(playbooks.data ?? []).map((p): [string, string] => [p, p])]}
              error={errors.playbook}
              help={v.project && playbooks.data?.length === 0 ? tr('The project has no playbooks yet; sync it first.') : undefined}
            />
            {project.data?.allow_override && (
              <Askable label={tr('Branch')} ask={ask('scm_branch')} onAsk={(o) => setAsk('scm_branch', o)} help={tr('Overrides the project\'s branch, tag or commit.')} error={errors.scm_branch}>
                <input className="input mono" value={v.scm_branch} onChange={(e) => set('scm_branch', e.target.value)} placeholder={project.data?.scm_branch || tr('default branch')} />
              </Askable>
            )}
            <Askable label={tr('Execution environment')} ask={ask('execution_environment')} onAsk={(o) => setAsk('execution_environment', o)} error={errors.execution_environment}>
              <Lookup label={<span className="sr-only">{tr('Execution environment')}</span>} resource="execution_environments/" value={v.execution_environment} onChange={(x) => set('execution_environment', x)} placeholder={tr('Default')} meta={(e) => e.image} />
            </Askable>
          </div>
        </Panel>
      )}

      <Panel title={kind === 'job' ? tr('Where and with what access') : tr('Defaults for the nodes')}>
        <div className="form-grid">
          <Askable label={tr('Inventory')} ask={ask('inventory')} onAsk={(o) => setAsk('inventory', o)} error={errors.inventory} help={kind === 'workflow' ? tr('Overrides the inventory of every node that asks for one.') : undefined}>
            <Lookup label={<span className="sr-only">{tr('Inventory')}</span>} resource="inventories/" value={v.inventory} onChange={(x) => set('inventory', x)} placeholder={kind === 'workflow' ? tr('Use each node’s own') : tr('Search inventories…')} />
          </Askable>
          <Askable label={tr('Limit')} ask={ask('limit')} onAsk={(o) => setAsk('limit', o)} help={tr('Host pattern, e.g. web:&eu')} error={errors.limit}>
            <input className="input mono" value={v.limit} onChange={(e) => set('limit', e.target.value)} />
          </Askable>
          {kind === 'job' && (
            <Askable label={tr('Credentials')} ask={ask('credential')} onAsk={(o) => setAsk('credential', o)} error={errors.credentials} full>
              <MultiLookup label={<span className="sr-only">{tr('Credentials')}</span>} resource="credentials/" values={v.credentials} onChange={(x) => set('credentials', x)} meta={(c) => c.kind ?? c.summary_fields?.credential_type?.name ?? ''} />
            </Askable>
          )}
          {kind === 'job' && (
            <Askable label={tr('Instance groups')} ask={ask('instance_groups')} onAsk={(o) => setAsk('instance_groups', o)} help={tr('Preferred groups, tried in this order. Empty uses the inventory\'s and organization\'s groups.')} full>
              <MultiLookup label={<span className="sr-only">{tr('Instance groups')}</span>} resource="instance_groups/" values={instanceGroups} onChange={(x) => set('instance_groups', x)} ordered />
            </Askable>
          )}
          {kind === 'job' && (
            <div className="field full">
              <Check
                label={tr('Only these instance groups: do not fall back to the inventory\'s and organization\'s')}
                checked={v.prevent_instance_group_fallback}
                onChange={(x) => set('prevent_instance_group_fallback', x)}
              />
            </div>
          )}
          {kind === 'workflow' && (
            <Askable label={tr('Branch')} ask={ask('scm_branch')} onAsk={(o) => setAsk('scm_branch', o)} help={tr('Passed to nodes whose project allows branch override.')}>
              <input className="input mono" value={v.scm_branch} onChange={(e) => set('scm_branch', e.target.value)} />
            </Askable>
          )}
        </div>
      </Panel>

      {kind === 'job' && (
        <Panel title={tr('How it runs')}>
          <div className="form-grid">
            <Askable label={tr('Job type')} ask={ask('job_type')} onAsk={(o) => setAsk('job_type', o)}>
              <select className="select" value={v.job_type} onChange={(e) => set('job_type', e.target.value)}>
                <option value="run">{tr('Run — make changes')}</option>
                <option value="check">{tr('Check — dry run, report what would change')}</option>
              </select>
            </Askable>
            <Askable label={tr('Verbosity')} ask={ask('verbosity')} onAsk={(o) => setAsk('verbosity', o)}>
              <select className="select" value={v.verbosity} onChange={(e) => set('verbosity', Number(e.target.value))}>
                {['0 · normal', '1 · verbose', '2 · more verbose', '3 · debug', '4 · connection debug', '5 · WinRM debug'].map((l, i) => (
                  <option key={i} value={i}>
                    {l}
                  </option>
                ))}
              </select>
            </Askable>
            <Askable label={tr('Forks')} ask={ask('forks')} onAsk={(o) => setAsk('forks', o)} help={tr('Hosts in parallel; 0 uses the default (5).')} error={errors.forks}>
              <input className="input" type="number" min={0} value={v.forks} onChange={(e) => set('forks', e.target.value)} />
            </Askable>
            <Askable label={tr('Timeout (seconds)')} ask={ask('timeout')} onAsk={(o) => setAsk('timeout', o)} help={tr('0 means no timeout.')} error={errors.timeout}>
              <input className="input" type="number" min={0} value={v.timeout} onChange={(e) => set('timeout', e.target.value)} />
            </Askable>
            <Askable label={tr('Job slices')} ask={ask('job_slice_count')} onAsk={(o) => setAsk('job_slice_count', o)} help={tr('Split the inventory across this many parallel jobs.')} error={errors.job_slice_count}>
              <input className="input" type="number" min={1} value={v.job_slice_count} onChange={(e) => set('job_slice_count', e.target.value)} />
            </Askable>
            <Askable label={tr('Tags')} ask={ask('tags')} onAsk={(o) => setAsk('tags', o)} help={tr('Comma separated; only tasks with these tags run.')}>
              <input className="input mono" value={v.job_tags} onChange={(e) => set('job_tags', e.target.value)} />
            </Askable>
            <Askable label={tr('Skip tags')} ask={ask('skip_tags')} onAsk={(o) => setAsk('skip_tags', o)}>
              <input className="input mono" value={v.skip_tags} onChange={(e) => set('skip_tags', e.target.value)} />
            </Askable>
            <Askable label={tr('Start at task')} ask={ask('start_at_task')} onAsk={(o) => setAsk('start_at_task', o)} help={tr('Name of the task to start from (--start-at-task); empty runs the whole playbook.')} error={errors.start_at_task}>
              <input className="input mono" value={v.start_at_task} onChange={(e) => set('start_at_task', e.target.value)} />
            </Askable>
            <Askable label={tr('Show changes (--diff)')} ask={ask('diff_mode')} onAsk={(o) => setAsk('diff_mode', o)}>
              <Check label={tr('Show what changed in files and templates')} checked={v.diff_mode} onChange={(x) => set('diff_mode', x)} />
            </Askable>
            <div className="field full">
              <span className="label" style={{ fontWeight: 500 }}>
                {tr('Options')}
              </span>
              <div className="row wrap" style={{ gap: 18 }}>
                <Check label={tr('Privilege escalation (become)')} checked={v.become_enabled} onChange={(x) => set('become_enabled', x)} />
                <Check label={tr('Allow concurrent runs')} checked={v.allow_simultaneous} onChange={(x) => set('allow_simultaneous', x)} />
                <Check label={tr('Use fact cache')} checked={v.use_fact_cache} onChange={(x) => set('use_fact_cache', x)} />
                <Check label={tr('Force handlers')} checked={v.force_handlers} onChange={(x) => set('force_handlers', x)} />
              </div>
            </div>
          </div>
        </Panel>
      )}

      <Panel title={tr('Variables')}>
        <div className="form-grid">
          <div className="full row" style={{ justifyContent: 'flex-end', marginBottom: -8 }}>
            <label className="check small muted">
              <input type="checkbox" checked={ask('variables')} onChange={(e) => setAsk('variables', e.target.checked)} />
              {tr('Ask at launch')}
            </label>
          </div>
          <VarsEditor label={tr('Extra variables')} value={v.extra_vars} onChange={(x) => set('extra_vars', x)} error={errors.extra_vars} rows={8} help={tr('Highest precedence: override inventory and role variables. YAML or JSON.')} />
          {kind === 'workflow' && kind && (
            <div className="field full">
              <Check label={tr('Allow concurrent runs')} checked={v.allow_simultaneous} onChange={(x) => set('allow_simultaneous', x)} />
            </div>
          )}
        </div>
      </Panel>

      {kind === 'job' && (
        <Panel title={tr('Outputs')}>
          <div className="form-grid">
            <OutputsEditor value={v.outputs} onChange={(x) => set('outputs', x)} error={errors.outputs} />
          </div>
        </Panel>
      )}

      <Panel title={tr('Organize and trigger')}>
        <div className="form-grid">
          <Askable label={tr('Labels')} ask={ask('labels')} onAsk={(o) => setAsk('labels', o)} full>
            <MultiLookup label={<span className="sr-only">{tr('Labels')}</span>} resource="labels/" params={orgId ? { organization: orgId } : undefined} values={v.labels} onChange={(x) => set('labels', x)} />
          </Askable>
          <SelectField
            label={tr('Webhook')}
            value={v.webhook_service}
            onChange={(x) => set('webhook_service', x)}
            options={[
              ['', tr('Off')],
              ['github', asIs('GitHub')],
              ['gitlab', asIs('GitLab')],
              ['bitbucket_dc', tr('Bitbucket Data Center')],
              ['generic', tr('Generic (any JSON, mapped with JSONPath)')],
            ]}
            help={tr('Launch on push or pull request events.')}
            error={errors.webhook_service}
          />
          {v.webhook_service && v.webhook_service !== 'generic' && (
            <Lookup label={tr('Webhook credential')} resource="credentials/" value={v.webhook_credential} onChange={(x) => set('webhook_credential', x)} optional help={tr('Token used to post commit statuses back.')} />
          )}
          {tpl?.webhook_service && v.webhook_service === tpl.webhook_service && <WebhookKey res={res} id={tpl.id} />}
          {kind === 'job' && (
            <TextInput label={tr('Provisioning callback key')} value={v.host_config_key} onChange={(x) => set('host_config_key', x)} optional mono help={tr('Lets new hosts request a run of this template for themselves.')} />
          )}
        </div>
      </Panel>

      {tpl && <TextArea label={tr('Describe this change')} value={v.comment} onChange={(x) => set('comment', x)} optional rows={2} help={tr('Shown in History next to the change.')} />}

      <div className="form-actions">
        <button className="btn primary" disabled={saving || !v.name.trim()}>
          {saving ? tr('Saving…') : tpl ? tr('Save changes') : kind === 'job' ? tr('Create job template') : tr('Create workflow template')}
        </button>
        <Link className="btn ghost" to={tpl ? `/templates/${kind}/${tpl.id}` : '/templates'}>
          {tr('Cancel')}
        </Link>
        {kind === 'workflow' && !tpl && <span className="small muted">{tr('Add nodes in the visualizer after creating it.')}</span>}
      </div>
    </form>
  )
  if (tpl) return form
  return (
    <div className="page">
      <PageHead crumbs={[{ label: tr('Templates'), to: '/templates' }]} title={kind === 'job' ? tr('New job template') : tr('New workflow template')} />
      <div className="page-body">{form}</div>
    </div>
  )
}
