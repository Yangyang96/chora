import { render, screen } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import { describe, expect, test } from 'vitest'
import type { RunView, TrajectoryRecord } from '../types'
import { LanguageProvider } from '../i18n'
import { TrajectoryLedger } from './TrajectoryLedger'

function record(sequence: number, title: string): TrajectoryRecord {
  return { sequence, source: 'Chora', kind: 'system', title, summary: `${title} summary`, status: 'completed', startedAt: '2026-09-07T00:00:00Z', details: [] }
}

function run(attempt: number, trajectory: TrajectoryRecord[]): RunView {
  return {
    id: 'run-1', status: 'running', version: attempt, attempt, adapter: 'pi',
    room: { id: 'room-1', name: 'Room', description: '' },
    task: { id: 'task-1', title: 'Task', goal: 'Goal' }, context: [], timeline: [], artifacts: [], unknowns: [], criteria: [], trajectory,
  }
}

describe('TrajectoryLedger attempt selection', () => {
  test('selects the latest record when a retry changes the attempt', async () => {
    const initial = [record(1, 'Old start'), record(2, 'Old error')]
    const view = render(<TrajectoryLedger run={run(1, initial)} />)
    await userEvent.click(screen.getByRole('listitem', { name: 'Old start: Old start summary' }))
    expect(screen.getByLabelText('Selected trajectory step details')).toHaveTextContent('Old start')

    view.rerender(<TrajectoryLedger run={run(2, [...initial, record(3, 'Retry started')])} />)
    expect(screen.getByLabelText('Selected trajectory step details')).toHaveTextContent('Retry started')
  })

  test('translates a stable stopped-execution record without changing task content', () => {
    window.localStorage.setItem('chora.locale', 'zh-CN')
    const stopped = record(1, 'Agent start failed')
    stopped.summary = 'Execution stopped safely: runtime error.'
    render(<LanguageProvider><TrajectoryLedger run={{ ...run(1, [stopped]), task: { id: 'task-1', title: 'Task', goal: 'Keep this exact user goal' } }} /></LanguageProvider>)
    expect(screen.getAllByText('Agent 启动失败').length).toBeGreaterThan(0)
    expect(screen.getAllByText('执行已安全停止：运行时错误。').length).toBeGreaterThan(0)
    expect(screen.getByText('Keep this exact user goal')).toBeInTheDocument()
  })
})


test('collapses long replies without deleting text or disguising check failure', async () => {
  window.localStorage.setItem('chora.locale', 'en')
  const original = 'README modified. Tests failed. ' + 'Original explanation. '.repeat(50)
  const testRun: RunView = { ...run(1, []), agentReport: { id: 'report-1', attemptId: 'attempt-1', summary: 'Done', finalText: original, completedAt: '2026-09-07T00:00:00Z', authority: 'non_authoritative_agent_claim', claimedChecks: [{ criterionId: 'criterion-1', status: 'FAIL', evidence: 'Tool error' }] } }
  const view = render(<LanguageProvider><TrajectoryLedger run={testRun} /></LanguageProvider>)
  expect(screen.getByText('No Apply result recorded.')).toBeInTheDocument()
  expect(screen.getByText('Chora result summary')).toBeInTheDocument()
  expect(screen.getByText('Reported checks: 1 failed, 0 unknown, 0 passed.')).toBeInTheDocument()
  let details = screen.getByText('View full Agent reply').closest('details')!
  expect(details.open).toBe(false)
  await userEvent.click(screen.getByText('View full Agent reply'))
  expect(details.open).toBe(true)
  expect(details.querySelector('p')?.textContent).toBe(original)
  view.rerender(<LanguageProvider><TrajectoryLedger run={{ ...testRun, id: 'run-2' }} /></LanguageProvider>)
  details = screen.getByText('View full Agent reply').closest('details')!
  expect(details.open).toBe(false)
})

