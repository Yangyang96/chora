import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test, vi, beforeEach } from 'vitest'
import { ModelSelector } from './ModelSelector'
import { api } from '../api'

vi.mock('../api', () => ({ api: vi.fn() }))
const mockedApi = vi.mocked(api)

beforeEach(() => mockedApi.mockReset())

test('renders an explicit empty catalog state instead of loading forever', async () => {
  mockedApi.mockResolvedValue({ agentId: 'pi', runtimeIdentity: 'pi', runtimeVersion: '1', models: [], digest: 'd' })
  render(<ModelSelector onChange={vi.fn()} />)
  await waitFor(() => expect(screen.getByRole('status')).toHaveTextContent('No supported models'))
  expect(screen.queryByRole('combobox')).not.toBeInTheDocument()
})

test('selecting a catalog model emits a complete binding', async () => {
  const catalog = { agentId: 'pi', runtimeIdentity: 'pi', runtimeVersion: '1', models: [{ provider: 'openai', modelId: 'gpt-5' }], digest: 'd' }
  mockedApi.mockResolvedValue(catalog)
  const onChange = vi.fn()
  render(<ModelSelector onChange={onChange} />)
  const select = await screen.findByRole('combobox')
  await userEvent.selectOptions(select, 'openai:gpt-5')
  expect(onChange).toHaveBeenCalledWith(expect.objectContaining({ catalog, provider: 'openai', modelId: 'gpt-5', source: 'coding_agent' }))
})
