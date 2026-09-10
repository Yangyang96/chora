import { ActionButton } from './ActionButton'
import { useState } from 'react'
import { ProjectSettings } from './ProjectSettings'
import { TaskResources } from './TaskResources'
import { TaskProgress } from './TaskProgress'
import type { FormEvent } from 'react'
import { useI18n } from '../i18n'
import type { AgentExecutionProfile } from '../types'
import type { TaskResourceSelection } from '../taskFirstTypes'
import { AgentExecutionDisclosure, AgentExecutionProfileSelector } from './AgentExecutionProfile'
import type { PiDiscoveryFetch } from './PiDiscovery'

type NewTaskProps = {
  projectId?: string
  roomId?: string
  initialRequirement?: string
  roomName: string
  busy: boolean
  preparationPending?: boolean
  trustedLocalSelectionReady?: boolean
  trustedLocalAcknowledgementPolicy?: string
  piDiscovery?: PiDiscoveryFetch
  onCancel: () => void
  onSubmit: (requirement: string, agentExecutionProfile: AgentExecutionProfile, projectSettingsVersion?: number, resources?: TaskResourceSelection[]) => void
  onAcknowledgeTrustedLocal: () => Promise<boolean>
}

export function NewTask({ projectId, roomId, roomName, busy, preparationPending = false, trustedLocalSelectionReady, trustedLocalAcknowledgementPolicy, piDiscovery, onCancel, onSubmit, onAcknowledgeTrustedLocal, initialRequirement = '' }: NewTaskProps) {
  const { t } = useI18n()
  const [settingsVersion, setSettingsVersion] = useState<number>()
  const [resourceBlocker, setResourceBlocker] = useState('')
  const [resources, setResources] = useState<TaskResourceSelection[]>()
  const [requirement, setRequirement] = useState(initialRequirement)
  const [selectedProfile, setSelectedProfile] = useState<AgentExecutionProfile>()
  const localWorkbench = piDiscovery !== undefined && (piDiscovery.phase !== 'loaded' || piDiscovery.discovery.state !== 'unavailable')
  const agentExecutionProfile = selectedProfile ?? (localWorkbench ? undefined : 'standard')
  const discoveryPending = piDiscovery !== undefined && piDiscovery.phase !== 'loaded'
  const unavailable = discoveryPending || (localWorkbench && (agentExecutionProfile !== 'trusted_local' || piDiscovery?.phase !== 'loaded' || piDiscovery.discovery.state !== 'ready'))
  const [disclosurePending, setDisclosurePending] = useState(false)
  const usesTaskResources = Boolean(projectId && roomId)

  const startDisabledReason = [
    busy ? t(preparationPending ? 'Preparing task worktree…' : 'Wait for the current operation to finish.') : '',
    disclosurePending ? t('Finish the Local Connected acknowledgement first.') : '',
    !requirement.trim() ? t('Enter the task requirement.') : '',
    discoveryPending ? t('Checking Pi availability. Please wait.') : '',
    !agentExecutionProfile || (localWorkbench && agentExecutionProfile !== 'trusted_local') ? t('Choose Local Connected and acknowledge its host access before starting.') : '',
    localWorkbench && agentExecutionProfile === 'trusted_local' && !discoveryPending && piDiscovery?.phase === 'loaded' && piDiscovery.discovery.state !== 'ready' ? t('Open the Pi readiness details and resolve the reported issue.') : '',
    usesTaskResources && (!resources || resources.length === 0) ? resourceBlocker || t('Loading repositories…') : '',
    !usesTaskResources && projectId && settingsVersion === undefined ? t('Wait for Project settings to load; resolve any settings error shown above.') : '',
  ].filter(Boolean).join('\n')

  function submit(event: FormEvent) {
    event.preventDefault()
    if (!requirement.trim()) return
    if (disclosurePending || unavailable || !agentExecutionProfile) return
    if (usesTaskResources && (!resources || resources.length === 0)) return
    if (projectId && !roomId && settingsVersion === undefined) return
    if (projectId && roomId) onSubmit(requirement.trim(), agentExecutionProfile, undefined, resources)
    else if (projectId) onSubmit(requirement.trim(), agentExecutionProfile, settingsVersion)
    else onSubmit(requirement.trim(), agentExecutionProfile)
  }

  return (
    <><TaskProgress preparing={busy} /><form className="panel" onSubmit={submit}>
      <div className="panel-context">
        {t('New task in {room}', { room: roomName })}
      </div>
      {localWorkbench && <p className="section-note">{t('New tasks start from the current branch’s committed HEAD. Uncommitted and untracked changes stay in the original checkout and are not copied. Commit them externally first if the task needs them.')}</p>}
      <label>
        {t('What should Chora build?')}
        <textarea value={requirement} onChange={(event) => setRequirement(event.target.value)} placeholder={t('Describe the requirement…')} autoFocus required />
      </label>
      {projectId && roomId
        ? <TaskResources projectId={projectId} roomId={roomId} busy={busy} onReady={setResources} onBlockedChange={setResourceBlocker} />
        : projectId && <ProjectSettings projectId={projectId} readOnly onReady={setSettingsVersion} />}
      <AgentExecutionProfileSelector
        value={agentExecutionProfile}
        disabled={busy}
        trustedLocalSelectable={trustedLocalSelectionReady}
        piDiscovery={piDiscovery}
        acknowledgedPolicyVersion={trustedLocalAcknowledgementPolicy}
        onChange={setSelectedProfile}
        onAcknowledgeTrustedLocal={onAcknowledgeTrustedLocal}
        onDisclosurePendingChange={setDisclosurePending}
      />
      <p className="agent-profile-summary">
        {agentExecutionProfile ? <>{t('Selected profile:')} <AgentExecutionDisclosure profile={agentExecutionProfile} /></> : t('Choose Local Connected and acknowledge its host access before starting.')}
      </p>
      <div className="form-actions">
        <ActionButton type="button" className="btn-secondary" disabled={(busy && !preparationPending) || disclosurePending} disabledReason={disclosurePending ? 'Finish the Local Connected acknowledgement first.' : 'Wait for the current operation to finish.'} onClick={onCancel}>
          {preparationPending ? t('Cancel preparation') : t('Cancel')}
        </ActionButton>
        <ActionButton type="submit" className="btn-primary" disabled={busy || disclosurePending || unavailable || !agentExecutionProfile || !requirement.trim() || (usesTaskResources ? !resources || resources.length === 0 : !!projectId && settingsVersion === undefined)} disabledReason={startDisabledReason}>
          {busy ? t('Starting…') : t('Start')}
        </ActionButton>
      </div>
    </form></>
  )
}
