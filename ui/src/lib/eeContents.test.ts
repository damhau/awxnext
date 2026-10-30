import { describe, expect, it } from 'vitest'
import { filterCollections, readOfOtherImage, shortDigest, type Collection } from './eeContents'

const cols: Collection[] = [
  { name: 'ansible.posix', version: '1.5.4', path: '/usr/share/ansible/collections' },
  { name: 'community.general', version: '9.5.0', path: '/usr/share/ansible/collections' },
  { name: 'kubernetes.core', version: '5.0.0', path: '/runner/collections' },
]

describe('execution environment contents', () => {
  it('filters the collections by name or path', () => {
    expect(filterCollections(cols, '')).toBe(cols)
    expect(filterCollections(cols, 'GENERAL').map((c) => c.name)).toEqual(['community.general'])
    expect(filterCollections(cols, 'runner').map((c) => c.name)).toEqual(['kubernetes.core'])
  })
  it('tells a read of another image apart', () => {
    expect(readOfOtherImage({ status: 'successful', image: 'a:1', current_image: 'a:2' })).toBe(true)
    expect(readOfOtherImage({ status: 'successful', image: 'a:1', current_image: 'a:1' })).toBe(false)
    expect(readOfOtherImage({ status: 'none', current_image: 'a:1' })).toBe(false)
  })
  it('shortens digests', () => {
    expect(shortDigest('sha256:0123456789abcdef0123')).toBe('sha256:0123456789ab')
    expect(shortDigest(undefined)).toBe('')
  })
})
