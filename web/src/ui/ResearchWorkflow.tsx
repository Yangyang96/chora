import { useEffect, useRef, useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'

export type ResearchExecution = {
  id: string; taskId: string; projectId: string; version: number; status: string
  reason?: string; stopRequested: boolean; runId?: string
  gate?: { id: string; inputDigest: string; version: number; report: { summary: string; citations: string[]; no_answer: boolean }; decision?: string; instructions?: string }
  projection?: { evidence?: Record<string, { url: string; digest: string }>; budget?: { spent_tokens: number; usage_unknown: boolean }; [key: string]: unknown }
}

export function ResearchWorkflow({ taskId, roomId, onNavigate, onManaged, refreshKey = 0 }: { taskId: string; roomId: string; onNavigate: (url: string) => void; onManaged?: (managed: boolean) => void; refreshKey?: number }) {
  const { t } = useI18n()
  const [value, setValue] = useState<ResearchExecution | null>(null)
  const [instructions, setInstructions] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [entry, setEntry] = useState<{ enabled: boolean; eligible: boolean; projectId: string; question: string } | null>(null)
  const decisionKey = useRef(commandKey('research-decision'))
  useEffect(() => {
    const controller = new AbortController()
    void api<{ workflow: ResearchExecution | null; enabled: boolean; eligible: boolean; projectId: string; question: string }>(`/api/tasks/${encodeURIComponent(taskId)}/workflow`, { signal: controller.signal })
      .then((next) => { if (!controller.signal.aborted) { setValue(next.workflow); setEntry(next); onManaged?.(Boolean(next.workflow)) } })
      .catch(() => {})
    return () => controller.abort()
  }, [taskId, onManaged, refreshKey])
  useEffect(() => {
    if (!value || typeof EventSource === 'undefined') return
    const source = new EventSource(`/api/projects/${encodeURIComponent(value.projectId)}/workflows/${encodeURIComponent(value.id)}/events`)
    const update = (event: MessageEvent) => {
      try {
        const next = JSON.parse(event.data).data as ResearchExecution
        setValue((old) => old && next.id === old.id && next.version > old.version ? next : old)
      } catch { setError(t('Could not read research progress. Reload to reconnect.')) }
    }
    source.addEventListener('workflow', update as EventListener)
    return () => source.close()
  }, [value?.id, value?.projectId, t])
  if (!value) return entry?.enabled && entry.eligible ? <section className="panel" aria-label={t('Research before coding')}>
    <h2>{t('Research before coding')}</h2><p>{t('Research public Chora documentation, then review the proposal before coding starts.')}</p>
    {error && <p role="alert">{error}</p>}
    <button disabled={busy} onClick={() => {
      setBusy(true); setError('')
      void api<ResearchExecution>(`/api/tasks/${encodeURIComponent(taskId)}/workflow`, { method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': `research-${taskId}` }, body: JSON.stringify({ projectId: entry.projectId, question: entry.question }) })
        .then((next) => { setValue(next); onManaged?.(true) }).catch((reason) => setError(message(reason))).finally(() => setBusy(false))
    }}>{t('Start research before coding')}</button>
  </section> : null
  const workflow = value
  async function send(action: string, decision?: string) {
    setBusy(true); setError('')
    try {
      const next = await api<ResearchExecution>(`/api/projects/${encodeURIComponent(workflow.projectId)}/workflows/${encodeURIComponent(workflow.id)}/${action}`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': action === 'decision' ? decisionKey.current : commandKey('research-' + action) },
        body: JSON.stringify(action === 'decision' ? { version: workflow.gate!.version, gateId: workflow.gate!.id, inputDigest: workflow.gate!.inputDigest, decision, instructions } : {}),
      })
      setValue((old) => old && old.version > next.version ? old : next)
    } catch (reason) { setError(message(reason)) } finally { setBusy(false) }
  }
  const labels: Record<string, string> = { researching: 'Researching', waiting: 'Review the research proposal', confirming: 'Saving your decision', approved: 'Preparing coding', coding: 'Coding and checks', awaiting_review: 'Ready for your review', stopping: 'Stopping', stopped: 'Stopped', rejected: 'Proposal rejected', failed: 'Research failed', unresolved: 'Needs reconciliation' }
  return <section className="panel" aria-label={t('Research before coding')}>
    <h2>{t('Research before coding')}</h2>
    <p role="status">{t(labels[workflow.status] ?? workflow.status)}</p>
    {workflow.reason && <p>{t(workflow.reason)}</p>}
    {error && <p role="alert">{error}</p>}
    {workflow.gate && <>
      <p>{workflow.gate.report.summary}</p>
      {workflow.gate.report.no_answer && <p>{t('The available sources cannot answer this question.')}</p>}
      <ul>{workflow.gate.report.citations.map((citation) => {
        const source = workflow.projection?.evidence?.[citation]
        return <li key={citation}>{source?.url.startsWith('https://') ? <a href={source.url} rel="noreferrer" target="_blank">{citation}</a> : citation}</li>
      })}</ul>
      {workflow.status === 'waiting' && <>
        <label>{t('Changes or instructions for coding')}<textarea value={instructions} maxLength={4000} onChange={(event) => setInstructions(event.target.value)} /></label>
        <p>{t('Confirming permits coding within this task’s existing scope. Review and delivery remain separate.')}</p>
        <button disabled={busy || workflow.gate.report.no_answer} onClick={() => void send('decision', 'accept')}>{t('Confirm and code')}</button>
        <button disabled={busy} onClick={() => void send('decision', 'reject')}>{t('Reject proposal')}</button>
      </>}
    </>}
    {workflow.status === 'unresolved' && <button disabled={busy} onClick={() => void send('reconcile')}>{t('Reconcile existing operation')}</button>}
    {!workflow.stopRequested && !['stopped', 'rejected', 'failed', 'awaiting_review'].includes(workflow.status) && <button disabled={busy} onClick={() => void send('stop')}>{t('Stop research and coding')}</button>}
    {workflow.runId && <button onClick={() => onNavigate(`/rooms/${encodeURIComponent(roomId)}/tasks/${encodeURIComponent(taskId)}/runs/${encodeURIComponent(workflow.runId!)}`)}>{t('Open coding result')}</button>}
    <details><summary>{t('Diagnostics')}</summary><pre>{JSON.stringify(workflow, null, 2)}</pre></details>
  </section>
}
