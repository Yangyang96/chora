import { useEffect, useRef, useState } from 'react'
import { api, APIError, message } from '../api'
import { useI18n } from '../i18n'
import type { ProjectView } from '../types'
import { boardPhases, type BoardCard, type TaskBoardPage } from '../taskBoardTypes'
import './taskBoard.css'

const labels: Record<string, string> = {
  editing_plan: 'Edit plan', plan_review: 'Review plan', ready_to_start: 'Ready to start', replanning: 'Replan',
  resource_preparation: 'Preparing workspace', resource_preparation_failed: 'Workspace preparation failed',
  waiting_retry: 'Waiting to retry', executing: 'Executing', changes_requested: 'Changes requested', related_task: 'Open related Task',
  pending_apply: 'Waiting to apply', pending_commit: 'Commit changes', pending_push: 'Push branch', pending_pr: 'Create pull request',
  pending_merge: 'Merge PR', pr_closed: 'PR closed · Not merged', apply_uncertain: 'Local application needs recovery',
  apply_conflict: 'Local application conflict', apply_writing: 'Applying changes', delivery_recovery_required: 'Delivery needs recovery',
  delivery_writing: 'Delivery in progress', applying: 'Applying changes', waiting_gate: 'Needs your input',
  needs_reconciliation: 'Needs reconciliation', apply_recovery: 'Local application needs recovery',
  retry_pending: 'Waiting to retry', retrying: 'Retrying automatically', retry_exhausted: 'Automatic retries exhausted', retry_blocked: 'Retry blocked',
  open_task: 'View details', view_result: 'View result', retry: 'Review retry options', monitor: 'View progress',
  open_checks: 'View checks', recover: 'Recover execution', open_delivery: 'View delivery details', answer_gate: 'Review required decision',
  commit: 'Committed', push: 'Pushed', pr: 'PR created', merged: 'Merged',
  not_started: 'Not started', checks_none: 'No checks selected', checks_not_applicable: 'Checks not applicable', checks_passed: 'Checks passed', checks_failed: 'Checks failed', checks_unavailable: 'Checks unavailable',

  preparing: 'Preparing', working: 'Working', review: 'Review', delivery: 'Delivery', finished: 'Finished',
  reconciliation: 'Needs reconciliation', edit_plan: 'Edit plan', review_plan: 'Review plan', start_run: 'Start Run',
  start_attempt: 'Ready to start', monitor_run: 'View progress', recover_run: 'Recover execution',
  recover_verification: 'Recover verification', review_result: 'Review result', apply_patch: 'Apply accepted changes',
  retry_implementation: 'Changes requested', continue_successor_plan: 'Replan', open_related_task: 'Open related Task',
  view_terminal: 'View details', none: 'View details', running: 'Executing', stopping: 'Stopping',
  awaiting_verification: 'Waiting for checks', verifying: 'Checking', recovery_required: 'Needs recovery',
  verification_recovery_required: 'Verification needs recovery', revision_required: 'Changes requested',
  awaiting_review: 'Awaiting result review', accepted: 'Accepted · Delivery pending', completed: 'No changes',
  ready: 'Ready to start', draft: 'Preparing', cancelled: 'Cancelled', stopped: 'Stopped · Can retry',
  delivered: 'Delivered', applied_locally: 'Applied locally', no_change: 'No changes', closed: 'Closed',
  partially_delivered_closed: 'Partly delivered · Remaining work closed', mixed_delivery: 'Mixed delivery',
  superseded: 'Superseded', waiting_for_retry: 'Waiting to retry', automatic_retry: 'Retrying automatically',
  blocking_gate: 'Needs your input', result_review: 'Awaiting result review', cleanup_failed: 'Cleanup needs attention',
  delivery_pending: 'Delivery pending', delivery_failed: 'Delivery needs attention', unknown: 'Needs reconciliation',
}
export function boardLabel(code: string) { return labels[code] ?? 'Needs reconciliation' }

function readFilters() {
  const q = new URLSearchParams(window.location.search)
  return {
    mode: q.get('mode') === 'list' ? 'list' : 'board', roomId: q.get('roomId') ?? '', repoId: q.get('repoId') ?? '',
    phase: q.get('phase') ?? '', attention: q.get('attention') === 'required', archived: q.get('archived') === 'include',
  }
}