test('summarizes grouped changes, final-content checks, and Apply without scalar fallback', () => {
  window.localStorage.setItem('chora.locale', 'en')
  const result = {
    group: { repositories: [
      { repoId: 'a', changedPaths: ['src/shared.js', 'test/new.js'], checks: { Status: 'PASS', FinalContentVerified: true } },
      { repoId: 'b', changedPaths: ['src/shared.js'], checks: { Status: 'PASS', FinalContentVerified: true } },
    ] },
  } as RunView['resourceResult']
  const fixture: RunView = { ...run(1, []), resourceResult: result, resourceApply: { operationId: 'apply-1', status: 'applied', repositories: [] }, agentReport: { id: 'report-1', attemptId: 'attempt-1', summary: 'Done', finalText: 'Explanation. '.repeat(60), completedAt: '', authority: 'non_authoritative_agent_claim', claimedChecks: [] } }
  const view = render(<LanguageProvider><TrajectoryLedger run={fixture} /></LanguageProvider>)
  expect(screen.getByText('Reviewable changes: 3 files.')).toBeInTheDocument()
  expect(screen.getByText('Repository checks: 0 failed, 0 unverified, 2 passed for final contents.')).toBeInTheDocument()
  expect(screen.getByText('All repository changes have been applied.')).toBeInTheDocument()
  expect(screen.queryByText('No check results recorded.')).not.toBeInTheDocument()
  expect(screen.queryByText('No reviewable patch recorded.')).not.toBeInTheDocument()
  expect(screen.queryByText('No Apply result recorded.')).not.toBeInTheDocument()
  const mixed = { ...result!, group: { ...result!.group, repositories: [
    { ...result!.group.repositories[0], checks: { ...result!.group.repositories[0].checks, Status: 'FAIL', FinalContentVerified: false } },
    { ...result!.group.repositories[1], checks: { ...result!.group.repositories[1].checks, Status: 'UNKNOWN', FinalContentVerified: false } },
  ] } }
  view.rerender(<LanguageProvider><TrajectoryLedger run={{ ...fixture, resourceResult: mixed, resourceApply: { ...fixture.resourceApply!, status: 'partial' } }} /></LanguageProvider>)
  expect(screen.getByText('Repository checks: 1 failed, 1 unverified, 0 passed for final contents.')).toBeInTheDocument()
  expect(screen.getByText('Some repositories were applied; recovery is still needed.')).toBeInTheDocument()
})

test('summarizes accepted task-branch work without a stale Apply claim', () => {
  const grouped = {
    repositories: [{ repoId: 'a', name: 'API', baseRef: 'refs/heads/main', deliveryMode: 'task_branch' as const, taskBranch: 'chora/task/a' }],
    group: { repositories: [{ repoId: 'a', changedPaths: ['src/a.ts'], checks: { Status: 'PASS', FinalContentVerified: true } }] },
  } as RunView['resourceResult']
  const fixture: RunView = { ...run(1, []), status: 'accepted', resourceResult: grouped, agentReport: { id: 'report-1', attemptId: 'attempt-1', summary: 'Done', finalText: 'Explanation. '.repeat(60), completedAt: '', authority: 'non_authoritative_agent_claim', claimedChecks: [] } }
  const rendered = render(<LanguageProvider><TrajectoryLedger run={fixture} /></LanguageProvider>)
  expect(screen.getByText('Task branch delivery is tracked per repository below.')).toBeInTheDocument()
  expect(screen.queryByText('No Apply result recorded.')).not.toBeInTheDocument()
  rendered.rerender(<LanguageProvider><TrajectoryLedger run={{ ...fixture, status: 'awaiting_review' }} /></LanguageProvider>)
  expect(screen.queryByText('Task branch delivery is tracked per repository below.')).not.toBeInTheDocument()
  expect(screen.getByText('Changes are ready for review in the Task worktree.')).toBeInTheDocument()
})
