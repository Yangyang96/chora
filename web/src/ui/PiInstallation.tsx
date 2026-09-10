import { useEffect, useRef, useState } from 'react'
import { APIError, api, commandKey, message } from '../api'
import { useI18n } from '../i18n'
import { ActionButton } from './ActionButton'
import type { PiDiscoveryView } from '../types'

export type PiInstallationState = {
  code: 'missing' | 'prerequisite_blocked' | 'installing' | 'installed' | 'failed' | 'cancelled' | 'drifted'
  reason?: string
  message?: string
  stateVersion: number
  operationId?: string
  destinationPath: string
  version?: string
  source?: string
  selectionPresent: boolean
  configured: boolean
  configurationAction?: string
  updatedAt: string
}

export type PiInstallationView = {
  available: boolean
  state?: PiInstallationState
  restartRequired: boolean
  active: boolean
}

const fixedVersion = '0.85.1'

function validView(value: unknown): value is PiInstallationView {
  if (!value || typeof value !== 'object') return false
  const view = value as Record<string, unknown>
  if (typeof view.available !== 'boolean' || typeof view.restartRequired !== 'boolean' || typeof view.active !== 'boolean') return false
  if (view.state === undefined) return !view.available
  if (!view.state || typeof view.state !== 'object') return false
  const state = view.state as Record<string, unknown>
  return ['missing', 'prerequisite_blocked', 'installing', 'installed', 'failed', 'cancelled', 'drifted'].includes(String(state.code)) &&
    Number.isSafeInteger(state.stateVersion) && Number(state.stateVersion) >= 0 &&
    typeof state.destinationPath === 'string' && typeof state.selectionPresent === 'boolean' &&
    typeof state.configured === 'boolean' && typeof state.updatedAt === 'string' &&
    (state.operationId === undefined || typeof state.operationId === 'string') &&
    (state.version === undefined || typeof state.version === 'string') &&
    (state.source === undefined || typeof state.source === 'string')
}