export function TaskBoard({ project, roomId, onNavigate, onNewTask }: {
  project: ProjectView; roomId?: string; onNavigate: (url: string) => void; onNewTask: () => void
}) {
  const { t, locale } = useI18n()
  const [filters, setFilters] = useState(readFilters)
  const [page, setPage] = useState<TaskBoardPage>()
  const [options, setOptions] = useState<Pick<TaskBoardPage, 'rooms' | 'repositories' | 'optionsSnapshot' | 'nextOptionsCursor'>>()
  const [failure, setFailure] = useState('')
  const [loading, setLoading] = useState(false)
  const reload = useRef<(() => void) | undefined>(undefined)
  const more = useRef<(() => void) | undefined>(undefined)
  const moreOptions = useRef<(() => void) | undefined>(undefined)
  const pageRef = useRef(page)
  const restoredPosition = useRef(false)
  const scopeRoom = roomId ?? filters.roomId
  const room = project.rooms.find((item) => item.id === roomId)
  const readOnly = project.state === 'archived' || room?.state === 'archived'
  const creationRoom = project.rooms.find((item) => item.id === (roomId ?? project.defaultRoomId))
  const phaseNames: Record<string, string> = { preparing: '准备中', working: '处理中', review: '待审阅', delivery: '交付中', finished: '已结束' }
  const phaseLabel = (phase: string) => locale === 'zh-CN' && phaseNames[phase] ? phaseNames[phase] : t(boardLabel(phase))

  useEffect(() => {
    const restore = () => setFilters(readFilters())
    window.addEventListener('popstate', restore)
    return () => window.removeEventListener('popstate', restore)
  }, [])

  useEffect(() => {
    if (!page || restoredPosition.current) return
    restoredPosition.current = true
    const saved = window.history.state?.taskBoardPosition
    if (!saved) return
    const element = document.getElementById(saved.focusId)
    if (element instanceof HTMLElement) element.focus({ preventScroll: true })
    const main = document.querySelector('.app-main')
    if (main && typeof saved.scrollTop === 'number') main.scrollTop = saved.scrollTop
  }, [page])

  function change(values: Partial<typeof filters>) {
    const next = { ...filters, ...values }
    const q = new URLSearchParams()
    if (next.mode === 'list') q.set('mode', 'list')
    if (next.roomId && !roomId) q.set('roomId', next.roomId)
    if (next.repoId) q.set('repoId', next.repoId)
    if (next.phase) q.set('phase', next.phase)
    if (next.attention) q.set('attention', 'required')
    if (next.archived) q.set('archived', 'include')
    window.history.pushState({}, '', `${window.location.pathname}${q.size ? `?${q}` : ''}`)
    setFilters(next)
  }

  useEffect(() => {
    let disposed = false
    let active: AbortController | undefined
    let timer: number | undefined
    let loadedOptions: typeof options
    const params = new URLSearchParams({ limit: '50' })
    if (scopeRoom) params.set('roomId', scopeRoom)
    if (filters.repoId) params.set('repoId', filters.repoId)
    if (filters.phase) params.set('phase', filters.phase)
    if (filters.attention) params.set('attention', 'required')
    if (filters.archived) params.set('archived', 'include')
    const base = `/api/v2/projects/${encodeURIComponent(project.id)}/task-board?${params}`
    pageRef.current = undefined
    setPage(undefined); setOptions(undefined); setFailure('')

    const schedule = () => {
      window.clearTimeout(timer)
      if (!disposed && document.visibilityState !== 'hidden') timer = window.setTimeout(() => void load(), 2000)
    }
    const load = async (cursor?: string, optionsCursor?: string) => {
      if (disposed || active || document.visibilityState === 'hidden') return
      window.clearTimeout(timer)
      active = new AbortController()
      setLoading(true)
      try {
        const result = await api<TaskBoardPage>(`${base}${cursor ? `&cursor=${encodeURIComponent(cursor)}` : ''}${optionsCursor ? `&optionsCursor=${encodeURIComponent(optionsCursor)}` : ''}`, { signal: active.signal })
        if (disposed) return
        const retainedOptions = result.optionsSnapshot && loadedOptions?.optionsSnapshot === result.optionsSnapshot ? loadedOptions : undefined
        loadedOptions = {
          optionsSnapshot: result.optionsSnapshot,
          rooms: Array.from(new Map([...(retainedOptions?.rooms ?? []), ...result.rooms].map((item) => [item.roomId, item])).values()),
          repositories: Array.from(new Map([...(retainedOptions?.repositories ?? []), ...result.repositories].map((item) => [item.repoId, item])).values()),
          nextOptionsCursor: !optionsCursor && retainedOptions ? retainedOptions.nextOptionsCursor : result.nextOptionsCursor,
        }
        setOptions(loadedOptions)
        // Filter-option pagination has its own snapshot and must not replace
        // already loaded Task pages or advance their cursor.
        if (optionsCursor) return
        // A refresh restarts pagination. A successful next page retains the same
        // backend snapshot; stale cursors are discarded, never mixed into totals.
        const retained = !cursor && pageRef.current?.snapshot === result.snapshot ? pageRef.current : undefined
        const previous = cursor || retained ? pageRef.current?.cards ?? [] : []
        const cards = Array.from(new Map([...previous, ...result.cards].map((card) => [card.taskId, card])).values())
        const next = { ...result, cards, nextCursor: retained ? retained.nextCursor : result.nextCursor }
        pageRef.current = next; setPage(next); setFailure('')
      } catch (reason) {
        if (disposed) return
        setFailure(message(reason))
        if (optionsCursor && reason instanceof APIError && reason.status === 409) {
          loadedOptions = undefined
          setOptions((current) => current ? { ...current, nextOptionsCursor: undefined } : undefined)
        }
        if (cursor && reason instanceof APIError && reason.status === 409) {
          pageRef.current = undefined; setPage(undefined)
        }
      } finally {
        active = undefined
        if (!disposed) { setLoading(false); schedule() }
      }
    }
    const resume = () => { if (document.visibilityState !== 'hidden') void load() }
    const visibility = () => {
      if (document.visibilityState === 'hidden') window.clearTimeout(timer)
      else resume()
    }
    reload.current = () => void load()
    more.current = () => { const cursor = pageRef.current?.nextCursor; if (cursor) void load(cursor) }
    moreOptions.current = () => { const cursor = loadedOptions?.nextOptionsCursor; if (cursor) void load(undefined, cursor) }
    window.addEventListener('focus', resume)
    window.addEventListener('online', resume)
    document.addEventListener('visibilitychange', visibility)
    void load()
    return () => {
      disposed = true; active?.abort(); window.clearTimeout(timer); more.current = undefined; moreOptions.current = undefined; reload.current = undefined
      window.removeEventListener('focus', resume); window.removeEventListener('online', resume)
      document.removeEventListener('visibilitychange', visibility)
    }
  }, [project.id, scopeRoom, filters.repoId, filters.phase, filters.attention, filters.archived])

  function follow(event: React.MouseEvent<HTMLAnchorElement>, url: string) {
    if (event.button !== 0 || event.metaKey || event.ctrlKey || event.shiftKey || event.altKey) return
    event.preventDefault()
    const main = document.querySelector('.app-main')
    window.history.replaceState({ ...window.history.state, taskBoardPosition: { focusId: event.currentTarget.id, scrollTop: main?.scrollTop ?? 0 } }, '', window.location.href)
    onNavigate(url)
  }
  function cardView(card: BoardCard) {
    // Board data only navigates. The owning screen reloads state and authority.
    const fallback = `/rooms/${encodeURIComponent(card.roomId)}/tasks/${encodeURIComponent(card.taskId)}`
    const primaryURL = card.source.latestRunId ? `${fallback}/runs/${encodeURIComponent(card.source.latestRunId)}` : fallback
    const target = card.nextAction.url
    const safeURL = /^\/rooms\/[^/?#]+\/tasks\/[^/?#]+(?:\/runs\/[^/?#]+)?$/.test(target) ? target : fallback
    return <article key={card.taskId} className={`board-card${card.visibility.readOnly ? ' board-card-readonly' : ''}`}>
      <h3><a id={`board-title-${card.taskId}`} href={primaryURL} onClick={(event) => follow(event, primaryURL)}>{card.title}</a></h3>
      <p className="board-card-context">{card.roomName} · {t('{count} repositories', { count: card.repositories.length })}</p>
      {card.repositories.length > 0 && <ul className="board-repositories">{card.repositories.map((repo) => <li key={repo.repoId}>{repo.name}{repo.status ? ` · ${phaseLabel(repo.status)}` : ''}{repo.checks ? ` · ${phaseLabel(`checks_${repo.checks}`)}` : ''}{Boolean(repo.achievements?.length) && <span> · {t('Recorded achievements')}: {repo.achievements!.map(phaseLabel).join(', ')}</span>}{repo.deliveryObservedAt && <time dateTime={repo.deliveryObservedAt}>{t('Delivery observed at {time}', { time: new Date(repo.deliveryObservedAt).toLocaleString(locale) })}</time>}</li>)}</ul>}
      <p>{phaseLabel(card.substate)}</p>
      {card.outcome && <p><strong>{phaseLabel(card.outcome)}</strong></p>}
      {card.attention.state !== 'none' && <div className="board-attention"><strong>{t(card.attention.state === 'unknown' ? 'Needs reconciliation' : 'Needs attention')}</strong><ul>{card.attention.reasons.map((reason, i) => <li key={`${reason.code}:${i}`}>{phaseLabel(reason.code)}{reason.summary && <p>{reason.summary}</p>}</li>)}</ul></div>}
      {card.visibility.readOnly && <p>{t('Read-only history')}</p>}
      {card.lastActivityAt && <time dateTime={card.lastActivityAt}>{new Date(card.lastActivityAt).toLocaleString(locale)}</time>}
      <a id={`board-next-${card.taskId}`} className="board-next" href={safeURL} onClick={(event) => follow(event, safeURL)}>{failure || card.visibility.readOnly ? t('View details') : phaseLabel(card.nextAction.kind)}</a>
    </article>
  }
  const phases = [...boardPhases, 'reconciliation']
  return <section className="task-board" aria-label={t('Task board')}>
    <header className="task-board-header">
      <div><p className="page-eyebrow">{project.name}{room ? ` / ${room.name}` : ''}</p><h1>{t('Tasks')}</h1></div>
      <div className="form-actions"><button type="button" className="btn-secondary" onClick={() => onNavigate(roomId ? `/rooms/${encodeURIComponent(roomId)}` : `/projects/${encodeURIComponent(project.id)}`)}>{t(roomId ? 'Room home' : 'Project resources')}</button><button type="button" className="btn-primary" disabled={readOnly || !creationRoom || creationRoom.state !== 'active'} onClick={onNewTask}>{t('＋ New Task')}</button></div>
    </header>
    {readOnly && <p className="warning-banner">{t('Read-only history')}</p>}
    <div className="board-filters">
      <div role="group" aria-label={t('Task presentation')}><button type="button" aria-pressed={filters.mode === 'board'} onClick={() => change({ mode: 'board' })}>{t('Board')}</button><button type="button" aria-pressed={filters.mode === 'list'} onClick={() => change({ mode: 'list' })}>{t('List')}</button></div>
      {!roomId && <label>{t('Room')}<select value={filters.roomId} onChange={(event) => change({ roomId: event.target.value })}><option value="">{t('All Rooms')}</option>{(options?.rooms ?? project.rooms.slice(0, 100).map((item) => ({ roomId: item.id, name: item.name }))).map((item) => <option key={item.roomId} value={item.roomId}>{item.name}</option>)}</select></label>}
      <label>{t('Repository')}<select value={filters.repoId} onChange={(event) => change({ repoId: event.target.value })}><option value="">{t('All repositories')}</option>{(options?.repositories ?? []).map((item) => <option key={item.repoId} value={item.repoId}>{item.name}</option>)}</select></label>
      {options?.nextOptionsCursor && <button type="button" disabled={loading || Boolean(failure)} onClick={() => moreOptions.current?.()}>{t('Load more filters')}</button>}
      <label>{t('Phase')}<select value={filters.phase} onChange={(event) => change({ phase: event.target.value })}><option value="">{t('All phases')}</option>{phases.map((phase) => <option key={phase} value={phase}>{phaseLabel(phase)}</option>)}</select></label>
      <label><input type="checkbox" checked={filters.attention} onChange={(event) => change({ attention: event.target.checked })} />{t('Needs attention')}</label>
      <label><input type="checkbox" checked={filters.archived} onChange={(event) => change({ archived: event.target.checked })} />{t('Include archived')}</label>
      <button type="button" disabled={loading} onClick={() => reload.current?.()}>{t('Refresh')}</button>
    </div>
    {failure && <p className="warning-banner" role="alert">{t(page ? 'Refresh failed. Showing the last observed state.' : 'Task board unavailable. Retry to load tasks.')} <span>{failure}</span></p>}
    {!page && !failure && <p role="status">{t('Loading…')}</p>}
    {page && <>
      <p className="board-freshness">{t('{shown} of {total} tasks', { shown: page.cards.length, total: page.total })} · {t('Needs attention')}: {page.counts.attention} · {t('Needs reconciliation')}: {page.counts.reconciliation} · {t('Read at {time}', { time: new Date(page.observedAt).toLocaleTimeString(locale) })}</p>
      {page.total === 0 ? <p className="empty">{t('No tasks match these filters.')}</p> : filters.mode === 'list' ? <div className="board-task-list">{page.cards.map((card) => <div key={card.taskId}><span className="board-list-phase">{phaseLabel(card.phase ?? 'reconciliation')}</span>{cardView(card)}</div>)}</div> : <div className="board-columns">{phases.map((phase) => {
        const cards = page.cards.filter((card) => (card.phase ?? 'reconciliation') === phase)
        const count = phase === 'reconciliation' ? page.counts.reconciliation : page.counts.phase[phase] ?? 0
        if (phase === 'reconciliation' && count === 0) return null
        return <section key={phase} className={`board-column board-column-${phase}`} aria-label={phaseLabel(phase)}><h2>{phaseLabel(phase)} <span>{count}</span></h2>{cards.map(cardView)}{cards.length === 0 && <p className="board-card-context">{t(count ? 'More tasks on later pages' : 'No tasks')}</p>}</section>
      })}</div>}
      {page.nextCursor && <button type="button" className="btn-secondary" disabled={loading || Boolean(failure)} onClick={() => more.current?.()}>{t('Load more tasks')}</button>}
    </>}
  </section>
}
