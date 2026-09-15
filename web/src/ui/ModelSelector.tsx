import { useEffect, useState } from 'react'
import { api } from '../api'
import type { AgentExecutionProfile, ModelBinding } from '../types'

type Catalog = ModelBinding['catalog']

// The catalog belongs to the selected execution environment. The server remains
// authoritative and validates the binding on Task creation.
export function ModelSelector({ value, agentExecutionProfile, onChange, defaultLabel = 'Use coding agent default', disabled = false }: { value?: ModelBinding; agentExecutionProfile?: AgentExecutionProfile; onChange: (binding: ModelBinding | undefined) => void; defaultLabel?: string; disabled?: boolean }) {
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
  if (!catalog) return <p role="status">Loading supported models…</p>
  if (catalog.models.length === 0) return <p role="status">{error || 'No supported models are available.'}</p>
  return <label>Model<select disabled={disabled} value={value ? `${value.provider}:${value.modelId}` : ''} onChange={(event) => {
    if (!event.target.value) { onChange(undefined); return }
    const [provider, ...rest] = event.target.value.split(':')
    const modelId = rest.join(':')
    const model = catalog.models.find((item) => item.provider === provider && item.modelId === modelId)
    if (model) onChange({ catalog, provider: model.provider, modelId: model.modelId, selectedAt: new Date().toISOString(), source: 'coding_agent' })
  }}><option value="">{defaultLabel}</option>{catalog.models.map((model) => <option key={`${model.provider}:${model.modelId}`} value={`${model.provider}:${model.modelId}`}>{model.provider} · {model.modelId}</option>)}</select>{error && <small>{error}</small>}</label>
}
