import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { getAll } from '../../api/client'
import type { Any, Ref } from '../../api/types'
import { MultiLookup } from '../../components/form'
import { syncRelation } from '../../lib/relations'
import { tr } from '../../lib/i18n'

/**
 * Credentials, labels and instance groups saved as launch prompts on a
 * schedule or a workflow step (base: "schedules/5/", or null before it
 * exists).
 */
export function useSavedLabelsAndGroups(base: string | null, template: Any | undefined) {
  const credentialsQ = useQuery({ queryKey: [`${base}credentials/`], enabled: !!base, queryFn: () => getAll<Ref>(`${base}credentials/`) })
  const labelsQ = useQuery({ queryKey: [`${base}labels/`], enabled: !!base, queryFn: () => getAll<Ref>(`${base}labels/`) })
  const groupsQ = useQuery({ queryKey: [`${base}instance_groups/`], enabled: !!base, queryFn: () => getAll<Ref>(`${base}instance_groups/`) })
  // null until changed: the saved lists stay as they are.
  const [credentials, setCredentials] = useState<Ref[] | null>(null)
  const [labels, setLabels] = useState<Ref[] | null>(null)
  const [groups, setGroups] = useState<Ref[] | null>(null)
  // Only job templates prompt for credentials.
  const askCredentials = !!template?.ask_credential_on_launch && template?.type !== 'workflow_job_template'
  const askLabels = !!template?.ask_labels_on_launch
  const askGroups = !!template?.ask_instance_groups_on_launch
  return {
    askCredentials,
    askLabels,
    askGroups,
    /** Any of the lists is prompted. */
    any: askCredentials || askLabels || askGroups,
    credentials: credentials ?? credentialsQ.data ?? [],
    setCredentials,
    labels: labels ?? labelsQ.data ?? [],
    setLabels,
    groups: groups ?? groupsQ.data ?? [],
    setGroups,
    /** Saves the changed lists on the object at savedBase. */
    save: async (savedBase: string) => {
      if (askCredentials && credentials) await syncRelation(`${savedBase}credentials/`, (credentialsQ.data ?? []).map((r) => r.id), credentials.map((r) => r.id))
      if (askLabels && labels) await syncRelation(`${savedBase}labels/`, (labelsQ.data ?? []).map((r) => r.id), labels.map((r) => r.id))
      if (askGroups && groups) await syncRelation(`${savedBase}instance_groups/`, (groupsQ.data ?? []).map((r) => r.id), groups.map((r) => r.id), true)
    },
  }
}

export function SavedLabelsAndGroupsFields({
  rel,
  orgId,
  errors,
}: {
  rel: ReturnType<typeof useSavedLabelsAndGroups>
  orgId?: number | null
  errors: Record<string, string>
}) {
  return (
    <>
      {rel.askCredentials && (
        <MultiLookup
          label={tr('Credentials')}
          resource="credentials/"
          values={rel.credentials}
          onChange={rel.setCredentials}
          optional
          full
          error={errors.credentials}
          meta={(c) => c.kind ?? c.summary_fields?.credential_type?.name ?? ''}
          help={tr('Each replaces the template\'s credential of the same type. Credentials that ask for passwords at launch cannot be saved.')}
        />
      )}
      {rel.askLabels && (
        <MultiLookup
          label={tr('Labels')}
          resource="labels/"
          params={orgId ? { organization: orgId } : undefined}
          values={rel.labels}
          onChange={rel.setLabels}
          optional
          full
          error={errors.labels}
          help={tr('Added to the template\'s labels on the jobs launched.')}
        />
      )}
      {rel.askGroups && (
        <MultiLookup
          label={tr('Instance groups')}
          resource="instance_groups/"
          values={rel.groups}
          onChange={rel.setGroups}
          optional
          full
          ordered
          error={errors.instance_groups}
          help={tr('Tried in this order; empty uses the template\'s.')}
        />
      )}
    </>
  )
}
