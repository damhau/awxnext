// The column chooser: which of a list's columns are shown. A user's hidden
// columns are kept with their preferences (columns.<list>).

export interface ChoosableColumn {
  key: string
  /** Always shown. The first column (the object's name) always is. */
  fixed?: boolean
  /** The name in the chooser; columns with neither label nor text header
   * (action menus) are always shown. */
  label?: string
  header?: unknown
}

export function columnLabel(c: ChoosableColumn): string | undefined {
  return c.label ?? (typeof c.header === 'string' ? c.header : undefined)
}

/** The columns a user may hide. */
export function choosable<C extends ChoosableColumn>(cols: C[]): C[] {
  return cols.filter((c, i) => i > 0 && !c.fixed && columnLabel(c) !== undefined)
}

/** The columns shown, in order. */
export function shownColumns<C extends ChoosableColumn>(cols: C[], hidden: string[]): C[] {
  if (!hidden.length) return cols
  const can = new Set(choosable(cols).map((c) => c.key))
  return cols.filter((c) => !(can.has(c.key) && hidden.includes(c.key)))
}

export function toggleColumn(hidden: string[], key: string): string[] {
  return hidden.includes(key) ? hidden.filter((k) => k !== key) : [...hidden, key]
}
