import { ActionButton } from './ActionButton'
import { useEffect, useState } from 'react'
import type { ReactNode } from 'react'
import { api } from '../api'
import { useI18n } from '../i18n'
import type { ReadinessView } from '../types'
import { ProductInstallationCard } from './ProductInstallationCard'
import { piDiscoveryStateLabel, PiDiscoveryStatus } from './PiDiscovery'
import type { PiDiscoveryFetch } from './PiDiscovery'

type AppShellProps = {
  roomName?: string
  sidebar: ReactNode
  children: ReactNode
  piDiscovery?: PiDiscoveryFetch
  onHome?: () => void
}

const readinessNames: Record<string, string> = {
  source_baseline: 'Source Baseline',
  pi_image: 'Pi Image',
  docker_engine: 'Docker Engine',
  colima: 'Colima',
  oauth: 'OAuth',
  ca_proxy_model: 'CA / Proxy / Model',
  verifier: 'Verifier',
  task_worktrees: 'Task Worktrees',
  owned_residue: 'Owned Residue',
}

export function AppShell({ roomName, sidebar, children, piDiscovery, onHome }: AppShellProps) {
  const { locale, setLocale, t } = useI18n()
  const [readiness, setReadiness] = useState<ReadinessView | null>(null)
  const [refreshing, setRefreshing] = useState(false)
  const showLegacy = !piDiscovery || (piDiscovery.phase === 'loaded' && piDiscovery.discovery.state === 'unavailable')

  useEffect(() => {
    if (!showLegacy) return
    api<ReadinessView>('/api/readiness').then(setReadiness).catch(() => {})
  }, [showLegacy])

  async function refreshReadiness() {
    setRefreshing(true)
    try {
      setReadiness(await api<ReadinessView>('/api/readiness/refresh', { method: 'POST', body: '{}' }))
    } catch {
      // Keep the last safe projection visible; the next refresh may recover.
    } finally {
      setRefreshing(false)
    }
  }

  const blocked = readiness?.items.filter((item) => item.state === 'blocked') ?? []
  const checkFinishedBlocked = blocked.length > 0 && !refreshing
  const readinessLabel = readiness === null ? 'Checking…' : readiness.items.length > 0 && blocked.length === 0 && readiness.items.every((item) => item.state === 'ready') ? 'Ready' : 'Setup needed'
  return (
    <div className="app">
      <header className="app-topbar">
        {onHome ? <button type="button" className="app-logo app-home" aria-label={t('All projects')} onClick={onHome}>Chora<span className="app-logo-dot">.</span></button> : <span className="app-logo">Chora</span>}
        {roomName && <span className="app-room">{roomName}</span>}
        <div className="app-right">
          {!showLegacy && <>
            <span className="local-mode-label">{t('On this computer')}</span>
            <details className="local-pi-status">
              <summary><span className={`connection-dot ${piDiscovery?.phase === 'loaded' && piDiscovery.discovery.state === 'ready' ? 'ready' : ''}`} />{t(piDiscovery?.phase === 'loaded' ? piDiscoveryStateLabel(piDiscovery.discovery.state) ?? 'Local Connected check failed' : piDiscovery?.phase === 'error' ? 'Local Connected check failed' : 'Checking Local Connected…')}</summary>
              <div className="readiness-popover">
                {piDiscovery?.phase === 'loaded' && <PiDiscoveryStatus discovery={piDiscovery.discovery} />}
                <p>{t('Local Connected runs Pi on this computer without a sandbox. You acknowledge this before starting a task.')}</p>
              </div>
            </details>
          </>}
          {showLegacy && <><ProductInstallationCard />
          <details className={`app-readiness ${blocked.length > 0 ? 'blocked' : ''}`}>
            <summary>{t('Execution · {state}', { state: t(readinessLabel) })}</summary>
            <div className="readiness-popover">
              <strong>{t('Execution readiness')}</strong>
              {checkFinishedBlocked && (
                <div className="readiness-guidance" role="status">
                  <strong>{t('Real execution needs attention')}</strong>
                  <p>{t('No installation is running in the background.')}</p>
                  <p>{t('Source-checkout mode only checks real-execution dependencies; it does not automatically install Docker, Colima, images, or OAuth.')}</p>
                  <p>{t('Diagnostic Fake tasks remain available without the real execution environment.')}</p>
                </div>
              )}
              {readiness?.items.map((item) => (
                <div className={`readiness-item readiness-${item.state}`} key={item.key}>
                  <span>{item.state === 'ready' ? '✓' : item.state === 'blocked' ? '!' : '·'}</span>
                  <div>
                    <div className="readiness-item-heading">
                      <strong>{t(readinessNames[item.key] ?? item.key.replaceAll('_', ' '))}</strong>
                      <span className={`readiness-state readiness-state-${checkFinishedBlocked && item.state === 'checking' ? 'not-checked' : item.state}`}>
                        {t(checkFinishedBlocked && item.state === 'checking' ? 'Not checked' : item.state)}
                      </span>
                    </div>
                    {checkFinishedBlocked && item.state === 'checking' ? (
                      <p>{t('This check did not run because readiness stopped at the first blocker.')}</p>
                    ) : (
                      <p><b>{t('Observed')}</b> {t(item.observed)}</p>
                    )}
                    {item.state === 'blocked' && <>
                      <p><b>{t('Required')}</b> {t(item.required)}</p>
                      <p><b>{t('Action')}</b> {t(item.action)}</p>
                    </>}
                  </div>
                </div>
              ))}
              <ActionButton type="button" className="btn-secondary" disabled={refreshing} disabledReason={'Checking service readiness. Please wait.'} onClick={() => void refreshReadiness()}>
                {refreshing ? t('Checking…') : t('Refresh readiness')}
              </ActionButton>
            </div>
          </details>
          </>}
          <div className="app-lang" role="group" aria-label={t('Language')}>
            <button type="button" className={locale === 'en' ? 'on' : ''} onClick={() => setLocale('en')}>
              EN
            </button>
            <button type="button" className={locale === 'zh-CN' ? 'on' : ''} onClick={() => setLocale('zh-CN')}>
              中文
            </button>
          </div>
        </div>
      </header>
      <div className="app-layout">
        {sidebar}
        <main className="app-main">{children}</main>
      </div>
    </div>
  )
}
