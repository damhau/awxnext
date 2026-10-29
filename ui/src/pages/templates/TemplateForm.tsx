import { useQuery } from '@tanstack/react-query'
import { useId, useState, type ReactNode } from 'react'
import { Link, useNavigate, useSearchParams } from 'react-router'
import { api, ApiError, getAll } from '../../api/client'
import { useInvalidate, useObject } from '../../api/hooks'
import type { Ref, Template } from '../../api/types'
import { useOrg, usePreselectOrg } from '../../app/context'
import { Check, FormError, Lookup, MultiLookup, SelectField, TextArea, TextInput, VarsEditor, varsError, varsText } from '../../components/form'
import { PageHead, Panel } from '../../components/ui'
import { buildTree, flatten, useFolders } from '../../lib/folders'
import { cleanOutputs, type OutputDecl } from '../../lib/outputs'
import { OutputsEditor } from './OutputsEditor'
import { resOf, type Kind } from './TemplateDetail'

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
          Ask at launch
        </label>
      </div>
      {children}
      {error ? <span className="err">{error}</span> : help ? <span className="help">{help}</span> : null}
    </div>
  )
}

type Values = Record<string, any> // eslint-disable-line @typescript-eslint/no-explicit-any

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
      if (kind === 'job' && v.instance_groups) await sync('instance_groups', v.instance_groups, igs.data ?? [])
      invalidate('unified_job_templates', res, 'folders')
      nav(`/templates/${kind}/${saved.id}`)
    } catch (e) {
      if (e instanceof ApiError && Object.keys(e.fields).length) {
        setErrors(e.fields)
        const unknown = Object.keys(e.fields).filter((k) => !(k in v) && !(k in body))
        setError(unknown.length ? e : new Error('Some fields need attention.'))
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
      <Panel title="Basics">
        <div className="form-grid">
          <TextInput label="Name" value={v.name} onChange={(x) => set('name', x)} error={errors.name} autoFocus={!tpl} />
          {kind === 'workflow' && <Lookup label="Organization" resource="organizations/" value={v.organization} onChange={(x) => set('organization', x)} error={errors.organization} />}
          <SelectField
            label="Folder"
            value={v.folder ?? ''}
            onChange={(x) => set('folder', x ? Number(x) : null)}
            options={[['', 'None (organization level)'], ...flatten(tree.roots).map((n): [number, string] => [n.folder.id, `${'   '.repeat(n.depth)}${n.path}`])]}
            error={errors.folder}
            help="Access granted on the folder applies to the template."
          />
          <TextInput label="Description" value={v.description} onChange={(x) => set('description', x)} optional full />
        </div>
      </Panel>

      {kind === 'job' && (
        <Panel title="What runs">
          <div className="form-grid">
            <Lookup label="Project" resource="projects/" value={v.project} onChange={(x) => { set('project', x); set('playbook', '') }} error={errors.project} meta={(p) => p.scm_url ?? ''} />
            <SelectField
              label="Playbook"
              value={v.playbook}
              onChange={(x) => set('playbook', x)}
              options={[['', v.project ? (playbooks.isPending ? 'Loading…' : 'Choose a playbook') : 'Choose a project first'], ...(playbooks.data ?? []).map((p): [string, string] => [p, p])]}
              error={errors.playbook}
              help={v.project && playbooks.data?.length === 0 ? 'The project has no playbooks yet; sync it first.' : undefined}
            />
            {project.data?.allow_override && (
              <Askable label="Branch" ask={ask('scm_branch')} onAsk={(o) => setAsk('scm_branch', o)} help="Overrides the project's branch, tag or commit." error={errors.scm_branch}>
                <input className="input mono" value={v.scm_branch} onChange={(e) => set('scm_branch', e.target.value)} placeholder={project.data?.scm_branch || 'default branch'} />
              </Askable>
            )}
            <Askable label="Execution environment" ask={ask('execution_environment')} onAsk={(o) => setAsk('execution_environment', o)} error={errors.execution_environment}>
              <Lookup label={<span className="sr-only">Execution environment</span>} resource="execution_environments/" value={v.execution_environment} onChange={(x) => set('execution_environment', x)} placeholder="Default" meta={(e) => e.image} />
            </Askable>
          </div>
        </Panel>
      )}

      <Panel title={kind === 'job' ? 'Where and with what access' : 'Defaults for the nodes'}>
        <div className="form-grid">
          <Askable label="Inventory" ask={ask('inventory')} onAsk={(o) => setAsk('inventory', o)} error={errors.inventory} help={kind === 'workflow' ? 'Overrides the inventory of every node that asks for one.' : undefined}>
            <Lookup label={<span className="sr-only">Inventory</span>} resource="inventories/" value={v.inventory} onChange={(x) => set('inventory', x)} placeholder={kind === 'workflow' ? 'Use each node’s own' : 'Search inventories…'} />
          </Askable>
          <Askable label="Limit" ask={ask('limit')} onAsk={(o) => setAsk('limit', o)} help="Host pattern, e.g. web:&eu" error={errors.limit}>
            <input className="input mono" value={v.limit} onChange={(e) => set('limit', e.target.value)} />
          </Askable>
          {kind === 'job' && (
            <Askable label="Credentials" ask={ask('credential')} onAsk={(o) => setAsk('credential', o)} error={errors.credentials} full>
              <MultiLookup label={<span className="sr-only">Credentials</span>} resource="credentials/" values={v.credentials} onChange={(x) => set('credentials', x)} meta={(c) => c.kind ?? c.summary_fields?.credential_type?.name ?? ''} />
            </Askable>
          )}
          {kind === 'job' && (
            <Askable label="Instance groups" ask={ask('instance_groups')} onAsk={(o) => setAsk('instance_groups', o)} help="Preferred groups, in order. Empty uses the inventory's and organization's groups." full>
              <MultiLookup label={<span className="sr-only">Instance groups</span>} resource="instance_groups/" values={instanceGroups} onChange={(x) => set('instance_groups', x)} />
            </Askable>
          )}
          {kind === 'workflow' && (
            <Askable label="Branch" ask={ask('scm_branch')} onAsk={(o) => setAsk('scm_branch', o)} help="Passed to nodes whose project allows branch override.">
              <input className="input mono" value={v.scm_branch} onChange={(e) => set('scm_branch', e.target.value)} />
            </Askable>
          )}
        </div>
      </Panel>

      {kind === 'job' && (
        <Panel title="How it runs">
          <div className="form-grid">
            <Askable label="Job type" ask={ask('job_type')} onAsk={(o) => setAsk('job_type', o)}>
              <select className="select" value={v.job_type} onChange={(e) => set('job_type', e.target.value)}>
                <option value="run">Run — make changes</option>
                <option value="check">Check — dry run, report what would change</option>
              </select>
            </Askable>
            <Askable label="Verbosity" ask={ask('verbosity')} onAsk={(o) => setAsk('verbosity', o)}>
              <select className="select" value={v.verbosity} onChange={(e) => set('verbosity', Number(e.target.value))}>
                {['0 · normal', '1 · verbose', '2 · more verbose', '3 · debug', '4 · connection debug', '5 · WinRM debug'].map((l, i) => (
                  <option key={i} value={i}>
                    {l}
                  </option>
                ))}
              </select>
            </Askable>
            <Askable label="Forks" ask={ask('forks')} onAsk={(o) => setAsk('forks', o)} help="Hosts in parallel; 0 uses the default (5)." error={errors.forks}>
              <input className="input" type="number" min={0} value={v.forks} onChange={(e) => set('forks', e.target.value)} />
            </Askable>
            <Askable label="Timeout (seconds)" ask={ask('timeout')} onAsk={(o) => setAsk('timeout', o)} help="0 means no timeout." error={errors.timeout}>
              <input className="input" type="number" min={0} value={v.timeout} onChange={(e) => set('timeout', e.target.value)} />
            </Askable>
            <Askable label="Job slices" ask={ask('job_slice_count')} onAsk={(o) => setAsk('job_slice_count', o)} help="Split the inventory across this many parallel jobs." error={errors.job_slice_count}>
              <input className="input" type="number" min={1} value={v.job_slice_count} onChange={(e) => set('job_slice_count', e.target.value)} />
            </Askable>
            <Askable label="Tags" ask={ask('tags')} onAsk={(o) => setAsk('tags', o)} help="Comma separated; only tasks with these tags run.">
              <input className="input mono" value={v.job_tags} onChange={(e) => set('job_tags', e.target.value)} />
            </Askable>
            <Askable label="Skip tags" ask={ask('skip_tags')} onAsk={(o) => setAsk('skip_tags', o)}>
              <input className="input mono" value={v.skip_tags} onChange={(e) => set('skip_tags', e.target.value)} />
            </Askable>
            <Askable label="Show changes (--diff)" ask={ask('diff_mode')} onAsk={(o) => setAsk('diff_mode', o)}>
              <Check label="Show what changed in files and templates" checked={v.diff_mode} onChange={(x) => set('diff_mode', x)} />
            </Askable>
            <div className="field full">
              <span className="label" style={{ fontWeight: 500 }}>
                Options
              </span>
              <div className="row wrap" style={{ gap: 18 }}>
                <Check label="Privilege escalation (become)" checked={v.become_enabled} onChange={(x) => set('become_enabled', x)} />
                <Check label="Allow concurrent runs" checked={v.allow_simultaneous} onChange={(x) => set('allow_simultaneous', x)} />
                <Check label="Use fact cache" checked={v.use_fact_cache} onChange={(x) => set('use_fact_cache', x)} />
                <Check label="Force handlers" checked={v.force_handlers} onChange={(x) => set('force_handlers', x)} />
              </div>
            </div>
          </div>
        </Panel>
      )}

      <Panel title="Variables">
        <div className="form-grid">
          <div className="full row" style={{ justifyContent: 'flex-end', marginBottom: -8 }}>
            <label className="check small muted">
              <input type="checkbox" checked={ask('variables')} onChange={(e) => setAsk('variables', e.target.checked)} />
              Ask at launch
            </label>
          </div>
          <VarsEditor label="Extra variables" value={v.extra_vars} onChange={(x) => set('extra_vars', x)} error={errors.extra_vars} rows={8} help="Highest precedence: override inventory and role variables. YAML or JSON." />
          {kind === 'workflow' && kind && (
            <div className="field full">
              <Check label="Allow concurrent runs" checked={v.allow_simultaneous} onChange={(x) => set('allow_simultaneous', x)} />
            </div>
          )}
        </div>
      </Panel>

      {kind === 'job' && (
        <Panel title="Outputs">
          <div className="form-grid">
            <OutputsEditor value={v.outputs} onChange={(x) => set('outputs', x)} error={errors.outputs} />
          </div>
        </Panel>
      )}

      <Panel title="Organize and trigger">
        <div className="form-grid">
          <Askable label="Labels" ask={ask('labels')} onAsk={(o) => setAsk('labels', o)} full>
            <MultiLookup label={<span className="sr-only">Labels</span>} resource="labels/" params={orgId ? { organization: orgId } : undefined} values={v.labels} onChange={(x) => set('labels', x)} />
          </Askable>
          <SelectField
            label="Webhook"
            value={v.webhook_service}
            onChange={(x) => set('webhook_service', x)}
            options={[
              ['', 'Off'],
              ['github', 'GitHub'],
              ['gitlab', 'GitLab'],
              ['bitbucket_dc', 'Bitbucket Data Center'],
              ['generic', 'Generic (any JSON, mapped with JSONPath)'],
            ]}
            help="Launch on push or pull request events."
            error={errors.webhook_service}
          />
          {v.webhook_service && v.webhook_service !== 'generic' && (
            <Lookup label="Webhook credential" resource="credentials/" value={v.webhook_credential} onChange={(x) => set('webhook_credential', x)} optional help="Token used to post commit statuses back." />
          )}
          {kind === 'job' && (
            <TextInput label="Provisioning callback key" value={v.host_config_key} onChange={(x) => set('host_config_key', x)} optional mono help="Lets new hosts request a run of this template for themselves." />
          )}
        </div>
      </Panel>

      {tpl && <TextArea label="Describe this change" value={v.comment} onChange={(x) => set('comment', x)} optional rows={2} help="Shown in History next to the change." />}

      <div className="form-actions">
        <button className="btn primary" disabled={saving || !v.name.trim()}>
          {saving ? 'Saving…' : tpl ? 'Save changes' : `Create ${kind === 'job' ? 'job' : 'workflow'} template`}
        </button>
        <Link className="btn ghost" to={tpl ? `/templates/${kind}/${tpl.id}` : '/templates'}>
          Cancel
        </Link>
        {kind === 'workflow' && !tpl && <span className="small muted">Add nodes in the visualizer after creating it.</span>}
      </div>
    </form>
  )
  if (tpl) return form
  return (
    <div className="page">
      <PageHead crumbs={[{ label: 'Templates', to: '/templates' }]} title={kind === 'job' ? 'New job template' : 'New workflow template'} />
      <div className="page-body">{form}</div>
    </div>
  )
}
