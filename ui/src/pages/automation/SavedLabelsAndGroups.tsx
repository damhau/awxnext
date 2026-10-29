import { useQuery } from '@tanstack/react-query'
import { useState } from 'react'
import { getAll } from '../../api/client'
import type { Any, Ref } from '../../api/types'
import { MultiLookup } from '../../components/form'
import { syncRelation } from '../../lib/relations'

/**
 * Labels and instance groups saved as launch prompts on a schedule or a
 * workflow step (base: "schedules/5/", or null before it exists).
 */
export function useSavedLabelsAndGroups(base: string | null, template: Any | undefined) {
  const labelsQ = useQuery({ queryKey: [`${base}labels/`], enabled: !!base, queryFn: () => getAll<Ref>(`${base}labels/`) })
  const groupsQ = useQuery({ queryKey: [`${base}instance_groups/`], enabled: !!base, queryFn: () => getAll<Ref>(`${base}instance_groups/`) })
  // null until changed: the saved lists stay as they are.
  const [labels, setLabels] = useState<Ref[] | null>(null)
  const [groups, setGroups] = useState<Ref[] | null>(null)
  const askLabels = !!template?.ask_labels_on_launch
  const askGroups = !!template?.ask_instance_groups_on_launch
  return {
    askLabels,
    askGroups,
    labels: labels ?? labelsQ.data ?? [],
    setLabels,
    groups: groups ?? groupsQ.data ?? [],
    setGroups,
    /** Saves the changed lists on the object at savedBase. */
    save: async (savedBase: string) => {
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
      {rel.askLabels && (
        <MultiLookup
          label="Labels"
          resource="labels/"
          params={orgId ? { organization: orgId } : undefined}
          values={rel.labels}
          onChange={rel.setLabels}
          optional
          full
          error={errors.labels}
          help="Added to the template's labels on the jobs launched."
        />
      )}
      {rel.askGroups && (
        <MultiLookup
          label="Instance groups"
          resource="instance_groups/"
          values={rel.groups}
          onChange={rel.setGroups}
          optional
          full
          error={errors.instance_groups}
          help="In order of preference; empty uses the template's."
        />
      )}
    </>
  )
}