export function PiInstallation({ onChanged, existing }: { onChanged?: () => void; existing?: PiDiscoveryView }) {
  const { locale } = useI18n()
  const zh = locale === 'zh-CN'
  const text = (english: string, chinese: string) => zh ? chinese : english
  const [view, setView] = useState<PiInstallationView | null>(null)
  const [loading, setLoading] = useState(true)
  const [installing, setInstalling] = useState(false)
  const [cancelling, setCancelling] = useState(false)
  const [error, setError] = useState('')
  const mounted = useRef(true)
  const activeRequests = useRef<Set<AbortController>>(new Set())

  async function load(signal?: AbortSignal) {
    const next = await api<unknown>('/api/pi/installation', { signal })
    if (!validView(next)) throw new Error('invalid Pi installation status')
    if (mounted.current) setView(next)
    return next
  }

  useEffect(() => {
    mounted.current = true
    const controller = new AbortController()
    const requests = activeRequests.current
    requests.add(controller)
    void load(controller.signal).catch((reason) => {
      if (mounted.current && !controller.signal.aborted) setError(message(reason))
    }).finally(() => {
      requests.delete(controller)
      if (mounted.current) setLoading(false)
    })
    return () => {
      mounted.current = false
      for (const request of requests) request.abort()
      requests.clear()
    }
  }, [])

  async function install() {
    const expectedStateVersion = view?.state?.stateVersion
    if (expectedStateVersion === undefined || installing) return
    setInstalling(true)
    setError('')
    const controller = new AbortController()
    activeRequests.current.add(controller)
    const timer = window.setInterval(() => {
      void load(controller.signal).catch(() => {})
    }, 2000)
    try {
      const next = await api<unknown>('/api/pi/installation', {
        method: 'POST', signal: controller.signal,
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('pi-install') },
        body: JSON.stringify({ expectedStateVersion }),
      })
      if (!validView(next)) throw new Error('invalid Pi installation result')
      if (mounted.current) {
        setView(next)
        onChanged?.()
      }
    } catch (reason) {
      if (mounted.current && !controller.signal.aborted) {
        if (reason instanceof APIError && reason.status === 409) {
          try { await load(controller.signal) } catch { /* retain the last trustworthy state */ }
          if (mounted.current) setError(text('Installation state changed. Review the refreshed status and try again.', '安装状态已变化，请检查刷新后的状态再重试。'))
        } else {
          setError(message(reason))
        }
      }
    } finally {
      window.clearInterval(timer)
      activeRequests.current.delete(controller)
      if (mounted.current) setInstalling(false)
    }
  }

  async function cancel() {
    const operationId = view?.state?.operationId
    if (!operationId || cancelling) return
    setCancelling(true)
    setError('')
    const controller = new AbortController()
    activeRequests.current.add(controller)
    try {
      const next = await api<unknown>('/api/pi/installation/cancel', {
        method: 'POST', signal: controller.signal,
        headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('pi-install-cancel') },
        body: JSON.stringify({ operationId }),
      })
      if (!validView(next)) throw new Error('invalid Pi cancellation result')
      if (mounted.current) {
        setView(next)
        onChanged?.()
      }
    } catch (reason) {
      if (mounted.current && !controller.signal.aborted) setError(message(reason))
    } finally {
      activeRequests.current.delete(controller)
      if (mounted.current) setCancelling(false)
    }
  }

  if (loading) return <p role="status">{text('Loading Pi installation status…', '正在读取 Pi 安装状态…')}</p>
  if (!view) return <div role="alert"><p>{text('Pi installation status could not be loaded.', '无法读取 Pi 安装状态。')}</p>{error && <p>{error}</p>}</div>
  if (!view.available) return <p>{text('Managed Pi installation is not available on this server.', '此服务未提供托管 Pi 安装。')}</p>

  const state = view.state
  if (!state) return <p role="alert">{text('Pi installation status is unavailable.', 'Pi 安装状态不可用。')}</p>
  if (!state.selectionPresent && existing?.state === 'ready') return <section className="pi-installation" aria-label={text('Pi installation', 'Pi 安装')}>
    <h3>{text('Pi installation', 'Pi 安装')}</h3>
    <p role="status">{text('Using your existing compatible Pi installation.', '正在使用已有的兼容 Pi。')}</p>
    <p>{existing.version} · {existing.executablePath}</p>
    <p>{text('Pi owns model configuration. Use /login and /model in Pi; each Attempt shows its observed model separately.', '模型配置由 Pi 管理。在 Pi 中使用 /login 和 /model；每次执行会单独显示实际观察到的模型。')}</p>
  </section>
  const canInstall = !installing && !view.active && state.code !== 'installing'
  const showCancel = state.code === 'installing' && !!state.operationId
  const status = state.code === 'installing'
    ? text('Installing the fixed Pi package…', '正在安装固定版本的 Pi…')
    : state.code === 'installed' && view.active && state.configured
      ? text('Pi is installed, active, and configured.', 'Pi 已安装、已启用并已配置。')
      : state.code === 'installed' && view.restartRequired
        ? text('Pi is installed. Restart Chora before using this installation.', 'Pi 已安装。请重启 Chora 后再使用此安装。')
        : state.code === 'installed'
          ? text('Pi is installed.', 'Pi 已安装。')
          : state.code === 'prerequisite_blocked'
            ? text('Install prerequisites need attention.', '安装前置条件需要处理。')
            : state.code === 'drifted'
              ? text('The managed Pi installation identity has changed.', '托管 Pi 安装身份已变化。')
              : state.code === 'failed'
                ? text('Pi installation failed.', 'Pi 安装失败。')
                : state.code === 'cancelled'
                  ? text('Pi installation was cancelled.', 'Pi 安装已取消。')
                  : text('Pi is not installed.', 'Pi 尚未安装。')

  return <section className="pi-installation" aria-label={text('Pi installation', 'Pi 安装')}>
    <h3>{text('Pi installation', 'Pi 安装')}</h3>
    <p role="status">{status}</p>
    <dl>
      <div><dt>{text('Fixed version', '固定版本')}</dt><dd>{fixedVersion}</dd></div>
      <div><dt>{text('Source', '来源')}</dt><dd>{state.source || 'Chora'}</dd></div>
      <div><dt>{text('Destination', '安装位置')}</dt><dd>{state.destinationPath}</dd></div>
    </dl>
    {!state.configured && <p>{state.selectionPresent
      ? text('Configure providers with the native command: ', '请使用原生命令配置服务商：')
      : text('Installation does not collect credentials. Configure providers afterward with: ', '安装过程不会收集凭据。安装后请使用以下命令配置服务商：')}
      <code>{state.configurationAction || 'pi'}</code>
    </p>}
    {!state.configured && <p>{text('In Pi, use /login to configure a provider and /model to choose a model. Model configuration belongs to Pi; execution mode is separate from reasoning effort.', '进入 Pi 后，使用 /login 配置服务商、/model 选择模型。模型配置由 Pi 管理；执行模式与推理强度是不同设置。')}</p>}
    {view.restartRequired && !view.active && <p>{text('Restart is required. This installation is not active yet.', '需要重启。此安装目前尚未启用。')}</p>}
    {state.message && <p>{state.message}</p>}
    {error && <p role="alert">{error}</p>}
    <div className="action-row">
      <ActionButton type="button" className="btn-secondary" disabled={installing || cancelling} disabledReason={text('Wait for the current operation to finish.', '请等待当前操作完成。')} onClick={() => void load().catch((reason) => setError(message(reason)))}>{text('Refresh', '刷新')}</ActionButton>
      {showCancel && <ActionButton type="button" className="btn-secondary" disabled={cancelling} disabledReason={text('Cancellation is already in progress.', '正在取消安装。')} onClick={() => void cancel()}>{cancelling ? text('Cancelling…', '正在取消…') : text('Cancel installation', '取消安装')}</ActionButton>}
      {!view.active && state.code !== 'installed' && <ActionButton type="button" className="btn-primary" disabled={!canInstall} disabledReason={text('Wait for the current installation operation to finish.', '请等待当前安装操作完成。')} onClick={() => void install()}>{installing ? text('Installing…', '正在安装…') : state.code === 'failed' || state.code === 'cancelled' ? text('Retry installation', '重试安装') : text('Install fixed Pi version', '安装固定 Pi 版本')}</ActionButton>}
    </div>
  </section>
}
