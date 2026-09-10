import { ActionButton } from './ActionButton'
import { useEffect, useRef, useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'
import { Icon } from './Icons'

type Application = { id: string; name: string; kind: 'editor' | 'terminal' }
type Continuity = { available?: boolean; ready: boolean; preparing?: boolean; cleanedUp?: boolean; selectionRequired?: boolean; path?: string; reason?: string; applications?: Application[] }
export function ExternalHandoff({ roomId, taskId }: { roomId: string; taskId?: string }) {
  const { t } = useI18n()
  const [state, setState] = useState<Continuity | null>(null)
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const menu = useRef<HTMLDetailsElement>(null)
  useEffect(() => {
    let cancelled = false
    setState(null)
    setError('')
    let timer: ReturnType<typeof setTimeout> | undefined
    const load = () => { void api<Continuity>(`/api/projects/${encodeURIComponent(roomId)}/continuity${taskId ? `?taskId=${encodeURIComponent(taskId)}` : ''}`)
      .then((value) => {
        if (cancelled) return
        setState(value)
        if (value.preparing) timer = setTimeout(load, 1000)
      })
      .catch((reason) => { if (!cancelled) setError(message(reason)) })
    }
    load()
    return () => { cancelled = true; clearTimeout(timer) }
  }, [roomId, taskId, refresh])
  useEffect(() => {
    const close = (event: PointerEvent) => { if (menu.current && !menu.current.contains(event.target as Node)) menu.current.open = false }
    document.addEventListener('pointerdown', close)
    return () => document.removeEventListener('pointerdown', close)
  }, [])
  async function open(application: string) {
    if (menu.current) menu.current.open = false
    setBusy(true)
    setError('')
    try {
      await api(`/api/projects/${encodeURIComponent(roomId)}/open-external`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('open-external') },
        body: JSON.stringify({ application, taskId }),
      })
    } catch (reason) { setError(message(reason)) } finally { setBusy(false) }
  }
  if (state?.available === false) return null
  if (state?.cleanedUp) return <section className="external-toolbar" aria-label={t('External tools')}>
    <p className="external-status" role="status">{t('Task worktrees have been cleaned up. Review and delivery history remain available.')}</p>
  </section>
  if (state?.selectionRequired && !taskId) return <section className="external-toolbar" aria-label={t('External tools')}>
    <p role="status">{t('Open a task to use its worktree in an editor or terminal.')}</p>
  </section>
  const applications = state?.applications ?? []
  const editor = applications.find((app) => app.kind === 'editor')
  const terminal = applications.find((app) => app.kind === 'terminal')
  const label = (app: Application) => t('Open in {application}', { application: app.name })
  return <section className="external-toolbar" aria-label={t('External tools')}>
    <div className="external-location" title={state?.path}>
      <Icon name="folder" />
      <span className="external-location-kind">{t(taskId ? 'Task worktree' : 'Project repository')}</span>
      {state?.path && <span className="external-path">{state.path}</span>}
    </div>
    <div className="external-actions">
      {[editor, terminal].filter((app): app is Application => Boolean(app)).map((app) =>
        <ActionButton key={app.id} type="button" className="icon-button" title={label(app)} aria-label={label(app)} disabled={!state?.ready || busy} disabledReason={busy ? 'Opening the application. Please wait.' : state?.reason || (state?.preparing ? 'Preparing task worktree…' : error || 'Checking the workspace directory. Please wait.')} onClick={() => void open(app.id)}><Icon name={app.id === 'editor' ? 'vscode' : app.kind} /></ActionButton>)}
      <details className="external-menu" ref={menu} onKeyDown={(event) => { if (event.key === 'Escape' && menu.current) { menu.current.open = false; menu.current.querySelector('summary')?.focus() } }}>
        <summary className="icon-button" aria-label={t('Open with…')} title={t('Open with…')}><Icon name="chevron-down" /></summary>
        <div className="external-menu-panel">
          <p className="external-menu-label">{t('Installed applications')}</p>
          {applications.length === 0 && <p className="external-menu-empty">{t('No supported applications found')}</p>}
          {applications.map((app) => <ActionButton type="button" key={app.id} disabled={!state?.ready || busy} disabledReason={busy ? 'Opening the application. Please wait.' : state?.reason || (state?.preparing ? 'Preparing task worktree…' : error || 'Checking the workspace directory. Please wait.')} onClick={() => void open(app.id)}><Icon name={app.id === 'editor' ? 'vscode' : app.kind} /><span>{app.name}</span></ActionButton>)}
          <ActionButton type="button" className="external-refresh" disabled={busy} disabledReason={'Wait for the current operation to finish.'} onClick={() => { if (menu.current) menu.current.open = false; setRefresh((n) => n + 1) }}><Icon name="refresh" /><span>{t('Check again')}</span></ActionButton>
        </div>
      </details>
    </div>
    {state && !state.ready && <p className={state.preparing ? "external-status" : "external-error"} role={state.preparing ? "status" : "alert"}>{t(state.reason ?? 'Project directory is unavailable')}</p>}
    {error && <p className="external-error" role="alert">{t(error)}</p>}
  </section>
}
