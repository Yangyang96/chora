import { render, screen, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test, vi } from 'vitest'
import type { RunView } from '../types'
import { LanguageProvider } from '../i18n'
import { RunStream } from './RunStream'

type RunStreamHandlers = {
  onCancel?: () => void
  onReview?: (kind: 'accept' | 'reject', note: string, rejectionClass?: 'implementation_gap' | 'planning_gap' | 'contract_change_required') => void
  onApply?: () => void
  onRetry?: (instructions: string) => void
  onSwitchProfile?: (profile: 'minimal' | 'standard' | 'trusted_local', reason: string) => Promise<boolean>
  onChangeRequirement?: () => void
}

function makeRun(overrides: Partial<RunView> = {}): RunView {
  return {
    id: 'run-1',
    status: 'awaiting_review',
    version: 6,
    attempt: 1,
    adapter: 'pi',
    room: { id: 'room-1', name: 'Lane Room', description: '' },
    task: { id: 'task-1', title: 'Add a sort button', goal: 'Add a sort button to the table.' },
    context: [],
    timeline: [],
    artifacts: [],
    unknowns: [],
    criteria: [],
    controls: { canCancel: false, canRetry: false, canReview: true, canAcceptAndApply: false },
    ...overrides,
  }
}

function makePlanRun(): RunView {
  return makeRun({
    plan: {
      revisionId: 'rev-1',
      activatedBy: 'local',
      content: {
        technical_steps: ['Read the table component', 'Add the sort handler', 'Add a focused test'],
        decisions: ['Sort ascending by default'],
        risks: ['Existing tests may assume stable order'],
        unknowns: ['Whether the table supports server-side sorting'],
      },
    },
  })
}

function renderWorkbench(run: RunView, handlers: RunStreamHandlers = {}) {
  const props = {
    run,
    busy: false,
    onCancel: handlers.onCancel ?? vi.fn(),
    onReview: handlers.onReview ?? vi.fn(),
    onApply: handlers.onApply ?? vi.fn(),
    onRetry: handlers.onRetry ?? vi.fn(),
    onSwitchProfile: handlers.onSwitchProfile ?? vi.fn(async () => true),
    onAcknowledgeTrustedLocal: vi.fn(async () => true),
    onResolveDecision: vi.fn(),
    onRetryVerification: vi.fn(),
    onChangeRequirement: handlers.onChangeRequirement ?? vi.fn(),
  }
  return render(<RunStream {...props} />)
}

function verifiedAttempt() {
  return {
    id: 'va-1', sequence: 1, state: 'completed', evidenceComplete: true, cleanupProven: true,
    createdAt: '2026-09-03T00:00:00Z', updatedAt: '2026-09-03T00:00:01Z',
    checks: [{ id: 'check-1', criterionId: 'criterion-1', status: 'passed', trust: 'verified', evidenceIds: ['ev-1'] }],
    commands: [],
  }
}

