import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { expect, test, vi } from 'vitest'
import { LanguageProvider } from '../i18n'
import type { ResourceCheckDerivationView, ResourceResultView } from '../taskFirstTypes'
import type { RunView } from '../types'
import { ResourceApplyStatus, ResourceResult } from './ResourceResult'
import { RunStream } from './RunStream'

function checks(overrides: Partial<ResourceCheckDerivationView> = {}): ResourceCheckDerivationView {
  return { RepositoryID: 'repo_one', Mode: 'auto', Status: 'UNKNOWN', Explanation: 'No applicable checks were discovered', FinalContentVerified: false, Checks: [], ...overrides }
}

function result(): ResourceResultView {
  const changed = (repoId: string, check: ResourceCheckDerivationView) => ({
    repoId, baseCommit: 'a'.repeat(40), baseTree: 'b'.repeat(40), patchDigest: 'c'.repeat(64), patchLocator: `patches/${repoId}.patch`, changedPaths: ['src/shared.ts'], checks: { ...check, RepositoryID: repoId },
  })
  return {
    group: {
      schemaVersion: 'chora.result-group.v2', id: 'result_1', runId: 'run_1', taskId: 'task_1', attemptId: 'attempt_1', agentReportId: 'report_1',
      resourceSnapshotDigest: 'd'.repeat(64), contractDigest: 'e'.repeat(64), contextDigest: 'f'.repeat(64), outcome: 'review_ready', createdAt: '2026-09-08T00:00:00Z',
      repositories: [
        changed('repo_one', checks({ Status: 'PASS', FinalContentVerified: true, Explanation: 'All selected checks passed against proven final repository contents', Checks: [{ CheckID: 'test', Name: 'Unit tests', Version: 1, Argv: ['npm', 'test'], WorkingDirectory: '.', ToolCallID: 'call-1', CommandDigest: '1'.repeat(64), ProviderSucceeded: true, ExitCode: 0, ObservedStatus: 'PASS', Status: 'PASS', Evidence: 'test exited 0', ContentFingerprint: '2'.repeat(64), FinalContentFingerprint: '2'.repeat(64), FinalContentVerified: true, Stale: false }] })),
        changed('repo_two', checks({ Mode: 'named', Explanation: 'Selected checks are missing, ambiguous, stale, or lack final-content proof', Checks: [{ CheckID: 'lint', Name: 'Lint', Version: 2, Argv: ['npm', 'run', 'lint'], WorkingDirectory: 'client app', ToolCallID: 'call-2', CommandDigest: '3'.repeat(64), ProviderSucceeded: true, ExitCode: null, ObservedStatus: 'PASS', Status: 'UNKNOWN', Evidence: 'final repository contents were not proven', ContentFingerprint: '', FinalContentFingerprint: '', FinalContentVerified: false, Stale: true }] })),
        { repoId: 'repo_three', baseCommit: '4'.repeat(40), baseTree: '5'.repeat(40), patchDigest: '6'.repeat(64), patchLocator: '', changedPaths: [], checks: checks({ RepositoryID: 'repo_three', Mode: 'none', Explanation: 'Not run / Unverified: no checks were selected' }) },
        { repoId: 'repo_four', baseCommit: '8'.repeat(40), baseTree: '9'.repeat(40), patchDigest: '0'.repeat(64), patchLocator: '', changedPaths: [], checks: checks({ RepositoryID: 'repo_four', NoApplicableChecks: true, Explanation: 'Not run / Unverified: no applicable checks were discovered' }) },
      ],
    },
    digest: '7'.repeat(64),
    patches: ['repo_one', 'repo_two'].map((repoId) => ({ repoId, files: [{ kind: 'modified', path: 'src/shared.ts', additions: 1, deletions: 1, lines: [{ kind: 'removed', oldLine: 1, text: 'old' }, { kind: 'added', newLine: 1, text: 'new' }] }] })),
  }
}

