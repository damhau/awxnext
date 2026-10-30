import { describe, expect, it } from 'vitest'
import { choosable, shownColumns, toggleColumn } from './columns'

const cols = [
  { key: 'name', header: 'Inventory' },
  { key: 'kind', header: 'Kind' },
  { key: 'hosts', header: 'Hosts' },
  { key: 'status', header: { jsx: true }, label: 'Status' },
  { key: 'org', header: 'Organization', fixed: true },
  { key: 'menu', header: { jsx: 'sr-only' } },
]

describe('column chooser', () => {
  it('offers every column but the first, fixed ones and action menus', () => {
    expect(choosable(cols).map((c) => c.key)).toEqual(['kind', 'hosts', 'status'])
  })
  it('hides the hidden ones, keeping the order', () => {
    expect(shownColumns(cols, ['hosts', 'status']).map((c) => c.key)).toEqual(['name', 'kind', 'org', 'menu'])
  })
  it('never hides the name, fixed columns or menus, nor fails on keys gone', () => {
    expect(shownColumns(cols, ['name', 'org', 'menu', 'gone']).map((c) => c.key)).toEqual(cols.map((c) => c.key))
  })
  it('toggles a column', () => {
    expect(toggleColumn(['kind'], 'hosts')).toEqual(['kind', 'hosts'])
    expect(toggleColumn(['kind', 'hosts'], 'kind')).toEqual(['hosts'])
  })
})
