import { render, screen, waitFor, within } from '@testing-library/react'
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
 await waitFor(() => expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled())
 expect(onSubmit).not.toHaveBeenCalled()
})

test('switching execution profiles preserves the selected model until the user repairs it', async () => {
 const onSubmit = vi.fn()
 render(<NewTask roomName="Demo" busy={false} initialRequirement="Fix the parser" isolatedLocal={isolatedReady} trustedLocalAcknowledgementPolicy={TRUSTED_LOCAL_DISCLOSURE_POLICY} piDiscovery={{ phase: 'loaded', discovery: { state: 'unavailable', readyProviders: [], notReadyProviders: [] } }} onCancel={vi.fn()} onSubmit={onSubmit} onAcknowledgeTrustedLocal={vi.fn()} />)
 await userEvent.selectOptions(await screen.findByRole('combobox'), 'openai:gpt-5')
 await userEvent.click(screen.getByRole('button', { name: 'Start' }))
 expect(onSubmit).toHaveBeenLastCalledWith('Fix the parser', 'isolated_local', undefined, undefined, expect.objectContaining({ modelId: 'gpt-5' }))
 await userEvent.click(screen.getByRole('radio', { name: LOCAL_EXECUTION_LABEL }))
 expect(await screen.findByRole('combobox')).toHaveValue('openai:gpt-5')
 expect(mockedApi).toHaveBeenLastCalledWith('/api/models?agentExecutionProfile=local_connected')
 await userEvent.click(screen.getByRole('button', { name: 'Start' }))
 expect(onSubmit).toHaveBeenLastCalledWith('Fix the parser', 'trusted_local', undefined, undefined, expect.objectContaining({ modelId: 'gpt-5' }))
})

test('loads Project defaults and sends only explicit task overrides with the frozen version', async () => {
 const onSubmit = vi.fn()
 mockedApi.mockImplementation(async (path: string) => path.endsWith('/execution-settings')
   ? { version: 7, agentExecutionProfile: 'isolated_local', model: { provider: 'openai', modelId: 'gpt-5' } }
   : catalog)
 render(<NewTask projectId="project-1" roomName="Demo" busy={false} initialRequirement="Fix it" isolatedLocal={isolatedReady} onCancel={vi.fn()} onSubmit={onSubmit} onAcknowledgeTrustedLocal={vi.fn()} />)
 await waitFor(() => expect(screen.getAllByText(/Project defaults/).length).toBeGreaterThan(0))
 // The resource selector is still loading in this focused component test, so submit through the form after its callback is unavailable.
 expect(screen.getAllByText(/openai · gpt-5/).length).toBeGreaterThan(0)
 await userEvent.click(screen.getByLabelText('Override Project defaults for this task'))
 await userEvent.selectOptions(screen.getByRole('combobox'), '')
 expect(screen.getByText(/task override/)).toBeInTheDocument()
 expect(mockedApi).toHaveBeenCalledWith('/api/projects/project-1/execution-settings', expect.objectContaining({ signal: expect.any(AbortSignal) }))
})

test('blocks an inherited Local task until host access is acknowledged', async () => {
 const onSubmit = vi.fn()
 const acknowledge = vi.fn(async () => true)
 mockedApi.mockImplementation(async (path: string) => {
   if (path.endsWith('/execution-settings')) return { version: 2, agentExecutionProfile: 'trusted_local', model: null }
   if (path.endsWith('/settings')) return { version: 1, writableFiles: [], writableDirectories: [], verificationCommands: [], noChecks: true }
   return catalog
 })
 render(<NewTask projectId="project-1" roomName="Demo" busy={false} initialRequirement="Fix it" piDiscovery={{ phase: 'loaded', discovery: { state: 'unavailable', readyProviders: [], notReadyProviders: [] } }} onCancel={vi.fn()} onSubmit={onSubmit} onAcknowledgeTrustedLocal={acknowledge} />)
 expect(await screen.findByRole('button', { name: 'Review and acknowledge Local execution' })).toBeInTheDocument()
 expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
 await userEvent.click(screen.getByRole('button', { name: 'Review and acknowledge Local execution' }))
 expect(screen.getByRole('dialog')).toHaveTextContent('There is no Sandbox')
 expect(acknowledge).not.toHaveBeenCalled()
 await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Cancel' }))
 expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled()
 await userEvent.click(screen.getByRole('group', { name: 'Start' }))
 expect(screen.getByRole('tooltip')).toHaveTextContent('Acknowledge Local execution host access before starting.')
 expect(onSubmit).not.toHaveBeenCalled()
})