test('renders patches, bases, and check truth separately for every repository', () => {
  render(<LanguageProvider><ResourceResult result={result()} /></LanguageProvider>)
  const first = screen.getByRole('article', { name: 'repo_one' })
  const second = screen.getByRole('article', { name: 'repo_two' })
  const empty = screen.getByRole('article', { name: 'repo_three' })
  expect(within(first).getByText('Checks passed for final repository contents')).toBeInTheDocument()
  expect(within(first).getByText('aaaaaaaaaaaa')).toHaveAttribute('title', 'a'.repeat(40))
  expect(within(first).getByRole('table')).toHaveTextContent('old')
  expect(within(second).getByText('Checks unverified for final repository contents')).toBeInTheDocument()
  expect(within(second).getByText(/stale because repository contents changed/)).toBeInTheDocument()
  expect(within(second).queryByText('Checks passed for final repository contents')).not.toBeInTheDocument()
  expect(within(empty).getByText('No changes in this repository.')).toBeInTheDocument()
  expect(within(empty).getByText('No checks selected · Unverified')).toBeInTheDocument()
  expect(within(screen.getByRole('article', { name: 'repo_four' })).getByText('No applicable checks discovered · Unverified')).toBeInTheDocument()
  expect(screen.getAllByText('src/shared.ts')).toHaveLength(2)
})

test('uses the grouped result in RunStream while preserving the review callback', async () => {
  const onReview = vi.fn()
  const run: RunView = {
    id: 'run_1', status: 'awaiting_review', version: 3, attempt: 1, adapter: 'pi',
    room: { id: 'room_1', name: 'Task room', description: '' },
    task: { id: 'task_1', title: 'Cross-repository change', goal: 'Update both repositories.' },
    context: [], timeline: [], artifacts: [], unknowns: [], criteria: [], resourceResult: result(),
    controls: { canCancel: false, canRetry: false, canReview: true, canAcceptAndApply: false },
  }
  render(<RunStream run={run} busy={false} onCancel={vi.fn()} onReview={onReview} onApply={vi.fn()} onRetry={vi.fn()} onSwitchProfile={vi.fn(async () => true)} onAcknowledgeTrustedLocal={vi.fn(async () => true)} onResolveDecision={vi.fn()} onRetryVerification={vi.fn()} onChangeRequirement={vi.fn()} />)
  expect(screen.getByRole('heading', { name: 'Repository results' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: 'Diff' })).not.toBeInTheDocument()
  expect(screen.getByText('Review each repository result')).toBeInTheDocument()
  await userEvent.click(screen.getByRole('button', { name: 'Accept' }))
  expect(onReview).toHaveBeenCalledWith('accept', '')
})

test('shows per-repository partial Apply and only continues on an explicit click', async () => {
  const onApply = vi.fn()
  const view = render(<LanguageProvider><ResourceApplyStatus repositories={[{ repoId: 'repo_one', name: 'API service', baseRef: 'HEAD' }, { repoId: 'repo_two', name: 'Worker', baseRef: 'HEAD' }]} apply={{ operationId: 'apply_1', status: 'partial', repositories: [{ repoId: 'repo_one', status: 'applied', sequence: 1 }, { repoId: 'repo_two', status: 'conflict', reason: 'Target changed', sequence: 2 }] }} canApply busy={false} onApply={onApply} /></LanguageProvider>)
  expect(screen.getByText('Some repositories were applied')).toBeInTheDocument()
  expect(screen.getByText('API service').closest('li')).toHaveTextContent('Written to repository')
  expect(screen.getByText('Worker').closest('li')).toHaveTextContent('Target changed')
  expect(screen.queryByText('repo_one')).not.toBeInTheDocument()
  expect(screen.queryByText(/Sequence/)).not.toBeInTheDocument()
  expect(onApply).not.toHaveBeenCalled()
  await userEvent.click(screen.getByRole('button', { name: 'Continue remaining repositories' }))
  expect(onApply).toHaveBeenCalledOnce()

  view.rerender(<LanguageProvider><ResourceApplyStatus repositories={[{ repoId: 'repo_one', name: 'API service', baseRef: 'HEAD' }, { repoId: 'repo_two', name: 'Worker', baseRef: 'HEAD' }]} apply={{ operationId: 'apply_1', status: 'applied', repositories: [{ repoId: 'repo_one', status: 'applied', sequence: 1 }, { repoId: 'repo_two', status: 'applied', sequence: 2 }] }} canApply busy={false} onApply={onApply} /></LanguageProvider>)
  expect(screen.getByText('Applied to all selected repositories')).toBeInTheDocument()
  expect(screen.queryByRole('button')).not.toBeInTheDocument()
})

