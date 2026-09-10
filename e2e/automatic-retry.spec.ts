import { expect, test } from '@playwright/test'

// Browser contract test: deterministic API responses exercise polling and recovery UX.
// Backend tests independently prove the persisted retry budget and process lifecycle.
test('updates automatic retries without a manual request, then exposes the exhausted failure', async ({ page }) => {
  const runPath = '/api/rooms/room-retry/tasks/task-retry/runs/run-retry'
  const room = { id: 'room-retry', name: 'Retry room', description: '', state: 'active', ownershipKind: 'legacy_standalone', version: 1, archivedAt: '', lastActivityAt: '', taskCounts: { total: 1, open: 1, terminal: 0 }, humanActionRequired: false }
  const retry = { state: 'pending', retriesUsed: 0, maxRetries: 2, lastFailureReason: 'runtime_output_limit_exceeded' }
  const run = {
    id: 'run-retry', status: 'recovery_required', version: 1, attempt: 1, adapter: 'pi', room,
    task: { id: 'task-retry', title: 'Automatic retry task', goal: 'Continue after runtime failures' },
    automaticRetry: retry, terminalReason: 'runtime_output_limit_exceeded',
    context: [], timeline: [], artifacts: [], unknowns: [], criteria: [],
    controls: { canCancel: false, canRetry: true, canReview: false },
  }
  let mutations = 0
  await page.route('**/api/**', async (route) => {
    const path = new URL(route.request().url()).pathname
    if (route.request().method() !== 'GET') mutations++
    const body = path === runPath ? run
      : path === '/api/rooms/room-retry' ? room
      : path === '/api/rooms/room-retry/workspace' ? { room, tasks: [{
          id: 'task-retry', title: run.task.title, status: 'open', archived: false, lastActivityAt: '',
          currentPlanRevision: null, runCount: 1, latestRun: { ...run, taskId: 'task-retry' },
          currentAction: { kind: 'recover_run', target: {}, url: '', reason: '' },
        }] }
      : path === '/api/rooms' ? { activeRooms: [room], archivedRooms: [] }
      : path.endsWith('/continuity') ? { available: false }
      : path === '/api/pi/discovery' ? { state: 'unavailable' }
      : undefined
    await route.fulfill({ status: body ? 200 : 404, json: body ?? { error: 'fixture unavailable' } })
  })
  await page.goto('/rooms/room-retry/tasks/task-retry/runs/run-retry')
  await expect(page.getByRole('status')).toContainText('Waiting to retry 1/2')
  await expect(page.getByRole('button', { name: 'Retry Pi', exact: true })).toHaveCount(0)
  retry.state = 'retrying'
  retry.retriesUsed = 1
  run.status = 'running'
  await expect(page.getByRole('status')).toContainText('Automatically retrying 1/2')
  retry.retriesUsed = 2
  await expect(page.getByRole('status')).toContainText('Automatically retrying 2/2')
  retry.state = 'exhausted'
  run.status = 'recovery_required'
  await expect(page.getByRole('alert')).toContainText('Failed 3 times · Needs attention')
  await expect(page.getByRole('alert')).toContainText('Agent output limit exceeded')
  await expect(page.getByRole('button', { name: 'Retry Pi', exact: true })).toBeVisible()
  expect(mutations).toBe(0)

  // The Room card also updates while this page stays open.
  retry.state = 'retrying'
  run.status = 'running'
  await page.goto('/rooms/room-retry')
  await expect(page.getByText('Automatically retrying 2/2', { exact: true })).toBeVisible()
  await page.getByRole('button', { name: 'Retry room', exact: true }).click()
  await expect(page.getByText('Automatically retrying 2/2', { exact: true })).toHaveCount(2)
  retry.state = 'exhausted'
  run.status = 'recovery_required'
  await expect(page.getByText('Failed 3 times · Needs attention', { exact: true })).toHaveCount(2)
  await expect(page.getByText('Agent output limit exceeded', { exact: true })).toBeVisible()
  expect(mutations).toBe(0)
})
