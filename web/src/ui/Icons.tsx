import type { CSSProperties } from 'react'
export type IconName = 'vscode' | 'editor' | 'terminal' | 'folder' | 'grid' | 'plus' | 'chevron-right' | 'chevron-down' | 'refresh' | 'more' | 'check' | 'clock' | 'alert'
const paths: Record<IconName, React.ReactNode> = {
  vscode: <path d="m16 3 5 2v14l-5 2-9-7-4 3V7l4 3 9-7ZM16 7l-6 5 6 5V7Z" />,
  editor: <><rect x="3" y="4" width="18" height="16" rx="3" /><path d="m9 9-3 3 3 3m6-6 3 3-3 3" /></>,
  terminal: <><rect x="3" y="4" width="18" height="16" rx="3" /><path d="m7 9 3 3-3 3m6 0h4" /></>,
  folder: <path d="M3 7a2 2 0 0 1 2-2h5l2 3h7a2 2 0 0 1 2 2v8a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z" />,
  grid: <><rect x="4" y="4" width="6" height="6" rx="1" /><rect x="14" y="4" width="6" height="6" rx="1" /><rect x="4" y="14" width="6" height="6" rx="1" /><rect x="14" y="14" width="6" height="6" rx="1" /></>,
  plus: <path d="M12 5v14M5 12h14" />,
  'chevron-right': <path d="m9 5 7 7-7 7" />,
  'chevron-down': <path d="m5 9 7 7 7-7" />,
  refresh: <><path d="M20 7v5h-5M4 17v-5h5" /><path d="M6.1 6.1A8 8 0 0 1 20 12M4 12a8 8 0 0 0 13.9 5.9" /></>,
  more: <><circle cx="5" cy="12" r="1" /><circle cx="12" cy="12" r="1" /><circle cx="19" cy="12" r="1" /></>,
  check: <path d="m5 12 4 4L19 6" />,
  clock: <><circle cx="12" cy="12" r="9" /><path d="M12 7v5l3 2" /></>,
  alert: <><path d="m12 3 10 18H2L12 3Z" /><path d="M12 9v4m0 3v1" /></>,
}
export function Icon({ name, style }: { name: IconName; style?: CSSProperties }) {
  return <svg className="ui-icon" style={style} width="18" height="18" viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth="1.6" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">{paths[name]}</svg>
}
