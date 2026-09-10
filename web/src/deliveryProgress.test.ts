import { describe, expect, test } from 'vitest'
import { deliveryDisplayStatus } from './deliveryProgress'
import { taskDisplayStatus } from './taskStatus'
import type { DeliveryStatus, DeliverySummary } from './taskDeliveryTypes'
import type { TaskSummary } from './types'

function summary(...statuses: DeliveryStatus[]): DeliverySummary {
  return { repositories: statuses.map((status, index) => ({ repoId: `${index}`, name: `Repo ${index}`, status })) }
}

describe('delivery progress across repositories', () => {
  test.each([
    ['uncommitted', 'Ready to commit'], ['committed', 'Committed locally'], ['pushed', 'Task branch pushed'],
    ['pr_open', 'Pull request open'], ['merged', 'Merged'], ['cleaned', 'Task worktree cleaned up'],
  ] as const)('replaces the accepted task label with %s', (status, label) => {
    const task = { status: 'open', latestRun: { status: 'accepted' }, currentAction: { kind: 'view_terminal' }, delivery: summary(status) } as TaskSummary
    expect(taskDisplayStatus(task).label).toBe(label)
  })
  test('partial cleanup never implies full delivery and still points to commit', () => {
    expect(deliveryDisplayStatus(summary('cleaned', 'uncommitted'))).toMatchObject({ label: 'Cleaned up {count}/{total}', values: { count: 1, total: 2 }, action: 'Commit task branch', tone: 'attention' })
  })
  test('unchanged and reference repositories do not hold delivery open', () => {
    expect(deliveryDisplayStatus(summary('cleaned', 'no_change')).label).toBe('Task worktree cleaned up')
    expect(deliveryDisplayStatus(summary('no_change')).label).toBe('No changes to deliver')
  })
  test('closed PR and interrupted operations need attention even beside cleaned repositories', () => {
    expect(deliveryDisplayStatus(summary('cleaned', 'pr_closed')).label).toBe('PR closed · Not merged')
    expect(deliveryDisplayStatus(summary('cleaned', 'recovery_required')).label).toBe('Delivery needs attention')
  })
  test('new execution is never masked by earlier delivery', () => {
    const task = { status: 'open', latestRun: { status: 'running' }, currentAction: { kind: 'monitor_run' }, delivery: summary('cleaned') } as TaskSummary
    expect(taskDisplayStatus(task).label).toBe('Working')
  })
  test('unavailable or empty delivery evidence is not completion', () => {
    expect(deliveryDisplayStatus({ ...summary('cleaned'), unavailable: true }).tone).toBe('attention')
    expect(deliveryDisplayStatus(summary()).label).toBe('Delivery status unavailable')
  })
})
