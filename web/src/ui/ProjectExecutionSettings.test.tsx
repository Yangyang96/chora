import { render, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, test, vi } from 'vitest'
import { api } from '../api'
import { ProjectExecutionSettingsPanel } from './ProjectExecutionSettings'

vi.mock('../api', async () => {
  const actual = await vi.importActual<typeof import('../api')>('../api')
  return { ...actual, api: vi.fn(), commandKey: () => 'key' }
})
const mockedApi = vi.mocked(api)
const catalog = { agentId: 'pi', runtimeIdentity: 'pi', runtimeVersion: '1', models: [{ provider: 'openai', modelId: 'gpt-5' }], digest: 'd' }

beforeEach(() => mockedApi.mockReset())

test('loads lazily and saves Project defaults with CAS and idempotency', async () => {
  mockedApi.mockImplementation(async (path: string, init?: RequestInit) => {
    if (path === '/api/models?agentExecutionProfile=isolated_local') return catalog
    if (init?.method === 'PUT') return { version: 4, agentExecutionProfile: 'isolated_local', model: null }
    return { version: 3, agentExecutionProfile: 'isolated_local', model: { provider: 'openai', modelId: 'gpt-5' } }
  })
  render(<ProjectExecutionSettingsPanel projectId="project-1" editable />)
  expect(mockedApi).not.toHaveBeenCalled()
  await userEvent.click(screen.getByText('Default execution settings'))
  await userEvent.selectOptions(await screen.findByRole('combobox'), '')
  await userEvent.click(screen.getByRole('button', { name: 'Save execution defaults' }))
  await waitFor(() => expect(mockedApi).toHaveBeenCalledWith('/api/projects/project-1/execution-settings', expect.objectContaining({
    method: 'PUT', headers: expect.objectContaining({ 'Idempotency-Key': 'key' }), body: JSON.stringify({ version: 3, agentExecutionProfile: 'isolated_local', model: null }),
  })))
})

test('ignores a stale Project fetch after the Project changes', async () => {
  let resolveOld!: (value: unknown) => void
  mockedApi.mockImplementation((path?: string) => {
    if (path?.includes('project-a')) return new Promise((resolve) => { resolveOld = resolve })
    if (path?.includes('project-b')) return Promise.resolve({ version: 2, agentExecutionProfile: 'isolated_local', model: null })
    return Promise.resolve(catalog)
  })
  const view = render(<ProjectExecutionSettingsPanel projectId="project-a" editable />)
  await userEvent.click(screen.getByText('Default execution settings'))
  view.rerender(<ProjectExecutionSettingsPanel projectId="project-b" editable />)
  resolveOld({ version: 99, agentExecutionProfile: 'trusted_local', model: null })
  await waitFor(() => expect(screen.getByRole('radio', { name: 'Isolated execution' })).toBeChecked())
})
