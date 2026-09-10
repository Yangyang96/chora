import { useCallback, useEffect, useRef, useState } from 'react'
import { api, commandKey, message } from '../api'
import { useI18n } from '../i18n'
import { ActionButton } from './ActionButton'

type CleanupOperation = { id: string; repoId: string; kind: string; status: 'preview' | 'writing' | 'recovery_required' | 'succeeded' | 'failed'; version: number; worktreePath?: string; paths?: string[]; reason?: string }
type ClosureEntry = { repoId: string; name: string; status: 'eligible' | 'retained' | 'closed'; achievement?: string; canCleanup?: boolean; cleanup?: CleanupOperation }
type Closure = { resultId: string; resultDigest: string; previewDigest: string; runVersion: number; closedAt?: string; entries: ClosureEntry[] }

export function ResultClosure({ runId, version, closed, onClosed }: { runId: string; version: number; closed: boolean; onClosed: () => void }) {
  const { locale, t } = useI18n()
  const local = (english: string, chinese: string) => locale === 'zh-CN' ? chinese : english
  const [preview, setPreview] = useState<Closure>()
  const [busy, setBusy] = useState(false)
  const [cleanupBusy, setCleanupBusy] = useState('')
  const [error, setError] = useState('')
  const generation = useRef(0)
  const requests = useRef<Set<AbortController>>(new Set())

  const beginRequest = useCallback(() => { const controller = new AbortController(); requests.current.add(controller); return controller }, [])
  const finishRequest = useCallback((controller: AbortController) => { requests.current.delete(controller) }, [])
  const load = useCallback(async (currentGeneration = generation.current) => {
    const controller = beginRequest()
    try {
      const next = await api<Closure>(`/api/v2/runs/${encodeURIComponent(runId)}/closure`, { signal: controller.signal })
      if (!controller.signal.aborted && generation.current === currentGeneration) setPreview(next)
      return next
    } finally { finishRequest(controller) }
  }, [beginRequest, finishRequest, runId])

  useEffect(() => {
    generation.current += 1
    const currentGeneration = generation.current
    const activeRequests = requests.current
    for (const request of activeRequests) request.abort()
    activeRequests.clear(); setPreview(undefined); setError(''); setBusy(false); setCleanupBusy('')
    if (closed) void load(currentGeneration).catch((reason) => { if (generation.current === currentGeneration) setError(message(reason)) })
    return () => { for (const request of activeRequests) request.abort(); activeRequests.clear() }
  }, [runId, version, closed, load])

  async function act(confirm: boolean) {
    const currentGeneration = generation.current
    const controller = beginRequest()
    setBusy(true); setError('')
    try {
      const next = await api<Closure>(`/api/v2/runs/${encodeURIComponent(runId)}/closure/${confirm ? 'confirm' : 'preview'}`, {
        method: 'POST', signal: controller.signal, headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('close-result') },
        body: JSON.stringify({ expectedVersion: version, resultDigest: confirm ? preview?.resultDigest : undefined, previewDigest: confirm ? preview?.previewDigest : undefined }),
      })
      if (controller.signal.aborted || generation.current !== currentGeneration) return
      setPreview(next)
      if (next.closedAt) onClosed()
    } catch (reason) {
      if (!controller.signal.aborted && generation.current === currentGeneration) { setError(message(reason)); if (confirm) setPreview(undefined) }
    } finally { finishRequest(controller); if (generation.current === currentGeneration) setBusy(false) }
  }

  function updateCleanup(repoId: string, cleanup: CleanupOperation) {
    setPreview((current) => current ? { ...current, entries: current.entries.map((entry) => entry.repoId === repoId ? { ...entry, cleanup } : entry) } : current)
  }
  async function cleanup(entry: ClosureEntry, confirm: boolean) {
    if (!preview || cleanupBusy) return
    const operationId = confirm ? entry.cleanup?.id : undefined
    if (confirm && !operationId) return
    const currentGeneration = generation.current
    const controller = beginRequest()
    setCleanupBusy(entry.repoId); setError('')
    try {
      const operation = await api<CleanupOperation>(`/api/v2/runs/${encodeURIComponent(runId)}/closure/cleanup/${confirm ? 'confirm' : 'preview'}`, {
        method: 'POST', signal: controller.signal, headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey(`closed-result-cleanup-${confirm ? 'confirm' : 'preview'}`) },
        body: JSON.stringify({ expectedVersion: version, resultDigest: preview.resultDigest, repoId: entry.repoId, ...(confirm ? { operationId } : {}) }),
      })
      if (controller.signal.aborted || generation.current !== currentGeneration) return
      updateCleanup(entry.repoId, operation)
      if (confirm) {
        onClosed()
        try { const refreshed = await load(currentGeneration); if (generation.current === currentGeneration) setPreview(refreshed) }
        catch (reason) { if (generation.current === currentGeneration) setError(message(reason)) }
      }
    } catch (reason) {
      if (!controller.signal.aborted && generation.current === currentGeneration) setError(message(reason))
    } finally { finishRequest(controller); if (generation.current === currentGeneration) setCleanupBusy('') }
  }

  const isClosed = closed || Boolean(preview?.closedAt)
  return <section className="review-summary" aria-label={t('Close remaining results')}>
    <strong>{t(isClosed ? 'Remaining results closed' : 'Close unwanted results')}</strong>
    <p>{t('Existing commits and applied changes are kept. Files stay until a separate cleanup.')}</p>
    {preview && <ul>{preview.entries.map((entry) => {
      const operation = entry.cleanup
      const actionable = isClosed && entry.status === 'closed' && Boolean(entry.canCleanup)
      const pending = cleanupBusy === entry.repoId
      return <li key={entry.repoId || 'scalar'}>
        <span>{entry.name} · {operation?.status === 'succeeded' ? local('Cleaned', '已清理') : t(entry.status === 'retained' ? 'Delivered work kept' : entry.status === 'closed' ? 'Closed' : 'Will close')}</span>
        {actionable && operation && operation.status !== 'succeeded' && <div className="delivery-preview" role="group" aria-label={local(`Cleanup preview for ${entry.name}`, `${entry.name} 的清理预览`)}>
          {operation.worktreePath && <p>{local('Task worktree', '任务工作树')} · <code>{operation.worktreePath}</code></p>}
          {operation.paths?.length ? <><p>{local('Files to remove', '将删除的文件')}</p><ul>{operation.paths.map((path) => <li key={path}><code>{path}</code></li>)}</ul></> : null}
          <p>{local('Only this retained task worktree is removed. Original repositories, branches, commits, and result history are kept.', '仅删除此保留的任务工作树。原始仓库、分支、提交和结果历史都会保留。')}</p>
          {operation.reason && <p role="alert">{operation.reason}</p>}
        </div>}
        {actionable && <div className="action-row">
          {!operation || operation.status === 'failed' ? <ActionButton type="button" className="btn-secondary" disabled={Boolean(cleanupBusy)} disabledReason={local('Wait for the current cleanup action to finish.', '请等待当前清理操作完成。')} onClick={() => void cleanup(entry, false)}>{pending ? local('Loading…', '正在读取…') : operation?.status === 'failed' ? local('Preview cleanup again', '重新预览清理') : local('Preview cleanup', '预览清理')}</ActionButton> : null}
          {operation?.status === 'preview' && <ActionButton type="button" className="btn-secondary" disabled={Boolean(cleanupBusy)} disabledReason={local('Wait for the current cleanup action to finish.', '请等待当前清理操作完成。')} onClick={() => void cleanup(entry, true)}>{pending ? local('Cleaning…', '正在清理…') : local('Confirm cleanup', '确认清理')}</ActionButton>}
          {(operation?.status === 'writing' || operation?.status === 'recovery_required') && <ActionButton type="button" className="btn-secondary" disabled={Boolean(cleanupBusy)} disabledReason={local('Wait for the current cleanup observation to finish.', '请等待当前清理状态确认完成。')} onClick={() => void cleanup(entry, true)}>{pending ? local('Observing…', '正在确认…') : local('Observe cleanup', '确认清理状态')}</ActionButton>}
        </div>}
      </li>
    })}</ul>}
    {error && <p role="alert">{error}</p>}
    {!isClosed && <div className="action-row">
      {preview ? <><ActionButton type="button" disabledReason={t('Wait for the current action to finish.')} disabled={busy} onClick={() => setPreview(undefined)}>{t('Cancel')}</ActionButton><ActionButton type="button" disabledReason={t(busy ? 'Wait for the current action to finish.' : 'No undelivered entries remain.')} disabled={busy || !preview.entries.some(entry => entry.status === 'eligible')} onClick={() => void act(true)}>{t('Confirm close')}</ActionButton></>
        : <ActionButton type="button" disabledReason={t('Wait for the current action to finish.')} disabled={busy} onClick={() => void act(false)}>{t(busy ? 'Loading…' : 'Preview close')}</ActionButton>}
    </div>}
    {isClosed && <div className="action-row"><ActionButton type="button" className="btn-secondary" disabled={busy || Boolean(cleanupBusy)} disabledReason={local('Wait for the current action to finish.', '请等待当前操作完成。')} onClick={() => {
      const currentGeneration = generation.current
      setBusy(true); setError('')
      void load(currentGeneration).catch((reason) => { if (generation.current === currentGeneration) setError(message(reason)) }).finally(() => { if (generation.current === currentGeneration) setBusy(false) })
    }}>{busy ? local('Refreshing…', '正在刷新…') : local('Refresh', '刷新')}</ActionButton></div>}
  </section>
}