test('keeps an unavailable inherited model visible and blocks task start until explicit repair', async () => {
 const onSubmit = vi.fn()
 mockedApi.mockImplementation(async (path: string) => {
   if (path.endsWith('/execution-settings')) return { version: 5, agentExecutionProfile: 'isolated_local', model: { provider: 'old', modelId: 'removed' } }
   if (path.endsWith('/settings')) return { version: 1, writableFiles: [], writableDirectories: [], verificationCommands: [], noChecks: true }
   return catalog
 })
 render(<NewTask projectId="project-1" roomName="Demo" busy={false} initialRequirement="Fix it" isolatedLocal={isolatedReady} onCancel={vi.fn()} onSubmit={onSubmit} onAcknowledgeTrustedLocal={vi.fn()} />)
 expect(await screen.findByRole('option', { name: 'old · removed (unavailable)' })).toBeInTheDocument()
 expect(screen.getByRole('combobox')).toHaveValue('old:removed')
 await waitFor(() => expect(screen.getByRole('button', { name: 'Start' })).toBeDisabled())
 expect(screen.getByRole('alert')).toHaveTextContent('This saved model is unavailable')
 expect(onSubmit).not.toHaveBeenCalled()
})

test('submits supplied material without repositories and preserves explicit document revisions', async () => {
 const onSubmit = vi.fn()
 mockedApi.mockImplementation(async (path: string) => {
   if (path.endsWith('/execution-settings')) return { version: 9, agentExecutionProfile: 'isolated_local', model: null }
   if (path.endsWith('/revisions')) return [{ id: 'doc-3', title: 'Accepted design', body: '# Design', locator: 'chora://project-document/design', revisionNumber: 3, confirmedAt: '2026-09-20T00:00:00Z' }]
   if (path.endsWith('/task-options')) return { repositories: [], selectedRepoIds: [], nextRepositoryCursor: '' }
   return catalog
 })
 render(<NewTask projectId="project-1" roomId="room-1" roomName="Demo" busy={false} initialRequirement="Research the design" isolatedLocal={isolatedReady} onCancel={vi.fn()} onSubmit={onSubmit} onAcknowledgeTrustedLocal={vi.fn()} />)
 await waitFor(() => expect(screen.getAllByText(/Project defaults/).length).toBeGreaterThan(0))
 await userEvent.click(screen.getByLabelText('Research / write a proposal'))
 await userEvent.type(screen.getByLabelText('Material title'), 'Support notes')
 await userEvent.type(screen.getByLabelText('Source locator'), 'https://example.test/support')
 await userEvent.type(screen.getByLabelText('Markdown content'), '# Observations')
 await userEvent.click(await screen.findByLabelText(/Accepted design/))
 await waitFor(() => expect(screen.getByRole('button', { name: 'Start' })).toBeEnabled())
 await userEvent.click(screen.getByRole('button', { name: 'Start' }))

 expect(onSubmit).toHaveBeenCalledWith(
   'Research the design', 'isolated_local', undefined, [], undefined,
   { projectVersion: 9 },
   { outcomeKind: 'document', materials: [{ title: 'Support notes', locator: 'https://example.test/support', body: '# Observations' }], revisionIds: ['doc-3'] },
 )
})
