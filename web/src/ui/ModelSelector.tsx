import { useEffect, useState } from 'react'
import { api } from '../api'
import type { ModelBinding } from '../types'

type Catalog = ModelBinding['catalog']

// Model selection is deliberately independent from execution-profile controls.
// The server remains authoritative and validates the binding on Task creation.
export function ModelSelector({ value, onChange }: { value?: ModelBinding; onChange: (binding: ModelBinding) => void }) {
  const [catalog, setCatalog] = useState<Catalog | null>(null)
  const [error, setError] = useState('')
  useEffect(() => {
    let active = true
    void api<{ models: Array<{ provider: string; modelId: string }> }>('/api/models').then((result) => {
      if (!active || result.models.length === 0) return
      const first = result.models[0]
      setCatalog({ agentId: 'chora-managed-agent', runtimeIdentity: 'catalog', runtimeVersion: '1', models: result.models, digest: '' })
      if (!value) setError('Model catalog must be bound by the coding agent before task creation.')
      void first
    }).catch(() => { if (active) setError('Supported model catalog is unavailable.') })
    return () => { active = false }
  }, [value])
  if (!catalog) return <p role="status">Loading supported models…</p>
  return <label>Model<select value={value ? `${value.provider}:${value.modelId}` : ''} onChange={(event) => {
    const [provider, ...rest] = event.target.value.split(':')
    const modelId = rest.join(':')
    const model = catalog.models.find((item) => item.provider === provider && item.modelId === modelId)
    if (model && value) onChange({ ...value, provider: model.provider, modelId: model.modelId })
  }}><option value="">Select a supported model</option>{catalog.models.map((model) => <option key={`${model.provider}:${model.modelId}`} value={`${model.provider}:${model.modelId}`}>{model.provider} · {model.modelId}</option>)}</select>{error && <small>{error}</small>}</label>
}
