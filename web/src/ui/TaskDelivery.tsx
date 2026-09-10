import { ActionButton } from './ActionButton'
import { useEffect, useMemo, useRef, useState } from 'react'
import { DeliveryDraftEditor } from './DeliveryDraftEditor'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'
import { Icon } from './Icons'
import type { DeliveryKind, DeliveryOperation, DeliveryRepository, TaskDeliveryView } from '../taskDeliveryTypes'
import { deliveryStatusLabel } from '../deliveryProgress'

type TaskDeliveryProps = { runId: string; expectedVersion: number; resultDigest: string; onStatusChange?: (view: TaskDeliveryView | undefined, unavailable: boolean) => void }
type Draft = { message: string; remote: string; title: string; body: string }

const emptyDraft: Draft = { message: '', remote: 'origin', title: '', body: '' }

function shortRef(value?: string) {
  if (!value) return '—'
  return value.length > 12 && /^[0-9a-f]+$/i.test(value) ? value.slice(0, 12) : value
}

function nextKind(repository: DeliveryRepository, capabilities: TaskDeliveryView['capabilities']): DeliveryKind | undefined {
  if (repository.status === 'uncommitted') return 'commit'
  if (repository.status === 'committed') return 'push'
  if (repository.status === 'pushed' && capabilities?.hosting && capabilities.createPR) return 'pr'
  if (repository.status === 'pr_open' && capabilities?.hosting && capabilities.merge) return 'merge'
  if (repository.status === 'merged' && capabilities?.cleanup) return 'cleanup'
  return undefined
}

function operationTitle(kind: DeliveryKind) {
  return ({ commit: 'Commit task branch', push: 'Push task branch', pr: 'Create pull request', merge: 'Merge pull request', cleanup: 'Clean up task worktree' } as const)[kind]
}

