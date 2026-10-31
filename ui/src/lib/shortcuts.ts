// Every keyboard shortcut, for the "?" overlay (roadmap M7-15). The shell's
// "g then a letter" jumps are bound from JUMPS, so the overlay cannot drift
// from them.

/** "g" then one of these keys goes to a page. */
export const JUMPS: Record<string, { to: string; label: string }> = {
  n: { to: '/', label: 'Now' },
  t: { to: '/templates', label: 'Templates' },
  j: { to: '/jobs', label: 'Jobs' },
  s: { to: '/schedules', label: 'Schedules' },
  a: { to: '/approvals', label: 'Approvals' },
  i: { to: '/inventories', label: 'Inventories' },
  p: { to: '/projects', label: 'Projects' },
  c: { to: '/credentials', label: 'Credentials' },
}

export interface Shortcut {
  /** Keys pressed one after the other; "mod+k" is one chord. */
  keys: string[]
  what: string
}

export const SHORTCUTS: { group: string; items: Shortcut[] }[] = [
  {
    group: 'Anywhere',
    items: [
      { keys: ['mod+k'], what: 'Jump to a template, job or host; type > for commands' },
      { keys: ['/'], what: 'Filter the list, or jump when there is none' },
      { keys: ['?'], what: 'Show these shortcuts' },
      { keys: ['escape'], what: 'Close a dialog or menu' },
      ...Object.entries(JUMPS).map(([k, j]) => ({ keys: ['g', k], what: `Go to ${j.label}` })),
    ],
  },
  {
    group: 'Lists',
    items: [
      { keys: ['j'], what: 'Next row' },
      { keys: ['k'], what: 'Previous row' },
      { keys: ['enter'], what: 'Open the row' },
      { keys: ['x'], what: 'Select the row, where rows can be selected' },
    ],
  },
  {
    group: 'On a row',
    items: [
      { keys: ['l'], what: 'Templates, management jobs: launch' },
      { keys: ['s'], what: 'Templates: star. Projects, inventory sources: sync' },
      { keys: ['r'], what: 'Jobs: relaunch. An inventory’s hosts: run a command' },
      { keys: ['a'], what: 'Approvals: approve' },
      { keys: ['d'], what: 'Approvals: deny' },
    ],
  },
  {
    group: 'Workflow editor',
    items: [
      { keys: ['arrowleft', 'arrowright', 'arrowup', 'arrowdown'], what: 'Move between steps' },
      { keys: ['a'], what: 'Add a step after the selected one' },
      { keys: ['delete'], what: 'Remove the selected step' },
    ],
  },
  {
    group: 'Job output',
    items: [{ keys: ['arrowleft', 'arrowright', 'arrowup', 'arrowdown'], what: 'Move between cells of the hosts × tasks grid' }],
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
