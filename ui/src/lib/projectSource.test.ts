import { describe, expect, it } from 'vitest'
import { scmTypeLabel, sourceBody, type SourceValues } from './projectSource'

const values: SourceValues = {
  scm_url: ' https://example.com/playbooks.tar.gz ',
  scm_branch: 'main',
  scm_refspec: 'refs/pull/*:refs/remotes/origin/pull/*',
  credential: 3,
  signature_validation_credential: 7,
  scm_clean: true,
  scm_delete_on_update: false,
  scm_track_submodules: true,
  scm_update_on_launch: true,
  scm_update_cache_timeout: 60,
  allow_override: true,
  local_path: 'site',
}

describe('project source body', () => {
  it('sends every git field', () => {
    expect(sourceBody('git', values)).toMatchObject({ scm_branch: 'main', scm_refspec: values.scm_refspec, scm_track_submodules: true, allow_override: true, credential: 3 })
  })
  it('clears what AWX refuses on archives', () => {
    const b = sourceBody('archive', values)
    expect(b).toMatchObject({ scm_url: 'https://example.com/playbooks.tar.gz', scm_branch: '', scm_refspec: '', scm_track_submodules: false, allow_override: false })
    expect(b).toMatchObject({ credential: 3, signature_validation_credential: 7, scm_update_on_launch: true, scm_update_cache_timeout: 60 })
  })
  it('keeps only the directory for manual projects', () => {
    expect(sourceBody('', values)).toEqual({ local_path: 'site', scm_url: '', credential: null, signature_validation_credential: null })
  })
  it('labels each type', () => {
    expect(['git', 'archive', ''].map((t) => scmTypeLabel(t as '' | 'git' | 'archive'))).toEqual(['Git', 'Remote archive', 'Directory on the controller'])
  })
})
