import { useEffect, useRef, useState } from 'react'
import { api, commandKey, message } from '../api'
import type { ModelIdentity, ProjectExecutionSettings } from '../types'
import { ActionButton } from './ActionButton'
import { AgentExecutionProfileSelector } from './AgentExecutionProfile'
import { ModelSelector } from './ModelSelector'
import { useI18n } from '../i18n'

export function ProjectExecutionSettingsPanel({ projectId, editable }: { projectId: string; editable: boolean }) {
  const { t } = useI18n()
  const currentProject = useRef(projectId); currentProject.current = projectId
  const [value, setValue] = useState<ProjectExecutionSettings>()
  const [open, setOpen] = useState(false)
  const [profile, setProfile] = useState<'trusted_local' | 'isolated_local'>('isolated_local')
  const [model, setModel] = useState<ModelIdentity | null>(null)
  const [validModel, setValidModel] = useState(true)
  const [busy, setBusy] = useState(false)
  const [error, setError] = useState('')
  const endpoint = `/api/projects/${encodeURIComponent(projectId)}/execution-settings`
  useEffect(() => {
    if (!open) return
    const requested = projectId; const controller = new AbortController()
    setValue(undefined); setError('')
    void api<ProjectExecutionSettings>(endpoint, { signal: controller.signal }).then((next) => {
      if (controller.signal.aborted || currentProject.current !== requested) return
      setValue(next); setProfile(next.agentExecutionProfile); setModel(next.model)
    }).catch((reason) => { if (currentProject.current === requested && !controller.signal.aborted) setError(message(reason)) })
    return () => controller.abort()
  }, [endpoint, open, projectId])
  async function save() {
    if (!value || !validModel) return
    const requested = projectId; setBusy(true); setError('')
    try {
      const next = await api<ProjectExecutionSettings>(endpoint, { method: 'PUT', headers: { 'Content-Type': 'application/json', 'Idempotency-Key': commandKey('project-execution-settings') }, body: JSON.stringify({ version: value.version, agentExecutionProfile: profile, model }) })
      if (currentProject.current === requested) { setValue(next); setProfile(next.agentExecutionProfile); setModel(next.model) }
    } catch (reason) { if (currentProject.current === requested) setError(message(reason)) } finally { if (currentProject.current === requested) setBusy(false) }
  }
  return <details className="panel project-settings" open={open} onToggle={(event) => setOpen(event.currentTarget.open)}>
    <summary><strong>{t('Default execution settings')}</strong></summary>
    <p className="section-note">{t('New tasks inherit these defaults and freeze the effective environment and model when they are created. Saving Local execution here does not acknowledge host access.')}</p>
    {error && <p role="alert" className="error-banner">{error}</p>}
    {!value && !error && <p role="status">{t('Loading…')}</p>}
    {value && <>
      <AgentExecutionProfileSelector value={profile} disabled={!editable || busy} trustedLocalSelectable requireTrustedLocalAcknowledgement={false} onChange={(next) => setProfile(next as 'trusted_local' | 'isolated_local')} onAcknowledgeTrustedLocal={async () => false} />
      <ModelSelector agentExecutionProfile={profile} identity={model} onIdentityChange={setModel} onValidityChange={setValidModel} defaultLabel="Use runtime default" disabled={!editable || busy} />
      {!editable && <p className="entry-help">{t('Restore the Project before editing execution defaults.')}</p>}
      <ActionButton type="button" className="btn-primary" disabled={!editable || busy || !validModel} disabledReason={!validModel ? 'Choose a supported model or the runtime default.' : 'Saving changes. Please wait.'} onClick={() => void save()}>{busy ? t('Saving…') : t('Save execution defaults')}</ActionButton>
    </>}
  </details>
}
