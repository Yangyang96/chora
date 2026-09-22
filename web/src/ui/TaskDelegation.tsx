import { useEffect, useRef, useState } from 'react'
import { api, commandKey } from '../api'
import { useI18n } from '../i18n'

type Assignment = { role: string; title: string; requirement: string }
type ProposalSource = { runId: string; attemptId: string; resultId: string; resultDigest: string; planDigest: string; current: boolean }
type Proposal = { available: boolean; reason?: string; assignments: Assignment[]; source?: ProposalSource }
type Delegation = {
  parentTaskId: string; version: number; state: string; reason?: string; eligible: boolean; parentUrl?: string
  assignments: Assignment[]
  children: { position: number; role: string; title: string; taskId?: string; runId?: string; state: string; url?: string; resultId?: string; resultDigest?: string; markdown?: string }[]
  source?: ProposalSource
  synthesis?: { enabled: boolean; state: string; taskId?: string; runId?: string; url?: string; resultId?: string; resultDigest?: string; markdown?: string; current: boolean; reason?: string }
  planning?: { state: string; version: number; runId?: string; reason?: string }
}

export function TaskDelegation({ taskId, roomId, onNavigate, planningTask = false, starting = false, onStartPlanning }: { taskId: string; roomId?: string; onNavigate: (url: string) => void; planningTask?: boolean; starting?: boolean; onStartPlanning?: (synthesize: boolean) => Promise<void> }) {
  const { t } = useI18n()
  const [view, setView] = useState<Delegation | null>(null)
  const [assignments, setAssignments] = useState<Assignment[]>([{ role: '', title: '', requirement: '' }])
  const [error, setError] = useState('')
  const [loadError, setLoadError] = useState('')
  const [proposal, setProposal] = useState<Proposal | null>(null)
  const [proposalLoading, setProposalLoading] = useState(false)
  const [proposalError, setProposalError] = useState('')
  const proposalRequest = useRef<AbortController | null>(null)
  const [synthesize, setSynthesize] = useState(false)
  const [busy, setBusy] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const epoch = useRef(0)
  const endpoint = `/api/tasks/${encodeURIComponent(taskId)}/delegation`
  useEffect(() => () => proposalRequest.current?.abort(), [endpoint])
  useEffect(() => {
    const controller = new AbortController()
    let timer: ReturnType<typeof setTimeout> | undefined
    const generation = ++epoch.current
    async function load() {
      try {
        const next = await api<Delegation>(endpoint, { signal: controller.signal })
        if (!controller.signal.aborted && generation === epoch.current) { setView(next); setLoadError('') }
      } catch (e) {
        if (!controller.signal.aborted && generation === epoch.current) setLoadError(e instanceof Error ? e.message : String(e))
      } finally {
        if (!controller.signal.aborted && generation === epoch.current) timer = setTimeout(() => void load(), 2000)
      }
    }
    if (!busy) void load()
    return () => { controller.abort(); clearTimeout(timer); ++epoch.current }
  }, [endpoint, refresh, busy])

  async function loadProposal() {
    proposalRequest.current?.abort()
    const controller = new AbortController()
    proposalRequest.current = controller
    setProposal(null); setProposalLoading(true); setProposalError(''); setError('')
    try {
      const next = await api<Proposal>(`${endpoint}/proposal`, { signal: controller.signal })
      if (!controller.signal.aborted) setProposal(next)
    } catch (e) {
      if (!controller.signal.aborted) setProposalError(e instanceof Error ? e.message : String(e))
    } finally {
      if (!controller.signal.aborted) setProposalLoading(false)
    }
  }
  async function mutate(action: 'start' | 'start_proposal' | 'stop' | 'resume' | 'planning_stop' | 'planning_resume') {
    if (action === 'start_proposal' && (!proposal?.available || !proposal.source?.current)) return
    proposalRequest.current?.abort()
    setProposalLoading(false)
    ++epoch.current
    setBusy(true); setError('')
    try {
      const next = await api<Delegation>(action === 'start' || action === 'start_proposal' ? endpoint : `${endpoint}/${action.replace('planning_', 'planning/')}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey(`delegation-${action}`) },
        body: JSON.stringify(action === 'start' ? { assignments, ...(synthesize ? { synthesize: true } : {}) } : action === 'start_proposal' ? { ...(synthesize ? { synthesize: true } : {}), sourceAttemptId: proposal!.source!.attemptId, expectedResultDigest: proposal!.source!.resultDigest } : { expectedVersion: action.startsWith('planning_') ? view?.planning?.version : view?.version }),
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
  if (view && !view.eligible && view.state === 'not_started' && !view.planning) return null
  return <section className="panel" aria-label={t('Agent delegation')}>
    <h2>{t('Agent delegation')}</h2>
    {!planningTask && <p>{t('Define up to four research assignments. Start authorizes sequential Agent execution with this task’s frozen material and settings. You review each result; nothing is accepted or delivered automatically.')}</p>}
    {(error || loadError) && <p role="alert">{error || loadError}</p>}
    {!view && <p role="status">{t('Loading…')}</p>}
    {view?.state === 'not_started' && !view.planning && <>
      <label className="task-choice"><input type="checkbox" checked={synthesize} disabled={busy || starting} onChange={event => setSynthesize(event.target.checked)} />{t('Generate one synthesis after research')}</label>
      {synthesize && <p>{t('Authorizes one additional Agent attempt using only the frozen child results. You review the report; no result is accepted automatically.')}</p>}
    </>}
    {view?.planning && <section aria-label={t('Research planning')}>
      <h3>{t('Research planning')}</h3>
      <p role="status">{t('Planning status')}: {t(view.planning.state)}</p>
      {view.planning.reason && <p>{view.planning.reason}</p>}
      <p>{t('This start authorizes one planning attempt, then up to four research assignments from its valid result. Final acceptance remains yours.')}</p>
      {view.planning.runId && roomId && <button type="button" onClick={() => onNavigate(`/rooms/${encodeURIComponent(roomId)}/tasks/${encodeURIComponent(taskId)}/runs/${encodeURIComponent(view.planning!.runId!)}`)}>{t('Open planning run')}</button>}
      {(view.planning.state === 'planning' || view.planning.state === 'blocked') && <button type="button" disabled={busy} onClick={() => void mutate('planning_stop')}>{t('Stop research planning')}</button>}
      {view.planning.state === 'blocked' && view.eligible && <button type="button" disabled={busy} onClick={() => void mutate('planning_resume')}>{t('Resume research planning')}</button>}
    </section>}
    {view?.state === 'not_started' && !view.planning && planningTask && <p>
      {t('One start authorizes a planning Agent and up to four sequential research assignments. You review the results.')}
      {onStartPlanning && <button type="button" disabled={busy || starting} onClick={() => { setBusy(true); void onStartPlanning(synthesize).finally(() => { setBusy(false); setRefresh(n => n + 1) }) }}>{t('Start research delegation')}</button>}
    </p>}
    {view?.state === 'not_started' && !view.planning && !planningTask && <>
      <section aria-label={t('Agent-proposed plan')}>
        <h3>{t('Agent-proposed plan')}</h3>
        <p>{t('Load fixed assignments from this Task’s latest completed Agent result. Loading a plan does not start work or accept the result.')}</p>
        <button type="button" disabled={busy || proposalLoading} onClick={() => void loadProposal()}>{t('Load Agent plan')}</button>
        {proposalLoading && <p role="status">{t('Loading…')}</p>}
        {proposalError && <p role="alert">{proposalError}</p>}
        {proposal && !proposal.available && <p role="status">{proposal.reason || t('No valid Agent plan is available in the current result.')}</p>}
        {proposal?.available && proposal.source && <>
          <ol>{proposal.assignments.map((assignment, index) => <li key={index}><strong>{assignment.role}</strong> · {assignment.title}<p style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{assignment.requirement}</p></li>)}</ol>
          <PlanSource source={proposal.source} />
          <p>{t('Starting authorizes these fixed assignments with the parent’s frozen material and settings. Final result acceptance remains yours.')}</p>
          <button type="button" className="btn-primary" disabled={busy || proposalLoading || !proposal.source.current} onClick={() => void mutate('start_proposal')}>{t('Start proposed delegation')}</button>
        </>}
      </section>
      <h3>{t('Define assignments manually')}</h3>
      <form onSubmit={event => { event.preventDefault(); if (valid && !busy) void mutate('start') }}>
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
    </form></>}
    {view && view.state !== 'not_started' && <>
      <p role="status">{t('Delegation status')}: {t(view.state)}</p>
      {view.reason && <p>{view.reason}</p>}
      {view.source && <><PlanSource source={view.source} />{!view.source.current && <p role="status">{t('The parent result has changed. This delegation keeps its original plan and source.')}</p>}</>}
      <ol>{view.children.map(child => <li key={child.position}>
        <strong>{child.role}</strong> · {child.title} · {t(child.state)}{' '}
        {child.url && <button type="button" onClick={() => onNavigate(child.url!)}>{t('Open child task')}</button>}
        {child.markdown && <details><summary>{t('Delegated finding')}</summary><pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{child.markdown}</pre><p>{t('Source result')}: {child.resultId}</p><p style={{ overflowWrap: 'anywhere' }}>{t('Result digest')}: {child.resultDigest}</p></details>}
      </li>)}</ol>
      {view.synthesis?.enabled && <section aria-label={t('Synthesis report')}>
        <h3>{t('Synthesis report')}</h3>
        <p role="status">{t('Synthesis status')}: {t(view.synthesis.state)}</p>
        {view.synthesis.reason && <p>{view.synthesis.reason}</p>}
        {view.synthesis.url && <button type="button" onClick={() => onNavigate(view.synthesis!.url!)}>{t('Open synthesis task')}</button>}
        {view.synthesis.markdown && <details><summary>{t('Read synthesis report')}</summary><pre style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{view.synthesis.markdown}</pre><p>{t('Source result')}: {view.synthesis.resultId}</p><p style={{ overflowWrap: 'anywhere' }}>{t('Result digest')}: {view.synthesis.resultDigest}</p></details>}
        {!view.synthesis.current && view.synthesis.taskId && <p role="status">{t('A child result has changed. This report retains its original frozen sources and is not regenerated automatically.')}</p>}
      </section>}
      {view.state === 'awaiting_review' && <p>{t('All assignments finished execution. Review their evidence and results before accepting them.')}</p>}
      {(view.state === 'running' || view.state === 'blocked') && <button type="button" disabled={busy} onClick={() => void mutate('stop')}>{t('Stop delegation')}</button>}
      {view.state === 'blocked' && view.eligible && <button type="button" disabled={busy} onClick={() => void mutate('resume')}>{t('Resume delegation')}</button>}
    </>}
  </section>
}

function PlanSource({ source }: { source: ProposalSource }) {
  const { t } = useI18n()
  return <details><summary>{t('Plan source')}</summary><dl style={{ overflowWrap: 'anywhere' }}>
    <dt>{t('Source result')}</dt><dd>{source.resultId}</dd>
    <dt>{t('Result digest')}</dt><dd>{source.resultDigest}</dd>
    <dt>{t('Plan digest')}</dt><dd>{source.planDigest}</dd>
  </dl></details>
}
