import { render, screen } from '@testing-library/react'
import { describe, expect, test } from 'vitest'
import { LanguageProvider } from '../i18n'
import { ModelProvenance } from './ModelProvenance'

describe('ModelProvenance', () => {
  test('shows every observed identity, explicit missing provider, and historical unknowns', () => {
    render(<ModelProvenance history={[
      { id: 'a1', sequence: 1, state: 'failed', snapshotId: 's1', snapshotDigest: 'd1', modelProvenance: { status: 'unknown', identities: [], reason: 'No model identity was observed for this Attempt.' }, artifacts: [], unknowns: [] },
      { id: 'a2', sequence: 2, state: 'completed', snapshotId: 's2', snapshotDigest: 'd2', modelProvenance: { status: 'observed', identities: [{ provider: 'openai', modelId: 'gpt-a' }, { provider: 'unknown', modelId: 'gpt-b' }] }, artifacts: [], unknowns: [] },
    ]} />)
    expect(screen.getByText('Attempt 1')).toBeInTheDocument()
    expect(screen.getByText(/Unknown · No model identity/)).toBeInTheDocument()
    expect(screen.getByText('gpt-a')).toBeInTheDocument()
    expect(screen.getByText('Unknown provider')).toBeInTheDocument()
    expect(screen.getByText('gpt-b')).toBeInTheDocument()
  })

  test('localizes unknown provenance without turning it into a configured default', () => {
    window.localStorage.setItem('chora.locale', 'zh-CN')
    render(<LanguageProvider><ModelProvenance current={{ status: 'unknown', identities: [], reason: 'No model identity was observed for this Attempt.' }} /></LanguageProvider>)
    expect(screen.getByRole('heading', { name: '模型来源' })).toBeInTheDocument()
    expect(screen.getByText(/未知 · 此尝试未观察到模型身份/)).toBeInTheDocument()
    window.localStorage.clear()
  })
})
