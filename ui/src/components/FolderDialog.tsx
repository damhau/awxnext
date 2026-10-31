import { useState } from 'react'
import { api } from '../api/client'
import { useOrg } from '../app/context'
import { flatten, subtreeIds, type FolderNode } from '../lib/folders'
import { SelectField } from './form'
import { Dialog } from './overlay'
import { tr } from '../lib/i18n'

/** Creates a folder (optionally inside a parent) or renames and moves one. */
export function FolderDialog({
  roots,
  editing,
  parent,
  onClose,
  onSaved,
}: {
  roots: FolderNode[]
  editing?: FolderNode
  parent?: FolderNode
  onClose: () => void
  onSaved: (id: number) => void
}) {
  const { org, orgs } = useOrg()
  const [name, setName] = useState(editing?.folder.name ?? '')
  const [description, setDescription] = useState(editing?.folder.description ?? '')
  const [parentId, setParentId] = useState<string>(String(editing ? editing.folder.parent ?? '' : parent?.folder.id ?? ''))
  const [orgId, setOrgId] = useState<string>(String(editing?.folder.organization ?? parent?.folder.organization ?? org?.id ?? orgs[0]?.id ?? ''))
  const [error, setError] = useState<unknown>(null)
  const own = editing ? new Set(subtreeIds(editing)) : new Set<number>()
  const parents = flatten(roots).filter((n) => !own.has(n.folder.id) && String(n.folder.organization) === orgId)
  const save = async () => {
    setError(null)
    const body = { name: name.trim(), description, parent: parentId ? Number(parentId) : null, organization: Number(orgId) }
    try {
      const r = editing ? await api.patch<{ id: number }>(`folders/${editing.folder.id}/`, body) : await api.post<{ id: number }>('folders/', body)
      onSaved(r.id)
    } catch (e) {
      setError(e)
    }
  }
  return (
    <Dialog
      title={editing ? tr('Edit folder {name}', { name: editing.folder.name }) : tr('New folder')}
      onClose={onClose}
      footer={
        <>
          <button className="btn" onClick={onClose}>
            {tr('Cancel')}
          </button>
          <button className="btn primary" disabled={!name.trim() || !orgId} onClick={save}>
            {editing ? tr('Save') : tr('Create folder')}
          </button>
        </>
      }
    >
      {error ? <div className="form-error">{(error as Error).message}</div> : null}
      <div className="field">
        <label htmlFor="fn">{tr('Name')}</label>
        <input id="fn" className="input" value={name} onChange={(e) => setName(e.target.value)} autoFocus />
      </div>
      <div className="field">
        <label htmlFor="fd">
          {tr('Description')} <span className="opt">{tr('· optional')}</span>
        </label>
        <input id="fd" className="input" value={description} onChange={(e) => setDescription(e.target.value)} />
      </div>
      {!editing && orgs.length > 1 && (
        <SelectField label={tr('Organization')} value={orgId} onChange={(v) => { setOrgId(v); setParentId('') }} options={orgs.map((o): [string, string] => [String(o.id), o.name])} />
      )}
      <SelectField
        label={tr('Inside')}
        value={parentId}
        onChange={setParentId}
        options={[['', tr('Top level')], ...parents.map((n): [string, string] => [String(n.folder.id), `${'   '.repeat(n.depth)}${n.path}`])]}
        help={tr('Roles granted on a folder apply to everything in it and in its subfolders.')}
      />
    </Dialog>
  )
}
