import { describe, expect, test } from 'vitest'
import type { RunStatus, TaskSummary } from './types'
import { taskDisplayStatus } from './taskStatus'

function task(runStatus?: RunStatus, patchApplicationState?: string): TaskSummary {
  return {
    id: 'task-1', title: 'Ship Kusion task', status: 'open', archived: false,
    lastActivityAt: '2026-09-07T00:00:00Z', currentPlanRevision: null, runCount: runStatus ? 1 : 0,
    currentAction: { kind: 'none', target: {}, url: '', reason: '' },
    latestRun: runStatus ? {
      id: 'run-1', taskId: 'task-1', status: runStatus, patchApplicationState, version: 1, attempt: 1,
      createdAt: '', updatedAt: '', startedAt: '', reviewRequestedAt: '', terminalAt: '',
    } : null,
  }
}

describe('taskDisplayStatus', () => {
  test.each([
    ['running', undefined, 'Working', 'working'],
    ['stopping', undefined, 'Stopping', 'working'],
    ['awaiting_review', undefined, 'Waiting for review', 'attention'],
    ['recovery_required', undefined, 'Recovery needed', 'attention'],
    ['accepted', undefined, 'Accepted', 'success'],
    ['accepted', 'pending', 'Accepted', 'success'],
    ['accepted', 'applied', 'Applied', 'success'],
    ['accepted', 'applying', 'Applying', 'working'],
    ['accepted', 'conflict', 'Recovery needed', 'attention'],
    ['cancelled', undefined, 'Cancelled', 'muted'],
  ] satisfies [RunStatus, string | undefined, string, string][])('derives %s with application %s', (status, application, label, tone) => {
    expect(taskDisplayStatus(task(status, application))).toMatchObject({ label, tone })
  })

  test('does not infer that an accepted task was applied', () => {
    expect(taskDisplayStatus(task('accepted')).label).toBe('Accepted')
  })
})


test.each([
  ['pending', 'recovery_required', 0, 'Waiting to retry {current}/{max}', 'working'],
  ['retrying', 'running', 1, 'Automatically retrying {current}/{max}', 'working'],
  ['retrying', 'ready', 2, 'Automatically retrying {current}/{max}', 'working'],
  ['exhausted', 'recovery_required', 2, 'Failed {count} times · Needs attention', 'attention'],
  ['blocked', 'recovery_required', 1, 'Automatic retry blocked · Needs attention', 'attention'],
] as const)('shows automatic retry %s in task cards', (state, status, retriesUsed, label, tone) => {
  const item = task(status)
  item.latestRun!.automaticRetry = { state, retriesUsed, maxRetries: 2, lastFailureReason: 'runtime_output_limit_exceeded' }
  expect(taskDisplayStatus(item)).toMatchObject({ label, tone, detail: 'Agent output limit exceeded' })
})

test('shows a concrete Agent failure for older attempts without retry metadata', () => {
  const item = task('recovery_required')
  item.latestRun!.terminalReason = 'agent_model_error'
  expect(taskDisplayStatus(item)).toMatchObject({ label: 'Agent failed · Needs attention', detail: 'Agent model request failed', tone: 'attention' })
})

test.each(['cancelled', 'awaiting_review', 'completed'] as const)('does not obscure %s with stale retry metadata', (status) => {
  const item = task(status)
  item.latestRun!.automaticRetry = { state: 'retrying', retriesUsed: 1, maxRetries: 2, lastFailureReason: 'runtime_exit_nonzero' }
  expect(taskDisplayStatus(item).label).not.toContain('retry')
})

test('shows current startup failure when an automatic retry could not launch', () => {
  const item = task('recovery_required')
  item.latestRun!.terminalReason = 'runtime_start_failed'
  item.latestRun!.automaticRetry = { state: 'blocked', retriesUsed: 1, maxRetries: 2, lastFailureReason: 'runtime_output_limit_exceeded' }
  expect(taskDisplayStatus(item).detail).toBe('Agent could not start')
})