describe('RunStream workbench sections', () => {
  test('translates the generated Local Connected verification disposition', () => {
    window.localStorage.setItem('chora.locale', 'zh-CN')
    const run = makeRun({
      verificationDisposition: { state: 'not_applicable', reason: 'Local Connected uses Agent-reported checks; no independent Verifier ran.' },
      criteria: [{ id: 'criterion-1', title: 'Requested behavior is implemented', status: 'passed', evidence: '' }],
    })
    const props = {
      run, busy: false, onCancel: vi.fn(), onReview: vi.fn(), onApply: vi.fn(), onRetry: vi.fn(),
      onSwitchProfile: vi.fn(async () => true), onAcknowledgeTrustedLocal: vi.fn(async () => true),
      onResolveDecision: vi.fn(), onRetryVerification: vi.fn(), onChangeRequirement: vi.fn(),
    }
    render(<LanguageProvider><RunStream {...props} /></LanguageProvider>)
    expect(screen.getAllByText('本地连接使用 Agent 自报检查；未运行独立验证器。').length).toBeGreaterThan(0)
    expect(screen.getByText('已实现要求的行为')).toBeInTheDocument()
    window.localStorage.clear()
  })

  test('renders the labeled Workbench sections in order', () => {
    const run = makeRun({
      plan: {
        revisionId: 'rev-1',
        activatedBy: 'local',
        content: { technical_steps: ['Read the table component'], decisions: [], risks: [], unknowns: [] },
      },
      reviewablePatch: {
        evidenceKind: 'agent_reported', agentReportId: 'report-1',
        patchDigest: 'a'.repeat(64), baselineDigest: 'b'.repeat(64), declaredFilesDigest: 'c'.repeat(64),
        resultId: 'result-1', agentAttemptId: 'attempt-1', verificationAttemptId: 'va-1', artifactId: 'artifact-1', rawDownload: '/patch',
        files: [{ path: 'internal/domain/task.go', additions: 3, deletions: 1, lines: [{ kind: 'added', newLine: 1, text: 'change' }] }],
      },
    })
    renderWorkbench(run)

    expect(screen.getByRole('heading', { name: 'Intent' })).toBeInTheDocument()
    const intent = screen.getByRole('heading', { name: 'Intent' }).closest('section') as HTMLElement
    expect(within(intent).getByText('Add a sort button to the table.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Plan' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Progress' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Changed files' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Diff' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Checks' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Review & Apply' })).toBeInTheDocument()
    expect(screen.getAllByText('Reviewable patch').length).toBeGreaterThan(0)
    expect(screen.queryByText('Verified patch')).not.toBeInTheDocument()
  })

  test('renders the full Plan including decisions, risks, and unknowns', () => {
    renderWorkbench(makePlanRun())

    expect(screen.getByRole('heading', { name: 'Plan' })).toBeInTheDocument()
    expect(screen.getByText('Read the table component')).toBeInTheDocument()
    expect(screen.getByText('Add the sort handler')).toBeInTheDocument()
    expect(screen.getByText('Add a focused test')).toBeInTheDocument()

    expect(screen.getByText('Sort ascending by default')).toBeInTheDocument()
    expect(screen.getByText('Existing tests may assume stable order')).toBeInTheDocument()
    expect(screen.getByText('Whether the table supports server-side sorting')).toBeInTheDocument()

    expect(screen.getByRole('heading', { name: 'Decisions' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Risks' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Unknowns' })).toBeInTheDocument()
  })

  test('shows empty states for a run with no plan, patch, or checks', () => {
    renderWorkbench(makeRun())

    expect(screen.getByRole('heading', { name: 'Plan' })).toBeInTheDocument()
    expect(screen.getByText('No plan recorded yet.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Changed files' })).toBeInTheDocument()
    expect(screen.getByText('No changed files.')).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: 'Diff' })).toBeInTheDocument()
    expect(screen.getByText('No diff available yet.')).toBeInTheDocument()
    expect(screen.getByText('No checks recorded yet.')).toBeInTheDocument()
    expect(screen.getByText('No observable steps yet.')).toBeInTheDocument()
  })

  test('shows an actionable safe reason when the configured Pi model fails', () => {
    renderWorkbench(makeRun({
      status: 'recovery_required',
      terminalReason: 'agent_model_error',
      controls: { canCancel: false, canRetry: true, canReview: false, canAcceptAndApply: false },
    }))

    expect(screen.getByText('Agent failed · Needs attention')).toBeInTheDocument()
    expect(screen.getByRole('alert')).toHaveTextContent('Agent model request failed')
    expect(screen.getByRole('alert')).toHaveTextContent('select an available default model in Pi')
  })

  test('shows a No changed files empty state when the patch has no files', () => {
    const run = makeRun({
      reviewablePatch: {
        patchDigest: 'a'.repeat(64), baselineDigest: 'b'.repeat(64), declaredFilesDigest: 'c'.repeat(64),
        resultId: 'result-1', agentAttemptId: 'attempt-1', verificationAttemptId: 'va-1', artifactId: 'artifact-1', rawDownload: '/patch', files: [],
      },
    })
    renderWorkbench(run)

    expect(screen.getByRole('heading', { name: 'Changed files' })).toBeInTheDocument()
    expect(screen.getByText('No changed files.')).toBeInTheDocument()
  })

  test('keeps no-verifier disclosure visible after the run reaches review', () => {
    const run = makeRun({
      adapter: 'some-provider',
      verificationDisposition: { state: 'not_applicable', reason: 'This run uses Agent-reported evidence.' },
      agentReport: {
        id: 'report-1', attemptId: 'attempt-1', summary: 'Implemented the requested sort.', finalText: 'Done',
        completedAt: '2026-09-03T00:00:00Z', authority: 'non_authoritative_agent_claim', claimedChecks: [],
      },
      reviewablePatch: {
        evidenceKind: 'agent_reported', agentReportId: 'report-1',
        patchDigest: 'a'.repeat(64), baselineDigest: 'b'.repeat(64), declaredFilesDigest: 'c'.repeat(64),
        resultId: 'result-1', agentAttemptId: 'attempt-1', verificationAttemptId: '', artifactId: 'artifact-1', rawDownload: '/patch',
        files: [{ path: 'sort.ts', lines: [{ kind: 'added', newLine: 1, text: 'sort()' }] }],
      },
    })
    renderWorkbench(run)

    expect(screen.getByText('Independent verification was not run')).toBeInTheDocument()
    expect(screen.getByText('Agent-reported review · planned versus actual')).toBeInTheDocument()
    expect(screen.getByText('Implemented the requested sort.')).toBeInTheDocument()
    expect(screen.getAllByText('Reviewable patch').length).toBeGreaterThan(0)
    expect(screen.queryByText('Verified patch')).not.toBeInTheDocument()
  })

  test('lists changed files with per-file additions and deletions', () => {
    const run = makeRun({
      reviewablePatch: {
        patchDigest: 'a'.repeat(64), baselineDigest: 'b'.repeat(64), declaredFilesDigest: 'c'.repeat(64),
        resultId: 'result-1', agentAttemptId: 'attempt-1', verificationAttemptId: 'va-1', artifactId: 'artifact-1', rawDownload: '/patch',
        files: [
          { path: 'internal/domain/task.go', additions: 3, deletions: 0, lines: [{ kind: 'added', newLine: 1, text: 'change' }] },
          { path: 'internal/domain/task_test.go', additions: 6, deletions: 2, lines: [{ kind: 'added', newLine: 1, text: 'change' }] },
        ],
      },
    })
    renderWorkbench(run)

    const changedFiles = screen.getByRole('heading', { name: 'Changed files' }).closest('section') as HTMLElement
    expect(changedFiles).toHaveTextContent('internal/domain/task.go')
    expect(changedFiles).toHaveTextContent('internal/domain/task_test.go')
    expect(changedFiles).toHaveTextContent('+3')
    expect(changedFiles).toHaveTextContent('+6')
    expect(changedFiles).toHaveTextContent('−2')
  })
})

