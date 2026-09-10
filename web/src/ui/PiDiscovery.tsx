import { useEffect, useState } from 'react'
import { api, message } from '../api'
import { useI18n } from '../i18n'
import type { PiDiscoveryState, PiDiscoveryView } from '../types'

// PiDiscoveryFetch is the app-level lifecycle of the one-shot discovery probe.
// `loading` precedes the first resolution, `error` is a failed fetch, and
// `loaded` carries the exact wire view.
export type PiDiscoveryFetch =
  | { phase: 'loading' }
  | { phase: 'error' }
  | { phase: 'loaded'; discovery: PiDiscoveryView }

export function piDiscoveryStateLabel(state: PiDiscoveryState): string {
  switch (state) {
    case 'restart_required': return 'Restart Chora to activate Pi'
    case 'installing': return 'Installing Pi'
    case 'drifted': return 'Pi installation changed'
    case 'ready':
      return 'Pi ready'
    case 'unavailable':
      return 'Local Connected is not available on this server'
    case 'missing':
      return 'Pi not found'
    case 'not_executable':
      return 'Pi is not executable'
    case 'incompatible_version':
      return 'Pi version is incompatible'
    case 'unconfigured':
      return 'Pi is not configured'
  }
}

// PiDiscoveryStatus renders a resolved discovery. For `ready` it names the
// version and ready provider names; for every non-ready state it renders the
// state label and the wire `reason` verbatim (never inventing text).
export function PiDiscoveryStatus({ discovery, className = '' }: {
  discovery: PiDiscoveryView
  className?: string
}) {
  const { t } = useI18n()
  const label = piDiscoveryStateLabel(discovery.state)

  if (discovery.state === 'ready') {
    return (
      <div className={`pi-discovery-status ready ${className}`.trim()}>
        <strong>{t(label)}</strong>
        {discovery.version && <span>{t('Version {version}', { version: discovery.version })}</span>}
        {discovery.readyProviders.length > 0 && (
          <span>{t('Ready providers')}: {discovery.readyProviders.join(', ')}</span>
        )}
      </div>
    )
  }

  return (
    <div className={`pi-discovery-status not-ready ${className}`.trim()}>
      <strong>{t(label)}</strong>
      {discovery.reason && <span>{discovery.reason}</span>}
    </div>
  )
}

export function PiDiscovery() {
  const { t } = useI18n()
  const [phase, setPhase] = useState<'loading' | 'error' | 'loaded'>('loading')
  const [discovery, setDiscovery] = useState<PiDiscoveryView | null>(null)
  const [error, setError] = useState('')
  const [reload, setReload] = useState(0)

  useEffect(() => {
    let cancelled = false
    setPhase('loading')
    setError('')
    api<PiDiscoveryView>('/api/pi/discovery')
      .then((next) => {
        if (cancelled) return
        setDiscovery(next)
        setPhase('loaded')
      })
      .catch((reason) => {
        if (cancelled) return
        setError(message(reason))
        setPhase('error')
      })
    return () => {
      cancelled = true
    }
  }, [reload])

  if (phase === 'loading') {
    return <p className="pi-discovery" role="status">{t('Checking Local Connected…')}</p>
  }
  if (phase === 'error' || !discovery) {
    return (
      <div className="pi-discovery" role="alert">
        <p>{t('Local Connected check failed')}</p>
        {error && <p>{error}</p>}
        <button type="button" className="btn-secondary" onClick={() => setReload((n) => n + 1)}>
          {t('Refresh')}
        </button>
      </div>
    )
  }
  return (
    <div className="pi-discovery">
      <PiDiscoveryStatus discovery={discovery} />
      <button type="button" className="btn-secondary" onClick={() => setReload((n) => n + 1)}>
        {t('Refresh')}
      </button>
    </div>
  )
}
