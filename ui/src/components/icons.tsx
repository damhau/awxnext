// Line icons on a 16px grid, 1.5px strokes; they inherit currentColor.
import type { SVGProps } from 'react'

const paths: Record<string, string> = {
  search: 'M7 11.5a4.5 4.5 0 1 0 0-9 4.5 4.5 0 0 0 0 9zM10.5 10.5 14 14',
  chevron: 'M4 6l4 4 4-4',
  chevronRight: 'M6 4l4 4-4 4',
  play: 'M4.5 3v10l8-5z',
  plus: 'M8 3v10M3 8h10',
  x: 'M4 4l8 8M12 4l-8 8',
  more: 'M3.5 8h.01M8 8h.01M12.5 8h.01',
  folder: 'M1.5 4.5v8h13v-7H7.5L6 4H1.5z',
  star: 'M8 2l1.8 3.8 4.2.5-3.1 2.9.8 4.1L8 11.3 4.3 13.3l.8-4.1L2 6.3l4.2-.5z',
  refresh: 'M13 8a5 5 0 1 1-1.5-3.5M13 2.5v3h-3',
  copy: 'M5.5 5.5h7v7h-7zM3.5 10.5v-7h7',
  trash: 'M3 4.5h10M6.5 4.5v-2h3v2M4.5 4.5l.5 9h6l.5-9',
  edit: 'M10.5 2.5l3 3-7.5 7.5H3v-3z',
  clock: 'M8 14A6 6 0 1 0 8 2a6 6 0 0 0 0 12zM8 4.5V8l2.5 1.5',
  check: 'M3 8.5l3 3 7-7',
  download: 'M8 2.5v8M4.5 7.5 8 11l3.5-3.5M3 13.5h10',
  external: 'M9.5 2.5h4v4M13.5 2.5 7 9M11.5 9.5v4h-9v-9h4',
  filter: 'M2 3h12L9.5 8.5v4.5l-3-1.5v-3z',
  menu: 'M2.5 4.5h11M2.5 8h11M2.5 11.5h11',
  templates: 'M2.5 2.5h11v11h-11zM2.5 6.5h11M6.5 6.5v7',
  jobs: 'M3 4.5h10M3 8h10M3 11.5h6',
  inventory: 'M3 2.5h10v4H3zM3 9.5h10v4H3z',
  project: 'M4 2.5h8v11H4zM6 5.5h4M6 8h4',
  key: 'M10 6.5a3 3 0 1 1-6 0 3 3 0 0 1 6 0zM9 8.5l4.5 4.5M11.5 11l1.5-1.5',
  calendar: 'M2.5 3.5h11v10h-11zM2.5 6.5h11M5 2v3M11 2v3',
  approve: 'M8 14A6 6 0 1 0 8 2a6 6 0 0 0 0 12zM5.5 8.2l1.8 1.8 3.3-3.6',
  users: 'M6 7.5a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5zM1.5 13.5c.4-2.5 2.2-4 4.5-4s4.1 1.5 4.5 4M11 3a2.2 2.2 0 0 1 0 4.4M12.5 9.6c1.2.5 2 1.8 2.2 3.9',
  team: 'M5 7a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM11 7a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM1.5 13c.3-2.2 1.7-3.5 3.5-3.5S8.2 10.8 8.5 13M7.5 13c.3-2.2 1.7-3.5 3.5-3.5s3.2 1.3 3.5 3.5',
  shield: 'M8 1.5l5.5 2v4c0 3.5-2.3 5.8-5.5 7-3.2-1.2-5.5-3.5-5.5-7v-4z',
  server: 'M2.5 2.5h11v4.5h-11zM2.5 9h11v4.5h-11zM5 4.75h.01M5 11.25h.01',
  activity: 'M1.5 8h3l2-4.5 3 9 2-4.5h3',
  settings: 'M8 10.5a2.5 2.5 0 1 0 0-5 2.5 2.5 0 0 0 0 5zM8 1.5v2M8 12.5v2M1.5 8h2M12.5 8h2M3.4 3.4l1.4 1.4M11.2 11.2l1.4 1.4M12.6 3.4l-1.4 1.4M4.8 11.2l-1.4 1.4',
  bell: 'M4 11V7a4 4 0 1 1 8 0v4l1.5 1.5h-11zM6.5 14h3',
  box: 'M2.5 5 8 2l5.5 3v6L8 14l-5.5-3zM2.5 5 8 8l5.5-3M8 8v6',
  git: 'M5 3v10M5 6a5 5 0 0 0 5 5M11 13a2 2 0 1 0 0-4 2 2 0 0 0 0 4zM5 4.5a1.5 1.5 0 1 0 0-3 1.5 1.5 0 0 0 0 3z',
  org: 'M2.5 13.5v-9l5-2.5 5 2.5v9M5.5 6.5h.01M10.5 6.5h.01M5.5 9.5h.01M10.5 9.5h.01M6.5 13.5v-2h3v2',
  workflow: 'M2.5 3h4v3h-4zM9.5 10h4v3h-4zM4.5 6v2.5a3 3 0 0 0 3 3h2',
  host: 'M2.5 3.5h11v7h-11zM5.5 13.5h5M8 10.5v3',
  stop: 'M4 4h8v8H4z',
  history: 'M2.5 8a5.5 5.5 0 1 0 1.6-3.9M2.5 2.5v2.5H5M8 5v3l2 1.5',
  logout: 'M6 2.5H3v11h3M10 5l3 3-3 3M13 8H6',
  pause: 'M5 3.5v9M11 3.5v9',
  arrowDown: 'M8 3v10M4 9l4 4 4-4',
  info: 'M8 14A6 6 0 1 0 8 2a6 6 0 0 0 0 12zM8 7v4M8 5h.01',
  link: 'M6.5 9.5l3-3M7 4.5l1-1a2.8 2.8 0 0 1 4 4l-1 1M9 11.5l-1 1a2.8 2.8 0 0 1-4-4l1-1',
  terminal: 'M2.5 3h11v10h-11zM5 6.5l2 1.5-2 1.5M8.5 10h2.5',
}

export type IconName = keyof typeof paths

export function Icon({ name, size = 16, ...rest }: { name: IconName; size?: number } & SVGProps<SVGSVGElement>) {
  const filled = name === 'play' || name === 'stop'
  return (
    <svg
      aria-hidden="true"
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill={filled ? 'currentColor' : 'none'}
      stroke={filled ? 'none' : 'currentColor'}
      strokeWidth={1.5}
      strokeLinecap="round"
      strokeLinejoin="round"
      {...rest}
    >
      <path d={paths[name]} />
    </svg>
  )
}
