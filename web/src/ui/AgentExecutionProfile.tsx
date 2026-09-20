import { ActionButton } from './ActionButton'
import { useId, useRef, useState } from 'react'
import { useI18n } from '../i18n'
import type { AgentExecution, AgentExecutionProfile } from '../types'
import { PiDiscoveryStatus } from './PiDiscovery'
import type { PiDiscoveryFetch } from './PiDiscovery'

export const TRUSTED_LOCAL_DISCLOSURE_POLICY = 'chora.trusted-local-disclosure.v1'
export const LOCAL_EXECUTION_LABEL = 'Local execution · No Sandbox'

const profiles: Array<{ id: AgentExecutionProfile; label: string; description: string }> = [
  { id: 'isolated_local', label: 'Isolated execution', description: 'Pinned Pi in an isolated environment with bounded files, resources, network, and credentials.' },
  { id: 'trusted_local', label: LOCAL_EXECUTION_LABEL, description: 'Supported local Pi with your native configuration and host access.' },
]

export function agentExecutionProfileLabel(profile?: AgentExecutionProfile): string | null {
  if (profile === 'trusted_local') return LOCAL_EXECUTION_LABEL
  if (profile === 'isolated_local') return 'Isolated execution'
  return profile ?? null
}

export function AgentExecutionDisclosure({ agentExecution, profile, className = '' }: {
  agentExecution?: AgentExecution
  profile?: AgentExecutionProfile
  className?: string
}) {
  const { t } = useI18n()
  const selected = agentExecution?.profile ?? profile
  const label = agentExecutionProfileLabel(selected)
  if (!label) return null
  return <span className={`agent-execution-disclosure ${selected === 'trusted_local' ? 'trusted-local' : ''} ${className}`.trim()}>{t(label)}</span>
}

export function AgentExecutionProfileSelector({ value, disabled, trustedLocalSelectable = true, piDiscovery, acknowledgedPolicyVersion, onChange, onAcknowledgeTrustedLocal, onDisclosurePendingChange }: {
  value?: AgentExecutionProfile
  disabled?: boolean
  trustedLocalSelectable?: boolean
  piDiscovery?: PiDiscoveryFetch
  acknowledgedPolicyVersion?: string
  onChange: (profile: AgentExecutionProfile) => void
  onAcknowledgeTrustedLocal: () => Promise<boolean>
  onDisclosurePendingChange?: (pending: boolean) => void
}) {
  const { t } = useI18n()
  const legendID = useId()
  const previousProfile = useRef(value)
  const [pendingTrustedLocal, setPendingTrustedLocal] = useState(false)
  const [acknowledging, setAcknowledging] = useState(false)
  // Local execution is selectable only when Pi discovery resolves ready, or
  // when discovery is `unavailable` (an M1 server whose local execution path is
  // the managed local_pi source, gated only by the disclosure acknowledgement).
  // A selector without discovery management (piDiscovery undefined) keeps the
  // acknowledgement flow independent of the probe.
  const piReady = piDiscovery === undefined
    || (piDiscovery.phase === 'loaded' && (piDiscovery.discovery.state === 'ready' || piDiscovery.discovery.state === 'unavailable'))

  function closeDisclosure() {
    setPendingTrustedLocal(false)
    onDisclosurePendingChange?.(false)
  }

  function select(profile: AgentExecutionProfile) {
    if (profile !== 'trusted_local' || acknowledgedPolicyVersion === TRUSTED_LOCAL_DISCLOSURE_POLICY) {
      onChange(profile)
      return
    }
    previousProfile.current = value
    setPendingTrustedLocal(true)
    onDisclosurePendingChange?.(true)
  }

  async function acknowledge() {
    setAcknowledging(true)
    const acknowledged = await onAcknowledgeTrustedLocal()
    setAcknowledging(false)
    closeDisclosure()
    if (acknowledged) onChange('trusted_local')
    else if (previousProfile.current) onChange(previousProfile.current)
  }

  return (
    <>
      <fieldset className="agent-profile-selector" aria-labelledby={legendID} disabled={disabled || pendingTrustedLocal}>
        <legend id={legendID}>{t('Execution environment')}</legend>
        {profiles.map((profile) => (
          <label key={profile.id} className={value === profile.id ? 'selected' : ''}>
            <input
              type="radio"
              name={`agent-profile-${legendID}`}
              value={profile.id}
              aria-label={t(profile.label)}
              disabled={profile.id === 'trusted_local' ? (!trustedLocalSelectable || !piReady) : false}
              checked={(pendingTrustedLocal ? 'trusted_local' : value) === profile.id}
              onChange={() => select(profile.id)}
            />
            <span>
              <strong>{t(profile.label)}</strong>
              <small>{t(profile.description)}</small>
            </span>
          </label>
        ))}
      </fieldset>

      {piDiscovery?.phase === 'loading' && (
        <p className="pi-discovery-inline">{t('Checking Local execution…')}</p>
      )}
      {piDiscovery?.phase === 'error' && (
        <p className="pi-discovery-inline">{t('Local execution check failed')}</p>
      )}
      {piDiscovery?.phase === 'loaded' && piDiscovery.discovery.state !== 'ready' && piDiscovery.discovery.state !== 'unavailable' && (
        <PiDiscoveryStatus discovery={piDiscovery.discovery} className="pi-discovery-inline" />
      )}

      {pendingTrustedLocal && (
        <div className="disclosure-backdrop" role="presentation">
          <section className="trusted-local-disclosure" role="dialog" aria-modal="true" aria-labelledby={`${legendID}-disclosure-title`}>
            <h2 id={`${legendID}-disclosure-title`}>{t(LOCAL_EXECUTION_LABEL)}</h2>
            <p><strong>{t('Disclosure policy:')}</strong> <code>{TRUSTED_LOCAL_DISCLOSURE_POLICY}</code></p>
            <p>{t('Local execution runs the supported local Pi directly as a process on this host.')}</p>
            <p>{t('It inherits the user’s Pi configuration, Skills, Rules, MCP servers, Hooks, Extensions, login state, shell environment, configuration, and credentials.')}</p>
            <p>{t('It can have broader filesystem and network access than an isolated environment.')}</p>
            <p>{t('There is no Sandbox. Chora does not claim that unrelated host directories are unreadable, network access is restricted, ambient credentials are absent, or the run is reproducible.')}</p>
            <div className="form-actions">
              <ActionButton type="button" className="btn-secondary" disabled={acknowledging} disabledReason={t('Saving the local execution acknowledgement. Please wait.')} onClick={closeDisclosure}>{t('Cancel')}</ActionButton>
              <ActionButton type="button" className="btn-primary" disabled={acknowledging} disabledReason={t('Saving the local execution acknowledgement. Please wait.')} onClick={acknowledge}>
                {t(acknowledging ? 'Acknowledging…' : 'Acknowledge and use local execution')}
              </ActionButton>
            </div>
          </section>
        </div>
      )}
    </>
  )
}
