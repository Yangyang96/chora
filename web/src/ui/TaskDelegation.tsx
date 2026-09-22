import { useEffect, useRef, useState } from 'react'
import { api, commandKey } from '../api'
import { useI18n } from '../i18n'

type Assignment = { role: string; title: string; requirement: string }
type Delegation = {
  parentTaskId: string; version: number; state: string; reason?: string; eligible: boolean; parentUrl?: string
  assignments: Assignment[]
  children: { position: number; role: string; title: string; taskId?: string; runId?: string; state: string; url?: string; resultId?: string; resultDigest?: string; markdown?: string }[]
}

export function TaskDelegation({ taskId, onNavigate }: { taskId: string; onNavigate: (url: string) => void }) {
  const { t } = useI18n()
  const [view, setView] = useState<Delegation | null>(null)
  const [assignments, setAssignments] = useState<Assignment[]>([{ role: '', title: '', requirement: '' }])
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const epoch = useRef(0)
  const endpoint = `/api/tasks/${encodeURIComponent(taskId)}/delegation`
  useEffect(() => {
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    const generation = ++epoch.current
    async function load() {
      try {
        const next = await api<Delegation>(endpoint, { signal: controller.signal })
        if (!controller.signal.aborted && generation === epoch.current) { setView(next); setError('') }
      } catch (e) {
        if (!controller.signal.aborted && generation === epoch.current) setError(e instanceof Error ? e.message : String(e))
      } finally {
        if (!controller.signal.aborted && generation === epoch.current) timer = setTimeout(() => void load(), 2000)
      }
    }
    if (!busy) void load()
    return () => { controller.abort(); clearTimeout(timer); ++epoch.current }
  }, [endpoint, refresh, busy])

  async function mutate(action: 'start' | 'stop' | 'resume') {
    ++epoch.current
    setBusy(true); setError('')
    try {
      const next = await api<Delegation>(action === 'start' ? endpoint : `${endpoint}/${action}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey(`delegation-${action}`) },
        body: JSON.stringify(action === 'start' ? { assignments } : { expectedVersion: view?.version }),
      })
      setView(next)
    } catch (e) { setError(e instanceof Error ? e.message : String(e)) }
    finally { setBusy(false); setRefresh(n => n + 1) }
  }
  function edit(index: number, key: keyof Assignment, value: string) {
    setAssignments(items => items.map((item, i) => i === index ? { ...item, [key]: value } : item))
  }
  const valid = assignments.every(a => a.role.trim() && a.title.trim() && a.requirement.trim()) && new Set(assignments.map(a => a.role.trim().toLowerCase())).size === assignments.length
  if (view?.state === 'child') return <section className="panel" aria-label={t('Agent delegation')}><h2>{t('Agent delegation')}</h2><button type="button" onClick={() => view.parentUrl && onNavigate(view.parentUrl)}>{t('Open parent task')}</button></section>
  if (view && !view.eligible && view.state === 'not_started') return null
  return <section className="panel" aria-label={t('Agent delegation')}>
    <h2>{t('Agent delegation')}</h2>
    <p>{t('Define up to four research assignments. Start authorizes sequential Agent execution with this task’s frozen material and settings. You review each result; nothing is accepted or delivered automatically.')}</p>
    {error && <p role="alert">{error}</p>}
    {!view && <p role="status">{t('Loading…')}</p>}
    {view?.state === 'not_started' && <form onSubmit={event => { event.preventDefault(); if (valid && !busy) void mutate('start') }}>
      {assignments.map((assignment, index) => <fieldset key={index} disabled={busy}>
        <legend>{t('Assignment')} {index + 1}</legend>
        <label>{t('Agent role')}<input aria-label={`${t('Agent role')} ${index + 1}`} value={assignment.role} maxLength={80} required onChange={e => edit(index, 'role', e.target.value)} /></label>
        <label>{t('Task title')}<input aria-label={`${t('Task title')} ${index + 1}`} value={assignment.title} maxLength={256} required onChange={e => edit(index, 'title', e.target.value)} /></label>
        <label>{t('Assignment instructions')}<textarea aria-label={`${t('Assignment instructions')} ${index + 1}`} value={assignment.requirement} maxLength={4000} required onChange={e => edit(index, 'requirement', e.target.value)} /></label>
        {assignments.length > 1 && <button type="button" onClick={() => setAssignments(items => items.filter((_, i) => i !== index))}>{t('Remove assignment')}</button>}
      </fieldset>)}
      <div className="actions">
        <button type="button" disabled={busy || assignments.length >= 4} onClick={() => setAssignments(items => [...items, { role: '', title: '', requirement: '' }])}>{t('Add assignment')}</button>
        <button type="submit" className="btn-primary" disabled={busy || !valid}>{t('Start delegation')}</button>
      </div>
    </form>}
    {view && view.state !== 'not_started' && <>
      <p role="status">{t('Delegation status')}: {t(view.state)}</p>
      {view.reason && <p>{view.reason}</p>}
      <ol>{view.children.map(child => <li key={child.position}>
        <strong>{child.role}</strong> · {child.title} · {t(child.state)}{' '}
        {child.url && <button type="button" onClick={() => onNavigate(child.url!)}>{t('Open child task')}</button>}
        {child.markdown && <details><summary>{t('Delegated finding')}</summary><pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{child.markdown}</pre><p>{t('Source result')}: {child.resultId}</p><p style={{ overflowWrap: 'anywhere' }}>{t('Result digest')}: {child.resultDigest}</p></details>}
      </li>)}</ol>
      {view.state === 'awaiting_review' && <p>{t('All assignments finished execution. Review their evidence and results before accepting them.')}</p>}
      {(view.state === 'running' || view.state === 'blocked') && <button type="button" disabled={busy} onClick={() => void mutate('stop')}>{t('Stop delegation')}</button>}
      {view.state === 'blocked' && view.eligible && <button type="button" disabled={busy} onClick={() => void mutate('resume')}>{t('Resume delegation')}</button>}
    </>}
  </section>
}
