import { ActionButton } from './ActionButton'
import { useEffect, useState } from 'react'
import { ProjectSettings } from './ProjectSettings'
import { TaskResources } from './TaskResources'
import { TaskProgress } from './TaskProgress'
import type { FormEvent } from 'react'
import { useI18n } from '../i18n'
import type { AgentExecutionProfile, IsolatedLocalView, ModelBinding, ModelIdentity, ProjectExecutionSettings, TaskExecutionSettingsInput } from '../types'
import { api, message } from '../api'
import type { TaskResourceSelection } from '../taskFirstTypes'
import { AgentExecutionDisclosure, AgentExecutionProfileSelector, TRUSTED_LOCAL_DISCLOSURE_POLICY } from './AgentExecutionProfile'
import type { PiDiscoveryFetch } from './PiDiscovery'
import { IsolatedLocal } from './IsolatedLocal'
import { ModelSelector } from './ModelSelector'
import { TaskMaterials } from './TaskMaterials'
import type { TaskContextSelection, TaskMaterialInput } from '../projectDocumentTypes'

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
  isolatedLocal?: IsolatedLocalView
  onPrepareIsolatedLocal?: () => Promise<void> | void
  onCancel: () => void
	onSubmit: (requirement: string, agentExecutionProfile: AgentExecutionProfile, projectSettingsVersion?: number, resources?: TaskResourceSelection[], modelBinding?: ModelBinding, executionSettings?: TaskExecutionSettingsInput, context?: TaskContextSelection) => void
  onAcknowledgeTrustedLocal: () => Promise<boolean>
}

