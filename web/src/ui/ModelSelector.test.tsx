import { act, render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test, vi, beforeEach } from 'vitest'
import { ModelSelector } from './ModelSelector'
import { api } from '../api'
import type { ModelBinding } from '../types'

vi.mock('../api', () => ({ api: vi.fn() }))
const mockedApi = vi.mocked(api)

beforeEach(() => mockedApi.mockReset())

const catalog: ModelBinding['catalog'] = { agentId: 'pi', runtimeIdentity: 'pi', runtimeVersion: '1', models: [{ provider: 'openai', modelId: 'gpt-5' }], digest: 'd' }

test('renders an explicit empty catalog state instead of loading forever', async () => {
  mockedApi.mockResolvedValue({ agentId: 'pi', runtimeIdentity: 'pi', runtimeVersion: '1', models: [], digest: 'd' })
  render(<ModelSelector onChange={vi.fn()} />)
  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('No supported models'))
  expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
})

test('selecting a catalog model emits a complete binding', async () => {
  mockedApi.mockResolvedValue(catalog)
  const onChange = vi.fn()
  render(<ModelSelector onChange={onChange} />)
  const select = await screen.findByRole('combobox')
  await userEvent.selectOptions(select, 'openai:gpt-5')
  expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ catalog, provider: 'openai', modelId: 'gpt-5', source: 'coding_agent' }))
})

test('changing model selection reuses the catalog and can restore the native default', async () => {
  mockedApi.mockResolvedValue(catalog)
  const onChange = vi.fn()
  const { rerender } = render(<ModelSelector agentExecutionProfile="isolated_local" onChange={onChange} />)
  await userEvent.selectOptions(await screen.findByRole('combobox'), 'openai:gpt-5')
  rerender(<ModelSelector agentExecutionProfile="isolated_local" value={onChange.mock.calls[0][0]} onChange={onChange} />)
  expect(mockedApi).toHaveBeenCalledExactlyOnceWith('/api/models?agentExecutionProfile=isolated_local')
  await userEvent.selectOptions(screen.getByRole('combobox'), '')
  expect(onChange).toHaveBeenLastCalledWith(undefined)
})

test.each(['resolve', 'reject'] as const)('ignores an old profile catalog that later %ss', async (outcome) => {
  let resolveOld!: (value: ModelBinding['catalog']) => void
  let rejectOld!: (error: Error) => void
  mockedApi.mockReturnValueOnce(new Promise((resolve, reject) => { resolveOld = resolve; rejectOld = reject }))
  mockedApi.mockResolvedValueOnce({ ...catalog, models: [{ provider: 'local', modelId: 'native' }] })
  const onChange = vi.fn()
  const { rerender } = render(<ModelSelector agentExecutionProfile="isolated_local" onChange={onChange} />)
  rerender(<ModelSelector agentExecutionProfile="trusted_local" onChange={onChange} />)
  await screen.findByRole('option', { name: 'local · native' })
  expect(mockedApi).toHaveBeenLastCalledWith('/api/models?agentExecutionProfile=local_connected')
  await act(async () => {
    if (outcome === 'resolve') resolveOld(catalog)
    else rejectOld(new Error('Old probe failed'))
  })
  expect(screen.getByRole('option', { name: 'local · native' })).toBeInTheDocument()
  expect(screen.queryByRole('option', { name: 'openai · gpt-5' })).not.toBeInTheDocument()
  expect(screen.queryByText('Supported model catalog is unavailable.')).not.toBeInTheDocument()
})