describe('RunStream checks provenance', () => {
  test('shows Agent-reported and Independently verified side by side when both exist', () => {
    const run = makeRun({
      criteria: [{ id: 'criterion-1', title: 'Sort works', status: 'passed', evidence: 'Independent Verifier evidence: ev-1' }],
      agentReport: {
        id: 'report-1', attemptId: 'attempt-1', summary: 'Done', finalText: 'Done', completedAt: '2026-09-03T00:00:00Z',
        authority: 'non_authoritative_agent_claim',
        claimedChecks: [{ criterionId: 'criterion-1', status: 'passed', evidence: 'The Agent observed the button sorting rows.' }],
      },
      verification: {
        id: 'verification-1', agentAttemptId: 'attempt-1', state: 'completed',
        bindings: { baselineDigest: 'a'.repeat(64), patchDigest: 'b'.repeat(64), contextSnapshotDigest: 'c'.repeat(64), acceptanceContractDigest: 'd'.repeat(64), verifierPolicyVersion: 'v1', verifierPolicyDigest: 'e'.repeat(64) },
        attempts: [verifiedAttempt()],
      },
    })
    renderWorkbench(run)

    expect(screen.getByText('Agent-reported')).toBeInTheDocument()
    expect(screen.getByText('Independently verified')).toBeInTheDocument()
    expect(screen.getByText('The Agent observed the button sorting rows.')).toBeInTheDocument()
    expect(screen.getByText('Trust: verified')).toBeInTheDocument()
    expect(screen.getByText('ev-1')).toBeInTheDocument()
  })

  test('never invents an independent-verification claim when only the Agent reported checks', () => {
    const run = makeRun({
      criteria: [{ id: 'criterion-1', title: 'Sort works', status: 'passed', evidence: 'Legacy Agent claim (non-authoritative): The Agent observed sorting.' }],
      agentReport: {
        id: 'report-1', attemptId: 'attempt-1', summary: 'Done', finalText: 'Done', completedAt: '2026-09-03T00:00:00Z',
        authority: 'non_authoritative_agent_claim',
        claimedChecks: [{ criterionId: 'criterion-1', status: 'passed', evidence: 'The Agent observed sorting.' }],
      },
    })
    renderWorkbench(run)

    expect(screen.getByText('Agent-reported')).toBeInTheDocument()
    expect(screen.getByText('The Agent observed sorting.')).toBeInTheDocument()
    expect(screen.queryByText('Independently verified')).not.toBeInTheDocument()
    expect(screen.queryByText(/Independent Verifier evidence/)).not.toBeInTheDocument()
    expect(screen.queryByText(/Trust:/)).not.toBeInTheDocument()
  })

  test('shows the verified check from verification attempts without an Agent claim', () => {
    const run = makeRun({
      criteria: [{ id: 'criterion-1', title: 'Sort works', status: 'passed', evidence: 'Independent Verifier evidence: ev-9' }],
      verification: {
        id: 'verification-1', agentAttemptId: 'attempt-1', state: 'completed',
        bindings: { baselineDigest: 'a'.repeat(64), patchDigest: 'b'.repeat(64), contextSnapshotDigest: 'c'.repeat(64), acceptanceContractDigest: 'd'.repeat(64), verifierPolicyVersion: 'v1', verifierPolicyDigest: 'e'.repeat(64) },
        attempts: [{
          id: 'va-1', sequence: 1, state: 'completed', evidenceComplete: true, cleanupProven: true,
          createdAt: '2026-09-03T00:00:00Z', updatedAt: '2026-09-03T00:00:01Z',
          checks: [{ id: 'check-1', criterionId: 'criterion-1', status: 'passed', trust: 'verified', evidenceIds: ['ev-9'] }],
          commands: [],
        }],
      },
    })
    renderWorkbench(run)

    expect(screen.getByText('Independently verified')).toBeInTheDocument()
    expect(screen.getByText('ev-9')).toBeInTheDocument()
    expect(screen.queryByText(/The Agent observed/)).not.toBeInTheDocument()
  })

  test('uses verified patch and independent-review wording only when verifier evidence exists', () => {
    const run = makeRun({
      adapter: 'pi',
      criteria: [{ id: 'criterion-1', title: 'Sort works', status: 'passed', evidence: 'Independent Verifier evidence: ev-1' }],
      verificationDisposition: { state: 'available', reason: 'Verifier adopted.' },
      verification: {
        id: 'verification-1', agentAttemptId: 'attempt-1', state: 'completed',
        bindings: { baselineDigest: 'a'.repeat(64), patchDigest: 'b'.repeat(64), contextSnapshotDigest: 'c'.repeat(64), acceptanceContractDigest: 'd'.repeat(64), verifierPolicyVersion: 'v1', verifierPolicyDigest: 'e'.repeat(64) },
        attempts: [verifiedAttempt()],
      },
      reviewablePatch: {
        evidenceKind: 'independent_verification',
        patchDigest: 'a'.repeat(64), baselineDigest: 'b'.repeat(64), declaredFilesDigest: 'c'.repeat(64),
        resultId: 'result-1', agentAttemptId: 'attempt-1', verificationAttemptId: 'va-1', artifactId: 'artifact-1', rawDownload: '/patch',
        files: [{ path: 'sort.ts', lines: [{ kind: 'added', newLine: 1, text: 'sort()' }] }],
      },
    })
    renderWorkbench(run)

    expect(screen.getAllByText('Verified patch').length).toBeGreaterThan(0)
    expect(screen.queryByText('Reviewable patch')).not.toBeInTheDocument()
    expect(screen.getByText('Review · planned versus actual')).toBeInTheDocument()
    expect(screen.getByText('The Agent returned a Patch for independent review.')).toBeInTheDocument()
  })
})