export function NewTask({ projectId, roomId, roomName, busy, preparationPending = false, trustedLocalSelectionReady, trustedLocalAcknowledgementPolicy, piDiscovery, isolatedLocal, onPrepareIsolatedLocal, onCancel, onSubmit, onAcknowledgeTrustedLocal, initialRequirement = '' }: NewTaskProps) {
  const { t } = useI18n()
  const [settingsVersion, setSettingsVersion] = useState<number>()
  const [resourceBlocker, setResourceBlocker] = useState('')
  const [resources, setResources] = useState<TaskResourceSelection[]>()
  const [requirement, setRequirement] = useState(initialRequirement)
  const [selectedProfile, setSelectedProfile] = useState<AgentExecutionProfile>()
  const [modelBinding, setModelBinding] = useState<ModelBinding>()
  const [projectExecution, setProjectExecution] = useState<ProjectExecutionSettings>()
  const [executionError, setExecutionError] = useState('')
  const [overrideExecution, setOverrideExecution] = useState(false)
  const [modelIdentity, setModelIdentity] = useState<ModelIdentity | null>(null)
  const [modelValid, setModelValid] = useState(true)
  const [localAcknowledged, setLocalAcknowledged] = useState(false)
  const [disclosureRequest, setDisclosureRequest] = useState(0)
  const [suppliedOnly, setSuppliedOnly] = useState(false)
  const [materials, setMaterials] = useState<TaskMaterialInput[]>([])
  const [revisionIds, setRevisionIds] = useState<string[]>([])
  const [materialBlocker, setMaterialBlocker] = useState('')
  const localWorkbench = piDiscovery !== undefined
  const agentExecutionProfile = selectedProfile ?? projectExecution?.agentExecutionProfile ?? 'isolated_local'
  const trustedLocalUnavailable = agentExecutionProfile === 'trusted_local' && (piDiscovery?.phase !== 'loaded' || (piDiscovery.discovery.state !== 'ready' && piDiscovery.discovery.state !== 'unavailable'))
  const isolatedLocalUnavailable = agentExecutionProfile === 'isolated_local' && isolatedLocal?.state !== 'ready'
  const unavailable = trustedLocalUnavailable || isolatedLocalUnavailable
  const [disclosurePending, setDisclosurePending] = useState(false)
  const usesTaskResources = Boolean(projectId && roomId)
  const trustedLocalAcknowledged = localAcknowledged || trustedLocalAcknowledgementPolicy === TRUSTED_LOCAL_DISCLOSURE_POLICY
  async function acknowledgeLocal() { const accepted = await onAcknowledgeTrustedLocal(); if (accepted) setLocalAcknowledged(true); return accepted }
  useEffect(() => {
    if (!projectId) return
    const controller = new AbortController(); setProjectExecution(undefined); setExecutionError(''); setOverrideExecution(false); setSelectedProfile(undefined); setModelIdentity(null); setModelValid(true)
    void api<ProjectExecutionSettings>(`/api/projects/${encodeURIComponent(projectId)}/execution-settings`, { signal: controller.signal }).then((next) => { if (!controller.signal.aborted) { setProjectExecution(next); setModelIdentity(next.model); setModelValid(next.model === null) } }).catch((reason) => { if (!controller.signal.aborted) setExecutionError(message(reason)) })
    return () => controller.abort()
  }, [projectId])

  const startDisabledReason = [
    busy ? t(preparationPending ? 'Preparing task worktree…' : 'Wait for the current operation to finish.') : '',
    disclosurePending ? t('Finish the Local execution acknowledgement first.') : '',
    !requirement.trim() ? t('Enter the task requirement.') : '',
    isolatedLocalUnavailable ? t('Prepare Isolated execution and restart Chora if requested before starting.') : '',
    trustedLocalUnavailable ? t('Open the Pi readiness details and resolve the reported issue.') : '',
    usesTaskResources && !suppliedOnly && (!resources || resources.length === 0) ? resourceBlocker || t('Loading repositories…') : '',
    usesTaskResources && suppliedOnly && materialBlocker ? materialBlocker : '',
    projectId && !projectExecution ? executionError || t('Loading Project execution defaults…') : '',
    !modelValid ? t('Choose a supported model or the runtime default.') : '',
    agentExecutionProfile === 'trusted_local' && !trustedLocalAcknowledged ? t('Acknowledge Local execution host access before starting.') : '',
    !usesTaskResources && projectId && settingsVersion === undefined ? t('Wait for Project settings to load; resolve any settings error shown above.') : '',
  ].filter(Boolean).join('\n')

  function submit(event: FormEvent) {
    event.preventDefault()
    if (!requirement.trim()) return
    if (disclosurePending || unavailable || !agentExecutionProfile || !modelValid || (projectId && !projectExecution)) return
    if (agentExecutionProfile === 'trusted_local' && !trustedLocalAcknowledged) return
    if (usesTaskResources && !suppliedOnly && (!resources || resources.length === 0)) return
    if (usesTaskResources && suppliedOnly && (materialBlocker || materials.length === 0)) return
    if (projectId && !roomId && settingsVersion === undefined) return
    if (!projectId && !modelBinding) { onSubmit(requirement.trim(), agentExecutionProfile); return }
    if (projectId && roomId && projectExecution) onSubmit(requirement.trim(), agentExecutionProfile, undefined, suppliedOnly ? [] : resources, undefined, { projectVersion: projectExecution.version, ...(overrideExecution ? { agentExecutionProfile: agentExecutionProfile as 'trusted_local' | 'isolated_local', model: modelIdentity } : {}) }, { ...(suppliedOnly ? { outcomeKind: 'document' as const, materials } : {}), revisionIds })
    else if (projectId) onSubmit(requirement.trim(), agentExecutionProfile, settingsVersion, undefined, modelBinding)
    else onSubmit(requirement.trim(), agentExecutionProfile, undefined, undefined, modelBinding)
  }

  return (
    <><TaskProgress preparing={busy} /><form className="panel" onSubmit={submit}>
      <div className="panel-context">
        {t('New task in {room}', { room: roomName })}
      </div>
      {usesTaskResources && <fieldset className="task-purpose" disabled={busy}>
        <legend>{t('What would you like to do?')}</legend>
        {[
          { document: false, label: 'Develop code', description: 'Work in selected repositories, then review code changes.' },
          { document: true, label: 'Research / write a proposal', description: 'Use supplied material to produce a document for review. No repository changes.' },
        ].map((purpose) => <label className="task-purpose-choice" key={purpose.label}>
          <input type="radio" name="task-purpose" aria-label={t(purpose.label)} aria-describedby={purpose.document ? 'document-purpose-description' : 'code-purpose-description'} checked={suppliedOnly === purpose.document} onChange={() => { setSuppliedOnly(purpose.document); setResources(undefined); setResourceBlocker('') }} />
          <span><strong>{t(purpose.label)}</strong><small id={purpose.document ? 'document-purpose-description' : 'code-purpose-description'}>{t(purpose.description)}</small></span>
        </label>)}
      </fieldset>}
      {localWorkbench && !suppliedOnly && <p className="section-note">{t('New tasks start from the current branch’s committed HEAD. Uncommitted and untracked changes stay in the original checkout and are not copied. Commit them externally first if the task needs them.')}</p>}
      <label>
        {t(suppliedOnly ? 'What should Chora investigate or document?' : 'What should Chora build?')}
        <textarea value={requirement} onChange={(event) => setRequirement(event.target.value)} placeholder={t('Describe the requirement…')} autoFocus required />
      </label>

      {projectId && roomId && <TaskMaterials roomId={roomId} enabled={suppliedOnly} busy={busy} onMaterialsChange={setMaterials} onRevisionIdsChange={setRevisionIds} onBlockedChange={setMaterialBlocker} />}
      {projectId && roomId
        ? !suppliedOnly && <TaskResources projectId={projectId} roomId={roomId} busy={busy} onReady={setResources} onBlockedChange={setResourceBlocker} />
        : projectId && <ProjectSettings projectId={projectId} readOnly onReady={setSettingsVersion} />}
      {projectId && executionError && <p role="alert" className="error-banner">{executionError}</p>}
      {projectId && projectExecution && <div className="section-note">
        <strong>{t('Effective execution')}</strong>: {agentExecutionProfile === 'trusted_local' ? t('Local execution · No Sandbox') : t('Isolated execution')} · {modelIdentity ? `${modelIdentity.provider} · ${modelIdentity.modelId}` : t('runtime default')} · {overrideExecution ? t('task override') : t('Project defaults')}
        <label className="task-choice"><input type="checkbox" checked={overrideExecution} onChange={(event) => { const checked = event.target.checked; setOverrideExecution(checked); setSelectedProfile(undefined); setModelIdentity(projectExecution.model); setModelValid(projectExecution.model === null) }} />{t('Override Project defaults for this task')}</label>
      </div>}
      <AgentExecutionProfileSelector
        value={agentExecutionProfile}
        disabled={busy || (!!projectId && !overrideExecution)}
        trustedLocalSelectable={trustedLocalSelectionReady}
        disclosureRequest={disclosureRequest}
        piDiscovery={piDiscovery}
        acknowledgedPolicyVersion={trustedLocalAcknowledgementPolicy}
        onChange={(profile) => {
          if (profile !== agentExecutionProfile) setModelValid(modelIdentity === null)
          setSelectedProfile(profile)
        }}
        onAcknowledgeTrustedLocal={acknowledgeLocal}
        onDisclosurePendingChange={setDisclosurePending}
      />
      {projectId ? <ModelSelector agentExecutionProfile={agentExecutionProfile} identity={modelIdentity} onIdentityChange={setModelIdentity} onValidityChange={setModelValid} defaultLabel="Use runtime default" disabled={!overrideExecution} /> : <ModelSelector key={agentExecutionProfile} agentExecutionProfile={agentExecutionProfile} value={modelBinding} onChange={setModelBinding} onValidityChange={setModelValid} />}
      {agentExecutionProfile === 'trusted_local' && !trustedLocalAcknowledged && <ActionButton type="button" className="btn-secondary" disabled={busy} disabledReason="Wait for the current operation to finish." onClick={() => setDisclosureRequest((value) => value + 1)}>{t('Review and acknowledge Local execution')}</ActionButton>}
      {agentExecutionProfile === 'isolated_local' && isolatedLocal && <IsolatedLocal value={isolatedLocal} onPrepare={onPrepareIsolatedLocal ?? (() => {})} />}
      <p className="agent-profile-summary">
        {agentExecutionProfile ? <>{t('Selected environment:')} <AgentExecutionDisclosure profile={agentExecutionProfile} /></> : t('Choose Local execution and acknowledge its host access before starting.')}
      </p>
      <div className="form-actions">
        <ActionButton type="button" className="btn-secondary" disabled={(busy && !preparationPending) || disclosurePending} disabledReason={disclosurePending ? 'Finish the Local execution acknowledgement first.' : 'Wait for the current operation to finish.'} onClick={onCancel}>
          {preparationPending ? t('Cancel preparation') : t('Cancel')}
        </ActionButton>
        <ActionButton type="submit" className="btn-primary" disabled={busy || disclosurePending || unavailable || !modelValid || !agentExecutionProfile || !requirement.trim() || (agentExecutionProfile === 'trusted_local' && !trustedLocalAcknowledged) || (usesTaskResources ? (!suppliedOnly && (!resources || resources.length === 0)) || (suppliedOnly && (Boolean(materialBlocker) || materials.length === 0)) || !projectExecution : !!projectId && settingsVersion === undefined)} disabledReason={startDisabledReason}>
          {busy ? t('Starting…') : t('Start')}
        </ActionButton>
      </div>
    </form></>
  )
}
