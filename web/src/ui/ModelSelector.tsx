import { useEffect, useState } from 'react'
import { api } from '../api'
import type { AgentExecutionProfile, ModelBinding, ModelIdentity } from '../types'
import { useI18n } from '../i18n'

type Catalog = ModelBinding['catalog']

// The catalog belongs to the selected execution environment. The server remains
// authoritative and validates the binding on Task creation.
export function ModelSelector({ value, identity, agentExecutionProfile, onChange, onIdentityChange, onValidityChange, defaultLabel = 'Use coding agent default', disabled = false }: { value?: ModelBinding; identity?: ModelIdentity | null; agentExecutionProfile?: AgentExecutionProfile; onChange?: (binding: ModelBinding | undefined) => void; onIdentityChange?: (identity: ModelIdentity | null) => void; onValidityChange?: (valid: boolean) => void; defaultLabel?: string; disabled?: boolean }) {
  const { t } = useI18n()
  const [catalog, setCatalog] = useState<Catalog | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let active = true
    setCatalog(null)
    setError('')
    const profile = agentExecutionProfile === 'trusted_local' ? 'local_connected' : agentExecutionProfile
    const path = profile ? `/api/models?agentExecutionProfile=${encodeURIComponent(profile)}` : '/api/models'
    void api<Catalog>(path).then((result) => {
      if (!active) return
      setCatalog(result)
      setError(result.models.length === 0
        ? 'No supported models are available from the bound coding agent.'
        : '')
    }).catch(() => {
      if (active) {
        setCatalog({ agentId: '', runtimeIdentity: '', runtimeVersion: '', models: [], digest: '' })
        setError('Supported model catalog is unavailable.')
      }
    })
    return () => { active = false }
  }, [agentExecutionProfile])
  const selected = identity === undefined ? (value ? { provider: value.provider, modelId: value.modelId } : null) : identity
  const selectedKey = selected ? `${selected.provider}:${selected.modelId}` : ''
  const available = !selected || !!catalog?.models.some((item) => item.provider === selected.provider && item.modelId === selected.modelId)
  useEffect(() => { onValidityChange?.(catalog ? available : !selected) }, [available, catalog, onValidityChange, selected])
  if (!catalog) return <p role="status">{t('Loading supported models…')}</p>
  if (catalog.models.length === 0 && !selected) return <p role="status">{t(error || 'No supported models are available.')}</p>
  return <label>{t('Model')}<select disabled={disabled} aria-invalid={!available} value={selectedKey} onChange={(event) => {
    if (!event.target.value) { onChange?.(undefined); onIdentityChange?.(null); return }
    const [provider, ...rest] = event.target.value.split(':')
    const modelId = rest.join(':')
    const model = catalog.models.find((item) => item.provider === provider && item.modelId === modelId)
    if (model) { onChange?.({ catalog, provider: model.provider, modelId: model.modelId, selectedAt: new Date().toISOString(), source: 'coding_agent' }); onIdentityChange?.(model) }
  }}><option value="">{t(defaultLabel)}</option>{!available && selected && <option value={selectedKey}>{selected.provider} · {selected.modelId} ({t('unavailable')})</option>}{catalog.models.map((model) => <option key={`${model.provider}:${model.modelId}`} value={`${model.provider}:${model.modelId}`}>{model.provider} · {model.modelId}</option>)}</select>{!available ? <small role="alert">{t('This saved model is unavailable for the selected environment. Choose a supported model or the runtime default.')}</small> : error && <small>{t(error)}</small>}</label>
}