describe('RunStream review actions', () => {
  test('mounts task branch delivery after acceptance instead of showing deferred Apply copy', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => ({ ok: true, status: 200, json: async () => ({ capabilities: { hosting: false }, repositories: [{ repoId: 'repo-a', name: 'frontend', targetBranch: 'main', taskBranch: 'chora/task/repo-a', status: 'committed', operations: [] }] }) } as Response)))
    const resourceResult = {
      digest: 'result-digest',
      repositories: [{ repoId: 'repo-a', name: 'frontend', baseRef: 'refs/heads/main', deliveryMode: 'task_branch' as const, taskBranch: 'chora/task/repo-a' }],
      group: { repositories: [] },
      patches: [],
    } as unknown as RunView['resourceResult']
    renderWorkbench(makeRun({ status: 'accepted', resourceResult }))
    expect(await screen.findByRole('heading', { name: 'Task branch delivery' })).toBeInTheDocument()
    const delivery = screen.getByRole('region', { name: 'Task branch delivery' })
    const repository = within(delivery).getByRole('article', { name: 'frontend' })
    expect(await within(repository).findByText('Committed locally')).toBeInTheDocument()
    expect(screen.queryByText(/planned for a later stage/i)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Apply accepted Patch/i })).not.toBeInTheDocument()
  })

  test('keeps accept, reject, and change-requirement wiring intact', async () => {
    const onReview = vi.fn()
    const onChangeRequirement = vi.fn()
    renderWorkbench(makeRun(), { onReview, onChangeRequirement })

    await userEvent.click(screen.getByRole('button', { name: 'Accept' }))
    expect(onReview).toHaveBeenCalledWith('accept', '')

    await userEvent.click(screen.getByRole('button', { name: 'Ask Agent to fix' }))
    expect(onReview).toHaveBeenCalledWith('reject', '', 'implementation_gap')

    await userEvent.click(screen.getByRole('button', { name: 'Change requirement' }))
    expect(onChangeRequirement).toHaveBeenCalledOnce()
  })

  test('keeps retry wiring intact', async () => {
    const onRetry = vi.fn()
    const run = makeRun({
      status: 'revision_required',
      controls: { canCancel: false, canRetry: true, canReview: false, canSwitchAgentExecutionProfile: false },
    })
    renderWorkbench(run, { onRetry })

    await userEvent.click(screen.getByRole('button', { name: 'Retry Pi' }))
    expect(onRetry).toHaveBeenCalledWith('Resolve the rejected acceptance gap and return an updated result.')
  })

  test('renders pending Apply, conflict recovery, and loading controls', async () => {
    const onApply = vi.fn()
    const pending = makeRun({
      status: 'accepted',
      verifiedReview: {
        id: 'review-1', resultId: 'result-1', kind: 'accept', reason: 'Accepted', actorId: 'user', sessionId: 'session',
        patchDigest: 'a'.repeat(64), decidedAt: '2026-09-03T00:00:00Z',
      },
      controls: { canCancel: false, canRetry: false, canReview: false, canApplyPatch: true },
    })
    const view = renderWorkbench(pending, { onApply })

    expect(screen.getAllByText('Accepted · Not applied').length).toBeGreaterThan(0)
    expect(screen.getByText(/code has not been written/)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('button', { name: 'Apply accepted Patch' }))
    expect(onApply).toHaveBeenCalledOnce()

    const conflict = makeRun({
      status: 'accepted',
      patchApplication: {
        state: 'conflict', version: 2, patchDigest: 'a'.repeat(64), targetIdentity: 'target', baseRevision: 'base',
        affectedPaths: ['sort.ts'], preStateDigest: 'b'.repeat(64), reason: 'Target changed after review.',
        startedAt: '2026-09-03T00:00:00Z', updatedAt: '2026-09-03T00:00:01Z',
      },
      controls: { canCancel: false, canRetry: false, canReview: false, canApplyPatch: true },
    })
    view.rerender(<RunStream
      run={conflict} busy
      onCancel={vi.fn()} onReview={vi.fn()} onApply={onApply} onRetry={vi.fn()}
      onSwitchProfile={vi.fn(async () => true)} onAcknowledgeTrustedLocal={vi.fn(async () => true)}
      onResolveDecision={vi.fn()} onRetryVerification={vi.fn()} onChangeRequirement={vi.fn()}
    />)

    expect(screen.getAllByText('Apply conflict').length).toBeGreaterThan(0)
    expect(screen.getByText('Target changed after review.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Applying…' })).toBeDisabled()
  })

  test('renders zh-CN labels for the new Workbench sections', () => {
    window.localStorage.setItem('chora.locale', 'zh-CN')
    render(
      <LanguageProvider>
        <RunStream
          run={makePlanRun()}
          busy={false}
          onCancel={vi.fn()}
          onReview={vi.fn()}
          onApply={vi.fn()}
          onRetry={vi.fn()}
          onSwitchProfile={vi.fn(async () => true)}
          onAcknowledgeTrustedLocal={vi.fn(async () => true)}
          onResolveDecision={vi.fn()}
          onRetryVerification={vi.fn()}
          onChangeRequirement={vi.fn()}
        />
      </LanguageProvider>,
    )

    expect(screen.getByRole('heading', { name: '意图' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '方案' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '进度' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '检查' })).toBeInTheDocument()
    expect(screen.getByRole('heading', { name: '评审与应用' })).toBeInTheDocument()
    window.localStorage.removeItem('chora.locale')
  })
})

 test.each(['runtime_output_limit_exceeded', 'runtime_stream_invalid'])('explains stopped execution for %s', (terminalReason) => {
  renderWorkbench(makeRun({ status: 'recovery_required', terminalReason, verificationDisposition: { state: 'not_applicable', reason: 'No independent Verifier ran.' } }))
  expect(screen.getByRole('alert')).toHaveTextContent('Agent execution stopped')
  expect(screen.getByRole('alert')).toHaveTextContent('task worktree')
  expect(screen.queryByText(/Implementation finished/)).not.toBeInTheDocument()
 })


