import { useCallback, useEffect, useRef, useState } from 'react'
import { api, commandKey, message } from '../api'
import type { AppPreviewConfig, AppPreviewView } from '../appPreviewTypes'
import { useI18n } from '../i18n'

type Props = { runId: string; expectedVersion: number }
type Action = 'save' | 'start' | 'stop' | 'cleanup'

const emptyConfig: AppPreviewConfig = { command: '', workingDirectory: '.', port: 3000 }

function safePreviewURL(value: string | undefined) {
  if (!value) return undefined
  try {
    const parsed = new URL(value)
    if (parsed.protocol !== 'http:' || !['127.0.0.1', 'localhost'].includes(parsed.hostname) || !parsed.port || parsed.username || parsed.password) return undefined
    return parsed.toString()
  } catch { return undefined }
}

export function AppPreview({ runId, expectedVersion }: Props) {
  const { locale } = useI18n()
  const local = (english: string, chinese: string) => locale === 'zh-CN' ? chinese : english
  const [expanded, setExpanded] = useState(false)
  const [view, setView] = useState<AppPreviewView>()
  const [repoId, setRepoId] = useState('')
  const [config, setConfig] = useState<AppPreviewConfig>(emptyConfig)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const generation = useRef(0)
  const request = useRef<AbortController | undefined>(undefined)

  const beginRequest = useCallback(() => {
    request.current?.abort()
    const controller = new AbortController()
    request.current = controller
    return controller
  }, [])

  const accept = useCallback((next: AppPreviewView, selected?: string) => {
    setView(next)
    const nextRepo = selected || next.repoId || next.repositories[0]?.repoId || ''
    setRepoId(nextRepo)
    const saved = next.preview.config
    setConfig(saved?.command.trim() ? saved : next.suggestions[0] ?? emptyConfig)
  }, [])

  const load = useCallback(async (selected?: string, quiet = false) => {
    const currentGeneration = generation.current
    const controller = beginRequest()
    if (!quiet) setBusy(true)
    setError('')
    try {
      const query = selected ? `?repoId=${encodeURIComponent(selected)}` : ''
      const next = await api<AppPreviewView>(`/api/v2/runs/${encodeURIComponent(runId)}/app-preview${query}`, { signal: controller.signal })
      if (!controller.signal.aborted && generation.current === currentGeneration) accept(next, selected)
    } catch (reason) {
      if (!controller.signal.aborted && generation.current === currentGeneration) setError(message(reason))
    } finally {
      if (!controller.signal.aborted && generation.current === currentGeneration && !quiet) setBusy(false)
    }
  }, [accept, beginRequest, runId])

  useEffect(() => {
    generation.current += 1
    request.current?.abort()
    setExpanded(false); setView(undefined); setRepoId(''); setConfig(emptyConfig); setBusy(false); setError('')
    return () => { generation.current += 1; request.current?.abort() }
  }, [runId])

  useEffect(() => {
    if (!expanded || busy || !view || !['starting', 'running'].includes(view.preview.state)) return
    const timer = window.setTimeout(() => void load(repoId, true), view.preview.state === 'starting' ? 750 : 1500)
    return () => window.clearTimeout(timer)
  }, [busy, expanded, load, repoId, view])

  async function act(action: Action) {
    if (!repoId || busy) return
    const currentGeneration = generation.current
    const controller = beginRequest()
    setBusy(true); setError('')
    try {
      const next = await api<AppPreviewView>(`/api/v2/runs/${encodeURIComponent(runId)}/app-preview/${action}`, {
        method: 'POST', signal: controller.signal,
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey(`app-preview-${action}`) },
        body: JSON.stringify({ repoId, expectedVersion, ...(['save', 'start'].includes(action) ? { config: { ...config, command: config.command.trim(), workingDirectory: config.workingDirectory.trim() } } : {}) }),
      })
      if (!controller.signal.aborted && generation.current === currentGeneration) accept(next, repoId)
    } catch (reason) {
      if (!controller.signal.aborted && generation.current === currentGeneration) setError(message(reason))
    } finally {
      if (!controller.signal.aborted && generation.current === currentGeneration) setBusy(false)
    }
  }

  const state = view?.preview.state
  const editable = Boolean(view?.available && repoId && !busy && !['starting', 'running', 'recovery_required'].includes(state ?? ''))
  const validConfig = Boolean(config.command.trim() && config.workingDirectory.trim() && Number.isInteger(config.port) && config.port > 0 && config.port <= 65535 && !config.workingDirectory.startsWith('/'))
  const previewURL = safePreviewURL(view?.preview.url)

  return <details className="app-preview" open={expanded} onToggle={(event) => {
    const open = event.currentTarget.open
    setExpanded(open)
    if (open && !view && !busy) void load()
    if (!open) { request.current?.abort(); setView(undefined); setBusy(false) }
  }}>
    <summary>{local('App preview (optional)', '应用预览（可选）')}</summary>
    <div className="workbench-section-body">
      <p className="section-note">{local('Previewing is optional and does not block code review or delivery. Starting and cleanup always require an explicit action.', '应用预览是可选功能，不会阻塞代码评审或交付。启动和清理始终需要显式操作。')}</p>
      {busy && !view && <p role="status">{local('Loading app preview…', '正在加载应用预览…')}</p>}
      {error && <p role="alert" className="error-banner">{error}</p>}
      {view && <>
        <p><strong>{view.profile === 'local_connected' ? local('Local execution · No Sandbox', '本机执行 · 无沙箱') : view.profile === 'isolated_local' ? local('Isolated app preview', '隔离应用预览') : local('App preview', '应用预览')}</strong></p>
        {view.reason && <p className="warning-banner">{view.reason}</p>}
        {view.repositories.length > 1 && <label>{local('Repository', '仓库')}<select value={repoId} disabled={busy} onChange={(event) => { const selected = event.target.value; setRepoId(selected); setView(undefined); void load(selected) }}>{view.repositories.map((repository) => <option key={repository.repoId} value={repository.repoId}>{repository.name}</option>)}</select></label>}
        {view.available && <div className="form-grid">
          {view.suggestions.length > 0 && <label>{local('Suggested configuration', '建议配置')}<select disabled={!editable} value="" onChange={(event) => { const suggestion = view.suggestions[Number(event.target.value)]; if (suggestion) setConfig(suggestion) }}><option value="">{local('Choose a suggestion…', '选择建议配置…')}</option>{view.suggestions.map((suggestion, index) => <option key={`${suggestion.command}:${suggestion.workingDirectory}:${suggestion.port}`} value={index}>{suggestion.command} · {suggestion.workingDirectory} · {suggestion.port}</option>)}</select></label>}
          <label>{local('Command', '命令')}<input value={config.command} disabled={!editable} onChange={(event) => setConfig((current) => ({ ...current, command: event.target.value }))} /></label>
          <label>{local('Working directory (relative)', '工作目录（相对路径）')}<input value={config.workingDirectory} disabled={!editable} onChange={(event) => setConfig((current) => ({ ...current, workingDirectory: event.target.value }))} /></label>
          <label>{local('App port', '应用端口')}<input type="number" min={1} max={65535} value={config.port} disabled={!editable} onChange={(event) => setConfig((current) => ({ ...current, port: Number(event.target.value) }))} /></label>
        </div>}
        {state && <p role="status">{local('Preview state', '预览状态')} · {state}</p>}
        {view.preview.reason && <p role="alert" className="warning-banner">{view.preview.reason}</p>}
        {view.preview.logTruncated && <p className="section-note">{local('Earlier app logs were truncated.', '较早的应用日志已截断。')}</p>}
        {view.preview.logs && <pre aria-label={local('Preview logs', '预览日志')} style={{ whiteSpace: 'pre-wrap', maxHeight: 320, overflow: 'auto' }}>{view.preview.logs}</pre>}
        {previewURL && <p><a href={previewURL} target="_blank" rel="noopener noreferrer">{local('Open app preview', '打开应用预览')}</a></p>}
        {view.preview.url && !previewURL && <p className="warning-banner">{local('The preview URL was withheld because it is not an allowed loopback URL.', '预览 URL 不是允许的安全回环地址，因此未显示。')}</p>}
        {(view.available || ['starting', 'running', 'stopped', 'failed', 'recovery_required'].includes(state ?? '')) && <div className="action-row">
          {view.available && <button type="button" className="btn-secondary" disabled={!editable || !validConfig} onClick={() => void act('save')}>{local('Save configuration', '保存配置')}</button>}
          {view.available && <button type="button" className="btn-primary" disabled={!editable || !validConfig} onClick={() => void act('start')}>{local('Start preview', '启动预览')}</button>}
          {(['starting', 'running', 'recovery_required'].includes(state ?? '')) && <button type="button" className="btn-secondary" disabled={busy} onClick={() => void act('stop')}>{local('Stop preview', '停止预览')}</button>}
          {(['stopped', 'failed', 'recovery_required'].includes(state ?? '')) && <button type="button" className="btn-secondary" disabled={busy} onClick={() => void act('cleanup')}>{local('Clean up preview', '清理预览')}</button>}
        </div>}
      </>}
    </div>
  </details>
}
