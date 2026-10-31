import { tr } from './i18n'
// Where a project's content comes from, and the fields each kind of source
// takes. AWX refuses a branch on archive projects, and a refspec or submodule
// tracking on anything but git.

export type ScmType = '' | 'git' | 'archive'

export const scmTypeOptions: [ScmType, string][] = [
  ['git', tr('Git repository')],
  ['archive', tr('Remote archive')],
  ['', tr('Directory on the controller')],
]

export function scmTypeLabel(t: ScmType): string {
  return t === 'git' ? 'Git' : t === 'archive' ? tr('Remote archive') : tr('Directory on the controller')
}

export interface SourceValues {
  scm_url: string
  scm_branch: string
  scm_refspec: string
  credential: number | null
  signature_validation_credential: number | null
  scm_clean: boolean
  scm_delete_on_update: boolean
  scm_track_submodules: boolean
  scm_update_on_launch: boolean
  scm_update_cache_timeout: number
  allow_override: boolean
  local_path: string
}

/** The source part of a project's create or update body. */
export function sourceBody(t: ScmType, v: SourceValues): Record<string, unknown> {
  if (t === '') {
    return { local_path: v.local_path, scm_url: '', credential: null, signature_validation_credential: null }
  }
  const git = t === 'git'
  return {
    scm_url: v.scm_url.trim(),
    scm_branch: git ? v.scm_branch : '',
    scm_refspec: git ? v.scm_refspec : '',
    scm_track_submodules: git && v.scm_track_submodules,
    allow_override: git && v.allow_override,
    credential: v.credential,
    signature_validation_credential: v.signature_validation_credential,
    scm_clean: v.scm_clean,
    scm_delete_on_update: v.scm_delete_on_update,
    scm_update_on_launch: v.scm_update_on_launch,
    scm_update_cache_timeout: Number(v.scm_update_cache_timeout) || 0,
  }
}
