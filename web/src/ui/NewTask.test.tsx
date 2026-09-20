import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { beforeEach, expect, test, vi } from 'vitest'
import type { IsolatedLocalState } from '../types'
import { NewTask } from './NewTask'
import { api } from '../api'
import { TRUSTED_LOCAL_DISCLOSURE_POLICY, LOCAL_EXECUTION_LABEL } from './AgentExecutionProfile'

vi.mock('../api', () => ({ api: vi.fn() }))
const mockedApi = vi.mocked(api)
const catalog = { agentId: 'pi', runtimeIdentity: 'pi', runtimeVersion: '1', models: [{ provider: 'openai', modelId: 'gpt-5' }], digest: 'd' }
beforeEach(() => { mockedApi.mockReset(); mockedApi.mockResolvedValue(catalog) })

const isolatedReady = {
 state: 'ready', reason: 'Ready', preparationAvailable: true, imageId: 'sha256:pinned',
 piVersion: '0.85.1', nodeVersion: '22.19.0',
 policy: { network: 'restricted', resources: 'selected repositories', files: 'task worktrees', credentials: 'OpenAI Codex OAuth only' },
} as const

test('Start explains missing task requirements, blocks submission and clears when ready', async () => {
 const onSubmit = vi.fn()
 render(<NewTask roomName="Demo" busy={false} isolatedLocal={isolatedReady} onPrepareIsolatedLocal={vi.fn()} onCancel={vi.fn()} onSubmit={onSubmit} onAcknowledgeTrustedLocal={vi.fn()} />)
 expect(screen.getByRole('button', {name:'Start'})).toBeDisabled()
 await userEvent.click(screen.getByRole('group', {name:'Start'}))
 expect(screen.getByRole('tooltip')).toHaveTextContent('Enter the task requirement.')
 expect(onSubmit).not.toHaveBeenCalled()
 await userEvent.type(screen.getByRole('textbox'), 'Fix the parser')
 expect(screen.getByRole('button', {name:'Start'})).toBeEnabled()
 await userEvent.click(screen.getByRole('button', {name:'Start'}))
 expect(onSubmit).toHaveBeenCalledWith('Fix the parser', 'isolated_local')
})

test.each(['not_prepared', 'preparing', 'failed', 'restart_required'] satisfies IsolatedLocalState[])('%s Isolated execution cannot start a task', async (state) => {
 const onSubmit = vi.fn()
 render(<NewTask roomName="Demo" busy={false} initialRequirement="Fix the parser" isolatedLocal={{ ...isolatedReady, state }} onPrepareIsolatedLocal={vi.fn()} onCancel={vi.fn()} onSubmit={onSubmit} onAcknowledgeTrustedLocal={vi.fn()} />)
 expect(screen.getByRole('radio', { name: 'Isolated execution' })).toBeChecked()
 expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
 expect(onSubmit).not.toHaveBeenCalled()
})

test('switching execution profiles clears the selected model before submission', async () => {
 const onSubmit = vi.fn()
 render(<NewTask roomName="Demo" busy={false} initialRequirement="Fix the parser" isolatedLocal={isolatedReady} trustedLocalAcknowledgementPolicy={TRUSTED_LOCAL_DISCLOSURE_POLICY} piDiscovery={{ phase: 'loaded', discovery: { state: 'unavailable', readyProviders: [], notReadyProviders: [] } }} onCancel={vi.fn()} onSubmit={onSubmit} onAcknowledgeTrustedLocal={vi.fn()} />)
 await userEvent.selectOptions(await screen.findByRole('combobox'), 'openai:gpt-5')
 await userEvent.click(screen.getByRole('button', { name: 'Start' }))
 expect(onSubmit).toHaveBeenLastCalledWith('Fix the parser', 'isolated_local', undefined, undefined, expect.objectContaining({ modelId: 'gpt-5' }))
 await userEvent.click(screen.getByRole('radio', { name: LOCAL_EXECUTION_LABEL }))
 expect(await screen.findByRole('combobox')).toHaveValue('')
 expect(mockedApi).toHaveBeenLastCalledWith('/api/models?agentExecutionProfile=local_connected')
 await userEvent.click(screen.getByRole('button', { name: 'Start' }))
 expect(onSubmit).toHaveBeenLastCalledWith('Fix the parser', 'trusted_local')
})
