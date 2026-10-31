import { tr } from './i18n'
// Every keyboard shortcut, for the "?" overlay (roadmap M7-15). The shell's
// "g then a letter" jumps are bound from JUMPS, so the overlay cannot drift
// from them.

/** "g" then one of these keys goes to a page. */
export const JUMPS: Record<string, { to: string; label: string }> = {
  n: { to: '/', label: tr('Now') },
  t: { to: '/templates', label: tr('Templates') },
  j: { to: '/jobs', label: tr('Jobs') },
  s: { to: '/schedules', label: tr('Schedules') },
  a: { to: '/approvals', label: tr('Approvals') },
  i: { to: '/inventories', label: tr('Inventories') },
  p: { to: '/projects', label: tr('Projects') },
  c: { to: '/credentials', label: tr('Credentials') },
}

export interface Shortcut {
  /** Keys pressed one after the other; "mod+k" is one chord. */
  keys: string[]
  what: string
}

export const SHORTCUTS: { group: string; items: Shortcut[] }[] = [
  {
    group: tr('Anywhere'),
    items: [
      { keys: ['mod+k'], what: tr('Jump to a template, job or host; type > for commands') },
      { keys: ['/'], what: tr('Filter the list, or jump when there is none') },
      { keys: ['?'], what: tr('Show these shortcuts') },
      { keys: ['escape'], what: tr('Close a dialog or menu') },
      ...Object.entries(JUMPS).map(([k, j]) => ({ keys: ['g', k], what: tr('Go to {label}', { label: j.label }) })),
    ],
  },
  {
    group: tr('Lists'),
    items: [
      { keys: ['j'], what: tr('Next row') },
      { keys: ['k'], what: tr('Previous row') },
      { keys: ['enter'], what: tr('Open the row') },
      { keys: ['x'], what: tr('Select the row, where rows can be selected') },
    ],
  },
  {
    group: tr('On a row'),
    items: [
      { keys: ['l'], what: tr('Templates, management jobs: launch') },
      { keys: ['s'], what: tr('Templates: star. Projects, inventory sources: sync') },
      { keys: ['r'], what: tr('Jobs: relaunch. An inventory’s hosts: run a command') },
      { keys: ['a'], what: tr('Approvals: approve') },
      { keys: ['d'], what: tr('Approvals: deny') },
    ],
  },
  {
    group: tr('Workflow editor'),
    items: [
      { keys: ['arrowleft', 'arrowright', 'arrowup', 'arrowdown'], what: tr('Move between steps') },
      { keys: ['a'], what: tr('Add a step after the selected one') },
      { keys: ['delete'], what: tr('Remove the selected step') },
    ],
  },
  {
    group: tr('Job output'),
    items: [{ keys: ['arrowleft', 'arrowright', 'arrowup', 'arrowdown'], what: tr('Move between cells of the hosts × tasks grid') }],
  },
]

const names: Record<string, [string, string]> = {
  mod: ['⌘', 'Ctrl'],
  escape: ['Esc', 'Esc'],
  enter: ['Enter', 'Enter'],
  delete: ['Delete', 'Delete'],
  arrowleft: ['←', '←'],
  arrowright: ['→', '→'],
  arrowup: ['↑', '↑'],
  arrowdown: ['↓', '↓'],
}

/** The keys of one press, as shown: "mod+k" is ["⌘", "K"] on a Mac. */
export function keyCaps(key: string, mac: boolean): string[] {
  return key.split('+').map((k) => names[k]?.[mac ? 0 : 1] ?? (k.length === 1 ? k.toUpperCase() : k))
}

export const isMac = () => typeof navigator !== 'undefined' && /Mac|iPhone|iPad/.test(navigator.platform)