test('separates failed Pi checks from the requirement and human patch review', async () => {
  const evidence = 'Agent-reported, non-authoritative: verify-1 reported a tool error'
  renderWorkbench(makeRun({
    agentExecution: { profile: 'trusted_local', runtimeSource: 'local_pi', executionProvider: 'trusted_host', capabilityPolicy: 'pi.native', trustDisclosurePolicy: 'chora.trusted-local-disclosure.v1', sandboxed: false, disclosureLabel: 'Trusted Local · No Sandbox' },
    reviewablePatch: { evidenceKind: 'agent_reported', patchDigest: 'a'.repeat(64), baselineDigest: 'b'.repeat(64), declaredFilesDigest: 'c'.repeat(64), resultId: 'result-1', agentAttemptId: 'attempt-1', verificationAttemptId: '', artifactId: 'artifact-1', rawDownload: '/patch', files: [{ path: 'README.md', lines: [] }] },
    criteria: [{ id: 'criterion-1', title: 'Requested behavior is implemented', status: 'FAIL', evidence }],
    verificationDisposition: { state: 'not_applicable', reason: 'Local Connected uses Agent-reported checks; no independent Verifier ran.' },
    agentReport: { id: 'report-1', attemptId: 'attempt-1', summary: 'Agent completed the Local Connected turn.', finalText: 'README changed; make test failed.', completedAt: '2026-09-07T00:00:00Z', authority: 'non_authoritative_agent_claim', claimedChecks: [{ criterionId: 'criterion-1', status: 'FAIL', evidence }] },
  }))
  const checks = screen.getByRole('table', { name: 'Checks' })
  expect(within(checks).getByText('Requested change')).toBeInTheDocument()
  expect(within(checks).getByText('Check failed')).toBeInTheDocument()
  expect(within(checks).queryByText('Requested behavior is implemented')).not.toBeInTheDocument()
  expect(screen.getByText('Checks did not pass.')).toBeInTheDocument()
  expect(screen.queryByText('Human patch review')).not.toBeInTheDocument()
  expect(screen.getByText('Review the changes and check results, then decide whether to write them to your repository.')).toBeInTheDocument()
  const details = within(checks).getByText('View recorded evidence').closest('details')!
  expect(details.open).toBe(false)
  await userEvent.click(within(checks).getByText('View recorded evidence'))
  expect(details.open).toBe(true)
  expect(within(details).getByText(evidence)).toBeInTheDocument()
})

