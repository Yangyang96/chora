import { useEffect, useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'
import type { RunView } from '../types'
import { ActionButton } from './ActionButton'

export type DocumentRevision = {
  id: string; number: number; kind: string; body: string; bodyDigest: string
  source: { runId: string; attemptId: string; resultId: string; agentReportId: string; eventId: string; resultDigest: string; sourceTextDigest: string }
  editNote: string; actorId: string; createdAt: string; status: string; acceptedContextRevisionId?: string
}
export type ProjectDocumentView = {
  taskId: string; roomId: string; version: number; status: string; revisions: DocumentRevision[]
  reviews: Array<{ id: string; revisionId: string; kind: string; note: string; actorId: string; decidedAt: string }>
}

export function ProjectDocument({ run, onChanged }: { run: RunView; onChanged?: () => void }) {
  const { t } = useI18n()
  const [document, setDocument] = useState<ProjectDocumentView>()
  const [body, setBody] = useState('')
  const [note, setNote] = useState('')
  const [error, setError] = useState('')
  const [busy, setBusy] = useState(false)
  const [refresh, setRefresh] = useState(0)
  const endpoint = `/api/tasks/${encodeURIComponent(run.task.id)}/document`
  const result = run.resourceResult?.group
  const latest = document?.revisions.at(-1)
  const active = ['draft', 'ready', 'running', 'stopping', 'verifying', 'awaiting_verification'].includes(run.status)
  const editable = !active && !run.resultClosed
  const changed = body !== latest?.body
  const canImport = Boolean(result?.attemptId && document && !document.revisions.some(revision => revision.kind !== 'human_edit' && revision.source.attemptId === result.attemptId))

  useEffect(() => {
    const controller = new AbortController()
    setDocument(undefined); setError('')
    void api<ProjectDocumentView>(endpoint, { signal: controller.signal }).then(value => {
      if (!controller.signal.aborted) { setDocument(value); setBody(value.revisions.at(-1)?.body ?? '') }
    }).catch(reason => { if (!controller.signal.aborted) setError(message(reason)) })
    return () => controller.abort()
  }, [endpoint, refresh, result?.attemptId])

  async function mutate(action: 'import' | 'save' | 'accept' | 'reject') {
    if (!document) return
    setBusy(true); setError('')
    try {
      const review = action === 'accept' || action === 'reject'
      const value = await api<ProjectDocumentView>(endpoint + (review ? '/reviews' : ''), {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey(`document-${action}`) },
        body: JSON.stringify({ expectedVersion: document.version, note, ...(review ? { kind: action } : action === 'import' ? { sourceAttemptId: result?.attemptId } : { body }) }),
      })
      setDocument(value); setBody(value.revisions.at(-1)?.body ?? ''); setNote(''); onChanged?.()
    } catch (reason) { setError(message(reason)) }
    finally { setBusy(false) }
  }

  return <section className="workbench-section project-document" aria-label={t('Project document')}>
    <div className="workbench-section-head"><h2>{t('Project document')}</h2></div>
    <div className="workbench-section-body">
      <p>{t('Agent proposals need human review. Acceptance saves a Room revision; it does not write to a repository or start another task.')}</p>
      <details><summary>{t('Supplied sources')}</summary>
        {(run.materials ?? []).map((material, index) => <article key={index}><h3>{material.title}</h3><p>{material.locator}</p><pre style={{ whiteSpace: 'pre-wrap' }}>{material.body}</pre><code>{material.digest}</code></article>)}
        <p>{t('Source labels identify supplied material. Claims and missing evidence still need review.')}</p>
      </details>
      {error && <div role="alert"><p className="error-banner">{error}</p><button type="button" disabled={busy} onClick={() => setRefresh(value => value + 1)}>{t('Reload document')}</button></div>}
      {!document && !error && <p>{t('Loading…')}</p>}
      {canImport && <div className="panel">
        <h3>{t('Agent proposal')}</h3><pre style={{ whiteSpace: 'pre-wrap' }}>{result?.markdown}</pre>
        <ActionButton className="btn-primary" disabled={busy || !editable} disabledReason="Wait for execution to finish." onClick={() => void mutate('import')}>{t('Save agent proposal for review')}</ActionButton>
      </div>}
      {latest && <>
        <p>{t('Revision {number}', { number: latest.number })} · {t(latest.status)} · {t(latest.kind === 'human_edit' ? 'Human revision' : 'Agent proposal')}</p>
        <label>{t('Document Markdown')}<textarea rows={16} value={body} disabled={busy || !editable} onChange={event => setBody(event.target.value)} /></label>
        <label>{t('Feedback or revision note')}<textarea value={note} maxLength={2000} disabled={busy || !editable} onChange={event => setNote(event.target.value)} /></label>
        <div className="form-actions">
          <ActionButton className="btn-secondary" disabled={busy || !editable || !changed || !body.trim() || new TextEncoder().encode(body).length > 65536} disabledReason="Edit the document before saving a new revision (up to 64 KiB)." onClick={() => void mutate('save')}>{t('Save new revision')}</ActionButton>
          <ActionButton className="btn-primary" disabled={busy || !editable || changed || latest.status !== 'pending'} disabledReason="Save changes and review the current pending revision first." onClick={() => void mutate('accept')}>{t('Accept this revision')}</ActionButton>
          <ActionButton className="btn-secondary" disabled={busy || !editable || changed || latest.status !== 'pending' || !note.trim()} disabledReason="Add feedback for the current pending revision." onClick={() => void mutate('reject')}>{t('Request document revision')}</ActionButton>
        </div>
        {latest.acceptedContextRevisionId && <p role="status">{t('Accepted. Explicitly select this revision in a later task’s context to use it.')} <code>{latest.acceptedContextRevisionId}</code></p>}
        <details><summary>{t('Document history and sources')}</summary>
          {document?.revisions.map(revision => <article key={revision.id}><h3>{t('Revision {number}', { number: revision.number })} · {t(revision.status)}</h3><p>{revision.actorId} · {revision.createdAt}</p><p>{revision.editNote}</p><pre style={{ whiteSpace: 'pre-wrap' }}>{revision.body}</pre><p>{t('Body digest')}: <code>{revision.bodyDigest}</code></p><p>{t('Source result')}: <code>{revision.source.resultId}</code> · <code>{revision.source.sourceTextDigest}</code></p>{document.reviews.filter(review => review.revisionId === revision.id).map(review => <p key={review.id}>{t(review.kind)} · {review.actorId} · {review.note}</p>)}</article>)}
        </details>
      </>}
    </div>
  </section>
}