test('does not infer no applicable checks from an empty observation list', () => {
  const value = result()
  value.group.repositories = [{ ...value.group.repositories[3], checks: checks({ RepositoryID: 'repo_four', NoApplicableChecks: false, Explanation: 'Selection explanation is missing' }) }]
  render(<LanguageProvider><ResourceResult result={value} /></LanguageProvider>)
  expect(screen.getByText('No checks observed · Unverified')).toBeInTheDocument()
  expect(screen.queryByText('No applicable checks discovered · Unverified')).not.toBeInTheDocument()
})

test('shows frozen branch and restores unified and split code review', async () => {
 const view = result()
 view.repositories = [{ repoId: 'repo_one', name: 'API service', baseRef: 'refs/heads/feature/test-mode' }, {repoId: 'repo_two', name: 'Worker', baseRef: 'HEAD'}]
 render(<ResourceResult result={view} />)
 const repo = screen.getByRole('article', {name: 'repo_one'})
 expect(within(repo).getByRole('heading', {name:'API service'})).toBeInTheDocument()
 expect(within(repo).getByText('feature/test-mode')).toBeInTheDocument()
 expect(within(repo).getByText(/Base tree/).closest('details')).not.toHaveAttribute('open')
 expect(within(repo).getByRole('table')).toHaveTextContent('old')
 await userEvent.click(within(repo).getByRole('button', {name:'Split'}))
 expect(within(repo).getByText('Before')).toBeInTheDocument()
 expect(within(repo).getByText('After')).toBeInTheDocument()
 expect(within(repo).getByRole('button', {name:'Split'})).toHaveAttribute('aria-pressed','true')
 expect(within(screen.getByRole('article',{name:'repo_two'})).getByText('Detached HEAD')).toBeInTheDocument()
 const headings = screen.getAllByRole('heading',{name:'Changes'})
 expect(new Set(headings.map(h=>h.id)).size).toBe(headings.length)
})