test.each([
  [[], 'No complete passing check result is recorded.'],
  [['UNKNOWN'], 'No complete passing check result is recorded.'],
  [['PASS', 'UNKNOWN'], 'No complete passing check result is recorded.'],
  [['PASS'], 'Recorded checks passed.'],
  [['PASS', 'FAIL'], 'Checks did not pass.'],
])('keeps check outcome separate from a successful repository write (%j)', (statuses, expected) => {
  renderWorkbench(makeRun({
    status: 'accepted',
    agentExecution: { profile: 'trusted_local', runtimeSource: 'local_pi', executionProvider: 'trusted_host', capabilityPolicy: 'pi.native', trustDisclosurePolicy: 'chora.trusted-local-disclosure.v1', sandboxed: false, disclosureLabel: 'Trusted Local · No Sandbox' },
    agentReport: { id: 'report', attemptId: 'attempt', summary: '', finalText: '', completedAt: '', authority: 'non_authoritative_agent_claim', claimedChecks: (statuses as string[]).map((status, i) => ({ criterionId: `c${i}`, status, evidence: '' })) },
    patchApplication: { state: 'applied', version: 1, patchDigest: 'a', targetIdentity: 'target', baseRevision: 'base', affectedPaths: ['README.md'], preStateDigest: 'b', startedAt: '', updatedAt: '' },
  }))
  const result = screen.getByRole('region', { name: 'Changes and next steps' })
  expect(within(result).getByText(expected as string)).toBeInTheDocument()
  expect(within(result).getByText('Written to repository')).toBeInTheDocument()
  expect(within(result).getByText(/Later Git commits are not tracked here/)).toBeInTheDocument()
  expect(within(result).queryByText(/then decide whether to write/)).not.toBeInTheDocument()
  expect(within(result).queryByRole('button', { name: 'Write changes to repository' })).not.toBeInTheDocument()
})