function OperationPreview({ operation }: { operation: DeliveryOperation }) {
  const { t } = useI18n()
  return <div className="delivery-preview" role="group" aria-label={t('Delivery preview')}>
    <strong>{t(operationTitle(operation.kind))}</strong>
    {operation.message && <p>{t('Commit message')} · <code style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{operation.message}</code></p>}
    {operation.paths?.length ? <><p>{t('Reviewed files')}</p><ul>{operation.paths.map((path) => <li key={path}><code>{path}</code></li>)}</ul></> : null}
    {operation.remote && <p>{t('Remote')} · <code>{operation.remote}</code></p>}
    {operation.url && <p>{t('Remote URL')} · <code>{operation.url}</code></p>}
    {operation.remoteRef && <p>{t('Destination ref')} · <code>{operation.remoteRef}</code></p>}
    {operation.head && <p>{t('Task branch head')} · <code>{shortRef(operation.head)}</code></p>}
    {operation.tree && <p>{t('Tree')} · <code>{shortRef(operation.tree)}</code></p>}
    {operation.remoteHead && <p>{t('Remote task head')} · <code>{shortRef(operation.remoteHead)}</code></p>}
    {operation.targetHead && <p>{t('Remote target head')} · <code>{shortRef(operation.targetHead)}</code></p>}
    {operation.commit && <p>{t('Commit')} · <code>{shortRef(operation.commit)}</code></p>}
    {operation.repository && <p>{t('GitHub repository')} · <code>{operation.repository}</code></p>}
    {operation.headBranch && <p>{t('Head branch')} · <code>{operation.headBranch}</code></p>}
    {operation.baseBranch && <p>{t('Base branch')} · <code>{operation.baseBranch}</code></p>}
    {operation.baseHead && <p>{t('Base branch head')} · <code>{shortRef(operation.baseHead)}</code></p>}
    {operation.title && <p>{t('Title')} · {operation.title}</p>}
    {operation.body && <p style={{ whiteSpace: 'pre-wrap', overflowWrap: 'anywhere' }}>{t('Description')} · {operation.body}</p>}
    {operation.number !== undefined && <p>{t('Pull request number')} · <code>#{operation.number}</code></p>}
    {operation.mergeMethod && <p>{t('Merge method')} · <code>{operation.mergeMethod}</code></p>}
    {operation.worktreePath && <p>{t('Task worktree')} · <code>{operation.worktreePath}</code></p>}
    {operation.prUrl && <p><a href={operation.prUrl} target="_blank" rel="noreferrer">{t('Open pull request')}</a></p>}
    {operation.reason && <p className="warning-banner">{operation.reason}</p>}
  </div>
}

export function TaskDelivery({ runId, expectedVersion, resultDigest, onStatusChange }: TaskDeliveryProps) {
  const { t } = useI18n()
  const [view, setView] = useState<TaskDeliveryView>()
  const [error, setError] = useState('')
  const [repoErrors, setRepoErrors] = useState<Record<string, string>>({})
  const [busyRepos, setBusyRepos] = useState<Record<string, boolean>>({})
  const [previews, setPreviews] = useState<Record<string, DeliveryOperation | undefined>>({})
  const [draftReady, setDraftReady] = useState<Record<string, boolean>>({})
  const [draftRefresh, setDraftRefresh] = useState<Record<string, number>>({})
  const [drafts, setDrafts] = useState<Record<string, Draft>>({})
  const request = useRef<AbortController | undefined>(undefined)
  const generation = useRef(0)
  const version = view?.version ?? expectedVersion

  useEffect(() => { onStatusChange?.(view, Boolean(error)) }, [view, error, onStatusChange])

  useEffect(() => { setView(undefined); setDrafts({}); setDraftReady({}); setDraftRefresh({}) }, [runId, resultDigest])

  useEffect(() => {
    request.current?.abort(); generation.current += 1
    const currentGeneration = generation.current
    const controller = new AbortController()
    request.current = controller
    setError(''); setRepoErrors({}); setBusyRepos({}); setPreviews({})
    void api<TaskDeliveryView>(`/api/v2/runs/${encodeURIComponent(runId)}/delivery`, { signal: controller.signal })
      .then((value) => { if (!controller.signal.aborted && generation.current === currentGeneration) setView(value) })
      .catch((reason) => { if (!controller.signal.aborted && generation.current === currentGeneration) setError(message(reason)) })
    return () => controller.abort()
  }, [runId, resultDigest, expectedVersion])

  const unresolved = useMemo(() => view?.repositories.some((repository) => repository.operations.some((operation) => operation.status === 'writing')), [view])

  function draftKey(repoId: string) { return `${runId}:${resultDigest}:${repoId}` }
  function draft(repoId: string) { return drafts[draftKey(repoId)] ?? emptyDraft }
  function updateDraft(repoId: string, change: Partial<Draft>) {
    const key = draftKey(repoId)
    setDrafts((current) => ({ ...current, [key]: { ...(current[key] ?? emptyDraft), ...change } }))
  }
  function setBusy(repoId: string, value: boolean) { setBusyRepos((current) => ({ ...current, [repoId]: value })) }

  async function preview(repository: DeliveryRepository, kind: DeliveryKind) {
    const values = draft(repository.repoId)
    const currentGeneration = generation.current
    setBusy(repository.repoId, true); setRepoErrors((current) => ({ ...current, [repository.repoId]: '' }))
    try {
      const operation = await api<DeliveryOperation>(`/api/v2/runs/${encodeURIComponent(runId)}/delivery/preview`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey(`delivery-${kind}-preview`) },
        body: JSON.stringify({ repoId: repository.repoId, expectedVersion: version, resultDigest, kind,
          ...(kind === 'commit' ? { message: values.message.trim() } : {}), ...(kind === 'push' ? { remote: values.remote.trim() } : {}),
          ...(kind === 'pr' ? { title: values.title.trim(), body: values.body.trim() } : {}) }),
      })
      if (generation.current === currentGeneration) setPreviews((current) => ({ ...current, [repository.repoId]: operation }))
    } catch (reason) { if (generation.current === currentGeneration) setRepoErrors((current) => ({ ...current, [repository.repoId]: message(reason) })) }
    finally { if (generation.current === currentGeneration) setBusy(repository.repoId, false) }
  }

  async function confirm(operation: DeliveryOperation) {
    const currentGeneration = generation.current
    setBusy(operation.repoId, true); setRepoErrors((current) => ({ ...current, [operation.repoId]: '' }))
    try {
      const outcome = await api<DeliveryOperation>(`/api/v2/runs/${encodeURIComponent(runId)}/delivery/confirm`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey(`delivery-${operation.kind}-confirm`) },
        body: JSON.stringify({ operationId: operation.id, expectedVersion: version, resultDigest }),
      })
      if (generation.current !== currentGeneration) return
      setPreviews((current) => ({ ...current, [operation.repoId]: outcome }))
      await refresh(operation.repoId, currentGeneration)
    } catch (reason) { if (generation.current === currentGeneration) setRepoErrors((current) => ({ ...current, [operation.repoId]: message(reason) })) }
    finally { if (generation.current === currentGeneration) setBusy(operation.repoId, false) }
  }

  async function refresh(repoId: string, expectedGeneration = generation.current) {
    setBusy(repoId, true); setRepoErrors((current) => ({ ...current, [repoId]: '' }))
    try {
      const readOnly = view?.repositories.some((repository) => repository.repoId === repoId && ['legacy', 'no_change', 'awaiting_review'].includes(repository.status))
      const next = readOnly ? await api<TaskDeliveryView>(`/api/v2/runs/${encodeURIComponent(runId)}/delivery`) : await api<TaskDeliveryView>(`/api/v2/runs/${encodeURIComponent(runId)}/delivery/refresh`, {
        method: 'POST', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('delivery-refresh') },
        body: JSON.stringify({ repoId, expectedVersion: version, resultDigest }),
      })
      if (generation.current === expectedGeneration) { setView(next); setPreviews((current) => ({ ...current, [repoId]: undefined })); setDraftRefresh((current) => ({ ...current, [repoId]: (current[repoId] ?? 0) + 1 })) }
    } catch (reason) { if (generation.current === expectedGeneration) setRepoErrors((current) => ({ ...current, [repoId]: message(reason) })) }
    finally { if (generation.current === expectedGeneration) setBusy(repoId, false) }
  }

  if (!view && !error) return <section className="workbench-section task-delivery" aria-label={t('Task branch delivery')}><p role="status">{t('Loading delivery status…')}</p></section>
  if (error) return <section className="workbench-section task-delivery" aria-label={t('Task branch delivery')}><p role="alert" className="error-banner">{error}</p></section>
  if (!view) return null

  return <section className="workbench-section task-delivery" aria-labelledby="task-delivery-title">
    <div className="workbench-section-head"><div><span className="eyebrow">{t('Delivery')}</span><h2 id="task-delivery-title">{t('Task branch delivery')}</h2></div></div>
    <div className="workbench-section-body">
      {unresolved && <p role="alert" className="warning-banner">{t('A delivery operation was interrupted. Refresh its repository before continuing.')}</p>}
      {view.repositories.map((repository) => {
        const kind = nextKind(repository, view.capabilities)
        const operation = previews[repository.repoId]
        const pending = Boolean(busyRepos[repository.repoId])
        const interrupted = repository.operations.some((item) => item.status === 'writing')
        const values = draft(repository.repoId)
        return <article className="panel delivery-repository" aria-label={repository.name} data-repo-id={repository.repoId} key={repository.repoId}>
          <header className="delivery-repository-header">
            <div className="delivery-repository-heading">
              <h3>{repository.name}</h3>
              <div className="delivery-repository-summary"><span className={`delivery-status delivery-stage-${repository.status}`}>{t(deliveryStatusLabel(repository.status))}</span><span>{t('Target branch')} · <code>{repository.targetBranch || '—'}</code></span></div>
            </div>
            <ActionButton type="button" className="icon-button delivery-refresh" aria-label={t('Refresh status')} title={pending ? t('Refreshing…') : t('Refresh status')} aria-busy={pending} disabled={pending} disabledReason={'Updating this repository’s delivery status. Please wait.'} onClick={() => void refresh(repository.repoId)}><Icon name="refresh" /></ActionButton>
          </header>
          {(repository.taskBranch || repository.commit || repository.remote || repository.worktreePath || repository.number !== undefined || repository.mergeMethod) && <details className="delivery-git-details"><summary>{t('Git details')}</summary><div>
            {repository.taskBranch && <p>{t('Task branch')} · <code>{repository.taskBranch}</code></p>}
            {repository.commit && <p>{t('Commit')} · <code>{shortRef(repository.commit)}</code></p>}
            {repository.remote && <p>{t('Remote')} · <code>{repository.remote}</code></p>}
            {repository.number !== undefined && <p>{t('Pull request number')} · <code>#{repository.number}</code></p>}
            {repository.mergeMethod && <p>{t('Merge method')} · <code>{repository.mergeMethod}</code></p>}
            {repository.worktreePath && <p>{t('Task worktree')} · <code>{repository.worktreePath}</code></p>}
          </div></details>}
          {repository.prUrl && <p><a href={repository.prUrl} target="_blank" rel="noreferrer">{t('Open pull request')}</a></p>}
          {repository.reason && <p className="warning-banner">{repository.reason}</p>}
          {(kind === 'commit' || kind === 'pr') && <DeliveryDraftEditor key={`${runId}:${resultDigest}:${repository.repoId}:${kind}`} runId={runId} expectedVersion={version} resultDigest={resultDigest} repoId={repository.repoId} kind={kind} refreshToken={draftRefresh[repository.repoId] ?? 0} value={values} disabled={pending || Boolean(operation)} disabledReason={pending ? 'Updating this repository’s delivery status. Please wait.' : 'Cancel the active preview before editing the draft.'} onChange={(value) => updateDraft(repository.repoId, value)} onReady={(ready) => setDraftReady((current) => current[repository.repoId] === ready ? current : { ...current, [repository.repoId]: ready })} />}
          {kind === 'push' && <label>{t('Remote')}<input value={values.remote} disabled={pending || Boolean(operation)} onChange={(event) => updateDraft(repository.repoId, { remote: event.target.value })} /></label>}

          {operation && <OperationPreview operation={operation} />}
          {repoErrors[repository.repoId] && <p role="alert" className="error-banner">{repoErrors[repository.repoId]}</p>}
          {(kind && !operation || operation?.status === 'preview') && <div className="form-actions delivery-primary-actions">
            {kind && !operation && <ActionButton type="button" className="btn-primary" disabled={pending || interrupted || ((kind === 'commit' || kind === 'pr') && !draftReady[repository.repoId]) || (kind === 'commit' && !values.message.trim()) || (kind === 'push' && !values.remote.trim()) || (kind === 'pr' && (!values.title.trim() || !values.body.trim()))} disabledReason={pending ? 'Preparing this delivery operation. Please wait.' : interrupted ? 'Refresh this repository to reconcile the interrupted operation.' : ((kind === 'commit' || kind === 'pr') && !draftReady[repository.repoId]) ? 'Prepare a draft for the current changes before previewing.' : kind === 'commit' ? 'Enter a commit message.' : kind === 'push' ? 'Enter a remote name.' : 'Enter a pull request title.'} onClick={() => void preview(repository, kind)}>{pending ? t('Preparing preview…') : t('Preview {action}', { action: t(operationTitle(kind)) })}</ActionButton>}
            {operation?.status === 'preview' && <ActionButton type="button" className="btn-accept" disabled={pending} disabledReason={'Updating this repository’s delivery status. Please wait.'} onClick={() => void confirm(operation)}>{pending ? t('Confirming…') : t('Confirm {action}', { action: t(operationTitle(operation.kind)) })}</ActionButton>}
            {operation?.status === 'preview' && <ActionButton type="button" className="btn-secondary" disabled={pending} disabledReason={'Updating this repository’s delivery status. Please wait.'} onClick={() => setPreviews((current) => ({ ...current, [repository.repoId]: undefined }))}>{t('Cancel preview')}</ActionButton>}
          </div>}
          {repository.status === 'pushed' && !view.capabilities?.hosting && <p className="section-note">{view.capabilities?.reason ? `${view.capabilities.reason} ` : ''}{t('GitHub pull requests are unavailable. Install the GitHub CLI, run gh auth login, and refresh this repository.')}</p>}
          {repository.status === 'pr_open' && <p className="section-note">{t('To address pull request feedback, create a new Task from this pull request branch and complete a new Review before delivery continues.')}</p>}
          {repository.status === 'merged' && !view.capabilities?.cleanup && <p className="section-note">{t('Cleanup is unavailable until the merge and a completely clean task worktree are proven.')}</p>}
          {repository.operations.length > 0 && <details className="check-evidence"><summary>{t('Delivery history')}</summary><ul>{repository.operations.map((item) => <li key={item.id}>{t(operationTitle(item.kind))} · {t(item.status)}{item.reason ? ` · ${item.reason}` : ''}</li>)}</ul></details>}
        </article>
      })}
    </div>
  </section>
}
