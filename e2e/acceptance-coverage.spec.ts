import { expect, test } from '@playwright/test'

// Command evidence is intentionally passing: the UI must not promote it into
// an assertion that every original acceptance requirement has been verified.
test('keeps acceptance gaps visible beside passing selected checks', async ({ page }) => {
  const room = { id: 'room-coverage', name: 'Coverage room', description: '', state: 'active', ownershipKind: 'legacy_standalone', version: 1, taskCounts: { total: 1, open: 1, terminal: 0 } }
  const run = {
    id: 'run-coverage', status: 'awaiting_review', version: 2, attempt: 1, adapter: 'pi', room,
    task: { id: 'task-coverage', title: 'Browser filtering', goal: 'Implement filtering and browser navigation.' },
    context: [], timeline: [], artifacts: [], unknowns: [],
    criteria: [{ id: 'history', title: 'Browser Back restores the filter', status: 'passed', evidence: 'Legacy Agent claim' }],
    controls: { canCancel: false, canRetry: false, canReview: true },
    resourceResult: {
      digest: 'a'.repeat(64), patches: [],
      group: { schemaVersion: 'chora.result-group.v2', agentReportId: 'report-coverage', resourceSnapshotDigest: 'b'.repeat(64),
        repositories: [{ repoId: 'repo-app', baseCommit: 'c'.repeat(40), baseTree: 'd'.repeat(40), patchDigest: 'e'.repeat(64), changedPaths: [],
          checks: { RepositoryID: 'repo-app', Mode: 'auto', Status: 'PASS', FinalContentVerified: true,
            Checks: [{ CheckID: 'unit', Name: 'Unit tests', Argv: ['node', '--test'], WorkingDirectory: '.', Status: 'PASS', FinalContentVerified: true, ProviderSucceeded: true, ExitCode: 0 }] },
        }],
      },
    },
  }
  let mutations = 0
  await page.route('**/api/**', async route => {
    const path = new URL(route.request().url()).pathname
    if (route.request().method() !== 'GET') mutations++
    const body = path.endsWith('/runs/run-coverage') ? run
      : path === '/api/rooms/room-coverage' ? room
      : path.endsWith('/workspace') ? { room, tasks: [] }
      : path === '/api/rooms' ? { activeRooms: [room], archivedRooms: [] }
      : path.endsWith('/continuity') ? { available: false }
      : undefined
    await route.fulfill({ status: body ? 200 : 404, json: body ?? { error: 'fixture unavailable' } })
  })
  await page.goto('/rooms/room-coverage/tasks/task-coverage/runs/run-coverage')
  await expect(page.getByText('Checks passed for final repository contents', { exact: true })).toBeVisible()
  const coverage = page.getByRole('region', { name: 'Acceptance coverage', exact: true })
  await expect(coverage).toContainText('Browser Back restores the filter')
  await expect(coverage).toContainText('Coverage requires review')
  await expect(coverage).toContainText('Missing evidence remains unverified')
  await page.setViewportSize({ width: 390, height: 844 })
  await expect(coverage).toBeVisible()
  await page.getByRole('button', { name: '中文', exact: true }).click()
  await expect(page.getByRole('region', { name: '验收覆盖情况', exact: true })).toContainText('进入待评审不代表验收通过')
  expect(mutations).toBe(0)
})