test.each(['pending', 'retrying'] as const)('shows %s progress without asking for manual retry', (state) => {
  renderWorkbench(makeRun({
    status: state === 'pending' ? 'recovery_required' : 'running',
    terminalReason: 'runtime_output_limit_exceeded',
    automaticRetry: { state, retriesUsed: state === 'pending' ? 0 : 1, maxRetries: 2, lastFailureReason: 'runtime_output_limit_exceeded' },
    controls: { canCancel: true, canRetry: true, canReview: false },
  }))
  expect(screen.getByRole('status')).toHaveTextContent('1/2')
  expect(screen.getByRole('status')).toHaveTextContent('Last failure: Agent output limit exceeded')
  expect(screen.getByRole('status')).toHaveTextContent('No manual retry is needed')
  expect(screen.queryByRole('button', { name: 'Retry Pi' })).not.toBeInTheDocument()
  expect(screen.queryByText('Task needs attention')).not.toBeInTheDocument()
})

test('asks for human action after three failed executions and preserves manual retry', async () => {
  const onRetry = vi.fn()
  renderWorkbench(makeRun({
    status: 'recovery_required', terminalReason: 'runtime_exit_nonzero',
    automaticRetry: { state: 'exhausted', retriesUsed: 2, maxRetries: 2, lastFailureReason: 'runtime_exit_nonzero' },
    controls: { canCancel: false, canRetry: true, canReview: false },
  }), { onRetry })
  expect(screen.getByRole('alert')).toHaveTextContent('Failed 3 times · Needs attention')
  expect(screen.getByRole('alert')).toHaveTextContent('Agent exited with an error')
  await userEvent.click(screen.getByRole('button', { name: 'Retry Pi' }))
  expect(onRetry).toHaveBeenCalledOnce()
})

