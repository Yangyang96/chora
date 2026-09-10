import { ActionButton } from './ActionButton'
import { useId, useRef, useState } from 'react'
import { useI18n } from '../i18n'
import type { AgentExecution, AgentExecutionProfile } from '../types'
import { PiDiscoveryStatus } from './PiDiscovery'
import type { PiDiscoveryFetch } from './PiDiscovery'

export const TRUSTED_LOCAL_DISCLOSURE_POLICY = 'chora.trusted-local-disclosure.v1'
export const TRUSTED_LOCAL_LABEL = 'Trusted Local · No Sandbox'

const profiles: Array<{ id: AgentExecutionProfile; label: string; description: string }> = [
  { id: 'minimal', label: 'Minimal', description: 'Managed Pi with the bounded core tools in a Docker Sandbox.' },
  { id: 'standard', label: 'Standard', description: 'Managed Pi with the version-pinned Standard capability policy in a Docker Sandbox.' },
  { id: 'trusted_local', label: TRUSTED_LOCAL_LABEL, description: 'Supported local Pi with your native configuration and host access.' },
]

export function agentExecutionProfileLabel(profile?: AgentExecutionProfile): string | null {
  if (profile === 'trusted_local') return TRUSTED_LOCAL_LABEL
  if (profile === 'minimal') return 'Minimal'
  if (profile === 'standard') return 'Standard'
  return null
}

export function AgentExecutionDisclosure({ agentExecution, profile, className = '' }: {
  agentExecution?: AgentExecution
  profile?: AgentExecutionProfile
  className?: string
}) {
  const selected = agentExecution?.profile ?? profile
  const label = agentExecutionProfileLabel(selected)
  if (!label) return null
  return <span className={`agent-execution-disclosure ${selected === 'trusted_local' ? 'trusted-local' : ''} ${className}`.trim()}>{label}</span>
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
  // Local Connected is selectable only when Pi discovery resolves ready, or
  // when discovery is `unavailable` (an M1 server whose Trusted Local path is
  // the managed local_pi source, gated only by the disclosure acknowledgement).
  // A selector without discovery management (piDiscovery undefined) keeps the
  // legacy behaviour so minimal/standard and the disclosure flow stay
  // independent of the probe.
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
        <legend id={legendID}>Agent profile</legend>
        {profiles.map((profile) => (
          <label key={profile.id} className={value === profile.id ? 'selected' : ''}>
            <input
              type="radio"
              name={`agent-profile-${legendID}`}
              value={profile.id}
              aria-label={t(profile.label)}
              disabled={profile.id === 'trusted_local' ? (!trustedLocalSelectable || !piReady) : (piDiscovery !== undefined && (piDiscovery.phase !== 'loaded' || piDiscovery.discovery.state !== 'unavailable'))}
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

      {piDiscovery?.phase === 'loaded' && piDiscovery.discovery.state !== 'unavailable' && <p>{t('Docker Sandbox profiles are unavailable in this local workbench. Choose Local Connected explicitly; it runs without a sandbox.')}</p>}

      {piDiscovery?.phase === 'loading' && (
        <p className="pi-discovery-inline">{t('Checking Local Connected…')}</p>
      )}
      {piDiscovery?.phase === 'error' && (
        <p className="pi-discovery-inline">{t('Local Connected check failed')}</p>
      )}
      {piDiscovery?.phase === 'loaded' && piDiscovery.discovery.state !== 'ready' && piDiscovery.discovery.state !== 'unavailable' && (
        <PiDiscoveryStatus discovery={piDiscovery.discovery} className="pi-discovery-inline" />
      )}

      {pendingTrustedLocal && (
        <div className="disclosure-backdrop" role="presentation">
          <section className="trusted-local-disclosure" role="dialog" aria-modal="true" aria-labelledby={`${legendID}-disclosure-title`}>
            <h2 id={`${legendID}-disclosure-title`}>{TRUSTED_LOCAL_LABEL}</h2>
            <p><strong>Disclosure policy:</strong> <code>{TRUSTED_LOCAL_DISCLOSURE_POLICY}</code></p>
            <p>Trusted Local runs the supported local Pi directly as a process on this host.</p>
            <p>It inherits the user’s Pi configuration, Skills, Rules, MCP servers, Hooks, Extensions, login state, shell environment, configuration, and credentials.</p>
            <p>It can have broader filesystem and network access than a managed profile.</p>
            <p>There is no Sandbox. Chora does not claim that unrelated host directories are unreadable, network access is restricted, ambient credentials are absent, or the run is reproducible.</p>
            <div className="form-actions">
              <ActionButton type="button" className="btn-secondary" disabled={acknowledging} disabledReason={'Saving the Local Connected acknowledgement. Please wait.'} onClick={closeDisclosure}>Cancel</ActionButton>
              <ActionButton type="button" className="btn-primary" disabled={acknowledging} disabledReason={'Saving the Local Connected acknowledgement. Please wait.'} onClick={acknowledge}>
                {acknowledging ? 'Acknowledging…' : 'Acknowledge and use Trusted Local'}
              </ActionButton>
            </div>
          </section>
        </div>
      )}
    </>
  )
}