test('shows explicit Task delivery without Apply while legacy Apply remains direct', async () => {
  const fetchMock = vi.fn(async (input: string | URL | Request, _init?: RequestInit) => new Response(JSON.stringify(String(input).endsWith('/delivery/draft-context') ? { fingerprint: 'source-one', owner: 'local', templates: [], warnings: [] } : String(input).endsWith('/delivery/message') ? { message: 'fix: update frontend\n\nUpdate the reviewed frontend behavior.', provider: 'test', model: 'test', fingerprint: 'source-one' } : { capabilities: { hosting: false, cleanup: true }, repositories: [{ repoId: 'repo_one', name: 'Frontend delivery', targetBranch: 'main', taskBranch: 'chora/task/repo_one', status: 'uncommitted', operations: [] }] }), { status: 200, headers: { 'Content-Type': 'application/json' } }))
  vi.stubGlobal('fetch', fetchMock)
  const resourceResult = result()
  resourceResult.repositories = [{ repoId: 'repo_one', name: 'Frontend', baseRef: 'refs/heads/main', deliveryMode: 'task_branch', taskBranch: 'chora/task/repo_one', worktreePath: '/tmp/chora/task/repo_one' }]
  const run: RunView = {
    id: 'run_1', status: 'accepted', version: 4, attempt: 1, adapter: 'pi',
    room: { id: 'room_1', name: 'Task room', description: '' }, task: { id: 'task_1', title: 'Task', goal: 'Task' },
    context: [], timeline: [], artifacts: [], unknowns: [], criteria: [], resourceResult,
    controls: { canCancel: false, canRetry: false, canReview: false, canApplyPatch: true },
  }
  const props = { run, busy: false, onCancel: vi.fn(), onReview: vi.fn(), onApply: vi.fn(), onRetry: vi.fn(), onSwitchProfile: vi.fn(async () => true), onAcknowledgeTrustedLocal: vi.fn(async () => true), onResolveDecision: vi.fn(), onRetryVerification: vi.fn(), onChangeRequirement: vi.fn() }
  const rendered = render(<RunStream {...props} />)
  const repository = screen.getByRole('article', { name: 'repo_one' })
  expect(within(repository).getByText('chora/task/repo_one')).toBeInTheDocument()
  expect(within(repository).getByText('/tmp/chora/task/repo_one')).toBeInTheDocument()
  expect(screen.getByRole('region', { name: 'Task branch delivery' })).toBeInTheDocument()
  expect(screen.getByRole('heading', { name: 'Review' })).toBeInTheDocument()
  expect(screen.queryByRole('heading', { name: 'Review & Apply' })).not.toBeInTheDocument()
  expect(screen.queryByText('Commit, Push and pull requests are planned for a later stage.')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: 'Apply reviewed changes' })).not.toBeInTheDocument()
  await waitFor(() => expect(screen.getByRole('button', { name: 'Preview Commit task branch' })).toBeInTheDocument())
  expect(fetchMock.mock.calls.filter(([input]) => /\/delivery\/(preview|confirm|refresh)$/.test(String(input)))).toHaveLength(0)
  expect(fetchMock.mock.calls.filter(([input]) => String(input).endsWith('/delivery'))).toHaveLength(1)
  expect(fetchMock.mock.calls[0][1]?.method).toBeUndefined()
  rendered.rerender(<RunStream {...props} run={{ ...run, resourceResult: { ...resourceResult, repositories: [{ repoId: 'repo_one', name: 'Frontend', baseRef: 'refs/heads/main' }] } }} />)
  expect(screen.getByRole('button', { name: 'Apply reviewed changes' })).toBeInTheDocument()
  vi.unstubAllGlobals()
})

test('hides group Apply when a mixed result contains any task-branch repository', () => {
  vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
  const resourceResult = result()
  resourceResult.repositories = [
    { repoId: 'repo_one', name: 'Task branch repo', baseRef: 'refs/heads/main', deliveryMode: 'task_branch', taskBranch: 'chora/task/repo_one', worktreePath: '/tmp/chora/task/repo_one' },
    { repoId: 'repo_two', name: 'Legacy repo', baseRef: 'HEAD' },
  ]
  const run: RunView = {
    id: 'run_1', status: 'accepted', version: 4, attempt: 1, adapter: 'pi',
    room: { id: 'room_1', name: 'Task room', description: '' }, task: { id: 'task_1', title: 'Task', goal: 'Task' },
    context: [], timeline: [], artifacts: [], unknowns: [], criteria: [], resourceResult,
    resourceApply: { operationId: 'apply_1', status: 'partial', repositories: [{ repoId: 'repo_one', status: 'pending', sequence: 1 }, { repoId: 'repo_two', status: 'conflict', reason: 'Target changed', sequence: 2 }] },
    controls: { canCancel: false, canRetry: false, canReview: false, canApplyPatch: true },
  }
  render(<RunStream run={run} busy={false} onCancel={vi.fn()} onReview={vi.fn()} onApply={vi.fn()} onRetry={vi.fn()} onSwitchProfile={vi.fn(async () => true)} onAcknowledgeTrustedLocal={vi.fn(async () => true)} onResolveDecision={vi.fn()} onRetryVerification={vi.fn()} onChangeRequirement={vi.fn()} />)
  expect(screen.getByRole('region', { name: 'Task branch delivery' })).toBeInTheDocument()
  expect(screen.queryByText('Some repositories were applied')).not.toBeInTheDocument()
  expect(screen.queryByRole('button', { name: /Apply|Continue remaining repositories/ })).not.toBeInTheDocument()
  vi.unstubAllGlobals()
})