test('renders automatic retry details in Chinese', () => {
  window.localStorage.setItem('chora.locale', 'zh-CN')
  render(<LanguageProvider><RunStream run={makeRun({
    status: 'running', automaticRetry: { state: 'retrying', retriesUsed: 2, maxRetries: 2, lastFailureReason: 'agent_model_error' },
  })} busy={false} onCancel={vi.fn()} onReview={vi.fn()} onApply={vi.fn()} onRetry={vi.fn()}
    onSwitchProfile={vi.fn(async () => true)} onAcknowledgeTrustedLocal={vi.fn(async () => true)}
    onResolveDecision={vi.fn()} onRetryVerification={vi.fn()} onChangeRequirement={vi.fn()}/></LanguageProvider>)
  expect(screen.getByRole('status')).toHaveTextContent('正在自动重试 2/2')
  expect(screen.getByRole('status')).toHaveTextContent('上次失败原因：Agent 模型请求失败')
  window.localStorage.removeItem('chora.locale')
})


test('lets the user cancel while an automatic retry is pending', async () => {
  const onCancel = vi.fn()
  renderWorkbench(makeRun({ status: 'recovery_required',
    automaticRetry: { state: 'pending', retriesUsed: 0, maxRetries: 2, lastFailureReason: 'runtime_exit_nonzero' },
    controls: { canCancel: true, canRetry: false, canReview: false },
  }), { onCancel })
  await userEvent.click(screen.getByRole('button', { name: 'Cancel' }))
  expect(onCancel).toHaveBeenCalledOnce()
})

test('explains the current startup blocker separately from the prior Agent failure', () => {
  const blocker = 'Automatic retry is blocked because the recorded Pi execution route is unavailable. Restore Pi and the Task worktree, then recover explicitly.'
  renderWorkbench(makeRun({ status: 'ready',
    automaticRetry: { state: 'blocked', retriesUsed: 1, maxRetries: 2, lastFailureReason: 'runtime_output_limit_exceeded' },
    blockers: [blocker], controls: { canCancel: false, canRetry: true, canReview: false },
  }))
  expect(screen.getByRole('alert')).toHaveTextContent('Agent output limit exceeded')
  expect(screen.getByRole('alert')).toHaveTextContent(blocker)
  expect(screen.getByRole('button', { name: 'Continue prepared retry' })).toBeInTheDocument()
})

test('shows the current launch failure instead of the automatic retry trigger', () => {
  renderWorkbench(makeRun({ status: 'recovery_required', terminalReason: 'runtime_start_failed',
    automaticRetry: { state: 'blocked', retriesUsed: 1, maxRetries: 2, lastFailureReason: 'runtime_output_limit_exceeded' },
    controls: { canCancel: false, canRetry: true, canReview: false },
  }))
  expect(screen.getByRole('alert')).toHaveTextContent('Last failure: Agent could not start')
  expect(screen.getByRole('alert')).not.toHaveTextContent('Agent output limit exceeded')
})
